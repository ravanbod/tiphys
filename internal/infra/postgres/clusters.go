package postgres

import (
	"context"

	"github.com/jackc/pgx/v5"

	"tiphys/internal/application"
	"tiphys/internal/domain"
)

type clusterRepo struct{ q dbtx }

var _ application.ClusterRepository = clusterRepo{}

const clusterCols = `id, name, environment, kubeconfig_secret, kubernetes_version, last_check_at,
	reachable, privilege_check_ok, admission_mode, created_at, updated_at, updated_by`

func scanCluster(row pgx.CollectableRow) (domain.Cluster, error) {
	var c domain.Cluster
	var mode string
	err := row.Scan(&c.ID, &c.Name, &c.Environment, &c.KubeconfigSecret, &c.KubernetesVersion,
		&c.LastCheckAt, &c.Reachable, &c.PrivilegeCheckOK, &mode, &c.CreatedAt, &c.UpdatedAt, &c.UpdatedBy)
	c.AdmissionMode = domain.AdmissionMode(mode)
	return c, err
}

func (r clusterRepo) Get(ctx context.Context, name string) (domain.Cluster, error) {
	rows, err := r.q.Query(ctx, `SELECT `+clusterCols+` FROM clusters WHERE name = $1`, name)
	if err != nil {
		return domain.Cluster{}, mapErr(err, "get cluster")
	}
	c, err := pgx.CollectExactlyOneRow(rows, scanCluster)
	return c, mapErr(err, "get cluster")
}

func (r clusterRepo) List(ctx context.Context) ([]domain.Cluster, error) {
	rows, err := r.q.Query(ctx, `SELECT `+clusterCols+` FROM clusters ORDER BY name`)
	if err != nil {
		return nil, mapErr(err, "list clusters")
	}
	cs, err := pgx.CollectRows(rows, scanCluster)
	return cs, mapErr(err, "list clusters")
}

func (r clusterRepo) Create(ctx context.Context, c domain.Cluster) (domain.Cluster, error) {
	if c.AdmissionMode == "" {
		c.AdmissionMode = domain.AdmissionNone
	}
	rows, err := r.q.Query(ctx, `
		INSERT INTO clusters (name, environment, kubeconfig_secret, kubernetes_version, last_check_at,
			reachable, privilege_check_ok, admission_mode, updated_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		RETURNING `+clusterCols,
		c.Name, c.Environment, c.KubeconfigSecret, c.KubernetesVersion, c.LastCheckAt,
		c.Reachable, c.PrivilegeCheckOK, string(c.AdmissionMode), c.UpdatedBy)
	if err != nil {
		return domain.Cluster{}, mapErr(err, "create cluster")
	}
	created, err := pgx.CollectExactlyOneRow(rows, scanCluster)
	return created, mapErr(err, "create cluster")
}

// Update writes the admin-editable fields and the health fields. It keeps
// the previous updated_by when the caller leaves it empty.
func (r clusterRepo) Update(ctx context.Context, c domain.Cluster) error {
	tag, err := r.q.Exec(ctx, `
		UPDATE clusters SET environment = $2, kubeconfig_secret = $3, kubernetes_version = $4,
			last_check_at = $5, reachable = $6, privilege_check_ok = $7, admission_mode = $8,
			updated_at = now(), updated_by = COALESCE(NULLIF($9, ''), updated_by)
		WHERE name = $1`,
		c.Name, c.Environment, c.KubeconfigSecret, c.KubernetesVersion, c.LastCheckAt,
		c.Reachable, c.PrivilegeCheckOK, string(c.AdmissionMode), c.UpdatedBy)
	return affected(tag, err, "update cluster")
}

// UpdateHealth writes only the monitor's columns, leaving environment, secret
// and the audit columns (updated_at/by, which track admin edits) untouched.
func (r clusterRepo) UpdateHealth(ctx context.Context, name string, h application.ClusterHealth) error {
	var checked any
	if !h.LastCheckAt.IsZero() {
		checked = h.LastCheckAt
	}
	tag, err := r.q.Exec(ctx, `
		UPDATE clusters SET kubernetes_version = $2, last_check_at = $3, reachable = $4,
			privilege_check_ok = $5, admission_mode = $6
		WHERE name = $1`,
		name, h.KubernetesVersion, checked, h.Reachable, h.PrivilegeCheckOK, string(h.AdmissionMode))
	return affected(tag, err, "update cluster health")
}

