import { useEffect, useMemo, useRef, useState } from 'react';
import { useSearchParams } from 'react-router-dom';
import { ApiError, api, errorText } from '../api';
import type { Action, AccessRequest, CatalogWorkload, DecisionInfo, ManagedTeamView, MyAccessGrant, ScopeView, WorkloadAction, WorkloadSummary } from '../api';
import { ActionChips, KindTag } from '../components/Badges';
import { ComboBox } from '../components/ComboBox';
import { Select } from '../components/Select';
import { BatchScope, GroupStatus } from '../components/RequestGroup';
import { groupKey, groupRequests } from '../lib/requestBatches';
import { LIST_SEARCH_FROM, matchesQuery } from '../lib/comboFilter';
import { ErrorBanner, Loading } from '../components/Feedback';
import { Icon } from '../components/Icon';
import { PageHeader } from '../components/PageHeader';
import { ScopeCell, scopeCluster } from '../components/Scope';
import { Seg } from '../components/Seg';
import { useAuth } from '../context/auth';
import { useBreakGlass } from '../context/breakglass';
import { useToast } from '../context/toast';
import { useApi } from '../hooks/useApi';
import { useNow } from '../hooks/useNow';
import { explainDecision } from '../lib/decision';
import { mergeTarget, sameScope } from '../lib/requestMerge';
import type { MergeableGrant } from '../lib/requestMerge';
import { APP_SCOPE_HINT, actionOptionLabel, isAppAction } from '../lib/grantForm';
import { fmtDateTime, fmtDay, fmtHours, fmtReopens, fmtWhen } from '../lib/time';
import { ARGO_UI } from '../lib/features';
import { requestPrefill, requestUrl } from '../lib/requestLink';
import type { RequestPrefill } from '../lib/requestLink';
import { breakGlassKey, breakGlassOn, breakGlassScheduled } from '../lib/breakGlass';

/** The server's cap on a batch (application.MaxBatchNamespaces). */
const MAX_BATCH = 20;
const ALL_ACTIONS: Action[] = ['logs', 'exec', 'restart', 'run-job', 'sync', 'rollback'];
const DURATIONS: { value: number | 'perm'; label: string; hours: number | null }[] = [
  { value: 1, label: '1 hour', hours: 1 }, { value: 8, label: '8 hours', hours: 8 }, { value: 24, label: '1 day', hours: 24 },
  { value: 168, label: '7 days', hours: 168 }, { value: 'perm', label: 'Permanent', hours: null },
];

/** Availability of one grant right now, derived from the workload decisions of its namespace. */
type Avail = { state: 'open' } | { state: 'closed'; text: string; why: string } | { state: 'unknown' } | { state: 'expired' };

function availability(g: MyAccessGrant, wls: WorkloadSummary[] | undefined, now: number, canBg: boolean): Avail {
  if (g.expired) return { state: 'expired' };
  if (!wls) return { state: 'unknown' };
  const covered = g.scope.kind === 'workloads' ? wls.filter((w) => g.scope.workloads?.some((x) => x.kind === w.kind && x.name === w.name)) : wls;
  // Sync is decided per Argo CD application, not per workload.
  const infos: DecisionInfo[] = covered.flatMap((w) => g.actions.filter((a): a is WorkloadAction => !isAppAction(a)).map((a) => w.access[a]));
  if (infos.length === 0) return { state: 'unknown' };
  if (infos.some((i) => i.allowed)) return { state: 'open' };
  const closed = infos.find((i) => i.reason === 'outside_window' || i.reason === 'holiday') ?? infos[0];
  const e = explainDecision(closed, { action: g.actions[0], now, canBreakGlass: canBg });
  const when = closed.reopensAt ? `Closed until ${fmtReopens(closed.reopensAt, now)}` : 'Not available now';
  return { state: 'closed', text: when, why: closed.reason === 'outside_window' ? 'Time window' : closed.reason === 'holiday' ? 'Holiday' : (e?.text ?? 'Not available') };
}

