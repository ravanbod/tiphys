# tiphys

Brokered access portal: developers log in with Keycloak and read logs, exec,
restart workloads and trigger CronJobs in Kubernetes without ever holding
cluster credentials. Authorization lives in the portal's own PostgreSQL;
every action is audited and every exec session recorded. `spec.md` is the
behavior spec, `tiphys-design/` the visual design, `docs/api.md` the HTTP
contract between `web/` and the Go server.

## Working with the user

- Before any cross-cutting or structural change (touching many layers,
  changing port signatures, new packages), describe the design and get
  approval first. "Do all of it" approves features, not a design the user
  hasn't seen.
- Keep changes minimal and in the existing style.
- After implementing, verify end to end (see Testing) and clean up every
  process, container and temp file you started.

## Layout

```
cmd/server/main.go               composition root (manual DI, no framework)
internal/domain                  entities + Decide (the only authorization logic)
internal/application
  ports.go                       outbound ports: what the app needs from infra
  *.go                           services (see below)
internal/infra/
  postgres/                      repositories, goose migrations, privileges
  kubeaccess/                    the ONLY holder of Kubernetes clients
  credentials/                   cluster kubeconfig store (k8s Secrets / dir)
  oidc/ sealer/                  Keycloak code flow + PKCE, AES-GCM sealing
  argocd/ recording/ loki/ rocketchat/
internal/transport/serverhttp    gin handlers, middleware, WebSockets, SPA serving
internal/config internal/monitoring internal/requestid
internal/integration             whole-portal tests with fake cluster/Argo/MinIO
web/                             React + TypeScript frontend (Vite)
deploy/chart/tiphys-connector    per-cluster Helm chart (RBAC + admission policies)
deploy/portal                    portal manifests; deploy/sql roles + test DB helper
docs/                            api.md, database.md, kubeaccess.md, frontend.md
```

Application services: `PolicyService` (immutable snapshot of users, teams,
grants, rules, Argo data; reloaded after every change and every 5 s),
`AccessService` (wraps `domain.Decide`, audits denials), `AuditService`,
`WorkloadService` (list/logs/restart/run-job), `ExecService` (recorded
sessions), `StreamManager` (re-evaluates open streams), `AuthService`,
`AdminService`, `ClusterService` (health + privilege monitor),
`BreakGlassService`, `AccessRequestService`, `NotificationService`,
`MaintenanceService`.

## Design rules

- **Dependencies point inward.** `domain` imports nothing internal. `infra`
  implements ports defined in `application`. Transport calls services only.
- **One decision point.** Every handler, WebSocket and the stream supervisor
  decides through `AccessService` → `domain.Decide`. Never re-implement a
  check in a handler or service. `Decide` is pure: all data comes from the
  `Policy` snapshot, time comes in as a parameter.
- **kubeaccess is the only code with Kubernetes clients.** It exposes typed
  verbs, never takes a client path/selector/body, and independently checks
  protected namespaces, DNS-1123 names, kinds/shells and pod owner chains
  before every call. Everything is done as `tiphys:user` with the real
  username as an impersonation extra.
- **Audit before action.** An action does not run if its "started" audit
  event cannot be written to PostgreSQL (`WorkloadService.start`,
  `ExecService.Open`). The Loki push is queued and never blocks.
- **Admin changes** go through `AdminService.mutate`/`ClusterService.audited`:
  the change and its audit event (with before/after) share one transaction.
- **Explicit data flow.** Dependencies are injected through constructors;
  `context` carries only the read-only request ID.
- **Secrets from files** (`NAME_FILE`), never logged.

## Logging

zap: JSON in `GIN_MODE=release`, console otherwise; sampling disabled on
purpose. Log each error once where it occurs. Business outcomes (denials,
validation failures) are not logged by handlers; infrastructure failures are
Warn/Error. Every request-path line carries `requestid.Field(ctx)`.

## Behavior that isn't obvious from the code

