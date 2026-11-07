# Tiphys HTTP API (v1)

Contract between `web/` and the Go portal. The frontend is served by the portal itself (same origin), so there is no CORS. Behavior is defined by `spec.md`; this file fixes paths and JSON shapes.

## Conventions

- Base path `/api/v1`, JSON, `camelCase`. Timestamps are RFC 3339 UTC strings (`"2026-10-20T10:12:00Z"`). Durations are integer seconds unless named `...Minutes`.
- Auth: HttpOnly session cookie `tiphys_session` (set by `/auth/callback`). The browser never sees Keycloak tokens.
- CSRF: every non-GET request (and WebSocket upgrade) must send `X-CSRF-Token: <csrfToken from GET /me>`. Rate limit: 30 non-stream actions per user per minute → `429`.
- Errors: HTTP status + body `{"error": {"code": string, "message": string, "details"?: object}}`.
  - `401 unauthenticated` → frontend redirects to `/auth/login?next=<path>`, or to the login page `/login?next=<path>` when `GET /auth/options` says `localLogin`.
  - `403 denied` → authorization decision; `details` is a `DecisionInfo` (below), so the UI can show the reason, `reopensAt` and `closesAt`.
  - `403 forbidden_role` → role does not allow the endpoint. `403 break_glass_disabled` → break-glass is turned off in the settings. `409 break_glass_approval_required` → `POST /break-glass` in mode `approval`: request it instead. `409 auto_sync_enabled` → a rollback of an app that syncs automatically (Argo CD refuses it). `404 not_found`. `409 conflict` (`409 last_admin` when a change would leave no active admin). `422 invalid` (`details.fields?: Record<string,string>`). `429 rate_limited`. `502 cluster_error` (cluster unreachable/forbidden). `502 argo_error` (Argo CD unreachable or refused a sync). `503 recording_unavailable` (exec refused because MinIO is down).
- Health: `GET /healthz`, `GET /readyz` (no auth). Metrics are on a separate port.
- Lists are returned as plain arrays unless stated (audit uses a cursor).

```ts
type Action = 'logs' | 'exec' | 'restart' | 'run-job' | 'sync' | 'rollback'; // sync, rollback: a whole Argo CD application; workload access maps hold the other four
type Role = 'user' | 'auditor' | 'admin';
type WorkloadKind = 'Deployment' | 'StatefulSet' | 'DaemonSet' | 'Rollout' | 'CronJob';
type DenyReason =
  | 'protected_namespace' | 'user_disabled' | 'role' | 'no_grant' | 'grant_expired'
  | 'outside_window' | 'holiday' | 'argo_stale'
  | 'app_scope'; // sync or rollback of an app with cluster-scoped resources (other than its own Namespace objects and kinds in settings.argoSyncClusterKinds): admins only

interface DecisionInfo {
  allowed: boolean;
  reason: DenyReason | 'allowed' | 'admin';
  environment: string;
  grantIds: number[];
  breakGlassId: number | null;
  reopensAt: string | null;   // set when denied by time
  closesAt: string | null;    // earliest of window close, holiday start, grant expiry, break-glass end
}
// What the user may do with a workload, per action.
type AccessMap = Record<Action, DecisionInfo>;
```

## Auth

| Method | Path | Notes |
| --- | --- | --- |
| GET | `/auth/login?next=/path` | Redirects to Keycloak (code flow + PKCE). `next` must be a same-origin path. |
| GET | `/auth/callback` | Keycloak redirect target; sets the session cookie; redirects to `next` (default `/`). |
| GET | `/auth/options` | No login. `{ localLogin: boolean }`: whether the login page shows the local password form (`LOCAL_USERS_ENABLED`). |
| POST | `/auth/local-login` | Only with `LOCAL_USERS_ENABLED` (404 otherwise). No CSRF token; `Origin` must be the portal's (`403 bad_origin`). `{ username, password, next? }` → sets the session cookie, `200 {"redirect": "<next or />"}`. `401 invalid_credentials` for an unknown user, a Keycloak user or a wrong password (indistinguishable); `403 user_disabled` only after the password matched; `429 rate_limited` above 20 attempts per IP or 5 per username per minute. The session lasts at most 8 h. |
| POST | `/auth/logout` | CSRF. Ends the session; responds `200 {"redirect": "<Keycloak logout URL, or />"}` (`/login` for a local user) and the frontend navigates there. |
| GET | `/api/v1/me` | Current user. 401 if not logged in. |

