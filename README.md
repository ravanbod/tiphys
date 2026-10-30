# Tiphys

A brokered access portal for Kubernetes. Developers sign in with Keycloak and
read logs, open a shell, restart workloads, trigger CronJobs and sync Argo CD
applications **without ever holding cluster credentials**. Authorization lives
in the portal's own database; every action is audited and every exec session is
recorded and replayable.

## Features

- **Workloads**: live and downloadable logs, a shell in a container (or in a
  debug container next to it), restarts with rollout progress, manual CronJob
  runs with job history.
- **Namespace overview**: workloads, the namespace's Kubernetes events, and a
  traffic graph from load balancers, Ingresses and Gateway API routes to
  Services and pods.
- **Argo CD**: each application's health, sync state, conditions, last
  operation and deploy history; a diff of what a sync would change; and a Sync
  button (target revision only, never prune).
- **Access control**: grants for teams or users on an Argo CD project, a
  namespace or single workloads; time windows, holidays and self-service
  break-glass; access requests with approval; admins from Keycloak groups.
- **Audit**: every action and denial in PostgreSQL and Loki, exec sessions
  recorded to MinIO with Object Lock and replayable in the browser, live watch
  of open sessions, Rocket.Chat notifications.
- **No credentials for users**: the portal acts on each cluster as one fixed,
  impersonated identity with the real username attached, limited by RBAC and
  admission policies installed by a small Helm chart.

## Screenshots

<picture><source media="(prefers-color-scheme: dark)" srcset="docs/screenshots/dark/namespace.png"><img src="docs/screenshots/light/namespace.png" alt="Namespace page: Argo CD applications with Sync, and the workloads with their allowed actions"></picture>

<table>
  <tr>
    <td width="50%"><picture><source media="(prefers-color-scheme: dark)" srcset="docs/screenshots/dark/traffic.png"><img src="docs/screenshots/light/traffic.png" alt="Traffic graph from load balancer and Ingress to Service and Deployment, one path pinned"></picture><br><sub>Traffic graph: how requests reach the pods</sub></td>
    <td width="50%"><picture><source media="(prefers-color-scheme: dark)" srcset="docs/screenshots/dark/argo-diff.png"><img src="docs/screenshots/light/argo-diff.png" alt="Argo CD application panel showing the changes a sync would apply"></picture><br><sub>What an Argo CD sync would change</sub></td>
  </tr>
  <tr>
    <td width="50%"><picture><source media="(prefers-color-scheme: dark)" srcset="docs/screenshots/dark/logs.png"><img src="docs/screenshots/light/logs.png" alt="Live log tail of a pod with levels highlighted"></picture><br><sub>Live logs, searchable and downloadable</sub></td>
    <td width="50%"><picture><source media="(prefers-color-scheme: dark)" srcset="docs/screenshots/dark/exec.png"><img src="docs/screenshots/light/exec.png" alt="Recorded shell session in a production pod"></picture><br><sub>A shell in a pod, recorded</sub></td>
  </tr>
  <tr>
    <td width="50%"><picture><source media="(prefers-color-scheme: dark)" srcset="docs/screenshots/dark/replay.png"><img src="docs/screenshots/light/replay.png" alt="Replay of a recorded session with the typed commands listed"></picture><br><sub>Session replay with the typed commands</sub></td>
    <td width="50%"><picture><source media="(prefers-color-scheme: dark)" srcset="docs/screenshots/dark/audit.png"><img src="docs/screenshots/light/audit.png" alt="Audit log of actions, denials and break-glass"></picture><br><sub>Audit log</sub></td>
  </tr>
  <tr>
    <td width="50%"><picture><source media="(prefers-color-scheme: dark)" srcset="docs/screenshots/dark/grants.png"><img src="docs/screenshots/light/grants.png" alt="Grants by team or user on Argo projects, namespaces and workloads"></picture><br><sub>Grants</sub></td>
    <td></td>
  </tr>
</table>

The screenshots come from the frontend's mock mode (`npm run dev:mock`), so
the clusters, people and data in them are made up. GitHub shows the light or
dark set to match your theme.

