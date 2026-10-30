import { memo, useCallback, useEffect, useLayoutEffect, useMemo, useReducer, useRef, useState } from 'react';
import { Link, useParams, useSearchParams } from 'react-router-dom';
import { api, errorText, kindFromSegment, openStream, wsUrl, workloadPath } from '../api';
import type { LogOptions, ServerControl } from '../api';
import { EnvBadge } from '../components/Badges';
import { ErrorBanner } from '../components/Feedback';
import { Icon } from '../components/Icon';
import { Crumbs } from '../components/PageHeader';
import { useAuth } from '../context/auth';
import { useSetShellCtx } from '../context/shell';
import { useApi } from '../hooks/useApi';
import { useNow } from '../hooks/useNow';
import { describeEnd } from '../lib/endReason';
import type { LogLine } from '../lib/logs';
import { LineSplitter, SINCE_OPTIONS, TAIL_OPTIONS, parseLogLine, shortTs, splitHighlight } from '../lib/logs';
import { workloadUrl } from '../lib/routes';
import { explainDecision } from '../lib/decision';
import { fmtCountdown, msUntil } from '../lib/time';

const MAX_LINES = 20000;
const MOCK = import.meta.env.VITE_MOCK === '1';

const Row = memo(function Row({ line, q, wrap, cur, i }: { line: LogLine; q: string; wrap: boolean; cur: boolean; i: number }) {
  const parts = splitHighlight(line.msg, q);
  return (
    <div className={`${wrap ? 'wr' : 'nw'}${cur ? ' cur' : ''}`} data-row={i}>
      {line.ts && <span className="ts" title={line.ts}>{shortTs(line.ts)}</span>}
      <span className={`lv ${line.level}`}>{line.levelText}</span>
      <span style={{ flex: 1, minWidth: 0 }}>{parts.map((p, k) => (p.hit ? <mark className="hit" key={k}>{p.text}</mark> : p.text))}</span>
    </div>
  );
});

type Status = 'connecting' | 'live' | 'ended' | 'error';

