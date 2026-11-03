import type {
  AccessRequest, AuditEvent, BreakGlass, BreakGlassRequest, ClusterView, EnvironmentOverride, ExecSessionView, GrantView,
  HolidayView, JobSummary, K8sEvent, SettingsView, TimeWindowView, UserView, WorkloadKind, Role, Action,
} from '../api/types';

export const MIN = 60_000;
export const HOUR = 3_600_000;
export const DAY = 86_400_000;
export const CSRF = 'mock-csrf-token';

export interface MockUser extends UserView {
  teamIds: number[];
}

export interface WlDef {
  kind: WorkloadKind;
  name: string;
  desired: number;
  ready: number;
  strategy?: string;
  images?: string[];
  schedule?: string;
  suspended?: boolean;
  concurrencyPolicy?: string;
  lastResult?: 'succeeded' | 'failed' | 'running' | null;
  lastScheduleMinutesAgo?: number;
  // Set by the mock live stream: restarts of the first pod, a new pod during a rollout, newer events.
  liveRestarts?: number;
  liveSurge?: boolean;
  liveEvents?: K8sEvent[];
}
export interface NsDef {
  cluster: string;
  namespace: string;
  workloads: WlDef[];
}
export interface AppDef {
  name: string;
  project: string;
  cluster: string;
  namespace: string;
  health: 'Healthy' | 'Progressing' | 'Degraded';
  sync: 'Synced' | 'OutOfSync';
}

export interface Operation {
  id: string;
  kind: 'restart' | 'run-job' | 'sync';
  cluster: string;
  namespace: string;
  name: string;
  jobName?: string;
  owner: number;
  willFail?: boolean;
}

export interface Db {
  now0: number;
  users: MockUser[];
  teams: { id: number; name: string; description: string; memberIds: number[] }[];
  clusters: ClusterView[];
  apps: AppDef[];
  namespaces: NsDef[];
  protectedNamespaces: string[];
  envOverrides: EnvironmentOverride[];
  grants: GrantView[];
  windows: TimeWindowView[];
  holidays: HolidayView[];
  requests: AccessRequest[];
  breakGlasses: BreakGlass[];
  bgRequests: BreakGlassRequest[];
  sessions: ExecSessionView[];
  audit: AuditEvent[];
  settings: SettingsView;
  argo: { fetchedAt: number; stale: boolean; appCount: number; projectCount: number; lastError: string | null };
  jobs: Record<string, JobSummary[]>; // key cluster/ns/cronjob
  ops: Record<string, Operation>;
  nextId: Record<string, number>;
}

const iso = (t: number) => new Date(t).toISOString();

let db: Db | null = null;
export function getDb(): Db {
  if (!db) db = seed();
  return db;
}
export function nextId(key: string): number {
  const d = getDb();
  d.nextId[key] = (d.nextId[key] ?? 1000) + 1;
  return d.nextId[key];
}

export function personaRole(): Role {
  try {
    const r = window.localStorage.getItem('tiphys.mockRole');
    if (r === 'admin' || r === 'auditor' || r === 'user') return r;
  } catch { /* ignore */ }
  return 'user';
}
export function personaId(role: Role): number {
  return role === 'admin' ? 2 : role === 'auditor' ? 3 : 1;
}
export function currentUser(): MockUser {
  const d = getDb();
  return d.users.find((u) => u.id === personaId(personaRole()))!;
}

