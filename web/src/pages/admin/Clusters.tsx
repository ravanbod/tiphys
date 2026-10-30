import { useId, useMemo, useState } from 'react';
import { api, ApiError } from '../../api';
import type { ArgoStatus, ClusterView, EnvironmentOverride, EnvironmentOverrideInput } from '../../api';
import { AdminErrorBanner, ConfirmAdminDialog, FieldError, ReadOnlyNote } from '../../components/AdminPieces';
import { EnvBadge } from '../../components/Badges';
import { Dialog } from '../../components/Dialog';
import { Empty, ErrorBanner, Loading, Skeleton } from '../../components/Feedback';
import { Icon } from '../../components/Icon';
import { PageHeader } from '../../components/PageHeader';
import { Seg } from '../../components/Seg';
import { useAuth } from '../../context/auth';
import { useToast } from '../../context/toast';
import { useApi } from '../../hooks/useApi';
import { useNow } from '../../hooks/useNow';
import { adminFieldErrors } from '../../lib/adminErrorText';
import { fmtRelative, fmtSpan } from '../../lib/time';
import { useAdminMutation } from '../../lib/useAdminMutation';
import { ARGO_UI } from '../../lib/features';

const NEW_ENV = '__new__';

export function Clusters() {
  const { isAdmin, me } = useAuth();
  // Clusters always come from Secrets annotated tiphys/cluster, which the portal
  // never writes. Pasting a kubeconfig here (it is saved as a local file, dir
  // credential store only) is on just for local testing, when the server runs
  // with CLUSTER_REGISTRATION_FROM_UI.
  const CLUSTER_REGISTRATION_FROM_UI = me.settings.clusterRegistration;
  // The server checks clusters in the background; poll so the status updates by itself.
  const clusters = useApi(() => api.clusters(), [], { poll: 15_000 });
  const overrides = useApi(() => api.environmentOverrides(), []);
  const now = useNow(30_000);
  const m = useAdminMutation();
  const toast = useToast();
  const [dialog, setDialog] = useState<
    | { kind: 'add' }
    | { kind: 'replace'; cluster: string }
    | { kind: 'delete'; cluster: string }
    | { kind: 'env'; cluster: string }
    | { kind: 'override' }
    | null
  >(null);
  const [workingOn, setWorkingOn] = useState<string | null>(null);

  const list = clusters.data;
  const knownEnvs = useMemo(() => {
    const s = new Set<string>(['prod', 'staging']);
    (list ?? []).forEach((c) => c.environment && s.add(c.environment));
    (overrides.data ?? []).forEach((o) => s.add(o.environment));
    return Array.from(s).sort();
  }, [list, overrides.data]);
  const envOf = useMemo(() => new Map((list ?? []).map((c) => [c.name, c.environment])), [list]);

  const replaceRow = (v: ClusterView) =>
    clusters.setData((prev) => prev?.map((c) => (c.name === v.name ? v : c)));

  const check = async (name: string) => {
    setWorkingOn(`check:${name}`);
    const r = await m.run(() => api.checkCluster(name));
    setWorkingOn(null);
    if (r.ok) {
      replaceRow(r.value);
      toast(`${name}: ${r.value.reachable ? 'reachable' : 'unreachable'}, privilege check ${r.value.privilegeCheckOk ? 'OK' : 'failed'}`, r.value.reachable && r.value.privilegeCheckOk ? 'ok' : 'err');
    }
  };

  const setEnv = async (name: string, env: string) => {
    setWorkingOn(`env:${name}`);
    const r = await m.run(() => api.setClusterEnvironment(name, env), `${name} is now ${env}`);
    setWorkingOn(null);
    if (r.ok) {
      replaceRow(r.value);
      overrides.reload();
    }
    return r.ok;
  };

  return (
    <>
      <PageHeader
        title="Clusters & environments"
        subtitle={
          CLUSTER_REGISTRATION_FROM_UI
            ? 'Add a cluster here (local testing), or create a Secret annotated tiphys/cluster in the portal namespace.'
            : 'Clusters come from Secrets annotated tiphys/cluster in the portal namespace.'
        }
      >
        {isAdmin && CLUSTER_REGISTRATION_FROM_UI ? (
          <button type="button" className="btn pri" onClick={() => setDialog({ kind: 'add' })}>
            <Icon name="plus" />
            Add cluster
          </button>
        ) : !isAdmin ? (
          <ReadOnlyNote />
        ) : null}
      </PageHeader>

      <AdminErrorBanner error={m.error} />

      {clusters.loading && !list && <Loading what="clusters" />}
      {!!clusters.error && !list && <ErrorBanner error={clusters.error} onRetry={clusters.reload} />}

      {list && (
        <div className="card">
          <div className="tbl-wrap">
            <table className="tbl" style={{ minWidth: 900 }}>
              <thead>
                <tr>
                  <th>Cluster</th>
                  <th>Environment</th>
                  <th>Connection</th>
                  <th>Last check</th>
                  <th>Kubernetes</th>
                  <th>Privilege check</th>
                  <th>Admission</th>
                  <th />
                </tr>
              </thead>
              <tbody>
                {list.map((c) => (
                  <tr key={c.name} className={c.usable ? undefined : 'off'}>
                    <td className="mono">
                      <b>{c.name}</b>
                    </td>
                    <td>
                      {c.environmentFromSecret ? (
                        <span className="col" style={{ gap: 2 }} title="Set by the tiphys/environment annotation of the cluster's Secret">
                          <EnvBadge env={c.environment ?? 'unlabeled'} />
                          <span className="muted" style={{ fontSize: 11.5 }}>From its Secret</span>
                        </span>
                      ) : isAdmin ? (
                        <select
                          className="sel"
                          aria-label={`Environment for ${c.name}`}
                          value={c.environment ?? ''}
                          disabled={m.busy}
                          onChange={(e) => {
                            if (e.target.value === NEW_ENV) setDialog({ kind: 'env', cluster: c.name });
                            else if (e.target.value) void setEnv(c.name, e.target.value);
                          }}
                        >
                          {c.environment === null && (
                            <option value="" disabled>
                              unlabeled
                            </option>
                          )}
                          {Array.from(new Set([...knownEnvs, ...(c.environment ? [c.environment] : [])])).map((e) => (
                            <option key={e} value={e}>
                              {e}
                            </option>
                          ))}
                          <option value={NEW_ENV}>Other…</option>
                        </select>
                      ) : (
                        <EnvBadge env={c.environment ?? 'unlabeled'} />
                      )}
                    </td>
                    <td>
                      {!c.credentialLoaded ? (
                        <>
                          <span className="chip warn">No credential</span>
                          <div className="muted" style={{ fontSize: 11.5, marginTop: 2 }}>
                            Its Secret is missing or was rejected
                          </div>
                        </>
                      ) : c.reachable ? (
                        <span className="chip ok">
                          <span className="dot ok" />
                          Reachable
                        </span>
                      ) : (
                        <span className="chip err">
                          <span className="dot err" />
                          Unreachable
                        </span>
                      )}
                      {c.credentialLoaded && c.reachable && !c.usable && (
                        <div className="muted" style={{ fontSize: 11.5, marginTop: 2 }}>Not usable until checks pass</div>
                      )}
                    </td>
                    <td>{c.lastCheckAt ? fmtRelative(c.lastCheckAt, now) : 'Never'}</td>
                    <td className="mono">{c.kubernetesVersion ?? '—'}</td>
                    <td className="wrapcell">
                      {c.privilegeCheckOk ? (
                        <span className="chip ok">OK</span>
                      ) : (
                        <span className="chip err">Over-privileged</span>
                      )}
                      {c.privilegeViolations.length > 0 && (
                        <ul className="muted" style={{ margin: '4px 0 0', paddingLeft: 16, fontSize: 11.5 }}>
                          {c.privilegeViolations.map((v) => (
                            <li key={v}>{v}</li>
                          ))}
                        </ul>
                      )}
                    </td>
                    <td>
                      <span className={`chip${c.admissionMode === 'none' ? ' warn' : ''}`}>
                        {c.admissionMode === 'vap' ? 'VAP' : c.admissionMode === 'kyverno' ? 'Kyverno' : 'None'}
                      </span>
                    </td>
                    <td style={{ textAlign: 'right' }}>
                      {isAdmin && (
                        <span className="row" style={{ justifyContent: 'flex-end', gap: 4 }}>
                          <button type="button" className="btn ghost sm" disabled={m.busy} onClick={() => void check(c.name)}>
                            <Icon name="refresh" size={12} />
                            {workingOn === `check:${c.name}` ? 'Checking…' : 'Check now'}
                          </button>
                          {CLUSTER_REGISTRATION_FROM_UI && (
                            <button
                              type="button"
                              className={`btn sm${c.privilegeCheckOk ? ' ghost' : ''}`}
                              disabled={m.busy}
                              onClick={() => setDialog({ kind: 'replace', cluster: c.name })}
                            >
                              Replace credential
                            </button>
                          )}
                          <button
                            type="button"
                            className="btn ghost sm"
                            aria-label={`Delete cluster ${c.name}`}
                            disabled={m.busy}
                            onClick={() => setDialog({ kind: 'delete', cluster: c.name })}
                          >
                            <Icon name="trash" size={13} />
                          </button>
                        </span>
                      )}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
          {list.length === 0 && (
            <Empty>
              No clusters yet. {CLUSTER_REGISTRATION_FROM_UI ? 'Add one, or create' : 'Create'} a Secret annotated{' '}
              <span className="mono">tiphys/cluster: &lt;name&gt;</span> with a <span className="mono">kubeconfig</span> key in the
              portal namespace; it appears here within seconds.
            </Empty>
          )}
        </div>
      )}

      <div className="two" style={{ marginTop: 16 }}>
        <div className="col grow" style={{ flex: 1, minWidth: 0, gap: 16 }}>
          <OverridesCard
            overrides={overrides.data}
            error={overrides.error}
            loading={overrides.loading}
            reload={overrides.reload}
            envOf={envOf}
            isAdmin={isAdmin}
            onAdd={() => setDialog({ kind: 'override' })}
          />
          <ProtectedCard isAdmin={isAdmin} />
        </div>
        <div style={{ width: 360, flex: 'none' }}>
          <ArgoCard isAdmin={isAdmin} argoUrl={me.settings.argoUrl} />
        </div>
      </div>

      {dialog?.kind === 'add' && (
        <CredentialDialog
          knownEnvs={knownEnvs}
          onClose={() => setDialog(null)}
          onDone={() => {
            setDialog(null);
            clusters.reload();
          }}
        />
      )}
      {dialog?.kind === 'replace' && (
        <CredentialDialog
          knownEnvs={knownEnvs}
          cluster={dialog.cluster}
          onClose={() => setDialog(null)}
          onDone={() => {
            setDialog(null);
            clusters.reload();
          }}
        />
      )}
      {dialog?.kind === 'delete' && (
        <DeleteClusterDialog
          name={dialog.cluster}
          deletesCredential={CLUSTER_REGISTRATION_FROM_UI}
          onClose={() => setDialog(null)}
          onDone={() => {
            setDialog(null);
            clusters.reload();
            overrides.reload();
          }}
        />
      )}
      {dialog?.kind === 'env' && (
        <NewEnvDialog
          cluster={dialog.cluster}
          knownEnvs={knownEnvs}
          onSave={setEnv}
          onClose={() => setDialog(null)}
        />
      )}
      {dialog?.kind === 'override' && (
        <OverrideDialog
          knownEnvs={knownEnvs}
          onClose={() => setDialog(null)}
          onDone={() => {
            setDialog(null);
            overrides.reload();
          }}
        />
      )}
    </>
  );
}

// ---------------------------------------------------------------- overrides

function overrideScope(o: EnvironmentOverride) {
  if (o.kind === 'namespace') {
    return (
      <>
        <span className="tag">Namespace</span> <span className="mono">{o.namespace}</span>{' '}
        <span className="muted">on {o.cluster}</span>
      </>
    );
  }
  return (
    <>
      <span className="tag">Argo project</span> <span className="mono">{o.argoProject}</span>{' '}
      <span className="muted">{o.cluster ? `on ${o.cluster}` : 'on all clusters'}</span>
    </>
  );
}

function OverridesCard({
  overrides, error, loading, reload, envOf, isAdmin, onAdd,
}: {
  overrides: EnvironmentOverride[] | undefined;
  error: unknown;
  loading: boolean;
  reload: () => void;
  envOf: Map<string, string | null>;
  isAdmin: boolean;
  onAdd: () => void;
}) {
  const m = useAdminMutation();
  const [pendingDelete, setPendingDelete] = useState<EnvironmentOverride | null>(null);
  const del = async (o: EnvironmentOverride) => {
    const r = await m.run(() => api.deleteEnvironmentOverride(o.id), 'Override removed');
    if (r.ok) {
      setPendingDelete(null);
      reload();
    }
  };
  return (
    <div className="card">
      <div className="hd">
        Environment overrides <small>{ARGO_UI ? 'Per Argo project or per namespace' : 'Per namespace'}</small>
        <span className="grow" />
        {isAdmin && (
          <button type="button" className="btn sm" onClick={onAdd}>
            <Icon name="plus" />
            Add override
          </button>
        )}
      </div>
      {loading && !overrides && <Skeleton rows={2} />}
      {!!error && !overrides && <div className="bd"><ErrorBanner error={error} onRetry={reload} /></div>}
      {overrides && (
        <div className="tbl-wrap">
          <table className="tbl" style={{ minWidth: 560 }}>
            <thead>
              <tr>
                <th>Scope</th>
                <th>Cluster default</th>
                <th>Effective environment</th>
                <th />
              </tr>
            </thead>
            <tbody>
              {overrides.map((o) => {
                const def = o.cluster ? envOf.get(o.cluster) : undefined;
                const same = def === o.environment;
                return (
                  <tr key={o.id}>
                    <td>{overrideScope(o)}</td>
                    <td>{def ? <EnvBadge env={def} /> : <span className="muted">—</span>}</td>
                    <td>
                      <EnvBadge env={o.environment} /> <span className="muted">{same ? 'same as cluster' : 'overridden'}</span>
                    </td>
                    <td style={{ textAlign: 'right' }}>
                      {isAdmin && (
                        <button
                          type="button"
                          className="btn ghost sm"
                          aria-label={`Delete override ${o.namespace ?? o.argoProject ?? ''}`}
                          onClick={() => setPendingDelete(o)}
                        >
                          <Icon name="trash" size={13} />
                        </button>
                      )}
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
          {overrides.length === 0 && <Empty>No overrides. Every target uses its cluster's environment.</Empty>}
        </div>
      )}
      {pendingDelete && (
        <ConfirmAdminDialog
          title="Delete this override?"
          confirmLabel="Delete override"
          danger
          busy={m.busy}
          error={m.error}
          onConfirm={() => void del(pendingDelete)}
          onClose={() => {
            m.clear();
            setPendingDelete(null);
          }}
        >
          <div>
            {overrideScope(pendingDelete)} goes back to its cluster's environment (<b>{pendingDelete.cluster ? envOf.get(pendingDelete.cluster) ?? 'unlabeled' : 'cluster label'}</b>
            ), so time windows and holidays for <EnvBadge env={pendingDelete.environment} /> stop applying to it.
          </div>
        </ConfirmAdminDialog>
      )}
    </div>
  );
}

function EnvInput({
  id, value, onChange, knownEnvs, invalid, describedBy,
}: {
  id: string;
  value: string;
  onChange: (v: string) => void;
  knownEnvs: string[];
  invalid?: boolean;
  describedBy?: string;
}) {
  return (
    <>
      <input
        className="input mono"
        list={`${id}-list`}
        value={value}
        onChange={(e) => onChange(e.target.value)}
        aria-invalid={invalid}
        aria-describedby={describedBy}
        autoComplete="off"
      />
      <datalist id={`${id}-list`}>
        {knownEnvs.map((e) => (
          <option key={e} value={e} />
        ))}
      </datalist>
    </>
  );
}

function OverrideDialog({ knownEnvs, onClose, onDone }: { knownEnvs: string[]; onClose: () => void; onDone: () => void }) {
  const formId = useId();
  const m = useAdminMutation();
  const [kind, setKind] = useState<'namespace' | 'argo_project'>('namespace');
  const [cluster, setCluster] = useState('');
  const [namespace, setNamespace] = useState('');
  const [project, setProject] = useState('');
  const [env, setEnv] = useState('');
  const [localErr, setLocalErr] = useState<Record<string, string>>({});
  const clusters = useApi(() => api.catalogClusters(), []);
  const projects = useApi(() => api.argoProjects(), [], { enabled: ARGO_UI });
  const namespaces = useApi(() => api.catalogNamespaces(cluster), [cluster], { enabled: kind === 'namespace' && cluster !== '' });
  const fields = { ...adminFieldErrors(m.error), ...localErr };

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    const errs: Record<string, string> = {};
    if (kind === 'namespace') {
      if (!cluster) errs.cluster = 'Pick a cluster';
      if (!namespace) errs.namespace = 'Pick a namespace';
    } else if (!project) errs.argoProject = 'Pick an Argo project';
    if (!env.trim()) errs.environment = 'Environment is required';
    setLocalErr(errs);
    if (Object.keys(errs).length) return;
    const body: EnvironmentOverrideInput =
      kind === 'namespace'
        ? { kind, cluster, namespace, environment: env.trim() }
        : { kind, argoProject: project, ...(cluster ? { cluster } : {}), environment: env.trim() };
    const r = await m.run(() => api.createEnvironmentOverride(body), 'Override added');
    if (r.ok) onDone();
  };

  const clusterSel = (
    <label className="f">
      Cluster {kind === 'argo_project' && <span className="hint">(optional)</span>}
      <select
        className="sel"
        style={{ height: 30 }}
        value={cluster}
        onChange={(e) => {
          setCluster(e.target.value);
          setNamespace('');
        }}
        aria-invalid={!!fields.cluster}
      >
        <option value="">{kind === 'argo_project' ? 'All clusters' : 'Choose a cluster'}</option>
        {clusters.data?.map((c) => (
          <option key={c.name} value={c.name}>
            {c.name} ({c.environment || 'unlabeled'})
          </option>
        ))}
      </select>
      <FieldError id={`${formId}-c`} text={fields.cluster} />
    </label>
  );

  return (
    <Dialog
      title="Add environment override"
      subtitle={ARGO_UI ? "A namespace override beats an Argo project override, which beats the cluster's environment." : "A namespace override beats the cluster's environment."}
      onClose={m.busy ? () => {} : onClose}
      footer={
        <>
          <button type="button" className="btn" onClick={onClose} disabled={m.busy}>
            Cancel
          </button>
          <button type="submit" form={formId} className="btn pri" disabled={m.busy}>
            {m.busy ? 'Saving…' : 'Add override'}
          </button>
        </>
      }
    >
      <form id={formId} className="col" onSubmit={(e) => void submit(e)}>
        <div className="f">
          Applies to
          <Seg
            label="Override kind"
            value={kind}
            onChange={(k) => {
              setKind(k);
              setLocalErr({});
            }}
            options={[
              { value: 'namespace', label: 'Namespace' },
              ...(ARGO_UI ? [{ value: 'argo_project' as const, label: 'Argo project' }] : []),
            ]}
          />
        </div>
        {!!clusters.error && <ErrorBanner error={clusters.error} onRetry={clusters.reload} />}
        {clusterSel}
        {kind === 'namespace' ? (
          <label className="f">
            Namespace
            <select
              className="sel"
              style={{ height: 30 }}
              value={namespace}
              disabled={!cluster || namespaces.loading}
              onChange={(e) => setNamespace(e.target.value)}
              aria-invalid={!!fields.namespace}
            >
              <option value="">{!cluster ? 'Choose a cluster first' : namespaces.loading ? 'Loading…' : 'Choose a namespace'}</option>
              {namespaces.data?.map((n) => (
                <option key={n} value={n}>
                  {n}
                </option>
              ))}
            </select>
            <FieldError id={`${formId}-n`} text={fields.namespace} />
            {!!namespaces.error && <span className="err-text">Could not list namespaces for {cluster}.</span>}
          </label>
        ) : (
          <label className="f">
            Argo project
            <select className="sel" style={{ height: 30 }} value={project} onChange={(e) => setProject(e.target.value)} aria-invalid={!!fields.argoProject}>
              <option value="">Choose a project</option>
              {projects.data?.map((p) => (
                <option key={p} value={p}>
                  {p}
                </option>
              ))}
            </select>
            <FieldError id={`${formId}-p`} text={fields.argoProject} />
          </label>
        )}
        <label className="f">
          Environment
          <EnvInput id={`${formId}-env`} value={env} onChange={setEnv} knownEnvs={knownEnvs} invalid={!!fields.environment} />
          <FieldError id={`${formId}-e`} text={fields.environment} />
        </label>
        <AdminErrorBanner error={Object.keys(adminFieldErrors(m.error)).length ? null : m.error} />
      </form>
    </Dialog>
  );
}

// ---------------------------------------------------------------- protected namespaces

function ProtectedCard({ isAdmin }: { isAdmin: boolean }) {
  const prot = useApi(() => api.protectedNamespaces(), []);
  const m = useAdminMutation();
  const [value, setValue] = useState('');
  const [pendingRemove, setPendingRemove] = useState<string | null>(null);

  const add = async (e: React.FormEvent) => {
    e.preventDefault();
    const ns = value.trim();
    if (!ns) return;
    const r = await m.run(() => api.addProtectedNamespace(ns), `${ns} is now protected`);
    if (r.ok) {
      setValue('');
      prot.reload();
    }
  };
  const remove = async (ns: string) => {
    const r = await m.run(() => api.removeProtectedNamespace(ns), `${ns} is no longer protected`);
    if (r.ok) {
      setPendingRemove(null);
      prot.reload();
    }
  };

  return (
    <div className="card">
      <div className="hd">
        <Icon name="lock" />
        Protected namespaces <small>No grant can reach these. Grant pickers hide them.</small>
      </div>
      <div className="bd col" style={{ gap: 12 }}>
        {pendingRemove === null && <AdminErrorBanner error={m.error} />}
        {prot.loading && !prot.data && <Skeleton rows={1} />}
        {!!prot.error && !prot.data && <ErrorBanner error={prot.error} onRetry={prot.reload} />}
        <div className="row wrap">
          {prot.data?.map((ns) => (
            <span key={ns} className="chip" style={{ height: 26, padding: isAdmin ? '0 6px 0 10px' : '0 10px' }}>
              <span className="mono">{ns}</span>
              {isAdmin && (
                <button
                  type="button"
                  className="btn ghost sm"
                  style={{ padding: '0 2px', height: 20 }}
                  aria-label={`Remove ${ns}`}
                  disabled={m.busy}
                  onClick={() => setPendingRemove(ns)}
                >
                  <Icon name="x" size={12} />
                </button>
              )}
            </span>
          ))}
          {prot.data?.length === 0 && <span className="muted">No protected namespaces.</span>}
        </div>
        {isAdmin && (
          <form className="row" onSubmit={(e) => void add(e)}>
            <input
              className="input mono"
              style={{ width: 200 }}
              placeholder="Add namespace"
              aria-label="Add protected namespace"
              value={value}
              onChange={(e) => setValue(e.target.value)}
              disabled={m.busy}
            />
            <button type="submit" className="btn" disabled={m.busy || !value.trim()}>
              Add
            </button>
          </form>
        )}
      </div>
      {pendingRemove !== null && (
        <ConfirmAdminDialog
          title={`Remove protection from ${pendingRemove}?`}
          confirmLabel="Remove protection"
          danger
          busy={m.busy}
          error={m.error}
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
    </div>
  );
}

// ---------------------------------------------------------------- argo cache

function ArgoCard({ isAdmin, argoUrl }: { isAdmin: boolean; argoUrl: string }) {
  const argo = useApi<ArgoStatus>(() => api.argo(), []);
  const m = useAdminMutation();
  const refresh = async () => {
    const r = await m.run(() => api.refreshArgo(), 'Argo CD data refreshed');
    if (r.ok) argo.setData(r.value);
  };
  const a = argo.data;
  return (
    <div className="card">
      <div className="hd">
        Argo CD cache
        <span className="grow" />
        {isAdmin && (
          <button type="button" className="btn sm" disabled={m.busy} onClick={() => void refresh()}>
            <Icon name="refresh" size={12} />
            {m.busy ? 'Refreshing…' : 'Refresh now'}
          </button>
        )}
      </div>
      <div className="bd col" style={{ gap: 10 }}>
        <AdminErrorBanner error={m.error} />
        {argo.loading && !a && <Skeleton rows={2} />}
        {!!argo.error && !a && <ErrorBanner error={argo.error} onRetry={argo.reload} />}
        {a && (
          <>
            <div className="row wrap">
              <span className={`chip ${a.stale ? 'warn' : 'ok'}`}>{a.stale ? 'Stale' : 'Fresh'}</span>
              <span className="muted">
                {a.ageSeconds === null ? 'Never fetched' : `fetched ${fmtSpan(a.ageSeconds)} ago`}
              </span>
            </div>
            <dl className="kv" style={{ margin: 0 }}>
              {argoUrl && (<><dt>Dashboard</dt><dd style={{ margin: 0 }}><a className="mono" href={argoUrl} target="_blank" rel="noreferrer" style={{ wordBreak: 'break-all' }}>{argoUrl}</a></dd></>)}
              <dt>Applications</dt>
              <dd style={{ margin: 0 }}>{a.appCount}</dd>
              <dt>Projects</dt>
              <dd style={{ margin: 0 }}>{a.projectCount}</dd>
            </dl>
            {a.stale && (
              <div className="banner prod" style={{ fontSize: 12 }}>
                {ARGO_UI ? 'While stale, project-scoped grants do not match.' : 'While stale, namespace pages show old Argo app status.'}
              </div>
            )}
            {a.lastError && (
              <div className="banner red" role="alert" style={{ fontSize: 12, wordBreak: 'break-word' }}>
                Last error: {a.lastError}
              </div>
            )}
          </>
        )}
      </div>
    </div>
  );
}

// ---------------------------------------------------------------- dialogs

function NewEnvDialog({
  cluster, knownEnvs, onSave, onClose,
}: {
  cluster: string;
  knownEnvs: string[];
  onSave: (cluster: string, env: string) => Promise<boolean>;
  onClose: () => void;
}) {
  const formId = useId();
  const [env, setEnv] = useState('');
  const [busy, setBusy] = useState(false);
  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!env.trim()) return;
    setBusy(true);
    const ok = await onSave(cluster, env.trim());
    setBusy(false);
    if (ok) onClose();
  };
  return (
    <Dialog
      title={`Environment for ${cluster}`}
      subtitle="Environment names are free text, for example prod or staging."
      width={420}
      onClose={busy ? () => {} : onClose}
      footer={
        <>
          <button type="button" className="btn" onClick={onClose} disabled={busy}>
            Cancel
          </button>
          <button type="submit" form={formId} className="btn pri" disabled={busy || !env.trim()}>
            {busy ? 'Saving…' : 'Save'}
          </button>
        </>
      }
    >
      <form id={formId} className="col" onSubmit={(e) => void submit(e)}>
        <label className="f">
          Environment
          <EnvInput id={formId} value={env} onChange={setEnv} knownEnvs={knownEnvs} />
        </label>
      </form>
    </Dialog>
  );
}

function CredentialDialog({
  cluster, knownEnvs, onClose, onDone,
}: {
  /** Set for "Replace credential"; omitted for "Add cluster". */
  cluster?: string;
  knownEnvs: string[];
  onClose: () => void;
  onDone: () => void;
}) {
  const formId = useId();
  const m = useAdminMutation();
  const [name, setName] = useState('');
  const [env, setEnv] = useState('');
  const [kubeconfig, setKubeconfig] = useState('');
  const [localErr, setLocalErr] = useState<Record<string, string>>({});
  const err = m.error instanceof ApiError ? m.error : null;
  const violations = err?.violations ?? [];
  const fields = { ...adminFieldErrors(m.error), ...localErr };
  const hasFieldOrViolation = Object.keys(adminFieldErrors(m.error)).length > 0 || violations.length > 0;

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    const errs: Record<string, string> = {};
    if (!cluster && !name.trim()) errs.name = 'Cluster name is required';
    if (!kubeconfig.trim()) errs.kubeconfig = 'Paste the kubeconfig of the portal ServiceAccount';
    setLocalErr(errs);
    if (Object.keys(errs).length) return;
    const r = await m.run(
      () =>
        cluster
          ? api.replaceCredential(cluster, kubeconfig)
          : api.addCluster({ name: name.trim(), ...(env.trim() ? { environment: env.trim() } : {}), kubeconfig }),
      cluster ? `Credential for ${cluster} replaced` : `Cluster ${name.trim()} added`,
    );
    // The token must not stay in memory/DOM once saved.
    if (r.ok) {
      setKubeconfig('');
      onDone();
    }
  };

  return (
    <Dialog
      title={cluster ? `Replace credential for ${cluster}` : 'Add cluster'}
      subtitle={cluster ? 'Rotate the stored ServiceAccount token.' : undefined}
      width={560}
      onClose={m.busy ? () => {} : onClose}
      footer={
        <>
          <button type="button" className="btn" onClick={onClose} disabled={m.busy}>
            Cancel
          </button>
          <button type="submit" form={formId} className="btn pri" disabled={m.busy}>
            {m.busy ? 'Validating…' : 'Validate & save'}
          </button>
        </>
      }
    >
      <form id={formId} className="col" onSubmit={(e) => void submit(e)}>
        <div className="banner prod" style={{ alignItems: 'flex-start', fontSize: 12 }}>
          <span>
            <b>Install the per-cluster RBAC chart first.</b> It creates the limited portal ServiceAccount whose kubeconfig you paste below.
          </span>
        </div>
        {!cluster && (
          <div className="row wrap" style={{ alignItems: 'flex-start', gap: 12 }}>
            <label className="f" style={{ flex: 1, minWidth: 180 }}>
              Cluster name
              <input
                className="input mono"
                data-autofocus
                value={name}
                onChange={(e) => setName(e.target.value)}
                aria-invalid={!!fields.name}
                autoComplete="off"
              />
              <FieldError id={`${formId}-n`} text={fields.name} />
            </label>
            <label className="f" style={{ flex: 1, minWidth: 160 }}>
              Environment <span className="hint">(optional)</span>
              <EnvInput id={formId} value={env} onChange={setEnv} knownEnvs={knownEnvs} invalid={!!fields.environment} />
              <FieldError id={`${formId}-e`} text={fields.environment} />
            </label>
          </div>
        )}
        <label className="f">
          Kubeconfig of the portal ServiceAccount
          <textarea
            className="input mono"
            rows={7}
            style={{ height: 'auto', padding: 8, fontSize: 11.5, lineHeight: 1.5 }}
            value={kubeconfig}
            onChange={(e) => setKubeconfig(e.target.value)}
            placeholder={'apiVersion: v1\nkind: Config\nclusters: …'}
            autoComplete="off"
            autoCapitalize="off"
            spellCheck={false}
            aria-invalid={!!fields.kubeconfig}
          />
          <FieldError id={`${formId}-k`} text={fields.kubeconfig} />
        </label>
        {violations.length > 0 && (
          <div className="col" style={{ gap: 0, border: '1px solid var(--ln)', borderRadius: 6 }} role="alert">
            <div className="row" style={{ padding: '8px 12px', alignItems: 'flex-start' }}>
              <span className="chip err" style={{ padding: '0 5px', marginTop: 1 }}>
                <Icon name="x" size={12} />
              </span>
              <div>
                <b>Refused: too much power</b>
                <div className="muted" style={{ fontSize: 12 }}>
                  The credential can do more than the portal needs. Use the ServiceAccount created by the RBAC chart.
                </div>
              </div>
            </div>
            {violations.map((v) => (
              <div key={v} className="row" style={{ padding: '8px 12px', borderTop: '1px solid var(--ln)', alignItems: 'flex-start' }}>
                <span className="chip err" style={{ padding: '0 5px', marginTop: 1 }}>
                  <Icon name="x" size={12} />
                </span>
                <span className="mono" style={{ wordBreak: 'break-word' }}>{v}</span>
              </div>
            ))}
          </div>
        )}
        <AdminErrorBanner error={hasFieldOrViolation ? null : m.error} />
        <div className="muted" style={{ fontSize: 12 }}>
          After saving, the token is never shown again. To rotate it later, use Replace credential.
        </div>
      </form>
    </Dialog>
  );
}

function DeleteClusterDialog({
  name,
  deletesCredential,
  onClose,
  onDone,
}: {
  name: string;
  /** True when the portal manages credentials (registration from the UI), so its Secret goes too. */
  deletesCredential: boolean;
  onClose: () => void;
  onDone: () => void;
}) {
  const m = useAdminMutation();
  const go = async () => {
    const r = await m.run(() => api.deleteCluster(name), `Cluster ${name} deleted`);
    if (r.ok) onDone();
  };
  return (
    <ConfirmAdminDialog
      title={`Delete cluster ${name}?`}
      confirmLabel="Delete cluster"
      danger
      busy={m.busy}
      error={m.error}
      onConfirm={() => void go()}
      onClose={onClose}
    >
      <div className="banner prod" style={{ fontSize: 12 }}>
        {deletesCredential ? (
          <>
            The stored credential is deleted and Tiphys stops reaching <span className="mono">{name}</span>.
          </>
        ) : (
          <>
            <span className="mono">{name}</span> is removed from Tiphys, with its environment label. Its credential Secret must be
            deleted first, otherwise the cluster is registered again.
          </>
        )}
      </div>
    </ConfirmAdminDialog>
  );
}
