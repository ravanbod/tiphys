import { useId, useState } from 'react';
import { api } from '../../api';
import { AdminErrorBanner, ConfirmAdminDialog, FieldError } from '../../components/AdminPieces';
import { ErrorBanner, Skeleton } from '../../components/Feedback';
import { Icon } from '../../components/Icon';
import type { IconName } from '../../components/Icon';
import { useApi } from '../../hooks/useApi';
import { adminFieldErrors } from '../../lib/adminErrorText';
import { checkSyncClusterKind, MAX_SYNC_CLUSTER_KINDS } from '../../lib/syncClusterKinds';
import { useAdminMutation } from '../../lib/useAdminMutation';

/**
 * Settings lists that apply at once (not through the page's Save): a chip
 * per entry, removable by admins, and a field to add one.
 */
function ChipListCard({ icon, title, note, items, loading, error, onRetry, empty, isAdmin, busy, mutationError, addLabel, placeholder, addError, onAdd, onRemove, children }: {
  icon: IconName; title: string; note: string; items: string[] | undefined; loading: boolean; error: unknown; onRetry: () => void;
  empty: string; isAdmin: boolean; busy: boolean; mutationError: unknown; addLabel: string; placeholder: string; addError?: string;
  onAdd: (v: string) => void; onRemove: (v: string) => void; children?: React.ReactNode;
}) {
  const id = useId();
  const [value, setValue] = useState('');
  const submit = (e: React.FormEvent) => {
    e.preventDefault();
    const v = value.trim();
    if (v) onAdd(v);
  };
  return (
    <div className="card">
      <div className="hd">
        <Icon name={icon} />
        {title} <small>{note}</small>
      </div>
      <div className="bd col" style={{ gap: 12 }}>
        <AdminErrorBanner error={mutationError} />
        {loading && !items && <Skeleton rows={1} />}
        {!!error && !items && <ErrorBanner error={error} onRetry={onRetry} />}
        <div className="row wrap">
          {items?.map((v) => (
            <span key={v} className="chip" style={{ height: 26, padding: isAdmin ? '0 6px 0 10px' : '0 10px' }}>
              <span className="mono">{v}</span>
              {isAdmin && (
                <button type="button" className="btn ghost sm" style={{ padding: '0 2px', height: 20 }} aria-label={`Remove ${v}`} disabled={busy} onClick={() => onRemove(v)}>
                  <Icon name="x" size={12} />
                </button>
              )}
            </span>
          ))}
          {items?.length === 0 && <span className="muted">{empty}</span>}
        </div>
        {isAdmin && (
          <form className="col" style={{ gap: 4 }} onSubmit={submit}>
            <div className="row">
              <input
                className="input mono" style={{ width: 320, maxWidth: '100%' }} placeholder={placeholder} aria-label={addLabel}
                value={value} onChange={(e) => setValue(e.target.value)} disabled={busy}
                aria-invalid={!!addError} aria-describedby={addError ? `${id}-e` : undefined}
              />
              <button type="submit" className="btn" disabled={busy || !value.trim()}>Add</button>
            </div>
            <FieldError id={`${id}-e`} text={addError} />
          </form>
        )}
        {children}
      </div>
    </div>
  );
}

/** Namespaces no grant can reach; removing protection asks first. */
export function ProtectedNamespacesCard({ isAdmin }: { isAdmin: boolean }) {
  const prot = useApi(() => api.protectedNamespaces(), []);
  const m = useAdminMutation();
  const [pendingRemove, setPendingRemove] = useState<string | null>(null);

  const add = async (ns: string) => {
    const r = await m.run(() => api.addProtectedNamespace(ns), `${ns} is now protected`);
    if (r.ok) prot.reload();
  };
  const remove = async (ns: string) => {
    const r = await m.run(() => api.removeProtectedNamespace(ns), `${ns} is no longer protected`);
    if (r.ok) {
      setPendingRemove(null);
      prot.reload();
    }
  };

  return (
    <ChipListCard
      icon="lock" title="Protected namespaces" note="No grant can reach these. Grant pickers hide them."
      items={prot.data} loading={prot.loading} error={prot.error} onRetry={prot.reload} empty="No protected namespaces."
      isAdmin={isAdmin} busy={m.busy} mutationError={pendingRemove === null ? m.error : null}
      addLabel="Add protected namespace" placeholder="Add namespace" onAdd={(ns) => void add(ns)} onRemove={setPendingRemove}
    >
      {pendingRemove !== null && (
        <ConfirmAdminDialog
          title={`Remove protection from ${pendingRemove}?`} confirmLabel="Remove protection" danger busy={m.busy} error={m.error}
          onConfirm={() => void remove(pendingRemove)}
          onClose={() => {
            m.clear();
            setPendingRemove(null);
          }}
        >
          <div className="banner prod" style={{ fontSize: 12 }}>
            Grants can then be created for <span className="mono">{pendingRemove}</span> and its workloads become visible in pickers.
          </div>
        </ConfirmAdminDialog>
      )}
    </ChipListCard>
  );
}