export function MyAccess() {
  const { me, usesGrants } = useAuth();
  const toast = useToast();
  const bg = useBreakGlass();
  const now = useNow(60000);
  const [sp, setSp] = useSearchParams();
  const [tab, setTab] = useState<'grants' | 'requests'>('grants');
  const bgKey = breakGlassKey(me);
  const data = useApi(() => api.myAccess(), [bgKey]);
  const formRef = useRef<HTMLElement>(null);
  const reasonRef = useRef<HTMLTextAreaElement>(null);

  // availability: fetch live workload decisions per (cluster, namespace) referenced by a grant
  const nsKeys = useMemo(() => {
    const s = new Set<string>();
    for (const g of data.data?.grants ?? []) if (g.scope.cluster && g.scope.namespace && !g.expired) s.add(`${g.scope.cluster}/${g.scope.namespace}`);
    return [...s];
  }, [data.data]);
  const [wls, setWls] = useState<Record<string, WorkloadSummary[] | undefined>>({});
  useEffect(() => {
    let alive = true;
    for (const k of nsKeys) {
      const [c, n] = k.split('/');
      api.workloads(c, n).then((w) => alive && setWls((x) => ({ ...x, [k]: w }))).catch(() => alive && setWls((x) => ({ ...x, [k]: undefined })));
    }
    return () => { alive = false; };
  }, [nsKeys, bgKey]);
  const apps = useApi(() => api.apps(), [bgKey]);
  // Team managers may also ask for their teams.
  const managed = useApi(() => api.myTeams(), [], { enabled: me.managedTeams.length > 0 });

  const requesting = sp.get('request') === '1';
  useEffect(() => {
    if (requesting) {
      formRef.current?.scrollIntoView({ block: 'start' });
      window.setTimeout(() => reasonRef.current?.focus(), 50);
    }
  }, [requesting, sp, data.data]);

  // A "Request access" link may name what was denied; the form starts with it.
  const prefill = requestPrefill(sp);
  // My teams links here with ?team=<id> to ask for that team.
  const prefillTeam = me.managedTeams.find((t) => String(t.id) === sp.get('team'))?.id ?? 0;
  const closeRequest = () => {
    const n = new URLSearchParams(sp);
    for (const k of ['request', 'cluster', 'namespace', 'action', 'kind', 'name', 'team']) n.delete(k);
    setSp(n, { replace: true });
  };

  if (data.loading && !data.data) return <Loading what="your access" />;
  if (data.error && !data.data) return <ErrorBanner error={data.error} onRetry={data.reload} />;
  const d = data.data!;
  const grants = [...d.grants].sort((a, b) => Number(a.expired) - Number(b.expired) || a.id - b.id);

  function availOf(g: MyAccessGrant): Avail {
    if (g.scope.kind === 'argo_project') {
      if (g.expired) return { state: 'expired' };
      const projectApps = (apps.data ?? []).filter((a) => a.project === g.scope.argoProject);
      if (!apps.data) return { state: 'unknown' };
      if (me.argo.stale) return { state: 'closed', text: 'Paused', why: 'Argo CD data is out of date' };
      return projectApps.some((a) => g.actions.some((x) => a.access.includes(x))) ? { state: 'open' } : projectApps.length ? { state: 'closed', text: 'Not available now', why: 'Closed by a time window' } : { state: 'unknown' };
    }
    return availability(g, wls[`${g.scope.cluster}/${g.scope.namespace}`], now, usesGrants && breakGlassOn(me));
  }

  return (
    <>
      <PageHeader title="My access" subtitle="What you can do, where, and whether it's open right now">
        <button type="button" className="btn pri" onClick={() => { const n = new URLSearchParams(sp); n.set('request', '1'); setSp(n, { replace: true }); }}><Icon name="plus" />Request access</button>
      </PageHeader>

      {me.breakGlass && (breakGlassScheduled(me.breakGlass, Date.now()) ? (
        <div className="banner red"><Icon name="clock" /><span><b>Break-glass is scheduled</b> from {fmtDateTime(me.breakGlass.startedAt)} until {fmtDateTime(me.breakGlass.expiresAt)}. It will override time windows only; it never adds access.</span>
          <span className="grow" /><button type="button" className="btn sm" onClick={() => void bg.end()}>Cancel</button></div>
      ) : (
        <div className="banner red"><Icon name="warn" /><span><b>Break-glass is active</b> until {fmtDateTime(me.breakGlass.expiresAt)}. It overrides time windows only; it never adds access.</span>
          <span className="grow" /><button type="button" className="btn sm" onClick={() => void bg.end()}>End now</button></div>
      ))}

      <div className="two">
        <div className="col grow" style={{ gap: 16, minWidth: 0 }}>
          <div className="tabs" role="tablist">
            <button type="button" role="tab" aria-selected={tab === 'grants'} className={`tabbtn${tab === 'grants' ? ' on' : ''}`} onClick={() => setTab('grants')}>Grants <span className="c">{grants.length}</span></button>
            <button type="button" role="tab" aria-selected={tab === 'requests'} className={`tabbtn${tab === 'requests' ? ' on' : ''}`} onClick={() => setTab('requests')}>Requests <span className="c">{groupRequests(d.requests).length}</span></button>
          </div>

          {tab === 'grants' && (
            <div className="card" style={{ marginTop: -8, borderTop: 0, borderRadius: '0 0 8px 8px' }}>
              <div className="tbl-wrap">
                <table className="tbl nowrap">
                  <thead><tr><th>Scope</th><th>Cluster</th><th>Actions</th><th>Source</th><th>Expiry</th><th>Available now</th></tr></thead>
                  <tbody>
                    {grants.length === 0 && <tr><td colSpan={6} className="empty">You have no grants yet. Request access to get started.</td></tr>}
                    {grants.map((g) => {
                      const a = availOf(g);
                      return (
                        <tr key={g.id} className={g.expired ? 'off' : undefined}>
                          <td><ScopeCell scope={g.scope} struck={g.expired} /></td>
                          <td className="mono">{scopeCluster(g.scope)}</td>
                          <td><div style={{ minWidth: 200 }}><ActionChips actions={g.actions} /></div></td>
                          <td>{g.holder.kind === 'team' ? <>via team <b>{g.holder.name}</b></> : 'Direct'}</td>
                          <td>{g.expired ? `Expired ${fmtDay(g.expiresAt)}` : g.expiresAt ? fmtDateTime(g.expiresAt) : <span className="muted">None</span>}</td>
                          <td>
                            {a.state === 'open' && <span className="chip ok"><span className="dot ok" />Open now</span>}
                            {a.state === 'expired' && <span className="chip">Expired</span>}
                            {a.state === 'unknown' && <span className="muted">—</span>}
                            {a.state === 'closed' && (<><span className="chip warn"><span className="dot warn" />{a.text}</span><div className="muted" style={{ fontSize: 11.5 }}>{a.why}</div></>)}
                          </td>
                        </tr>
                      );
                    })}
                  </tbody>
                </table>
              </div>
            </div>
          )}

          {tab === 'requests' && (
            <div className="col" style={{ gap: 8, marginTop: -8 }}>
              <div className="card" style={{ borderTop: 0, borderRadius: '0 0 8px 8px' }}>
                <div className="tbl-wrap">
                  <table className="tbl nowrap">
                    <thead><tr><th>Requested</th><th>Scope</th><th>Actions</th><th>Duration</th><th>Status</th></tr></thead>
                    <tbody>
                      {d.requests.length === 0 && <tr><td colSpan={5} className="empty">No requests yet.</td></tr>}
                      {groupRequests(d.requests).map((g) => <RequestRow key={groupKey(g)} g={g} now={now} />)}
                    </tbody>
                  </table>
                </div>
              </div>
            </div>
          )}
        </div>

        {/* Open only on request, so the tables get the full width otherwise. */}
        {requesting && (
          <RequestForm
            key={`${prefill ? requestUrl(prefill) : ''}#${prefillTeam}`} prefill={prefill} prefillTeam={prefillTeam}
            formRef={formRef} reasonRef={reasonRef} grants={d.grants} teams={managed.data ?? []} now={now}
            onSent={() => { toast('Request sent. An administrator will review it.'); data.reload(); managed.reload(); setTab('requests'); closeRequest(); }}
            onCancel={closeRequest}
          />
        )}
      </div>
    </>
  );
}

