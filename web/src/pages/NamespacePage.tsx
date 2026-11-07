import { useState } from 'react';
import { Link, useNavigate, useParams, useSearchParams } from 'react-router-dom';
import { api } from '../api';
import { ApiError } from '../api';
import type { K8sEvent, NamespaceApp, TrafficGraph, WorkloadAction, WorkloadSummary } from '../api';
import { ActionButton } from '../components/ActionButton';
import { ArgoAppPanel } from '../components/ArgoAppPanel';
import { ActionChips, EnvBadge, HealthChip, KindTag, SyncChip } from '../components/Badges';
import { Bump } from '../components/Bump';
import { Empty, ErrorBanner, Loading, Skeleton } from '../components/Feedback';
import { Icon } from '../components/Icon';
import { RunCronDialog, SyncAppDialog } from '../components/OperationDialogs';
import { TrafficFlow } from '../components/TrafficFlow';
import { Crumbs, PageHeader } from '../components/PageHeader';
import { WhoHasAccess } from '../components/WhoHasAccess';
import { useAuth } from '../context/auth';
import { useLive } from '../context/live';
import { useSetShellCtx } from '../context/shell';
import { eventKey, eventSig, rowClass, useAnimatedList } from '../hooks/useAnimatedList';
import type { AnimatedRow } from '../hooks/useAnimatedList';
import { useApi } from '../hooks/useApi';
import { useUrlState } from '../hooks/useUrlState';
import type { ApiState } from '../hooks/useApi';
import { useNow } from '../hooks/useNow';
import { appSyncTarget, grantedOrClosed } from '../lib/decision';
import { cronJobsUrl, logsUrl, workloadUrl } from '../lib/routes';
import { fmtDateTime, fmtRelative } from '../lib/time';
import { inClusterNodes, trafficRules, withoutNodes } from '../lib/traffic';

const NO_WORKLOADS: WorkloadSummary[] = [];

type Tab = 'workloads' | 'events' | 'traffic';

/** The events and traffic of the whole namespace need logs on all of it. */
const denied = (err: unknown) => err instanceof ApiError && err.status === 403;

