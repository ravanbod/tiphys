import { useCallback, useEffect, useRef, useState } from 'react';
import { Link, useParams } from 'react-router-dom';
import { api, openStream, wsUrl } from '../../api';
import type { EndReason } from '../../api';
import { EnvBadge } from '../../components/Badges';
import { ErrorBanner } from '../../components/Feedback';
import { Icon } from '../../components/Icon';
import { TerminateSessionDialog } from '../../components/TerminateSessionDialog';
import { XTerm } from '../../components/XTerm';
import type { XTermHandle } from '../../components/XTerm';
import { useAuth } from '../../context/auth';
import { useApi } from '../../hooks/useApi';
import { useNow } from '../../hooks/useNow';
import { describeEnd } from '../../lib/endReason';
import { fmtCountdown, fmtDuration, fmtTime, msUntil } from '../../lib/time';
import '../../styles/auditpages.css';

interface Ended {
  reason: EndReason | string;
  message?: string;
}

type Link_ = 'connecting' | 'live' | 'ended' | 'failed' | 'lost';

function EndedCard({
  title, detail, chip, tone, env, rows, sessionId,
}: {
  title: string;
  detail: string;
  chip: string;
  tone: 'neutral' | 'warn' | 'err';
  env: string;
  rows: { k: string; v: React.ReactNode }[];
  sessionId: string;
}) {
  return (
    <div className="dlg" style={{ width: 'min(520px,100%)' }} role="alertdialog" aria-labelledby="wt-end-title" aria-describedby="wt-end-detail">
      <div className="hd" style={{ paddingTop: 20 }}>
        <div className="row">
          <span className={`chip${tone === 'warn' ? ' warn' : tone === 'err' ? ' err' : ''}`}>{chip}</span>
          <span className="grow" />
          <EnvBadge env={env} />
        </div>
        <h2 id="wt-end-title" style={{ marginTop: 10, fontSize: 18 }}>{title}</h2>
        <p id="wt-end-detail">{detail}</p>
      </div>
      <div className="bd">
        <dl className="kv" style={{ gridTemplateColumns: '110px 1fr' }}>
          {rows.map((r) => (
            <div key={r.k} style={{ display: 'contents' }}>
              <dt>{r.k}</dt>
              <dd>{r.v}</dd>
            </div>
          ))}
        </dl>
      </div>
      <div className="ft">
        <Link className="btn" to="/sessions" data-autofocus>Back to sessions</Link>
        <Link className="btn act" to={`/sessions/${encodeURIComponent(sessionId)}`}><Icon name="play" />Replay</Link>
      </div>
    </div>
  );
}