/** A request, or a batch of them (one row: its namespaces and how they were decided). */
function RequestRow({ g, now }: { g: AccessRequest[]; now: number }) {
  const r = g[0];
  return (
    <tr className={g.every((x) => x.status === 'expired') ? 'off' : undefined}>
      <td>{fmtWhen(r.createdAt, now)}</td>
      <td>
        {g.length > 1 ? <BatchScope g={g} /> : (
          <><span className="mono">{r.scope.kind === 'argo_project' ? r.scope.argoProject : r.scope.kind === 'workloads' && r.scope.workloads?.length === 1 ? r.scope.workloads[0].name : r.scope.namespace}</span>{r.scope.cluster ? <> · {r.scope.cluster}</> : null}</>
        )}
        {r.team && <div className="muted" style={{ fontSize: 11.5, marginTop: 2 }}>for team {r.team.name}</div>}
      </td>
      <td><div style={{ minWidth: 200 }}><ActionChips actions={r.actions} /></div></td>
      <td>{fmtHours(r.durationHours)}</td>
      <td><GroupStatus g={g} /></td>
    </tr>
  );
}

/**
 * Where the scope matches grants the user already has, their actions are
 * shown as held and only new ones are requested; the hint says whether
 * approval adds to the user's own grant (see lib/requestMerge) or creates one.
 * A team manager may ask for a team instead: the team's grants count then,
 * and approval grants the team.
 */