export function NamespacePage() {
  const { cluster = '', ns = '' } = useParams();
  const { me } = useAuth();
  const [{ q, kind }, setFilters] = useUrlState({ q: '', kind: '' });
  const [run, setRun] = useState<WorkloadSummary | null>(null);
  const navigate = useNavigate();
  const list = useApi(() => api.workloads(cluster, ns), [cluster, ns, me.breakGlass?.id]);
  const nss = useApi(() => api.namespaces(), [me.breakGlass?.id]);
  const info = nss.data?.find((n) => n.cluster === cluster && n.namespace === ns);
  const environment = info?.environment ?? list.data?.[0]?.environment;
  useSetShellCtx(environment ? cluster : undefined, environment);
  const [sp] = useSearchParams();
  const want = sp.get('tab');
  const tab: Tab = want === 'events' || want === 'traffic' ? want : 'workloads';
  // Events and traffic can be large: each is fetched the first time its tab
  // is opened (per namespace) and kept when switching tabs; Refresh reloads.
  const key = `${cluster}/${ns}`;
  const [opened, setOpened] = useState({ key, tabs: [tab] });
  if (opened.key !== key) setOpened({ key, tabs: [tab] });
  else if (!opened.tabs.includes(tab)) setOpened({ key, tabs: [...opened.tabs, tab] });
  const events = useApi(() => api.namespaceEvents(cluster, ns), [cluster, ns, me.breakGlass?.id], { enabled: opened.tabs.includes('events') });
  const traffic = useApi(() => api.namespaceTraffic(cluster, ns), [cluster, ns, me.breakGlass?.id], { enabled: opened.tabs.includes('traffic') });
  useLive({ cluster, namespace: ns }, (changes) => {
    const all = changes.length === 0;
    if (all || changes.some((c) => c.type !== 'event')) list.reload();
    if (opened.tabs.includes('events') && (all || changes.some((c) => c.type === 'event' || c.type === 'pod_restarted'))) events.reload();
  });
  const warnings = events.data?.filter((e) => e.type === 'Warning').length ?? 0;
  const problems = traffic.data?.nodes.filter((n) => n.problem).length ?? 0;

  const all = list.data ?? NO_WORKLOADS;
  const kinds = [...new Set(all.map((w) => w.kind))].sort();
  const shown = all.filter((w) => (!kind || w.kind === kind) && (!q || w.name.toLowerCase().includes(q.toLowerCase())));
  const rows = useAnimatedList(list.data ? shown : undefined, (w) => `${w.kind}/${w.name}`, (w) => `${w.ready}/${w.desired}|${w.suspended}`, `${key}|${kind}|${q}`);
  const hasCron = all.some((w) => w.kind === 'CronJob');

  return (
    <>
      <Crumbs items={[{ label: 'Workloads', to: '/' }, { label: cluster, mono: true }, { label: ns, mono: true }]} />
      <PageHeader
        title={<span className="mono">{ns}</span>}
        subtitle={<>On <span className="mono">{cluster}</span>{list.data ? ` · ${all.length} workload${all.length === 1 ? '' : 's'} you can use` : ''}</>}
      >
        {info && <EnvBadge env={info.environment} />}
        <WhoHasAccess cluster={cluster} namespace={ns} />
      </PageHeader>

      <div className="tabs" role="tablist" style={{ marginBottom: 12 }}>
        <TabLink id="workloads" tab={tab}>Workloads {list.data && <span className="c"><Bump value={all.length} /></span>}</TabLink>
        <TabLink id="events" tab={tab}>
          Events {warnings > 0 && <span className="c bad" title={`${warnings} warning${warnings === 1 ? '' : 's'}`}><Bump value={warnings} /></span>}
        </TabLink>
        <TabLink id="traffic" tab={tab}>
          Traffic {problems > 0 && <span className="c bad" title={`${problems} problem${problems === 1 ? '' : 's'}`}>{problems}</span>}
        </TabLink>
      </div>

      {tab === 'events' ? <NamespaceEvents events={events} scope={key} /> : tab === 'traffic' ? <NamespaceTraffic traffic={traffic} cluster={cluster} ns={ns} /> : (
        <>
          <ArgoApps cluster={cluster} ns={ns} />

          <div className="row wrap">
            <select className="sel" aria-label="Kind" value={kind} onChange={(e) => setFilters({ kind: e.target.value })}>
              <option value="">All kinds</option>{kinds.map((k) => <option key={k}>{k}</option>)}
            </select>
            <label className="search" style={{ width: 280, height: 28, background: 'var(--sf)' }}>
              <Icon name="search" />
              <input value={q} onChange={(e) => setFilters({ q: e.target.value })} placeholder="Filter by name" aria-label="Filter by name" />
            </label>
            <span className="grow" />
            {list.data && <span className="muted" aria-live="polite">Showing {shown.length} of {all.length}</span>}
            {hasCron && <Link className="btn" to={cronJobsUrl(cluster, ns)} title="Recent runs of the namespace's CronJobs">Job history</Link>}
          </div>

          {list.loading && !list.data ? <Loading what="workloads" /> : list.error ? <ErrorBanner error={list.error} onRetry={list.reload} /> : (
            <div className="card">
              <div className="tbl-wrap">
                <table className="tbl">
                  <thead><tr><th>Workload</th><th>Kind</th><th>Status</th><th>You can</th><th /></tr></thead>
                  <tbody>
                    {rows.length === 0 && (
                      <tr><td colSpan={5} className="empty">{all.length === 0 ? 'No workloads you can use in this namespace.' : 'No workloads match these filters.'}</td></tr>
                    )}
                    {rows.map(({ item: w, key: rk, anim }) => {
                      const loc = { cluster, namespace: ns, kind: w.kind, name: w.name };
                      return (
                        <tr key={rk} className={rowClass(anim)}>
                          <td><Link className="mono" to={workloadUrl(loc)}><b style={{ fontWeight: 500 }}>{w.name}</b></Link></td>
                          <td><KindTag kind={w.kind} /></td>
                          <td className="mono muted">{w.kind === 'CronJob' ? (w.suspended ? 'suspended' : w.schedule) : <><Bump value={`${w.ready}/${w.desired}`} /> ready</>}</td>
                          <td><ActionChips actions={(Object.keys(w.access) as WorkloadAction[]).filter((a) => w.access[a].allowed)} /></td>
                          <td style={{ textAlign: 'right' }}>
                            <div className="row" style={{ justifyContent: 'flex-end', gap: 4 }}>
                              {/* Only for users with a run-job grant; a closed window shows why. */}
                              {w.kind === 'CronJob' && grantedOrClosed(w.access['run-job']) && (
                                <ActionButton action="run-job" decision={w.access['run-job']} label="Run now" icon="bolt" size="sm" target={w.name} hint="inline" request={{ cluster, namespace: ns, workload: { kind: w.kind, name: w.name } }} onClick={() => setRun(w)} />
                              )}
                              <Link className="btn ghost sm" to={logsUrl(loc)}>Logs</Link>
                              <Link className="btn ghost sm" to={workloadUrl(loc)}>Open</Link>
                            </div>
                          </td>
                        </tr>
                      );
                    })}
                  </tbody>
                </table>
              </div>
            </div>
          )}
        </>
      )}
      {run && (
        <RunCronDialog
          cron={run}
          onClose={(changed) => { setRun(null); if (changed) list.reload(); }}
          onViewRunning={() => navigate(cronJobsUrl(cluster, ns))}
        />
      )}
    </>
  );
}

