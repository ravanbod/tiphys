import type {
  AccessRequest, Action, AppSummary, AuditEvent, BreakGlass, BreakGlassRequest, CronJobSummary, DecisionExplain, ExecSessionView,
  GrantInput, GrantView, JobSummary, K8sEvent, Me, NamespaceApp, NamespaceSummary, Pod, Role, ScopeView, SettingsView,
  ArgoAppStatus, ArgoDeploy, ArgoDiff, TrafficGraph, TrafficNode, WorkloadDetail, WorkloadKind, WorkloadSummary,
} from '../api/types';
import { kindFromSegment } from '../api/query';
import { CSRF, DAY, HOUR, MIN, currentUser, getDb, nextId, personaRole } from './db';
import type { MockUser, NsDef, WlDef } from './db';
import { WORKLOAD_ACTIONS, accessMap, covers, decide, envOf, nsActions, userGrants } from './decide';
import { buildCast, commandList } from './scripts';
import { mergeTarget } from '../lib/requestMerge';
import { ACTIONS } from '../lib/grantForm';
import { terminateSocket } from './sockets';

export class MockError extends Error {
  constructor(public status: number, public code: string, message: string, public details?: Record<string, unknown>) {
    super(message);
  }
}
const iso = (t: number) => new Date(t).toISOString();
const forbiddenRole = () => new MockError(403, 'forbidden_role', 'Your role does not allow this');
const notFound = (what = 'Not found') => new MockError(404, 'not_found', what);
const invalid = (fields: Record<string, string>, message = 'Please fix the highlighted fields') => new MockError(422, 'invalid', message, { fields });

export interface Ctx {
  params: Record<string, string>;
  query: URLSearchParams;
  body: Record<string, unknown>;
  user: MockUser;
  method: string;
}
type Handler = (c: Ctx) => unknown;
interface Route { method: string; re: RegExp; keys: string[]; h: Handler }
const routes: Route[] = [];
function on(method: string, pattern: string, h: Handler) {
  const keys: string[] = [];
  const re = new RegExp('^' + pattern.replace(/:([a-zA-Z]+)/g, (_m, k: string) => { keys.push(k); return '([^/]+)'; }) + '/?$');
  routes.push({ method, re, keys, h });
}

export function dispatch(method: string, path: string, query: URLSearchParams, body: Record<string, unknown>): { status: number; data: unknown } {
  const user = currentUser();
  for (const r of routes) {
    if (r.method !== method) continue;
    const m = r.re.exec(path);
    if (!m) continue;
    const params: Record<string, string> = {};
    r.keys.forEach((k, i) => (params[k] = decodeURIComponent(m[i + 1])));
    try {
      const data = r.h({ params, query, body, user, method });
      if (data === undefined) return { status: 204, data: undefined };
      if (data && typeof data === 'object' && '__status' in (data as object)) {
        const { __status, ...rest } = data as { __status: number; data: unknown };
        return { status: __status, data: rest.data };
      }
      return { status: 200, data };
    } catch (e) {
      if (e instanceof MockError) return { status: e.status, data: { error: { code: e.code, message: e.message, details: e.details } } };
      throw e;
    }
  }
  return { status: 404, data: { error: { code: 'not_found', message: `No mock route for ${method} ${path}` } } };
}

// ---- guards
const isAdmin = (u: MockUser) => u.role === 'admin';
function needStaff(u: MockUser) { if (u.role === 'user') throw forbiddenRole(); }
function needAdmin(c: Ctx) { if (!isAdmin(c.user)) throw forbiddenRole(); }
function adminRead(c: Ctx) { if (c.user.role === 'user') throw forbiddenRole(); }

function denied(info: ReturnType<typeof decide>): MockError {
  return new MockError(403, 'denied', 'Access denied', info as unknown as Record<string, unknown>);
}

// ---- helpers over the fake cluster
function clusterGuard(cluster: string) {
  const c = getDb().clusters.find((x) => x.name === cluster);
  if (!c) throw notFound(`Unknown cluster ${cluster}`);
  if (!c.reachable) throw new MockError(502, 'cluster_error', `Cluster ${cluster} is unreachable right now (last seen ${Math.max(1, Math.round((Date.now() - Date.parse(c.lastCheckAt ?? '')) / MIN))} min ago).`);
  if (!c.usable) throw new MockError(502, 'cluster_error', `Cluster ${cluster} is not usable: its credential failed the privilege check.`);
}
function findNs(cluster: string, ns: string): NsDef {
  const d = getDb();
  if (d.protectedNamespaces.includes(ns)) throw denied(decide(currentUser(), cluster, ns, null, null, 'logs'));
  const n = d.namespaces.find((x) => x.cluster === cluster && x.namespace === ns);
  if (!n) throw notFound(`Namespace ${ns} not found on ${cluster}`);
  return n;
}
function findWl(cluster: string, ns: string, kind: string, name: string): { ns: NsDef; wl: WlDef } {
  const n = findNs(cluster, ns);
  const k = kindFromSegment(kind) as WorkloadKind;
  const wl = n.workloads.find((w) => w.kind === k && w.name === name);
  if (!wl) throw notFound(`${k} ${name} not found`);
  return { ns: n, wl };
}
function summary(u: MockUser, n: NsDef, w: WlDef): WorkloadSummary {
  const s: WorkloadSummary = {
    kind: w.kind, name: w.name, namespace: n.namespace, cluster: n.cluster, environment: envOf(n.cluster, n.namespace),
    desired: w.desired, ready: w.ready, access: accessMap(u, n.cluster, n.namespace, w.kind, w.name),
  };
  if (w.kind === 'CronJob') { s.suspended = !!w.suspended; s.schedule = w.schedule; }
  return s;
}
function canUse(s: Pick<WorkloadSummary, 'access'>): boolean {
  return WORKLOAD_ACTIONS.some((a) => s.access[a].allowed || s.access[a].reason === 'outside_window' || s.access[a].reason === 'holiday');
}
function requireAction(u: MockUser, n: NsDef, w: WlDef, action: Action) {
  const info = decide(u, n.cluster, n.namespace, w.kind, w.name, action);
  if (!info.allowed) throw denied(info);
  return info;
}

const POD_SUFFIX = ['2kq8x', '9mzt5', 'xv7wd', '4tn2p', '8wq5c', 'k9xzd', 'q8zcl', 'h7r2s'];
function podsFor(n: NsDef, w: WlDef): Pod[] {
  const now = Date.now();
  const rs = w.kind === 'StatefulSet' ? '' : '7d9c6b5f4-';
  const count = w.desired || 1;
  const pods: Pod[] = Array.from({ length: count }, (_, i) => {
    const name = w.kind === 'StatefulSet' ? `${w.name}-${i}` : `${w.name}-${rs}${POD_SUFFIX[i % POD_SUFFIX.length]}`;
    const oom = w.name === 'payments-api' && n.cluster === 'prod-1' && i === 1;
    const notReady = w.ready < w.desired && i >= w.ready;
    const two = w.name === 'payments-api';
    const container = (cn: string, restart = 0, isOom = false) => ({
      name: cn, image: `${w.name}:2026.10.17-4`, ready: !notReady, state: notReady ? 'Waiting' : 'Running', stateReason: notReady ? 'CrashLoopBackOff' : '',
      restartCount: restart, lastState: isOom ? 'Terminated' : null, lastReason: isOom ? 'OOMKilled' : null, lastExitCode: isOom ? 137 : null,
      lastStartedAt: isOom ? iso(now - 16 * HOUR) : null, lastFinishedAt: isOom ? iso(now - 2 * HOUR) : null, memoryLimit: isOom ? '512Mi' : '256Mi', init: false,
    });
    return {
      name, phase: notReady ? 'Running' : 'Running', ready: notReady ? `${two ? 1 : 0}/${two ? 2 : 1}` : `${two ? 2 : 1}/${two ? 2 : 1}`, restarts: oom ? 1 : notReady ? 14 : 0,
      node: ['node-a3', 'node-b1', 'node-a1'][i % 3], createdAt: iso(now - (3 * DAY + 12 * HOUR)),
      lastTermination: oom ? 'OOMKilled' : null, lastTerminatedAt: oom ? iso(now - 2 * HOUR) : null,
      containers: [container('app', oom ? 1 : notReady ? 14 : 0, oom), ...(two ? [{ ...container('metrics-sidecar'), memoryLimit: '64Mi' }] : [])],
    };
  });
  const bumped = w.liveRestarts ?? 0;
  if (bumped && pods[0]) {
    const p = pods[0];
    pods[0] = {
      ...p, restarts: p.restarts + bumped, lastTermination: 'OOMKilled', lastTerminatedAt: iso(now - 20_000),
      containers: p.containers.map((c, i) => (i === 0 ? { ...c, restartCount: c.restartCount + bumped, lastState: 'Terminated', lastReason: 'OOMKilled', lastExitCode: 137, lastFinishedAt: iso(now - 20_000) } : c)),
    };
  }
  if (w.liveSurge && w.kind !== 'StatefulSet') {
    const p = pods[0];
    pods.push({
      ...p, name: `${w.name}-5c8f7d6b9-n3w7p`, phase: 'Pending', ready: `0/${p.containers.length}`, restarts: 0, createdAt: iso(now - 3000),
      lastTermination: null, lastTerminatedAt: null,
      containers: p.containers.map((c) => ({ ...c, ready: false, state: 'Waiting', stateReason: 'ContainerCreating', restartCount: 0, lastState: null, lastReason: null, lastExitCode: null })),
    });
  }
  return pods;
}