function RequestForm({ prefill, prefillTeam, formRef, reasonRef, grants, teams, now, onSent, onCancel }: {
  prefill: RequestPrefill | null; prefillTeam: number;
  formRef: React.RefObject<HTMLElement>; reasonRef: React.RefObject<HTMLTextAreaElement>; grants: MyAccessGrant[];
  teams: ManagedTeamView[]; now: number; onSent: () => void; onCancel: () => void;
}) {
  const [forTeam, setForTeam] = useState(prefillTeam);
  const team = forTeam ? teams.find((t) => t.id === forTeam) : undefined;
  const holder = forTeam ? 'team' : 'user';
  const holderGrants: MergeableGrant[] = forTeam ? (team?.grants ?? []).map((g) => ({ ...g, holder: { kind: 'team' as const } })) : grants;
  const [kind, setKind] = useState<ScopeView['kind']>(prefill?.workload ? 'workloads' : 'namespace');
  const [project, setProject] = useState('');
  const [cluster, setCluster] = useState(prefill?.cluster ?? '');
  const [namespace, setNamespace] = useState(prefill?.namespace ?? '');
  // More namespaces for a namespace scope: sent as one batch, decided together.
  const [extra, setExtra] = useState<string[]>([]);
  const [picked, setPicked] = useState<CatalogWorkload[]>(prefill?.workload ? [prefill.workload] : []);
  const [wq, setWq] = useState('');
  const [actions, setActions] = useState<Action[]>([prefill?.action ?? 'logs']);
  const [dur, setDur] = useState<number | 'perm'>('perm');
  const [reason, setReason] = useState('');
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<unknown>(null);
  const projects = useApi(() => api.argoProjects(), [], { enabled: ARGO_UI });
  const clusters = useApi(() => api.catalogClusters(), []);
  const nss = useApi(() => api.catalogNamespaces(cluster), [cluster], { enabled: !!cluster });
  const wls = useApi(() => api.catalogWorkloads(cluster, namespace), [cluster, namespace], { enabled: !!cluster && !!namespace && kind === 'workloads' });
  const shownWls = (wls.data ?? []).filter((w) => matchesQuery(`${w.kind} ${w.name}`, wq));
  const fields = err instanceof ApiError ? err.fields : {};

  useEffect(() => { if (!project && projects.data?.[0]) setProject(projects.data[0]); }, [projects.data, project]);
  useEffect(() => {
    if (clusters.data && !clusters.data.some((c) => c.name === cluster)) setCluster(clusters.data[0]?.name ?? '');
  }, [clusters.data, cluster]);
  // The catalog leaves out only protected clusters, where requests are refused.
  const linkedHidden = !!prefill && !!clusters.data && !clusters.data.some((c) => c.name === prefill.cluster);
  // A namespace the cluster lists stays chosen (or the same name on another
  // cluster), and so does a prefilled one an unreachable cluster cannot list;
  // otherwise none is: the user picks it, so a request never goes to a
  // namespace they did not choose. Picked workloads reset when the place changes.
  useEffect(() => {
    if (!nss.data) return;
    const linked = prefill && cluster === prefill.cluster ? prefill.namespace : '';
    setNamespace((n) => (nss.data!.includes(n) || (n && n === linked) ? n : ''));
  }, [nss.data, prefill, cluster]);
  const lastCluster = useRef(cluster);
  useEffect(() => {
    if (lastCluster.current !== cluster) {
      lastCluster.current = cluster;
      setExtra([]);
    }
  }, [cluster]);
  const where = `${cluster}/${namespace}`;
  const lastWhere = useRef(where);
  useEffect(() => {
    if (lastWhere.current !== where) {
      lastWhere.current = where;
      setPicked([]);
    }
  }, [where]);

  const scope: ScopeView | null = kind === 'argo_project' ? (project ? { kind, argoProject: project } : null)
    : !cluster || !namespace ? null : kind === 'namespace' ? { kind, cluster, namespace } : picked.length ? { kind, cluster, namespace, workloads: picked } : null;
  const selected = kind === 'namespace' ? [namespace, ...extra.filter((n) => n !== namespace)].filter(Boolean) : [];
  const batch = selected.length > 1;
  const heldIn = (ns: string) => new Set(holderGrants
    .filter((g) => !g.expired && sameScope(g.scope, { kind: 'namespace', cluster, namespace: ns })).flatMap((g) => g.actions));
  const heldBy = new Map(selected.map((ns) => [ns, heldIn(ns)]));
  // In a batch an action counts as held only where every namespace has it.
  const same = scope && !batch ? holderGrants.filter((g) => !g.expired && sameScope(g.scope, scope)) : [];
  const held = batch ? new Set(ALL_ACTIONS.filter((a) => selected.every((ns) => heldBy.get(ns)!.has(a)))) : new Set(same.flatMap((g) => g.actions));
  const hours = DURATIONS.find((x) => x.value === dur)!.hours;
  const target = scope && !batch ? mergeTarget(holderGrants, scope, hours === null ? null : now + hours * 3600_000, now, holder) : null;
  const longer = same.find((g) => g.holder.kind === holder);
  const wanted = actions.filter((a) => !held.has(a));
  // Namespaces that already have every wanted action are left out of a batch.
  const toSend = selected.filter((ns) => wanted.some((a) => !heldBy.get(ns)!.has(a)));
  const skipped = selected.filter((ns) => !toSend.includes(ns));
  const addNs = (ns: string) => (namespace ? setExtra((x) => [...x, ns]) : setNamespace(ns));
  const removeNs = (ns: string) => {
    if (ns !== namespace) return setExtra((x) => x.filter((n) => n !== ns));
    const next = extra.find((n) => n !== ns) ?? '';
    setNamespace(next);
    setExtra((x) => x.filter((n) => n !== ns && n !== next));
  };

  const toggleAction = (a: Action) => setActions((x) => (x.includes(a) ? x.filter((y) => y !== a) : [...x, a]));
  const togglePick = (w: CatalogWorkload) => setPicked((x) => (x.some((y) => y.kind === w.kind && y.name === w.name) ? x.filter((y) => !(y.kind === w.kind && y.name === w.name)) : [...x, w]));

  async function send(e: React.FormEvent) {
    e.preventDefault();
    setBusy(true);
    setErr(null);
    const sent: ScopeView = scope ?? (kind === 'argo_project' ? { kind, argoProject: project } : kind === 'namespace' ? { kind, cluster, namespace } : { kind, cluster, namespace, workloads: picked });
    const teamPart = forTeam ? { teamId: forTeam } : {};
    try {
      if (batch && toSend.length > 1) {
        await api.createAccessRequestBatch({ cluster, namespaces: toSend, actions: wanted, durationHours: hours, reason: reason.trim(), ...teamPart });
      } else {
        const one: ScopeView = batch ? { kind: 'namespace', cluster, namespace: toSend[0] } : sent;
        await api.createAccessRequest({ scope: one, actions: wanted, durationHours: hours, reason: reason.trim(), ...teamPart });
      }
      setExtra([]);
      setReason('');
      onSent();
    } catch (ex) {
      setErr(ex);
    } finally {
      setBusy(false);
    }
  }

  const fe = (k: string) => (fields[k] ? <span className="err-text" role="alert">{fields[k]}</span> : null);
  return (
    <aside className="card" style={{ width: 400, flex: 'none', maxWidth: '100%' }} aria-label="Request access" ref={formRef as React.RefObject<HTMLElement>}>
      <div className="hd">Request access</div>
      <form className="bd col" style={{ gap: 14 }} onSubmit={send}>
        {linkedHidden && (
          <div className="banner yel" role="note"><Icon name="warn" /><span>Access on cluster <b className="mono">{prefill!.cluster}</b> cannot be requested here. Ask an administrator.</span></div>
        )}
        {teams.length > 0 && (
          <label className="f">For
            <Select label="For" style={{ height: 30, width: '100%' }} value={forTeam} onChange={setForTeam}
              options={[{ value: 0, label: 'Myself' }, ...teams.map((t) => ({ value: t.id, label: `Team ${t.name}` }))]} />
            {team && <span className="hint">Approval grants the team: all {team.members.length} member{team.members.length === 1 ? '' : 's'} get this access.</span>}
          </label>
        )}
        <div className="f">Scope
          <Seg label="Scope" style={{ alignSelf: 'flex-start' }} value={kind} onChange={setKind} options={[...(ARGO_UI ? [{ value: 'argo_project' as const, label: 'Argo project' }] : []), { value: 'namespace' as const, label: 'Namespace' }, { value: 'workloads' as const, label: 'Workloads' }]} />
        </div>
        {kind === 'argo_project' ? (
          <label className="f">Argo project<ComboBox label="Argo project" value={project} onChange={setProject} invalid={!!fe('argoProject')} options={(projects.data ?? []).map((p) => ({ value: p }))} />{fe('argoProject')}</label>
        ) : (
          <>
            <label className="f">Cluster<Select label="Cluster" style={{ height: 30, width: '100%' }} value={cluster} onChange={setCluster}
              options={(clusters.data ?? []).map((c) => ({ value: c.name, label: `${c.name} (${c.environment})` }))} /></label>
            {kind === 'namespace' ? (
              <div className="f" role="group" aria-label="Namespaces">
                <span>Namespaces <span className="hint">Pick several to ask for them at once (up to {MAX_BATCH})</span></span>
                {selected.length > 0 && (
                  <div className="pill-row">
                    {selected.map((ns) => (
                      <span key={ns} className="chip ns-chip">
                        <span className="mono">{ns}</span>
                        <button type="button" className="linklike" aria-label={`Remove ${ns}`} onClick={() => removeNs(ns)}><Icon name="x" size={12} /></button>
                      </span>
                    ))}
                  </div>
                )}
                <ComboBox label="Add a namespace" value="" onChange={addNs} invalid={!!fe('namespace') || !!fe('namespaces')}
                  disabled={selected.length >= MAX_BATCH}
                  placeholder={nss.loading ? 'Loading…' : selected.length ? 'Add another namespace…' : 'Type to search…'}
                  options={(nss.data ?? []).filter((n) => !selected.includes(n)).map((n) => ({ value: n }))} />
                {batch && <span className="hint">One request per namespace; an administrator decides them together.</span>}
                {fe('namespace')}{fe('namespaces')}{fe('cluster')}
              </div>
            ) : (
              <label className="f">Namespace
                <ComboBox label="Namespace" value={namespace} onChange={(v) => { setNamespace(v); setWq(''); }} invalid={!!fe('namespace')}
                  placeholder={nss.loading ? 'Loading…' : 'Type to search…'} options={(nss.data ?? []).map((n) => ({ value: n }))} />
                {fe('namespace')}</label>
            )}
          </>
        )}
        {kind === 'workloads' && (
          <fieldset className="f" style={{ border: 0, padding: 0, margin: 0 }}>
            <legend style={{ padding: 0, marginBottom: 4 }}>Workloads</legend>
            {(wls.data ?? []).length > LIST_SEARCH_FROM && (
              <label className="search" style={{ width: '100%', height: 28, marginBottom: 6, background: 'var(--sf)' }}>
                <Icon name="search" />
                <input value={wq} onChange={(e) => setWq(e.target.value)} placeholder="Filter workloads" aria-label="Filter workloads" />
              </label>
            )}
            <div className="col" style={{ gap: 0, border: '1px solid var(--ln)', borderRadius: 6, maxHeight: 220, overflow: 'auto' }}>
              {(wls.data ?? []).length === 0 && <div className="muted" style={{ padding: 10 }}>{wls.loading ? 'Loading…' : 'No workloads found.'}</div>}
              {(wls.data ?? []).length > 0 && shownWls.length === 0 && <div className="muted" style={{ padding: 10 }}>No workload matches.</div>}
              {shownWls.map((w, i) => (
                <label key={`${w.kind}/${w.name}`} className="chk" style={{ padding: '8px 10px', borderTop: i ? '1px solid var(--ln)' : undefined }}>
                  <input type="checkbox" checked={picked.some((y) => y.kind === w.kind && y.name === w.name)} onChange={() => togglePick(w)} />
                  <KindTag kind={w.kind} /><span className="mono">{w.name}</span>
                </label>
              ))}
            </div>
            {fe('workloads')}
          </fieldset>
        )}
        <fieldset className="f" style={{ border: 0, padding: 0, margin: 0 }}>
          <legend style={{ padding: 0, marginBottom: 4 }}>Actions</legend>
          <div className="row wrap" style={{ gap: 14 }}>
            {ALL_ACTIONS.map((a) => {
              // Sync applies to whole Argo CD apps, which a workloads scope cannot cover.
              const off = isAppAction(a) && kind === 'workloads' && !actions.includes(a);
              const has = held.has(a);
              return (
                <label key={a} className="chk" title={has ? (team ? 'The team already has this here' : 'You already have this here') : off ? APP_SCOPE_HINT : undefined}>
                  <input type="checkbox" checked={has || actions.includes(a)} disabled={has || off} onChange={() => toggleAction(a)} />{actionOptionLabel(a)}
                </label>
              );
            })}
          </div>
          {batch && skipped.length > 0 && wanted.length > 0 && (
            <span className="hint" role="note">
              {team ? 'The team already has' : 'You already have'} {wanted.join(', ')} in {skipped.join(', ')}, so {skipped.length === 1 ? 'it is' : 'they are'} left out.
            </span>
          )}
          {batch && held.size > 0 && (
            <span className="hint" role="note">{team ? 'The team already has' : 'You already have'} {[...held].join(', ')} in all of them.</span>
          )}
          {same.length > 0 && (
            <span className="hint" role="note">
              {team ? 'The team already has' : 'You already have'} {[...held].join(', ')} here.{' '}
              {target
                ? `This request adds to that grant, which then ${hours === null ? 'becomes permanent' : `lasts ${DURATIONS.find((x) => x.value === dur)!.label} from approval`}.`
                : longer
                  ? `${team ? 'The team\'s' : 'Your'} grant here lasts longer than this request, so it becomes a separate grant.`
                  : 'You have it through a team; this request becomes a grant of your own.'}
            </span>
          )}
          {fe('actions')}
        </fieldset>
        <div className="f">Duration
          <Seg label="Duration" style={{ alignSelf: 'flex-start', flexWrap: 'wrap' }} value={dur} onChange={setDur} options={DURATIONS.map((x) => ({ value: x.value, label: x.label }))} />
        </div>
        <label className="f">Reason <span className="hint">Required</span>
          <textarea ref={reasonRef} className="input" rows={3} value={reason} onChange={(e) => setReason(e.target.value)} aria-invalid={!!fields.reason} placeholder="What are you investigating, and why do you need this access?" />
          {fe('reason')}
        </label>
        {err != null && !Object.keys(fields).length && <div className="banner red" role="alert">{errorText(err)}</div>}
        <div className="row" style={{ justifyContent: 'flex-end' }}>
          <button type="button" className="btn" onClick={onCancel}>Cancel</button>
          <button type="submit" className="btn pri" disabled={busy || !reason.trim() || wanted.length === 0 || (kind !== 'argo_project' && !namespace) || (batch && toSend.length === 0)}>
            {batch && toSend.length > 1 ? `Send ${toSend.length} requests` : 'Send request'}
          </button>
        </div>
      </form>
    </aside>
  );
}