```ts
interface Me {
  id: number;
  username: string;
  displayName: string;
  role: Role;
  teams: { id: number; name: string }[];
  csrfToken: string;
  breakGlass: BreakGlass | null;     // active one, if any
  breakGlassRequest: BreakGlassRequest | null; // own pending request (mode approval), if any; the UI polls /me while it is set
  settings: { breakGlassMode: 'off' | 'self' | 'approval'; breakGlassDefaultMinutes: number; breakGlassMaxMinutes: number; execIdleTimeoutMinutes: number; execMaxSessionMinutes: number; clusterRegistration: boolean; debugEnabled: boolean; argoUrl: string /* Argo CD in the browser (ARGO_PUBLIC_URL), for app links */; localUsers: boolean /* LOCAL_USERS_ENABLED */ };
  argo: { fetchedAt: string | null; ageSeconds: number | null; stale: boolean };  // stale => project-scoped grants do not match
}

interface BreakGlass {
  id: number; userId: number; username: string; reason: string;
  startedAt: string; expiresAt: string; endedAt: string | null; endedBy: string | null;
  active: boolean;
}
```

## Catalog and workloads (roles `user` and `auditor` per their grants; admins see everything non-protected)

Path segment `{kind}` is lowercase plural: `deployments | statefulsets | daemonsets | rollouts | cronjobs`. `{c}` is the cluster name, `{ns}` the namespace.