/**
 * The first workload behind an Ingress (and an in-cluster gRPC Service), the
 * others behind HTTPRoutes on a shared Gateway; a workload with no ready pods
 * makes its Service a problem. StatefulSets also get a headless Service, and
 * a leftover Service selects nothing.
 */
function trafficFor(n: NsDef): TrafficGraph {
  const g: TrafficGraph = { nodes: [], edges: [] };
  const node = (kind: string, namespace: string, name: string, extra: Partial<TrafficNode> = {}) => {
    const id = `${kind}/${namespace}/${name}`;
    if (!g.nodes.some((x) => x.id === id)) g.nodes.push({ id, kind, namespace, name, details: [], ready: 0, total: 0, problem: '', ...extra });
    return id;
  };
  const edge = (from: string, to: string, ...labels: string[]) => g.edges.push({ from, to, labels });
  const host = `${n.namespace}.example.com`;
  n.workloads.filter((w) => w.kind !== 'CronJob').forEach((w, i) => {
    const pods = { ready: w.ready, total: w.desired };
    const svc = node('Service', n.namespace, w.name, { details: ['port 80→8080'], ...pods, problem: w.ready === 0 ? 'no ready pods' : '' });
    edge(svc, node(w.kind, n.namespace, w.name, pods));
    if (i === 0) {
      const ing = node('Ingress', n.namespace, n.namespace, { details: ['class nginx', 'TLS'] });
      edge(node('Address', '', '10.20.0.15'), ing);
      edge(ing, svc, ...['/', '/api', '/v1', '/v2', '/webhooks', '/health', '/metrics', '/admin'].map((p) => host + p));
      edge(node('Service', n.namespace, `${w.name}-grpc`, { details: ['port 9090'], ...pods }), node(w.kind, n.namespace, w.name));
    } else {
      const gw = node('Gateway', 'gateway-system', 'public', { details: ['class eg', '10.20.0.40'] });
      const route = node('HTTPRoute', n.namespace, w.name, { details: [`${w.name}.${host}`] });
      edge(gw, route, 'listener https');
      edge(route, svc, '/');
    }
    if (w.kind === 'StatefulSet') {
      edge(node('Service', n.namespace, `${w.name}-headless`, { details: ['headless', 'port 5432'], ...pods }), node(w.kind, n.namespace, w.name));
    }
  });
  if (g.nodes.length) node('Service', n.namespace, 'legacy-admin', { details: ['port 80→8080'], problem: 'no running pods match the selector' });
  if (n.cluster === 'prod-1' && n.namespace === 'payments-worker') {
    // Istio ambient: a waypoint applies a mesh route to one Service and carries another directly.
    const via = (id: string, ...more: string[]) => g.nodes.find((x) => x.id === id)?.details.push('via waypoint waypoint', ...more);
    const wp = node('Waypoint', n.namespace, 'waypoint', { details: ['10.96.4.12'], ready: 2, total: 2 });
    const grpc = `Service/${n.namespace}/payments-worker-grpc`;
    const route = node('HTTPRoute', n.namespace, 'payments-worker-grpc', { details: ['for Service payments-worker-grpc:9090'] });
    edge(wp, route, 'payments-worker-grpc:9090');
    edge(route, grpc, '/');
    via(grpc, 'routed by HTTPRoute payments-worker-grpc');
    const headless = `Service/${n.namespace}/payments-ledger-headless`;
    edge(wp, headless);
    via(headless);
  }
  return g;
}
function eventsFor(w: WlDef): K8sEvent[] {
  const now = Date.now();
  const e = (type: K8sEvent['type'], reason: string, message: string, objSuffix: string, minAgo: number, count = 1): K8sEvent => ({
    type, reason, message, object: `pod/${w.name}-${objSuffix}`, count, firstSeen: iso(now - minAgo * MIN), lastSeen: iso(now - minAgo * MIN),
  });
  const out: K8sEvent[] = [
    e('Normal', 'Pulled', 'Image already present on machine', '7d9c6b5f4-9mzt5', 120),
    e('Normal', 'Started', 'Started container app', '7d9c6b5f4-9mzt5', 119),
  ];
  if (w.name === 'payments-api') out.unshift(e('Warning', 'OOMKilling', 'Container app exceeded its memory limit and was killed.', '7d9c6b5f4-9mzt5', 121));
  if (w.ready < w.desired) out.unshift(e('Warning', 'BackOff', 'Back-off restarting failed container app', '7d9c6b5f4-4tn2p', 3, 42));
  return [...(w.liveEvents ?? []), ...out];
}

function jobsFor(cluster: string, ns: string, cron: string): JobSummary[] {
  return getDb().jobs[`${cluster}/${ns}/${cron}`] ?? [];
}
function cronSummary(u: MockUser, n: NsDef, w: WlDef): CronJobSummary {
  const jobs = jobsFor(n.cluster, n.namespace, w.name).sort((a, b) => Date.parse(b.startedAt ?? '') - Date.parse(a.startedAt ?? ''));
  const active = jobs.filter((j) => j.status === 'running').length;
  const lastResult = active ? 'running' : jobs[0]?.status ?? w.lastResult ?? null;
  return {
    ...summary(u, n, w), lastScheduleAt: w.lastScheduleMinutesAgo !== undefined ? iso(Date.now() - w.lastScheduleMinutesAgo * MIN) : null,
    lastResult, activeJobs: active, concurrencyPolicy: w.concurrencyPolicy ?? 'Allow', recentJobs: jobs,
  };
}

// ---- auth + me
on('GET', '/me', (c) => {
  const d = getDb();
  const bg = d.breakGlasses.find((b) => b.userId === c.user.id && b.active && Date.parse(b.expiresAt) > Date.now()) ?? null;
  const me: Me = {
    id: c.user.id, username: c.user.username, displayName: c.user.displayName, role: c.user.role,
    teams: c.user.teams, csrfToken: CSRF, breakGlass: bg,
    breakGlassRequest: d.settings.breakGlassMode === 'off' ? null : pendingBgRequest(c.user.id) ?? null,
    settings: { breakGlassDefaultMinutes: d.settings.breakGlassDefaultMinutes, breakGlassMaxMinutes: d.settings.breakGlassMaxMinutes, execIdleTimeoutMinutes: d.settings.execIdleTimeoutMinutes, execMaxSessionMinutes: d.settings.execMaxSessionMinutes, clusterRegistration: true, debugEnabled: true, argoUrl: 'https://argocd.example.com', breakGlassMode: d.settings.breakGlassMode, localUsers: true },
    argo: { fetchedAt: iso(d.argo.fetchedAt), ageSeconds: Math.round((Date.now() - d.argo.fetchedAt) / 1000) + (d.argo.stale ? 1200 : 0), stale: d.argo.stale },
  };
  return me;
});
on('POST', '/auth/logout', () => ({ redirect: '/login' }));

