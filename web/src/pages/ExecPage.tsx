import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { Link, useNavigate, useParams, useSearchParams } from 'react-router-dom';
import { api, errorText, kindFromSegment, openStream, wsUrl, workloadPath } from '../api';
import type { DecisionInfo, ServerControl, WorkloadDetail } from '../api';
import type { StreamHandle } from '../api/ws';
import { EnvBadge, isProd } from '../components/Badges';
import { Dialog } from '../components/Dialog';
import { ErrorBanner, Loading } from '../components/Feedback';
import { Icon } from '../components/Icon';
import { Seg } from '../components/Seg';
import { XTerm } from '../components/XTerm';
import type { XTermHandle } from '../components/XTerm';
import { useAuth } from '../context/auth';
import { useBreakGlass } from '../context/breakglass';
import { useSetShellCtx } from '../context/shell';
import { useApi } from '../hooks/useApi';
import { useNow } from '../hooks/useNow';
import { explainDecision } from '../lib/decision';
import { describeEnd } from '../lib/endReason';
import type { EndInfo } from '../lib/endReason';
import { workloadUrl } from '../lib/routes';
import { fmtCountdown, fmtTime, msUntil } from '../lib/time';
import { breakGlassOn, breakGlassLabel } from '../lib/breakGlass';

type Phase = 'connect' | 'session' | 'ended';

