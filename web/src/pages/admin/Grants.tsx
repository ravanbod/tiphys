import { useEffect, useId, useMemo, useState } from 'react';
import { ApiError, api } from '../../api';
import type {
  AccessCheckInput, Action, CatalogCluster, DecisionExplain, GrantFilters, GrantView, HolidayView, TeamView,
  TimeWindowView, UserView, WorkloadKind,
} from '../../api';
import { FieldError, ReadOnlyNote } from '../../components/AdminBits';
import '../../components/adminPages.css';
import { ActionChips, EnvBadge } from '../../components/Badges';
import { Dialog } from '../../components/Dialog';
import { Empty, ErrorBanner, Skeleton } from '../../components/Feedback';
import { Icon } from '../../components/Icon';
import { PageHeader } from '../../components/PageHeader';
import { SCOPE_KIND_LABEL, ScopeCell, scopeCluster, scopeName } from '../../components/Scope';
import { Seg } from '../../components/Seg';
import { useAuth } from '../../context/auth';
import { useToast } from '../../context/toast';
import { useApi } from '../../hooks/useApi';
import { buildSteps, verdict } from '../../lib/accessCheckSteps';
import type { CheckStep } from '../../lib/accessCheckSteps';
import { ADMIN_TZ, localInputToIso } from '../../lib/adminDatetime';
import { fieldError, unmatchedFields } from '../../lib/adminFields';
import {
  ACTIONS, SYNC_SCOPE_HINT, actionOptionLabel, actionPhrase, emptyGrantForm, formFromGrant, grantInputFromForm, parseWlKey, validateGrantForm, wlKey,
} from '../../lib/grantForm';
import type { GrantForm } from '../../lib/grantForm';
import { fmtDateTime, fmtDay, fmtSpan } from '../../lib/time';
import { ARGO_UI } from '../../lib/features';

const SCOPE_KINDS: GrantForm['scopeKind'][] = ARGO_UI ? ['argo_project', 'namespace', 'workloads'] : ['namespace', 'workloads'];
const FORM_FIELDS = ['holder', 'scope', 'argoProject', 'cluster', 'namespace', 'workloads', 'actions', 'expiresAt', 'note'];

function uniq(xs: (string | undefined | null)[]): string[] {
  return Array.from(new Set(xs.filter((x): x is string => !!x))).sort();
}

function ExpiryCell({ g, now }: { g: GrantView; now: number }) {
  if (!g.expiresAt) return <span className="muted">None</span>;
  if (g.expired) return <span>Expired {fmtDay(g.expiresAt, ADMIN_TZ)}</span>;
  const secs = (Date.parse(g.expiresAt) - now) / 1000;
  if (secs <= 14 * 86400) return <span className="chip warn">Expires in {fmtSpan(secs)}</span>;
  return <span>{fmtDateTime(g.expiresAt, ADMIN_TZ)}</span>;
}

function HolderCell({ g }: { g: GrantView }) {
  return (
    <span>
      <span className={`chip${g.holder.kind === 'team' ? ' info' : ''}`}>{g.holder.kind === 'team' ? 'Team' : 'User'}</span>{' '}
      <b>{g.holder.name}</b>
    </span>
  );
}

/* ------------------------------------------------------------------ editor */

interface Pickers {
  teams: TeamView[];
  users: UserView[];
  clusters: CatalogCluster[];
  argoProjects: string[];
}

