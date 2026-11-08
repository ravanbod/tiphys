# tiphys-portal chart

The portal itself, installed once (usually in the cluster with Argo CD). The
per-cluster part is [tiphys-connector](../README.md). The main
[README](../../../README.md#install-in-a-cluster) walks through the install,
with CloudNativePG for the database.

```sh
helm install tiphys oci://registry-1.docker.io/behradrvb/tiphys-portal \
  --version <X.Y.Z> -n tiphys-portal -f values.yaml
```

| Object | Purpose |
| --- | --- |
| Deployment `tiphys-portal` | One replica (live streams are per pod), RollingUpdate with `maxSurge: 1`, read-only root filesystem, non-root, 60 s grace period so recordings are finalized |
| ConfigMap `tiphys-portal` | `env` plus the `*_FILE` paths of `secrets.files`; a change rolls the pod |
| ServiceAccount, Role + RoleBinding `tiphys-portal-credentials` | get/list/watch on Secrets in the release namespace only: the cluster Secrets (`tiphys/cluster: <name>`). The portal never writes Secrets |
| Service `tiphys-portal` | `http` (80 → 8080) and `metrics` (9090) |
| Ingress, ServiceMonitor | Optional |

The chart creates no Secrets: you create the two it reads (or let an
operator such as External Secrets do it).

## Values

| Value | Default | Meaning |
| --- | --- | --- |
| `image.repository` | `behradrvb/tiphys` | |
| `image.tag` | the chart's `appVersion` | A released chart's `appVersion` is its own version, so chart `X.Y.Z` runs image `X.Y.Z` |
| `image.digest` | `""` | `sha256:...`; wins over the tag |
| `env` | see `values.yaml` | Plain settings as environment variables, named as in `.env.example`; empty values are left out. `PUBLIC_URL` is required. The chart sets `PORT`, `METRICS_PORT`, `CREDENTIAL_STORE=secrets` and `CREDENTIAL_NAMESPACE` itself |
| `secrets.existingSecret` | `tiphys-portal-secrets` | Mounted at `/secrets/portal` |
| `secrets.files` | OIDC client secret, session keys, Argo CD token, MinIO keys | `*_FILE` setting → key in that Secret; `""` leaves the setting out (`LOKI_PASSWORD_FILE`, `LOKI_TOKEN_FILE`, `ARGO_CA_FILE` are off by default) |
| `database.existingSecret` | `tiphys-portal-database` | Secret with the DSNs |
| `database.appUrlKey` | `app-url` | `DATABASE_URL`, required |
| `database.retentionUrlKey` | `retention-url` | `DATABASE_RETENTION_URL`; a missing key turns audit retention deletes off |
| `database.migrateUrlKey` | `migrate-url` | `DATABASE_MIGRATE_URL` (owner role); when present the portal migrates at startup |
| `serviceAccount.name`, `.annotations` | `tiphys-portal`, `{}` | |
| `service.type`, `.port`, `.metricsPort` | `ClusterIP`, `80`, `9090` | |
| `ingress.enabled` | `false` | |
| `ingress.className` | `""` | |
| `ingress.host` | host of `env.PUBLIC_URL` | |
| `ingress.tlsSecretName` | `""` | No TLS section when empty |
| `ingress.annotations` | `{}` | Raise the proxy timeouts: shells, debug sessions and log streams are long-lived WebSockets |
| `serviceMonitor.enabled`, `.interval`, `.labels` | `false`, `30s`, `{}` | Prometheus Operator |
| `resources` | requests 100m / 128Mi, limit 512Mi | |
| `podAnnotations`, `podLabels`, `nodeSelector`, `tolerations`, `affinity`, `imagePullSecrets` | empty | |

`deploy/portal/portal.yaml` holds the same objects as plain manifests; keep
the two in step.
