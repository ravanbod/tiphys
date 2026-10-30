# kubeaccess and the cluster credentials

`internal/infra/kubeaccess` implements `application.ClusterAccess`; it is the
only code that holds Kubernetes clients. `internal/infra/credentials`
implements `application.CredentialStore`. The per-cluster Helm chart is in
`deploy/chart/tiphys-connector` (see its README).

## Wiring (composition root)

```go
store, _ := credentials.NewSecretStoreFromConfig(cfg.KubeconfigPath, cfg.Namespace, 5*time.Minute, logger)
// local development: credentials.NewDirStore("./clusters", 2*time.Second, logger)

reg := kubeaccess.NewRegistry(store, logger)
access := kubeaccess.New(reg, protected, logger /*, kubeaccess.WithExtraDomain("portal.mycompany.example") */)
go reg.Run(ctx) // loads every credential, then follows the store

var _ application.ClusterAccess = access
```

`protected` is anything with `IsProtected(ns string) bool` (the portal's
protected-namespace list). `Registry` is embedded in `Access`, so `Registered`,
`SetUsable`, `Version`, `Probe` and `ProbeKubeconfig` are the `ClusterAccess`
methods of the same name.

The portal's ServiceAccount in the management cluster needs get, list, watch,
create, update and delete on Secrets in its own namespace (SecretStore).

## What every verb does

1. Validates the cluster name, namespace (DNS-1123 label), object names, the
   workload kind and, for exec, the shell, against the allow-lists in `domain`.
2. Refuses protected namespaces (reads included) before it looks at the cluster.
3. Looks the cluster up: `ErrClusterUnknown`, or `ErrClusterUnusable` when the
   privilege check failed or the portal called `SetUsable(false)`.
4. Derives a rest.Config for this call from the cluster's base config (API
   server, CA, connector token) with `Impersonate-User: tiphys:user`,
   `Impersonate-Group: tiphys:users` and the extras
   `<domain>/username` and `<domain>/break-glass` (always sent, empty when no
   break-glass). The TLS transport is shared by client-go, so a config per call
   is cheap; nothing is keyed by user.
5. For verbs that name a pod: re-reads the pod and walks its owner chain
   (Pod -> ReplicaSet -> Deployment/Rollout, Pod -> StatefulSet,
   Pod -> DaemonSet, Pod -> Job -> CronJob; group and kind are compared, not only
   the name). Mismatch: `ErrPodNotInWorkload`.

Label and field selectors are never built from workload contents. Pod and event
lists are filtered in memory by owner chain (Service selectors for the traffic
graph are matched in memory too); the only selector is
`job-name=<validated job name>` when watching a Job.

## Behavior notes

- **ListNamespaces** lists namespace names of a cluster (the only verb without a namespace argument). Protected namespaces are left out. It needs the `namespaces` get/list rule in `tiphys-actions`; without it the call is forbidden and the portal uses Argo CD's namespaces only.

- **NamespaceTraffic** builds the namespace page's traffic graph: load balancer
  addresses (Ingress and LoadBalancer Service status) and Gateways → Ingresses,
  HTTPRoutes and GRPCRoutes → Services → the workloads of the running pods each
  Service selects (Pod → ReplicaSet → Deployment/Rollout, else the pod's
  controller). ClusterIP Services no Ingress or route reaches are left out.
  Gateway API objects are read with the dynamic client (`v1`, falling back to
  `v1beta1`/`v1alpha2`); a cluster without the CRDs simply has no routes. The
  Gateways routes attach to are read with `get` for their class, addresses and
  `Programmed` condition, except in protected namespaces (name only); Services
  in other namespaces are shown but never read. Nodes carry a `Problem` when
  traffic may not get through (Service or Gateway not found, route not accepted
  or refs not resolved, load balancer pending, no running or ready pods). Needs
  the `services`, `ingresses`, `httproutes`/`grpcroutes` and `gateways` rules in
  `tiphys-actions`; without them the call is forbidden (the Gateway lookup only
  loses the details).

- **Logs**: `TailLines` in {0, 100, 1000, 10000} (0 = whole log, for downloads),
  `SinceSeconds` in {0, 300, 3600, 86400}. The container must exist in the pod.
- **Exec**: SPDY, TTY, command is only the allow-listed shell. Output must be
  drained by the caller until `Done`. `Err()` is nil after `Close()`.
- **Debug** (needs `WithDebugImage`, else `ErrDebugUnavailable`; also refused
  while the cluster's last privilege check did not find the
  `tiphys-debug-only` VAP): after the checks of Exec, adds one ephemeral
  container `tiphys-debug-<suffix>` to the pod through
  `pods/ephemeralcontainers` (`update`, one retry on conflict): the image,
  `sleep <lifetime>` (1 min to 25 h), `targetContainerName` = the chosen
  regular container, nothing else. It polls the pod until the container runs
  (at most 45 s, so the WebSocket upgrade stays under ingress timeouts) and then
  execs the shell in it. Admission or Pod Security refusals, image pull
  failures and the timeout return `*DebugError` with the cluster's message.
  Ephemeral containers cannot be removed: the container stops when its sleep
  ends and stays in the pod spec until the pod is replaced.
- **Restart** patches only the restart field. **WatchRollout** polls every 2 s
  and emits on change. At the deadline it sends one last status with message
  "deadline exceeded" (neither Complete nor Failed); a channel that closes
  without Complete or Failed means timed out or cancelled. Deadline 0 means the
  Deployment's `progressDeadlineSeconds`, else 600 s. A Rollout counts as done
  only when `status.restartedAt >= spec.restartAt` and the phase is Healthy.
