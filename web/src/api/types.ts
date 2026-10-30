// Mirrors docs/api.md. Keep in sync with the backend contract.

/** `sync` acts on a whole Argo CD application; the others on a workload. */
export type Action = 'logs' | 'exec' | 'restart' | 'run-job' | 'sync';
export type WorkloadAction = Exclude<Action, 'sync'>;
export type Role = 'user' | 'auditor' | 'admin';
export type WorkloadKind = 'Deployment' | 'StatefulSet' | 'DaemonSet' | 'Rollout' | 'CronJob';
export type DenyReason =
  | 'protected_namespace'
  | 'user_disabled'
  | 'role'
  | 'no_grant'
  | 'grant_expired'
  | 'outside_window'
  | 'holiday'
  | 'argo_stale'
  | 'app_scope';

export interface DecisionInfo {
  allowed: boolean;
  reason: DenyReason | 'allowed' | 'admin';
  environment: string;
  grantIds: number[];
  breakGlassId: number | null;
  reopensAt: string | null;
  closesAt: string | null;
}
export type AccessMap = Record<WorkloadAction, DecisionInfo>;

// ---- errors
export interface ApiErrorBody {
  error: { code: string; message: string; details?: Record<string, unknown> };
}

// ---- auth
export interface Me {
  id: number;
  username: string;
  displayName: string;
  role: Role;
  teams: { id: number; name: string }[];
  csrfToken: string;
  breakGlass: BreakGlass | null;
  settings: {
    /** False when break-glass is turned off in the settings: the UI hides it. */
    breakGlassEnabled: boolean;
    breakGlassDefaultMinutes: number;
    breakGlassMaxMinutes: number;
    execIdleTimeoutMinutes: number;
    execMaxSessionMinutes: number;
    /** True when clusters can be added from the UI (CLUSTER_REGISTRATION_FROM_UI on the server). */
    clusterRegistration: boolean;
    /** True when the portal offers debug containers (DEBUG_IMAGE on the server). */
    debugEnabled: boolean;
    /** Argo CD's address in the browser, for links to applications. */
    argoUrl: string;
    /** True when admins may create users with a portal password (LOCAL_USERS_ENABLED on the server). */
    localUsers: boolean;
  };
  argo: { fetchedAt: string | null; ageSeconds: number | null; stale: boolean };
}

export interface BreakGlass {
  id: number;
  userId: number;
  username: string;
  reason: string;
  startedAt: string;
  expiresAt: string;
  endedAt: string | null;
  endedBy: string | null;
  active: boolean;
}

// ---- catalog / workloads
export interface AppSummary {
  name: string;
  project: string;
  cluster: string;
  namespace: string;
  environment: string;
  health: 'Healthy' | 'Progressing' | 'Degraded' | 'Suspended' | 'Missing' | 'Unknown';
  sync: 'Synced' | 'OutOfSync' | 'Unknown';
  workloadCount: number | null;
  access: Action[];
}

/** An Argo CD application deploying to a namespace (its destination or any of its resources). */
export interface NamespaceApp {
  name: string;
  project: string;
  appNamespace: string;
  health: string;
  sync: string;
  /** End of the last sync operation; null if none finished (never synced, or running). */
  lastSyncAt: string | null;
  lastSyncPhase: 'Running' | 'Succeeded' | 'Failed' | 'Error' | 'Terminating' | null;
  /** Whether the viewer may sync the whole app (every namespace it deploys to). */
  access: { sync: DecisionInfo };
  /** The namespace that decision is about: the one that denied, or the destination. */
  syncNamespace: string;
}

/** An Argo CD application's conditions and last operation (GET .../apps/{appNs}/{name}/status). */
export interface ArgoAppStatus {
  health: string;
  healthMessage: string;
  syncStatus: string;
  syncRevision: string;
  conditions: { type: string; message: string; at: string | null }[];
  lastOperation: {
    phase: string;
    message: string;
    startedAt: string;
    finishedAt: string | null;
    initiatedBy: string;
    automated: boolean;
    revision: string;
    resources: {
      group: string; kind: string; namespace: string; name: string;
      status: string; message: string; hookType: string; hookPhase: string; syncPhase: string;
    }[];
  } | null;
}

