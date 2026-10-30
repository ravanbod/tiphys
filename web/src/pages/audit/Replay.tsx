import { useCallback, useDeferredValue, useEffect, useMemo, useRef, useState } from 'react';
import { Link, useParams } from 'react-router-dom';
import { ApiError, api, errorText } from '../../api';
import { Avatar, BreakGlassMark, EnvBadge } from '../../components/Badges';
import { CastPlayer } from '../../components/CastPlayer';
import type { CastPlayerHandle } from '../../components/CastPlayer';
import { ErrorBanner, Skeleton } from '../../components/Feedback';
import { Icon } from '../../components/Icon';
import { Crumbs } from '../../components/PageHeader';
import { useApi } from '../../hooks/useApi';
import { useNow } from '../../hooks/useNow';
import { deriveCommands, findOutputMatches, parseAsciicast } from '../../lib/asciicast';
import type { ParsedCast } from '../../lib/asciicast';
import { indexAtOrBefore } from '../../lib/castTime';
import { endChip } from '../../lib/endReason';
import { fmtDuration, fmtWhen } from '../../lib/time';
import { fetchOnce } from '../../lib/onceFetch';
import '../../styles/auditpages.css';

const SPEEDS = [1, 2, 4, 8];
const IDLE_LIMIT = 2;

type Rec =
  | { status: 'loading' }
  | { status: 'ok'; text: string; parsed: ParsedCast }
  | { status: 'error'; error: unknown };

function mmss(sec: number): string {
  return fmtDuration(Math.max(0, Math.floor(sec)));
}

function recordingErrorText(e: unknown): { title: string; detail: string } {
  if (e instanceof ApiError && e.status === 503) {
    return { title: 'Recording storage is unavailable', detail: 'The recording store (MinIO) could not be reached. Try again in a moment.' };
  }
  if (e instanceof ApiError && e.status === 404) {
    return {
      title: 'No recording found for this session',
      detail: 'It may have been removed by the retention policy, or the session never produced a recording.',
    };
  }
  return { title: 'The recording could not be loaded', detail: errorText(e) };
}

