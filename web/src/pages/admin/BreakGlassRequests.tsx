import { useState } from 'react';
import { api } from '../../api';
import type { BreakGlassRequest } from '../../api';
import { Avatar } from '../../components/Badges';
import { Empty, ErrorBanner, Skeleton } from '../../components/Feedback';
import { Icon } from '../../components/Icon';
import { FieldError } from '../../components/AdminBits';
import { useAuth } from '../../context/auth';
import { useToast } from '../../context/toast';
import '../../components/adminPages.css';
import { useApi } from '../../hooks/useApi';
import { useNow } from '../../hooks/useNow';
import { breakGlassDurations } from '../../lib/breakGlass';
import { fmtCountdown, fmtDateTime, fmtWhen, msUntil } from '../../lib/time';

const STATUS_TONE: Record<BreakGlassRequest['status'], string> = {
  pending: 'warn', approved: 'ok', rejected: 'err', cancelled: '', expired: '',
};

function StatusChip({ status }: { status: BreakGlassRequest['status'] }) {
  return <span className={`chip ${STATUS_TONE[status]}`}>{status.charAt(0).toUpperCase() + status.slice(1)}</span>;
}

/**
 * Break-glass requests (mode `approval`) on the Access requests page: pending
 * ones first, as they are urgent, with the last few decisions below. Hidden
 * while the mode is not `approval` and nothing is pending.
 */
export function BreakGlassRequests() {
  const { me, isAdmin } = useAuth();
  const now = useNow(1000);
  const reqs = useApi(() => api.breakGlassRequests(), [], { poll: 15_000 });
  const [showDecided, setShowDecided] = useState(false);
  const all = reqs.data ?? [];
  const pending = all.filter((r) => r.status === 'pending');
  const decided = all.filter((r) => r.status !== 'pending').slice(0, 10);
  if (me.settings.breakGlassMode !== 'approval' && pending.length === 0) return null;

  return (
    <div className="card">
      <div className="hd">
        Break-glass requests <small>{pending.length} pending · each starts when approved</small>
      </div>
      {reqs.error ? (
        <div className="bg-rq"><ErrorBanner error={reqs.error} onRetry={reqs.reload} /></div>
      ) : reqs.loading && !reqs.data ? (
        <Skeleton rows={2} />
      ) : pending.length === 0 ? (
        <Empty>No pending break-glass requests.</Empty>
      ) : (
        <div role="list">
          {pending.map((r) => (
            <div key={r.id} role="listitem" className="bg-rq">
              <PendingRow req={r} now={now} canDecide={isAdmin} max={me.settings.breakGlassMaxMinutes} onDone={reqs.reload} />
            </div>
          ))}
        </div>
      )}
      {decided.length > 0 && (
        <div className="bg-rq-ft">
          <button type="button" className="row linklike blue" style={{ alignSelf: 'flex-start' }} aria-expanded={showDecided} onClick={() => setShowDecided((v) => !v)}>
            Recently decided ({decided.length})
            <Icon name={showDecided ? 'chevd' : 'chev'} />
          </button>
          {showDecided && (
            <div className="tbl-wrap">
              <table className="tbl">
                <tbody>
                  {decided.map((r) => (
                    <tr key={r.id}>
                      <td><b>{r.username}</b></td>
                      <td><StatusChip status={r.status} /></td>
                      <td className="muted">
                        {r.status === 'approved' ? `${r.approvedMinutes} minutes` : r.status === 'expired' ? 'not decided in time' : ''}
                        {r.decidedBy && r.decidedBy !== 'system' ? ` · by ${r.decidedBy}` : ''}
                      </td>
                      <td className="muted">{fmtWhen(r.decidedAt, now)}</td>
                      <td>“{r.reason}”{r.decisionNote ? <span className="muted"> · {r.decisionNote}</span> : null}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </div>
      )}
    </div>
  );
}

function PendingRow({
  req, now, canDecide, max, onDone,
}: { req: BreakGlassRequest; now: number; canDecide: boolean; max: number; onDone: () => void }) {
  const toast = useToast();
  const [minutes, setMinutes] = useState(Math.min(req.durationMinutes, max));
  const [note, setNote] = useState('');
  const [noteErr, setNoteErr] = useState<string | undefined>();
  const [busy, setBusy] = useState<'approve' | 'reject' | null>(null);
  const [error, setError] = useState<unknown>(null);
  const left = msUntil(req.expiresAt, now);

  const run = async (kind: 'approve' | 'reject') => {
    if (kind === 'reject' && !note.trim()) {
      setNoteErr('A note is required when rejecting.');
      return;
    }
    setBusy(kind);
    setError(null);
    setNoteErr(undefined);
    try {
      if (kind === 'approve') {
        const res = await api.approveBreakGlassRequest(req.id, {
          ...(note.trim() ? { note: note.trim() } : {}),
          ...(minutes !== req.durationMinutes ? { durationMinutes: minutes } : {}),
        });
        toast(`Approved ${req.username}’s break-glass for ${res.approvedMinutes} minutes.`);
      } else {
        await api.rejectBreakGlassRequest(req.id, { note: note.trim() });
        toast(`Rejected ${req.username}’s break-glass request.`);
      }
      onDone();
    } catch (e) {
      setError(e);
    } finally {
      setBusy(null);
    }
  };

  return (
    <>
      <span className="row" style={{ gap: 8 }}>
        <Avatar name={req.username} size={24} />
        <b>{req.username}</b>
        <span className="grow" />
        <span className="muted" title={`Requested ${fmtDateTime(req.createdAt)}`}>
          {left !== null && left > 0 ? `expires in ${fmtCountdown(left)}` : 'expiring'}
        </span>
      </span>
      <div className="quote">{req.durationMinutes} minutes · “{req.reason}”</div>
      {canDecide && (
        <div className="row wrap" style={{ gap: 16, alignItems: 'flex-end' }}>
          <label className="f">
            Duration
            <select className="sel" style={{ height: 30 }} value={minutes} onChange={(e) => setMinutes(Number(e.target.value))}>
              {breakGlassDurations(max, req.durationMinutes).map((m) => (
                <option key={m} value={m}>{m} minutes{m === req.durationMinutes ? ' (as requested)' : ''}</option>
              ))}
            </select>
          </label>
          <label className="f grow" style={{ minWidth: 200 }}>
            <span>Note to requester <span className="hint">Required when rejecting</span></span>
            <input
              className="input"
              value={note}
              aria-invalid={noteErr ? true : undefined}
              onChange={(e) => { setNote(e.target.value); setNoteErr(undefined); }}
            />
            <FieldError msg={noteErr} />
          </label>
          <div className="row" style={{ gap: 10 }}>
            <button type="button" className="btn" disabled={busy !== null} onClick={() => void run('reject')}>
              {busy === 'reject' ? 'Rejecting…' : 'Reject'}
            </button>
            <button type="button" className="btn dng" disabled={busy !== null} onClick={() => void run('approve')}>
              <Icon name="check" />
              {busy === 'approve' ? 'Approving…' : minutes === req.durationMinutes ? 'Approve' : `Approve for ${minutes} minutes`}
            </button>
          </div>
        </div>
      )}
      {error !== null && <ErrorBanner error={error} />}
    </>
  );
}