- **Argo cache older than 15 minutes** → project-scoped grants stop matching
  (reason `argo_stale`); namespace and workload grants are unaffected.
- **Break-glass** only overrides time windows and holidays; it is tagged on a
  decision only when it actually overrode a closed window. The whole feature has
  an on/off setting (`Settings.BreakGlassEnabled`, default on, Settings page):
  off means `Start` returns `ErrBreakGlassDisabled` (403 `break_glass_disabled`),
  `PolicyService` attaches no break-glass to decisions (so active ones stop
  overriding windows and open streams that depended on them end), and the UI hides
  it (`me.settings.breakGlassEnabled`).
- **Logs are never limited by time windows or holidays** (`Policy.timeClosed` and
  `boundaries` return early for `logs`), so reading logs never needs break-glass,
  with the feature on or off. Rules that still list `logs` are ignored and the UI
  no longer offers it.
- **Debug**: an exec session in a new ephemeral container
  (`DEBUG_IMAGE`, e.g. netshoot pinned by digest) that `kubeaccess.Debug` adds
  to the pod with `targetContainerName` = the chosen container, so it shares
  its processes and the pod's network. It is authorized as `exec` (no own
  action), recorded and re-evaluated like exec; `exec_sessions.debug_container`
  and the `exec.start` details name the container. The container runs only
  `sleep <max session + 1 min>` and is never removed (Kubernetes cannot); it
  stays in the pod spec until the pod is replaced. Off when `DEBUG_IMAGE` is
  empty (route not registered, `me.settings.debugEnabled` false) and on any
  cluster whose privilege check did not find the chart's `tiphys-debug-only`
  VAP (`debug.enabled`, VAP mode only), which admits only that exact container
  shape for `tiphys:user`. The upgrade waits up to 45 s for the container.
- `closes_at` is computed by scanning window/holiday boundaries, grant
  expiries and the break-glass end for the first instant `Decide` would deny.
- **Exec**: recording is written in 30 s segments (segment 0 = asciicast
  header, written at start so a dead MinIO refuses the session). A failed
  flush ends the session with `recording_failed`.
- **WebSockets** authenticate with the session cookie, check `Origin`, and
  take the CSRF token as `?csrf=`. Binary frames are raw bytes, text frames
  JSON control messages. Denials happen before the upgrade as normal HTTP
  errors.
- **Sessions**: Keycloak tokens are stored encrypted server-side; the cookie
  is a random token whose sha256 is the DB key. Refreshed every ≤5 min; the
  scope set deliberately omits `offline_access` so the portal session ends
  with the Keycloak session.
- **Audit**: the app DB role has INSERT/SELECT only on `audit_events`;
  retention deletes use a separate role (`DATABASE_RETENTION_URL`).
- **Admins from Keycloak groups**: `TIPHYS_ADMIN_GROUPS` (comma-separated)
  promotes members to admin at every login (`AuthService.promoteByGroups`),
  audited as an `admin.change` with before/after. It never demotes, ignores
  case and a leading `/`, and skips disabled users. It is the only use of
  Keycloak groups: the portal DB still holds the role, groups only grant it. The client needs a group
  membership mapper adding `groups` to the ID token (`OIDC_GROUPS_CLAIM`).
  `TIPHYS_BOOTSTRAP_ADMINS` (comma-separated usernames, case-insensitive) works
  the same way at every login (audited as `promote_by_username`), also for users
  who already logged in; a user demoted on the Users page while still listed is
  promoted again at the next login. Both go through `AuthService.promoteAdmin`.
- **Local users**: with `LOCAL_USERS_ENABLED=true` (default
  off) an admin can create a user with a password (`users.password_hash`,
  bcrypt cost 12) who logs in on the login page through `POST /auth/local-login`
  (`AuthService.LocalLogin`), never through Keycloak, so Keycloak's MFA and
  policies do not apply. The session has no tokens and lasts at most 8 h; the
  5-minute refresh (`refreshLocal`) only checks the user is not disabled and the
  feature is still on, so turning it off ends local sessions within 5 minutes.
  A Keycloak login never binds to a local user (and the DB CHECK forbids a sub
  with a hash); a Keycloak user cannot be given a password. Setting a password
  ends the user's sessions. Login is limited per IP and per username in memory
  (per pod). Admin groups and bootstrap admins do not apply to local users.
