import { useEffect, useMemo, useRef, useState } from 'react';
import { useSearchParams } from 'react-router-dom';
import { ApiError, api, errorText } from '../api';
import type { Action, AccessRequest, CatalogWorkload, DecisionInfo, MyAccessGrant, ScopeView, WorkloadAction, WorkloadSummary } from '../api';
import { ActionChips, KindTag } from '../components/Badges';
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
import { SYNC_SCOPE_HINT, actionOptionLabel } from '../lib/grantForm';
import { fmtDateTime, fmtDay, fmtHours, fmtReopens, fmtWhen } from '../lib/time';
import { ARGO_UI } from '../lib/features';
import { breakGlassOn } from '../lib/breakGlass';

const ALL_ACTIONS: Action[] = ['logs', 'exec', 'restart', 'run-job', 'sync'];
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
  const infos: DecisionInfo[] = covered.flatMap((w) => g.actions.filter((a): a is WorkloadAction => a !== 'sync').map((a) => w.access[a]));
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
  const data = useApi(() => api.myAccess(), [me.breakGlass?.id]);
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
  }, [nsKeys, me.breakGlass?.id]);
  const apps = useApi(() => api.apps(), [me.breakGlass?.id]);

  useEffect(() => {
    if (sp.get('request') === '1') {
      formRef.current?.scrollIntoView({ block: 'start' });
      window.setTimeout(() => reasonRef.current?.focus(), 50);
    }
  }, [sp, data.data]);

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
        <button type="button" className="btn pri" onClick={() => { const n = new URLSearchParams(sp); n.set('request', '1'); setSp(n, { replace: true }); formRef.current?.scrollIntoView({ block: 'start' }); window.setTimeout(() => reasonRef.current?.focus(), 50); }}><Icon name="plus" />Request access</button>
      </PageHeader>

      {me.breakGlass && (
        <div className="banner red"><Icon name="warn" /><span><b>Break-glass is active</b> until {fmtDateTime(me.breakGlass.expiresAt)}. It overrides time windows only; it never adds access.</span>
          <span className="grow" /><button type="button" className="btn sm" onClick={() => void bg.end()}>End now</button></div>
      )}

      <div className="two">
        <div className="col grow" style={{ gap: 16, minWidth: 0 }}>
          <div className="tabs" role="tablist">
            <button type="button" role="tab" aria-selected={tab === 'grants'} className={`tabbtn${tab === 'grants' ? ' on' : ''}`} onClick={() => setTab('grants')}>Grants <span className="c">{grants.length}</span></button>
            <button type="button" role="tab" aria-selected={tab === 'requests'} className={`tabbtn${tab === 'requests' ? ' on' : ''}`} onClick={() => setTab('requests')}>Requests <span className="c">{d.requests.length}</span></button>
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
                          <td><ActionChips actions={g.actions} /></td>
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
                      {d.requests.map((r) => <RequestRow key={r.id} r={r} now={now} />)}
                    </tbody>
                  </table>
                </div>
              </div>
            </div>
          )}
        </div>

        <RequestForm
          formRef={formRef} reasonRef={reasonRef} grants={d.grants} now={now}
          onSent={() => { toast('Request sent. An administrator will review it.'); data.reload(); setTab('requests'); const n = new URLSearchParams(sp); n.delete('request'); setSp(n, { replace: true }); }}
          onCancel={() => { const n = new URLSearchParams(sp); n.delete('request'); setSp(n, { replace: true }); }}
        />
      </div>
    </>
  );
}

function RequestRow({ r, now }: { r: AccessRequest; now: number }) {
  const tone = r.status === 'pending' ? 'warn' : r.status === 'rejected' ? 'err' : r.status === 'approved' ? 'ok' : '';
  return (
    <tr className={r.status === 'expired' ? 'off' : undefined}>
      <td>{fmtWhen(r.createdAt, now)}</td>
      <td><span className="mono">{r.scope.kind === 'argo_project' ? r.scope.argoProject : r.scope.kind === 'workloads' && r.scope.workloads?.length === 1 ? r.scope.workloads[0].name : r.scope.namespace}</span>{r.scope.cluster ? <> · {r.scope.cluster}</> : null}</td>
      <td><ActionChips actions={r.actions} /></td>
      <td>{fmtHours(r.durationHours)}</td>
      <td>
        <span className={`chip ${tone}`}>{r.status.charAt(0).toUpperCase() + r.status.slice(1)}</span>
        {r.decisionNote && <div className="muted" style={{ fontSize: 11.5, marginTop: 2 }}>Note: {r.decisionNote}</div>}
      </td>
    </tr>
  );
}

/**
 * Where the scope matches grants the user already has, their actions are
 * shown as held and only new ones are requested; the hint says whether
 * approval adds to the user's own grant (see lib/requestMerge) or creates one.
 */