// ---- catalog / workloads
on('GET', '/apps', (c) => {
  const d = getDb();
  const out: AppSummary[] = [];
  for (const a of d.apps) {
    const access = nsActions(c.user, a.cluster, a.namespace);
    if (!access.length) continue;
    const ns = d.namespaces.find((n) => n.cluster === a.cluster && n.namespace === a.namespace);
    out.push({ ...a, environment: envOf(a.cluster, a.namespace), workloadCount: ns ? ns.workloads.length : null, access });
  }
  return out;
});
on('GET', '/namespaces', (c) => {
  const d = getDb();
  const out: NamespaceSummary[] = [];
  for (const n of d.namespaces) {
    const access = nsActions(c.user, n.cluster, n.namespace);
    if (!access.length) continue;
    out.push({
      cluster: n.cluster, namespace: n.namespace, environment: envOf(n.cluster, n.namespace),
      apps: d.apps.filter((a) => a.cluster === n.cluster && a.namespace === n.namespace).map((a) => a.name),
      argoProjects: [...new Set(d.apps.filter((a) => a.cluster === n.cluster && a.namespace === n.namespace).map((a) => a.project))], access,
    });
  }
  return out;
});
on('GET', '/clusters/:c/namespaces/:ns/workloads', (c) => {
  clusterGuard(c.params.c);
  const n = findNs(c.params.c, c.params.ns);
  return n.workloads.filter((w) => w.kind !== 'CronJob' || true).map((w) => summary(c.user, n, w)).filter(canUse);
});
on('GET', '/clusters/:c/namespaces/:ns/apps', (c) => {
  const { c: cluster, ns } = c.params;
  if (!nsActions(c.user, cluster, ns).length) throw denied(decide(c.user, cluster, ns, null, null, 'logs'));
  return getDb().apps.filter((a) => a.cluster === cluster && a.namespace === ns).map((a, i): NamespaceApp => ({
    name: a.name, project: a.project, appNamespace: 'argocd', health: a.health, sync: a.sync,
    lastSyncAt: a.sync === 'Synced' ? new Date(Date.now() - (i + 1) * 47 * 60_000).toISOString() : new Date(Date.now() - 26 * 3600_000).toISOString(),
    lastSyncPhase: a.health === 'Degraded' ? 'Failed' : 'Succeeded',
    access: { sync: decide(c.user, cluster, ns, null, null, 'sync') }, syncNamespace: ns,
  }));
});
function mockApp(c: { user: MockUser; params: Record<string, string> }) {
  clusterGuard(c.params.c);
  const { c: cluster, ns, app } = c.params;
  if (!nsActions(c.user, cluster, ns).length) throw denied(decide(c.user, cluster, ns, null, null, 'logs'));
  const a = getDb().apps.find((x) => x.cluster === cluster && x.namespace === ns && x.name === app);
  if (!a) throw notFound(`Application ${app} not found`);
  return a;
}
const REVS = ['9f3c2e1a7b4d5c6e8f9a0b1c2d3e4f5a6b7c8d9e', '4b7e9a1c3d5f7a9b1c3d5e7f9a1b3c5d7e9f1a3b', '1a2b3c4d5e6f7a8b9c0d1e2f3a4b5c6d7e8f9a0b'];
on('GET', '/clusters/:c/namespaces/:ns/apps/:appNs/:app/status', (c): ArgoAppStatus => {
  const a = mockApp(c);
  const failed = a.health === 'Degraded';
  return {
    health: a.health, healthMessage: failed ? 'Deployment "payments-worker" exceeded its progress deadline' : '',
    syncStatus: a.sync, syncRevision: REVS[0],
    conditions: failed ? [{ type: 'SyncError', message: 'one or more objects failed to apply, reason: Deployment.apps "payments-worker" is invalid: spec.selector: field is immutable', at: iso(Date.now() - 26 * 3600_000) }] : [],
    lastOperation: {
      phase: failed ? 'Failed' : 'Succeeded', message: failed ? 'one or more objects failed to apply' : 'successfully synced (all tasks run)',
      startedAt: iso(Date.now() - 47 * 60_000), finishedAt: iso(Date.now() - 46 * 60_000), initiatedBy: '', automated: true, revision: REVS[0],
      resources: [
        { group: 'batch', kind: 'Job', namespace: a.namespace, name: `${a.name}-migrate`, status: 'Synced', message: 'job.batch/' + a.name + '-migrate created', hookType: 'PreSync', hookPhase: 'Succeeded', syncPhase: 'PreSync' },
        { group: 'apps', kind: 'Deployment', namespace: a.namespace, name: a.name, status: failed ? 'SyncFailed' : 'Synced', message: failed ? 'spec.selector: field is immutable' : 'deployment.apps/' + a.name + ' configured', hookType: '', hookPhase: '', syncPhase: 'Sync' },
        { group: '', kind: 'Service', namespace: a.namespace, name: a.name, status: 'Synced', message: 'service/' + a.name + ' unchanged', hookType: '', hookPhase: '', syncPhase: 'Sync' },
      ],
    },
  };
});
on('GET', '/clusters/:c/namespaces/:ns/apps/:appNs/:app/history', (c): ArgoDeploy[] => {
  mockApp(c);
  const msgs = ['Bump api image to 2026.10.17-4', 'Raise memory limit to 512Mi', 'Add readiness probe'];
  return REVS.map((rev, i) => ({
    id: 3 - i, revision: rev, revisions: [], deployedAt: iso(Date.now() - (i * 26 + 1) * 3600_000), initiatedBy: i === 1 ? 'sara.moradi' : '',
    automated: i !== 1, source: 'https://git.example.com/platform/deploy.git', current: i === 0,
    commit: { author: ['Nima Karimi', 'Sara Moradi', 'Reza Ahmadi'][i], date: iso(Date.now() - (i * 26 + 2) * 3600_000), message: msgs[i], tags: [] },
  }));
});
on('GET', '/clusters/:c/namespaces/:ns/apps/:appNs/:app/diff', (c): ArgoDiff => {
  const a = mockApp(c);
  const info = decide(c.user, c.params.c, c.params.ns, null, null, 'sync');
  if (!info.allowed) throw denied(info);
  if (a.sync === 'Synced') return { resources: [], truncated: false };
  return {
    truncated: false,
    resources: [
      { group: 'apps', kind: 'Deployment', namespace: a.namespace, name: a.name, state: 'changed', hidden: false, truncated: false,
        diff: `--- live\n+++ desired\n@@ -12,7 +12,7 @@\n     spec:\n       containers:\n       - name: app\n-        image: ${a.name}:2026.10.17-4\n+        image: ${a.name}:2026.10.30-1\n         resources:\n           limits:\n             memory: 512Mi\n` },
      { group: '', kind: 'ConfigMap', namespace: a.namespace, name: `${a.name}-config`, state: 'missing', hidden: false, truncated: false,
        diff: `--- live\n+++ desired\n@@ -0,0 +1,6 @@\n+apiVersion: v1\n+data:\n+  FEATURE_X: "true"\n+kind: ConfigMap\n+metadata:\n+  name: ${a.name}-config\n` },
      { group: '', kind: 'Secret', namespace: a.namespace, name: `${a.name}-db`, state: 'changed', hidden: true, truncated: false, diff: '' },
    ],
  };
});
on('POST', '/clusters/:c/namespaces/:ns/apps/:appNs/:app/sync', (c) => {
  clusterGuard(c.params.c);
  const { c: cluster, ns, app } = c.params;
  const a = getDb().apps.find((x) => x.cluster === cluster && x.namespace === ns && x.name === app);
  if (!a) throw notFound(`Application ${app} not found`);
  const info = decide(c.user, cluster, ns, null, null, 'sync');
  if (!info.allowed) throw denied(info);
  const id = opId();
  getDb().ops[id] = { id, kind: 'sync', cluster, namespace: ns, name: app, owner: c.user.id, willFail: a.health === 'Degraded' };
  return { __status: 202, data: { operationId: id } };
});
on('GET', '/clusters/:c/namespaces/:ns/access', (c) => {
  const { c: cluster, ns } = c.params;
  const kind = (c.query.get('kind') as WorkloadKind) || null;
  const name = c.query.get('name') || null;
  const n = findNs(cluster, ns);
  if (kind && name) {
    const access = accessMap(c.user, cluster, ns, kind, name);
    if (!canUse({ access })) throw denied(access.logs);
  } else if (!nsActions(c.user, n.cluster, n.namespace).length) {
    throw denied(decide(c.user, cluster, ns, null, null, 'logs'));
  }
  const d = getDb();
  return d.grants
    .filter((g) => !(g.expiresAt && Date.parse(g.expiresAt) < Date.now()) && covers(g, cluster, ns, kind, name).match)
    .map((g) => ({
      id: g.id, scope: g.scope, actions: g.actions, expiresAt: g.expiresAt,
      holder: {
        ...g.holder,
        members: g.holder.kind === 'team' ? d.users.filter((u) => u.teamIds.includes(g.holder.id) && !u.disabled).map((u) => u.displayName || u.username) : [],
      },
    }));
});
on('GET', '/clusters/:c/namespaces/:ns/workloads/:kind/:name', (c) => {
  clusterGuard(c.params.c);
  const { ns, wl } = findWl(c.params.c, c.params.ns, c.params.kind, c.params.name);
  const s = summary(c.user, ns, wl);
  if (!canUse(s)) throw denied(s.access.logs);
  const d = getDb();
  const app = d.apps.find((a) => a.cluster === ns.cluster && a.namespace === ns.namespace);
  const detail: WorkloadDetail = {
    ...s, argoApp: app?.name ?? null, strategy: wl.strategy ?? 'RollingUpdate', updated: wl.ready, available: wl.ready,
    images: wl.images ?? [], createdAt: iso(Date.now() - 40 * DAY), lastRolloutAt: iso(Date.now() - 3 * DAY),
    cronJobCount: ns.workloads.filter((w) => w.kind === 'CronJob').length,
  };
  return detail;
});
on('GET', '/clusters/:c/namespaces/:ns/workloads/:kind/:name/pods', (c) => {
  clusterGuard(c.params.c);
  const { ns, wl } = findWl(c.params.c, c.params.ns, c.params.kind, c.params.name);
  requireAction(c.user, ns, wl, 'logs');
  if (wl.kind === 'CronJob') {
    return jobsFor(ns.cluster, ns.namespace, wl.name).flatMap((j) => j.podNames.map((pn) => ({
      name: pn, phase: j.status === 'running' ? 'Running' : j.status === 'failed' ? 'Failed' : 'Succeeded', ready: '0/1', restarts: 0, node: 'node-a1', createdAt: j.startedAt ?? iso(Date.now()),
      lastTermination: null, lastTerminatedAt: null,
      containers: [{ name: 'job', image: `${wl.name}:latest`, ready: false, state: j.status === 'running' ? 'Running' : 'Terminated', stateReason: '', restartCount: 0, lastState: null, lastReason: null, lastExitCode: null, lastStartedAt: null, lastFinishedAt: null, memoryLimit: null, init: false }],
    } as Pod)));
  }
  return podsFor(ns, wl);
});
on('GET', '/clusters/:c/namespaces/:ns/workloads/:kind/:name/pods/:pod/logs/download', (c) => {
  const { ns, wl } = findWl(c.params.c, c.params.ns, c.params.kind, c.params.name);
  requireAction(c.user, ns, wl, 'logs');
  const lines = Array.from({ length: 200 }, (_, i) => `${new Date(Date.now() - (200 - i) * 1000).toISOString()} INFO mock log line ${i} from ${c.params.pod}`);
  return { __raw: lines.join('\n') + '\n', __type: 'text/plain' };
});
on('GET', '/clusters/:c/namespaces/:ns/workloads/:kind/:name/events', (c) => {
  clusterGuard(c.params.c);
  const { ns, wl } = findWl(c.params.c, c.params.ns, c.params.kind, c.params.name);
  requireAction(c.user, ns, wl, 'logs');
  return eventsFor(wl);
});
on('GET', '/clusters/:c/namespaces/:ns/events', (c) => {
  clusterGuard(c.params.c);
  const n = findNs(c.params.c, c.params.ns);
  const info = decide(c.user, n.cluster, n.namespace, null, null, 'logs');
  if (!info.allowed) throw denied(info);
  return n.workloads.flatMap(eventsFor).sort((a, b) => Date.parse(b.lastSeen) - Date.parse(a.lastSeen));
});
on('GET', '/clusters/:c/namespaces/:ns/traffic', (c) => {
  clusterGuard(c.params.c);
  const n = findNs(c.params.c, c.params.ns);
  const info = decide(c.user, n.cluster, n.namespace, null, null, 'logs');
  if (!info.allowed) throw denied(info);
  return trafficFor(n);
});
on('GET', '/clusters/:c/namespaces/:ns/cronjobs', (c) => {
  clusterGuard(c.params.c);
  const n = findNs(c.params.c, c.params.ns);
  const list = n.workloads.filter((w) => w.kind === 'CronJob').map((w) => cronSummary(c.user, n, w));
  return list.filter((s) => s.access['run-job'].allowed || s.access.logs.allowed || s.access['run-job'].reason === 'outside_window');
});