function seed(): Db {
  const now = Date.now();
  const teams = [
    { id: 1, name: 'Payments', description: 'Payments platform', memberIds: [1, 5, 6, 7] },
    { id: 2, name: 'Search', description: 'Search and indexing', memberIds: [4, 9] },
    { id: 3, name: 'Notifications', description: 'Notifications and digests', memberIds: [5] },
    { id: 4, name: 'Platform', description: 'Platform / SRE', memberIds: [2] },
    { id: 5, name: 'Security', description: 'Security and audit', memberIds: [3] },
  ];
  const mkUser = (id: number, username: string, displayName: string, role: Role, extra: Partial<MockUser> = {}): MockUser => ({
    id, username, displayName, role, disabled: false, keycloakBound: true, local: false,
    lastLoginAt: iso(now - id * 40 * MIN), teams: [], teamIds: [], activeSessions: 0, ...extra,
  });
  const users: MockUser[] = [
    mkUser(1, 'sara.moradi', 'Sara Moradi', 'user', { activeSessions: 1 }),
    mkUser(2, 'reza.ahmadi', 'Reza Ahmadi', 'admin'),
    mkUser(3, 'leila.safavi', 'Leila Safavi', 'auditor'),
    mkUser(4, 'nima.karimi', 'Nima Karimi', 'user'),
    mkUser(5, 'mina.hosseini', 'Mina Hosseini', 'user'),
    mkUser(6, 'arash.rahimi', 'Arash Rahimi', 'user', { lastLoginAt: iso(now - DAY - HOUR) }),
    mkUser(7, 'kian.jalali', 'Kian Jalali', 'user'),
    mkUser(8, 'parisa.d', 'Parisa Dehghani', 'user', { disabled: true, lastLoginAt: iso(now - 48 * DAY) }),
    mkUser(9, 'omid.tavakoli', '', 'user', { keycloakBound: false, lastLoginAt: null }),
    mkUser(10, 'ops-bot', 'Ops bot', 'user', { keycloakBound: false, local: true }),
  ];
  for (const u of users) {
    u.teamIds = teams.filter((t) => t.memberIds.includes(u.id)).map((t) => t.id);
    u.teams = u.teamIds.map((id) => ({ id, name: teams.find((t) => t.id === id)!.name }));
  }

  const clusters: ClusterView[] = [
    { name: 'prod-1', environment: 'prod', reachable: true, kubernetesVersion: 'v1.30.4', privilegeCheckOk: true, privilegeViolations: [], admissionMode: 'vap', lastCheckAt: iso(now - 20_000), usable: true, credentialLoaded: true, environmentFromSecret: false, protected: false },
    { name: 'prod-2', environment: 'prod', reachable: false, kubernetesVersion: 'v1.30.4', privilegeCheckOk: true, privilegeViolations: [], admissionMode: 'vap', lastCheckAt: iso(now - 11 * MIN), usable: false, credentialLoaded: true, environmentFromSecret: false, protected: false },
    { name: 'staging-1', environment: 'staging', reachable: true, kubernetesVersion: 'v1.29.8', privilegeCheckOk: true, privilegeViolations: [], admissionMode: 'kyverno', lastCheckAt: iso(now - 15_000), usable: true, credentialLoaded: true, environmentFromSecret: true, protected: true },
    { name: 'staging-2', environment: 'staging', reachable: true, kubernetesVersion: 'v1.29.8', privilegeCheckOk: false, privilegeViolations: ['credential can get Secrets in kube-system (bound to cluster-admin)', 'credential can create Pods'], admissionMode: 'none', lastCheckAt: iso(now - 15_000), usable: false, credentialLoaded: true, environmentFromSecret: false, protected: false },
  ];

  const apps: AppDef[] = [
    { name: 'payments-api', project: 'payments', cluster: 'prod-1', namespace: 'payments-api', health: 'Healthy', sync: 'Synced' },
    { name: 'payments-api-config', project: 'payments', cluster: 'prod-1', namespace: 'payments-api', health: 'Healthy', sync: 'OutOfSync' },
    { name: 'search-indexer', project: 'search', cluster: 'prod-2', namespace: 'search-indexer', health: 'Progressing', sync: 'OutOfSync' },
    { name: 'payments-worker', project: 'payments', cluster: 'prod-1', namespace: 'payments-worker', health: 'Healthy', sync: 'Synced' },
    { name: 'notifications-api', project: 'notifications', cluster: 'prod-1', namespace: 'notifications-api', health: 'Degraded', sync: 'Synced' },
    { name: 'search-api', project: 'search', cluster: 'prod-2', namespace: 'search-api', health: 'Healthy', sync: 'Synced' },
    { name: 'payments-api', project: 'payments', cluster: 'staging-1', namespace: 'payments-api', health: 'Healthy', sync: 'OutOfSync' },
    { name: 'notifications-worker', project: 'notifications', cluster: 'staging-1', namespace: 'notifications-worker', health: 'Healthy', sync: 'Synced' },
    { name: 'search-indexer', project: 'search', cluster: 'staging-1', namespace: 'search-indexer', health: 'Healthy', sync: 'Synced' },
  ];

  const dep = (name: string, desired: number, ready = desired, extra: Partial<WlDef> = {}): WlDef => ({ kind: 'Deployment', name, desired, ready, strategy: 'RollingUpdate', images: [`${name}:2026.10.17-4`], ...extra });
  const cron = (name: string, schedule: string, extra: Partial<WlDef> = {}): WlDef => ({ kind: 'CronJob', name, desired: 0, ready: 0, schedule, concurrencyPolicy: 'Forbid', lastResult: 'succeeded', ...extra });
  const payCrons = [
    cron('reconcile-ledger', '*/15 * * * *', { lastScheduleMinutesAgo: 11 }),
    cron('daily-settlement', '0 2 * * *', { lastScheduleMinutesAgo: 660, concurrencyPolicy: 'Allow' }),
    cron('expire-pending-charges', '0 */6 * * *', { lastResult: 'running', lastScheduleMinutesAgo: 101 }),
    cron('archive-reports', '0 3 * * 0', { suspended: true, lastResult: 'failed', lastScheduleMinutesAgo: 3 * 24 * 60 }),
  ];
  const namespaces: NsDef[] = [
    { cluster: 'prod-1', namespace: 'payments-api', workloads: [
      dep('payments-api', 3), dep('payments-gateway', 2),
      { kind: 'Rollout', name: 'payments-canary', desired: 4, ready: 4, strategy: 'Canary', images: ['payments-api:2026.10.18-1'] },
      ...payCrons,
    ] },
    { cluster: 'prod-1', namespace: 'payments-worker', workloads: [dep('payments-worker', 2), { kind: 'StatefulSet', name: 'payments-ledger', desired: 2, ready: 2, strategy: 'RollingUpdate', images: ['ledger:1.9.2'] }] },
    { cluster: 'prod-1', namespace: 'notifications-api', workloads: [dep('notifications-api', 2, 1), dep('notifications-dispatcher', 2)] },
    { cluster: 'prod-2', namespace: 'search-indexer', workloads: [dep('search-indexer', 2), dep('search-indexer-ui', 1)] },
    { cluster: 'prod-2', namespace: 'search-api', workloads: [dep('search-api', 2), dep('search-api-cache', 2), { kind: 'DaemonSet', name: 'search-node-agent', desired: 4, ready: 4, strategy: 'RollingUpdate', images: ['agent:3.1'] }, dep('search-api-gw', 1)] },
    { cluster: 'staging-1', namespace: 'payments-api', workloads: [
      dep('payments-api', 3), dep('payments-gateway', 2),
      { kind: 'Rollout', name: 'payments-canary', desired: 4, ready: 4, strategy: 'Canary', images: ['payments-api:2026.10.18-1'] },
      cron('reconcile-ledger', '*/15 * * * *', { lastScheduleMinutesAgo: 6 }),
    ] },
    { cluster: 'staging-1', namespace: 'notifications-worker', workloads: [dep('notifications-worker', 1)] },
    { cluster: 'staging-1', namespace: 'search-indexer', workloads: [{ kind: 'StatefulSet', name: 'search-indexer', desired: 2, ready: 2, strategy: 'RollingUpdate', images: ['indexer:5.0'] }, dep('search-indexer-ui', 1)] },
  ];

  const g = (id: number, holder: GrantView['holder'], scope: GrantView['scope'], actions: Action[], expiresAt: number | null, note: string, createdBy = 'reza.ahmadi', extra: Partial<GrantView> = {}): GrantView => ({
    id, holder, scope, actions, expiresAt: expiresAt ? iso(expiresAt) : null, note, expired: !!expiresAt && expiresAt < now,
    createdBy, createdAt: iso(now - 20 * DAY), environment: null, ...extra,
  });
  const grants: GrantView[] = [
    g(1, { kind: 'team', id: 1, name: 'Payments' }, { kind: 'argo_project', argoProject: 'payments' }, ['logs', 'exec', 'restart', 'run-job', 'sync'], null, 'Core team'),
    g(2, { kind: 'team', id: 1, name: 'Payments' }, { kind: 'namespace', cluster: 'prod-2', namespace: 'search-indexer' }, ['logs'], null, 'Cross-team debugging'),
    g(3, { kind: 'user', id: 1, name: 'Sara Moradi' }, { kind: 'workloads', cluster: 'staging-1', namespace: 'payments-api', workloads: [{ kind: 'Deployment', name: 'payments-api' }, { kind: 'Deployment', name: 'payments-gateway' }] }, ['logs', 'exec', 'run-job'], now + 13 * DAY, 'Release prep'),
    g(4, { kind: 'team', id: 1, name: 'Payments' }, { kind: 'namespace', cluster: 'staging-1', namespace: 'notifications-worker' }, ['logs'], null, ''),
    g(5, { kind: 'user', id: 6, name: 'Arash Rahimi' }, { kind: 'namespace', cluster: 'prod-1', namespace: 'payments-api' }, ['exec'], now - 8 * DAY, ''),
    g(6, { kind: 'team', id: 2, name: 'Search' }, { kind: 'argo_project', argoProject: 'search' }, ['logs'], null, ''),
    g(7, { kind: 'team', id: 2, name: 'Search' }, { kind: 'namespace', cluster: 'staging-1', namespace: 'search-indexer' }, ['logs', 'exec'], null, ''),
    g(8, { kind: 'team', id: 2, name: 'Search' }, { kind: 'namespace', cluster: 'prod-2', namespace: 'search-indexer' }, ['logs', 'exec'], null, ''),
    g(9, { kind: 'user', id: 1, name: 'Sara Moradi' }, { kind: 'namespace', cluster: 'prod-1', namespace: 'notifications-api' }, ['logs'], now + 1 * DAY, 'Approved request'),
    g(10, { kind: 'team', id: 1, name: 'Payments' }, { kind: 'workloads', cluster: 'prod-1', namespace: 'payments-api', workloads: [{ kind: 'Deployment', name: 'payments-api' }, { kind: 'Deployment', name: 'payments-gateway' }] }, ['logs', 'exec'], now + 41 * DAY, 'Release freeze coverage'),
  ];
  // Sara's expired direct grant (design: namespace payments-api on prod-1, exec, expired)
  grants.push(g(11, { kind: 'user', id: 1, name: 'Sara Moradi' }, { kind: 'namespace', cluster: 'prod-1', namespace: 'payments-worker' }, ['exec'], now - 8 * DAY, ''));

  const windows: TimeWindowView[] = [
    { id: 1, name: 'Prod working hours', scopeKind: 'environment', scopeValue: 'prod', actions: ['exec', 'restart', 'run-job'], daysOfWeek: [6, 0, 1, 2, 3], startTime: '08:00', endTime: '17:00', timezone: 'Asia/Tehran', effect: 'allow_inside', enabled: true },
    { id: 2, name: 'Prod change freeze', scopeKind: 'environment', scopeValue: 'prod', actions: ['exec', 'restart', 'run-job'], daysOfWeek: [2], startTime: '13:00', endTime: '15:00', timezone: 'Asia/Tehran', effect: 'deny_inside', enabled: true },
    { id: 3, name: 'Contractors, business hours', scopeKind: 'team', scopeValue: 'Contractors', actions: ['logs'], daysOfWeek: [6, 0, 1, 2, 3], startTime: '09:00', endTime: '16:00', timezone: 'Asia/Tehran', effect: 'allow_inside', enabled: true },
    { id: 4, name: 'Staging exec', scopeKind: 'cluster', scopeValue: 'staging-1', actions: ['exec'], daysOfWeek: [0, 1, 2, 3, 4, 5, 6], startTime: '07:00', endTime: '23:00', timezone: 'Asia/Tehran', effect: 'allow_inside', enabled: true },
    { id: 5, name: 'Night batch (crosses midnight)', scopeKind: 'environment', scopeValue: 'staging', actions: ['run-job'], daysOfWeek: [6, 0, 1, 2, 3, 4], startTime: '22:00', endTime: '06:00', timezone: 'Asia/Tehran', effect: 'allow_inside', enabled: false },
  ];
  const d = (offset: number) => iso(now + offset * DAY).slice(0, 10);
  const holidays: HolidayView[] = [
    { id: 1, date: d(8), title: 'Public holiday', environments: ['prod'] },
    { id: 2, date: d(22), title: 'Company day off', environments: ['prod', 'staging'] },
    { id: 3, date: d(62), title: 'Public holiday', environments: ['prod'] },
    { id: 4, date: d(85), title: 'Company day off', environments: [] },
  ];

  const req = (id: number, userId: number, scope: AccessRequest['scope'], actions: Action[], hours: number | null, reason: string, status: AccessRequest['status'], createdAgo: number, extra: Partial<AccessRequest> = {}): AccessRequest => ({
    id, userId, username: users.find((u) => u.id === userId)!.username, scope, actions, durationHours: hours, reason, status,
    decidedBy: status === 'pending' ? null : 'reza.ahmadi', decidedAt: status === 'pending' ? null : iso(now - createdAgo + 30 * MIN),
    decisionNote: null, resultingGrantId: null, createdAt: iso(now - createdAgo), ...extra,
  });
  const requests: AccessRequest[] = [
    req(1, 4, { kind: 'workloads', cluster: 'prod-2', namespace: 'search-indexer', workloads: [{ kind: 'StatefulSet', name: 'search-indexer' }] }, ['logs', 'exec'], 24, 'Index rebuild is stuck on shard 4. I need a shell to inspect the lock files and the node\'s disk usage. Logs alone don\'t show it.', 'pending', 2 * HOUR),
    req(2, 5, { kind: 'namespace', cluster: 'staging-1', namespace: 'notifications-worker' }, ['logs', 'run-job'], 168, 'Need to replay failed digests after yesterday\'s outage.', 'pending', 2.4 * HOUR),
    req(3, 6, { kind: 'workloads', cluster: 'prod-1', namespace: 'payments-worker', workloads: [{ kind: 'Deployment', name: 'payments-worker' }] }, ['restart'], 1, 'Worker leaking connections; restarting is the only fix until the patch ships.', 'pending', 26 * HOUR),
    req(4, 1, { kind: 'workloads', cluster: 'prod-2', namespace: 'search-indexer', workloads: [{ kind: 'StatefulSet', name: 'search-indexer' }] }, ['exec'], 24, 'Checking indexer lock files for the search team.', 'pending', 2.2 * HOUR),
    // Approving adds restart to Sara's grant 9 on the same namespace (it ends sooner).
    req(8, 1, { kind: 'namespace', cluster: 'prod-1', namespace: 'notifications-api' }, ['restart'], 168, 'Digest workers hang after the queue failover; I need to restart them while we fix it.', 'pending', 0.5 * HOUR),
    req(5, 1, { kind: 'workloads', cluster: 'prod-1', namespace: 'payments-api', workloads: [{ kind: 'Deployment', name: 'payments-api' }] }, ['restart'], 1, 'Restart after config change.', 'rejected', 2 * DAY, { decisionNote: "Use the deploy pipeline for restarts. Ping me if it's an incident." }),
    req(6, 1, { kind: 'namespace', cluster: 'prod-1', namespace: 'notifications-api' }, ['logs'], 168, 'Investigating digest delays.', 'approved', 6 * DAY, { resultingGrantId: 9 }),
    req(7, 1, { kind: 'namespace', cluster: 'prod-1', namespace: 'payments-worker' }, ['exec'], 8, 'Debug ledger worker.', 'expired', 14 * DAY),
  ];

  const settings: SettingsView = {
    breakGlassMode: 'self', breakGlassRequestMaxAgeMinutes: 60, execMaxSessionMinutes: 30, execIdleTimeoutMinutes: 10, breakGlassDefaultMinutes: 60, breakGlassMaxMinutes: 120,
    retentionAuditDays: 365, retentionRecordingsDays: 90,
    rocket: { webhookUrl: 'https://chat.internal/hooks/9fK2xQ7', channel: '#platform-access', events: { break_glass_started: true, break_glass_requested: true, request_created: true, request_decided: false, exec_in_prod: true, admin_changes: true } },
    lokiRetentionInfo: 'Loki keeps audit lines for 400 days (set by operations).',
  };

  const bgNima: BreakGlass = { id: 1, userId: 4, username: 'nima.karimi', reason: 'Index rebuild is stuck on shard 4 and the time window closed at 13:00; need to inspect lock files.', startedAt: iso(now - 45 * MIN), expiresAt: iso(now - 15 * MIN), endedAt: iso(now - 15 * MIN), endedBy: 'nima.karimi', active: false };

  // A pending break-glass request (shown on the admin's Access requests page) and a decided one.
  const bgRequests: BreakGlassRequest[] = [
    { id: 2, userId: 6, username: 'arash.rahimi', reason: 'Notifications worker is stuck on a poisoned message and prod exec is closed tonight.', durationMinutes: 30, status: 'pending', decidedBy: null, decidedAt: null, decisionNote: null, approvedMinutes: null, breakGlassId: null, createdAt: iso(now - 6 * MIN), expiresAt: iso(now + 54 * MIN) },
    { id: 1, userId: 4, username: 'nima.karimi', reason: bgNima.reason, durationMinutes: 30, status: 'approved', decidedBy: 'reza.ahmadi', decidedAt: iso(now - 45 * MIN), decisionNote: null, approvedMinutes: 30, breakGlassId: 1, createdAt: iso(now - 50 * MIN), expiresAt: iso(now + 10 * MIN) },
  ];

  const sess = (id: string, userId: number, pod: string, cluster: string, ns: string, wl: string, container: string, env: string, startedAgoMin: number, durSec: number | null, endReason: ExecSessionView['endReason'], extra: Partial<ExecSessionView> = {}): ExecSessionView => {
    const u = users.find((x) => x.id === userId)!;
    return {
      id, userId, username: u.username, cluster, namespace: ns, workloadKind: 'Deployment', workloadName: wl, pod, container, shell: '/bin/sh', debugContainer: null,
      environment: env, breakGlassId: null, startedAt: iso(now - startedAgoMin * MIN), endedAt: durSec === null ? null : iso(now - startedAgoMin * MIN + durSec * 1000),
      durationSeconds: durSec, endReason, recordingBytes: durSec === null ? 18_400 : 42_000 + durSec * 31, active: durSec === null, ...extra,
    };
  };
  const sessions: ExecSessionView[] = [
    sess('sess_7Hq2Lm', 1, 'payments-api-7d9c6b5f4-2kq8x', 'prod-1', 'payments-api', 'payments-api', 'app', 'prod', 28, null, null),
    sess('sess_Kx81Pw', 7, 'payments-worker-6c8d7-x4m9d', 'prod-1', 'payments-worker', 'payments-worker', 'worker', 'prod', 34, null, null),
    sess('sess_9Fk3Qa', 4, 'search-indexer-6f7d9-q8zcl', 'prod-2', 'search-indexer', 'search-indexer', 'indexer', 'prod', 60 + 25, 708, 'client_closed', { breakGlassId: 1, workloadKind: 'StatefulSet' }),
    sess('sess_Bn42Ty', 5, 'notifications-api-5c8d7-h7r2s', 'staging-1', 'notifications-worker', 'notifications-worker', 'app', 'staging', 170, 390, 'client_closed'),
    sess('sess_Rt55Zc', 6, 'payments-api-7d9c6b5f4-xv7wd', 'prod-1', 'payments-api', 'payments-api', 'app', 'prod', 210, 1800, 'max_duration'),
    sess('sess_Wq09Dd', 1, 'payments-api-7d9c6b5f4-9mzt5', 'prod-1', 'payments-api', 'payments-api', 'app', 'prod', 260, 845, 'outside_window'),
    sess('sess_Lm73Hh', 4, 'search-api-5d6b9-b5k1j', 'prod-2', 'search-api', 'search-api', 'app', 'prod', 20 * 60, 201, 'terminated_by_admin'),
  ];
  users[0].activeSessions = 1;

  const mkEv = (id: number, agoMin: number, username: string, userId: number | null, eventType: string, action: Action | null, result: AuditEvent['result'], extra: Partial<AuditEvent> = {}): AuditEvent => ({
    id, at: iso(now - agoMin * MIN), userId, username, eventType, action, cluster: null, namespace: null, workloadKind: null, workloadName: null,
    pod: null, container: null, environment: null, result, reason: null, breakGlassId: null, sessionId: null, details: null,
    sourceIp: '10.20.4.' + (10 + (id % 40)), userAgent: 'Mozilla/5.0 (Macintosh) Chrome/130', ...extra,
  });
  const audit: AuditEvent[] = [];
  let aid = 5000;
  const push = (agoMin: number, ...rest: [string, number | null, string, Action | null, AuditEvent['result'], Partial<AuditEvent>?]) => {
    const [username, userId, eventType, action, result, extra] = rest;
    audit.push(mkEv(aid--, agoMin, username, userId, eventType, action, result, extra ?? {}));
  };
  push(2, 'sara.moradi', 1, 'exec.started', 'exec', 'succeeded', { cluster: 'prod-1', namespace: 'payments-api', workloadKind: 'Deployment', workloadName: 'payments-api', pod: 'payments-api-7d9c6b5f4-2kq8x', container: 'app', environment: 'prod', sessionId: 'sess_7Hq2Lm', details: { shell: '/bin/sh', matchedGrant: 'Team Payments → Argo project payments' } });
  push(5, 'reza.ahmadi', 2, 'admin.grant.updated', null, 'succeeded', { details: { grantId: 1, holder: 'team Payments', before: { actions: ['logs', 'exec'] }, after: { actions: ['logs', 'exec', 'restart'] } } });
  push(18, 'nima.karimi', 4, 'exec.denied', 'exec', 'denied', { cluster: 'prod-2', namespace: 'search-indexer', workloadName: 'search-indexer', environment: 'prod', reason: 'outside_window' });
  push(20, 'nima.karimi', 4, 'break_glass.started', null, 'succeeded', { cluster: 'prod-2', namespace: 'search-indexer', environment: 'prod', breakGlassId: 1, details: { reason: bgNima.reason, minutes: 30 } });
  push(21, 'mina.hosseini', 5, 'logs.viewed', 'logs', 'allowed', { cluster: 'staging-1', namespace: 'notifications-worker', workloadName: 'notifications-worker', pod: 'notifications-worker-5c8d7-hq2lm', container: 'app', environment: 'staging', details: { tailLines: 1000, follow: true } });
  push(35, 'sara.moradi', 1, 'restart', 'restart', 'succeeded', { cluster: 'prod-1', namespace: 'payments-api', workloadKind: 'Deployment', workloadName: 'payments-gateway', environment: 'prod', details: { outcome: 'succeeded' } });
  push(40, 'arash.rahimi', 6, 'exec.denied', 'exec', 'denied', { cluster: 'prod-1', namespace: 'payments-api', workloadName: 'payments-api', environment: 'prod', reason: 'no_grant' });
  push(58, 'mina.hosseini', 5, 'access_request.created', null, 'succeeded', { cluster: 'staging-1', namespace: 'notifications-worker', environment: 'staging', details: { requestId: 2 } });
  push(70, 'kian.jalali', 7, 'logs.downloaded', 'logs', 'succeeded', { cluster: 'prod-1', namespace: 'payments-worker', workloadName: 'payments-worker', environment: 'prod', details: { bytes: 5_242_880, truncated: false } });
  push(88, 'nima.karimi', 4, 'run-job', 'run-job', 'failed', { cluster: 'prod-1', namespace: 'payments-api', workloadName: 'reconcile-ledger', environment: 'prod', details: { outcome: 'failed', message: 'BackoffLimitExceeded' } });
  push(112, 'reza.ahmadi', 2, 'admin.time_window.updated', null, 'succeeded', { details: { id: 2, name: 'Prod change freeze', before: { startTime: '13:00', endTime: '14:00' }, after: { startTime: '13:00', endTime: '15:00' } } });
  push(130, 'leila.safavi', 3, 'session.recording.read', null, 'succeeded', { sessionId: 'sess_9Fk3Qa', environment: 'prod' });
  push(160, 'reza.ahmadi', 2, 'admin.user.updated', null, 'succeeded', { details: { username: 'parisa.d', before: { disabled: false }, after: { disabled: true } } });
  push(190, 'sara.moradi', 1, 'login', null, 'succeeded');
  push(250, 'sara.moradi', 1, 'exec.ended', 'exec', 'succeeded', { cluster: 'prod-1', namespace: 'payments-api', pod: 'payments-api-7d9c6b5f4-9mzt5', environment: 'prod', sessionId: 'sess_Wq09Dd', reason: 'outside_window' });
  for (let i = 0; i < 90; i++) {
    const uu = users[(i * 3) % 7];
    const kinds: [string, Action | null, AuditEvent['result']][] = [['logs.viewed', 'logs', 'allowed'], ['logs.viewed', 'logs', 'allowed'], ['exec.started', 'exec', 'succeeded'], ['restart', 'restart', 'succeeded'], ['exec.denied', 'exec', 'denied'], ['login', null, 'succeeded']];
    const [et, ac, rs] = kinds[i % kinds.length];
    const prod = i % 3 !== 0;
    push(300 + i * 13, uu.username, uu.id, et, ac, rs, {
      cluster: prod ? 'prod-1' : 'staging-1', namespace: prod ? 'payments-api' : 'notifications-worker', workloadName: prod ? 'payments-api' : 'notifications-worker', environment: prod ? 'prod' : 'staging',
      reason: rs === 'denied' ? 'no_grant' : null,
    });
  }
  audit.sort((a, b) => Date.parse(b.at) - Date.parse(a.at));

  // recent jobs per cronjob
  const mkJob = (cronJob: string, name: string, minAgo: number, status: JobSummary['status'], manual: boolean, by: string | null, finishedMin?: number): JobSummary => ({
    name, cronJob, triggeredBy: by, manual, status, active: status === 'running' ? 1 : 0, succeeded: status === 'succeeded' ? 1 : 0, completions: 1,
    startedAt: iso(now - minAgo * MIN), finishedAt: status === 'running' ? null : iso(now - (finishedMin ?? minAgo - 1) * MIN), podNames: [`${name}-x7k2p`],
  });
  const jobs: Record<string, JobSummary[]> = {
    'prod-1/payments-api/reconcile-ledger': [mkJob('reconcile-ledger', 'reconcile-ledger-29341530', 11, 'succeeded', false, null)],
    'prod-1/payments-api/daily-settlement': [mkJob('daily-settlement', 'daily-settlement-29341080', 660, 'succeeded', false, null)],
    'prod-1/payments-api/expire-pending-charges': [mkJob('expire-pending-charges', 'expire-pending-charges-29340720', 101, 'running', false, null)],
    'prod-1/payments-api/archive-reports': [mkJob('archive-reports', 'archive-reports-29338400', 3 * 24 * 60, 'failed', false, null)],
    'staging-1/payments-api/reconcile-ledger': [mkJob('reconcile-ledger', 'reconcile-ledger-29341535', 6, 'succeeded', false, null)],
  };

  return {
    now0: now, users, teams, clusters, apps, namespaces, protectedNamespaces: ['kube-system', 'argocd', 'cert-manager'],
    envOverrides: [
      { id: 1, kind: 'namespace', cluster: 'prod-1', namespace: 'payments-sandbox', environment: 'staging' },
      { id: 2, kind: 'argo_project', cluster: 'prod-2', argoProject: 'search-lab', environment: 'staging' },
    ],
    grants, windows, holidays, requests, breakGlasses: [bgNima], bgRequests, sessions, audit, settings,
    argo: { fetchedAt: now - 42_000, stale: false, appCount: apps.length, projectCount: 3, lastError: null },
    jobs, ops: {}, nextId: { grant: 100, request: 100, user: 100, team: 100, window: 100, holiday: 100, bg: 100, bgRequest: 100, override: 100, audit: 9000 },
  };
}
