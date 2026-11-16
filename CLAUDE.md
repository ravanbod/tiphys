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
deploy/chart/tiphys-portal       the portal's Helm chart (same objects as deploy/portal)
deploy/portal                    portal manifests; deploy/sql roles + test DB helper
deploy/cnpg                      example CloudNativePG cluster with the three DB roles
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

Details live in `spec.md` and `docs/`; these are the rules easy to break.

- **Argo cache older than 15 minutes** → project-scoped grants stop matching
  (`argo_stale`); other grants are unaffected. Argo CD only annotates
  namespaces the clusters report (`ClusterAccess.ListNamespaces`, cached 30 s,
  plus namespaces the user's grants name); it never adds a namespace or cluster.
- **Auditors work like users** (`Role.UsesGrants`) and also read audit,
  sessions and configuration. Only admins change configuration or decide
  requests; `/my/access` lists only the caller's own requests.
- **Cluster and environment grants** are admin only (requests get 422).
  `environment` follows `Policy.Environment` (namespace override → Argo project
  override → cluster label); `unlabeled` cannot be granted; protected
  namespaces are never covered.
- **Approving merges** the actions into the requester's (or the team's own)
  unexpired grant on exactly the same scope that ends no later than the
  approval (`mergeTarget`); otherwise, or with `separate: true`, a new grant.
  `web/src/lib/requestMerge.ts` mirrors the rule.
- **Break-glass** overrides only time windows and holidays; logs are never
  time-limited. `Settings.BreakGlassMode` is `self`, `off`
  (`ErrBreakGlassDisabled`; active ones stop overriding) or `approval`
  (`BreakGlassService.Request`, admins decide on Access requests). It can be
  scheduled ≤ 7 days ahead; the snapshot holds the active or next one per user
  (`ListCurrent`). The old `break_glass.enabled` key is still written for the
  previous release.
- **Delete pod** is authorized as `restart` (`WorkloadService.DeletePod`,
  `kubeaccess.DeletePod`: owner chain, then a UID precondition); hidden
  (`WorkloadDetail.podDelete`, `ClusterAccess.PodDeleteReady`) and refused
  (`ErrPodDeleteUnavailable`) on clusters lacking the chart's
  `tiphys-pod-delete-only` policy (VAP or Kyverno).
- **Debug** is exec in an ephemeral container (`DEBUG_IMAGE`,
  `kubeaccess.Debug`), authorized and recorded as exec and never removed. Off
  without `DEBUG_IMAGE` or on clusters lacking the chart's `tiphys-debug-only`
  VAP.
- **Exec recording**: 30 s segments, segment 0 written at start (a dead MinIO
  refuses the session); a failed flush ends it with `recording_failed`.
- `closes_at` is the first instant `Decide` would deny (windows, holidays,
  grant expiries, break-glass end).
- **WebSockets** authenticate with the cookie, check `Origin`, take CSRF as
  `?csrf=`; binary frames are raw bytes, text frames JSON control. Denials are
  plain HTTP errors before the upgrade.
- **Sessions**: Keycloak tokens encrypted server-side; the cookie is a random
  token whose sha256 is the DB key; refreshed every ≤ 5 min; no
  `offline_access`, so the session ends with Keycloak's.
- **Audit**: the app DB role has only INSERT/SELECT on `audit_events`;
  retention uses `DATABASE_RETENTION_URL`.
- **Admin promotion** at every login (`AuthService.promoteAdmin`):
  `TIPHYS_ADMIN_GROUPS` (groups claim `OIDC_GROUPS_CLAIM`, the only use of
  Keycloak groups) and `TIPHYS_BOOTSTRAP_ADMINS` (usernames). Never demotes;
  skips disabled and local users.
- **Batches**: requests for 2–20 namespaces of one cluster share a `batch_id`
  and are decided only as a whole (`DecideBatch`, one transaction; single
  approve/reject refuse them). Admins grant 2–50 namespaces at once
  (`AdminService.CreateGrants`, no merging).
- **Team managers** (`team_managers`) add and remove members (enabled Keycloak
  users only) and request access for the team; `requireTeamManager` checks in
  the transaction. Only admins change a team's name, grants and managers.
- **Local users** (`LOCAL_USERS_ENABLED`): bcrypt password via
  `POST /auth/local-login`, no Keycloak MFA, sessions ≤ 8 h whose refresh only
  re-checks the user and the flag. Never combined with a Keycloak sub.
- **Dev login** (`DEV_LOGIN_USER`, refused in release) swaps Keycloak for
  `oidc.Dev` on the normal login path; `admin` is promoted through the
  `tiphys-dev-admins` group.
- **Traffic graph** (`kubeaccess.NamespaceTraffic`) is read from the cluster,
  not Argo, including Istio ambient waypoints; authorized as `logs` on the
  namespace. In-cluster grouping: `inClusterNodes` in `web/src/lib/traffic.ts`.
- **Argo CD sync and rollback** are separate actions (not on `workloads`
  scopes). `domain.DecideApp` needs the action on every namespace the app
  touches and a fresh cache; cluster-scoped resources are admin only except
  `Settings.ArgoSyncClusterKinds` (`domain.CheckSyncClusterKind`). Never prune,
  dry run or pick a revision on sync. Rollback refuses auto-sync apps (409) and
  other sources (422) and decides on the app plus the revision
  (`ArgoApp.Including`). Diffs are audited; Secret contents are never shown.
- **Argo UI** stays off (`ARGO_UI = false`, `web/src/lib/features.ts`) except
  the namespace page's apps card and the Argo CD cache card.
- **Live changes** (`GET /api/v1/live`, SSE, ≤ 10 namespaces, ≤ 8 streams per
  user and pod): `kubeaccess.WatchNamespace` as `tiphys:user`, every change
  decided again (`liveView`), a `StreamLive` ending after 10 min. One stream
  per tab (`web/src/context/live.tsx`).
- **Cluster health** (`ClusterService.Run`): healthy every 30 s, failing every
  10 s, privilege check every 5 min; three failures → unreachable. A cluster is
  unusable until its privilege check passes.
- **Clusters are registered by Secrets** annotated `tiphys/cluster: <name>`
  (key `kubeconfig`). `tiphys/environment` wins over the UI (409 on
  `SetEnvironment`); `tiphys/protected: "true"` hides the cluster from
  non-admins' pickers and refuses requests. **The portal never writes
  Secrets** (`SecretStore` returns `ErrReadOnly`).
  `CLUSTER_REGISTRATION_FROM_UI=true` (only with `CREDENTIAL_STORE=dir`) is for
  local testing.
- **API tokens** (`domain.APIToken`, `AuthService.AuthenticateToken`): admins
  create them on Settings with permissions from `domain.AllPermissions`; only
  the sha256 is stored. A token principal has `Principal.Token` set and a
  role-less `User` named `token:<name>`, so `RequireRole` always refuses it;
  services that tokens may call use `RequirePermission`. The router lets a
  token reach only `tokenRoutes` (time windows, holidays, settings GET and
  `PATCH`) and skips CSRF for it. Tokens never manage tokens.
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
with a PostgreSQL service), frontend (typecheck, lint, tests, build) and the
image, which it pushes to Docker Hub on `main` and `v*` tags once both passed
(variable `DOCKERHUB_USERNAME`, secret `DOCKERHUB_TOKEN`, optional
`DOCKERHUB_IMAGE`; without the token it only builds), and lints both charts,
pushing them as OCI charts with the tag's version on `v*` tags. Keep
`deploy/portal/portal.yaml` and the portal chart in step. The Dockerfile pulls base
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
