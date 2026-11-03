import { del, get, patch, post, put, request, requestText } from './client';
import { kindSegment, qs, seg } from './query';
import type {
  AccessCheckInput, AccessGrant, AccessRequest, Action, AdminOverview, AppSummary, ArgoAppStatus, ArgoDeploy, ArgoDiff, ArgoStatus, AuditFilters, AuditPage, BreakGlass, BreakGlassRequest,
  CatalogCluster, CatalogWorkload, ClusterView, CreateAccessRequest, CronJobSummary, DecisionExplain,
  EnvironmentOverride, EnvironmentOverrideInput, ExecSessionDetail, ExecSessionView, GrantFilters, GrantInput,
  GrantView, HolidayInput, HolidayView, K8sEvent, Me, MyAccess, NamespaceApp, NamespaceSummary, Pod, SessionFilters, TrafficGraph,
  SettingsView, TeamView, TimeWindowInput, TimeWindowView, UserView, WorkloadDetail, WorkloadKind, WorkloadSummary,
  Role,
} from './types';

export interface WorkloadRef {
  cluster: string;
  namespace: string;
  kind: WorkloadKind | string;
  name: string;
}

export function workloadPath(r: WorkloadRef): string {
  return `/clusters/${seg(r.cluster)}/namespaces/${seg(r.namespace)}/workloads/${kindSegment(r.kind)}/${seg(r.name)}`;
}

export interface LogOptions {
  container?: string;
  previous?: boolean;
  follow?: boolean;
  tailLines?: number;
  sinceSeconds?: number;
  timestamps?: boolean;
}

const appPath = (cluster: string, ns: string, appNamespace: string, name: string) =>
  `/clusters/${seg(cluster)}/namespaces/${seg(ns)}/apps/${seg(appNamespace)}/${seg(name)}`;

