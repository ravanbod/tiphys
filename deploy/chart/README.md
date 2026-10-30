# tiphys-connector chart

Per-cluster part of Tiphys. It contains no workloads: only RBAC, the connector
ServiceAccount with its token Secret, and the admission policies that cap what
the portal's identity can do.

| Object | Purpose |
| --- | --- |
| ServiceAccount `tiphys-connector`, Secret `tiphys-connector-token` (both in the release namespace, `tiphys-connector`) | The identity whose token goes into the portal's kubeconfig |
| ClusterRole + binding `tiphys-connector-impersonator` | The connector may impersonate only user `tiphys:user`, group `tiphys:users` and the two extras |
| ClusterRole + binding `tiphys-actions` | What `tiphys:users` may do (bound to the group, not to the connector); the full list is in the spec's RBAC section. Besides workloads, pods, events and logs it reads `namespaces` (names for the pickers, without Argo CD) and, for the namespace page's traffic graph, `services`, `ingresses`, Gateway API `httproutes`/`grpcroutes` and `get` on `gateways`. After upgrading the portal, run `helm upgrade` on each cluster: until then the portal falls back to Argo CD's namespaces, and the traffic graph shows an error |
| ClusterRole + binding `tiphys-connector-admission-reader` | get/list `validatingadmissionpolicies` and Kyverno `clusterpolicies`, so the portal can show the cluster's admission mode. It reads policy names only and grants nothing on workloads or Secrets |
| `tiphys-restart-only`, `tiphys-jobs-from-cronjobs`, `tiphys-protected-namespaces` | ValidatingAdmissionPolicy + binding (`admission.mode=vap`), or Kyverno ClusterPolicy (`kyverno`) |
| `tiphys-debug-only` (only with `debug.enabled`) | ValidatingAdmissionPolicy + binding; there is no Kyverno version, so debug needs `admission.mode=vap`. With it, `tiphys-actions` also gets `update` on `pods/ephemeralcontainers` (see Debug containers) |

## Values