## Documentation

- Behavior: [spec.md](spec.md) · HTTP contract: [docs/api.md](docs/api.md) ·
  UI design: [tiphys-design/](tiphys-design/)
- [docs/kubeaccess.md](docs/kubeaccess.md) (cluster access),
  [docs/database.md](docs/database.md) (schema and roles),
  [docs/frontend.md](docs/frontend.md) (web app and mock mode),
  [deploy/chart/README.md](deploy/chart/README.md) (per-cluster chart)
- Backend: Go (gin, client-go, pgx), see [CLAUDE.md](CLAUDE.md) for the
  architecture rules. Frontend: React + TypeScript + xterm.js +
  asciinema-player in [web/](web/), embedded into the Go binary.
- Storage: PostgreSQL (state + audit), MinIO with Object Lock (recordings),
  Loki (immutable audit copy).

## How it fits together

```
browser ──HTTPS/WSS──▶ portal ──impersonates tiphys:user──▶ each cluster's API server
                         │  ├─ PostgreSQL   grants, rules, audit
                         │  ├─ MinIO        exec recordings (Object Lock)
                         │  ├─ Loki         audit copy the portal cannot alter
                         │  ├─ Argo CD      apps, projects, ownership; sync
                         │  └─ Keycloak     login (code flow + PKCE)
                         └─ nothing runs inside the clusters: each gets a small
                            Helm chart of RBAC + admission policies
                            (deploy/chart/tiphys-connector)
```

## Run it locally (no Kubernetes needed)

Prerequisite: Docker (about 1 GB of RAM for Postgres, Keycloak, MinIO and the portal).

```sh
docker compose -f deploy/dev/docker-compose.yml up -d --build
# open http://localhost:8080
docker compose -f deploy/dev/docker-compose.yml down        # add -v to drop the data
```

Sign in with `reza` / `reza` (admin), `sara` / `sara` (developer) or `leila` /
`leila` (auditor). Without a cluster and Argo CD you can use users, teams,
grants, time windows, break-glass, access requests, settings and the audit
log. For the workload screens without any backend:
`cd web && npm ci && npm run dev:mock` (add `?as=admin|auditor`).

If the image build fails fetching Go modules (some proxies answer 403 for some
modules), set another proxy: `GOPROXY=https://<proxy>,direct docker compose ... up --build`.

Running the portal natively instead (faster edit loop): start the dependencies
with `docker compose -f deploy/dev/docker-compose.yml up -d postgres keycloak minio`,
build the frontend into the binary (`cd web && npm ci && npm run build && cp -R dist/. ../internal/transport/serverhttp/webdist/`),
then create `.env` in the project root (copy `.env.example`; the Keycloak realm from Compose is on `http://keycloak.localhost:18080/realms/tiphys`, client `tiphys` / `dev-client-secret`) and run `go run ./cmd/server`. The server reads `.env` by itself.

To connect a real cluster later: install `deploy/chart/tiphys-connector` there,
build the connector kubeconfig (see `deploy/chart/README.md`), and register the
cluster by creating a Secret annotated `tiphys/cluster: <name>` (see below);
or, for local testing only, paste it on the Clusters page when
`CLUSTER_REGISTRATION_FROM_UI` is on (see below). Use the same cluster name as in
Argo CD, and set `ARGO_URL` / `ARGO_TOKEN` (environment of the `portal` service
in `deploy/dev/docker-compose.yml`, or exported before `docker compose up`) so
grants by Argo project resolve.

### Registering a cluster

The portal watches the Secrets of its own namespace. Any Secret annotated
`tiphys/cluster: <cluster name>` with the kubeconfig in its `kubeconfig` key
is a cluster: it appears on the Clusters page within seconds, and changing or
deleting the Secret is picked up the same way. The Secret's own name does not
matter.

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: staging-1-kubeconfig       # any name
  namespace: tiphys-portal          # the portal's namespace
  annotations:
    tiphys/cluster: staging-1            # the cluster name (same as in Argo CD)
    tiphys/environment: staging          # optional: the cluster's environment
type: Opaque
stringData:
  kubeconfig: |
    <the connector kubeconfig>