/** A deploy of an Argo CD application, newest first (GET .../history). */
export interface ArgoDeploy {
  id: number;
  revision: string;
  /** Multi-source apps have one per source and no `revision`. */
  revisions: string[];
  deployedAt: string;
  initiatedBy: string;
  automated: boolean;
  source: string;
  current: boolean;
  commit: { author: string; date: string | null; message: string; tags: string[] } | null;
}

/** What a sync would change (GET .../diff, needs sync on the app). */
export interface ArgoDiff {
  resources: {
    group: string; kind: string; namespace: string; name: string;
    /** changed: differs; missing: only in Git; extra: only in the cluster (never pruned by a sync). */
    state: 'changed' | 'missing' | 'extra';
    /** Unified diff of the YAML, live → desired. */
    diff: string;
    /** A Secret: its content is never shown. */
    hidden: boolean;
    truncated: boolean;
  }[];
  truncated: boolean;
}

/** Progress events of an Argo CD sync operation. */
export interface ArgoSyncProgress {
  phase: string;
  message: string;
  synced: number;
  failed: number;
  total: number;
}

export interface NamespaceSummary {
  cluster: string;
  namespace: string;
  environment: string;
  apps: string[];
  argoProjects: string[];
  access: Action[];
}

export interface WorkloadSummary {
  kind: WorkloadKind;
  name: string;
  namespace: string;
  cluster: string;
  environment: string;
  desired: number;
  ready: number;
  suspended?: boolean;
  schedule?: string;
  access: AccessMap;
}

export interface WorkloadDetail extends WorkloadSummary {
  argoApp: string | null;
  strategy: string;
  updated: number;
  available: number;
  images: string[];
  createdAt: string;
  lastRolloutAt: string | null;
  cronJobCount: number;
}

export interface PodContainer {
  name: string;
  image: string;
  ready: boolean;
  state: string;
  stateReason: string;
  restartCount: number;
  lastState: string | null;
  lastReason: string | null;
  lastExitCode: number | null;
  lastStartedAt: string | null;
  lastFinishedAt: string | null;
  memoryLimit: string | null;
  init: boolean;
}

export interface Pod {
  name: string;
  phase: string;
  ready: string;
  restarts: number;
  node: string;
  createdAt: string;
  lastTermination: string | null;
  lastTerminatedAt: string | null;
  containers: PodContainer[];
}

export interface K8sEvent {
  type: 'Normal' | 'Warning';
  reason: string;
  message: string;
  object: string;
  count: number;
  firstSeen: string;
  lastSeen: string;
}

/** How traffic from outside reaches a namespace's pods (GET .../traffic). */
export interface TrafficGraph {
  nodes: TrafficNode[];
  edges: TrafficEdge[];
}

export interface TrafficNode {
  /** `Kind/namespace/name` */
  id: string;
  /** Address, Gateway, Ingress, HTTPRoute, GRPCRoute, Service, a workload kind, ReplicaSet, Job or Pod */
  kind: string;
  /** Empty for an Address. */
  namespace: string;
  name: string;
  details: string[];
  ready: number;
  total: number;
  /** Why traffic may not get through; empty when nothing was found. */
  problem: string;
}

export interface TrafficEdge {
  from: string;
  to: string;
  labels: string[];
}

export interface JobSummary {
  name: string;
  cronJob: string;
  triggeredBy: string | null;
  manual: boolean;
  status: 'running' | 'succeeded' | 'failed';
  active: number;
  succeeded: number;
  completions: number;
  startedAt: string | null;
  finishedAt: string | null;
  podNames: string[];
}

export interface CronJobSummary extends WorkloadSummary {
  lastScheduleAt: string | null;
  lastResult: 'succeeded' | 'failed' | 'running' | null;
  activeJobs: number;
  concurrencyPolicy: string;
  recentJobs: JobSummary[];
}

export interface RestartProgress {
  desired: number;
  updated: number;
  ready: number;
  available: number;
}
export type OperationOutcome = 'succeeded' | 'failed' | 'timed_out';
export interface OperationDone {
  outcome: OperationOutcome;
  message: string;
}

export interface CatalogCluster {
  name: string;
  environment: string;
}
export interface CatalogWorkload {
  kind: WorkloadKind;
  name: string;
}