- **Dev login**: `DEV_LOGIN_USER=<name>` (refused with `GIN_MODE=release`, the
  default) replaces Keycloak with `oidc.Dev`: its authorize URL is the portal's
  own callback, so the normal login path runs (user, session, `login` audit),
  and `/login` redirects straight to `/auth/login`. `DEV_LOGIN_USER=admin` is
  put in `tiphys-dev-admins`, which is appended to the admin groups, so it is
  promoted at every login; any other name is a regular user. The refresh token
  carries the username, so after switching users the old session ends at its
  next refresh (≤5 min). OIDC_* settings are optional then.
- **Namespace lists** (pickers and "By namespace") come only from the clusters
  (`ClusterAccess.ListNamespaces`, cached 30 s per cluster), plus, for "By
  namespace", the namespaces the user's grants name on registered clusters. A
  failing cluster lists nothing. Argo CD never adds a namespace or cluster: it
  only annotates these with apps and projects, so apps on clusters tiphys does
  not know (or in namespaces no cluster reported) are not shown. Argo CD is
  optional enrichment (apps, health/sync, project grants).
- **Traffic graph** on the namespace page, like Argo CD's
  network view: `GET .../namespaces/{ns}/traffic` (`kubeaccess.NamespaceTraffic`)
  draws LB addresses and Gateways → Ingresses/HTTPRoutes/GRPCRoutes → Services →
  workloads from the cluster itself (not Argo), with a `problem` on nodes where
  traffic may not get through. Authorized like namespace events (`logs` on the
  whole namespace). Needs the chart's `services`, `ingresses`, `httproutes`/
  `grpcroutes` and `gateways` (get only) rules; Gateways in protected namespaces
  are never read.
- **Argo CD sync**: a
  `sync` action (grants, window rules, requests; not on a `workloads` scope)
  lets users sync a whole app from the namespace page's Argo CD card through
  `POST .../apps/{appNs}/{name}/sync` (`WorkloadService.SyncApp`), with the
  same `ARGO_TOKEN_FILE`, which therefore needs `applications, sync`.
  `domain.DecideApp` allows it only when `Decide` allows `sync` on **every**
  namespace the app deploys to (destination and status.resources), with a
  fresh Argo cache; apps with cluster-scoped resources other than their own
  Namespace objects are admins only (`app_scope`). Always the target
  revision, never prune/dry run/revision; the username goes into the
  operation's `infos`. Audited as `argo.sync`, followed by polling
  `operationState` (10 min), then the Argo cache is refreshed.
  Clicking an app opens its details, read live from Argo CD (`applications,
  get`): status/conditions/last operation and history (anyone who sees the
  namespace's apps), and the diff a sync would apply (`.../diff`, same right
  as the sync, audited as `argo.diff`; Secret contents are never shown).
- **Argo apps on the namespace page** are shown although the rest of the Argo
  UI is behind `ARGO_UI = false` (`web/src/lib/features.ts`):
  `GET .../namespaces/{ns}/apps` returns every app whose destination or
  resources are in the namespace, with health, sync and the last sync
  operation (`status.operationState`), linked to `ARGO_PUBLIC_URL` (default
  `ARGO_URL`, sent as `me.settings.argoUrl`). The Argo CD cache card on
  Clusters & environments (fetch age, counts, last error, Refresh now) is
  shown too.
- **Cluster health** is checked in the background by `ClusterService.Run`
  (tick 5 s): healthy clusters every 30 s, ones that failed their last check
  every 10 s, privilege check every 5 min. Three failures in a row mark a
  cluster unreachable; one success marks it reachable again. "Credential not
  loaded yet" counts as an ordinary failure. The Clusters page polls every 15 s.