function GrantEditor({
  form, setForm, pickers, onSaved, onDeleted, onCancel,
}: {
  form: GrantForm;
  setForm: (f: GrantForm) => void;
  pickers: Pickers;
  onSaved: (g: GrantView) => void;
  onDeleted: () => void;
  onCancel: () => void;
}) {
  const toast = useToast();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const [serverFields, setServerFields] = useState<Record<string, string>>({});
  const [localFields, setLocalFields] = useState<Record<string, string>>({});
  const [confirmDelete, setConfirmDelete] = useState(false);
  const formDomId = useId();
  const fields = { ...serverFields, ...localFields };

  const set = (patch: Partial<GrantForm>) => {
    setForm({ ...form, ...patch });
    setLocalFields({});
  };

  // A different grant loaded into the editor starts clean.
  const formId = form.id;
  useEffect(() => {
    setError(null);
    setServerFields({});
    setLocalFields({});
  }, [formId]);

  const needsNs = form.scopeKind !== 'argo_project';
  const namespaces = useApi(() => api.catalogNamespaces(form.cluster), [form.cluster], { enabled: needsNs && !!form.cluster });
  const workloads = useApi(
    () => api.catalogWorkloads(form.cluster, form.namespace),
    [form.cluster, form.namespace],
    { enabled: form.scopeKind === 'workloads' && !!form.cluster && !!form.namespace },
  );

  const nsOptions = uniq([...(namespaces.data ?? []), form.namespace]);
  const wlOptions = useMemo(() => {
    const byKey = new Map<string, { kind: WorkloadKind; name: string }>();
    for (const w of workloads.data ?? []) byKey.set(wlKey(w), w);
    for (const k of form.workloads) if (!byKey.has(k)) byKey.set(k, parseWlKey(k));
    return Array.from(byKey.values());
  }, [workloads.data, form.workloads]);

  const holderList = form.holderKind === 'team' ? pickers.teams : pickers.users;
  const holderName =
    form.holderKind === 'team'
      ? pickers.teams.find((t) => t.id === form.holderId)?.name
      : pickers.users.find((u) => u.id === form.holderId)?.displayName;
  const clusterEnv = pickers.clusters.find((c) => c.name === form.cluster)?.environment;

  const toggleAction = (a: Action) =>
    set({ actions: form.actions.includes(a) ? form.actions.filter((x) => x !== a) : [...form.actions, a] });
  const toggleWl = (k: string) =>
    set({ workloads: form.workloads.includes(k) ? form.workloads.filter((x) => x !== k) : [...form.workloads, k] });

  const save = async () => {
    const errs = validateGrantForm(form, Date.now());
    setLocalFields(errs);
    setServerFields({});
    setError(null);
    if (Object.keys(errs).length > 0) return;
    setBusy(true);
    try {
      const input = grantInputFromForm(form);
      const saved = form.id === null ? await api.createGrant(input) : await api.updateGrant(form.id, input);
      toast(form.id === null ? 'Grant created.' : 'Grant saved.');
      onSaved(saved);
    } catch (e) {
      setError(e);
      if (e instanceof ApiError) setServerFields(e.fields);
    } finally {
      setBusy(false);
    }
  };

  const remove = async () => {
    if (form.id === null) return;
    setBusy(true);
    try {
      await api.deleteGrant(form.id);
      toast('Grant deleted.');
      setConfirmDelete(false);
      onDeleted();
    } catch (e) {
      setConfirmDelete(false);
      setError(e);
    } finally {
      setBusy(false);
    }
  };

  const leftover = unmatchedFields(fields, FORM_FIELDS);
  const sentence = (
    <span style={{ fontSize: 13 }}>
      {form.holderKind === 'team' ? 'Team' : 'User'} <b>{holderName ?? '…'}</b> can {actionPhrase(form.actions)} for{' '}
      {form.scopeKind === 'argo_project' && (
        <>every namespace of Argo project <span className="mono">{form.argoProject || '…'}</span> on all clusters</>
      )}
      {form.scopeKind === 'namespace' && (
        <>namespace <span className="mono">{form.namespace || '…'}</span> on cluster <span className="mono">{form.cluster || '…'}</span></>
      )}
      {form.scopeKind === 'workloads' && (
        <>
          {form.workloads.length} workload{form.workloads.length === 1 ? '' : 's'}
          {form.workloads.length > 0 && (
            <> ({form.workloads.map((k, i) => (
              <span key={k}>{i > 0 && ', '}<span className="mono">{parseWlKey(k).name}</span></span>
            ))})</>
          )}{' '}
          in namespace <span className="mono">{form.namespace || '…'}</span> on cluster <span className="mono">{form.cluster || '…'}</span>
        </>
      )}
      {form.expiryMode === 'date' && localInputToIso(form.expiry)
        ? <>, until {fmtDateTime(localInputToIso(form.expiry), ADMIN_TZ)} ({ADMIN_TZ}).</>
        : <>, with no expiry.</>}
    </span>
  );

  return (
    <Dialog
      title={form.id === null ? 'New grant' : `Edit grant #${form.id}`}
      width={680}
      closeOnBackdrop={false}
      onClose={() => { if (!busy) onCancel(); }}
      footer={
        <>
          {form.id !== null && (
            <button type="button" className="btn" style={{ marginRight: 'auto', color: 'var(--red)' }} disabled={busy} onClick={() => setConfirmDelete(true)}>
              <Icon name="trash" />
              Delete
            </button>
          )}
          <button type="button" className="btn" onClick={onCancel} disabled={busy}>Cancel</button>
          <button type="submit" form={formDomId} className="btn pri" disabled={busy}>
            {busy ? 'Saving…' : form.id === null ? 'Create grant' : 'Save grant'}
          </button>
        </>
      }
    >
      <form
        id={formDomId}
        className="col"
        style={{ gap: 16 }}
        onSubmit={(e) => { e.preventDefault(); void save(); }}
        noValidate
      >
        <div className="f" role="group" aria-label="Holder">
          1 · Holder
          <div className="row">
            <Seg
              label="Holder kind"
              value={form.holderKind}
              onChange={(v) => set({ holderKind: v, holderId: null })}
              options={[{ value: 'team', label: 'Team' }, { value: 'user', label: 'User' }]}
            />
            <select
              className="sel"
              style={{ height: 30, flex: 1 }}
              aria-label={form.holderKind === 'team' ? 'Team' : 'User'}
              aria-invalid={fields.holder ? true : undefined}
              value={form.holderId ?? ''}
              onChange={(e) => set({ holderId: e.target.value ? Number(e.target.value) : null })}
            >
              <option value="">Choose a {form.holderKind}…</option>
              {holderList.map((h) =>
                'members' in h ? (
                  <option key={h.id} value={h.id}>{h.name} ({h.members.length} members)</option>
                ) : (
                  <option key={h.id} value={h.id}>{h.displayName} ({h.username}){h.disabled ? ' · disabled' : ''}</option>
                ),
              )}
            </select>
          </div>
          <FieldError msg={fieldError(fields, 'holder')} />
        </div>

        <div className="f" role="group" aria-label="Scope kind">
          2 · Scope kind
          <Seg
            label="Scope kind"
            value={form.scopeKind}
            onChange={(v) => set({ scopeKind: v })}
            style={{ alignSelf: 'flex-start' }}
            options={SCOPE_KINDS.map((k) => ({ value: k, label: SCOPE_KIND_LABEL[k] }))}
          />
          <FieldError msg={fieldError(fields, 'scope.kind', 'scopeKind')} />
        </div>

        <div className="f" role="group" aria-label="Where">
          <span>3 · Where <span className="hint">{form.scopeKind === 'argo_project' ? 'Argo project (all clusters)' : form.scopeKind === 'namespace' ? 'Cluster → namespace' : 'Cluster → namespace → workloads'}</span></span>
          {form.scopeKind === 'argo_project' ? (
            <>
              <select
                className="sel"
                style={{ height: 30 }}
                aria-label="Argo project"
                aria-invalid={fields.argoProject ? true : undefined}
                value={form.argoProject}
                onChange={(e) => set({ argoProject: e.target.value })}
              >
                <option value="">Choose an Argo project…</option>
                {uniq([...pickers.argoProjects, form.argoProject]).map((p) => (
                  <option key={p} value={p}>{p}</option>
                ))}
              </select>
              <FieldError msg={fieldError(fields, 'argoProject', 'scope.argoProject')} />
            </>
          ) : (
            <>
              <div className="row" style={{ gap: 8 }}>
                <select
                  className="sel"
                  style={{ height: 30, flex: 1 }}
                  aria-label="Cluster"
                  aria-invalid={fields.cluster ? true : undefined}
                  value={form.cluster}
                  onChange={(e) => set({ cluster: e.target.value, namespace: '', workloads: [] })}
                >
                  <option value="">Cluster…</option>
                  {uniq([...pickers.clusters.map((c) => c.name), form.cluster]).map((c) => (
                    <option key={c} value={c}>{c}</option>
                  ))}
                </select>
                <Icon name="chev" className="muted" />
                <select
                  className="sel"
                  style={{ height: 30, flex: 1 }}
                  aria-label="Namespace"
                  aria-invalid={fields.namespace ? true : undefined}
                  disabled={!form.cluster}
                  value={form.namespace}
                  onChange={(e) => set({ namespace: e.target.value, workloads: [] })}
                >
                  <option value="">{namespaces.loading ? 'Loading…' : 'Namespace…'}</option>
                  {nsOptions.map((n) => (
                    <option key={n} value={n}>{n}</option>
                  ))}
                </select>
              </div>
              {clusterEnv && (
                <span className="row" style={{ gap: 6, fontWeight: 400 }}>
                  Environment <EnvBadge env={clusterEnv} />
                </span>
              )}
              <FieldError msg={fieldError(fields, 'cluster', 'scope.cluster')} />
              <FieldError msg={fieldError(fields, 'namespace', 'scope.namespace')} />
              {namespaces.error ? <ErrorBanner error={namespaces.error} onRetry={namespaces.reload} /> : null}
              {form.scopeKind === 'workloads' && form.namespace && (
                <>
                  <div className="pickbox" role="group" aria-label="Workloads">
                    {workloads.loading && wlOptions.length === 0 && <div style={{ padding: 8 }}><Skeleton rows={2} /></div>}
                    {!workloads.loading && wlOptions.length === 0 && !workloads.error && (
                      <div className="muted" style={{ padding: '8px 10px' }}>No workloads in this namespace.</div>
                    )}
                    {wlOptions.map((w) => (
                      <label key={wlKey(w)} className="chk">
                        <input type="checkbox" checked={form.workloads.includes(wlKey(w))} onChange={() => toggleWl(wlKey(w))} />
                        <span className="tag">{w.kind}</span>
                        <span className="mono">{w.name}</span>
                      </label>
                    ))}
                  </div>
                  {workloads.error ? <ErrorBanner error={workloads.error} onRetry={workloads.reload} /> : null}
                  <FieldError msg={fieldError(fields, 'workloads', 'scope.workloads')} />
                </>
              )}
            </>
          )}
          <span className="hint row" style={{ gap: 6 }}>
            <Icon name="lock" size={13} />
            Protected namespaces (kube-system, argocd) are hidden from pickers.
          </span>
        </div>

        <div className="f" role="group" aria-label="Actions">
          4 · Actions
          <div className="row wrap" style={{ gap: 16 }}>
            {ACTIONS.map((a) => {
              const off = a === 'sync' && form.scopeKind === 'workloads' && !form.actions.includes(a);
              return (
                <label key={a} className="chk" title={off ? SYNC_SCOPE_HINT : undefined}>
                  <input type="checkbox" checked={form.actions.includes(a)} disabled={off} onChange={() => toggleAction(a)} />
                  {actionOptionLabel(a)}
                  {a === 'logs' && <span className="muted">(events, pod status)</span>}
                </label>
              );
            })}
          </div>
          <FieldError msg={fieldError(fields, 'actions')} />
        </div>

        <div className="f" role="group" aria-label="Expiry">
          5 · Expiry
          <div className="row wrap">
            <Seg
              label="Expiry"
              value={form.expiryMode}
              onChange={(v) => set({ expiryMode: v })}
              options={[{ value: 'none', label: 'None' }, { value: 'date', label: 'Date & time' }]}
            />
            {form.expiryMode === 'date' && (
              <>
                <input
                  className="input"
                  type="datetime-local"
                  style={{ width: 220 }}
                  aria-label="Expiry date and time"
                  aria-invalid={fields.expiresAt ? true : undefined}
                  value={form.expiry}
                  onChange={(e) => set({ expiry: e.target.value })}
                />
                <span className="muted">{ADMIN_TZ}</span>
              </>
            )}
          </div>
          <FieldError msg={fieldError(fields, 'expiresAt', 'expiry')} />
        </div>

        <label className="f">
          <span>6 · Note <span className="hint">Optional</span></span>
          <input className="input" value={form.note} onChange={(e) => set({ note: e.target.value })} />
          <FieldError msg={fieldError(fields, 'note')} />
        </label>

        <div className="banner info" style={{ alignItems: 'flex-start', flexDirection: 'column', gap: 2 }}>
          <span className="lbl" style={{ color: 'var(--ac)' }}>This grant means</span>
          {sentence}
        </div>

        {leftover.length > 0 && (
          <div className="banner red" role="alert" style={{ flexDirection: 'column', alignItems: 'flex-start' }}>
            {leftover.map(([k, m]) => (
              <span key={k}><b>{k}</b>: {m}</span>
            ))}
          </div>
        )}
        {error !== null && Object.keys(serverFields).length === 0 && <ErrorBanner error={error} />}
      </form>
      {confirmDelete && (
        <Dialog
          title={`Delete grant #${form.id}?`}
          subtitle="The holder loses this access immediately; open streams that depend on it end within 30 seconds."
          onClose={() => setConfirmDelete(false)}
          footer={
            <>
              <button type="button" className="btn" onClick={() => setConfirmDelete(false)} disabled={busy}>Cancel</button>
              <button type="button" className="btn dng" onClick={() => void remove()} disabled={busy}>
                {busy ? 'Deleting…' : 'Delete grant'}
              </button>
            </>
          }
        >
          <div className="banner info" style={{ alignItems: 'flex-start' }}>{sentence}</div>
        </Dialog>
      )}
    </Dialog>
  );
}