| Method | Path | Returns |
| --- | --- | --- |
| GET | `/apps` | `AppSummary[]` — Argo applications on the usable clusters whose namespace the user holds a grant for. |
| GET | `/namespaces` | `NamespaceSummary[]` — for "By namespace" grouping: namespaces read from the usable clusters and those the user's grants name on registered clusters, limited to those the user holds a grant for (admins: all non-protected). `apps`/`argoProjects` annotate them; Argo never adds a namespace. |
| GET | `/clusters/{c}/namespaces/{ns}/workloads` | `WorkloadSummary[]` the user can use (live from Kubernetes). |
| GET | `/clusters/{c}/namespaces/{ns}/workloads/{kind}/{name}` | `WorkloadDetail` |
| GET | `/clusters/{c}/namespaces/{ns}/workloads/{kind}/{name}/pods` | `Pod[]` (needs `logs`) |
| GET | `/clusters/{c}/namespaces/{ns}/workloads/{kind}/{name}/events` | `K8sEvent[]` (needs `logs`) |
| GET | `/clusters/{c}/namespaces/{ns}/events` | `K8sEvent[]`: the newest 200 events of every object in the namespace, newest first. Needs `logs` on the whole namespace (a namespace or Argo project grant; a grant on single workloads gives 403, use the workload's `/events`). |
| GET | `/clusters/{c}/namespaces/{ns}/traffic` | `TrafficGraph`: how traffic from outside reaches the namespace's pods, like Argo CD's network view. Needs `logs` on the whole namespace (same rule as `/events`). `interface TrafficGraph { nodes: TrafficNode[]; edges: TrafficEdge[] }`, `interface TrafficNode { id: string; kind: string; namespace: string; name: string; details: string[]; ready: number; total: number; problem: string }` (`id` is `Kind/namespace/name`; `kind` is `Address` (no namespace), `Gateway`, `Waypoint` (an Istio ambient waypoint), `Ingress`, `HTTPRoute`, `GRPCRoute`, `Service`, a workload kind, `ReplicaSet`, `Job` or `Pod`; `ready`/`total` count running pods behind a Service or workload; `problem` is empty unless traffic may not get through; every Service of the namespace is a node; what no `Address`, `Gateway`, `Ingress` or Service whose details say `LoadBalancer` or `NodePort` leads to is reachable only inside the cluster), `interface TrafficEdge { from: string; to: string; labels: string[] }` (labels: host and path, route matches, listener). |
| GET | `/clusters/{c}/namespaces/{ns}/access?kind=&name=` | `AccessGrant[]` ("Who has access" for developers): unexpired grants reaching the namespace, or the workload when `kind` (e.g. `Deployment`) and `name` are given. Same check as listing the namespace / opening the workload; 403 otherwise. `interface AccessGrant { id: number; holder: { kind: 'team' \| 'user'; id: number; name: string; members: string[] }; scope: ScopeView; actions: Action[]; expiresAt: string \| null }` (no note or author; `members` are a team's enabled members). Admins and auditors use `/admin/grants/covering`. |
| GET | `/clusters/{c}/namespaces/{ns}/apps` | `NamespaceApp[]` — Argo CD applications deploying to the namespace (destination or any resource there), from the Argo cache (≤60 s old). Same check as listing the namespace; 403 otherwise. |
| GET | `/clusters/{c}/namespaces/{ns}/cronjobs` | `CronJobSummary[]` incl. recent jobs (user needs `run-job` or `logs` on at least one CronJob; list is filtered to allowed ones) |
| POST | `/clusters/{c}/namespaces/{ns}/workloads/{kind}/{name}/restart` | 202 `{ operationId }` (needs `restart`; not for CronJob) |
| POST | `/clusters/{c}/namespaces/{ns}/cronjobs/{name}/run` | 202 `{ operationId, jobName }` (needs `run-job`); 409 `already_running` when `concurrencyPolicy: Forbid` and a job is active |
| POST | `/clusters/{c}/namespaces/{ns}/apps/{appNamespace}/{name}/sync` | 202 `{ operationId }`. Syncs an Argo CD application that deploys to the namespace (404 otherwise) to its target revision, always without prune, dry run or a chosen revision; the user is named in the operation's info. Needs `sync` on **every** namespace the app deploys to (destination and resources) and a fresh Argo cache; an app with other cluster-scoped resources needs an admin (`app_scope`), unless their kinds are in `settings.argoSyncClusterKinds`; the `argo.sync` details then name them (`clusterResources`). 403 names the denying namespace in the audit event. 409 `already_running` when Argo CD has another operation running; 502 `argo_error` when Argo CD fails. Audited as `argo.sync` (started, then the outcome). |
| GET | `/clusters/{c}/namespaces/{ns}/apps/{appNamespace}/{name}/status` | `ArgoAppStatus`: health and sync (with messages), `status.conditions` and the last operation with its per-resource results, read from Argo CD on each call. Same check as listing the namespace's apps (checked before the app is looked up, so 404 reveals nothing); 502 `argo_error`. |
| GET | `/clusters/{c}/namespaces/{ns}/apps/{appNamespace}/{name}/history` | `ArgoDeploy[]`: the last 10 deploys of `status.history`, newest first, each with its commit (author, message) when Argo CD can describe the revision (Git, single source), `portalUser`/`portalAction` (`argo.sync` or `argo.rollback`) when the audit shows it was started through the portal (matched by the operation's `startedAt`; Argo CD's `initiatedBy` is then the token's account), and `rollbackBlocked`: `current`, `auto_sync`, `multi_source`, `source_changed` (the deploy's source differs from the app's current one but for `targetRevision`) or null. Same check as `status`. |
| GET | `/clusters/{c}/namespaces/{ns}/apps/{appNamespace}/{name}/history/{id}/rollback` | `ArgoRollbackPlan`: what rolling back to deploy `id` would do. Reads the manifests of the app's current source at the deploy's revision from Argo CD (`applications, get`) and places them with the cluster's discovery (as `tiphys:user`): a namespaced object without a namespace goes to the destination, a kind the cluster does not serve counts as cluster-scoped. Needs `rollback` on the app as it is now (a denial is audited); the plan's own decision is not audited. 409 `auto_sync_enabled`; 422 for a deploy not in the history, the current one, a multi-source app or a changed source; 502 `argo_error` / `cluster_error`. |
| POST | `/clusters/{c}/namespaces/{ns}/apps/{appNamespace}/{name}/history/{id}/rollback` | 202 `{ operationId }`. Rolls the app back to deploy `id`, like Rollback in Argo CD, always without prune or dry run. Allowed only when the plan's decision is (`rollback` on every namespace of the app and of the revision, a fresh Argo cache, `app_scope` for cluster-scoped resources of either). Argo CD's rollback request carries no info, so the user is named only in the audit: `argo.rollback` (started, with `historyId`, `revision`, `namespaces`, `clusterResources`; then the outcome with `operationStartedAt`). Same errors as the plan, plus 409 `already_running`. Progress on `/operations/{id}/stream` like a sync. The app is then OutOfSync with Git until the next sync. |
| GET | `/clusters/{c}/namespaces/{ns}/apps/{appNamespace}/{name}/diff` | `ArgoDiff`: what a sync would change, from Argo CD's managed resources (normalized live vs predicted state): `changed`, `missing` (only in Git) or `extra` (only in the cluster; never pruned), each with a unified YAML diff without `status`, managed fields and server-set metadata; hooks left out; Secrets listed with `hidden: true` and no content. At most 200 resources and 2,000 lines per diff. It shows manifests, so it needs what a sync needs (`sync` on every namespace of the app) and is audited as `argo.diff` before Argo CD is asked. |
| GET | `/operations/{id}/stream` | Server-Sent Events for restart / run / sync progress (see below). Only the operation's owner or an admin. |
| GET | `/live?ns={cluster}/{namespace}&ns=...` | Server-Sent Events: what changes in 1–10 namespaces (see below). `422` for a bad or missing `ns`, `429 rate_limited` over 8 open streams per user. |
| GET | `/clusters/{c}/namespaces/{ns}/workloads/{kind}/{name}/pods/{pod}/logs/download?container=&previous=&tailLines=&sinceSeconds=&timestamps=` | `text/plain` attachment, capped at 50 MB. Header `X-Tiphys-Truncated: true` when capped. Needs `logs`. |
| GET | `/catalog/argo-projects` | `string[]` (any logged-in user, for pickers and the request form). |
| GET | `/catalog/clusters` | `{ name, environment }[]`. Protected clusters (`tiphys/protected`) are listed for admins only. |
| GET | `/catalog/clusters/{c}/namespaces` | `string[]` — namespaces read from the cluster itself, protected ones removed; empty when the cluster cannot be read, and for non-admins on a protected cluster. Cached for 30 s. |
| GET | `/catalog/clusters/{c}/namespaces/{ns}/workloads` | `{ kind, name }[]` (live; protected namespaces refused; `404` for non-admins on a protected cluster). |

```ts
interface AppSummary {
  name: string;               // Argo application
  project: string;
  cluster: string;
  namespace: string;          // destination namespace
  environment: string;
  health: 'Healthy' | 'Progressing' | 'Degraded' | 'Suspended' | 'Missing' | 'Unknown';
  sync: 'Synced' | 'OutOfSync' | 'Unknown';
  workloadCount: number | null;   // null until loaded
  access: Action[];               // actions the user may currently do on this app's namespace (union)
}

interface NamespaceApp {
  name: string; project: string;
  appNamespace: string;               // namespace of the Application object (Argo link: {argoUrl}/applications/{appNamespace}/{name})
  health: string; sync: string;       // 'Unknown' when Argo reports none
  lastSyncAt: string | null;          // status.operationState.finishedAt; null if never synced or a sync is running
  lastSyncPhase: 'Running' | 'Succeeded' | 'Failed' | 'Error' | 'Terminating' | null;
  access: { sync: DecisionInfo; rollback: DecisionInfo }; // the whole app: allowed only if allowed on every namespace it deploys to; a rollback to a deploy is decided again with that deploy's resources (ArgoRollbackPlan)
  syncNamespace: string;              // the namespace the sync decision is about (the one that denied, else the destination)
  adminOnlyResources: string[];       // cluster-scoped resources ("Kind.group/name", core kinds "Kind/name"; other than its own Namespaces and allowed kinds) that leave the sync to admins; empty unless access.sync.reason is app_scope
  autoSync: boolean;                  // spec.syncPolicy.automated: Argo CD refuses a rollback
  multiSource: boolean;               // spec.sources: rolled back in Argo CD only
}

interface ArgoResourceRef { group: string; kind: string; namespace: string; name: string } // namespace '' when cluster-scoped

interface ArgoRollbackPlan {
  id: number; revision: string;       // the deploy
  decision: DecisionInfo;             // rollback on what the app manages now plus what the revision deploys (a rollback never prunes)
  decisionNamespace: string;          // the namespace the decision is about (the one that denied, else the destination)
  adminOnlyResources: string[];       // like NamespaceApp.adminOnlyResources, for both; empty unless decision.reason is app_scope
  added: ArgoResourceRef[];           // in the revision, not managed now (hooks included)
  kept: ArgoResourceRef[];            // managed now, not in the revision: they stay
  truncated: boolean;                 // more than 200 in either list
}

interface NamespaceSummary {
  cluster: string; namespace: string; environment: string;
  apps: string[]; argoProjects: string[];
  access: Action[];
}

interface WorkloadSummary {
  kind: WorkloadKind; name: string; namespace: string; cluster: string; environment: string;
  desired: number; ready: number;       // for CronJob both 0
  suspended?: boolean; schedule?: string;
  access: AccessMap;
}

interface WorkloadDetail extends WorkloadSummary {
  argoApp: string | null;
  strategy: string; updated: number; available: number;
  images: string[]; createdAt: string; lastRolloutAt: string | null;
  cronJobCount: number;
}

interface Pod {
  name: string; phase: string; ready: string /* "2/2" */; restarts: number; node: string; createdAt: string;
  lastTermination: string | null; lastTerminatedAt: string | null;
  containers: {
    name: string; image: string; ready: boolean; state: string; stateReason: string; restartCount: number;
    lastState: string | null; lastReason: string | null; lastExitCode: number | null;
    lastStartedAt: string | null; lastFinishedAt: string | null; memoryLimit: string | null; init: boolean;
  }[];
}

interface K8sEvent {
  type: 'Normal' | 'Warning'; reason: string; message: string; object: string;
  count: number; firstSeen: string; lastSeen: string;
}

interface CronJobSummary extends WorkloadSummary {
  lastScheduleAt: string | null; lastResult: 'succeeded' | 'failed' | 'running' | null;
  activeJobs: number; concurrencyPolicy: string;
  recentJobs: JobSummary[];
}
interface JobSummary {
  name: string; cronJob: string; triggeredBy: string | null; manual: boolean;
  status: 'running' | 'succeeded' | 'failed'; active: number; succeeded: number; completions: number;
  startedAt: string | null; finishedAt: string | null; podNames: string[];
}
```

### Operation stream (SSE)

`GET /operations/{id}/stream` → `text/event-stream`, events with JSON `data`:

- `event: progress` — restart: `{ desired, updated, ready, available }`; run-job: a `JobSummary`; sync and rollback: `{ phase, message, synced, failed, total }` (`phase` is `Pending` until Argo CD starts the operation, then its `operationState.phase`; counts are the sync result's resources).
- `event: done` — `{ outcome: 'succeeded' | 'failed' | 'timed_out', message: string }`. The server then closes the stream.

### Live changes (SSE)

`GET /live?ns=<cluster>/<namespace>` (repeated, 1–10) → `text/event-stream`. One stream per browser tab follows the namespaces the open page shows; the pages refetch their data when something changes, and notable changes become toasts. The server watches pods and events in each namespace as `tiphys:user` and attributes them to their workload through the owner chain; anything that belongs to no workload is dropped. Events from before the stream are not sent.

- `retry: 3000`, then `event: ready` — `{ namespaces: { cluster, namespace, error? }[] }`. `error` (`denied` | `invalid` | `not_found` | `cluster_error`) means that namespace is not followed (it needs a grant there, like `.../workloads`; a denial is audited). After a reconnect the client should refetch, since changes may have been missed.
- `event: change` — a `LiveChange`, decided on its own against the current policy: with `logs` on the workload everything is sent; with only other actions on it, `workload` and the rollout changes (the counts the workload list shows) are sent and every other change becomes `{ type: 'workload' }` without details; without a grant on the workload nothing.
- `event: end` — `{ reason: EndReason }` before the server closes the stream. It does so after 10 minutes (`max_duration`; the browser reconnects, which checks the login again), when the login session ends or the user is disabled.
- A `: ping` comment every 25 s keeps proxies from closing the stream.

```ts
interface LiveChange {
  cluster: string; namespace: string;
  kind: WorkloadKind; name: string;   // the workload it belongs to
  type: 'pod_added' | 'pod_removed' | 'pod_updated'   // phase, readiness, terminating
      | 'pod_restarted'                               // a container restarted
      | 'workload'                                    // something changed; status when the counts were read
      | 'rollout_started' | 'rollout_complete' | 'rollout_failed'
      | 'event';                                      // a warning, or a CronJob's Job starting (SuccessfulCreate) or finishing
  pod?: string; phase?: string;                       // pod changes
  container?: string; reason?: string; exitCode?: number;  // pod_restarted: how the container last ended
  status?: { desired: number; updated: number; ready: number; available: number; complete: boolean; failed: boolean; message: string };
  event?: K8sEvent;                                   // event; a repeating event is sent at most every 30 s
}
```

A rollout is a new revision of the workload (Deployment revision, StatefulSet update revision, DaemonSet generation, Rollout pod hash or restart), seen when its pods change; it is followed until it completes or fails (at most 15 minutes). Scaling is a `workload` change.

## WebSockets (logs, exec, watch)

Authenticated by the session cookie; the server checks `Origin` equals the portal's origin and the `csrf` query parameter equals the CSRF token (browsers cannot set headers on WebSockets): `?csrf=<csrfToken>`. Paths:

- Logs: `GET /api/v1/clusters/{c}/namespaces/{ns}/workloads/{kind}/{name}/pods/{pod}/logs/stream?container=&previous=&follow=true&tailLines=100&sinceSeconds=&timestamps=`
- Exec: `GET /api/v1/clusters/{c}/namespaces/{ns}/workloads/{kind}/{name}/pods/{pod}/exec?container=&shell=/bin/sh&cols=80&rows=24`
- Debug: `GET /api/v1/clusters/{c}/namespaces/{ns}/workloads/{kind}/{name}/pods/{pod}/debug?container=&shell=/bin/sh&cols=80&rows=24` — exec in a new debug container (`DEBUG_IMAGE`) that shares `container`'s processes and the pod's network. Needs the `exec` action and is an exec session in every other way (recorded, re-evaluated, same limits). Registered only when `me.settings.debugEnabled`. The upgrade waits until the container runs (up to 45 s).
- Live watch (admin/auditor, read-only): `GET /api/v1/sessions/{id}/watch`

Framing: **binary frames carry raw bytes** (server→client: log chunk / terminal output; client→server on exec: stdin). **Text frames carry JSON control messages.**

Server → client control messages:

```ts
type ServerControl =
  | { type: 'ready'; sessionId?: string; startedAt: string; closesAt: string | null; idleTimeoutSeconds?: number; maxDurationSeconds?: number; environment: string; breakGlassId: number | null }
  | { type: 'warning'; code: 'closing_soon'; closesAt: string; reason: string /* outside_window | holiday | grant_expired | break_glass_ended | max_duration */; message: string }
  | { type: 'closes_at'; closesAt: string | null }       // re-evaluation moved the deadline
  | { type: 'truncated' }                                 // log tail dropped data
  | { type: 'end'; reason: EndReason; message: string };  // followed by a normal close
type EndReason =
  | 'client_closed' | 'process_exited' | 'idle_timeout' | 'max_duration' | 'outside_window' | 'holiday'
  | 'grant_revoked' | 'grant_expired' | 'user_disabled' | 'role_changed' | 'terminated_by_admin'
  | 'break_glass_ended' | 'recording_failed' | 'cluster_error' | 'portal_restart' | 'denied';
```

Client → server control messages (exec only): `{ type: 'resize', cols: number, rows: number }`, `{ type: 'ping' }`. Origin, CSRF and login failures are rejected **before the upgrade** as normal HTTP errors. Everything else that stops a stream from opening (denied by `Decide`, protected namespace, recording storage unavailable, pod not in workload, cluster error) is returned **after** the upgrade, because browsers cannot read the status or body of a rejected WebSocket handshake: the server sends one `end` message and closes with code 1008. The `end` message then carries `reason` (`denied`, `recording_failed` or `cluster_error`), `code` (`denied`, `recording_unavailable`, `pod_not_in_workload`, `invalid`, `forbidden_role`, `not_found`, `cluster_error`; for debug also `debug_unavailable` — the cluster lacks the chart's debug policy — and `debug_failed` — the cluster refused or could not start the container, `message` says why), `message`, and for `denied` a `decision: DecisionInfo` with `reopensAt` / `closesAt`. Nothing was opened on the cluster and the refusal is audited.

## Access, requests, break-glass

| Method | Path | Notes |
| --- | --- | --- |
| GET | `/my/access` | `MyAccess` (own grants and own requests; `user` and `auditor`) |
| GET | `/access-requests` | Own requests for `user`; all (optionally `?status=pending`) for `admin`/`auditor`. |
| POST | `/access-requests` | `user` or `auditor`. `CreateAccessRequest` → `AccessRequest`. `422` for a protected namespace or a protected cluster. |
| POST | `/access-requests/{id}/approve` | admin. Body `{ note?: string, durationHours?: number \| null, actions?: Action[], separate?: boolean }` (override of the requested duration; `actions` replaces the requested ones, fewer or more, as long as they fit the scope — e.g. no `sync` on a `workloads` scope; the `access_request.decide` audit details carry `requestedActions` and `actions`). In the same transaction it adds the actions to the requester's own unexpired grant on exactly the same scope (workloads as a set) that ends no later than the approved access — the longest of them; that grant then takes the new expiry — or, when there is none or `separate` is true, creates a grant. `resultingGrantId` is that grant; the audit details carry `merged` and, when merged, the grant's `before`/`after` actions and expiry. Team grants are never changed. Returns `AccessRequest`. |
| POST | `/access-requests/{id}/reject` | admin. Body `{ note?: string }` |
| POST | `/break-glass` | role `user` or `auditor`. Body `{ reason: string /* ≥ 20 chars */, durationMinutes?: number }` → `BreakGlass` |
| POST | `/break-glass/{id}/end` | owner or admin. |
| GET | `/break-glass` | admin/auditor: recent ones (`BreakGlass[]`, newest first). |
| POST | `/break-glass/requests` | role `user` or `auditor`, mode `approval` (`403 break_glass_disabled` when off, `409` in mode `self`). Body as `POST /break-glass` → `201 BreakGlassRequest`. `409` with a pending request or an active break-glass. |
| GET | `/break-glass/requests?status=` | admin/auditor: up to 200, newest first. A pending request past its expiry reads as `expired`. |
| POST | `/break-glass/requests/{id}/approve` | admin. Body `{ note?: string, durationMinutes?: number /* omitted or 0: as requested; 1..max */ }`. Starts the break-glass now; `409` if no longer pending or the requester already has one active, `403 break_glass_disabled` when off. → `BreakGlassRequest` |
| POST | `/break-glass/requests/{id}/reject` | admin. Body `{ note?: string }` → `BreakGlassRequest` |
| POST | `/break-glass/requests/{id}/cancel` | the requester only → `BreakGlassRequest` |

```ts
interface BreakGlassRequest {
  id: number; userId: number; username: string; reason: string;
  durationMinutes: number;             // requested
  status: 'pending' | 'approved' | 'rejected' | 'cancelled' | 'expired';
  decidedBy: string | null;            // 'system' when expired
  decidedAt: string | null; decisionNote: string | null;
  approvedMinutes: number | null;      // approved: the duration the admin approved
  breakGlassId: number | null;         // approved: the break-glass it started
  createdAt: string;
  expiresAt: string;                   // when it expires if still pending (createdAt + the setting)
}

interface MyAccess {
  grants: {
    id: number; holder: { kind: 'team' | 'user'; name: string };
    scope: ScopeView; actions: Action[]; expiresAt: string | null; note: string; expired: boolean;
  }[];
  teams: { id: number; name: string }[];
  breakGlass: BreakGlass | null;
  requests: AccessRequest[];
}

interface ScopeView {
  kind: 'argo_project' | 'namespace' | 'workloads' | 'cluster' | 'environment';
  argoProject?: string; cluster?: string; namespace?: string;
  workloads?: { kind: WorkloadKind; name: string }[];
  environment?: string;            // environment scope
}
// cluster (cluster only) and environment (environment only) are admin grants; a request with them is 422.

interface CreateAccessRequest {
  scope: ScopeView; actions: Action[];
  durationHours: number | null;   // null = permanent
  reason: string;
}
interface AccessRequest {
  id: number; userId: number; username: string; scope: ScopeView; actions: Action[];
  durationHours: number | null; reason: string;
  status: 'pending' | 'approved' | 'rejected' | 'expired';
  decidedBy: string | null; decidedAt: string | null; decisionNote: string | null;
  resultingGrantId: number | null; createdAt: string;
}
```

## Sessions, recordings, audit (admin and auditor; users get 403)

| Method | Path | Notes |
| --- | --- | --- |
| GET | `/sessions?user=&cluster=&namespace=&environment=&status=active\|ended&breakGlass=true&from=&to=&limit=&before=` | `ExecSessionView[]` newest first (`before` = ISO start time for paging) |
| GET | `/sessions/{id}` | `ExecSessionView & { commands: { at: string; offsetSeconds: number; text: string }[] }` — command list is best-effort |
| GET | `/sessions/{id}/recording` | asciicast v2 (`application/x-asciicast`), assembled from the segments. Writing this read is itself audited. |
| POST | `/sessions/{id}/terminate` | admin. 204. Stream ends within 5 s. |
| GET | `/audit?from=&to=&user=&eventType=&action=&result=&cluster=&namespace=&environment=&breakGlassId=&sessionId=&q=&limit=50&cursor=` | `{ items: AuditEvent[]; nextCursor: string \| null }` |
| GET | `/audit/export.csv?...same filters...` | CSV, max 10,000 rows |

```ts
interface ExecSessionView {
  id: string; userId: number; username: string;
  cluster: string; namespace: string; workloadKind: WorkloadKind | null; workloadName: string | null;
  pod: string; container: string; shell: string; environment: string;
  debugContainer: string | null; // debug session: the ephemeral container it added; container is its target
  breakGlassId: number | null;
  startedAt: string; endedAt: string | null; durationSeconds: number | null;
  endReason: EndReason | null; recordingBytes: number; active: boolean;
}

interface AuditEvent {
  id: number; at: string; userId: number | null; username: string;
  eventType: string; action: Action | null;
  cluster: string | null; namespace: string | null; workloadKind: string | null; workloadName: string | null;
  pod: string | null; container: string | null; environment: string | null;
  result: 'allowed' | 'denied' | 'succeeded' | 'failed';
  reason: string | null; breakGlassId: number | null; sessionId: string | null;
  details: Record<string, unknown> | null; sourceIp: string | null; userAgent: string | null;
}
```

## Admin (role `admin`; `GET` also allowed for `auditor` = read-only configuration)

Every mutation writes an audit event with before/after in `details`. 409 on the "last admin" rule.

```ts
interface UserView {
  id: number; username: string; displayName: string; role: Role; disabled: boolean;
  keycloakBound: boolean; local: boolean /* portal password, never Keycloak */; lastLoginAt: string | null; teams: { id: number; name: string }[];
  activeSessions: number;
}
interface TeamView { id: number; name: string; description: string; members: { id: number; username: string; displayName: string }[] }
interface GrantView {
  id: number; holder: { kind: 'team' | 'user'; id: number; name: string };
  scope: ScopeView; actions: Action[]; expiresAt: string | null; note: string; expired: boolean;
  createdBy: string; createdAt: string; environment: string | null;
}
interface GrantInput { holder: { kind: 'team' | 'user'; id: number }; scope: ScopeView; actions: Action[]; expiresAt: string | null; note: string }
interface TimeWindowView {
  id: number; name: string; scopeKind: 'global' | 'environment' | 'cluster' | 'argo_project' | 'team'; scopeValue: string | null;
  actions: Action[]; daysOfWeek: number[] /* 0 = Sunday */; startTime: string /* HH:MM */; endTime: string /* <= startTime crosses midnight; == startTime is 24h */;
  timezone: string; effect: 'allow_inside' | 'deny_inside'; enabled: boolean;
}
interface HolidayView { id: number; date: string /* YYYY-MM-DD */; title: string; environments: string[] /* empty = all */ }
interface ClusterView {
  name: string; environment: string | null; reachable: boolean; kubernetesVersion: string | null;
  privilegeCheckOk: boolean; privilegeViolations: string[]; admissionMode: 'vap' | 'kyverno' | 'none';
  lastCheckAt: string | null; usable: boolean;
  credentialLoaded: boolean;   // false: the annotated Secret is missing or was rejected; the cluster stays listed
  environmentFromSecret: boolean; // the Secret's tiphys/environment annotation sets environment; PUT .../clusters/{name} is 409 then
  protected: boolean;          // the Secret's tiphys/protected annotation: hidden from access requests, grants still work
}
interface SettingsView {
  execMaxSessionMinutes: number; execIdleTimeoutMinutes: number;
  breakGlassMode: 'off' | 'self' | 'approval'; // off: nobody can start break-glass, active ones stop overriding time rules; approval: requested, then approved by an admin
  breakGlassDefaultMinutes: number; breakGlassMaxMinutes: number;
  breakGlassRequestMaxAgeMinutes: number;      // a pending break-glass request expires after this
  retentionAuditDays: number; retentionRecordingsDays: number;
  rocket: { webhookUrl: string; channel: string; events: Record<'break_glass_started' | 'break_glass_requested' | 'request_created' | 'request_decided' | 'exec_in_prod' | 'admin_changes', boolean> };
  lokiRetentionInfo: string | null;   // informational only
  argoSyncClusterKinds: string[];     // cluster-scoped kinds ("Kind.group", ≤50) that do not leave an app's sync to admins; core kinds, rbac/admissionregistration/apiextensions/apiregistration groups, StorageClass and PriorityClass are refused (422). Optional on PUT: left out, it is kept.
}
interface DecisionExplain extends DecisionInfo { cluster: string; namespace: string; workload: string | null; action: Action; evaluatedAt: string; userRole: Role; teams: string[]; matchedGrants: GrantView[] }
```

| Method | Path | Notes |
| --- | --- | --- |
| GET / POST | `/admin/users` | POST `{ username, displayName?, role, password? }` pre-creates a user; with `password` (needs `LOCAL_USERS_ENABLED`, at least 12 characters, at most 72 bytes; `422` on `password` otherwise) it creates a local user |
| PUT | `/admin/users/{id}/password` | `{ password }` sets a local user's password (`422` for a Keycloak user) and ends their sessions and streams. Audited without the password. |
| PATCH | `/admin/users/{id}` | `{ role?, disabled?, displayName? }`. Disabling ends sessions and streams within 5 s. |
| GET / POST | `/admin/teams` | POST `{ name, description }` |
| PATCH / DELETE | `/admin/teams/{id}` | PATCH `{ name?, description? }` |
| PUT | `/admin/teams/{id}/members` | `{ userIds: number[] }` |
| GET / POST | `/admin/grants` | GET filters: `holderKind, holderId, scopeKind, cluster, argoProject, action, expiring=true, expired=true`. `GrantView.environment` is the namespace's environment for namespace/workloads grants, the scope's for environment grants, else null. |
| GET | `/admin/grants/covering` | `?cluster=&namespace=&kind=&name=` → `GrantView[]`: grants whose scope reaches the namespace or workload (namespace, Argo project, cluster, environment, workloads grants), expired ones included. Without `kind`/`name` a workloads grant in the namespace counts. 422 without cluster/namespace. |
| PUT / DELETE | `/admin/grants/{id}` | PUT takes `GrantInput` |
| POST | `/admin/access-check` | `{ userId, cluster, namespace, workload?: {kind,name}, action, at?: string }` → `DecisionExplain` (the "access checker") |
| GET / POST | `/admin/time-windows` | |
| PUT / DELETE | `/admin/time-windows/{id}` | |
| GET / POST | `/admin/holidays` | |
| PUT / DELETE | `/admin/holidays/{id}` | |
| GET | `/admin/clusters` | `ClusterView[]` |
| POST | `/admin/clusters` | Only when `CLUSTER_REGISTRATION_FROM_UI` is on (`me.settings.clusterRegistration`; otherwise 404). `{ name, environment?, kubeconfig: string }` — validates (version + privilege check), creates the cluster and writes the kubeconfig to `CREDENTIAL_DIR` (this mode needs `CREDENTIAL_STORE=dir`; the portal never writes Secrets). Returns `ClusterView`; 422 with `details.violations` if over-privileged. The token is never returned. Clusters are always also registered by Secrets annotated `tiphys/cluster: <name>` in the portal's namespace, which the portal watches; a new Secret appears in `GET /admin/clusters` within seconds. |
| PUT | `/admin/clusters/{name}` | `{ environment }`. `409 conflict` when the cluster's Secret sets it (`tiphys/environment` annotation). |
| POST | `/admin/clusters/{name}/credential` | Only when `CLUSTER_REGISTRATION_FROM_UI` is on. `{ kubeconfig }` — replace credential (rotation). Otherwise rotate by updating the Secret. |
| POST | `/admin/clusters/{name}/check` | run the health + privilege check now → `ClusterView` |
| DELETE | `/admin/clusters/{name}` | Removes the cluster from the portal. With UI registration on it also deletes the credential file in `CREDENTIAL_DIR`. Otherwise `409 conflict` while the Secret still exists (delete the Secret or its annotation first). |
| GET / POST | `/admin/environment-overrides` | `{ id, kind: 'argo_project'\|'namespace', cluster?, argoProject?, namespace?, environment }` |
| DELETE | `/admin/environment-overrides/{id}` | |
| GET / POST | `/admin/protected-namespaces` | `string[]`; POST `{ namespace }` |
| DELETE | `/admin/protected-namespaces/{namespace}` | |
| GET | `/admin/argo` | `{ fetchedAt, ageSeconds, stale, appCount, projectCount, lastError }` |
| POST | `/admin/argo/refresh` | refresh now |
| GET / PUT | `/admin/settings` | `SettingsView` |
| POST | `/admin/settings/rocket/test` | sends a test message |
| GET | `/admin/overview` | `{ activeExecSessions, openLogStreams, pendingRequests, pendingBreakGlassRequests, activeBreakGlasses, clustersUnreachable }` for badges |