function RequestForm({ formRef, reasonRef, grants, now, onSent, onCancel }: {
  formRef: React.RefObject<HTMLElement>; reasonRef: React.RefObject<HTMLTextAreaElement>; grants: MyAccessGrant[]; now: number; onSent: () => void; onCancel: () => void;
}) {
  const [kind, setKind] = useState<ScopeView['kind']>('namespace');
  const [project, setProject] = useState('');
  const [cluster, setCluster] = useState('');
  const [namespace, setNamespace] = useState('');
  const [picked, setPicked] = useState<CatalogWorkload[]>([]);
  const [actions, setActions] = useState<Action[]>(['logs']);
  const [dur, setDur] = useState<number | 'perm'>('perm');
  const [reason, setReason] = useState('');
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<unknown>(null);
  const projects = useApi(() => api.argoProjects(), [], { enabled: ARGO_UI });
  const clusters = useApi(() => api.catalogClusters(), []);
  const nss = useApi(() => api.catalogNamespaces(cluster), [cluster], { enabled: !!cluster });
  const wls = useApi(() => api.catalogWorkloads(cluster, namespace), [cluster, namespace], { enabled: !!cluster && !!namespace && kind === 'workloads' });
  const fields = err instanceof ApiError ? err.fields : {};

  useEffect(() => { if (!project && projects.data?.[0]) setProject(projects.data[0]); }, [projects.data, project]);
  useEffect(() => { if (!cluster && clusters.data?.[0]) setCluster(clusters.data[0].name); }, [clusters.data, cluster]);
  useEffect(() => { setNamespace(nss.data?.[0] ?? ''); setPicked([]); }, [nss.data]);
  useEffect(() => { setPicked([]); }, [namespace]);

  const scope: ScopeView | null = kind === 'argo_project' ? (project ? { kind, argoProject: project } : null)
    : !cluster || !namespace ? null : kind === 'namespace' ? { kind, cluster, namespace } : picked.length ? { kind, cluster, namespace, workloads: picked } : null;
  const same = scope ? grants.filter((g) => !g.expired && sameScope(g.scope, scope)) : [];
  const held = new Set(same.flatMap((g) => g.actions));
  const hours = DURATIONS.find((x) => x.value === dur)!.hours;
  const target = scope ? mergeTarget(grants, scope, hours === null ? null : now + hours * 3600_000, now) : null;
  const longer = same.find((g) => g.holder.kind === 'user');
  const wanted = actions.filter((a) => !held.has(a));

  const toggleAction = (a: Action) => setActions((x) => (x.includes(a) ? x.filter((y) => y !== a) : [...x, a]));
  const togglePick = (w: CatalogWorkload) => setPicked((x) => (x.some((y) => y.kind === w.kind && y.name === w.name) ? x.filter((y) => !(y.kind === w.kind && y.name === w.name)) : [...x, w]));

  async function send(e: React.FormEvent) {
    e.preventDefault();
    setBusy(true);
    setErr(null);
    const sent: ScopeView = scope ?? (kind === 'argo_project' ? { kind, argoProject: project } : kind === 'namespace' ? { kind, cluster, namespace } : { kind, cluster, namespace, workloads: picked });
    try {
      await api.createAccessRequest({ scope: sent, actions: wanted, durationHours: hours, reason: reason.trim() });
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
        <div className="f">Scope
          <Seg label="Scope" style={{ alignSelf: 'flex-start' }} value={kind} onChange={setKind} options={[...(ARGO_UI ? [{ value: 'argo_project' as const, label: 'Argo project' }] : []), { value: 'namespace' as const, label: 'Namespace' }, { value: 'workloads' as const, label: 'Workloads' }]} />
        </div>
        {kind === 'argo_project' ? (
          <label className="f">Argo project<select className="sel" style={{ height: 30 }} value={project} onChange={(e) => setProject(e.target.value)}>{(projects.data ?? []).map((p) => <option key={p}>{p}</option>)}</select>{fe('argoProject')}</label>
        ) : (
          <>
            <label className="f">Cluster<select className="sel" style={{ height: 30 }} value={cluster} onChange={(e) => setCluster(e.target.value)}>{(clusters.data ?? []).map((c) => <option key={c.name} value={c.name}>{c.name} ({c.environment})</option>)}</select></label>
            <label className="f">Namespace<select className="sel" style={{ height: 30 }} value={namespace} onChange={(e) => setNamespace(e.target.value)}>{(nss.data ?? []).map((n) => <option key={n}>{n}</option>)}</select>{fe('namespace')}</label>
          </>
        )}
        {kind === 'workloads' && (
          <fieldset className="f" style={{ border: 0, padding: 0, margin: 0 }}>
            <legend style={{ padding: 0, marginBottom: 4 }}>Workloads</legend>
            <div className="col" style={{ gap: 0, border: '1px solid var(--ln)', borderRadius: 6, maxHeight: 220, overflow: 'auto' }}>
              {(wls.data ?? []).length === 0 && <div className="muted" style={{ padding: 10 }}>{wls.loading ? 'Loading…' : 'No workloads found.'}</div>}
              {(wls.data ?? []).map((w, i) => (
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
              const off = a === 'sync' && kind === 'workloads' && !actions.includes(a);
              const has = held.has(a);
              return (
                <label key={a} className="chk" title={has ? 'You already have this here' : off ? SYNC_SCOPE_HINT : undefined}>
                  <input type="checkbox" checked={has || actions.includes(a)} disabled={has || off} onChange={() => toggleAction(a)} />{actionOptionLabel(a)}
                </label>
              );
            })}
          </div>
          {same.length > 0 && (
            <span className="hint" role="note">
              You already have {[...held].join(', ')} here.{' '}
              {target
                ? `This request adds to that grant, which then ${hours === null ? 'becomes permanent' : `lasts ${DURATIONS.find((x) => x.value === dur)!.label} from approval`}.`
                : longer
                  ? 'Your grant here lasts longer than this request, so it becomes a separate grant.'
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
          <button type="submit" className="btn pri" disabled={busy || !reason.trim() || wanted.length === 0}>Send request</button>
        </div>
      </form>
    </aside>
  );
}