function TabLink({ id, tab, children }: { id: Tab; tab: Tab; children: React.ReactNode }) {
  // Keeps the other parameters (the tabs' filters).
  const [sp] = useSearchParams();
  const n = new URLSearchParams(sp);
  n.set('tab', id);
  return <Link role="tab" aria-selected={tab === id} className={tab === id ? 'on' : undefined} to={`?${n}`} replace>{children}</Link>;
}

/**
 * The Argo CD applications deploying to the namespace, with their sync state.
 * Hidden when there are none or they cannot be loaded (Argo CD is enrichment).
 * Every app has a Sync button, disabled with the reason when the viewer may
 * not sync it; a denial for another namespace the app deploys to names that
 * namespace.
 */
function ArgoApps({ cluster, ns }: { cluster: string; ns: string }) {
  const { me } = useAuth();
  const now = useNow(30_000);
  const apps = useApi(() => api.namespaceApps(cluster, ns), [cluster, ns, me.breakGlass?.id]);
  const [syncing, setSyncing] = useState<NamespaceApp | null>(null);
  const [shown, setShown] = useState<NamespaceApp | null>(null);
  if (!apps.data?.length) return null;
  const base = me.settings.argoUrl;
  const link = (a: NamespaceApp) => `${base}/applications/${encodeURIComponent(a.appNamespace || 'argocd')}/${encodeURIComponent(a.name)}`;
  return (
    <div className="card" style={{ marginBottom: 12 }}>
      <div className="hd">
        Argo CD
        {me.argo.stale && <small>· data from {fmtRelative(me.argo.fetchedAt, now)}, may be out of date</small>}
      </div>
      <div className="tbl-wrap">
        <table className="tbl">
          <thead><tr><th>Application</th><th>Project</th><th>Health</th><th>Sync</th><th>Last sync</th><th /></tr></thead>
          <tbody>
            {apps.data.map((a) => (
              <tr key={`${a.appNamespace}/${a.name}`}>
                <td>
                  <span className="row" style={{ gap: 6 }}>
                    <button type="button" className="linklike blue mono" title="Status, history and changes" onClick={() => setShown(a)}><b style={{ fontWeight: 500 }}>{a.name}</b></button>
                    {base && <a href={link(a)} target="_blank" rel="noreferrer" title="Open in Argo CD" aria-label={`Open ${a.name} in Argo CD`}><Icon name="ext" /></a>}
                  </span>
                </td>
                <td className="mono muted">{a.project}</td>
                <td><HealthChip health={a.health} /></td>
                <td><SyncChip sync={a.sync} /></td>
                <td><LastSync app={a} now={now} /></td>
                <td style={{ textAlign: 'right' }}>
                  <ActionButton
                    action="sync" decision={a.access.sync} label="Sync" icon="refresh" size="sm"
                    target={appSyncTarget(a, ns)} request={{ cluster, namespace: a.syncNamespace || ns }}
                    onClick={() => setSyncing(a)}
                  />
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      {shown && <ArgoAppPanel app={shown} cluster={cluster} ns={ns} argoLink={base ? link(shown) : ''} onClose={() => setShown(null)} onChanged={apps.reload} />}
      {syncing && (
        <SyncAppDialog app={syncing} cluster={cluster} namespace={ns} onClose={(changed) => { setSyncing(null); if (changed) apps.reload(); }} />
      )}
    </div>
  );
}

/**
 * How traffic from outside reaches the namespace (Ingresses, Gateway API
 * routes, LoadBalancer Services). What only traffic inside the cluster
 * reaches (Services nothing exposes, Istio waypoints and what they route to)
 * is shown under a divider and can be hidden; while hidden, its problems are
 * counted on the toggle.
 */
function NamespaceTraffic({ traffic, cluster, ns }: { traffic: ApiState<TrafficGraph>; cluster: string; ns: string }) {
  const [allOpen, setAllOpen] = useState(false);
  const [{ inCluster: withInCluster }, setView] = useUrlState({ inCluster: true });
  if (denied(traffic.error)) return <WholeNamespaceOnly what="The traffic graph" />;
  if (traffic.error) return <ErrorBanner error={traffic.error} onRetry={traffic.reload} />;
  const full = traffic.data;
  const inCluster = full ? inClusterNodes(full) : new Set<string>();
  const services = full?.nodes.filter((n) => n.kind === 'Service' && inCluster.has(n.id)).length ?? 0;
  const g = full && (withInCluster ? full : withoutNodes(full, inCluster));
  const problems = g?.nodes.filter((n) => n.problem).length ?? 0;
  const hiddenProblems = withInCluster ? 0 : full?.nodes.filter((n) => n.problem && inCluster.has(n.id)).length ?? 0;
  const hasRules = !!g?.nodes.some((n) => trafficRules(g, n).length > 0);
  const plural = (n: number, w: string) => `${n} ${w}${n === 1 ? '' : 's'}`;
  return (
    <div className="card">
      <div className="hd">
        Traffic
        {g && problems > 0 && <small>· <span style={{ color: 'var(--red)' }}>{plural(problems, 'problem')}</span></small>}
        <span className="grow" />
        {services > 0 && (
          <button
            type="button" className="btn ghost sm" aria-pressed={withInCluster} onClick={() => setView({ inCluster: !withInCluster })}
            title="Services that no Ingress, Gateway or load balancer leads to: other workloads call them inside the cluster, directly or through an Istio waypoint."
          >
            {withInCluster ? 'Hide' : 'Show'} in-cluster Services ({services})
            {hiddenProblems > 0 && <span style={{ color: 'var(--red)' }}> · {plural(hiddenProblems, 'problem')}</span>}
          </button>
        )}
        {hasRules && (
          <button type="button" className="btn ghost sm" aria-pressed={allOpen} onClick={() => setAllOpen((v) => !v)}>
            {allOpen ? 'Collapse all routes' : 'Expand all routes'}
          </button>
        )}
        <button type="button" className="btn ghost sm" onClick={traffic.reload} disabled={traffic.loading}>Refresh</button>
      </div>
      {!g ? <Skeleton rows={2} /> : g.nodes.length === 0
        ? (
          <div className="empty">
            Nothing in this namespace is exposed through an Ingress, a Gateway API route or a LoadBalancer Service
            {services > 0 ? `; ${plural(services, 'Service')} ${services === 1 ? 'is' : 'are'} reachable inside the cluster.` : '.'}
          </div>
        )
        : <TrafficFlow graph={g} cluster={cluster} ns={ns} allOpen={allOpen} inCluster={withInCluster ? inCluster : undefined} />}
    </div>
  );
}

/**
 * Recent Kubernetes events of the whole namespace, warnings only by default.
 * The events of a single workload are on its own page.
 */
function NamespaceEvents({ events, scope }: { events: ApiState<K8sEvent[]>; scope: string }) {
  const now = useNow(30_000);
  const [{ warnings: warningsOnly }, setView] = useUrlState({ warnings: true });
  const rows = useAnimatedList(events.data ? (warningsOnly ? events.data.filter((e) => e.type === 'Warning') : events.data) : undefined, eventKey, eventSig, `${scope}|${warningsOnly}`);
  if (denied(events.error)) return <WholeNamespaceOnly what="The events of the whole namespace" />;
  if (events.error) return <ErrorBanner error={events.error} onRetry={events.reload} />;

  const list: K8sEvent[] = events.data ?? [];
  const warnings = list.filter((e) => e.type === 'Warning').length;
  const empty = list.length === 0
    ? 'No recent events. Kubernetes keeps events for about an hour.'
    : `No warnings (${list.length} normal event${list.length === 1 ? '' : 's'} hidden).`;
  return (
    <div className="card">
      <div className="hd">
        Events
        {events.data && <small>· {warnings} warning{warnings === 1 ? '' : 's'} of {list.length}</small>}
        <span className="grow" />
        <label className="row" style={{ gap: 6, fontWeight: 400 }} title="Normal events are routine (Pulled, Started, Scheduled); warnings are problems such as BackOff or FailedCreate">
          <input type="checkbox" checked={warningsOnly} onChange={(e) => setView({ warnings: e.target.checked })} />
          Warnings only
        </label>
        <button type="button" className="btn ghost sm" onClick={events.reload} disabled={events.loading}>Refresh</button>
      </div>
      {!events.data ? <Skeleton rows={2} /> : <EventsTable rows={rows} empty={empty} now={now} />}
    </div>
  );
}

function WholeNamespaceOnly({ what }: { what: string }) {
  return (
    <div className="card">
      <Empty>{what} needs access to the whole namespace; your grants cover single workloads. Each workload's events are on its own page.</Empty>
    </div>
  );
}

function EventsTable({ rows, empty, now }: { rows: AnimatedRow<K8sEvent>[]; empty: string; now: number }) {
  return (
    <div className="tbl-wrap">
      <table className="tbl">
        <thead><tr><th>Type</th><th>Reason</th><th>Object</th><th>Message</th><th>Last seen</th></tr></thead>
        <tbody>
          {rows.length === 0 && <tr><td colSpan={5} className="empty">{empty}</td></tr>}
          {rows.map(({ item: e, key, anim }) => (
            <tr key={key} className={rowClass(anim)}>
              <td><span className={`chip ${e.type === 'Warning' ? 'err' : ''}`}>{e.type}</span></td>
              <td><b style={{ fontWeight: 500 }}>{e.reason}</b>{e.count > 1 && <span className="muted"> ×<Bump value={e.count} /></span>}</td>
              <td className="mono muted">{e.object}</td>
              <td>{e.message}</td>
              <td className="muted" style={{ whiteSpace: 'nowrap' }} title={fmtDateTime(e.lastSeen)}>{fmtRelative(e.lastSeen, now)}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

function LastSync({ app, now }: { app: NamespaceApp; now: number }) {
  const { lastSyncAt: at, lastSyncPhase: phase } = app;
  if (phase === 'Running' || phase === 'Terminating') return <span className="chip info"><span className="dot info" />Syncing</span>;
  if (!at) return <span className="muted">Never</span>;
  const failed = phase === 'Failed' || phase === 'Error';
  return (
    <span title={fmtDateTime(at)}>
      {fmtRelative(at, now)}
      {failed && <> <span className="chip err">{phase}</span></>}
    </span>
  );
}