export function LogsPage() {
  const { cluster = '', ns = '', kind: kindSeg = '', name = '' } = useParams();
  const kind = kindFromSegment(kindSeg);
  const [sp, setSp] = useSearchParams();
  const { me } = useAuth();
  const now = useNow(1000);
  const ref = useMemo(() => ({ cluster, namespace: ns, kind, name }), [cluster, ns, kind, name]);
  const wl = useApi(() => api.workload(ref), [ref, me.breakGlass?.id]);
  useSetShellCtx(wl.data ? cluster : undefined, wl.data?.environment);
  const podsQ = useApi(() => api.pods(ref), [ref], { poll: 15000 });
  const pods = podsQ.data ?? [];
  const pod = sp.get('pod') || pods[0]?.name || '';
  const podObj = pods.find((p) => p.name === pod);
  const containers = podObj ? podObj.containers.filter((c) => !c.init) : [];
  const container = sp.get('container') && containers.some((c) => c.name === sp.get('container')) ? (sp.get('container') as string) : containers[0]?.name ?? '';

  const [previous, setPrevious] = useState(false);
  const [follow, setFollow] = useState(true);
  const [tail, setTail] = useState<number>(1000);
  const [since, setSince] = useState<number>(0);
  const [timestamps, setTimestamps] = useState(true);
  const [wrap, setWrap] = useState(true);
  const [q, setQ] = useState('');
  const [onlyMatches, setOnlyMatches] = useState(false);
  const [curMatch, setCurMatch] = useState(0);
  const [attempt, setAttempt] = useState(0);

  const [status, setStatus] = useState<Status>('connecting');
  const [msg, setMsg] = useState('');
  const [warning, setWarning] = useState<{ message: string; closesAt: string } | null>(null);
  const [closesAt, setClosesAt] = useState<string | null>(null);
  const [truncated, setTruncated] = useState(false);
  const [paused, setPaused] = useState(false);
  const [newCount, setNewCount] = useState(0);
  const [limit, setLimit] = useState(3000);
  const [ver, bump] = useReducer((n: number) => n + 1, 0);
  const lines = useRef<LogLine[]>([]);
  const scroller = useRef<HTMLDivElement>(null);
  const stick = useRef(true);
  const raf = useRef(0);
  const pausedRef = useRef(false);
  pausedRef.current = paused;

  const schedule = useCallback(() => {
    if (raf.current) return;
    raf.current = window.requestAnimationFrame(() => { raf.current = 0; bump(); });
  }, []);

  // stream lifecycle
  useEffect(() => {
    if (!pod || !container) return;
    lines.current = [];
    setStatus('connecting'); setMsg(''); setWarning(null); setTruncated(false); setNewCount(0); setPaused(false); stick.current = true;
    bump();
    const dec = new TextDecoder();
    const split = new LineSplitter();
    const params = { container, previous: previous || undefined, follow: follow && !previous ? true : false, tailLines: tail, sinceSeconds: since || undefined, timestamps: timestamps || undefined };
    const url = wsUrl(`${workloadPath(ref)}/pods/${encodeURIComponent(pod)}/logs/stream`, params);
    let ended = false;
    const push = (raws: string[]) => {
      if (!raws.length) return;
      const parsed = raws.map((r) => parseLogLine(r, timestamps));
      const arr = lines.current;
      arr.push(...parsed);
      if (arr.length > MAX_LINES) arr.splice(0, arr.length - MAX_LINES);
      if (pausedRef.current) setNewCount((n) => n + parsed.length);
      schedule();
    };
    const h = openStream(url, {
      onBytes: (b) => push(split.push(dec.decode(b, { stream: true }))),
      onControl: (m: ServerControl) => {
        if (m.type === 'ready') { setStatus('live'); setClosesAt(m.closesAt); }
        else if (m.type === 'warning') setWarning({ message: m.message, closesAt: m.closesAt });
        else if (m.type === 'closes_at') { setClosesAt(m.closesAt); if (!m.closesAt || (msUntil(m.closesAt, Date.now()) ?? 0) > 5 * 60_000) setWarning(null); }
        else if (m.type === 'truncated') setTruncated(true);
        else if (m.type === 'end') {
          ended = true;
          push(split.flush());
          const d = describeEnd(m.reason, m.message);
          setStatus('ended');
          setMsg(m.reason === 'process_exited' && !m.message ? 'End of logs.' : `${d.title}. ${m.message || d.detail}`);
        }
      },
      onClose: (info) => {
        push(split.flush());
        if (ended) return;
        if (!info.opened) {
          setStatus('error');
          void api.workload(ref).then((w) => {
            const e = explainDecision(w.access.logs, { action: 'logs', target: name, canBreakGlass: me.role === 'user' });
            setMsg(e ? e.text : 'Could not open the log stream.');
          }).catch((e) => setMsg(errorText(e)));
        } else {
          setStatus('ended');
          setMsg('The log stream closed.');
        }
      },
    });
    return () => { h.close(); if (raf.current) { cancelAnimationFrame(raf.current); raf.current = 0; } };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [ref, pod, container, previous, follow, tail, since, timestamps, attempt]);

  const all = lines.current;
  const qq = q.trim();
  const display = useMemo(() => {
    if (!qq || !onlyMatches) return all.slice();
    const l = qq.toLowerCase();
    return all.filter((x) => x.raw.toLowerCase().includes(l));
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [ver, qq, onlyMatches]);
  const matches = useMemo(() => {
    if (!qq) return [] as number[];
    const l = qq.toLowerCase();
    const out: number[] = [];
    display.forEach((x, i) => { if (x.raw.toLowerCase().includes(l)) out.push(i); });
    return out;
  }, [display, qq]);
  const curIdx = matches.length ? matches[Math.min(curMatch, matches.length - 1)] : -1;
  const start = Math.max(0, display.length - limit);

  useEffect(() => { setCurMatch(0); }, [qq]);
  useLayoutEffect(() => {
    const el = scroller.current;
    if (el && stick.current) el.scrollTop = el.scrollHeight;
  });
  useEffect(() => {
    if (curIdx < 0 || !scroller.current) return;
    stick.current = false; setPaused(true);
    if (curIdx < start) setLimit(display.length - curIdx + 50);
    window.requestAnimationFrame(() => scroller.current?.querySelector(`[data-row="${curIdx}"]`)?.scrollIntoView({ block: 'center' }));
  }, [curIdx, curMatch, display.length, start]);

  const onScroll = () => {
    const el = scroller.current;
    if (!el) return;
    const atBottom = el.scrollHeight - el.scrollTop - el.clientHeight < 40;
    stick.current = atBottom;
    setPaused(!atBottom);
    if (atBottom) setNewCount(0);
  };
  const jump = () => {
    stick.current = true; setPaused(false); setNewCount(0);
    const el = scroller.current;
    if (el) el.scrollTop = el.scrollHeight;
  };
  const setParam = (k: string, v: string) => { const n = new URLSearchParams(sp); n.set(k, v); if (k === 'pod') n.delete('container'); setSp(n, { replace: true }); };

  const opts: LogOptions = { container, previous, tailLines: tail, sinceSeconds: since || undefined, timestamps };
  const dlUrl = pod ? api.logDownloadUrl(ref, pod, opts) : '#';
  const onDownload = async (e: React.MouseEvent) => {
    if (!MOCK) return; // real server: plain attachment link
    e.preventDefault();
    const res = await fetch(dlUrl);
    const blob = await res.blob();
    const a = document.createElement('a');
    a.href = URL.createObjectURL(blob);
    a.download = `${pod}-${container}.log`;
    a.click();
    setTimeout(() => URL.revokeObjectURL(a.href), 1000);
  };

  const env = wl.data?.environment;
  const left = closesAt ? msUntil(closesAt, now) : null;
  const decisionErr = podsQ.error;

  return (
    <div style={{ flex: 1, minHeight: 0, display: 'flex', flexDirection: 'column' }}>
      <div style={{ padding: '12px 20px 10px', display: 'flex', flexDirection: 'column', gap: 10, flex: 'none' }}>
        <div className="row wrap" style={{ gap: 12 }}>
          <Crumbs items={[
            { label: cluster, to: '/' }, { label: ns, mono: true },
            { label: name, to: workloadUrl(ref), mono: true }, { label: 'Logs' },
          ]} />
          {env && <EnvBadge env={env} />}
          {left !== null && left > 0 && left < 3600_000 && <span className="chip warn"><Icon name="clock" size={12} />Closes in {fmtCountdown(left)}</span>}
          <span className="grow" />
          <a className="btn" href={dlUrl} download={`${pod}-${container}.log`} onClick={onDownload} aria-disabled={!pod}><Icon name="down" />Download .log</a>
        </div>

        <div className="row wrap" style={{ gap: 10 }}>
          <label className="row" style={{ gap: 6 }}><span className="muted">Pod</span>
            <select className="sel mono" aria-label="Pod" style={{ minWidth: 230, maxWidth: '100%' }} value={pod} onChange={(e) => setParam('pod', e.target.value)}>
              {pods.length === 0 && <option value="">{podsQ.loading ? 'Loading…' : 'No pods'}</option>}
              {pods.map((p) => <option key={p.name} value={p.name}>{p.name}</option>)}
            </select>
          </label>
          <label className="row" style={{ gap: 6 }}><span className="muted">Container</span>
            <select className="sel mono" aria-label="Container" value={container} onChange={(e) => setParam('container', e.target.value)}>
              {containers.map((c) => <option key={c.name}>{c.name}</option>)}
            </select>
          </label>
          <label className="chk"><input type="checkbox" checked={previous} onChange={(e) => setPrevious(e.target.checked)} /> Previous container</label>
          <span style={{ width: 1, height: 20, background: 'var(--ln)' }} className="hide-sm" />
          <label className="chk"><input className="sw" type="checkbox" checked={follow && !previous} disabled={previous} onChange={(e) => setFollow(e.target.checked)} /> Live tail</label>
          <label className="row" style={{ gap: 6 }}><span className="muted">Lines</span>
            <select className="sel" aria-label="Lines to load" value={tail} onChange={(e) => setTail(Number(e.target.value))}>
              {TAIL_OPTIONS.map((n) => <option key={n} value={n}>{n.toLocaleString('en-US')}</option>)}
            </select>
          </label>
          <label className="row" style={{ gap: 6 }}><span className="muted">Since</span>
            <select className="sel" aria-label="Since" value={since} onChange={(e) => setSince(Number(e.target.value))}>
              <option value={0}>Any time</option>
              {SINCE_OPTIONS.map((o) => <option key={o.seconds} value={o.seconds}>{o.label}</option>)}
            </select>
          </label>
          <label className="chk"><input type="checkbox" checked={wrap} onChange={(e) => setWrap(e.target.checked)} /> Wrap lines</label>
          <label className="chk"><input type="checkbox" checked={timestamps} onChange={(e) => setTimestamps(e.target.checked)} /> Timestamps</label>
          <span className="grow" />
          <label className="search" style={{ width: 300, height: 28, background: 'var(--sf)', color: 'var(--tx)' }}>
            <Icon name="search" />
            <input value={q} onChange={(e) => setQ(e.target.value)} aria-label="Search logs" placeholder="Search logs"
              onKeyDown={(e) => { if (e.key === 'Enter' && matches.length) setCurMatch((c) => (e.shiftKey ? (c - 1 + matches.length) % matches.length : (c + 1) % matches.length)); }} />
            {qq && <span className="muted" style={{ fontSize: 12, whiteSpace: 'nowrap' }} aria-live="polite">{matches.length ? `${Math.min(curMatch, matches.length - 1) + 1} of ${matches.length}` : 'No matches'}</span>}
          </label>
          <button type="button" className="btn sm" aria-label="Previous match" disabled={!matches.length} onClick={() => setCurMatch((c) => (c - 1 + matches.length) % matches.length)}><Icon name="chevd" style={{ transform: 'rotate(180deg)' }} /></button>
          <button type="button" className="btn sm" aria-label="Next match" disabled={!matches.length} onClick={() => setCurMatch((c) => (c + 1) % matches.length)}><Icon name="chevd" /></button>
          <label className="chk"><input type="checkbox" checked={onlyMatches} onChange={(e) => setOnlyMatches(e.target.checked)} /> Only matching</label>
        </div>
        {warning && (
          <div className="banner yel" role="alert"><Icon name="warn" /><span><b>This log stream will close soon.</b> {warning.message}</span></div>
        )}
      </div>

      {decisionErr ? (
        <div style={{ padding: '0 20px' }}><ErrorBanner error={decisionErr} onRetry={podsQ.reload} /></div>
      ) : (
        <div className="logwrap">
          <div className="log logscroll" ref={scroller} onScroll={onScroll} role="log" aria-label="Log output" aria-live="off" tabIndex={0}>
            {start > 0 && (
              <div className="sep"><button type="button" className="btn tdark sm" onClick={() => setLimit((l) => l + 3000)}>Show {Math.min(3000, start).toLocaleString('en-US')} earlier lines</button></div>
            )}
            {display.length === 0 && status !== 'connecting' && <div className="sep">{status === 'error' ? '' : 'No log lines.'}</div>}
            {status === 'connecting' && display.length === 0 && <div className="sep">Connecting…</div>}
            {display.slice(start).map((l, k) => <Row key={l.id} line={l} q={qq} wrap={wrap} cur={start + k === curIdx} i={start + k} />)}
            {truncated && <div className="sep">Older lines were dropped by the server.</div>}
            {status === 'ended' && <div className="sep">— {msg} —</div>}
          </div>
          {paused && (status === 'live' || newCount > 0) && (
            <div style={{ position: 'absolute', right: 18, bottom: 14 }}>
              <button type="button" className="btn pri" onClick={jump}><Icon name="down" />Jump to latest{newCount ? ` · ${newCount} new` : ''}</button>
            </div>
          )}
        </div>
      )}

      <div className="row wrap" style={{ padding: '0 20px 12px', fontSize: 12, color: 'var(--tx3)', gap: 14, flex: 'none' }} aria-live="polite">
        <span className="row" style={{ gap: 6 }}>
          <span className={`dot ${status === 'live' ? 'ok' : status === 'error' ? 'err' : status === 'connecting' ? 'warn' : ''}`} />
          {status === 'live' ? (paused ? 'Auto-scroll paused — you scrolled up' : 'Live') : status === 'connecting' ? 'Connecting…' : status === 'ended' ? 'Stream ended' : msg || 'Could not connect'}
        </span>
        <span>{all.length.toLocaleString('en-US')} lines loaded</span>
        {(status === 'ended' || status === 'error') && <button type="button" className="linklike blue" onClick={() => setAttempt((a) => a + 1)}>Reconnect</button>}
        {status === 'error' && <Link to={workloadUrl(ref)}>Back to workload</Link>}
        <span className="grow" />
        <span className="mono">{pod} / {container}</span>
        <span>Viewing logs is recorded in the audit log</span>
      </div>
    </div>
  );
}
