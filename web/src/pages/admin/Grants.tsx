import { useEffect, useId, useMemo, useState } from 'react';
import { ApiError, api } from '../../api';
import type {
  AccessCheckInput, Action, CatalogCluster, DecisionExplain, GrantFilters, GrantView, HolidayView, TeamView,
  TimeWindowView, UserView, WorkloadKind,
} from '../../api';
import { FieldError, ReadOnlyNote } from '../../components/AdminBits';
import '../../components/adminPages.css';
import { ActionChips, EnvBadge } from '../../components/Badges';
import { ComboBox } from '../../components/ComboBox';
import { Select } from '../../components/Select';
import { Dialog } from '../../components/Dialog';
import { Empty, ErrorBanner, Skeleton } from '../../components/Feedback';
import { Icon } from '../../components/Icon';
import { PageHeader } from '../../components/PageHeader';
import { SCOPE_KIND_LABEL, ScopeCell, scopeCluster, scopeName } from '../../components/Scope';
import { Seg } from '../../components/Seg';
import { useAuth } from '../../context/auth';
import { useToast } from '../../context/toast';
import { useApi } from '../../hooks/useApi';
import { useUrlState } from '../../hooks/useUrlState';
import { buildSteps, verdict } from '../../lib/accessCheckSteps';
import type { CheckStep } from '../../lib/accessCheckSteps';
import { ADMIN_TZ, localInputToIso } from '../../lib/adminDatetime';
import { fieldError, unmatchedFields } from '../../lib/adminFields';
import {
  ACTIONS, APP_SCOPE_HINT, MAX_GRANT_BATCH, formNamespaces, actionOptionLabel, isAppAction, actionPhrase, emptyGrantForm, formFromGrant, grantInputFromForm, parseWlKey, validateGrantForm, wlKey,
} from '../../lib/grantForm';
import type { GrantForm } from '../../lib/grantForm';
import { fmtDateTime, fmtDay, fmtSpan } from '../../lib/time';
import { ARGO_UI } from '../../lib/features';
import { LIST_SEARCH_FROM, matchesQuery } from '../../lib/comboFilter';