// Delete removes the cluster and its environment overrides. It fails with
// ErrConflict while grants still reference the cluster.
func (r clusterRepo) Delete(ctx context.Context, name string) error {
	tag, err := r.q.Exec(ctx, `DELETE FROM clusters WHERE name = $1`, name)
	return affected(tag, err, "delete cluster")
}

func (r clusterRepo) ListEnvironmentOverrides(ctx context.Context) ([]domain.EnvironmentOverride, error) {
	rows, err := r.q.Query(ctx, overrideSelect+` ORDER BY o.id`)
	if err != nil {
		return nil, mapErr(err, "list environment overrides")
	}
	os, err := pgx.CollectRows(rows, scanOverride)
	return os, mapErr(err, "list environment overrides")
}

const overrideSelect = `
	SELECT o.id, o.kind, COALESCE(c.name, ''), COALESCE(o.argo_project, ''), COALESCE(o.namespace, ''),
		o.environment, o.created_at
	FROM environment_overrides o LEFT JOIN clusters c ON c.id = o.cluster_id`

func scanOverride(row pgx.CollectableRow) (domain.EnvironmentOverride, error) {
	var o domain.EnvironmentOverride
	var kind string
	err := row.Scan(&o.ID, &kind, &o.Cluster, &o.ArgoProject, &o.Namespace, &o.Environment, &o.CreatedAt)
	o.Kind = domain.EnvironmentOverrideKind(kind)
	return o, err
}

// CreateEnvironmentOverride fails with ErrConflict for a second override of
// the same (cluster, namespace) or Argo project, and with ErrNotFound for an
// unknown cluster.
func (r clusterRepo) CreateEnvironmentOverride(ctx context.Context, o domain.EnvironmentOverride) (domain.EnvironmentOverride, error) {
	var cid any
	if o.Cluster != "" {
		id, err := clusterID(ctx, r.q, o.Cluster)
		if err != nil {
			return domain.EnvironmentOverride{}, err
		}
		cid = id
	}
	var id int64
	err := r.q.QueryRow(ctx, `
		INSERT INTO environment_overrides (kind, cluster_id, argo_project, namespace, environment)
		VALUES ($1, $2, $3, $4, $5) RETURNING id`,
		string(o.Kind), cid, nullStr(o.ArgoProject), nullStr(o.Namespace), o.Environment).Scan(&id)
	if err != nil {
		return domain.EnvironmentOverride{}, mapErr(err, "create environment override")
	}
	rows, err := r.q.Query(ctx, overrideSelect+` WHERE o.id = $1`, id)
	if err != nil {
		return domain.EnvironmentOverride{}, mapErr(err, "create environment override")
	}
	created, err := pgx.CollectExactlyOneRow(rows, scanOverride)
	return created, mapErr(err, "create environment override")
}

func (r clusterRepo) DeleteEnvironmentOverride(ctx context.Context, id int64) error {
	tag, err := r.q.Exec(ctx, `DELETE FROM environment_overrides WHERE id = $1`, id)
	return affected(tag, err, "delete environment override")
}

func (r clusterRepo) ListProtectedNamespaces(ctx context.Context) ([]string, error) {
	rows, err := r.q.Query(ctx, `SELECT namespace FROM protected_namespaces ORDER BY namespace`)
	if err != nil {
		return nil, mapErr(err, "list protected namespaces")
	}
	ns, err := pgx.CollectRows(rows, pgx.RowTo[string])
	return ns, mapErr(err, "list protected namespaces")
}

// AddProtectedNamespace fails with ErrConflict if the namespace is listed.
func (r clusterRepo) AddProtectedNamespace(ctx context.Context, ns string) error {
	_, err := r.q.Exec(ctx, `INSERT INTO protected_namespaces (namespace) VALUES ($1)`, ns)
	return mapErr(err, "add protected namespace")
}

func (r clusterRepo) RemoveProtectedNamespace(ctx context.Context, ns string) error {
	tag, err := r.q.Exec(ctx, `DELETE FROM protected_namespaces WHERE namespace = $1`, ns)
	return affected(tag, err, "remove protected namespace")
}