export const api = {
  // ---- auth
  me: () => request<Me>('GET', '/me', { noRedirect: false }),
  logout: () => request<{ redirect: string }>('POST', '/auth/logout', { raw: true, body: {} }),
  authOptions: () => request<{ localLogin: boolean }>('GET', '/auth/options', { raw: true, noRedirect: true }),
  localLogin: (b: { username: string; password: string; next: string }) =>
    request<{ redirect: string }>('POST', '/auth/local-login', { raw: true, noRedirect: true, body: b }),

  // ---- catalog / workloads
  apps: () => get<AppSummary[]>('/apps'),
  namespaces: () => get<NamespaceSummary[]>('/namespaces'),
  workloads: (cluster: string, ns: string) =>
    get<WorkloadSummary[]>(`/clusters/${seg(cluster)}/namespaces/${seg(ns)}/workloads`),
  workload: (r: WorkloadRef) => get<WorkloadDetail>(workloadPath(r)),
  pods: (r: WorkloadRef) => get<Pod[]>(`${workloadPath(r)}/pods`),
  events: (r: WorkloadRef) => get<K8sEvent[]>(`${workloadPath(r)}/events`),
  namespaceEvents: (cluster: string, ns: string) =>
    get<K8sEvent[]>(`/clusters/${seg(cluster)}/namespaces/${seg(ns)}/events`),
  namespaceTraffic: (cluster: string, ns: string) =>
    get<TrafficGraph>(`/clusters/${seg(cluster)}/namespaces/${seg(ns)}/traffic`),
  whoHasAccess: (t: { cluster: string; namespace: string; kind?: WorkloadKind; name?: string }) =>
    get<AccessGrant[]>(`/clusters/${seg(t.cluster)}/namespaces/${seg(t.namespace)}/access${qs({ kind: t.kind, name: t.name })}`),
  namespaceApps: (cluster: string, ns: string) =>
    get<NamespaceApp[]>(`/clusters/${seg(cluster)}/namespaces/${seg(ns)}/apps`),
  appStatus: (cluster: string, ns: string, appNamespace: string, name: string) =>
    get<ArgoAppStatus>(`${appPath(cluster, ns, appNamespace, name)}/status`),
  appHistory: (cluster: string, ns: string, appNamespace: string, name: string) =>
    get<ArgoDeploy[]>(`${appPath(cluster, ns, appNamespace, name)}/history`),
  appDiff: (cluster: string, ns: string, appNamespace: string, name: string) =>
    get<ArgoDiff>(`${appPath(cluster, ns, appNamespace, name)}/diff`),
  syncApp: (cluster: string, ns: string, appNamespace: string, name: string) =>
    post<{ operationId: string }>(`${appPath(cluster, ns, appNamespace, name)}/sync`),
  cronJobs: (cluster: string, ns: string) =>
    get<CronJobSummary[]>(`/clusters/${seg(cluster)}/namespaces/${seg(ns)}/cronjobs`),
  restart: (r: WorkloadRef) => post<{ operationId: string }>(`${workloadPath(r)}/restart`),
  runCronJob: (cluster: string, ns: string, name: string) =>
    post<{ operationId: string; jobName: string }>(
      `/clusters/${seg(cluster)}/namespaces/${seg(ns)}/cronjobs/${seg(name)}/run`,
    ),
  logDownloadUrl: (r: WorkloadRef, pod: string, o: LogOptions) =>
    `/api/v1${workloadPath(r)}/pods/${seg(pod)}/logs/download${qs({
      container: o.container,
      previous: o.previous || undefined,
      tailLines: o.tailLines,
      sinceSeconds: o.sinceSeconds,
      timestamps: o.timestamps || undefined,
    })}`,
  argoProjects: () => get<string[]>('/catalog/argo-projects'),
  catalogClusters: () => get<CatalogCluster[]>('/catalog/clusters'),
  catalogNamespaces: (c: string) => get<string[]>(`/catalog/clusters/${seg(c)}/namespaces`),
  catalogWorkloads: (c: string, ns: string) =>
    get<CatalogWorkload[]>(`/catalog/clusters/${seg(c)}/namespaces/${seg(ns)}/workloads`),

  // ---- access, requests, break-glass
  myAccess: () => get<MyAccess>('/my/access'),
  accessRequests: (status?: string) => get<AccessRequest[]>(`/access-requests${qs({ status })}`),
  createAccessRequest: (b: CreateAccessRequest) => post<AccessRequest>('/access-requests', b),
  approveRequest: (id: number, b: { note?: string; durationHours?: number | null; actions?: Action[]; separate?: boolean }) =>
    post<AccessRequest>(`/access-requests/${id}/approve`, b),
  rejectRequest: (id: number, b: { note?: string }) => post<AccessRequest>(`/access-requests/${id}/reject`, b),
  startBreakGlass: (b: { reason: string; durationMinutes?: number }) => post<BreakGlass>('/break-glass', b),
  endBreakGlass: (id: number) => post<BreakGlass>(`/break-glass/${id}/end`),
  breakGlasses: () => get<BreakGlass[]>('/break-glass'),
  requestBreakGlass: (b: { reason: string; durationMinutes?: number }) => post<BreakGlassRequest>('/break-glass/requests', b),
  breakGlassRequests: (status?: string) => get<BreakGlassRequest[]>(`/break-glass/requests${qs({ status })}`),
  approveBreakGlassRequest: (id: number, b: { note?: string; durationMinutes?: number }) =>
    post<BreakGlassRequest>(`/break-glass/requests/${id}/approve`, b),
  rejectBreakGlassRequest: (id: number, b: { note?: string }) => post<BreakGlassRequest>(`/break-glass/requests/${id}/reject`, b),
  cancelBreakGlassRequest: (id: number) => post<BreakGlassRequest>(`/break-glass/requests/${id}/cancel`),

  // ---- sessions, audit
  sessions: (f: SessionFilters = {}) => get<ExecSessionView[]>(`/sessions${qs(f)}`),
  session: (id: string) => get<ExecSessionDetail>(`/sessions/${seg(id)}`),
  recording: (id: string, signal?: AbortSignal) => requestText(`/sessions/${seg(id)}/recording`, signal),
  terminateSession: (id: string) => post<void>(`/sessions/${seg(id)}/terminate`),
  audit: (f: AuditFilters = {}) => get<AuditPage>(`/audit${qs(f)}`),
  auditCsvUrl: (f: AuditFilters = {}) => `/api/v1/audit/export.csv${qs({ ...f, limit: undefined, cursor: undefined })}`,

  // ---- admin
  users: () => get<UserView[]>('/admin/users'),
  createUser: (b: { username: string; displayName?: string; role: Role; password?: string }) => post<UserView>('/admin/users', b),
  setUserPassword: (id: number, password: string) => put<UserView>(`/admin/users/${id}/password`, { password }),
  patchUser: (id: number, b: { role?: Role; disabled?: boolean; displayName?: string }) =>
    patch<UserView>(`/admin/users/${id}`, b),
  teams: () => get<TeamView[]>('/admin/teams'),
  createTeam: (b: { name: string; description: string }) => post<TeamView>('/admin/teams', b),
  patchTeam: (id: number, b: { name?: string; description?: string }) => patch<TeamView>(`/admin/teams/${id}`, b),
  deleteTeam: (id: number) => del<void>(`/admin/teams/${id}`),
  setTeamMembers: (id: number, userIds: number[]) => put<TeamView>(`/admin/teams/${id}/members`, { userIds }),
  grants: (f: GrantFilters = {}) => get<GrantView[]>(`/admin/grants${qs(f)}`),
  grantsCovering: (t: { cluster: string; namespace: string; kind?: WorkloadKind; name?: string }) =>
    get<GrantView[]>(`/admin/grants/covering${qs(t)}`),
  createGrant: (b: GrantInput) => post<GrantView>('/admin/grants', b),
  updateGrant: (id: number, b: GrantInput) => put<GrantView>(`/admin/grants/${id}`, b),
  deleteGrant: (id: number) => del<void>(`/admin/grants/${id}`),
  accessCheck: (b: AccessCheckInput) => post<DecisionExplain>('/admin/access-check', b),
  timeWindows: () => get<TimeWindowView[]>('/admin/time-windows'),
  createTimeWindow: (b: TimeWindowInput) => post<TimeWindowView>('/admin/time-windows', b),
  updateTimeWindow: (id: number, b: TimeWindowInput) => put<TimeWindowView>(`/admin/time-windows/${id}`, b),
  deleteTimeWindow: (id: number) => del<void>(`/admin/time-windows/${id}`),
  holidays: () => get<HolidayView[]>('/admin/holidays'),
  createHoliday: (b: HolidayInput) => post<HolidayView>('/admin/holidays', b),
  updateHoliday: (id: number, b: HolidayInput) => put<HolidayView>(`/admin/holidays/${id}`, b),
  deleteHoliday: (id: number) => del<void>(`/admin/holidays/${id}`),
  clusters: () => get<ClusterView[]>('/admin/clusters'),
  addCluster: (b: { name: string; environment?: string; kubeconfig: string }) =>
    post<ClusterView>('/admin/clusters', b),
  setClusterEnvironment: (name: string, environment: string) =>
    put<ClusterView>(`/admin/clusters/${seg(name)}`, { environment }),
  replaceCredential: (name: string, kubeconfig: string) =>
    post<ClusterView>(`/admin/clusters/${seg(name)}/credential`, { kubeconfig }),
  checkCluster: (name: string) => post<ClusterView>(`/admin/clusters/${seg(name)}/check`),
  deleteCluster: (name: string) => del<void>(`/admin/clusters/${seg(name)}`),
  environmentOverrides: () => get<EnvironmentOverride[]>('/admin/environment-overrides'),
  createEnvironmentOverride: (b: EnvironmentOverrideInput) =>
    post<EnvironmentOverride>('/admin/environment-overrides', b),
  deleteEnvironmentOverride: (id: number) => del<void>(`/admin/environment-overrides/${id}`),
  protectedNamespaces: () => get<string[]>('/admin/protected-namespaces'),
  addProtectedNamespace: (namespace: string) => post<string[]>('/admin/protected-namespaces', { namespace }),
  removeProtectedNamespace: (ns: string) => del<void>(`/admin/protected-namespaces/${seg(ns)}`),
  argo: () => get<ArgoStatus>('/admin/argo'),
  refreshArgo: () => post<ArgoStatus>('/admin/argo/refresh'),
  settings: () => get<SettingsView>('/admin/settings'),
  saveSettings: (b: SettingsView) => put<SettingsView>('/admin/settings', b),
  testRocket: () => post<void>('/admin/settings/rocket/test'),
  overview: () => get<AdminOverview>('/admin/overview'),
};