export function Replay() {
  const { id = '' } = useParams<{ id: string }>();
  const session = useApi(() => api.session(id), [id]);
  const s = session.data;
  const bgId = s?.breakGlassId ?? null;
  const bgs = useApi(() => api.breakGlasses(), [bgId], { enabled: bgId !== null });
  const bgReason = bgId !== null ? bgs.data?.find((b) => b.id === bgId)?.reason : undefined;

  // The recording is fetched once per page view; reading it is itself audited.
  const [attempt, setAttempt] = useState(0);
  const [rec, setRec] = useState<Rec>({ status: 'loading' });
  useEffect(() => {
    let alive = true;
    setRec({ status: 'loading' });
    fetchOnce(`recording:${id}`, () => api.recording(id), { force: attempt > 0 })
      .then((text) => {
        if (alive) setRec({ status: 'ok', text, parsed: parseAsciicast(text) });
      })
      .catch((e: unknown) => {
        if (alive) setRec({ status: 'error', error: e });
      });
    return () => {
      alive = false;
    };
  }, [id, attempt]);

  const parsed = rec.status === 'ok' ? rec.parsed : null;
  const eventTimes = useMemo(() => (parsed ? parsed.events.map((e) => e.t) : []), [parsed]);

  // Commands: the server's list, else derived from typed input in the recording.
  const commands = useMemo(() => {
    const fromApi = (s?.commands ?? []).map((c) => ({ offsetSeconds: c.offsetSeconds, text: c.text }));
    const list = fromApi.length ? fromApi : parsed ? deriveCommands(parsed.events) : [];
    return [...list].sort((a, b) => a.offsetSeconds - b.offsetSeconds);
  }, [s?.commands, parsed]);
  const offsets = useMemo(() => commands.map((c) => c.offsetSeconds), [commands]);

  const playerRef = useRef<CastPlayerHandle>(null);
  const [speed, setSpeed] = useState(1);
  const [curT, setCurT] = useState(0);
  const [playerError, setPlayerError] = useState<string | null>(null);
  const activeIdx = indexAtOrBefore(offsets, curT + 0.05);

  const seekTo = useCallback((t: number) => {
    playerRef.current?.seek(t);
    setCurT(t);
  }, []);

  // keep the active command visible inside its list
  const listRef = useRef<HTMLDivElement>(null);
  useEffect(() => {
    const list = listRef.current;
    if (!list || activeIdx < 0) return;
    const el = list.querySelector<HTMLElement>(`[data-idx="${activeIdx}"]`);
    if (!el) return;
    if (el.offsetTop < list.scrollTop) list.scrollTop = el.offsetTop;
    else if (el.offsetTop + el.offsetHeight > list.scrollTop + list.clientHeight) list.scrollTop = el.offsetTop + el.offsetHeight - list.clientHeight;
  }, [activeIdx]);

  // output search
  const [q, setQ] = useState('');
  const dq = useDeferredValue(q.trim());
  const matches = useMemo(() => (parsed && dq ? findOutputMatches(parsed.events, dq) : []), [parsed, dq]);
  const [mi, setMi] = useState(-1);
  useEffect(() => setMi(-1), [dq]);
  const stepMatch = (dir: 1 | -1) => {
    if (!matches.length) return;
    const n = mi < 0 ? (dir === 1 ? 0 : matches.length - 1) : (mi + dir + matches.length) % matches.length;
    setMi(n);
    seekTo(matches[n]);
  };

  const live = s?.active === true;
  const now = useNow(live ? 1000 : 0);
  const nowMs = Date.now();
  const duration = s ? (live ? Math.max(0, Math.floor((now - Date.parse(s.startedAt)) / 1000)) : s.durationSeconds) : null;
  const end = s && !s.active ? endChip(s.endReason) : null;

  const root: React.CSSProperties = { flex: 1, minHeight: 0, display: 'flex', flexDirection: 'column' };

  if (session.error != null && !s) {
    return (
      <div style={root}>
        <div className="rp-head">
          <Crumbs items={[{ label: 'Sessions', to: '/sessions' }, { label: id, mono: true }]} />
          <ErrorBanner error={session.error} onRetry={session.reload} title="Could not load this session" />
        </div>
      </div>
    );
  }

  const recErr = rec.status === 'error' ? recordingErrorText(rec.error) : null;

  return (
    <div style={root}>
      <div className="rp-wrap" style={{ overflow: 'auto' }}>
        <div className="rp-head">
          <Crumbs items={[{ label: 'Sessions', to: '/sessions' }, { label: id, mono: true }]} />
          {!s ? (
            <Skeleton rows={2} />
          ) : (
            <>
              <div className="row wrap" style={{ gap: 20, alignItems: 'flex-start' }}>
                <div className="row" style={{ gap: 10 }}>
                  <Avatar name={s.username} size={32} />
                  <div><b>{s.username}</b></div>
                </div>
                <dl className="kv" style={{ gridTemplateColumns: 'auto 1fr auto 1fr', gap: '4px 16px', flex: 1, minWidth: 'min(480px,100%)' }}>
                  <dt>Pod</dt>
                  <dd className="mono">{s.pod} · {s.container}{s.debugContainer && ` (debug: ${s.debugContainer})`}</dd>
                  <dt>Environment</dt>
                  <dd><EnvBadge env={s.environment} /> <span className="mono">{s.cluster}</span> <span className="muted mono">{s.namespace}</span></dd>
                  <dt>Started</dt>
                  <dd title={s.startedAt}>{fmtWhen(s.startedAt, nowMs)}</dd>
                  <dt>Duration</dt>
                  <dd className="mono">
                    {fmtDuration(duration)}
                    {live && <span className="chip prod"><span className="dot rec" />Open now</span>}
                    {end && <span className={`chip${end.tone === 'warn' ? ' warn' : end.tone === 'err' ? ' err' : ''}`} style={{ marginLeft: 6 }}>{end.text}</span>}
                  </dd>
                </dl>
                {s.breakGlassId !== null && <BreakGlassMark />}
              </div>
              {s.breakGlassId !== null && bgReason && (
                <div className="banner red"><span><b>Break-glass reason:</b> {bgReason}</span></div>
              )}
              {live && (
                <div className="banner info">
                  <span>This session is still open, so the recording is incomplete.</span>
                  <span className="grow" />
                  <Link className="btn sm" to={`/sessions/${encodeURIComponent(s.id)}/watch`}><Icon name="eye" size={12} />Watch live</Link>
                </div>
              )}
            </>
          )}
        </div>

        <div className="rp-main">
          <div className="rp-pane">
            <div className="termbar">
              <span className="mono">{s?.pod ?? '…'}</span>
              {s && <span className="mono tb-dim" style={{ color: '#8C97A8' }}>/ {s.container}</span>}
              <span className="grow" />
              <div className="rp-find">
                <label className="search">
                  <Icon name="search" />
                  <input
                    aria-label="Search session output"
                    placeholder="Search output"
                    value={q}
                    disabled={!parsed}
                    onChange={(e) => setQ(e.target.value)}
                    onKeyDown={(e) => {
                      if (e.key === 'Enter') stepMatch(e.shiftKey ? -1 : 1);
                    }}
                  />
                  <span className="cnt" aria-live="polite">
                    {dq ? (matches.length ? `${mi >= 0 ? `${mi + 1} / ` : ''}${matches.length} match${matches.length === 1 ? '' : 'es'}` : 'No matches') : ''}
                  </span>
                </label>
                <button type="button" className="btn tdark sm" aria-label="Previous match" disabled={!matches.length} onClick={() => stepMatch(-1)}>
                  <Icon name="chevd" size={12} style={{ transform: 'rotate(180deg)' }} />
                </button>
                <button type="button" className="btn tdark sm" aria-label="Next match" disabled={!matches.length} onClick={() => stepMatch(1)}>
                  <Icon name="chevd" size={12} />
                </button>
              </div>
            </div>

            <div className="rp-stage">
              {rec.status === 'loading' && (
                <div className="rp-msg" aria-busy="true">
                  <span className="muted" style={{ color: '#8C97A8' }}>Loading recording…</span>
                  <div className="skel" style={{ width: 320, background: '#1D2634' }} />
                  <div className="skel" style={{ width: 220, background: '#1D2634' }} />
                </div>
              )}
              {recErr && (
                <div className="rp-msg" role="alert">
                  <h2>{recErr.title}</h2>
                  <p>{recErr.detail}</p>
                  {live && <p>This session is still open. You can follow it live instead.</p>}
                  <div className="row wrap">
                    <button type="button" className="btn tdark" onClick={() => setAttempt((n) => n + 1)}>
                      <Icon name="refresh" size={12} />Try again
                    </button>
                    {live && s && (
                      <Link className="btn act" to={`/sessions/${encodeURIComponent(s.id)}/watch`}><Icon name="eye" />Watch live</Link>
                    )}
                    <Link className="btn tdark" to="/sessions">Back to sessions</Link>
                  </div>
                </div>
              )}
              {rec.status === 'ok' && rec.parsed.events.length === 0 && (
                <div className="rp-msg">
                  <h2>The recording is empty</h2>
                  <p>No terminal output was captured for this session.</p>
                  {live && s && <Link className="btn act" to={`/sessions/${encodeURIComponent(s.id)}/watch`}><Icon name="eye" />Watch live</Link>}
                </div>
              )}
              {rec.status === 'ok' && rec.parsed.events.length > 0 && (
                <CastPlayer
                  ref={playerRef}
                  text={rec.text}
                  eventTimes={eventTimes}
                  speed={speed}
                  idleLimit={IDLE_LIMIT}
                  onTime={setCurT}
                  onError={setPlayerError}
                />
              )}
              {playerError && (
                <div className="rp-msg" role="alert">
                  <h2>The recording could not be played</h2>
                  <p>{playerError}</p>
                </div>
              )}
            </div>

            <div className="rp-bar">
              <span className="dim">Replay · exactly what the user saw and typed</span>
              <span className="grow" />
              <span className="mono dim" aria-label="Position">{mmss(curT)}{parsed ? ` / ${mmss(parsed.duration)}` : ''}</span>
              <span className="dim">Speed</span>
              <div className="seg rp-seg" role="group" aria-label="Playback speed">
                {SPEEDS.map((v) => (
                  <button key={v} type="button" className={v === speed ? 'on' : ''} aria-pressed={v === speed} onClick={() => setSpeed(v)}>
                    {v}×
                  </button>
                ))}
              </div>
            </div>
          </div>

          <aside className="card rp-side" aria-label="Commands">
            <div className="hd">
              Commands <small>{commands.length} typed · click to jump</small>
            </div>
            <div className="rp-note">
              Best-effort: derived from typed input; tab-completion and editing keys can make it imperfect. The replay is authoritative.
            </div>
            <div className="rp-cmds" ref={listRef}>
              {rec.status === 'loading' && !s?.commands?.length ? (
                <Skeleton rows={4} />
              ) : commands.length === 0 ? (
                <div className="empty">No typed commands found.</div>
              ) : (
                commands.map((c, i) => (
                  <button
                    key={`${i}-${c.offsetSeconds}`}
                    type="button"
                    data-idx={i}
                    className={`rp-cmd${i === activeIdx ? ' on' : ''}`}
                    aria-current={i === activeIdx ? 'true' : undefined}
                    disabled={rec.status !== 'ok'}
                    onClick={() => seekTo(c.offsetSeconds)}
                  >
                    <span className="mono muted">{mmss(c.offsetSeconds)}</span>
                    <span className="mono">{c.text}</span>
                  </button>
                ))
              )}
            </div>
          </aside>
        </div>
      </div>
    </div>
  );
}