export function Watch() {
  const { id = '' } = useParams<{ id: string }>();
  const { isAdmin } = useAuth();
  const session = useApi(() => api.session(id), [id]);
  const s = session.data;
  const term = useRef<XTermHandle>(null);
  const [attempt, setAttempt] = useState(0);
  const [linkState, setLinkState] = useState<Link_>('connecting');
  const [ended, setEnded] = useState<Ended | null>(null);
  const [closesAt, setClosesAt] = useState<string | null>(null);
  const [warning, setWarning] = useState<string | null>(null);
  const [failReason, setFailReason] = useState<string | null>(null);
  const [confirming, setConfirming] = useState(false);

  const sessionActive = s?.active === true;

  useEffect(() => {
    if (!sessionActive) return;
    let alive = true;
    setLinkState('connecting');
    setEnded(null);
    setFailReason(null);
    term.current?.clear();
    const h = openStream(wsUrl(`/sessions/${encodeURIComponent(id)}/watch`), {
      onBytes: (b) => {
        if (alive) term.current?.write(b);
      },
      onControl: (m) => {
        if (!alive) return;
        switch (m.type) {
          case 'ready':
            setLinkState('live');
            setClosesAt(m.closesAt);
            if (m.cols && m.rows) term.current?.resize(m.cols, m.rows);
            break;
          case 'closes_at':
            setClosesAt(m.closesAt);
            if (!m.closesAt) setWarning(null);
            break;
          case 'warning':
            setClosesAt(m.closesAt);
            setWarning(m.message);
            break;
          case 'end':
            setLinkState('ended');
            setEnded({ reason: m.reason, message: m.message });
            break;
          default:
            break;
        }
      },
      onClose: ({ opened, wasEnded }) => {
        if (!alive || wasEnded) return;
        if (opened) {
          setLinkState('lost');
          setFailReason('The connection to the portal was lost.');
          return;
        }
        setLinkState('failed');
        setFailReason('Could not attach to this session.');
        // The upgrade failed: find out whether the session is simply over.
        api
          .session(id)
          .then((x) => {
            if (!alive) return;
            if (!x.active) setFailReason('Session not found or already ended.');
          })
          .catch(() => {
            if (alive) setFailReason('Session not found or already ended.');
          });
      },
    });
    return () => {
      alive = false;
      h.close();
    };
  }, [id, sessionActive, attempt]);

  const now = useNow(sessionActive && linkState === 'live' ? 1000 : 0);
  const elapsed = s ? Math.max(0, Math.floor((now - Date.parse(s.startedAt)) / 1000)) : 0;
  const left = msUntil(closesAt, now);

  const onTerminated = useCallback(() => setConfirming(false), []);

  const root: React.CSSProperties = { flex: 1, minHeight: 0, display: 'flex', flexDirection: 'column' };

  if (session.error != null && !s) {
    return (
      <div style={root}>
        <div style={{ padding: 20 }} className="col">
          <Link className="btn sm" to="/sessions" style={{ alignSelf: 'flex-start' }}>
            <Icon name="chev" size={12} style={{ transform: 'rotate(180deg)' }} />Sessions
          </Link>
          <ErrorBanner error={session.error} onRetry={session.reload} title="Could not load this session" />
        </div>
      </div>
    );
  }

  const endedNow = ended ? describeEnd(ended.reason, ended.message) : null;
  const alreadyEnded = s && !s.active;
  const preEnd = alreadyEnded && s ? describeEnd(s.endReason ?? 'client_closed') : null;
  const showTerminate = isAdmin && sessionActive && linkState !== 'ended';

  return (
    <div style={root}>
      <div className="termbar" role="toolbar" aria-label="Live session">
        <Link className="btn tdark sm" to="/sessions">
          <Icon name="chev" size={12} style={{ transform: 'rotate(180deg)' }} />Sessions
        </Link>
        <span className="mono">{s?.pod ?? '…'}</span>
        {s && <span className="mono tb-dim">/ {s.container}</span>}
        {s && <EnvBadge env={s.environment} />}
        {sessionActive && linkState === 'live' && <span className="rec" role="status" aria-label="Live">LIVE</span>}
        {sessionActive && linkState === 'connecting' && <span className="tb-dim" role="status">Connecting…</span>}
        <span className="chip dk"><Icon name="eye" size={12} />read-only</span>
        {s && <span className="tb-dim">{s.username}</span>}
        <span className="grow" />
        {s && sessionActive && linkState === 'live' && (
          <>
            <span className="row" style={{ gap: 6 }}>
              <Icon name="clock" style={{ color: '#8C97A8' }} />
              <span className="tb-dim">Elapsed</span>
              <span className="mono">{fmtDuration(elapsed)}</span>
            </span>
            {left !== null && (
              <span className="row" style={{ gap: 6 }}>
                <span className="tb-dim">Time left</span>
                <span className="mono" style={{ color: '#F5C26B' }}>{fmtCountdown(left)}</span>
              </span>
            )}
          </>
        )}
        {s && !sessionActive && <span className="mono tb-dim">{fmtDuration(s.durationSeconds)} total</span>}
        {showTerminate && (
          <button type="button" className="btn dng sm" onClick={() => setConfirming(true)}>Terminate</button>
        )}
      </div>

      {warning && linkState === 'live' && (
        <div className="wt-warn" role="status">
          <Icon name="warn" />
          <span>{warning}</span>
          <span className="grow" />
          {closesAt && <span className="mono">closes {fmtTime(closesAt, undefined, true)}</span>}
        </div>
      )}

      {(linkState === 'failed' || linkState === 'lost') && (
        <div className="wt-err" role="alert">
          <Icon name="warn" />
          <span><b>{failReason}</b></span>
          <span className="grow" />
          {linkState === 'lost' && (
            <button type="button" className="btn tdark sm" onClick={() => setAttempt((n) => n + 1)}>
              <Icon name="refresh" size={12} />Reconnect
            </button>
          )}
          <Link className="btn tdark sm" to={`/sessions/${encodeURIComponent(id)}`}><Icon name="play" size={12} />Replay</Link>
        </div>
      )}

      <div className="wt-stage">
        {sessionActive ? (
          <div className={ended ? 'wt-dim' : undefined} style={{ flex: 1, minHeight: 0, display: 'flex', flexDirection: 'column' }}>
            <XTerm ref={term} readOnly ariaLabel="Live session output (read-only)" />
          </div>
        ) : (
          <div style={{ flex: 1 }} />
        )}

        {endedNow && s && (
          <div className="wt-overlay">
            <EndedCard
              title={endedNow.title}
              detail={endedNow.detail}
              chip="Session closed"
              tone={endedNow.tone}
              env={s.environment}
              sessionId={s.id}
              rows={[
                { k: 'User', v: s.username },
                { k: 'Pod', v: <span className="mono">{s.pod} · {s.container}{s.debugContainer && ` (debug: ${s.debugContainer})`}</span> },
                { k: 'Recording', v: <span className="chip ok"><Icon name="check" size={12} />Saved</span> },
              ]}
            />
          </div>
        )}

        {alreadyEnded && s && preEnd && (
          <div className="wt-overlay">
            <EndedCard
              title="This session has already ended"
              detail={`${preEnd.title}. There is nothing to watch live, but the recording can be replayed.`}
              chip={preEnd.chip}
              tone={preEnd.tone}
              env={s.environment}
              sessionId={s.id}
              rows={[
                { k: 'User', v: s.username },
                { k: 'Pod', v: <span className="mono">{s.pod} · {s.container}{s.debugContainer && ` (debug: ${s.debugContainer})`}</span> },
                { k: 'Duration', v: <span className="mono">{fmtDuration(s.durationSeconds)}</span> },
              ]}
            />
          </div>
        )}
      </div>

      {confirming && s && (
        <TerminateSessionDialog
          session={s}
          onClose={() => setConfirming(false)}
          onDone={onTerminated}
        />
      )}
    </div>
  );
}
