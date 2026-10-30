import { useState } from 'react';
import { Link } from 'react-router-dom';
import { api } from '../api';
import type { AccessGrant, WorkloadKind } from '../api';
import { useAuth } from '../context/auth';
import { useApi } from '../hooks/useApi';
import { MiniGrantRow } from './AdminPieces';
import { Dialog } from './Dialog';
import { ErrorBanner, Loading } from './Feedback';

interface Target { cluster: string; namespace: string; kind?: WorkloadKind; name?: string }
type Row = AccessGrant & { expired?: boolean };

/**
 * "Who has access" button: lists the grants that reach a namespace or workload.
 * Developers see the unexpired grants of what they can open; admins and
 * auditors see every grant, expired ones included.
 */
export function WhoHasAccess(t: Target) {
  const [open, setOpen] = useState(false);
  return (
    <>
      <button type="button" className="btn" onClick={() => setOpen(true)}>Who has access</button>
      {open && <WhoHasAccessDialog t={t} onClose={() => setOpen(false)} />}
    </>
  );
}

function WhoHasAccessDialog({ t, onClose }: { t: Target; onClose: () => void }) {
  const { isUser } = useAuth();
  return isUser ? <UserDialog t={t} onClose={onClose} /> : <StaffDialog t={t} onClose={onClose} />;
}

function UserDialog({ t, onClose }: { t: Target; onClose: () => void }) {
  const grants = useApi(() => api.whoHasAccess(t), [t.cluster, t.namespace, t.kind, t.name]);
  return <AccessDialog t={t} onClose={onClose} loading={grants.loading && !grants.data} error={grants.error} onRetry={grants.reload} rows={grants.data ?? []} />;
}

function StaffDialog({ t, onClose }: { t: Target; onClose: () => void }) {
  const grants = useApi(() => api.grantsCovering(t), [t.cluster, t.namespace, t.kind, t.name]);
  const teams = useApi(() => api.teams(), []);
  const members = (id: number) => teams.data?.find((x) => x.id === id)?.members.map((m) => m.displayName || m.username) ?? [];
  const rows: Row[] = (grants.data ?? []).map((g) => ({
    ...g, holder: { ...g.holder, members: g.holder.kind === 'team' ? members(g.holder.id) : [] },
  }));
  return <AccessDialog t={t} onClose={onClose} loading={grants.loading && !grants.data} error={grants.error} onRetry={grants.reload} rows={rows} />;
}

function AccessDialog({ t, onClose, loading, error, onRetry, rows }: {
  t: Target; onClose: () => void; loading: boolean; error: unknown; onRetry: () => void; rows: Row[];
}) {
  const { isAdmin } = useAuth();
  return (
    <Dialog
      title="Who has access"
      subtitle={<><span className="mono">{t.name ?? t.namespace}</span> on <span className="mono">{t.cluster}</span></>}
      width={560}
      onClose={onClose}
      footer={<>{isAdmin && <Link className="btn ghost" to="/grants">Manage grants</Link>}<button type="button" className="btn" onClick={onClose}>Close</button></>}
    >
      {loading ? <Loading what="grants" /> : error ? <ErrorBanner error={error} onRetry={onRetry} /> : rows.length === 0 ? (
        <div className="muted">No grants reach this {t.name ? 'workload' : 'namespace'}. Admins have access everywhere except protected namespaces.</div>
      ) : (
        <div className="col" style={{ gap: 0 }}>
          {rows.map((g, i) => (
            <div key={g.id} style={{ borderTop: i > 0 ? '1px solid var(--ln)' : undefined, paddingTop: i > 0 ? 10 : 0 }}>
              <div className="row wrap">
                <span className={`chip${g.holder.kind === 'team' ? ' info' : ''}`}>{g.holder.kind === 'team' ? 'Team' : 'User'}</span>
                <b>{g.holder.name}</b>
                {g.holder.kind === 'team' && <span className="muted">{g.holder.members.join(', ') || 'no members'}</span>}
              </div>
              <MiniGrantRow grant={g} borderTop={false} />
            </div>
          ))}
        </div>
      )}
    </Dialog>
  );
}