```

- The portal only ever *reads* these Secrets (its Role has get, list and watch
  only). You create, change and delete them; it never writes to them.
- For local testing, `CLUSTER_REGISTRATION_FROM_UI=true` lets the Clusters page
  add a cluster and replace its credential by pasting a kubeconfig. That
  writes a file into `CREDENTIAL_DIR`, so it works only with
  `CREDENTIAL_STORE=dir` (the portal refuses to start with it and
  `CREDENTIAL_STORE=secrets`), and deleting a cluster in the UI then deletes
  that file. The compose dev setup has this on.
- The environment comes from the optional `tiphys/environment` annotation.
  While it is there it cannot be changed on the Clusters page, and changing it
  in the Secret changes the cluster (audited). Without it the environment is
  set on the Clusters page. If a Secret is deleted
  outside the portal the cluster stays, shown as "No credential", so its
  grants and history are kept; with UI registration off, delete the Secret
  first and then the cluster on the Clusters page.
- With `CREDENTIAL_STORE=secrets` the portal reads the namespace of its pod
  (or `CREDENTIAL_NAMESPACE`; running outside a cluster also needs
  `MANAGEMENT_KUBECONFIG` for the cluster that holds the Secrets). With
  `CREDENTIAL_STORE=dir`, each `<name>.kubeconfig` file in `CREDENTIAL_DIR`
  is a cluster.

## Install in a cluster

1. Per cluster: `helm install tiphys-connector deploy/chart/tiphys-connector`
   (normally deployed by an Argo CD ApplicationSet; see the chart README).
   Create a kubeconfig from its `tiphys-connector` token and put it in a Secret
   annotated `tiphys/cluster: <cluster name>` in the portal's namespace. The
   portal refuses credentials that are over-privileged. After upgrading the
   portal, `helm upgrade` the chart on every cluster too: new features may need
   new rules (the traffic graph, for instance, reads Services, Ingresses and
   Gateway API routes).
2. Portal: bootstrap database roles with `deploy/sql/roles.sql`
   ([docs/database.md](docs/database.md)), create the secrets listed at the top
   of `deploy/portal/portal.yaml`, and apply it. Set
   `TIPHYS_BOOTSTRAP_ADMINS` to your Keycloak username (comma-separated for
   several); listed users are made admin at every login.
3. Keycloak: a confidential client with the redirect URI
   `https://<portal>/auth/callback` and post-logout URI `https://<portal>/*`.
   To make a Keycloak group admin, add a *Group Membership* mapper to the
   client (claim name `groups`, add to ID token, full path off) and set
   `TIPHYS_ADMIN_GROUPS=<group>[,<group>]`.
4. Argo CD: give the account behind `ARGO_TOKEN_FILE` read access and sync,
   in `argocd-rbac-cm` (narrow `*/*` to projects if you like; the portal still
   decides who may sync what):

   ```
   p, role:tiphys, applications, get,  */*, allow
   p, role:tiphys, applications, sync, */*, allow
   p, role:tiphys, projects,     get,  *,   allow
   p, role:tiphys, clusters,     get,  *,   allow
   g, tiphys, role:tiphys
   ```

   Then grant the `sync` action (shown as "ArgoCD Sync") to the teams that may
   sync; nobody has it by default.
5. Ingress: allow WebSocket upgrades and long idle timeouts on
   `/api/v1/**/exec`, `/api/v1/**/debug` and `/logs/stream`.

## Development

```sh
go build ./cmd/... ./internal/... && go vet ./cmd/... ./internal/... && go test -race ./cmd/... ./internal/...   # integration tests need PostgreSQL
cd web && npm run typecheck && npm run lint && npm test && npm run build
```

See [CLAUDE.md](CLAUDE.md) for architecture rules, behavior that is not
obvious from the code, and test setups.

## Contributing and security

See [CONTRIBUTING.md](CONTRIBUTING.md). Report vulnerabilities privately as
described in [SECURITY.md](SECURITY.md).

## License

Apache License 2.0; see [LICENSE](LICENSE) and [NOTICE](NOTICE).
