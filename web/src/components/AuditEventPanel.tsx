import { useEffect, useRef } from 'react';
import { Link } from 'react-router-dom';
import type { AuditEvent } from '../api/types';
import { useApi } from '../hooks/useApi';
import { api } from '../api';
import { useAuth } from '../context/auth';
import { adminDiff } from '../lib/auditDiff';
import { eventTitle, reasonText, targetFull } from '../lib/auditLabels';
import { BreakGlassMark, EnvBadge, ResultChip } from './Badges';
import { Icon } from './Icon';

function fullTime(iso: string): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return iso;
  let tz = '';
  try {
    tz = Intl.DateTimeFormat().resolvedOptions().timeZone;
  } catch {
    tz = '';
  }
  const s = new Intl.DateTimeFormat('en-GB', {
    year: 'numeric', month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', second: '2-digit', hourCycle: 'h23',
  }).format(d);
  return `${s.replace(',', '')}${tz ? ` (${tz})` : ''}`;
}

/** Body of the event detail: all fields, details JSON, before/after diff and session links. */
export function AuditEventBody({ event: e, large }: { event: AuditEvent; large?: boolean }) {
  const { isAdmin, isAuditor } = useAuth();
  const sid = e.sessionId;
  const session = useApi(() => api.session(sid as string), [sid], { enabled: !!sid });
  const active = !!sid && session.data?.active === true;
  const canWatch = (isAdmin || isAuditor) && active;
  const diff = adminDiff(e.details);
  const target = targetFull(e);

  const row = (label: string, value: React.ReactNode, mono?: boolean) =>
    value === null || value === undefined || value === '' ? null : (
      <>
        <dt>{label}</dt>
        <dd className={mono ? 'mono' : undefined}>{value}</dd>
      </>
    );

  return (
    <div className="col" style={{ gap: 14 }}>
      <dl className="kv" title={e.at}>
        {row('Time', fullTime(e.at), true)}
        {row('User', e.username)}
        {row('Event', <>{eventTitle(e)} <span className="muted mono">{e.eventType}</span></>)}
        {row('Action', e.action)}
        {row('Target', target, true)}
        {row('Container', e.container, true)}
        {row('Environment', e.environment ? <EnvBadge env={e.environment} /> : null)}
        {row('Result', <><ResultChip result={e.result} />{e.reason && <span className="muted">{reasonText(e.reason)}</span>}</>)}
        {row('Break-glass', e.breakGlassId !== null ? <><BreakGlassMark /><span className="mono">#{e.breakGlassId}</span></> : null)}
        {row('Session', sid ? <>{sid}{session.data ? (active ? ' · open now' : '') : ''}</> : null, true)}
        {row('Source IP', e.sourceIp, true)}
        {row('User agent', e.userAgent)}
        {row('Event id', String(e.id), true)}
      </dl>

      {sid && (
        <div className="row wrap">
          <Link className="btn pri" to={`/sessions/${encodeURIComponent(sid)}`} style={large ? { flex: 1 } : undefined}>
            <Icon name="play" />Replay session
          </Link>
          {canWatch && (
            <Link className="btn" to={`/sessions/${encodeURIComponent(sid)}/watch`} style={large ? { flex: 1 } : undefined}>
              <Icon name="eye" />Watch live
            </Link>
          )}
        </div>
      )}

      {diff && (
        <div className="col" style={{ gap: 6 }}>
          <span className="lbl">Change (before / after)</span>
          {diff.length ? (
            <div className="card diff" role="group" aria-label="Changes">
              {diff.map((l, i) => (
                <div key={i} className={l.kind}>{l.text}</div>
              ))}
            </div>
          ) : (
            <span className="muted">No field differences recorded.</span>
          )}
        </div>
      )}

      {e.details && (
        <details open={!diff}>
          <summary className="lbl" style={{ cursor: 'pointer' }}>Details</summary>
          <div className="card" style={{ marginTop: 6 }}>
            <pre className="au-json">{JSON.stringify(e.details, null, 2)}</pre>
          </div>
        </details>
      )}
    </div>
  );
}

/** Desktop: right-hand card next to the table. */
export function AuditDrawer({ event, onClose }: { event: AuditEvent; onClose: () => void }) {
  return (
    <aside className="card au-drawer" aria-label="Event detail">
      <div className="hd">
        {eventTitle(event)}
        <EnvBadge env={event.environment} />
        <span className="grow" />
        <button type="button" className="btn ghost sm" aria-label="Close detail" onClick={onClose}>
          <Icon name="x" />
        </button>
      </div>
      <div className="bd">
        <AuditEventBody event={event} />
      </div>
    </aside>
  );
}

/** Phone: full-screen sheet. */
export function AuditSheet({ event, onClose }: { event: AuditEvent; onClose: () => void }) {
  const closeRef = useRef<HTMLButtonElement>(null);
  const cb = useRef(onClose);
  cb.current = onClose;
  useEffect(() => {
    const prev = document.activeElement as HTMLElement | null;
    closeRef.current?.focus();
    const prevOverflow = document.body.style.overflow;
    document.body.style.overflow = 'hidden';
    const onKey = (ev: KeyboardEvent) => {
      if (ev.key === 'Escape') cb.current();
    };
    document.addEventListener('keydown', onKey);
    return () => {
      document.removeEventListener('keydown', onKey);
      document.body.style.overflow = prevOverflow;
      prev?.focus?.();
    };
  }, []);
  return (
    <div className="au-sheet" role="dialog" aria-modal="true" aria-label="Event detail">
      <div className="au-sheet-bar">
        <button type="button" ref={closeRef} className="btn ghost" style={{ height: 44, width: 44, justifyContent: 'center', padding: 0 }} aria-label="Back to audit log" onClick={onClose}>
          <Icon name="chev" style={{ transform: 'rotate(180deg)' }} />
        </button>
        <b style={{ fontSize: 15 }}>{eventTitle(event)}</b>
        <EnvBadge env={event.environment} />
        <span className="grow" />
        <ResultChip result={event.result} />
      </div>
      <div className="au-sheet-body">
        <AuditEventBody event={event} large />
      </div>
    </div>
  );
}