/* ----------------------------------------------------------- access checker */

interface CheckResult {
  d: DecisionExplain;
  now: number;
  rules: TimeWindowView[] | null;
  holidays: HolidayView[] | null;
}

function StepMark({ s }: { s: CheckStep }) {
  if (s.tone === 'ok') return <span className="chip ok" aria-label="passed"><Icon name="check" size={12} /></span>;
  if (s.tone === 'err') return <span className="chip err" aria-label="failed"><Icon name="x" size={12} /></span>;
  if (s.tone === 'info') return <span className="chip info" aria-label="break-glass">BG</span>;
  return <span className="chip" aria-label={s.tone === 'skip' ? 'not evaluated' : 'information'}>—</span>;
}

function CheckResultView({ r, onEdit }: { r: CheckResult; onEdit: (g: GrantView) => void }) {
  const { d, now } = r;
  const v = verdict(d, now, ADMIN_TZ);
  const steps = buildSteps(d, now, { tz: ADMIN_TZ, rules: r.rules, holidays: r.holidays });
  return (
    <div className="col" style={{ gap: 14 }} aria-live="polite">
      <div
        className="row"
        style={{ padding: '12px 14px', borderRadius: 8, background: v.allowed ? 'var(--grnbg)' : 'var(--redbg)', gap: 12, alignItems: 'flex-start' }}
      >
        <span className={`chip ${v.allowed ? 'ok' : 'err'}`} style={{ height: 24, fontSize: 12, fontWeight: 600 }}>{v.chip}</span>
        <span style={{ color: v.allowed ? 'var(--grn)' : 'var(--red)' }}>{v.text}</span>
      </div>
      <div className="col" style={{ gap: 0 }}>
        {steps.map((s) => (
          <div key={s.id} className={`step${s.tone === 'skip' ? ' skip' : ''}`}>
            <StepMark s={s} />
            <div style={{ minWidth: 0, flex: 1 }}>
              <div className="t">{s.title}</div>
              {s.grants && s.grants.length > 0 ? (
                <div className="col" style={{ gap: 4, marginTop: 2 }}>
                  {s.grants.map((g) => (
                    <div key={g.id} className="row wrap d" style={{ gap: 6 }}>
                      <span>{g.holder.kind === 'team' ? 'Team' : 'User'} {g.holder.name} →</span>
                      <ScopeCell scope={g.scope} />
                      <ActionChips actions={g.actions} />
                      {g.expired && <span className="chip warn">expired</span>}
                      <button type="button" className="linklike blue" onClick={() => onEdit(g)}>Edit #{g.id}</button>
                    </div>
                  ))}
                </div>
              ) : (
                <div className="d">{s.detail}</div>
              )}
            </div>
          </div>
        ))}
      </div>
      <div className="muted" style={{ fontSize: 12 }}>
        Checked {fmtDateTime(d.evaluatedAt, ADMIN_TZ)} ({ADMIN_TZ}) for <b>{d.action}</b> on{' '}
        <span className="mono">{d.cluster}/{d.namespace}{d.workload ? `/${d.workload}` : ''}</span>
        {d.environment && <> · <EnvBadge env={d.environment} /></>}
      </div>
    </div>
  );
}