| Value | Default | Meaning |
| --- | --- | --- |
| `serviceAccountName` | `tiphys-connector` | |
| `extraDomain` | `portal.example.com` | Domain of the impersonation extras; must equal the portal's `KUBE_EXTRA_DOMAIN` |
| `protectedNamespaces` | `kube-system, kube-public, kube-node-lease, tiphys` | No write or exec by `tiphys:user` there (`tiphys` is the portal's namespace). Keep equal to the portal's protected list |
| `admission.mode` | `vap` | `vap` (Kubernetes 1.30+), `kyverno` (older clusters), `none` |
| `admission.validationActions` | `[Deny]` | VAP binding actions. Use `[Warn]` or `[Audit]` for a dry run |
| `admission.kyvernoAction` | `Enforce` | Kyverno `validationFailureAction` |
| `debug.enabled` | `false` | Debug containers; needs `admission.mode=vap` (the render fails otherwise) |
| `debug.image` | `""` | Debug image pinned by digest (`repo@sha256:<64 hex>`); must equal the portal's `DEBUG_IMAGE` |
| `debug.maxContainersPerPod` | `20` | Ephemeral containers a pod may hold; then debug on it is refused until the pod is replaced |
| `debug.maxLifetimeSeconds` | `90000` | Largest `sleep` allowed; the portal asks for its max exec session length plus one minute |

The protected list is rendered inline into the policy (a CEL variable), not into
a parameter resource: nothing else has to exist for the policy to work, and the
list changes together with a chart release. The portal enforces the same list
itself, reads included.

## Debug containers

With `debug.enabled`, `tiphys:user` may add ephemeral containers to pods, the
way `kubectl debug --target` does. That right alone would be an escalation (an
ephemeral container can take `envFrom` a Secret, run privileged, mount the
pod's volumes or any image), so `tiphys-debug-only` admits an update of
`pods/ephemeralcontainers` by `tiphys:user` only when it adds exactly one
container that:

- is named `tiphys-debug-*` and runs `debug.image`;
- runs `sleep <seconds>` (at most `debug.maxLifetimeSeconds`) and nothing else;
- targets a container of the pod;
- sets no other field: no `env`/`envFrom`, volume mounts, ports, probes,
  `securityContext`, `stdin`/`tty` or resources (only what the API server
  defaults is accepted).

The pod may hold at most `debug.maxContainersPerPod` ephemeral containers.
Protected namespaces are refused by `tiphys-protected-namespaces` as for every
other write. The container runs with the runtime's default capabilities and the pod's
security context: in a pod with `runAsUser` it runs as that user (tools that
need root, such as tcpdump, then fail), and namespaces with the `restricted`
Pod Security level or pods with `runAsNonRoot: true` and no `runAsUser` refuse a
root image such as netshoot; the user sees the cluster's message. The portal offers debug on a cluster only when its
privilege check finds `tiphys-debug-only`.

## Install

```sh
helm upgrade --install tiphys-connector deploy/chart/tiphys-connector \
  -n tiphys-connector --create-namespace \
  --set extraDomain=portal.mycompany.example \
  --set-json 'protectedNamespaces=["kube-system","kube-public","kube-node-lease","tiphys","argocd"]'
```

### Build the kubeconfig for the portal

```sh
NS=tiphys-connector
TOKEN=$(kubectl -n $NS get secret tiphys-connector-token -o jsonpath='{.data.token}' | base64 -d)
CA=$(kubectl -n $NS get secret tiphys-connector-token -o jsonpath='{.data.ca\.crt}')
cat > cluster.kubeconfig <<KC
apiVersion: v1
kind: Config
clusters:
- name: target
  cluster: {server: https://API_SERVER:6443, certificate-authority-data: $CA}
users:
- name: tiphys-connector
  user: {token: $TOKEN}
contexts:
- name: c
  context: {cluster: target, user: tiphys-connector}
current-context: c
KC
```

Put it in a Secret in the portal's namespace, annotated `tiphys/cluster: <cluster name>`,
with the kubeconfig in the `kubeconfig` key. The portal only watches and reads those Secrets; you
manage them (for local testing it can also take a pasted kubeconfig, see the top-level README):

```sh
kubectl -n tiphys-portal create secret generic staging-1-kubeconfig --from-file=kubeconfig=cluster.kubeconfig
kubectl -n tiphys-portal annotate secret staging-1-kubeconfig tiphys/cluster=staging-1 tiphys/environment=staging
```

The kubeconfig must hold an inline
token and CA only: exec/auth-provider plugins, `insecure-skip-tls-verify`,
token files, client certificates and `proxy-url` are rejected. Delete the file
afterwards.

## Deploying to every cluster with Argo CD

```yaml
apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: tiphys-connector
  namespace: argocd
spec:
  goTemplate: true
  goTemplateOptions: ["missingkey=error"]
  generators:
    - clusters:
        selector:
          matchExpressions:
            - {key: tiphys.example.com/exclude, operator: DoesNotExist}
  template:
    metadata:
      name: 'tiphys-connector-{{ .name }}'
    spec:
      project: platform
      source:
        repoURL: https://git.example.com/platform/tiphys.git
        targetRevision: main
        path: deploy/chart/tiphys-connector
        helm:
          valuesObject:
            extraDomain: portal.mycompany.example
            admission:
              mode: vap               # kyverno for clusters older than 1.30
            protectedNamespaces: [kube-system, kube-public, kube-node-lease, tiphys, argocd]
      destination:
        server: '{{ .server }}'
        namespace: tiphys-connector
      syncPolicy:
        automated: {prune: true, selfHeal: true}
        syncOptions: [CreateNamespace=true, ServerSideApply=true]
      ignoreDifferences:
        - kind: Secret
          name: tiphys-connector-token
          namespace: tiphys-connector
          jsonPointers: [/data]       # filled in by the token controller
```

To choose the admission mode per cluster, label the cluster Secret in Argo CD
(for example `tiphys.example.com/admission=kyverno`) and use
`{{ index .metadata.labels "tiphys.example.com/admission" }}`, or run two
ApplicationSets with different selectors.

## Notes and limits

- **Kubernetes versions.** The VAP policies use CEL optional types
  (`a.?b.orValue(x)`) and `variables`. They were tested on Kubernetes 1.37 and
  1.30 (see `docs/kubeaccess.md`). `object` and `oldObject` are dynamic in
  VAP, so every key of `spec` is compared, not a fixed list.
- **Restart-only** also pins the workload's labels, finalizers, ownerReferences
  and annotations (except the DaemonSet generation annotation that the API
  server maintains itself), so a patch through the portal cannot touch them.
- **Kyverno** needs a version whose `ClusterPolicy` supports JMESPath
  `parse_json` and `items` (tested with 1.19.1). Kyverno's default
  `resourceFilters` exclude `kube-system`, `kube-public` and `kube-node-lease`:
  in Kyverno mode the protected-namespaces rule does not fire there unless you
  remove those filters from Kyverno's ConfigMap. The portal's own check and the
  RBAC are unaffected.
- **Rollouts.** The `argoproj.io` rules only matter where the Rollout CRD is
  installed; elsewhere they never match.
- **Token rotation.** Create a second token Secret (same annotation), replace
  the credential in the portal, delete the old Secret.
- The ServiceAccount token Secret is long-lived by design (the portal cannot
  refresh a bound token). Treat it like a password; it can only impersonate
  `tiphys:user`.