/**
 * Cluster-scoped kinds whose resources do not leave syncing an Argo CD app to
 * admins (settings.argoSyncClusterKinds). Each change saves the list on its
 * own, from the settings as they are on the server, so it never carries the
 * page's unsaved edits; the page's Save leaves the list out, which keeps it.
 * Allowing a kind widens access, so adding asks first.
 */
export function SyncKindsCard({ isAdmin }: { isAdmin: boolean }) {
  const settings = useApi(() => api.settings(), []);
  const m = useAdminMutation();
  const [pendingAdd, setPendingAdd] = useState<string | null>(null);
  const [addError, setAddError] = useState<string | undefined>();
  const kinds = settings.data?.argoSyncClusterKinds;

  const save = async (next: string[], done: string) => {
    const r = await m.run(async () => {
      const fresh = await api.settings();
      return api.saveSettings({ ...fresh, argoSyncClusterKinds: next });
    }, done);
    if (r.ok) settings.setData(r.value);
    return r.ok;
  };
  const tryAdd = (kind: string) => {
    m.clear();
    const msg = checkSyncClusterKind(kind)
      || (kinds?.includes(kind) ? 'already listed' : '')
      || ((kinds?.length ?? 0) >= MAX_SYNC_CLUSTER_KINDS ? `at most ${MAX_SYNC_CLUSTER_KINDS} kinds` : '');
    setAddError(msg || undefined);
    if (!msg) setPendingAdd(kind);
  };
  const serverField = adminFieldErrors(m.error).argoSyncClusterKinds;

  return (
    <ChipListCard
      icon="refresh" title="Argo CD sync" note="Cluster-scoped kinds that do not leave syncing an app to admins."
      items={kinds} loading={settings.loading} error={settings.error} onRetry={settings.reload} empty="None: apps with cluster-scoped resources are synced by admins."
      isAdmin={isAdmin} busy={m.busy} mutationError={pendingAdd === null && !serverField ? m.error : null}
      addLabel="Allow a cluster-scoped kind" placeholder="Kind.group, e.g. ClusterInput.fluentbit.fluent.io"
      addError={addError ?? (pendingAdd === null ? serverField : undefined)}
      onAdd={tryAdd}
      onRemove={(kind) => void save((kinds ?? []).filter((k) => k !== kind), `${kind} needs an admin again`)}
    >
      <span className="hint">
        Core kinds (Namespace, PersistentVolume, ...), RBAC, admission, CRDs, API services, StorageClass and PriorityClass are never allowed.
      </span>
      {pendingAdd !== null && (
        <ConfirmAdminDialog
          title={`Let users sync ${pendingAdd}?`} confirmLabel="Allow" busy={m.busy} error={m.error}
          onConfirm={() => void save([...(kinds ?? []), pendingAdd], `${pendingAdd} is allowed for sync`).then((ok) => ok && setPendingAdd(null))}
          onClose={() => {
            m.clear();
            setPendingAdd(null);
          }}
        >
          <div className="banner prod" style={{ fontSize: 12 }}>
            Users who hold sync on every namespace of an app can then sync it with resources of this kind. Anyone who can push to the
            app's repository can add such resources, and they apply to the whole cluster.
          </div>
        </ConfirmAdminDialog>
      )}
    </ChipListCard>
  );
}