const SCOPE_KINDS: GrantForm['scopeKind'][] = [...(ARGO_UI ? ['argo_project' as const] : []), 'environment', 'cluster', 'namespace', 'workloads'];
const FORM_FIELDS = ['holder', 'scope', 'argoProject', 'environment', 'cluster', 'namespace', 'namespaces', 'workloads', 'actions', 'expiresAt', 'note'];
const WHERE_HINT: Record<GrantForm['scopeKind'], string> = {
  argo_project: 'Argo project (all clusters)',
  environment: 'Environment (every namespace labeled with it, all clusters)',
  cluster: 'Cluster (every namespace, also future ones)',
  namespace: 'Cluster → namespace',
  workloads: 'Cluster → namespace → workloads',
};

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
  environments: string[];
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

  const needsNs = form.scopeKind === 'namespace' || form.scopeKind === 'workloads';
  const namespaces = useApi(() => api.catalogNamespaces(form.cluster), [form.cluster], { enabled: needsNs && !!form.cluster });
  const workloads = useApi(
    () => api.catalogWorkloads(form.cluster, form.namespace),
    [form.cluster, form.namespace],
    { enabled: form.scopeKind === 'workloads' && !!form.cluster && !!form.namespace },
  );

  const nsOptions = uniq([...(namespaces.data ?? []), form.namespace]);
  // A new namespace grant may go on several namespaces at once (one grant each).
  const many = form.id === null && form.scopeKind === 'namespace';
  const nsList = formNamespaces(form);
  // The holder's grants on this cluster, to say where one already exists (a new one is created beside it).
  const existing = useApi(
    () => api.grants({ holderKind: form.holderKind, holderId: form.holderId!, scopeKind: 'namespace', cluster: form.cluster }),
    [form.holderKind, form.holderId, form.cluster],
    { enabled: many && form.holderId !== null && !!form.cluster },
  );
  const alreadyOn = nsList.filter((ns) => (existing.data ?? []).some((g) => !g.expired && g.scope.namespace === ns));
  const [wq, setWq] = useState('');
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
      if (many && nsList.length > 1) {
        const saved = await api.createGrants({ ...input, namespaces: nsList });
        toast(`${saved.length} grants created.`);
        onSaved(saved[0]);
        return;
      }
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
      {form.scopeKind === 'environment' && (
        <>every namespace in environment <span className="mono">{form.environment || '…'}</span> on all clusters</>
      )}
      {form.scopeKind === 'cluster' && (
        <>every namespace on cluster <span className="mono">{form.cluster || '…'}</span>, including new ones</>
      )}
      {form.scopeKind === 'namespace' && (nsList.length > 1 ? (
        <>{nsList.length} namespaces (<span className="mono">{nsList.join(', ')}</span>) on cluster <span className="mono">{form.cluster || '…'}</span>, one grant each</>
      ) : (
        <>namespace <span className="mono">{form.namespace || '…'}</span> on cluster <span className="mono">{form.cluster || '…'}</span></>
      ))}
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
            {busy ? 'Saving…' : form.id !== null ? 'Save grant' : nsList.length > 1 && many ? `Create ${nsList.length} grants` : 'Create grant'}
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
            <ComboBox
              key={form.holderKind}
              style={{ flex: 1 }}
              label={form.holderKind === 'team' ? 'Team' : 'User'}
              invalid={!!fields.holder}
              value={form.holderId === null ? '' : String(form.holderId)}
              onChange={(v) => set({ holderId: v ? Number(v) : null })}
              placeholder={`Choose a ${form.holderKind}…`}
              options={holderList.map((h) =>
                'members' in h
                  ? { value: String(h.id), label: h.name, hint: `${h.members.length} members` }
                  : { value: String(h.id), label: h.displayName, hint: `${h.username}${h.disabled ? ' · disabled' : ''}` },
              )}
            />
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
          <span>3 · Where <span className="hint">{WHERE_HINT[form.scopeKind]}</span></span>
          {form.scopeKind === 'argo_project' ? (
            <>
              <ComboBox
                label="Argo project"
                invalid={!!fields.argoProject}
                value={form.argoProject}
                onChange={(v) => set({ argoProject: v })}
                placeholder="Choose an Argo project…"
                options={uniq([...pickers.argoProjects, form.argoProject]).map((p) => ({ value: p }))}
              />
              <FieldError msg={fieldError(fields, 'argoProject', 'scope.argoProject')} />
            </>
          ) : form.scopeKind === 'environment' ? (
            <>
              <Select
                label="Environment"
                style={{ height: 30, width: '100%' }}
                invalid={!!fields.environment}
                value={form.environment}
                onChange={(v) => set({ environment: v })}
                options={[{ value: '', label: 'Choose an environment…', disabled: true }, ...uniq([...pickers.environments, form.environment]).map((v) => ({ value: v, label: v }))]}
              />
              <FieldError msg={fieldError(fields, 'environment', 'scope.environment')} />
            </>
          ) : form.scopeKind === 'cluster' ? (
            <>
              <Select
                label="Cluster"
                style={{ height: 30, width: '100%' }}
                invalid={!!fields.cluster}
                value={form.cluster}
                onChange={(v) => set({ cluster: v, namespace: '', extraNamespaces: [], workloads: [] })}
                options={[{ value: '', label: 'Choose a cluster…', disabled: true }, ...uniq([...pickers.clusters.map((c) => c.name), form.cluster]).map((c) => ({ value: c, label: c }))]}
              />
              {clusterEnv && (
                <span className="row" style={{ gap: 6, fontWeight: 400 }}>
                  Environment <EnvBadge env={clusterEnv} />
                </span>
              )}
              <FieldError msg={fieldError(fields, 'cluster', 'scope.cluster')} />
            </>
          ) : (
            <>
              <div className="row" style={{ gap: 8 }}>
                <Select
                  label="Cluster"
                  style={{ height: 30, flex: 1 }}
                  invalid={!!fields.cluster}
                  value={form.cluster}
                  onChange={(v) => set({ cluster: v, namespace: '', extraNamespaces: [], workloads: [] })}
                  options={[{ value: '', label: 'Cluster…', disabled: true }, ...uniq([...pickers.clusters.map((c) => c.name), form.cluster]).map((c) => ({ value: c, label: c }))]}
                />
                <Icon name="chev" className="muted" />
                <ComboBox
                  style={{ flex: 1 }}
                  label="Namespace"
                  invalid={!!fields.namespace}
                  disabled={!form.cluster}
                  value={form.namespace}
                  onChange={(v) => { set({ namespace: v, workloads: [] }); setWq(''); }}
                  placeholder={namespaces.loading ? 'Loading…' : 'Namespace…'}
                  options={nsOptions.map((n) => ({ value: n }))}
                />
              </div>
              {clusterEnv && (
                <span className="row" style={{ gap: 6, fontWeight: 400 }}>
                  Environment <EnvBadge env={clusterEnv} />
                </span>
              )}
              {many && form.namespace && (
                <div className="f" role="group" aria-label="More namespaces">
                  <span>More namespaces <span className="hint">Optional: the same grant on each, created together (up to {MAX_GRANT_BATCH})</span></span>
                  {form.extraNamespaces.length > 0 && (
                    <div className="pill-row">
                      {form.extraNamespaces.map((ns) => (
                        <span key={ns} className="chip ns-chip">
                          <span className="mono">{ns}</span>
                          <button type="button" className="linklike" aria-label={`Remove ${ns}`} onClick={() => set({ extraNamespaces: form.extraNamespaces.filter((x) => x !== ns) })}>
                            <Icon name="x" size={12} />
                          </button>
                        </span>
                      ))}
                    </div>
                  )}
                  <ComboBox
                    label="Add a namespace"
                    value=""
                    disabled={nsList.length >= MAX_GRANT_BATCH}
                    invalid={!!fields.namespaces}
                    onChange={(v) => set({ extraNamespaces: [...form.extraNamespaces, v] })}
                    placeholder="Add another namespace…"
                    options={nsOptions.filter((n) => !nsList.includes(n)).map((n) => ({ value: n }))}
                  />
                </div>
              )}
              {many && alreadyOn.length > 0 && (
                <span className="hint" role="note">
                  {holderName ?? 'This holder'} already has a grant on {alreadyOn.join(', ')}; a new grant is created beside it.
                </span>
              )}
              <FieldError msg={fieldError(fields, 'cluster', 'scope.cluster')} />
              <FieldError msg={fieldError(fields, 'namespace', 'scope.namespace')} />
              <FieldError msg={fieldError(fields, 'namespaces')} />
              {namespaces.error ? <ErrorBanner error={namespaces.error} onRetry={namespaces.reload} /> : null}
              {form.scopeKind === 'workloads' && form.namespace && (
                <>
                  {wlOptions.length > LIST_SEARCH_FROM && (
                    <label className="search" style={{ width: '100%', height: 28, background: 'var(--sf)' }}>
                      <Icon name="search" />
                      <input value={wq} onChange={(e) => setWq(e.target.value)} placeholder="Filter workloads" aria-label="Filter workloads" />
                    </label>
                  )}
                  <div className="pickbox" role="group" aria-label="Workloads">
                    {workloads.loading && wlOptions.length === 0 && <div style={{ padding: 8 }}><Skeleton rows={2} /></div>}
                    {!workloads.loading && wlOptions.length === 0 && !workloads.error && (
                      <div className="muted" style={{ padding: '8px 10px' }}>No workloads in this namespace.</div>
                    )}
                    {wlOptions.length > 0 && !wlOptions.some((w) => matchesQuery(`${w.kind} ${w.name}`, wq)) && (
                      <div className="muted" style={{ padding: '8px 10px' }}>No workload matches.</div>
                    )}
                    {wlOptions.filter((w) => matchesQuery(`${w.kind} ${w.name}`, wq)).map((w) => (
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
            {needsNs ? 'Protected namespaces (kube-system, argocd) are hidden from pickers.' : 'Protected namespaces (kube-system, argocd) are never covered.'}
          </span>
        </div>

        <div className="f" role="group" aria-label="Actions">
          4 · Actions
          <div className="row wrap" style={{ gap: 16 }}>
            {ACTIONS.map((a) => {
              const off = isAppAction(a) && form.scopeKind === 'workloads' && !form.actions.includes(a);
              return (
                <label key={a} className="chk" title={off ? APP_SCOPE_HINT : undefined}>
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
            <ComboBox
              label="User"
              value={userId}
              onChange={setUserId}
              placeholder="Choose a user…"
              options={users.map((u) => ({ value: String(u.id), label: u.displayName, hint: u.username }))}
            />
            <FieldError msg={fieldError(fields, 'userId')} />
          </label>
          <label className="f" style={{ width: 110 }}>
            Action
            <Select label="Action" style={{ height: 30, width: '100%' }} value={action} onChange={setAction} options={ACTIONS.map((a) => ({ value: a, label: a }))} />
            <FieldError msg={fieldError(fields, 'action')} />
          </label>
        </div>
        <div className="row wrap" style={{ gap: 8, alignItems: 'flex-end' }}>
          <label className="f" style={{ flex: '1 1 110px' }}>
            Cluster
            <Select
              label="Cluster"
              style={{ height: 30, width: '100%' }}
              value={cluster}
              onChange={(v) => { setCluster(v); setNamespace(''); setWorkload(''); }}
              options={[{ value: '', label: 'Cluster…', disabled: true }, ...clusters.map((c) => ({ value: c.name, label: c.name }))]}
            />
            <FieldError msg={fieldError(fields, 'cluster')} />
          </label>
          <label className="f" style={{ flex: '1 1 130px' }}>
            Namespace
            <ComboBox
              label="Namespace"
              value={namespace}
              disabled={!cluster}
              onChange={(v) => { setNamespace(v); setWorkload(''); }}
              placeholder={namespaces.loading ? 'Loading…' : 'Namespace…'}
              options={(namespaces.data ?? []).map((n) => ({ value: n }))}
            />
            <FieldError msg={fieldError(fields, 'namespace')} />
          </label>
          <label className="f" style={{ flex: '1.3 1 150px' }}>
            Workload
            <ComboBox
              label="Workload"
              value={workload}
              disabled={!namespace}
              onChange={setWorkload}
              placeholder={workloads.loading ? 'Loading…' : '(whole namespace)'}
              options={[
                { value: '', label: '(whole namespace)' },
                ...(workloads.data ?? []).map((w) => ({ value: `${w.kind}/${w.name}`, label: w.name, hint: w.kind })),
              ]}
            />
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

/** What the Grants search looks in: holder, scope names, cluster, environment, note, author. */
function grantSearchText(g: GrantView): string {
  const s = g.scope;
  return [
    g.holder.name, s.argoProject, s.cluster, s.namespace, s.environment, ...(s.workloads ?? []).map((w) => w.name),
    g.environment, g.note, g.createdBy,
  ].filter(Boolean).join(' ');
}

export function Grants() {
  const { isAdmin } = useAuth();
  const [{ q, holder: fHolder, kind: fKind, cluster: fCluster, project: fProject, action: fAction, expiring: fExpiring, expired: fExpired }, setF] =
    useUrlState({ q: '', holder: '', kind: '', cluster: '', project: '', action: '', expiring: false, expired: false });
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
  const overrides = useApi(() => api.environmentOverrides(), [], { enabled: isAdmin });
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
    // Every environment a namespace can have: cluster labels and overrides.
    environments: uniq([...(catClusters.data ?? []).map((c) => c.environment), ...(overrides.data ?? []).map((o) => o.environment)])
      .filter((e) => e !== 'unlabeled'),
  };

  const edit = (g: GrantView) => {
    setChecking(false);
    setForm(formFromGrant(g));
  };

  const all = grants.data ?? [];
  // The API filters by holder, scope kind, cluster, project and action; names are searched here.
  const list = q.trim() ? all.filter((g) => matchesQuery(grantSearchText(g), q)) : all;

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
        <label className="search" style={{ width: 240, height: 28, background: 'var(--sf)' }}>
          <Icon name="search" />
          <input value={q} onChange={(e) => setF({ q: e.target.value })} placeholder="Search names, namespaces, notes" aria-label="Search grants" />
        </label>
        <ComboBox
          small
          style={{ width: 200 }}
          label="Holder"
          value={fHolder}
          onChange={(v) => setF({ holder: v })}
          placeholder="Any holder"
          options={[
            { value: '', label: 'Any holder' },
            ...(teams.data ?? []).map((t) => ({ value: `team:${t.id}`, label: t.name, hint: 'Team' })),
            ...(users.data ?? []).map((u) => ({ value: `user:${u.id}`, label: u.displayName, hint: u.username })),
          ]}
        />
        <Select label="Scope kind" value={fKind} onChange={(v) => setF({ kind: v })} options={[{ value: '', label: 'Any scope' }, ...SCOPE_KINDS.map((k) => ({ value: k, label: SCOPE_KIND_LABEL[k] }))]} />
        <Select label="Cluster" value={fCluster} onChange={(v) => setF({ cluster: v })} options={[{ value: '', label: 'All clusters' }, ...clusterOptions.map((c) => ({ value: c, label: c }))]} />
        {ARGO_UI && (
          <Select label="Argo project" value={fProject} onChange={(v) => setF({ project: v })} options={[{ value: '', label: 'All Argo projects' }, ...projectOptions.map((p) => ({ value: p, label: p }))]} />
        )}
        <Select label="Action" value={fAction} onChange={(v) => setF({ action: v })} options={[{ value: '', label: 'Any action' }, ...ACTIONS.map((a) => ({ value: a, label: a }))]} />
        <label className="chk"><input type="checkbox" checked={fExpiring} onChange={(e) => setF({ expiring: e.target.checked })} /> Expiring soon</label>
        <label className="chk"><input type="checkbox" checked={fExpired} onChange={(e) => setF({ expired: e.target.checked })} /> Expired</label>
      </div>

      {grants.error ? (
        <ErrorBanner error={grants.error} onRetry={grants.reload} />
      ) : (
        <div className="card" aria-busy={grants.loading}>
          {grants.loading && !grants.data ? (
            <Skeleton rows={4} />
          ) : list.length === 0 ? (
            <Empty>
              {all.length > 0 ? `No grants match “${q.trim()}”.` : 'No grants match these filters.'}
            </Empty>
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