// Stays mounted while closed, so the last check is still there when reopened.
function AccessChecker({
  open, onClose, users, clusters, onEdit,
}: { open: boolean; onClose: () => void; users: UserView[]; clusters: CatalogCluster[]; onEdit: (g: GrantView) => void }) {
  const [userId, setUserId] = useState('');
  const [cluster, setCluster] = useState('');
  const [namespace, setNamespace] = useState('');
  const [workload, setWorkload] = useState('');
  const [action, setAction] = useState<Action>('exec');
  const [at, setAt] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const [result, setResult] = useState<CheckResult | null>(null);

  const namespaces = useApi(() => api.catalogNamespaces(cluster), [cluster], { enabled: !!cluster });
  const workloads = useApi(() => api.catalogWorkloads(cluster, namespace), [cluster, namespace], { enabled: !!cluster && !!namespace });

  const fields = error instanceof ApiError ? error.fields : {};
  const ready = userId !== '' && cluster !== '' && namespace !== '';
  const atIso = at ? localInputToIso(at) : null;

  const run = async () => {
    if (!ready) return;
    setBusy(true);
    setError(null);
    try {
      const body: AccessCheckInput = { userId: Number(userId), cluster, namespace, action };
      if (workload) {
        const w = workloads.data?.find((x) => `${x.kind}/${x.name}` === workload);
        if (w) body.workload = { kind: w.kind, name: w.name };
      }
      if (atIso) body.at = atIso;
      const d = await api.accessCheck(body);
      let rules: TimeWindowView[] | null = null;
      let holidays: HolidayView[] | null = null;
      if (!d.allowed && (d.reason === 'outside_window' || d.reason === 'holiday')) {
        [rules, holidays] = await Promise.all([api.timeWindows().catch(() => null), api.holidays().catch(() => null)]);
      }
      setResult({ d, now: Date.now(), rules, holidays });
    } catch (e) {
      setResult(null);
      setError(e);
    } finally {
      setBusy(false);
    }
  };

  if (!open) return null;
  return (
    <Dialog title="Access checker" subtitle="Why can or can’t someone do this?" width={640} onClose={onClose}>
      <form
        className="col"
        style={{ gap: 14 }}
        onSubmit={(e) => { e.preventDefault(); void run(); }}
      >
        <div className="row wrap" style={{ gap: 8, alignItems: 'flex-end' }}>
          <label className="f" style={{ flex: '1 1 150px' }}>
            User
            <select className="sel" style={{ height: 30 }} value={userId} onChange={(e) => setUserId(e.target.value)}>
              <option value="">Choose a user…</option>
              {users.map((u) => (
                <option key={u.id} value={u.id}>{u.displayName} ({u.username})</option>
              ))}
            </select>
            <FieldError msg={fieldError(fields, 'userId')} />
          </label>
          <label className="f" style={{ width: 110 }}>
            Action
            <select className="sel" style={{ height: 30 }} value={action} onChange={(e) => setAction(e.target.value as Action)}>
              {ACTIONS.map((a) => <option key={a} value={a}>{a}</option>)}
            </select>
            <FieldError msg={fieldError(fields, 'action')} />
          </label>
        </div>
        <div className="row wrap" style={{ gap: 8, alignItems: 'flex-end' }}>
          <label className="f" style={{ flex: '1 1 110px' }}>
            Cluster
            <select
              className="sel"
              style={{ height: 30 }}
              value={cluster}
              onChange={(e) => { setCluster(e.target.value); setNamespace(''); setWorkload(''); }}
            >
              <option value="">Cluster…</option>
              {clusters.map((c) => <option key={c.name} value={c.name}>{c.name}</option>)}
            </select>
            <FieldError msg={fieldError(fields, 'cluster')} />
          </label>
          <label className="f" style={{ flex: '1 1 130px' }}>
            Namespace
            <select
              className="sel"
              style={{ height: 30 }}
              value={namespace}
              disabled={!cluster}
              onChange={(e) => { setNamespace(e.target.value); setWorkload(''); }}
            >
              <option value="">{namespaces.loading ? 'Loading…' : 'Namespace…'}</option>
              {(namespaces.data ?? []).map((n) => <option key={n} value={n}>{n}</option>)}
            </select>
            <FieldError msg={fieldError(fields, 'namespace')} />
          </label>
          <label className="f" style={{ flex: '1.3 1 150px' }}>
            Workload
            <select className="sel mono" style={{ height: 30 }} value={workload} disabled={!namespace} onChange={(e) => setWorkload(e.target.value)}>
              <option value="">{workloads.loading ? 'Loading…' : '(whole namespace)'}</option>
              {(workloads.data ?? []).map((w) => (
                <option key={`${w.kind}/${w.name}`} value={`${w.kind}/${w.name}`}>{w.name} · {w.kind}</option>
              ))}
            </select>
            <FieldError msg={fieldError(fields, 'workload')} />
          </label>
        </div>
        {(namespaces.error || workloads.error) ? <ErrorBanner error={namespaces.error ?? workloads.error} /> : null}
        <div className="row wrap" style={{ gap: 8, alignItems: 'flex-end' }}>
          <label className="f" style={{ flex: '1 1 210px' }}>
            <span>At <span className="hint">Optional, {ADMIN_TZ}; default is now</span></span>
            <input className="input" type="datetime-local" value={at} onChange={(e) => setAt(e.target.value)} />
            <FieldError msg={fieldError(fields, 'at') ?? (at && !atIso ? 'Enter a valid date and time.' : undefined)} />
          </label>
          <button type="submit" className="btn pri" disabled={!ready || busy || (at !== '' && !atIso)}>
            <Icon name="search" />
            {busy ? 'Checking…' : 'Check access'}
          </button>
        </div>
        {error !== null && Object.keys(fields).length === 0 && <ErrorBanner error={error} />}
        {result ? (
          <CheckResultView r={result} onEdit={onEdit} />
        ) : (
          !busy && <div className="muted">Pick a user and a target, then press Check access to see which step allows or blocks it.</div>
        )}
      </form>
    </Dialog>
  );
}

