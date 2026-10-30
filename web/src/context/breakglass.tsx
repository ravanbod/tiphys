import { createContext, useCallback, useContext, useMemo, useState } from 'react';
import { ApiError, api, errorText } from '../api';
import type { Action } from '../api';
import { EnvBadge } from '../components/Badges';
import { Dialog } from '../components/Dialog';
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

  return (
    <C.Provider value={value}>
      {children}
      {isOpen && <BreakGlassDialog target={target} onClose={() => setOpen(false)} />}
    </C.Provider>
  );
}
export const useBreakGlass = () => useContext(C);

function durationOptions(max: number): number[] {
  const base = [15, 30, 60, 120, 180, 240, 480];
  const opts = base.filter((m) => m <= max);
  if (!opts.includes(max)) opts.push(max);
  return opts.sort((a, b) => a - b);
}

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

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    if (!valid || busy) return;
    setBusy(true);
    setErr(null);
    try {
      await api.startBreakGlass({ reason: reason.trim(), durationMinutes: minutes });
      await reload();
      toast('Break-glass started. The platform team has been notified.');
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
      title="Use break-glass"
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
            Start break-glass
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
            {durationOptions(max).map((m) => (
              <option key={m} value={m}>{m} minutes{m === def ? ' (default)' : ''}</option>
            ))}
          </select>
          <span className="hint">Maximum {max} minutes, set by admins.</span>
        </label>
        <div className="banner red" style={{ alignItems: 'flex-start' }}>
          <span>
            <b>The platform team is notified immediately.</b> Break-glass starts as soon as you submit; every action while it is active is marked in the audit log. It never grants access you do not already have.
          </span>
        </div>
        {err && <div className="banner red" role="alert">{err}</div>}
      </form>
    </Dialog>
  );
}
