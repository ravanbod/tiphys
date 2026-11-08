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
  [deploy/chart/README.md](deploy/chart/README.md) (per-cluster chart),
  [deploy/chart/tiphys-portal/README.md](deploy/chart/tiphys-portal/README.md) (portal chart)
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
    tiphys/protected: "true"             # optional: hide it from access requests
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
  set on the Clusters page.
- `tiphys/protected: "true"` hides the cluster from access requests: users and
  auditors do not see it (or its namespaces) in the request form and requests
  for it are refused, while admins still grant on it and grants work as
  usual. It always follows the annotation (audited); removing the annotation
  lifts it. If a Secret is deleted
  outside the portal the cluster stays, shown as "No credential", so its
  grants and history are kept; with UI registration off, delete the Secret
  first and then the cluster on the Clusters page.
- With `CREDENTIAL_STORE=secrets` the portal reads the namespace of its pod
  (or `CREDENTIAL_NAMESPACE`; running outside a cluster also needs
  `MANAGEMENT_KUBECONFIG` for the cluster that holds the Secrets). With
  `CREDENTIAL_STORE=dir`, each `<name>.kubeconfig` file in `CREDENTIAL_DIR`
  is a cluster.

## Install in a cluster

Every push to `main` and every `v*` tag publishes the portal's container image
on Docker Hub as [`behradrvb/tiphys`](https://hub.docker.com/r/behradrvb/tiphys)
(`main` and `sha-<commit>` follow the branch, `X.Y.Z` / `X.Y` / `latest` are
releases), and each release publishes both Helm charts next to it:
`oci://registry-1.docker.io/behradrvb/tiphys-portal` (the portal, once) and
`oci://registry-1.docker.io/behradrvb/tiphys-connector` (every cluster users
reach). Pin a release in production; `main` moves with every commit.

The portal runs in one cluster, usually the one with Argo CD, and needs
PostgreSQL 14+, a Keycloak realm, MinIO (or any S3 with Object Lock) for the
recordings and, optionally, Loki and Argo CD. The steps below use the
namespace `tiphys-portal`. If you prefer plain manifests to Helm,
`deploy/portal/portal.yaml` holds the same objects.

### 1. Database: CloudNativePG

We recommend [CloudNativePG](https://cloudnative-pg.io) (CNPG): replicas,
failover and backups with one manifest. The portal uses three roles
([docs/database.md](docs/database.md)): the owner runs the migrations, the app
role can add audit events but never change or delete them, and only the
retention role deletes old ones. [deploy/cnpg/cluster.yaml](deploy/cnpg/cluster.yaml) creates
the database and all three; you create their passwords and the Secret the
portal reads its DSNs from:

```sh
ns=tiphys-portal
kubectl create namespace $ns
for role in owner app retention; do
  pw=$(openssl rand -hex 24)                 # hex: safe inside a URL
  kubectl -n $ns create secret generic tiphys-db-$role --type=kubernetes.io/basic-auth \
    --from-literal=username=tiphys_$role --from-literal=password=$pw
  kubectl -n $ns label secret tiphys-db-$role cnpg.io/reload=true
  declare "${role}_url=postgres://tiphys_$role:$pw@tiphys-postgres-rw.$ns.svc:5432/tiphys?sslmode=require"
done
kubectl -n $ns create secret generic tiphys-portal-database \
  --from-literal=app-url="$app_url" --from-literal=retention-url="$retention_url" \
  --from-literal=migrate-url="$owner_url"
kubectl -n $ns apply -f deploy/cnpg/cluster.yaml   # after installing the CNPG operator
```

With `migrate-url` set, the portal applies the migrations itself at startup.
Any other PostgreSQL works as well: bootstrap the roles with
`deploy/sql/roles.sql` and put the same three DSNs into
`tiphys-portal-database`.

### 2. Portal

Create the Secret with the portal's other secrets (each key is mounted as a
file; leave out the ones you do not use and set them to `""` under
`secrets.files` in the values):

```sh
kubectl -n tiphys-portal create secret generic tiphys-portal-secrets \
  --from-literal=oidc-client-secret=<Keycloak client secret> \
  --from-literal=session-keys=$(openssl rand -hex 32) \
  --from-literal=argo-token=<Argo CD token> \
  --from-literal=minio-access-key=<key> --from-literal=minio-secret-key=<secret>
```

Then install the chart with your settings. `env` takes the variables
documented in [.env.example](.env.example); set `TIPHYS_BOOTSTRAP_ADMINS` to
your Keycloak username so you are admin at your first login:

```yaml
# values.yaml
env:
  PUBLIC_URL: https://tiphys.example.com
  OIDC_ISSUER_URL: https://keycloak.example.com/realms/main
  TIPHYS_BOOTSTRAP_ADMINS: your.username
  ARGO_URL: https://argocd.example.com
  MINIO_ENDPOINT: minio.example.com:9000
  KUBE_EXTRA_DOMAIN: tiphys.example.com    # same as the connector's extraDomain
ingress:
  enabled: true
  className: nginx
  tlsSecretName: tiphys-tls
  annotations:
    nginx.ingress.kubernetes.io/proxy-read-timeout: "3600"
    nginx.ingress.kubernetes.io/proxy-send-timeout: "3600"
```

```sh
helm install tiphys oci://registry-1.docker.io/behradrvb/tiphys-portal \
  --version <X.Y.Z> -n tiphys-portal -f values.yaml
```

All values are listed in
[deploy/chart/tiphys-portal/README.md](deploy/chart/tiphys-portal/README.md).

### 3. Clusters

On each cluster users should reach, install the connector chart (normally
through an Argo CD ApplicationSet; see [deploy/chart/README.md](deploy/chart/README.md)):

```sh
helm install tiphys-connector oci://registry-1.docker.io/behradrvb/tiphys-connector \
  --version <X.Y.Z> -n tiphys-connector --create-namespace \
  --set extraDomain=tiphys.example.com
```

Create a kubeconfig from its `tiphys-connector` token and register it with a
Secret annotated `tiphys/cluster: <cluster name>` in the portal's namespace
(see [Registering a cluster](#registering-a-cluster)). The portal refuses
credentials that are over-privileged. The chart's `protectedNamespaces` lists
`tiphys` as the portal's namespace; on the portal's own cluster, add the
namespace you installed it in (`tiphys-portal` above). After upgrading the portal, upgrade the
chart on every cluster to the same version: new features may need new rules
(the traffic graph, for instance, reads Services, Ingresses and Gateway API
routes).

### Keycloak, Argo CD and the Ingress

1. Keycloak: a confidential client with the redirect URI
   `https://<portal>/auth/callback` and post-logout URI `https://<portal>/*`.
   To make a Keycloak group admin, add a *Group Membership* mapper to the
   client (claim name `groups`, add to ID token, full path off) and set
   `TIPHYS_ADMIN_GROUPS=<group>[,<group>]`.
2. Argo CD: give the account behind `ARGO_TOKEN_FILE` read access and sync,
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
3. Ingress: allow WebSocket upgrades and long idle timeouts on
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