const opId = () => 'op_' + Math.random().toString(36).slice(2, 10);
on('POST', '/clusters/:c/namespaces/:ns/workloads/:kind/:name/restart', (c) => {
  clusterGuard(c.params.c);
  const { ns, wl } = findWl(c.params.c, c.params.ns, c.params.kind, c.params.name);
  if (wl.kind === 'CronJob') throw new MockError(422, 'invalid', 'CronJobs cannot be restarted');
  requireAction(c.user, ns, wl, 'restart');
  const id = opId();
  getDb().ops[id] = { id, kind: 'restart', cluster: ns.cluster, namespace: ns.namespace, name: wl.name, owner: c.user.id, willFail: wl.name === 'notifications-api' };
  return { __status: 202, data: { operationId: id } };
});
on('POST', '/clusters/:c/namespaces/:ns/cronjobs/:name/run', (c) => {
  clusterGuard(c.params.c);
  const { ns, wl } = findWl(c.params.c, c.params.ns, 'cronjobs', c.params.name);
  requireAction(c.user, ns, wl, 'run-job');
  const d = getDb();
  const key = `${ns.cluster}/${ns.namespace}/${wl.name}`;
  const jobs = (d.jobs[key] ??= []);
  if ((wl.concurrencyPolicy ?? 'Allow') === 'Forbid' && jobs.some((j) => j.status === 'running')) {
    throw new MockError(409, 'already_running', 'A run is already in progress for this CronJob.');
  }
  const jobName = `${wl.name}-manual-${Math.random().toString(36).slice(2, 7)}`;
  jobs.unshift({ name: jobName, cronJob: wl.name, triggeredBy: c.user.displayName || c.user.username, manual: true, status: 'running', active: 1, succeeded: 0, completions: 1, startedAt: iso(Date.now()), finishedAt: null, podNames: [`${jobName}-p4x2k`] });
  const id = opId();
  d.ops[id] = { id, kind: 'run-job', cluster: ns.cluster, namespace: ns.namespace, name: wl.name, jobName, owner: c.user.id };
  return { __status: 202, data: { operationId: id, jobName } };
});

on('GET', '/catalog/argo-projects', () => { return [...new Set([...getDb().apps.map((a) => a.project), 'search-lab'])].sort(); });
const hiddenCluster = (u: MockUser, name?: string) => !isAdmin(u) && !!getDb().clusters.find((x) => x.name === name)?.protected;
on('GET', '/catalog/clusters', (c) => { return getDb().clusters.filter((x) => x.usable && !hiddenCluster(c.user, x.name)).map((x) => ({ name: x.name, environment: x.environment ?? 'unlabeled' })); });
on('GET', '/catalog/clusters/:c/namespaces', (c) => {
  const d = getDb();
  if (hiddenCluster(c.user, c.params.c)) return [];
  return [...new Set([...d.namespaces.filter((n) => n.cluster === c.params.c).map((n) => n.namespace), ...(c.params.c === 'prod-1' ? ['payments-sandbox'] : [])])].filter((n) => !d.protectedNamespaces.includes(n)).sort();
});
on('GET', '/catalog/clusters/:c/namespaces/:ns/workloads', (c) => {
  if (getDb().protectedNamespaces.includes(c.params.ns)) throw new MockError(403, 'forbidden_role', 'Protected namespace');
  if (hiddenCluster(c.user, c.params.c)) throw new MockError(404, 'not_found', 'Not found');
  const n = getDb().namespaces.find((x) => x.cluster === c.params.c && x.namespace === c.params.ns);
  return (n?.workloads ?? []).map((w) => ({ kind: w.kind, name: w.name }));
});