// ---- websockets
export type EndReason =
  | 'client_closed'
  | 'process_exited'
  | 'idle_timeout'
  | 'max_duration'
  | 'outside_window'
  | 'holiday'
  | 'grant_revoked'
  | 'grant_expired'
  | 'user_disabled'
  | 'role_changed'
  | 'terminated_by_admin'
  | 'break_glass_ended'
  | 'recording_failed'
  | 'cluster_error'
  | 'portal_restart'
  | 'denied';

export type ServerControl =
  | {
      type: 'ready';
      sessionId?: string;
      startedAt: string;
      closesAt: string | null;
      idleTimeoutSeconds?: number;
      maxDurationSeconds?: number;
      /** watch sockets only: size of the watched terminal */
      cols?: number;
      rows?: number;
      environment: string;
      breakGlassId: number | null;
    }
  | { type: 'warning'; code: 'closing_soon'; closesAt: string; reason: string; message: string }
  | { type: 'closes_at'; closesAt: string | null }
  | { type: 'truncated' }
  | { type: 'end'; reason: EndReason; message: string };

export type ClientControl = { type: 'resize'; cols: number; rows: number } | { type: 'ping' };

// ---- access, requests, break-glass
export interface ScopeView {
  kind: 'argo_project' | 'namespace' | 'workloads';
  argoProject?: string;
  cluster?: string;
  namespace?: string;
  workloads?: { kind: WorkloadKind; name: string }[];
}

export interface MyAccessGrant {
  id: number;
  holder: { kind: 'team' | 'user'; name: string };
  scope: ScopeView;
  actions: Action[];
  expiresAt: string | null;
  note: string;
  expired: boolean;
}

/** A grant as "Who has access" shows it to developers (no note, members listed). */
export interface AccessGrant {
  id: number;
  holder: { kind: 'team' | 'user'; id: number; name: string; members: string[] };
  scope: ScopeView;
  actions: Action[];
  expiresAt: string | null;
}

export interface MyAccess {
  grants: MyAccessGrant[];
  teams: { id: number; name: string }[];
  breakGlass: BreakGlass | null;
  requests: AccessRequest[];
}

export interface CreateAccessRequest {
  scope: ScopeView;
  actions: Action[];
  durationHours: number | null;
  reason: string;
}

export interface AccessRequest {
  id: number;
  userId: number;
  username: string;
  scope: ScopeView;
  actions: Action[];
  durationHours: number | null;
  reason: string;
  status: 'pending' | 'approved' | 'rejected' | 'expired';
  decidedBy: string | null;
  decidedAt: string | null;
  decisionNote: string | null;
  resultingGrantId: number | null;
  createdAt: string;
}

// ---- sessions, audit
export interface ExecSessionView {
  id: string;
  userId: number;
  username: string;
  cluster: string;
  namespace: string;
  workloadKind: WorkloadKind | null;
  workloadName: string | null;
  pod: string;
  container: string;
  shell: string;
  environment: string;
  /** Debug session: the ephemeral container it added; `container` is its target. */
  debugContainer: string | null;
  breakGlassId: number | null;
  startedAt: string;
  endedAt: string | null;
  durationSeconds: number | null;
  endReason: EndReason | null;
  recordingBytes: number;
  active: boolean;
}

export interface SessionCommand {
  at: string;
  offsetSeconds: number;
  text: string;
}
export type ExecSessionDetail = ExecSessionView & { commands: SessionCommand[] };

export interface AuditEvent {
  id: number;
  at: string;
  userId: number | null;
  username: string;
  eventType: string;
  action: Action | null;
  cluster: string | null;
  namespace: string | null;
  workloadKind: string | null;
  workloadName: string | null;
  pod: string | null;
  container: string | null;
  environment: string | null;
  result: 'allowed' | 'denied' | 'succeeded' | 'failed';
  reason: string | null;
  breakGlassId: number | null;
  sessionId: string | null;
  details: Record<string, unknown> | null;
  sourceIp: string | null;
  userAgent: string | null;
}

export interface AuditPage {
  items: AuditEvent[];
  nextCursor: string | null;
}

export interface AuditFilters {
  from?: string;
  to?: string;
  user?: string;
  eventType?: string;
  action?: string;
  result?: string;
  cluster?: string;
  namespace?: string;
  environment?: string;
  breakGlassId?: string;
  sessionId?: string;
  q?: string;
  limit?: number;
  cursor?: string;
}

