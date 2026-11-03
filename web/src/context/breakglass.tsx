import { createContext, useCallback, useContext, useEffect, useMemo, useRef, useState } from 'react';
import { ApiError, api, errorText } from '../api';
import type { Action, BreakGlassRequest } from '../api';
import { EnvBadge } from '../components/Badges';
import { Dialog } from '../components/Dialog';
import { breakGlassDurations, breakGlassNeedsApproval } from '../lib/breakGlass';
import { fmtDateTime } from '../lib/time';
import { useAuth } from './auth';
import { useToast } from './toast';

export interface BreakGlassTarget {
  action?: Action;
  target?: string;
  environment?: string;
}
interface Ctx {
  open: (t?: BreakGlassTarget) => void;
  end: () => Promise<void>;
}
const C = createContext<Ctx>({ open: () => {}, end: async () => {} });

export const MIN_REASON = 20;

/** How often /me is refetched while a break-glass request waits for an admin. */
const PENDING_POLL_MS = 10_000;

export function BreakGlassProvider({ children }: { children: React.ReactNode }) {
  const { me, reload } = useAuth();
  const toast = useToast();
  const [target, setTarget] = useState<BreakGlassTarget | null>(null);
  const [isOpen, setOpen] = useState(false);

  const open = useCallback((t?: BreakGlassTarget) => {
    setTarget(t ?? null);
    setOpen(true);
  }, []);
  const end = useCallback(async () => {
    if (!me.breakGlass) return;
    try {
      await api.endBreakGlass(me.breakGlass.id);
      toast('Break-glass ended');
      await reload();
    } catch (e) {
      toast(errorText(e), 'err');
    }
  }, [me.breakGlass, reload, toast]);
  const value = useMemo(() => ({ open, end }), [open, end]);

  // While a request is pending, refetch /me so an approval shows without a
  // page reload, and tell the user how it was decided.
  const pending = me.breakGlassRequest?.id;
  useEffect(() => {
    if (!pending) return;
    const id = window.setInterval(() => void reload().catch(() => {}), PENDING_POLL_MS);
    return () => window.clearInterval(id);
  }, [pending, reload]);
  const prevPending = useRef(pending);
  const cancelled = useRef<number | null>(null);
  useEffect(() => {
    const was = prevPending.current;
    prevPending.current = pending;
    if (!was || pending === was || cancelled.current === was) return;
    if (me.breakGlass?.active) toast(`Break-glass approved: active until ${fmtDateTime(me.breakGlass.expiresAt)}.`);
    else toast('Your break-glass request was rejected or expired.', 'err');
  }, [pending, me.breakGlass, toast]);

  return (
    <C.Provider value={value}>
      {children}
      {isOpen && (me.breakGlassRequest
        ? <PendingDialog req={me.breakGlassRequest} onCancelled={(id) => { cancelled.current = id; }} onClose={() => setOpen(false)} />
        : <BreakGlassDialog target={target} onClose={() => setOpen(false)} />)}
    </C.Provider>
  );
}

function PendingDialog({ req, onCancelled, onClose }: { req: BreakGlassRequest; onCancelled: (id: number) => void; onClose: () => void }) {
  const { reload } = useAuth();
  const toast = useToast();
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  async function cancel() {
    if (busy) return;
    setBusy(true);
    setErr(null);
    try {
      onCancelled(req.id);
      await api.cancelBreakGlassRequest(req.id);
      await reload();
      toast('Break-glass request cancelled');
      onClose();
    } catch (ex) {
      setErr(errorText(ex));
      setBusy(false);
    }
  }

  return (
    <Dialog
      tone="bg"
      onClose={onClose}
      width={440}
      title="Break-glass requested"
      subtitle={<span>Waiting for an admin to approve it. It starts as soon as it is approved.</span>}
      footer={
        <>
          <button type="button" className={`btn${busy ? ' dis' : ''}`} aria-disabled={busy} onClick={() => void cancel()}>Cancel request</button>
          <button type="button" className="btn act" onClick={onClose} data-autofocus>Close</button>
        </>
      }
    >
      <dl className="kv" style={{ gridTemplateColumns: '120px 1fr' }}>
        <dt>Reason</dt><dd>{req.reason}</dd>
        <dt>Duration</dt><dd>{req.durationMinutes} minutes, from approval</dd>
        <dt>Requested</dt><dd>{fmtDateTime(req.createdAt)}</dd>
        <dt>Expires</dt><dd>{fmtDateTime(req.expiresAt)} if nobody decides</dd>
      </dl>
      {err && <div className="banner red" role="alert" style={{ marginTop: 12 }}>{err}</div>}
    </Dialog>
  );
}
export const useBreakGlass = () => useContext(C);