/* --------------------------------------------------------------------- page */

export function Grants() {
  const { isAdmin } = useAuth();
  const [fHolder, setFHolder] = useState('');
  const [fKind, setFKind] = useState('');
  const [fCluster, setFCluster] = useState('');
  const [fProject, setFProject] = useState('');
  const [fAction, setFAction] = useState('');
  const [fExpiring, setFExpiring] = useState(false);
  const [fExpired, setFExpired] = useState(false);
  // null: the editor dialog is closed.
  const [form, setForm] = useState<GrantForm | null>(null);
  const [checking, setChecking] = useState(false);

  const filters = useMemo<GrantFilters>(() => {
    const f: GrantFilters = {};
    if (fHolder) {
      const [k, id] = fHolder.split(':');
      f.holderKind = k as 'team' | 'user';
      f.holderId = Number(id);
    }
    if (fKind) f.scopeKind = fKind as GrantFilters['scopeKind'];
    if (fCluster) f.cluster = fCluster;
    if (fProject) f.argoProject = fProject;
    if (fAction) f.action = fAction as Action;
    if (fExpiring) f.expiring = true;
    if (fExpired) f.expired = true;
    return f;
  }, [fHolder, fKind, fCluster, fProject, fAction, fExpiring, fExpired]);

  const grants = useApi(() => api.grants(filters), [JSON.stringify(filters)]);
  const teams = useApi(() => api.teams(), []);
  const users = useApi(() => api.users(), []);
  const catClusters = useApi(() => api.catalogClusters(), [], { enabled: isAdmin });
  const catProjects = useApi(() => api.argoProjects(), [], { enabled: isAdmin && ARGO_UI });
  const now = Date.now();

  // Filter options: catalog (admins) plus everything seen in listed grants, so auditors still get options.
  const [seen, setSeen] = useState<{ clusters: string[]; projects: string[] }>({ clusters: [], projects: [] });
  useEffect(() => {
    if (!grants.data) return;
    setSeen((s) => ({
      clusters: uniq([...s.clusters, ...grants.data!.map((g) => g.scope.cluster)]),
      projects: uniq([...s.projects, ...grants.data!.map((g) => g.scope.argoProject)]),
    }));
  }, [grants.data]);
  const clusterOptions = uniq([...(catClusters.data ?? []).map((c) => c.name), ...seen.clusters, fCluster]);
  const projectOptions = uniq([...(catProjects.data ?? []), ...seen.projects, fProject]);

  const pickers: Pickers = {
    teams: teams.data ?? [],
    users: users.data ?? [],
    clusters: catClusters.data ?? [],
    argoProjects: catProjects.data ?? [],
  };

  const edit = (g: GrantView) => {
    setChecking(false);
    setForm(formFromGrant(g));
  };

  const list = grants.data ?? [];

  return (
    <>
      <PageHeader title="Grants" subtitle="Who can do what, where, and until when">
        {isAdmin ? (
          <>
            <button type="button" className="btn" onClick={() => setChecking(true)}>
              <Icon name="search" />
              Check access
            </button>
            <button type="button" className="btn pri" onClick={() => setForm(emptyGrantForm())}>
              <Icon name="plus" />
              New grant
            </button>
          </>
        ) : (
          <ReadOnlyNote />
        )}
      </PageHeader>

      <div className="row wrap">
        <select className="sel" aria-label="Holder" value={fHolder} onChange={(e) => setFHolder(e.target.value)}>
          <option value="">Any holder</option>
          <optgroup label="Teams">
            {(teams.data ?? []).map((t) => <option key={t.id} value={`team:${t.id}`}>{t.name}</option>)}
          </optgroup>
          <optgroup label="Users">
            {(users.data ?? []).map((u) => <option key={u.id} value={`user:${u.id}`}>{u.displayName}</option>)}
          </optgroup>
        </select>
        <select className="sel" aria-label="Scope kind" value={fKind} onChange={(e) => setFKind(e.target.value)}>
          <option value="">Any scope</option>
          {SCOPE_KINDS.map((k) => <option key={k} value={k}>{SCOPE_KIND_LABEL[k]}</option>)}
        </select>
        <select className="sel" aria-label="Cluster" value={fCluster} onChange={(e) => setFCluster(e.target.value)}>
          <option value="">All clusters</option>
          {clusterOptions.map((c) => <option key={c} value={c}>{c}</option>)}
        </select>
        {ARGO_UI && (
          <select className="sel" aria-label="Argo project" value={fProject} onChange={(e) => setFProject(e.target.value)}>
            <option value="">All Argo projects</option>
            {projectOptions.map((p) => <option key={p} value={p}>{p}</option>)}
          </select>
        )}
        <select className="sel" aria-label="Action" value={fAction} onChange={(e) => setFAction(e.target.value)}>
          <option value="">Any action</option>
          {ACTIONS.map((a) => <option key={a} value={a}>{a}</option>)}
        </select>
        <label className="chk"><input type="checkbox" checked={fExpiring} onChange={(e) => setFExpiring(e.target.checked)} /> Expiring soon</label>
        <label className="chk"><input type="checkbox" checked={fExpired} onChange={(e) => setFExpired(e.target.checked)} /> Expired</label>
      </div>

      {grants.error ? (
        <ErrorBanner error={grants.error} onRetry={grants.reload} />
      ) : (
        <div className="card" aria-busy={grants.loading}>
          {grants.loading && !grants.data ? (
            <Skeleton rows={4} />
          ) : list.length === 0 ? (
            <Empty>No grants match these filters.</Empty>
          ) : (
            <div className="tbl-wrap">
            <table className="tbl">
              <thead>
                <tr><th>Holder</th><th>Scope</th><th>Cluster</th><th>Actions</th><th>Expiry</th><th>Note</th></tr>
              </thead>
              <tbody>
                {list.map((g) => {
                  const sel = isAdmin && form?.id === g.id;
                  return (
                    <tr
                      key={g.id}
                      className={`${isAdmin ? 'click' : ''}${sel ? ' sel' : ''}${g.expired ? ' off' : ''}`}
                      tabIndex={isAdmin ? 0 : undefined}
                      aria-current={sel ? 'true' : undefined}
                      onClick={isAdmin ? () => edit(g) : undefined}
                      onKeyDown={isAdmin ? (e) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); edit(g); } } : undefined}
                    >
                      <td><HolderCell g={g} /></td>
                      <td title={`${SCOPE_KIND_LABEL[g.scope.kind]} ${scopeName(g.scope)}`}><ScopeCell scope={g.scope} struck={g.expired} /></td>
                      <td>
                        <span className="mono">{scopeCluster(g.scope)}</span>
                        {g.environment && <>{' '}<EnvBadge env={g.environment} /></>}
                      </td>
                      <td><ActionChips actions={g.actions} /></td>
                      <td><ExpiryCell g={g} now={now} /></td>
                      <td className="muted">{sel ? 'Editing…' : g.note || '—'}</td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
            </div>
          )}
        </div>
      )}

      {isAdmin && form && (
        <GrantEditor
          form={form}
          setForm={setForm}
          pickers={pickers}
          onSaved={() => { setForm(null); grants.reload(); }}
          onDeleted={() => { setForm(null); grants.reload(); }}
          onCancel={() => setForm(null)}
        />
      )}
      {isAdmin && (
        <AccessChecker
          open={checking}
          onClose={() => setChecking(false)}
          users={pickers.users}
          clusters={pickers.clusters}
          onEdit={edit}
        />
      )}
    </>
  );
}