- **Cluster credentials**: a cluster is unusable until its privilege check
  passes (connector must be allowed to impersonate `tiphys:user` and denied
  secrets/pods/system:admin/system:masters).
- **Clusters are registered by Secrets**, not through the UI: any Secret in the
  portal's namespace annotated `tiphys/cluster: <name>` (kubeconfig in the
  `kubeconfig` key) is a cluster; `ClusterService` creates the row and keeps
  it, shown as "No credential", if the Secret goes away. An optional
  `tiphys/environment: <label>` annotation sets the environment and wins over
  the UI: the row follows it on every watch event and sync (audited as
  `environment_from_secret`), `SetEnvironment` returns 409 while it is there,
  and the UI shows it read-only. Without it the environment is set in the UI
  and kept when the annotation is removed. **The portal never writes, updates or deletes Secrets**:
  `SecretStore.Put/Delete` always return `ErrReadOnly` and its Role is get/list/
  watch only; the operator manages the Secrets. `CLUSTER_REGISTRATION_FROM_UI=true`
  (local testing; config refuses it unless `CREDENTIAL_STORE=dir`) registers the
  Add/Replace endpoints and tells the UI (`me.settings.clusterRegistration`);
  the kubeconfig is then written to `CREDENTIAL_DIR`. `ClusterService.Add`
  creates the row before writing the credential so `registerFromStore`
  (triggered by it) finds it. Delete removes the credential file too in that
  mode, and otherwise refuses while the Secret exists.
- All env vars are documented in `.env.example`.

## Testing

- `go build ./cmd/... ./internal/... && go vet ./cmd/... ./internal/... && go test -race ./cmd/... ./internal/...`.
- Integration tests (`internal/integration`, `internal/infra/postgres`) need
  PostgreSQL: `eval "$(deploy/sql/test-db.sh start)"`, run, then
  `deploy/sql/test-db.sh stop`. They skip without `TIPHYS_TEST_DATABASE_URL`.
- Recording integration: `eval "$(deploy/minio/test-minio.sh start)"`.
- Kubernetes integration (opt-in, `TIPHYS_TEST_KUBECONFIG`) runs against a
  kind cluster with the chart installed; see `docs/kubeaccess.md`.
- Frontend: `cd web && npm run typecheck && npm run lint && npm test && npm run build`;
  `VITE_MOCK=1 npm run dev` runs it without the backend.
- Whole portal locally: `docker compose -f deploy/dev/docker-compose.yml up -d --build`
  (portal + Postgres + Keycloak + MinIO, no cluster needed).
- Whole-system smoke with a cluster: Keycloak (`quay.io/keycloak/keycloak:26.0`, `start-dev
  --import-realm`) + the PostgreSQL helper + `CREDENTIAL_STORE=dir`.
- Put temp files in the session scratchpad; kill every process and remove
  every container afterwards.

## CI

GitHub: `.github/workflows/ci.yml` runs the backend (build, vet, race tests
with a PostgreSQL service), frontend (typecheck, lint, tests, build) and
image build checks. The Dockerfile pulls base
images from Docker Hub unless `BASE_REGISTRY` (and `GOPROXY`, `GOSUMDB`,
`NPM_REGISTRY`) point at mirrors.

## Known open items

- v1 runs one replica; a restart drops live streams.
- Deploys are RollingUpdate (`maxSurge: 1`, `maxUnavailable: 0`), so two pods
  overlap briefly: the new pod's `AbandonedSessions` marks exec sessions still
  running on the old one as ended (`portal_restart`), and in-memory state
  (operations, live watch, rate limits) is per pod. Accepted for now; the fix
  is per-pod session ownership (instance ID + heartbeat). Migrations must stay
  compatible with the previous release, since it keeps running during them.
- Loki and MinIO least-privilege users are configured outside the portal; the
  portal only proves what it can (Object Lock, push-only usage).
