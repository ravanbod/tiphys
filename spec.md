# Tiphys — Behavior Spec

This document defines how Tiphys behaves; it describes the current code. The screens are in [tiphys-design/](tiphys-design/), the HTTP contract in [docs/api.md](docs/api.md).

## Scope

Tiphys is a brokered access portal: developers log in with Keycloak and read logs, exec, restart workloads, trigger CronJobs and sync or roll back Argo CD applications in Kubernetes **without ever holding cluster credentials**. Authorization lives in the portal's own database; every action is audited and every exec session is recorded.

**Goals**

1. Replace Argo CD as the developers' path to logs and exec, and remove the \~7,000-line Argo RBAC policy from that use case.
2. Grants by team or user, scoped to an Argo project, an environment, a cluster, a namespace, or specific workloads.
3. Time windows, holidays and break-glass.
4. A complete audit trail, including replayable exec sessions, that the portal itself cannot alter.
5. Users never receive a kubeconfig, token or any credential that works against a cluster API server.

**Non-goals (v1)**

- Editing manifests, scaling, deleting resources, port-forward, file copy into pods.
- Reading Secrets or ConfigMaps. (One exception: the Argo CD diff shows an application's manifests, ConfigMaps included, to users who may sync that application. Secret contents are never shown.)
- Storing application logs. Logs are streamed live from Kubernetes and never persisted (only the fact that someone viewed them is audited).
- Jira integration.
- Replacing Argo CD for deployment; Argo CD stays the source of truth for apps and ownership. The portal can ask Argo CD to sync an application to its target revision or roll it back to a deploy of its history, nothing more.

**Stack**: Go for the portal backend (gin, client-go, pgx), React + TypeScript (Vite) for the frontend, xterm.js for the terminal, asciinema-player for replay, PostgreSQL, MinIO (S3 API), Loki.

## Architecture

```
browser ──HTTPS/WSS──▶ portal ──impersonates tiphys:user──▶ each cluster's API server
                         │  ├─ PostgreSQL   grants, rules, audit
                         │  ├─ MinIO        exec recordings (Object Lock)
                         │  ├─ Loki         audit copy the portal cannot alter
                         │  ├─ Argo CD      apps, projects, ownership; sync
                         │  └─ Keycloak     login (code flow + PKCE)
                         └─ nothing runs inside the clusters: each gets a small
                            Helm chart of RBAC + admission policies
```

The portal connects to each API server directly, like Argo CD, using a token that can only act as the fixed identity `tiphys:user`, with the real username attached for the cluster's audit log.

## Authentication, users and roles

**Login**

- OIDC authorization code flow with PKCE against Keycloak. The portal is a confidential client.
- After login the portal creates a server-side session and sets an `HttpOnly`, `Secure`, `SameSite=Lax` cookie. Keycloak tokens never reach the browser.
- User identity = Keycloak `sub` (stable key) plus `preferred_username` (display, audit). Keycloak groups and roles are **not** used for authorization, with one exception: members of the groups in `TIPHYS_ADMIN_GROUPS` are promoted to admin at every login (see Roles).
- Session lifetime follows the Keycloak session; the portal re-checks with Keycloak (refresh) at least every 5 minutes and ends the session if the refresh fails.
- WebSocket connections (logs, exec) authenticate with the same session cookie, check `Origin` and take the CSRF token as a query parameter.
- **Local users** (optional, `LOCAL_USERS_ENABLED`, off by default): an admin can create a user with a password (bcrypt) who logs in on the portal's login page instead of Keycloak, so Keycloak's MFA and policies do not apply to them. Their session lasts at most 8 hours and ends within 5 minutes when the user is disabled or the feature is turned off. A Keycloak user can never be given a password, and a Keycloak login never binds to a local user. Logins are rate-limited per IP and per username.

**Users**

- A user record is created on first login, or pre-created by an admin with a Keycloak username (bound to `sub` at first login).
- Disabling a user ends all their open sessions and streams within 5 seconds.

**Roles** (stored in the portal DB, exactly one per user)

| Role | Can do |
| --- | --- |
| `user` | Use workloads per grants; request access; break-glass |
| `auditor` | Everything a `user` can (workloads per grants, access requests, break-glass), plus read the audit log, sessions and replays and a read-only view of configuration. Cannot change configuration or decide requests. |
| `admin` | Everything, including configuration and terminating sessions |

- Bootstrap: environment variable `TIPHYS_BOOTSTRAP_ADMINS` (comma-separated Keycloak usernames, case-insensitive) makes those users admins at every login, also when they already exist as regular users. `TIPHYS_ADMIN_GROUPS` (comma-separated Keycloak groups, read from the ID token's `groups` claim) does the same for group members. Both are audited as admin changes, never demote anyone, skip disabled users, and do not apply to local users. The portal database still holds the role; groups only grant it.
- Admins have implicit access to every non-protected scope and are not limited by time windows (assumption, see Defaults). Their actions are audited and their exec sessions recorded exactly like everyone else's.
- At least one active admin must always exist; the API refuses demoting or disabling the last one.

## Authorization

All decisions are made by one pure function in the portal, `Decide(user, target, action, now) → Decision`. It has no side effects, is covered by table-driven tests, and is the only place access logic lives.

### Concepts

- **Action**: `logs` (includes events and pod status), `exec`, `restart`, `run-job` on workloads; `sync` and `rollback` on a whole Argo CD application (see Whole-application decisions).
- **Target**: cluster + namespace + optional workload (kind, name) + optional pod/container.
- **Grant**: holder (team or user) + scope + actions + optional `expires_at`.
- **Team managers**: an admin may name managers for a team (Keycloak users, members or not). A manager adds and removes the team's members, so the people they add receive the team's grants; only admins change the team itself, its grants and its managers. Every change is audited under the manager's name. A manager may also request access for the team; approval grants the team.
- **Scope kinds**:
  - `argo_project`: every (cluster, namespace) that any Argo application in that project deploys to, on any cluster.
  - `namespace`: one namespace on one cluster.
  - `cluster` (admin grants only, not requestable): every non-protected namespace of one cluster, including namespaces created later.
  - `environment` (admin grants only, not requestable): every non-protected namespace whose environment (namespace override → Argo project override → cluster label) is the scope's. It follows label and override changes and does not depend on the Argo cache's age. `unlabeled` cannot be granted.
  - `workloads`: listed workloads (Deployment, StatefulSet, DaemonSet, Argo Rollout, CronJob) in one namespace on one cluster. Pods match when their owner chain resolves to a listed workload (Pod → ReplicaSet → Deployment/Rollout; Pod → StatefulSet; Pod → DaemonSet; Pod → Job → CronJob). A `workloads` scope cannot hold `sync` or `rollback`, since it cannot cover a whole application.

### Resolving Argo projects

- The portal reads Applications, AppProjects and Clusters through the Argo CD API, and refreshes its cache every 60 seconds and on demand from the admin UI. Its token's account needs `get` on applications, projects and clusters, and `sync` on applications (Argo CD authorizes a rollback as sync).
- For each application it records project, destination cluster, destination namespace, the namespaces of all resources in `status.resources`, and its cluster-scoped resources.
- A project scope covers the union of those (cluster, namespace) pairs. A namespace is never covered just because its name matches.
- If Argo is unreachable, the last good cache is used and the admin UI shows its age. If the cache is older than 15 minutes, project-scoped grants are treated as not matching (fail closed) and the UI says why.

### Environments

Effective environment of a target, first match wins: namespace override → Argo project override → cluster label → `unlabeled`. Environment names are free text set by admins (e.g. `prod`, `staging`).

### Time windows and holidays

- A **window rule** has: scope (global, environment, cluster, Argo project or team), actions, days of week, start time, end time, IANA timezone (default `Asia/Tehran`), and an effect:
  - `allow_inside`: the action is open only inside the window.
  - `deny_inside`: the action is closed inside the window.
- A rule applies to a request when its scope matches the target or the user's team, and its actions include the action.
- **Closed** if: any applicable `allow_inside` rules exist and `now` is inside none of them; or `now` is inside any applicable `deny_inside` rule; or today (in the rule's timezone, default Asia/Tehran) is a holiday whose environment list includes the target's environment.
- Windows that cross midnight (e.g. 22:00–06:00) must work.
- **Reading logs is never limited by time windows or holidays**: `logs` is always open when a grant covers it, so reading logs never needs break-glass. A rule that lists `logs` has no effect on it, and the UI does not offer `logs` in rules.

### Break-glass

- A user with role `user` or `auditor` starts break-glass with a reason (≥ 20 characters) and a duration (default 60 minutes, at most the admin-set maximum), now or **scheduled** for a later start (at most 7 days ahead, e.g. tonight at 19:00 for a planned change). A scheduled one applies from its start; until then the user (or an admin) can cancel it. A user has one active or scheduled break-glass at a time.
- The admin setting **break-glass mode** decides how it starts:
  - `self` (default): the user starts it, no approval.
  - `approval`: the user sends a break-glass request instead; it starts when an admin approves it, from that moment or from the requested start if that is later, for the requested duration or one the admin sets (shorter or longer, at most the maximum). The admin can also reject it (with a note); the requester can cancel it. A request not decided within the admin-set expiry (default 60 minutes, counted from the requested start when that is later) expires. A user has at most one pending request and none while a break-glass is active or scheduled. Requests send a Rocket.Chat alert; the decision is sent to the requester too.
  - `off`: see below.
- While active, it overrides **only** time windows and holidays, for all of that user's grants. It never grants a scope or action the user lacks.
- Starting it sends an immediate Rocket.Chat alert and writes an audit event. Every action taken under it is tagged with its id.
- Admins can turn the whole feature off in the settings (mode `off`). Then nobody can start or request one, pending requests cannot be approved, active ones stop overriding windows, and streams that depended on them end. Switching between `self` and `approval` leaves active break-glasses and pending requests as they are.

### Evaluation order

1. Target namespace is protected → **deny** `protected_namespace`.
2. User disabled → **deny** `user_disabled`.
3. Role `admin` → **allow** (skip 4–7). Roles `user` and `auditor` continue; any other role → **deny** `role`.
4. Collect grants held by the user directly or through any team; keep those not expired whose scope covers the target and whose actions include the action. None → **deny** `no_grant` (or `grant_expired` if only expired ones matched).
5. Compute environment.
6. Holiday or window closed? If not → **allow**.
7. Active break-glass → **allow** with `break_glass_id`; otherwise **deny** `outside_window` or `holiday`, with `reopens_at`.

**Decision** returns: allowed (bool), reason code, matched grant ids, environment, `break_glass_id`, `reopens_at` (when denied by time), and `closes_at` (the earliest of: window close, holiday start, grant expiry, break-glass end). The UI uses `reopens_at` and `closes_at` for its messages and countdowns.

### Whole-application decisions

`sync`, `rollback` and the Argo CD diff act on a whole Argo CD application, which may deploy to several namespaces. `DecideApp(user, app, action, now)` allows it only when `Decide` allows the action on **every** (cluster, namespace) the application deploys to: its destination and the namespaces of its resources. The first denial is returned with the namespace it is about. It also denies when the Argo cache is older than 15 minutes (the application's namespaces may have changed), and leaves to admins (`app_scope`) an application that has cluster-scoped resources other than the Namespace objects of its own namespaces and the kinds an admin allows in the settings (`argoSyncClusterKinds`, `Kind.group`; core kinds, RBAC, admission, CRDs, API services, StorageClass and PriorityClass can never be allowed), or that deploys nowhere the portal knows. An allowed decision carries the earliest `closes_at` and any break-glass one of the namespaces needed.

### Long-lived streams

Exec sessions and log tails are re-evaluated every 30 seconds and whenever grants, roles, windows, holidays or users change. The portal warns the client 5 minutes before `closes_at` and terminates the stream at `closes_at`, on idle timeout, at max duration, or as soon as a re-evaluation denies.

## Actions

All cluster access goes through the internal `kubeaccess` module (see Cluster connection), which exposes a small, fixed set of **verbs**. API handlers never get a raw Kubernetes client and never forward a client-supplied path, body or selector; the module builds every Kubernetes request itself.

| Verb | Action needed | Kubernetes calls made |
| --- | --- | --- |
| `ListNamespaces(cluster)` | — (names only, for pickers; protected namespaces left out) | list Namespaces |
| `ListWorkloads(ns)` | any action on that scope | list Deployments, StatefulSets, DaemonSets, Rollouts, CronJobs |
| `ListPods(workload)` | `logs` | list Pods, filtered by owner chain |
| `Events(workload)` | `logs` | list Events for the workload and its pods |
| `NamespaceEvents(ns)` | `logs` on the whole namespace | list Events of the namespace |
| `NamespaceTraffic(ns)` | `logs` on the whole namespace | list Ingresses, HTTPRoutes, GRPCRoutes, Services, Pods, ReplicaSets; get the Namespace (waypoint label), the Gateways routes attach to and the waypoints Services use |
| `Logs(pod, container, opts)` | `logs` | GET `pods/log` |
| `Exec(pod, container, shell)` | `exec` | CONNECT `pods/exec` (TTY, stdin) |
| `Debug(pod, container, shell)` | `exec` | UPDATE `pods/ephemeralcontainers`, then CONNECT `pods/exec` |
| `Restart(workload)` | `restart` | PATCH the workload (see below) |
| `RunCronJob(cronjob)` | `run-job` | GET CronJob, list Jobs, CREATE Job |

"On the whole namespace" means a grant on single workloads is not enough, since the result shows the other workloads of the namespace too. Argo CD actions (sync, rollback, status, history, diff) do not go through `kubeaccess`, except the discovery a rollback plan reads; see Argo CD applications.

Before every verb that names a pod, kubeaccess re-reads the pod and verifies its owner chain matches the workload in the request. A mismatch is refused and audited.

### Logs

- Options: `container`, `previous`, `follow`, `tailLines` (100 / 1,000 / 10,000), `sinceSeconds` (300 / 3,600 / 86,400), `timestamps`.
- Download = the same request without `follow`, capped at 50 MB; the response says if it was truncated.
- Log content passes through the portal unchanged and is **never stored**. The audit event records only who viewed which pod/container with which options.

### Exec

- Shell is one of an allow-list (`/bin/sh`, `/bin/bash`); no free-form commands.
- Terminal resize is supported.
- The portal records the session (see Audit). If recording storage is unavailable when a session starts, the session is **refused**; if it fails mid-session, the session is terminated.
- Limits: max session length and idle timeout from settings (defaults 2 hours and 30 minutes).

### Debug

- Optional (`DEBUG_IMAGE`, an image pinned by digest, e.g. netshoot). An exec session in a new ephemeral container that shares the chosen container's processes and the pod's network. It needs `exec`, and is recorded, limited and re-evaluated exactly like exec.
- The container runs only `sleep <max session length + 1 minute>`. Kubernetes cannot remove ephemeral containers, so it stays in the pod spec until the pod is replaced.
- Refused on clusters where the privilege check did not find the chart's `tiphys-debug-only` policy, which admits only that exact container shape for `tiphys:user`.

### Restart

| Kind | Patch |
| --- | --- |
| Deployment, StatefulSet, DaemonSet | set `spec.template.metadata.annotations["kubectl.kubernetes.io/restartedAt"]` to now (RFC 3339) |
| Argo Rollout | set `spec.restartAt` to now |

- The portal then watches status and streams progress to the browser (desired / updated / ready / available) until complete, failed, or `progressDeadlineSeconds` (default 600) passes.
- The outcome (succeeded, failed with reason, timed out) is written to the audit event.

### Run CronJob

- kubeaccess reads the CronJob and builds a Job from `spec.jobTemplate` only: name `<cronjob>-manual-<5 random chars>` truncated to 63 characters, template labels and annotations, plus annotation `cronjob.kubernetes.io/instantiate: manual` and `tiphys/triggered-by: <username>`, and an `ownerReference` to the CronJob.
- If `concurrencyPolicy` is `Forbid` and a Job owned by this CronJob is active, the run is refused with reason `already_running`.
- A suspended CronJob can still be run manually; the UI shows a warning.
- Job status is streamed until it finishes or for 10 minutes, whichever comes first; the outcome is audited.

### Namespace overview

- **Events**: the newest 200 events of every object in the namespace (`NamespaceEvents`).
- **Traffic**: how traffic from outside reaches the namespace, like Argo CD's network view: load balancer addresses and Gateways → Ingresses, HTTPRoutes and GRPCRoutes → Services → the workloads of the running pods each Service selects. Service selectors are matched in memory, never sent as selectors. A cluster without the Gateway API CRDs has no routes; Gateways in protected namespaces are never read; Services in other namespaces are shown but not followed. Istio ambient waypoints are drawn as entries of their own: a Service bound to one (`istio.io/use-waypoint` on the Service, else on the Namespace) is reached from the waypoint, through the HTTPRoutes attached to that Service when there are any; the waypoint's own Service and Deployment are not drawn, its box counts its pods. Everything no load balancer address, Gateway, Ingress or LoadBalancer/NodePort Service leads to (Services nothing exposes, waypoints and what they reach) comes last in each column under "Inside the cluster only"; "Hide in-cluster Services" hides it, and the toggle then counts its problems. Boxes carry a problem when traffic may not get through (missing Service or Gateway, route not accepted, load balancer pending, no running or ready pods).
- **Argo CD applications** deploying to the namespace (destination or any resource there), with health, sync and last sync, from the Argo cache.

### Argo CD applications

- **Sync** (`sync` via `DecideApp`): asks Argo CD to sync the application to its target revision, always without prune, dry run, a chosen revision or a subset of resources. The username goes into the operation's info, since every call is made as the token's account. The "started" audit event (`argo.sync`) is written before the request; the operation is then followed until it finishes or for 10 minutes, and the outcome is audited. Another running operation is refused with `already_running`.
- **Rollback** (`rollback` via `DecideApp`, a separate action because it deploys what is no longer in Git): asks Argo CD to roll the application back to a deploy of its history, like Rollback in Argo CD, always without prune or dry run. Argo CD refuses it for applications that sync automatically (`auto_sync_enabled`); the portal never turns auto-sync off. Multi-source applications, the current deploy and deploys whose source differs from the application's current one (but for the revision) are refused, since the portal cannot check what they would deploy. Before deciding, the portal reads the manifests of the current source at the deploy's revision from Argo CD and places them with the cluster's discovery (a namespaced object without a namespace goes to the destination; a kind the cluster does not serve counts as cluster-scoped), and decides on the application as it is now **plus** those namespaces and cluster-scoped resources, since a rollback never prunes. The plan (what the revision adds, what stays) is shown before confirming. Argo CD's rollback request carries no info, so only the audit names the user: `argo.rollback` is written before the request and the outcome after it, like a sync. The application stays OutOfSync with Git until the next sync.
- **Status and history** (anyone who sees the namespace's applications): health and sync with their messages, the application's conditions, the last operation with each resource's result, and the last 10 deploys with the commit behind each Git revision and the portal user who started it, when the audit shows one (the outcome of `argo.sync`/`argo.rollback` names the operation's start time, which Argo CD records on the deploy). Read from Argo CD on every request.
- **Diff** (same right as sync, audited as `argo.diff`): what a sync would change, from Argo CD's managed resources (normalized live state against predicted state): resources that differ, are missing from the cluster, or exist only in the cluster (never pruned). Hooks are left out, and Secrets are listed without content.

## Cluster connection

The portal connects directly to each cluster's API server, the same way Argo CD does. Nothing runs inside the clusters: each cluster gets only a small Helm chart of RBAC and admission policies, which Argo CD deploys to all clusters through an ApplicationSet.

### Per-cluster chart (no workloads)

- ServiceAccount `tiphys-connector` in the release namespace, plus a token Secret for it.
- The ClusterRoles and their bindings (below).
- The admission policies and their bindings (below), with the protected-namespace list as a parameter.

### Credentials

- Each cluster's kubeconfig (API server URL, CA, `tiphys-connector` token) is a Secret in the management cluster, in the portal's namespace, annotated `tiphys/cluster: <name>` (key `kubeconfig`). The name must match the cluster's name in Argo CD. An optional `tiphys/environment` annotation sets the cluster's environment and then wins over the admin UI. An optional `tiphys/protected: "true"` annotation makes it a protected cluster: users and auditors do not see it in the access request form and cannot request access to it, but admins still grant on it and those grants work as usual.
- The operator creates, replaces and deletes these Secrets; **the portal never writes Secrets** (its Role is get/list/watch only). It watches them, so a change applies without restart. A cluster whose Secret goes away stays, shown as "No credential", so its grants and history are kept.
- For local testing only, `CLUSTER_REGISTRATION_FROM_UI` lets admins add a cluster or replace its credential by pasting a kubeconfig, which is then written to a credential directory (never to a Secret).
- **Privilege check**, on registration and every 5 minutes, using SelfSubjectAccessReview as the connector: it must be allowed to impersonate `tiphys:user`, and must be **denied** each of: get Secrets in `kube-system`, create Pods (any namespace, `kube-system`, `default`), add ephemeral containers to Pods in any namespace, impersonate `system:admin`, impersonate group `system:masters`. If any denied check passes, the credential is over-privileged: the portal refuses to use it and flags the cluster. This stops anyone from registering Argo CD's admin credential by mistake. A cluster is unusable until its check passes.
- **Health**: the portal calls `/version` every 30 seconds on healthy clusters and every 10 seconds on clusters whose last check failed; after 3 failures in a row the cluster is marked unreachable, and one success marks it reachable again. The UI shows reachability, Kubernetes version, privilege-check result and admission mode.
- Tokens are rotated at least yearly (runbook: create a new token Secret, update the cluster's Secret, delete the old token).

### The `kubeaccess` module

All cluster access in the portal's code goes through one internal module, `kubeaccess`. It is the only code that holds Kubernetes clients, and it exposes only the verbs listed in Actions. Before every call it independently checks:

- the namespace is not protected (reads included);
- the pod's owner chain matches the workload named in the request;
- names are valid DNS-1123 and kinds and shells are on the allow-list.

### Identity toward Kubernetes

`kubeaccess` makes **every** Kubernetes call by impersonating one fixed user, with the real portal user carried as an impersonated extra:

```
Impersonate-User:  tiphys:user
Impersonate-Group: tiphys:users
Impersonate-Extra-portal.example.com%2fusername: <keycloak username>
Impersonate-Extra-portal.example.com%2fbreak-glass: <id or empty>
```

Kubernetes audit logs record the impersonated user and its extras, so the real person appears in the cluster's own audit log. Because the connector may impersonate only `tiphys:user` (enforced with `resourceNames`), a leaked portal credential cannot act as a cluster admin.

### RBAC (in the chart)

```yaml
# Connector ServiceAccount: may only impersonate the fixed identity
kind: ClusterRole
metadata: { name: tiphys-connector-impersonator }
rules:
- apiGroups: [""]
  resources: ["users"]
  verbs: ["impersonate"]
  resourceNames: ["tiphys:user"]
- apiGroups: [""]
  resources: ["groups"]
  verbs: ["impersonate"]
  resourceNames: ["tiphys:users"]
- apiGroups: ["authentication.k8s.io"]
  resources: ["userextras/portal.example.com/username", "userextras/portal.example.com/break-glass"]
  verbs: ["impersonate"]
---
# What the connector may read about admission (to report the admission mode)
kind: ClusterRole
metadata: { name: tiphys-connector-admission-reader }
rules:
- apiGroups: ["admissionregistration.k8s.io"]
  resources: ["validatingadmissionpolicies"]
  verbs: ["get", "list"]
- apiGroups: ["kyverno.io"]
  resources: ["clusterpolicies"]
  verbs: ["get", "list"]
---
# What the impersonated identity may do (bound to group tiphys:users)
kind: ClusterRole
metadata: { name: tiphys-actions }
rules:
- apiGroups: [""]
  resources: ["pods", "events"]
  verbs: ["get", "list", "watch"]
- apiGroups: [""]
  resources: ["services"]                         # traffic graph
  verbs: ["get", "list", "watch"]
- apiGroups: ["networking.k8s.io"]
  resources: ["ingresses"]                        # traffic graph
  verbs: ["get", "list", "watch"]
- apiGroups: ["gateway.networking.k8s.io"]
  resources: ["httproutes", "grpcroutes"]         # traffic graph
  verbs: ["get", "list", "watch"]
- apiGroups: ["gateway.networking.k8s.io"]
  resources: ["gateways"]                         # only the ones routes attach to
  verbs: ["get"]
- apiGroups: [""]
  resources: ["namespaces"]                       # names for pickers
  verbs: ["get", "list", "watch"]
- apiGroups: [""]
  resources: ["pods/log"]
  verbs: ["get"]
- apiGroups: [""]
  resources: ["pods/exec"]
  verbs: ["create", "get"]
- apiGroups: [""]
  resources: ["pods/ephemeralcontainers"]         # only with debug enabled
  verbs: ["update"]
- apiGroups: ["apps"]
  resources: ["deployments", "statefulsets", "daemonsets", "replicasets"]
  verbs: ["get", "list", "watch", "patch"]
- apiGroups: ["argoproj.io"]
  resources: ["rollouts"]
  verbs: ["get", "list", "watch", "patch"]
- apiGroups: ["batch"]
  resources: ["cronjobs"]
  verbs: ["get", "list", "watch"]
- apiGroups: ["batch"]
  resources: ["jobs"]
  verbs: ["get", "list", "watch", "create"]
```

No access to Secrets, ConfigMaps, delete, or any cluster-scoped write. The extras' domain is the chart value `extraDomain`, which must equal the portal's (`portal.example.com` by default); pick your own once and use it everywhere.

### Admission policies (defense in depth)

In the chart as `ValidatingAdmissionPolicy` + binding (Kubernetes 1.30+), matching requests whose `request.userInfo.username == "tiphys:user"`:

1. **Restart only**: on UPDATE of Deployments, StatefulSets, DaemonSets and Rollouts, reject unless the only change is the restart field (`restartedAt` annotation, or `spec.restartAt` for Rollouts). Compare every other field of `spec` against `oldObject`.
2. **Jobs only from CronJobs**: on CREATE of Jobs, require an `ownerReference` of kind `CronJob` and the `cronjob.kubernetes.io/instantiate: manual` annotation.
3. **Protected namespaces**: reject any write or exec in namespaces listed in the policy's parameter (kept in sync with the portal's protected list).
4. **Debug only** (with debug enabled): an update of `pods/ephemeralcontainers` may add exactly one container, named `tiphys-debug-*`, running the configured image and only `sleep <seconds>` within a bounded lifetime, targeting a container of the pod, and setting no env, volumes, ports, probes, securityContext or resources; a pod may hold a bounded number of them.

These cap the damage of a bug or compromise in the portal; they do not replace its checks. Policy 2 cannot prove a Job's spec equals its CronJob's template, so `kubeaccess` must build Jobs only from the CronJob it reads, never from client input.

If a cluster runs Kubernetes older than 1.30, policies 1–3 ship as Kyverno policies instead (debug is then unavailable), and the admin UI shows the cluster's admission mode.

## Data model

PostgreSQL. The outline below fixes meaning and constraints; exact types and indexes are in the goose migrations (`internal/infra/postgres/migrations/`). All tables have `created_at`; mutable ones also `updated_at` and `updated_by`.

```sql
users            (id, keycloak_sub UNIQUE NULL, username UNIQUE, display_name,
                  role CHECK (role IN ('user','auditor','admin')),
                  disabled BOOL, last_login_at,
                  password_hash NULL)           -- local users only; never together with keycloak_sub
teams            (id, name UNIQUE, description)
team_members     (team_id, user_id, PRIMARY KEY (team_id, user_id))
team_managers    (team_id, user_id, PRIMARY KEY (team_id, user_id))  -- may change the team's members

clusters         (id, name UNIQUE,            -- as named in Argo CD
                  environment,                 -- cluster-level label
                  kubeconfig_secret, kubernetes_version, last_check_at, reachable BOOL, privilege_check_ok BOOL,
                  admission_mode CHECK (admission_mode IN ('vap','kyverno','none')))
environment_overrides (id, kind CHECK (kind IN ('argo_project','namespace')),
                  cluster_id NULL,             -- required for kind = namespace
                  argo_project NULL, namespace NULL, environment)
protected_namespaces (namespace UNIQUE)        -- applies to all clusters

grants           (id, holder_kind CHECK (holder_kind IN ('team','user')),
                  team_id NULL, user_id NULL,
                  scope_kind CHECK (scope_kind IN ('argo_project','namespace','workloads','cluster','environment')),
                  argo_project NULL, cluster_id NULL, namespace NULL, environment NULL,
                  actions TEXT[] CHECK (actions <@ ARRAY['logs','exec','restart','run-job','sync','rollback']),
                  expires_at NULL, note, created_by)
grant_workloads  (grant_id, kind CHECK (kind IN ('Deployment','StatefulSet','DaemonSet','Rollout','CronJob')),
                  name, PRIMARY KEY (grant_id, kind, name))

time_windows     (id, name, scope_kind CHECK (scope_kind IN ('global','environment','cluster','argo_project','team')),
                  scope_value NULL, actions TEXT[], days_of_week SMALLINT[],  -- 0 = Sunday
                  start_time TIME, end_time TIME, timezone DEFAULT 'Asia/Tehran',
                  effect CHECK (effect IN ('allow_inside','deny_inside')), enabled BOOL)
holidays         (id, date, title, environments TEXT[])   -- empty = all environments

break_glass      (id, user_id, reason, started_at, expires_at, ended_at NULL, ended_by NULL)
access_requests  (id, user_id, scope fields as in grants (no cluster/environment), workloads JSONB, actions TEXT[],
                  duration_hours NULL,           -- NULL = permanent
                  reason, status CHECK (status IN ('pending','approved','rejected','expired')),
                  decided_by NULL, decided_at NULL, decision_note NULL, resulting_grant_id NULL)

exec_sessions    (id, user_id, cluster_id, namespace, pod, container, shell,
                  break_glass_id NULL, started_at, ended_at NULL,
                  end_reason NULL, recording_key, recording_bytes,
                  debug_container)              -- '' for exec, the ephemeral container for debug
audit_events     (id, at, user_id NULL, username, event_type, action NULL,
                  cluster NULL, namespace NULL, workload_kind NULL, workload_name NULL,
                  pod NULL, container NULL, environment NULL,
                  result CHECK (result IN ('allowed','denied','succeeded','failed')),
                  reason NULL, break_glass_id NULL, session_id NULL,
                  details JSONB,                 -- options, before/after for admin changes
                  source_ip, user_agent)
settings         (key PRIMARY KEY, value JSONB)
```

**Rules**

- Grant holder and scope fields are validated by CHECK constraints so that exactly the fields for the chosen kind are set.
- Approving a request grants it in the same transaction: the actions are added to the requester's own unexpired grant on exactly the same scope when that grant ends no later than the approved access (the longest such grant; it then takes the new expiry), so the new actions never outlast what was approved; otherwise, or when the admin asks for a separate grant, a new grant is created. A user's request never changes a team grant. A team manager may also request access for their team: approving it works the same way on the team's own grants (never the requester's), so every member gets it. The request form shows actions already held on the chosen scope as held and says which way approval will go; the admin sees the resulting grant before approving.
- `audit_events` is **append-only** for the application's database role: grant it INSERT and SELECT only. Retention deletes run under a separate role used only by the retention job.
- Expired grants are kept (the UI greys them out); a nightly job marks pending requests older than 7 days as `expired`.

## Audit, recording, retention and notifications

### Audit events

- Written for: login and logout; every `Decide` that ends in deny; every action started and its outcome (including Argo CD syncs and rollbacks); log views and downloads; Argo CD diff views; exec start and end; break-glass requests, their decisions and cancellations, start and end (a start after approval is recorded with the approving admin as actor and the owner in `details`); access requests and decisions; every admin change, with before and after values in `details`.
- Each event is written to PostgreSQL (for the UI) **and** pushed to Loki as one JSON line with label `app=tiphys`. The portal's Loki credentials can push but not delete. Loki is the copy that the portal cannot alter.
- An action must not run if its "started" audit event cannot be written to PostgreSQL. A Loki push failure is retried in the background and does not block.
- Kubernetes' own audit log (if enabled on the clusters) independently shows `tiphys:user` with the real username as an extra.

### Exec recording

- Format: asciicast v2 (header, then `o` output, `i` input and `r` resize events with timestamps), playable with asciinema-player.
- The portal writes the recording to MinIO in segments every 30 seconds as `sessions/<yyyy>/<mm>/<session-id>/<seq>.cast`, then a `manifest.json` at the end. A portal crash loses at most the last 30 seconds.
- Bucket has Object Lock enabled; each object gets a retention period equal to the recordings retention setting at the time it was written. The portal's MinIO credentials can put and read but not delete.
- **Command list** for the replay side panel: derived from input events, split at Enter, with timestamps. It is best-effort (tab completion and editing keys make it imperfect) and labelled as such; the replay is authoritative.
- **Live watch**: admins and auditors can attach read-only to an open session; the portal fans out the output stream. Admins can terminate it.

### Retention

- Settings: `retention.audit_days` and `retention.recordings_days` (defaults 365 and 180), editable by admins.
- A daily job deletes audit rows past retention (using its separate DB role) and relies on a MinIO lifecycle rule for recordings. Changing recordings retention applies to new recordings; existing objects keep their lock period.
- Loki retention is configured on the Loki side by operations; the admin UI shows it as information only.

### Notifications (Rocket.Chat)

- Incoming webhook URL and channel in settings; one toggle per event type: break-glass started, break-glass requested, access request created, request decided (also sent to the requester as a direct message when possible), exec opened in prod, admin changes to roles or grants.
- Each message states who, what, where (cluster, namespace, workload), environment, reason if any, and a link to the matching portal page.
- Sent asynchronously with up to 5 retries over 10 minutes. A notification failure is audited and never blocks the action.

## Security and non-functional requirements

### Security

1. **Single decision point**: all authorization goes through `Decide`. HTTP handlers, WebSocket handlers and background re-evaluation call it; none re-implement checks.
2. **No pass-through**: The portal API and the kubeaccess module accept only typed verbs with validated names (DNS-1123 for namespaces and object names, allow-listed shells and kinds). No raw paths, bodies, label selectors or field selectors from clients.
3. **kubeaccess verifies** that the pod belongs to the requested workload, that the namespace is not protected, and that the verb is allowed, before every call. Admission policies back this up for writes.
4. **Least privilege**: connector RBAC and admission policies exactly as in Cluster connection. The Argo CD token can read applications, projects and clusters and sync applications (which also covers rollback), nothing else. MinIO and Loki credentials cannot delete.
5. **Web**: CSRF protection on state-changing requests, strict Content-Security-Policy, `Origin` check on WebSockets, rate limit of 30 actions per user per minute (excluding streams).
6. **Secrets** (Keycloak client secret, DB, MinIO, Loki, Argo token, CA key) come from Kubernetes Secrets mounted as files; never logged.
7. **Reviews**: `Decide`, the kubeaccess module, the impersonation setup and the admission policies require human review before merge, plus tests listed in Acceptance criteria.

### Non-functional

| Item | Target |
| --- | --- |
| Users | under 100; up to 50 concurrent exec sessions and 200 concurrent log tails |
| Argo apps | about 1,000 across several clusters, one Argo CD instance |
| `Decide` latency | under 5 ms p99 using cached Argo data |
| Page load (workloads home) | under 1 s for a user with grants on 200 apps |
| Log first byte | under 2 s after opening |
| Cluster disconnect detection | within 90 s |
| Revocation (grant, user, window close) | streams end within 30 s; within 5 s for user disable and admin terminate |

### Deployment

- The portal runs in the management cluster next to Argo CD, is deployed by Argo CD, and needs network access to every cluster's API server (as Argo CD already has).
- v1 runs one portal replica; a restart drops live streams (users reconnect). Keep the portal stateless apart from in-flight streams so replicas can be added later. Deploys are rolling, so two pods overlap briefly: the new pod marks exec sessions still running on the old one as ended (`portal_restart`), and in-memory state (operations, live watch, rate limits) is per pod. Migrations must stay compatible with the previous release, which keeps running during them.
- The per-cluster chart contains only: the `tiphys-connector` ServiceAccount and token Secret, the ClusterRoles and bindings, and the admission policies and bindings. No Deployment.
- Health endpoints and Prometheus metrics on the portal: request counts by verb and result, decision latency, open streams, cluster reachability and privilege-check state.

## Acceptance criteria

**Authorization (`Decide` table tests)**

- [ ] A team grant on Argo project `payments` allows `logs` on a pod of `payments-api` and denies it on a pod of an app in another project.
- [ ] A workload grant on Deployment `payments-api` allows its pods and denies pods of `payments-worker` in the same namespace.
- [ ] A grant with `logs` only denies `exec` with reason `no_grant`.
- [ ] An expired grant denies with `grant_expired`.
- [ ] A protected namespace is denied for every role including admin.
- [ ] An `allow_inside` window 09:00–18:00 Asia/Tehran Sat–Wed denies exec in prod at 18:01 on Wednesday with `reopens_at` = Saturday 09:00, and allows it in staging.
- [ ] A window 22:00–06:00 behaves correctly at 23:00 and 05:00.
- [ ] A holiday for `prod` denies prod and not staging.
- [ ] Active break-glass allows outside a window but still denies a scope with no grant.
- [ ] A scheduled break-glass changes nothing before its start, overrides windows from it (a session open at the window's close continues), and never applies once cancelled.
- [ ] Namespace environment override beats project override, which beats the cluster label.
- [ ] With the Argo cache older than 15 minutes, project-scoped grants do not match.
- [ ] `logs` stays open outside every time window and on holidays.
- [ ] Syncing an application is allowed only with `sync` on every namespace it deploys to; a missing namespace denies with that namespace, and an application with other cluster-scoped resources needs an admin unless their kinds are allowed in the settings.

**End to end (against a test cluster)**

- [ ] A user with no cluster credentials logs in via Keycloak and tails logs of an allowed pod, including the previous container.
- [ ] Exec opens a shell; the session appears in Sessions; after disconnect it replays exactly, and the command list shows typed commands.
- [ ] With MinIO stopped, exec is refused with a clear message.
- [ ] Revoking the grant terminates an open exec within 30 seconds; disabling the user within 5 seconds.
- [ ] Five minutes before a window closes, the client receives a warning; at close the session ends with reason `outside_window`.
- [ ] Restart of a Deployment, StatefulSet, DaemonSet and Rollout each completes and reports progress.
- [ ] Run CronJob creates a Job owned by the CronJob; a second run with `concurrencyPolicy: Forbid` while the first is active is refused.
- [ ] Sync of an Argo CD application runs without prune, records the username in the operation's info, and reports its outcome; a sync while another operation runs is refused.
- [ ] Rolling back an application needs `rollback` (not `sync`) on every namespace it deploys to now and every namespace the deploy's revision deploys to; a revision with cluster-scoped resources needs an admin unless their kinds are allowed; applications with auto-sync, multi-source applications and deploys of another source are refused; it never prunes.
- [ ] The Kubernetes audit log shows `tiphys:user` with the real username extra for each of the above.

**Defense in depth (call the API server directly as `tiphys:user`)**

- [ ] Changing a Deployment's image is rejected by admission.
- [ ] Creating a Job without a CronJob owner is rejected.
- [ ] Reading a Secret is forbidden by RBAC, and adding a cluster with a cluster-admin kubeconfig is refused by the privilege check.
- [ ] The connector ServiceAccount cannot impersonate any user other than `tiphys:user`.

**Audit**

- [ ] Every action and denial above produces an audit event in PostgreSQL and in Loki.
- [ ] The application DB role cannot UPDATE or DELETE `audit_events`.
- [ ] Every admin change records before and after values.

## Defaults

| Item | Default |
| --- | --- |
| Max exec session length | 2 hours (admin setting) |
| Exec idle timeout | 30 minutes (admin setting) |
| Warning before forced close | 5 minutes |
| Open session when a window closes | Warned, then terminated at close |
| Break-glass duration | Default 60 minutes, maximum 240 (admin setting) |
| Break-glass mode | `self` (no approval); `approval` and `off` are admin settings |
| Break-glass request expiry | 60 minutes undecided (admin setting) |
| Admins and time windows | Admins are not limited by time windows |
| Retention defaults | Audit 365 days, recordings 180 days (admin settings) |
| Log download cap | 50 MB per download |
| Pending request expiry | 7 days |
| Kubernetes version | 1.30+ for ValidatingAdmissionPolicy; Kyverno fallback otherwise |
| Week start in UI | Saturday |
| Domain for impersonation extras | `portal.example.com`; set your own in the portal and the chart (`extraDomain`) |
| Cluster credential rotation | At least yearly, by updating the cluster's Secret |
| Argo CD sync deadline | 10 minutes |
| Rate limit | 30 actions per user per minute (`RATE_LIMIT_PER_MINUTE`) |