// ---- access, requests, break-glass
function scopeText(s: ScopeView): string {
  return s.kind === 'argo_project' ? s.argoProject! : s.kind === 'environment' ? s.environment! : s.kind === 'cluster' ? s.cluster! : s.namespace!;
}
on('GET', '/my/access', (c) => {
  const d = getDb();
  const grants = userGrants(c.user).map((g) => ({
    id: g.id, holder: { kind: g.holder.kind, name: g.holder.name }, scope: g.scope, actions: g.actions, expiresAt: g.expiresAt, note: g.note,
    expired: !!g.expiresAt && Date.parse(g.expiresAt) < Date.now(),
  }));
  const bg = d.breakGlasses.find((b) => b.userId === c.user.id && b.active && Date.parse(b.expiresAt) > Date.now()) ?? null;
  return { grants, teams: c.user.teams, breakGlass: bg, requests: d.requests.filter((r) => r.userId === c.user.id).sort((a, b) => b.createdAt.localeCompare(a.createdAt)) };
});
on('GET', '/access-requests', (c) => {
  const d = getDb();
  const status = c.query.get('status');
  let list = c.user.role === 'user' ? d.requests.filter((r) => r.userId === c.user.id) : d.requests;
  if (status) list = list.filter((r) => r.status === status);
  return [...list].sort((a, b) => b.createdAt.localeCompare(a.createdAt));
});
on('POST', '/access-requests', (c) => {
  const b = c.body as unknown as { scope: ScopeView; actions: Action[]; durationHours: number | null; reason: string };
  const fields: Record<string, string> = {};
  if (!b.reason || b.reason.trim().length < 10) fields.reason = 'Please explain why you need this (at least 10 characters).';
  if (!b.actions?.length) fields.actions = 'Pick at least one action.';
  if (b.scope?.kind === 'workloads' && !b.scope.workloads?.length) fields.workloads = 'Pick at least one workload.';
  if (b.scope?.kind === 'cluster' || b.scope?.kind === 'environment') fields.scope = 'A whole cluster or environment is granted by an admin only, not requested.';
  if (b.scope?.cluster && hiddenCluster(c.user, b.scope.cluster)) fields.scope = 'This cluster is protected and cannot be requested.';
  if (Object.keys(fields).length) throw invalid(fields);
  const r: AccessRequest = {
    id: nextId('request'), userId: c.user.id, username: c.user.username, scope: b.scope, actions: b.actions, durationHours: b.durationHours, reason: b.reason,
    status: 'pending', decidedBy: null, decidedAt: null, decisionNote: null, resultingGrantId: null, createdAt: iso(Date.now()),
  };
  getDb().requests.push(r);
  logAudit(c.user, 'access_request.created', null, 'succeeded', { details: { requestId: r.id, scope: scopeText(b.scope) } });
  return { __status: 201, data: r };
});
function holderOfUser(u: MockUser): GrantView['holder'] { return { kind: 'user', id: u.id, name: u.displayName || u.username }; }
on('POST', '/access-requests/:id/approve', (c) => {
  needAdmin(c);
  const d = getDb();
  const r = d.requests.find((x) => x.id === Number(c.params.id));
  if (!r) throw notFound();
  if (r.status !== 'pending') throw new MockError(409, 'conflict', 'This request was already decided.');
  const b = c.body as { note?: string; durationHours?: number | null; actions?: Action[]; separate?: boolean };
  if (b.actions?.length) {
    if (r.scope.kind === 'workloads' && b.actions.includes('sync')) throw invalid({ actions: 'Sync needs a namespace, cluster, environment or Argo project scope.' });
    r.actions = b.actions;
  }
  const hours = b.durationHours === undefined ? r.durationHours : b.durationHours;
  const u = d.users.find((x) => x.id === r.userId)!;
  const expiresAt = hours === null ? null : Date.now() + hours * HOUR;
  // Like the server: add to the requester's own same-scope grant that ends no later.
  const own = d.grants.filter((g) => g.holder.kind === 'user' && g.holder.id === u.id);
  let g = b.separate ? null : mergeTarget(own, r.scope, expiresAt, Date.now());
  if (g) {
    g.actions = ACTIONS.filter((a) => g!.actions.includes(a) || r.actions.includes(a));
    g.expiresAt = expiresAt === null ? null : iso(expiresAt);
  } else {
    g = {
      id: nextId('grant'), holder: holderOfUser(u), scope: r.scope, actions: r.actions, expiresAt: expiresAt === null ? null : iso(expiresAt), note: `Request #${r.id}`,
      expired: false, createdBy: c.user.username, createdAt: iso(Date.now()), environment: null,
    };
    d.grants.push(g);
  }
  Object.assign(r, { status: 'approved', decidedBy: c.user.username, decidedAt: iso(Date.now()), decisionNote: b.note || null, resultingGrantId: g.id, durationHours: hours });
  logAudit(c.user, 'access_request.approved', null, 'succeeded', { details: { requestId: r.id, grantId: g.id } });
  return r;
});
on('POST', '/access-requests/:id/reject', (c) => {
  needAdmin(c);
  const r = getDb().requests.find((x) => x.id === Number(c.params.id));
  if (!r) throw notFound();
  if (r.status !== 'pending') throw new MockError(409, 'conflict', 'This request was already decided.');
  const note = String((c.body as { note?: string }).note ?? '').trim();
  if (!note) throw invalid({ note: 'A note is required when rejecting.' });
  Object.assign(r, { status: 'rejected', decidedBy: c.user.username, decidedAt: iso(Date.now()), decisionNote: note });
  logAudit(c.user, 'access_request.rejected', null, 'succeeded', { details: { requestId: r.id } });
  return r;
});
/** Checks a break-glass reason and duration; returns the minutes. */
function bgInput(b: { reason?: string; durationMinutes?: number }): number {
  const d = getDb();
  const fields: Record<string, string> = {};
  if (!b.reason || b.reason.trim().length < 20) fields.reason = 'Reason must be at least 20 characters.';
  const mins = b.durationMinutes ?? d.settings.breakGlassDefaultMinutes;
  if (mins < 1 || mins > d.settings.breakGlassMaxMinutes) fields.durationMinutes = `At most ${d.settings.breakGlassMaxMinutes} minutes.`;
  if (Object.keys(fields).length) throw invalid(fields);
  return mins;
}
const hasActiveBg = (userId: number) => getDb().breakGlasses.some((x) => x.userId === userId && x.active && Date.parse(x.expiresAt) > Date.now());
/** Marks pending requests past their expiry as expired, as the server does every 30 s. */
function expireBgRequests() {
  for (const r of getDb().bgRequests) if (r.status === 'pending' && Date.parse(r.expiresAt) <= Date.now()) Object.assign(r, { status: 'expired', decidedBy: 'system', decidedAt: r.expiresAt });
}
function pendingBgRequest(userId: number): BreakGlassRequest | undefined {
  expireBgRequests();
  return getDb().bgRequests.find((r) => r.userId === userId && r.status === 'pending');
}
function startBg(userId: number, username: string, reason: string, mins: number): BreakGlass {
  const bg: BreakGlass = { id: nextId('bg'), userId, username, reason, startedAt: iso(Date.now()), expiresAt: iso(Date.now() + mins * MIN), endedAt: null, endedBy: null, active: true };
  getDb().breakGlasses.unshift(bg);
  return bg;
}
on('POST', '/break-glass', (c) => {
  if (c.user.role === 'admin') throw forbiddenRole();
  const d = getDb();
  if (d.settings.breakGlassMode === 'off') throw new MockError(403, 'break_glass_disabled', 'Break-glass is turned off.');
  if (d.settings.breakGlassMode === 'approval') throw new MockError(409, 'break_glass_approval_required', "Break-glass needs an admin's approval: request it instead.");
  const b = c.body as { reason?: string; durationMinutes?: number };
  const mins = bgInput(b);
  if (hasActiveBg(c.user.id)) throw new MockError(409, 'conflict', 'Break-glass is already active.');
  const bg = startBg(c.user.id, c.user.username, b.reason!.trim(), mins);
  logAudit(c.user, 'break_glass.start', null, 'succeeded', { breakGlassId: bg.id, details: { reason: bg.reason, minutes: mins } });
  return { __status: 201, data: bg };
});
on('POST', '/break-glass/requests', (c) => {
  if (c.user.role === 'admin') throw forbiddenRole();
  const d = getDb();
  if (d.settings.breakGlassMode === 'off') throw new MockError(403, 'break_glass_disabled', 'Break-glass is turned off.');
  if (d.settings.breakGlassMode === 'self') throw new MockError(409, 'conflict', 'conflict: break-glass starts without approval');
  const b = c.body as { reason?: string; durationMinutes?: number };
  const mins = bgInput(b);
  if (hasActiveBg(c.user.id)) throw new MockError(409, 'conflict', 'Break-glass is already active.');
  if (pendingBgRequest(c.user.id)) throw new MockError(409, 'conflict', 'conflict: you already have a pending break-glass request');
  const now = Date.now();
  const r: BreakGlassRequest = {
    id: nextId('bgRequest'), userId: c.user.id, username: c.user.username, reason: b.reason!.trim(), durationMinutes: mins, status: 'pending',
    decidedBy: null, decidedAt: null, decisionNote: null, approvedMinutes: null, breakGlassId: null, createdAt: iso(now),
    expiresAt: iso(now + d.settings.breakGlassRequestMaxAgeMinutes * MIN),
  };
  d.bgRequests.unshift(r);
  logAudit(c.user, 'break_glass.request', null, 'succeeded', { reason: r.reason, details: { requestId: r.id, minutes: mins } });
  return { __status: 201, data: r };
});
on('GET', '/break-glass/requests', (c) => {
  needStaff(c.user);
  expireBgRequests();
  const status = c.query.get('status');
  return getDb().bgRequests.filter((r) => !status || r.status === status);
});
function pendingBgById(id: string): BreakGlassRequest {
  expireBgRequests();
  const r = getDb().bgRequests.find((x) => x.id === Number(id));
  if (!r) throw notFound();
  if (r.status !== 'pending') throw new MockError(409, 'conflict', `conflict: request is already ${r.status}`);
  return r;
}
on('POST', '/break-glass/requests/:id/approve', (c) => {
  needAdmin(c);
  const d = getDb();
  if (d.settings.breakGlassMode === 'off') throw new MockError(403, 'break_glass_disabled', 'Break-glass is turned off.');
  const r = pendingBgById(c.params.id);
  const b = (c.body ?? {}) as { note?: string; durationMinutes?: number };
  const mins = b.durationMinutes || r.durationMinutes;
  if (mins < 1 || mins > d.settings.breakGlassMaxMinutes) throw invalid({ durationMinutes: `duration must be between 1 and ${d.settings.breakGlassMaxMinutes} minutes` });
  if (hasActiveBg(r.userId)) throw new MockError(409, 'conflict', `conflict: ${r.username} already has an active break-glass`);
  const bg = startBg(r.userId, r.username, r.reason, mins);
  Object.assign(r, { status: 'approved', decidedBy: c.user.username, decidedAt: iso(Date.now()), decisionNote: b.note ?? null, approvedMinutes: mins, breakGlassId: bg.id });
  logAudit(c.user, 'break_glass.start', null, 'succeeded', { breakGlassId: bg.id, reason: r.reason, details: { owner: r.username, requestId: r.id, minutes: mins, requestedMinutes: r.durationMinutes } });
  return r;
});
on('POST', '/break-glass/requests/:id/reject', (c) => {
  needAdmin(c);
  const r = pendingBgById(c.params.id);
  const note = ((c.body ?? {}) as { note?: string }).note ?? null;
  Object.assign(r, { status: 'rejected', decidedBy: c.user.username, decidedAt: iso(Date.now()), decisionNote: note });
  logAudit(c.user, 'break_glass.reject', null, 'succeeded', { details: { requestId: r.id, owner: r.username, note } });
  return r;
});
on('POST', '/break-glass/requests/:id/cancel', (c) => {
  const r = getDb().bgRequests.find((x) => x.id === Number(c.params.id));
  if (!r) throw notFound();
  if (r.userId !== c.user.id) throw forbiddenRole();
  pendingBgById(c.params.id);
  Object.assign(r, { status: 'cancelled', decidedBy: c.user.username, decidedAt: iso(Date.now()) });
  logAudit(c.user, 'break_glass.cancel', null, 'succeeded', { details: { requestId: r.id, owner: r.username } });
  return r;
});
on('POST', '/break-glass/:id/end', (c) => {
  const bg = getDb().breakGlasses.find((x) => x.id === Number(c.params.id));
  if (!bg) throw notFound();
  if (bg.userId !== c.user.id && !isAdmin(c.user)) throw forbiddenRole();
  Object.assign(bg, { active: false, endedAt: iso(Date.now()), endedBy: c.user.username });
  logAudit(c.user, 'break_glass.ended', null, 'succeeded', { breakGlassId: bg.id });
  return bg;
});
on('GET', '/break-glass', (c) => { needStaff(c.user); return getDb().breakGlasses; });