export function ExecPage() {
  const { cluster = '', ns = '', kind: kindSeg = '', name = '' } = useParams();
  const kind = kindFromSegment(kindSeg);
  const [sp, setSp] = useSearchParams();
  const nav = useNavigate();
  const { me, usesGrants } = useAuth();
  const bg = useBreakGlass();
  const ref = useMemo(() => ({ cluster, namespace: ns, kind, name }), [cluster, ns, kind, name]);
  // Debug: the shell runs in a new debug container that shares the chosen container's processes.
  const debug = sp.get('mode') === 'debug' && me.settings.debugEnabled;
  const wl = useApi(() => api.workload(ref), [ref, me.breakGlass?.id]);
  const podsQ = useApi(() => api.pods(ref), [ref], { enabled: !!wl.data?.access.logs.allowed });
  useSetShellCtx(wl.data ? cluster : undefined, wl.data?.environment);

  const pods = podsQ.data ?? [];
  const pod = sp.get('pod') || pods[0]?.name || '';
  const podObj = pods.find((p) => p.name === pod);
  const containers = podObj ? podObj.containers.filter((c) => !c.init) : [];
  const [container, setContainer] = useState('');
  const [shell, setShell] = useState<'/bin/sh' | '/bin/bash'>('/bin/sh');
  const [phase, setPhase] = useState<Phase>('connect');
  const [connecting, setConnecting] = useState(false);
  const [connectErr, setConnectErr] = useState<string | null>(null);
  const [attempt, setAttempt] = useState(0);

  const effContainer = container && containers.some((c) => c.name === container) ? container : sp.get('container') || containers[0]?.name || '';

  // session state
  const [startedAt, setStartedAt] = useState<number>(Date.now());
  const [closesAt, setClosesAt] = useState<string | null>(null);
  const [warning, setWarning] = useState<{ message: string; closesAt: string } | null>(null);
  const [sessionId, setSessionId] = useState('');
  const [end, setEnd] = useState<EndInfo | null>(null);
  const [endedAt, setEndedAt] = useState<number>(0);
  const [live, setLive] = useState(false);
  const [bgId, setBgId] = useState<number | null>(null);
  const term = useRef<XTermHandle>(null);
  const handle = useRef<StreamHandle | null>(null);
  const opened = useRef(false);
  const everReady = useRef(false);
  const now = useNow(1000);

  const closeSocket = useCallback(() => { handle.current?.close(); handle.current = null; }, []);
  useEffect(() => closeSocket, [closeSocket]);

  // keep-alive
  useEffect(() => {
    if (!live) return;
    const t = window.setInterval(() => handle.current?.sendControl({ type: 'ping' }), 25000);
    return () => window.clearInterval(t);
  }, [live]);

  const connect = () => {
    setConnectErr(null);
    setConnecting(true);
    opened.current = false;
    everReady.current = false;
    setEnd(null);
    setWarning(null);
    setAttempt((a) => a + 1);
    setPhase('session');
  };

  const open = useCallback((size: { cols: number; rows: number }) => {
    if (opened.current) return;
    opened.current = true;
    const url = wsUrl(`${workloadPath(ref)}/pods/${encodeURIComponent(pod)}/${debug ? 'debug' : 'exec'}`, { container: effContainer, shell, cols: size.cols, rows: size.rows });
    let ended = false;
    const backToConnect = (text: string) => { closeSocket(); setLive(false); setConnecting(false); setConnectErr(text); setPhase('connect'); };
    handle.current = openStream(url, {
      onBytes: (b) => term.current?.write(b),
      onControl: (m: ServerControl) => {
        if (m.type === 'ready') {
          everReady.current = true;
          setLive(true); setConnecting(false);
          setStartedAt(Date.parse(m.startedAt)); setClosesAt(m.closesAt); setSessionId(m.sessionId ?? ''); setBgId(m.breakGlassId);
          term.current?.focus();
        } else if (m.type === 'warning') setWarning({ message: m.message, closesAt: m.closesAt });
        else if (m.type === 'closes_at') { setClosesAt(m.closesAt); if (!m.closesAt || (msUntil(m.closesAt, Date.now()) ?? 0) > 5 * 60_000) setWarning(null); }
        else if (m.type === 'end') {
          ended = true;
          if (!everReady.current) { backToConnect(m.message || describeEnd(m.reason).detail); void refreshDecision(); return; }
          setLive(false); setEnd(describeEnd(m.reason, m.message, { maxMinutes: me.settings.execMaxSessionMinutes, idleMinutes: me.settings.execIdleTimeoutMinutes })); setEndedAt(Date.now()); setPhase('ended');
        }
      },
      onClose: (info) => {
        if (ended) return;
        if (!info.opened) { backToConnect('Could not open the shell.'); void refreshDecision(); return; }
        setLive(false);
        if (everReady.current) { setEnd(describeEnd('cluster_error', 'The connection to the portal was lost.')); setEndedAt(Date.now()); setPhase('ended'); }
        else backToConnect('The connection closed before the shell started.');
      },
    });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [ref, pod, effContainer, shell, debug, closeSocket, me.settings.execIdleTimeoutMinutes, me.settings.execMaxSessionMinutes]);

  // WebSocket handshake errors do not expose the HTTP body, so fall back to the workload's DecisionInfo.
  async function refreshDecision() {
    try {
      const w = await api.workload(ref);
      const e = explainDecision(w.access.exec, { action: 'exec', target: name, canBreakGlass: usesGrants && breakGlassOn(me) });
      if (e) setConnectErr(e.text);
      wl.reload();
    } catch (e) { setConnectErr(errorText(e)); }
  }

  const disconnect = () => { closeSocket(); setLive(false); setEnd(describeEnd('client_closed')); setEndedAt(Date.now()); setPhase('ended'); };
  const data = wl.data;

  if (wl.loading && !data) return <div style={{ padding: 24 }}><Loading what="workload" /></div>;
  if (wl.error && !data) return <div style={{ padding: 24 }}><ErrorBanner error={wl.error} onRetry={wl.reload} /></div>;
  if (!data) return null;

  const prod = isProd(data.environment);
  const execInfo = data.access.exec;
  const denial = explainDecision(execInfo, { action: 'exec', target: name, canBreakGlass: usesGrants && breakGlassOn(me) });

  if (phase === 'connect') {
    return (
      <div className="execbg">
        <ConnectDialog
          wl={data} info={execInfo} pod={pod} pods={pods.map((p) => p.name)} onPod={(p) => { const n = new URLSearchParams(sp); n.set('pod', p); setSp(n, { replace: true }); }}
          debugEnabled={me.settings.debugEnabled} debug={debug}
          onDebug={(d) => { const n = new URLSearchParams(sp); if (d) n.set('mode', 'debug'); else n.delete('mode'); setSp(n, { replace: true }); }}
          containers={containers.map((c) => c.name)} container={effContainer} onContainer={setContainer} shell={shell} onShell={setShell}
          prod={prod} denial={denial?.text ?? null} cta={denial?.cta ?? 'none'} error={connectErr} podsError={podsQ.error}
          onCancel={() => nav(workloadUrl(ref))} onConnect={connect} busy={connecting}
          onBreakGlass={() => bg.open({ action: 'exec', target: name, environment: data.environment })}
          breakGlassLabel={breakGlassLabel(me)}
        />
      </div>
    );
  }

  const left = closesAt ? msUntil(closesAt, now) : null;
  const warnLeft = warning ? msUntil(warning.closesAt, now) : null;
  const ended = phase === 'ended';
  const elapsed = (ended ? endedAt : now) - startedAt;

  return (
    <div style={{ flex: 1, minHeight: 0, display: 'flex', flexDirection: 'column', position: 'relative' }}>
      <div className="termbar" role="toolbar" aria-label="Session">
        <span className="mono">{pod}</span>
        <span className="mono" style={{ color: '#8C97A8' }}>/ {effContainer}{debug && ' · debug container'}</span>
        <EnvBadge env={data.environment} />
        {bgId !== null && <span className="bgm">Break-glass</span>}
        {ended ? (
          <span className="row" style={{ gap: 6, color: '#8C97A8' }}><span className="dot" style={{ background: '#7F8CA0' }} />Ended</span>
        ) : (
          <span className="rec" aria-label="Recording">REC</span>
        )}
        <span className="grow" />
        <span className="row" style={{ gap: 6 }}><Icon name="clock" style={{ color: '#8C97A8' }} /><span style={{ color: '#8C97A8' }}>{ended ? 'Total' : 'Elapsed'}</span><span className="mono">{fmtCountdown(elapsed)}</span></span>
        {!ended && left !== null && (
          <span className="row" style={{ gap: 6 }}><span style={{ color: '#8C97A8' }}>Time left</span><span className="mono" style={{ color: left < 5 * 60_000 ? '#F5C26B' : undefined }}>{fmtCountdown(left)}</span></span>
        )}
        {!ended && <button type="button" className="btn tdark sm" onClick={disconnect}><Icon name="x" />Disconnect</button>}
      </div>
      {!ended && warning && (
        <div className="termwarn" role="alert">
          <Icon name="warn" />
          <span><b>This session will close in {fmtCountdown(Math.max(0, warnLeft ?? 0))}.</b> {warning.message} Save your work; you can reconnect afterwards.</span>
          <span className="grow" />
          <span className="mono">{fmtCountdown(Math.max(0, warnLeft ?? 0))}</span>
        </div>
      )}
      <div style={{ flex: 1, minHeight: 0, position: 'relative', display: 'flex', flexDirection: 'column', background: '#0A0D12' }}>
        <div style={{ flex: 1, minHeight: 0, display: 'flex', flexDirection: 'column', opacity: ended ? 0.38 : 1 }} aria-hidden={ended}>
          <XTerm
            key={attempt} ref={term} ariaLabel={`Shell in ${pod}`}
            onReady={open} onData={(d) => handle.current?.sendBytes(d)} onResize={(cols, rows) => handle.current?.sendControl({ type: 'resize', cols, rows })}
          />
        </div>
        {connecting && !live && !ended && <div className="termover">{debug ? `Starting a debug container in ${pod}…` : `Connecting to ${pod}…`}</div>}
        {ended && end && (
          <div className="termover solid">
            <div className="dlg" style={{ width: 'min(520px, 100%)' }} role="alertdialog" aria-labelledby="end-title">
              <div className="hd" style={{ paddingTop: 20 }}>
                <div className="row"><span className={`chip ${end.tone === 'neutral' ? '' : end.tone}`}>Session closed</span><span className="grow" /><EnvBadge env={data.environment} /></div>
                <h2 id="end-title" style={{ marginTop: 10, fontSize: 18 }}>{end.title}</h2>
                <div className="sub">{end.detail}</div>
              </div>
              <div className="bd">
                <dl className="kv" style={{ gridTemplateColumns: '110px 1fr' }}>
                  <dt>Pod</dt><dd className="mono">{pod}</dd>
                  <dt>Started</dt><dd>{fmtTime(startedAt, undefined, true)} → {fmtTime(endedAt, undefined, true)}</dd>
                  <dt>Recording</dt><dd><span className="chip ok"><Icon name="check" size={12} />Saved</span> <span className="mono muted">{sessionId}</span></dd>
                </dl>
              </div>
              <div className="ft">
                <Link className="btn" to={workloadUrl(ref)}>Back to pods</Link>
                {end.suggestBreakGlass && usesGrants && breakGlassOn(me) && !me.breakGlass && (
                  <button type="button" className="btn dng" onClick={() => bg.open({ action: 'exec', target: name, environment: data.environment })}>{breakGlassLabel(me)}</button>
                )}
                {end.canReconnect && <button type="button" className="btn act" onClick={() => { setPhase('connect'); setConnecting(false); }}><Icon name="restart" />Reconnect</button>}
              </div>
            </div>
          </div>
        )}
      </div>
    </div>
  );
}

function ConnectDialog(p: {
  wl: WorkloadDetail; info: DecisionInfo; pod: string; pods: string[]; onPod: (n: string) => void; containers: string[]; container: string; onContainer: (c: string) => void;
  debugEnabled: boolean; debug: boolean; onDebug: (d: boolean) => void;
  shell: '/bin/sh' | '/bin/bash'; onShell: (s: '/bin/sh' | '/bin/bash') => void; prod: boolean; denial: string | null; cta: 'request' | 'breakglass' | 'none';
  error: string | null; podsError: unknown; onCancel: () => void; onConnect: () => void; busy: boolean; onBreakGlass: () => void; breakGlassLabel: string;
}) {
  const blocked = !!p.denial;
  const canConnect = !blocked && !!p.pod && !!p.container && !p.busy;
  return (
    <Dialog
      onClose={p.onCancel} width={440} tone={p.info.breakGlassId ? 'bg' : p.prod ? 'prod' : undefined} title={p.debug ? 'Open a debug shell' : 'Open a shell'} closeOnBackdrop={false}
      subtitle={<span className="row"><span className="mono" style={{ color: 'var(--tx)', overflowWrap: 'anywhere' }}>{p.pod || p.wl.name}</span><EnvBadge env={p.wl.environment} /></span>}
      footer={
        <>
          <button type="button" className="btn" onClick={p.onCancel}>Cancel</button>
          <button type="button" className={`btn act${canConnect ? '' : ' dis'}`} aria-disabled={!canConnect} onClick={() => canConnect && p.onConnect()} data-autofocus>
            <Icon name="term" />Connect
          </button>
        </>
      }
    >
      {p.prod && <div className="banner prod"><Icon name="warn" /><b>You are opening a shell in production.</b></div>}
      {p.info.breakGlassId && <div className="banner red"><Icon name="warn" /><span><b>Break-glass is active.</b> Time windows are overridden until it ends.</span></div>}
      {blocked && (
        <div className="banner red" role="alert" style={{ flexDirection: 'column', alignItems: 'flex-start', gap: 6 }}>
          <span>{p.denial}</span>
          {p.cta === 'request' && <Link to="/access?request=1">Request access</Link>}
          {p.cta === 'breakglass' && <button type="button" className="linklike blue" onClick={p.onBreakGlass}>{p.breakGlassLabel}</button>}
        </div>
      )}
      {p.error && !blocked && <div className="banner red" role="alert">{p.error}</div>}
      {p.podsError != null && <ErrorBanner error={p.podsError} />}
      <dl className="kv" style={{ gridTemplateColumns: '90px 1fr' }}>
        <dt>Cluster</dt><dd className="mono">{p.wl.cluster}</dd><dt>Namespace</dt><dd className="mono">{p.wl.namespace}</dd>
      </dl>
      {p.pods.length > 1 && (
        <label className="f">Pod<select className="sel mono" style={{ height: 30 }} value={p.pod} onChange={(e) => p.onPod(e.target.value)}>{p.pods.map((n) => <option key={n}>{n}</option>)}</select></label>
      )}
      {p.debugEnabled && (
        <div className="f">Run in<Seg label="Run in" style={{ alignSelf: 'flex-start' }} value={p.debug ? 'debug' : 'exec'} onChange={(v) => p.onDebug(v === 'debug')} options={[{ value: 'exec', label: 'The container' }, { value: 'debug', label: 'A debug container' }]} /></div>
      )}
      <label className="f">{p.debug ? 'Target container' : 'Container'}<select className="sel" style={{ height: 30 }} value={p.container} onChange={(e) => p.onContainer(e.target.value)}>{p.containers.map((c) => <option key={c}>{c}</option>)}</select></label>
      <div className="f">Shell<Seg label="Shell" style={{ alignSelf: 'flex-start' }} value={p.shell} onChange={p.onShell} options={[{ value: '/bin/sh', label: '/bin/sh', mono: true }, { value: '/bin/bash', label: '/bin/bash', mono: true }]} /></div>
      {p.debug && (
        <div className="banner info">
          <span>A debug container with network tools is added to the pod. It sees the target container's processes and the pod's network, and stays in the pod until the pod is replaced.</span>
        </div>
      )}
      <div className="banner info" style={{ alignItems: 'flex-start' }}>
        <span className="dot rec" style={{ marginTop: 5, background: '#E5352B' }} />
        <span>This session will be recorded and can be reviewed by the platform and security teams.</span>
      </div>
    </Dialog>
  );
}