function BreakGlassDialog({ target, onClose }: { target: BreakGlassTarget | null; onClose: () => void }) {
  const { me, reload } = useAuth();
  const toast = useToast();
  const { breakGlassDefaultMinutes: def, breakGlassMaxMinutes: max } = me.settings;
  const [reason, setReason] = useState('');
  const [minutes, setMinutes] = useState(Math.min(def, max));
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const len = reason.trim().length;
  const valid = len >= MIN_REASON;
  const approval = breakGlassNeedsApproval(me);

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    if (!valid || busy) return;
    setBusy(true);
    setErr(null);
    try {
      if (approval) {
        await api.requestBreakGlass({ reason: reason.trim(), durationMinutes: minutes });
        await reload();
        toast('Break-glass requested. It starts as soon as an admin approves it.');
      } else {
        await api.startBreakGlass({ reason: reason.trim(), durationMinutes: minutes });
        await reload();
        toast('Break-glass started. The platform team has been notified.');
      }
      onClose();
    } catch (ex) {
      setErr(ex instanceof ApiError && ex.fields.reason ? ex.fields.reason : errorText(ex));
      setBusy(false);
    }
  }

  return (
    <Dialog
      tone="bg"
      onClose={onClose}
      width={440}
      title={approval ? 'Request break-glass' : 'Use break-glass'}
      subtitle={
        target?.action ? (
          <span className="row" style={{ gap: 8 }}>
            <span>
              {target.action} on <span className="mono" style={{ color: 'var(--tx)' }}>{target.target}</span>
            </span>
            {target.environment && <EnvBadge env={target.environment} />}
          </span>
        ) : (
          <span>Overrides time windows and holidays for your existing grants.</span>
        )
      }
      footer={
        <>
          <button type="button" className="btn" onClick={onClose}>Cancel</button>
          <button type="submit" form="bg-form" className={`btn dng${valid && !busy ? '' : ' dis'}`} aria-disabled={!valid || busy}>
            {approval ? 'Send request' : 'Start break-glass'}
          </button>
        </>
      }
    >
      <form id="bg-form" onSubmit={submit} className="col">
        <label className="f">
          Reason <span className="hint">Required, at least {MIN_REASON} characters. Shown to the platform team and in the audit log.</span>
          <textarea
            className="input" rows={3} value={reason} onChange={(e) => setReason(e.target.value)}
            aria-describedby="bg-count" data-autofocus aria-invalid={!valid && len > 0}
          />
          <span id="bg-count" className="hint" style={{ alignSelf: 'flex-end' }}>
            {len} characters{!valid ? ` (${MIN_REASON - len} more needed)` : ''}
          </span>
        </label>
        <label className="f">
          Duration
          <select className="sel" style={{ height: 30 }} value={minutes} onChange={(e) => setMinutes(Number(e.target.value))}>
            {breakGlassDurations(max).map((m) => (
              <option key={m} value={m}>{m} minutes{m === def ? ' (default)' : ''}</option>
            ))}
          </select>
          <span className="hint">Maximum {max} minutes, set by admins.{approval ? ' Counted from approval; the admin may change it.' : ''}</span>
        </label>
        <div className="banner red" style={{ alignItems: 'flex-start' }}>
          {approval ? (
            <span>
              <b>An admin must approve this.</b> Break-glass starts when it is approved; every action while it is active is marked in the audit log. It never grants access you do not already have.
            </span>
          ) : (
            <span>
              <b>The platform team is notified immediately.</b> Break-glass starts as soon as you submit; every action while it is active is marked in the audit log. It never grants access you do not already have.
            </span>
          )}
        </div>
        {err && <div className="banner red" role="alert">{err}</div>}
      </form>
    </Dialog>
  );
}