function logAudit(u: MockUser, eventType: string, action: Action | null, result: AuditEvent['result'], extra: Partial<AuditEvent> = {}) {
  const d = getDb();
  d.audit.unshift({
    id: nextId('audit'), at: iso(Date.now()), userId: u.id, username: u.username, eventType, action, cluster: null, namespace: null, workloadKind: null, workloadName: null,
    pod: null, container: null, environment: null, result, reason: null, breakGlassId: null, sessionId: null, details: null, sourceIp: '10.20.4.7', userAgent: navigator.userAgent, ...extra,
  });
}

// ---- sessions, audit
on('GET', '/sessions', (c) => {
  needStaff(c.user);
  const q = c.query;
  let list = [...getDb().sessions].sort((a, b) => b.startedAt.localeCompare(a.startedAt));
  if (q.get('user')) list = list.filter((s) => s.username === q.get('user'));
  if (q.get('cluster')) list = list.filter((s) => s.cluster === q.get('cluster'));
  if (q.get('namespace')) list = list.filter((s) => s.namespace === q.get('namespace'));
  if (q.get('environment')) list = list.filter((s) => s.environment === q.get('environment'));
  if (q.get('status') === 'active') list = list.filter((s) => s.active);
  if (q.get('status') === 'ended') list = list.filter((s) => !s.active);
  if (q.get('breakGlass') === 'true') list = list.filter((s) => s.breakGlassId !== null);
  if (q.get('from')) list = list.filter((s) => s.startedAt >= q.get('from')!);
  if (q.get('to')) list = list.filter((s) => s.startedAt <= q.get('to')!);
  if (q.get('before')) list = list.filter((s) => s.startedAt < q.get('before')!);
  return list.slice(0, Number(q.get('limit') ?? 50));
});
function sessionOr404(id: string): ExecSessionView {
  const s = getDb().sessions.find((x) => x.id === id);
  if (!s) throw notFound('Session not found');
  return s;
}
on('GET', '/sessions/:id', (c) => { needStaff(c.user); const s = sessionOr404(c.params.id); return { ...s, commands: s.active ? [] : commandList().map((x) => ({ at: iso(Date.parse(s.startedAt) + x.offsetSeconds * 1000), offsetSeconds: x.offsetSeconds, text: x.text })) }; });
on('GET', '/sessions/:id/recording', (c) => {
  needStaff(c.user);
  const s = sessionOr404(c.params.id);
  logAudit(c.user, 'session.recording.read', null, 'succeeded', { sessionId: s.id });
  return { __raw: buildCast(s.pod, Date.parse(s.startedAt) / 1000), __type: 'application/x-asciicast' };
});
on('POST', '/sessions/:id/terminate', (c) => {
  needAdmin(c);
  const s = sessionOr404(c.params.id);
  if (!s.active) throw new MockError(409, 'conflict', 'Session already ended');
  terminateSocket(s.id, 'terminated_by_admin');
  Object.assign(s, { active: false, endedAt: iso(Date.now()), endReason: 'terminated_by_admin', durationSeconds: Math.round((Date.now() - Date.parse(s.startedAt)) / 1000) });
  logAudit(c.user, 'session.terminated', 'exec', 'succeeded', { sessionId: s.id, cluster: s.cluster, namespace: s.namespace, pod: s.pod, environment: s.environment });
  return undefined;
});
on('GET', '/audit', (c) => {
  needStaff(c.user);
  const q = c.query;
  let list = getDb().audit;
  const eq = (k: keyof AuditEvent, p: string) => { const v = q.get(p); if (v) list = list.filter((e) => String(e[k] ?? '') === v); };
  eq('username', 'user'); eq('action', 'action'); eq('result', 'result'); eq('cluster', 'cluster'); eq('namespace', 'namespace'); eq('environment', 'environment'); eq('breakGlassId', 'breakGlassId'); eq('sessionId', 'sessionId');
  if (q.get('eventType')) list = list.filter((e) => e.eventType.startsWith(q.get('eventType')!));
  if (q.get('from')) list = list.filter((e) => e.at >= q.get('from')!);
  if (q.get('to')) list = list.filter((e) => e.at <= q.get('to')!);
  if (q.get('q')) { const s = q.get('q')!.toLowerCase(); list = list.filter((e) => JSON.stringify(e).toLowerCase().includes(s)); }
  const limit = Math.min(200, Number(q.get('limit') ?? 50));
  const start = Number(q.get('cursor') ?? 0) || 0;
  const items = list.slice(start, start + limit);
  const next = start + limit < list.length ? String(start + limit) : null;
  return { items, nextCursor: next };
});

// ---- admin
const R = (path: string, h: Handler, method = 'GET') => on(method, path, (c) => { if (method === 'GET') adminRead(c); else needAdmin(c); return h(c); });
const userView = (u: MockUser) => ({ id: u.id, username: u.username, displayName: u.displayName, role: u.role, disabled: u.disabled, keycloakBound: u.keycloakBound, local: u.local, lastLoginAt: u.lastLoginAt, teams: u.teams, activeSessions: getDb().sessions.filter((s) => s.active && s.userId === u.id).length });
const teamView = (t: ReturnType<typeof getDb>['teams'][number]) => ({ id: t.id, name: t.name, description: t.description, members: t.memberIds.map((id) => getDb().users.find((u) => u.id === id)!).filter(Boolean).map((u) => ({ id: u.id, username: u.username, displayName: u.displayName })) });
function syncTeams() {
  const d = getDb();
  for (const u of d.users) {
    u.teamIds = d.teams.filter((t) => t.memberIds.includes(u.id)).map((t) => t.id);
    u.teams = u.teamIds.map((id) => ({ id, name: d.teams.find((t) => t.id === id)!.name }));
  }
}
R('/admin/users', () => getDb().users.map(userView));
R('/admin/users', (c) => {
  const b = c.body as { username?: string; displayName?: string; role?: Role; password?: string };
  if (!b.username?.trim()) throw invalid({ username: 'Username is required.' });
  if (b.password !== undefined && b.password.length < 12) throw invalid({ password: 'at least 12 characters' });
  const d = getDb();
  if (d.users.some((u) => u.username === b.username)) throw new MockError(409, 'conflict', 'That username already exists.');
  const u: MockUser = { id: nextId('user'), username: b.username.trim(), displayName: b.displayName ?? '', role: b.role ?? 'user', disabled: false, keycloakBound: false, local: !!b.password, lastLoginAt: null, teams: [], teamIds: [], activeSessions: 0 };
  d.users.push(u);
  logAudit(c.user, 'admin.user.created', null, 'succeeded', { details: { username: u.username, role: u.role } });
  return { __status: 201, data: userView(u) };
}, 'POST');
R('/admin/users/:id', (c) => {
  const d = getDb();
  const u = d.users.find((x) => x.id === Number(c.params.id));
  if (!u) throw notFound();
  const b = c.body as { role?: Role; disabled?: boolean; displayName?: string };
  const admins = d.users.filter((x) => x.role === 'admin' && !x.disabled);
  if (u.role === 'admin' && !u.disabled && admins.length === 1 && ((b.role && b.role !== 'admin') || b.disabled)) throw new MockError(409, 'conflict', 'At least one active admin must remain.');
  const before = { role: u.role, disabled: u.disabled };
  if (b.role) u.role = b.role;
  if (b.disabled !== undefined) u.disabled = b.disabled;
  if (b.displayName !== undefined) u.displayName = b.displayName;
  logAudit(c.user, 'admin.user.updated', null, 'succeeded', { details: { username: u.username, before, after: { role: u.role, disabled: u.disabled } } });
  return userView(u);
}, 'PATCH');
R('/admin/users/:id/password', (c) => {
  const u = getDb().users.find((x) => x.id === Number(c.params.id));
  if (!u) throw notFound();
  const b = c.body as { password?: string };
  if (!u.local) throw invalid({ password: 'only local users have a password; this user logs in through Keycloak' });
  if (!b.password || b.password.length < 12) throw invalid({ password: 'at least 12 characters' });
  logAudit(c.user, 'admin.change', null, 'succeeded', { details: { entity: 'user', operation: 'set_password', after: { username: u.username } } });
  return userView(u);
}, 'PUT');
R('/admin/teams', () => getDb().teams.map(teamView));
R('/admin/teams', (c) => {
  const b = c.body as { name?: string; description?: string };
  if (!b.name?.trim()) throw invalid({ name: 'Name is required.' });
  const t = { id: nextId('team'), name: b.name.trim(), description: b.description ?? '', memberIds: [] as number[] };
  getDb().teams.push(t);
  return { __status: 201, data: teamView(t) };
}, 'POST');
R('/admin/teams/:id', (c) => {
  const t = getDb().teams.find((x) => x.id === Number(c.params.id));
  if (!t) throw notFound();
  const b = c.body as { name?: string; description?: string };
  if (b.name !== undefined) t.name = b.name;
  if (b.description !== undefined) t.description = b.description;
  syncTeams();
  return teamView(t);
}, 'PATCH');
R('/admin/teams/:id', (c) => {
  const d = getDb();
  if (d.grants.some((g) => g.holder.kind === 'team' && g.holder.id === Number(c.params.id))) throw new MockError(409, 'conflict', 'This team still holds grants. Remove them first.');
  d.teams = d.teams.filter((t) => t.id !== Number(c.params.id));
  syncTeams();
  return undefined;
}, 'DELETE');
R('/admin/teams/:id/members', (c) => {
  const t = getDb().teams.find((x) => x.id === Number(c.params.id));
  if (!t) throw notFound();
  t.memberIds = ((c.body as { userIds?: number[] }).userIds ?? []).slice();
  syncTeams();
  logAudit(c.user, 'admin.team.members_changed', null, 'succeeded', { details: { team: t.name, members: t.memberIds } });
  return teamView(t);
}, 'PUT');