- **RunCronJob** builds the Job from `spec.jobTemplate` only. **WatchJob** only
  watches Jobs controlled by a CronJob and reports it in `JobInfo.CronJob`; the
  caller must check that name against the grant, because the port does not pass
  the CronJob name.
- **Errors**: API NotFound is returned wrapping `application.ErrNotFound`;
  other API errors are wrapped and logged once at Error (Forbidden, conflicts
  and cancellations at Debug).

## Privilege check and admission mode

`Probe`, `ProbeKubeconfig` and the registry (at load and, if the cluster is
unreachable, every 30 s until it answers) run SelfSubjectAccessReviews as the
connector, without impersonation. Required: impersonate user `tiphys:user`.
Must be denied: get Secrets in `kube-system`, create Pods in all namespaces,
`kube-system` and `default`, update `pods/ephemeralcontainers`, impersonate user
`system:admin`, impersonate group `system:masters`. Any failure fills `Violations`, sets `PrivilegeOK=false` and
marks the cluster unusable; `SetUsable(true)` cannot override that. The
portal's periodic `Probe` (every 5 minutes) re-evaluates, so a rotated
credential recovers without restart.

Admission mode: ValidatingAdmissionPolicy named `tiphys-*` -> `vap`, else Kyverno
ClusterPolicy named `tiphys-*` -> `kyverno`, else `none` (also when the read is
forbidden or the API is not served). `DebugReady` is true when the VAP
`tiphys-debug-only` exists (there is no Kyverno version).

Pasted kubeconfigs are accepted only with an https server, inline CA (or the
system roots) and an inline bearer token. Rejected (`ErrKubeconfigRejected`,
message names the reason, never the token): exec and auth-provider plugins,
`insecure-skip-tls-verify`, `tokenFile`, file paths for the CA, client
certificates, basic auth, `proxy-url`, impersonation fields.

## Credential stores

- `SecretStore` is read-only: any Secret in the portal namespace annotated
  `tiphys/cluster: <name>` is the credential of cluster `<name>`, with the
  kubeconfig in its `kubeconfig` key (the Secret's own name is free). Annotations
  cannot be filtered server-side, so the informer sees every Secret of the
  namespace and filters locally; an update that changes or removes the
  annotation also reports the old name. Several Secrets for one name: the first
  by name wins, with a warning. The store is strictly read-only: `Put` and
  `Delete` always return `ErrReadOnly` and the portal needs only get, list and
  watch on Secrets; whoever runs the cluster manages them. `CREDENTIAL_NAMESPACE`
  must be set outside a cluster (the namespace is never "all").
- `DirStore`: `<dir>/<name>.kubeconfig` (mode 0600, atomic writes), polled
  every 2 s; the files define the clusters (local development).
- Cluster names must be DNS-1123 and at most 63 characters.
- `ClusterService` registers a cluster row for every credential it sees
  (watch plus a 30 s sync) and keeps the row if the credential goes away.

## Tests

Unit tests (fake clientset, httptest API server that records the Impersonate-*
headers): `go test ./internal/infra/kubeaccess/... ./internal/infra/credentials/...`.

Integration tests against a real cluster are skipped unless
`TIPHYS_TEST_KUBECONFIG` is set. With kind (Kubernetes 1.30 or newer):

```sh
kind create cluster --name tiphys-e2e --kubeconfig /tmp/admin.kubeconfig
export KUBECONFIG=/tmp/admin.kubeconfig
# optional, for the Rollout restart test
kubectl create -f https://raw.githubusercontent.com/argoproj/argo-rollouts/master/manifests/crds/rollout-crd.yaml
kubectl create ns argo-rollouts && kubectl apply -n argo-rollouts --server-side -f https://github.com/argoproj/argo-rollouts/releases/latest/download/install.yaml
helm install tiphys-connector deploy/chart/tiphys-connector -n tiphys-connector --create-namespace
kubectl apply -f internal/infra/kubeaccess/testdata/e2e-fixtures.yaml
# connector kubeconfig, see deploy/chart/README.md ("Build the kubeconfig"), as /tmp/connector.kubeconfig

TIPHYS_TEST_KUBECONFIG=/tmp/connector.kubeconfig \
TIPHYS_TEST_ADMIN_KUBECONFIG=/tmp/admin.kubeconfig \
  go test -count=1 -v -run E2E ./internal/infra/kubeaccess/
```

They cover logs, exec (with resize), restart of Deployment, StatefulSet,
DaemonSet and Rollout with progress, RunCronJob (owner, annotations, Forbid),
and the defense-in-depth criteria called directly as `tiphys:user`: image change
and other edits rejected by admission, Job without CronJob owner rejected,
Secrets forbidden, the connector unable to impersonate anyone else, protected
namespaces rejecting writes and exec, and the privilege check flagging a
cluster-admin token. Set `TIPHYS_TEST_ADMISSION=kyverno` after installing the
chart with `admission.mode=kyverno`.

Debug: install the chart with `--set debug.enabled=true --set debug.image=<repo@sha256:...>`
(any image with `sleep` and `/bin/sh`, e.g. busybox pinned by digest) and set
`TIPHYS_TEST_DEBUG_IMAGE` to the same value. `TestE2EDebug` opens a debug shell
and sees the target's processes; `TestE2EAdmissionAllowsOnlyLockedDownDebugContainers`
adds containers directly as `tiphys:user` and expects `tiphys-debug-only` to
reject every field beyond the portal's. Without the variable they skip.