export interface SessionFilters {
  user?: string;
  cluster?: string;
  namespace?: string;
  environment?: string;
  status?: 'active' | 'ended';
  breakGlass?: boolean;
  from?: string;
  to?: string;
  limit?: number;
  before?: string;
}

// ---- admin
export interface UserView {
  id: number;
  username: string;
  displayName: string;
  role: Role;
  disabled: boolean;
  keycloakBound: boolean;
  /** Logs in with a portal password, never through Keycloak. */
  local: boolean;
  lastLoginAt: string | null;
  teams: { id: number; name: string }[];
  activeSessions: number;
}
export interface TeamView {
  id: number;
  name: string;
  description: string;
  members: { id: number; username: string; displayName: string }[];
}
export interface GrantView {
  id: number;
  holder: { kind: 'team' | 'user'; id: number; name: string };
  scope: ScopeView;
  actions: Action[];
  expiresAt: string | null;
  note: string;
  expired: boolean;
  createdBy: string;
  createdAt: string;
  environment: string | null;
}
export interface GrantInput {
  holder: { kind: 'team' | 'user'; id: number };
  scope: ScopeView;
  actions: Action[];
  expiresAt: string | null;
  note: string;
}
export interface GrantFilters {
  holderKind?: 'team' | 'user';
  holderId?: number;
  scopeKind?: ScopeView['kind'];
  cluster?: string;
  argoProject?: string;
  action?: Action;
  expiring?: boolean;
  expired?: boolean;
}
export interface TimeWindowView {
  id: number;
  name: string;
  scopeKind: 'global' | 'environment' | 'cluster' | 'argo_project' | 'team';
  scopeValue: string | null;
  actions: Action[];
  daysOfWeek: number[];
  startTime: string;
  endTime: string;
  timezone: string;
  effect: 'allow_inside' | 'deny_inside';
  enabled: boolean;
}
export type TimeWindowInput = Omit<TimeWindowView, 'id'>;
export interface HolidayView {
  id: number;
  date: string;
  title: string;
  environments: string[];
}
export type HolidayInput = Omit<HolidayView, 'id'>;
export interface ClusterView {
  name: string;
  environment: string | null;
  reachable: boolean;
  kubernetesVersion: string | null;
  privilegeCheckOk: boolean;
  privilegeViolations: string[];
  admissionMode: 'vap' | 'kyverno' | 'none';
  lastCheckAt: string | null;
  usable: boolean;
  /** False when the cluster's annotated Secret is missing or was rejected. */
  credentialLoaded: boolean;
  /** The Secret's tiphys/environment annotation sets the environment; not editable here. */
  environmentFromSecret: boolean;
}
export interface EnvironmentOverride {
  id: number;
  kind: 'argo_project' | 'namespace';
  cluster?: string;
  argoProject?: string;
  namespace?: string;
  environment: string;
}
export type EnvironmentOverrideInput = Omit<EnvironmentOverride, 'id'>;
export type RocketEvent =
  | 'break_glass_started'
  | 'request_created'
  | 'request_decided'
  | 'exec_in_prod'
  | 'admin_changes';
export interface SettingsView {
  execMaxSessionMinutes: number;
  execIdleTimeoutMinutes: number;
  breakGlassDefaultMinutes: number;
  breakGlassMaxMinutes: number;
  /** Turns the whole break-glass feature on or off. Logs never need break-glass either way. */
  breakGlassEnabled: boolean;
  retentionAuditDays: number;
  retentionRecordingsDays: number;
  rocket: { webhookUrl: string; channel: string; events: Record<RocketEvent, boolean> };
  lokiRetentionInfo: string | null;
}
export interface DecisionExplain extends DecisionInfo {
  cluster: string;
  namespace: string;
  workload: string | null;
  action: Action;
  evaluatedAt: string;
  userRole: Role;
  teams: string[];
  matchedGrants: GrantView[];
}
export interface AccessCheckInput {
  userId: number;
  cluster: string;
  namespace: string;
  workload?: { kind: WorkloadKind; name: string };
  action: Action;
  at?: string;
}
export interface ArgoStatus {
  fetchedAt: string | null;
  ageSeconds: number | null;
  stale: boolean;
  appCount: number;
  projectCount: number;
  lastError: string | null;
}
export interface AdminOverview {
  activeExecSessions: number;
  openLogStreams: number;
  pendingRequests: number;
  activeBreakGlasses: number;
  clustersUnreachable: number;
}