function grantFromInput(b: GrantInput, id: number, createdBy: string): GrantView {
  const d = getDb();
  const fields: Record<string, string> = {};
  if (!b.actions?.length) fields.actions = 'Pick at least one action.';
  if (b.scope.kind === 'workloads' && !b.scope.workloads?.length) fields.workloads = 'Pick at least one workload.';
  if (b.scope.kind === 'argo_project' && !b.scope.argoProject) fields.argoProject = 'Pick an Argo project.';
  if (b.scope.kind === 'environment' && !b.scope.environment) fields.environment = 'Pick an environment.';
  if (b.scope.kind === 'cluster' && !b.scope.cluster) fields.cluster = 'Pick a cluster.';
  if ((b.scope.kind === 'namespace' || b.scope.kind === 'workloads') && (!b.scope.cluster || !b.scope.namespace)) fields.namespace = 'Pick a cluster and namespace.';
  if (b.scope.namespace && d.protectedNamespaces.includes(b.scope.namespace)) fields.namespace = 'This namespace is protected.';
  if (Object.keys(fields).length) throw invalid(fields);
  const holderName = b.holder.kind === 'team' ? d.teams.find((t) => t.id === b.holder.id)?.name : d.users.find((u) => u.id === b.holder.id)?.displayName;
  if (!holderName) throw invalid({ holder: 'Unknown holder.' });
  return {
    id, holder: { kind: b.holder.kind, id: b.holder.id, name: holderName }, scope: b.scope, actions: b.actions, expiresAt: b.expiresAt, note: b.note ?? '',
    expired: !!b.expiresAt && Date.parse(b.expiresAt) < Date.now(), createdBy, createdAt: iso(Date.now()),
    environment: b.scope.kind === 'environment' ? b.scope.environment! : b.scope.namespace ? envOf(b.scope.cluster!, b.scope.namespace) : null,
  };
}
R('/admin/grants', (c) => {
  const q = c.query;
  const now = Date.now();
  let list = getDb().grants.map((g) => ({ ...g, expired: !!g.expiresAt && Date.parse(g.expiresAt) < now }));
  if (q.get('holderKind')) list = list.filter((g) => g.holder.kind === q.get('holderKind'));
  if (q.get('holderId')) list = list.filter((g) => g.holder.id === Number(q.get('holderId')));
  if (q.get('scopeKind')) list = list.filter((g) => g.scope.kind === q.get('scopeKind'));
  if (q.get('cluster')) list = list.filter((g) => g.scope.cluster === q.get('cluster'));
  if (q.get('argoProject')) list = list.filter((g) => g.scope.argoProject === q.get('argoProject'));
  if (q.get('action')) list = list.filter((g) => g.actions.includes(q.get('action') as Action));
  if (q.get('expiring') === 'true') list = list.filter((g) => g.expiresAt && !g.expired && Date.parse(g.expiresAt) < now + 14 * DAY);
  if (q.get('expired') === 'true') list = list.filter((g) => g.expired);
  return list;
});
R('/admin/grants/covering', (c) => {
  const q = c.query;
  const cluster = q.get('cluster') ?? '';
  const ns = q.get('namespace') ?? '';
  if (!cluster || !ns) throw invalid({ namespace: 'cluster and namespace are required' });
  const now = Date.now();
  return getDb().grants
    .map((g) => ({ ...g, expired: !!g.expiresAt && Date.parse(g.expiresAt) < now }))
    .filter((g) => covers(g, cluster, ns, (q.get('kind') as WorkloadKind) || null, q.get('name') || null).match);
});
R('/admin/grants', (c) => {
  const g = grantFromInput(c.body as unknown as GrantInput, nextId('grant'), c.user.username);
  getDb().grants.push(g);
  logAudit(c.user, 'admin.grant.created', null, 'succeeded', { details: { grantId: g.id, after: { holder: g.holder.name, scope: g.scope, actions: g.actions } } });
  return { __status: 201, data: g };
}, 'POST');
R('/admin/grants/:id', (c) => {
  const d = getDb();
  const i = d.grants.findIndex((g) => g.id === Number(c.params.id));
  if (i < 0) throw notFound();
  const before = d.grants[i];
  const g = grantFromInput(c.body as unknown as GrantInput, before.id, before.createdBy);
  g.createdAt = before.createdAt;
  d.grants[i] = g;
  logAudit(c.user, 'admin.grant.updated', null, 'succeeded', { details: { grantId: g.id, before: { actions: before.actions, expiresAt: before.expiresAt }, after: { actions: g.actions, expiresAt: g.expiresAt } } });
  return g;
}, 'PUT');
R('/admin/grants/:id', (c) => {
  const d = getDb();
  const g = d.grants.find((x) => x.id === Number(c.params.id));
  if (!g) throw notFound();
  d.grants = d.grants.filter((x) => x !== g);
  logAudit(c.user, 'admin.grant.deleted', null, 'succeeded', { details: { grantId: g.id, before: { holder: g.holder.name, actions: g.actions } } });
  return undefined;
}, 'DELETE');

R('/admin/access-check', (c) => {
  const b = c.body as { userId: number; cluster: string; namespace: string; workload?: { kind: WorkloadKind; name: string }; action: Action; at?: string };
  const u = getDb().users.find((x) => x.id === b.userId);
  if (!u) throw invalid({ userId: 'Pick a user.' });
  const info = decide(u, b.cluster, b.namespace, b.workload?.kind ?? null, b.workload?.name ?? null, b.action);
  const matched = userGrants(u).filter((g) => info.grantIds.includes(g.id));
  const out: DecisionExplain = {
    ...info, cluster: b.cluster, namespace: b.namespace, workload: b.workload?.name ?? null, action: b.action, evaluatedAt: b.at ?? iso(Date.now()),
    userRole: u.role, teams: u.teams.map((t) => t.name), matchedGrants: matched,
  };
  return out;
}, 'POST');

function crud<T extends { id: number }>(path: string, key: 'windows' | 'holidays', idKey: string, validate?: (b: Record<string, unknown>) => void) {
  R(path, () => getDb()[key]);
  R(path, (c) => {
    validate?.(c.body);
    const item = { ...c.body, id: nextId(idKey) } as unknown as T;
    (getDb()[key] as unknown as T[]).push(item);
    logAudit(c.user, `admin.${idKey}.created`, null, 'succeeded', { details: { after: c.body } });
    return { __status: 201, data: item };
  }, 'POST');
  R(`${path}/:id`, (c) => {
    validate?.(c.body);
    const arr = getDb()[key] as unknown as T[];
    const i = arr.findIndex((x) => x.id === Number(c.params.id));
    if (i < 0) throw notFound();
    const before = arr[i];
    arr[i] = { ...c.body, id: before.id } as unknown as T;
    logAudit(c.user, `admin.${idKey}.updated`, null, 'succeeded', { details: { before, after: arr[i] } });
    return arr[i];
  }, 'PUT');
  R(`${path}/:id`, (c) => {
    const arr = getDb()[key] as unknown as T[];
    const before = arr.find((x) => x.id === Number(c.params.id));
    if (!before) throw notFound();
    (getDb() as unknown as Record<string, unknown[]>)[key] = arr.filter((x) => x !== before);
    logAudit(c.user, `admin.${idKey}.deleted`, null, 'succeeded', { details: { before } });
    return undefined;
  }, 'DELETE');
}
crud('/admin/time-windows', 'windows', 'window', (b) => {
  const f: Record<string, string> = {};
  if (!String(b.name ?? '').trim()) f.name = 'Name is required.';
  if (!/^\d{2}:\d{2}$/.test(String(b.startTime))) f.startTime = 'Use HH:MM.';
  if (!/^\d{2}:\d{2}$/.test(String(b.endTime))) f.endTime = 'Use HH:MM.';
  if (!(b.daysOfWeek as number[] | undefined)?.length) f.daysOfWeek = 'Pick at least one day.';
  if (Object.keys(f).length) throw invalid(f);
});
crud('/admin/holidays', 'holidays', 'holiday', (b) => {
  if (!/^\d{4}-\d{2}-\d{2}$/.test(String(b.date))) throw invalid({ date: 'Use YYYY-MM-DD.' });
});

R('/admin/clusters', () => getDb().clusters);
function kubeconfigViolations(kc: string): string[] {
  const v: string[] = [];
  if (/cluster-admin/i.test(kc) || /admin/i.test(kc.split('\n').find((l) => /user|token/i.test(l)) ?? '')) {
    v.push('The credential can get Secrets in kube-system (it is bound to cluster-admin).', 'The credential can create Pods.', 'The credential can impersonate system:masters.');
  }
  return v;
}
R('/admin/clusters', (c) => {
  const b = c.body as { name?: string; environment?: string; kubeconfig?: string };
  const f: Record<string, string> = {};
  if (!b.name?.trim()) f.name = 'Cluster name is required.';
  if (!b.kubeconfig?.includes('server')) f.kubeconfig = 'This does not look like a kubeconfig (no server URL).';
  if (Object.keys(f).length) throw invalid(f);
  const d = getDb();
  if (d.clusters.some((x) => x.name === b.name)) throw new MockError(409, 'conflict', `Cluster ${b.name} already exists.`);
  const v = kubeconfigViolations(b.kubeconfig!);
  if (v.length) throw new MockError(422, 'invalid', 'Refused: the credential has too much power.', { violations: v });
  const cl = { name: b.name!.trim(), environment: b.environment || null, reachable: true, kubernetesVersion: 'v1.30.4', privilegeCheckOk: true, privilegeViolations: [], admissionMode: 'vap' as const, lastCheckAt: iso(Date.now()), usable: true, credentialLoaded: true, environmentFromSecret: false, protected: false };
  d.clusters.push(cl);
  logAudit(c.user, 'admin.cluster.added', null, 'succeeded', { details: { after: { name: cl.name, environment: cl.environment } } });
  return { __status: 201, data: cl };
}, 'POST');
R('/admin/clusters/:name', (c) => {
  const cl = getDb().clusters.find((x) => x.name === c.params.name);
  if (!cl) throw notFound();
  if (cl.environmentFromSecret) throw new MockError(409, 'conflict', `The environment of ${cl.name} comes from its Secret's tiphys/environment annotation; change it there.`);
  const before = cl.environment;
  cl.environment = String((c.body as { environment?: string }).environment ?? '') || null;
  logAudit(c.user, 'admin.cluster.updated', null, 'succeeded', { details: { cluster: cl.name, before: { environment: before }, after: { environment: cl.environment } } });
  return cl;
}, 'PUT');
R('/admin/clusters/:name/credential', (c) => {
  const cl = getDb().clusters.find((x) => x.name === c.params.name);
  if (!cl) throw notFound();
  const kc = String((c.body as { kubeconfig?: string }).kubeconfig ?? '');
  if (!kc.includes('server')) throw invalid({ kubeconfig: 'This does not look like a kubeconfig (no server URL).' });
  const v = kubeconfigViolations(kc);
  if (v.length) throw new MockError(422, 'invalid', 'Refused: the credential has too much power.', { violations: v });
  Object.assign(cl, { privilegeCheckOk: true, privilegeViolations: [], usable: cl.reachable, lastCheckAt: iso(Date.now()) });
  logAudit(c.user, 'admin.cluster.credential_replaced', null, 'succeeded', { details: { cluster: cl.name } });
  return cl;
}, 'POST');
R('/admin/clusters/:name/check', (c) => {
  const cl = getDb().clusters.find((x) => x.name === c.params.name);
  if (!cl) throw notFound();
  cl.lastCheckAt = iso(Date.now());
  return cl;
}, 'POST');
R('/admin/clusters/:name', (c) => {
  const d = getDb();
  if (!d.clusters.some((x) => x.name === c.params.name)) throw notFound();
  d.clusters = d.clusters.filter((x) => x.name !== c.params.name);
  return undefined;
}, 'DELETE');
R('/admin/environment-overrides', () => getDb().envOverrides);
R('/admin/environment-overrides', (c) => {
  const b = c.body as { kind?: 'argo_project' | 'namespace'; cluster?: string; argoProject?: string; namespace?: string; environment?: string };
  if (!b.environment?.trim()) throw invalid({ environment: 'Environment is required.' });
  if (b.kind === 'namespace' && (!b.cluster || !b.namespace)) throw invalid({ namespace: 'Cluster and namespace are required.' });
  if (b.kind === 'argo_project' && !b.argoProject) throw invalid({ argoProject: 'Pick a project.' });
  const o = { id: nextId('override'), kind: b.kind!, cluster: b.cluster, argoProject: b.argoProject, namespace: b.namespace, environment: b.environment };
  getDb().envOverrides.push(o);
  return { __status: 201, data: o };
}, 'POST');
R('/admin/environment-overrides/:id', (c) => { const d = getDb(); d.envOverrides = d.envOverrides.filter((o) => o.id !== Number(c.params.id)); return undefined; }, 'DELETE');
R('/admin/protected-namespaces', () => getDb().protectedNamespaces);
R('/admin/protected-namespaces', (c) => {
  const ns = String((c.body as { namespace?: string }).namespace ?? '').trim();
  if (!/^[a-z0-9]([-a-z0-9]*[a-z0-9])?$/.test(ns)) throw invalid({ namespace: 'Must be a valid namespace name (DNS-1123).' });
  const d = getDb();
  if (!d.protectedNamespaces.includes(ns)) d.protectedNamespaces.push(ns);
  return { __status: 201, data: d.protectedNamespaces };
}, 'POST');
R('/admin/protected-namespaces/:ns', (c) => { const d = getDb(); d.protectedNamespaces = d.protectedNamespaces.filter((n) => n !== c.params.ns); return undefined; }, 'DELETE');
const argoView = () => { const a = getDb().argo; const age = Math.round((Date.now() - a.fetchedAt) / 1000) + (a.stale ? 1200 : 0); return { fetchedAt: iso(a.fetchedAt), ageSeconds: age, stale: a.stale, appCount: a.appCount, projectCount: a.projectCount, lastError: a.stale ? 'argocd-server: connection refused' : a.lastError }; };
R('/admin/argo', () => argoView());
R('/admin/argo/refresh', () => { const a = getDb().argo; a.fetchedAt = Date.now(); a.stale = false; return argoView(); }, 'POST');
R('/admin/settings', () => getDb().settings);
R('/admin/settings', (c) => {
  const b = c.body as unknown as SettingsView;
  const f: Record<string, string> = {};
  for (const k of ['execMaxSessionMinutes', 'execIdleTimeoutMinutes', 'breakGlassDefaultMinutes', 'breakGlassMaxMinutes', 'breakGlassRequestMaxAgeMinutes', 'retentionAuditDays', 'retentionRecordingsDays'] as const) {
    if (!Number.isInteger(b[k]) || b[k] < 1) f[k] = 'Must be a positive whole number.';
  }
  if (!f.breakGlassDefaultMinutes && b.breakGlassDefaultMinutes > b.breakGlassMaxMinutes) f.breakGlassDefaultMinutes = 'Default cannot exceed the maximum.';
  if (Object.keys(f).length) throw invalid(f);
  const d = getDb();
  const before = d.settings;
  d.settings = { ...b, lokiRetentionInfo: before.lokiRetentionInfo };
  logAudit(c.user, 'admin.settings.updated', null, 'succeeded', { details: { before: { ...before, rocket: undefined }, after: { ...d.settings, rocket: undefined } } });
  return d.settings;
}, 'PUT');
R('/admin/settings/rocket/test', () => {
  if (!getDb().settings.rocket.webhookUrl) throw new MockError(502, 'cluster_error', 'The webhook URL is empty.');
  return undefined;
}, 'POST');
R('/admin/overview', () => {
  const d = getDb();
  return {
    activeExecSessions: d.sessions.filter((s) => s.active).length, openLogStreams: 3,
    pendingRequests: d.requests.filter((r) => r.status === 'pending').length,
    pendingBreakGlassRequests: (expireBgRequests(), d.bgRequests.filter((r) => r.status === 'pending').length),
    activeBreakGlasses: d.breakGlasses.filter((b) => b.active && Date.parse(b.expiresAt) > Date.now()).length,
    clustersUnreachable: d.clusters.filter((x) => !x.reachable).length,
  };
});

export { personaRole };
