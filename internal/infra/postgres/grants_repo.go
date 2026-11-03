package postgres

import (
	"context"

	"github.com/jackc/pgx/v5"

	"tiphys/internal/application"
	"tiphys/internal/domain"
)

type grantRepo struct{ q dbtx }

var _ application.GrantRepository = grantRepo{}

const grantSelect = `
	SELECT g.id, g.holder_kind, COALESCE(g.team_id, 0), COALESCE(g.user_id, 0), g.scope_kind,
		COALESCE(g.argo_project, ''), COALESCE(c.name, ''), COALESCE(g.namespace, ''),
		COALESCE(g.environment, ''), g.actions, g.expires_at, g.note, g.created_by, g.created_at, g.updated_at, g.updated_by
	FROM grants g LEFT JOIN clusters c ON c.id = g.cluster_id`

func scanGrant(row pgx.CollectableRow) (domain.Grant, error) {
	var g domain.Grant
	var holder, scope string
	var actions []string
	err := row.Scan(&g.ID, &holder, &g.TeamID, &g.UserID, &scope,
		&g.Scope.ArgoProject, &g.Scope.Cluster, &g.Scope.Namespace, &g.Scope.Environment,
		&actions, &g.ExpiresAt, &g.Note, &g.CreatedBy, &g.CreatedAt, &g.UpdatedAt, &g.UpdatedBy)
	g.HolderKind = domain.HolderKind(holder)
	g.Scope.Kind = domain.ScopeKind(scope)
	g.Actions = actionsFrom(actions)
	return g, err
}

// loadWorkloads attaches grant_workloads to workloads-kind grants.
func loadWorkloads(ctx context.Context, q dbtx, gs []domain.Grant) error {
	if len(gs) == 0 {
		return nil
	}
	ids := make([]int64, len(gs))
	idx := make(map[int64]int, len(gs))
	for i, g := range gs {
		ids[i] = g.ID
		idx[g.ID] = i
	}
	rows, err := q.Query(ctx, `
		SELECT grant_id, kind, name FROM grant_workloads
		WHERE grant_id = ANY($1) ORDER BY grant_id, kind, name`, ids)
	if err != nil {
		return mapErr(err, "load grant workloads")
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var kind, name string
		if err := rows.Scan(&id, &kind, &name); err != nil {
			return mapErr(err, "load grant workloads")
		}
		i := idx[id]
		gs[i].Scope.Workloads = append(gs[i].Scope.Workloads, domain.WorkloadRef{Kind: domain.WorkloadKind(kind), Name: name})
	}
	return mapErr(rows.Err(), "load grant workloads")
}

func (r grantRepo) Get(ctx context.Context, id int64) (domain.Grant, error) {
	rows, err := r.q.Query(ctx, grantSelect+` WHERE g.id = $1`, id)
	if err != nil {
		return domain.Grant{}, mapErr(err, "get grant")
	}
	g, err := pgx.CollectExactlyOneRow(rows, scanGrant)
	if err != nil {
		return domain.Grant{}, mapErr(err, "get grant")
	}
	gs := []domain.Grant{g}
	if err := loadWorkloads(ctx, r.q, gs); err != nil {
		return domain.Grant{}, err
	}
	return gs[0], nil
}

func (r grantRepo) List(ctx context.Context) ([]domain.Grant, error) {
	rows, err := r.q.Query(ctx, grantSelect+` ORDER BY g.id`)
	if err != nil {
		return nil, mapErr(err, "list grants")
	}
	gs, err := pgx.CollectRows(rows, scanGrant)
	if err != nil {
		return nil, mapErr(err, "list grants")
	}
	if err := loadWorkloads(ctx, r.q, gs); err != nil {
		return nil, err
	}
	return gs, nil
}

func holderCols(g domain.Grant) (team, user any) { return nullID(g.TeamID), nullID(g.UserID) }

func putWorkloads(ctx context.Context, q dbtx, grantID int64, ws []domain.WorkloadRef) error {
	for _, w := range ws {
		if _, err := q.Exec(ctx, `
			INSERT INTO grant_workloads (grant_id, kind, name) VALUES ($1, $2, $3)
			ON CONFLICT DO NOTHING`, grantID, string(w.Kind), w.Name); err != nil {
			return mapErr(err, "insert grant workload")
		}
	}
	return nil
}

func (r grantRepo) Create(ctx context.Context, g domain.Grant) (domain.Grant, error) {
	var id int64
	err := withTx(ctx, r.q, func(tx dbtx) error {
		sc, err := resolveScope(ctx, tx, g.Scope)
		if err != nil {
			return err
		}
		team, user := holderCols(g)
		err = tx.QueryRow(ctx, `
			INSERT INTO grants (holder_kind, team_id, user_id, scope_kind, argo_project, cluster_id,
				namespace, environment, actions, expires_at, note, created_by, updated_by)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $12)
			RETURNING id`,
			string(g.HolderKind), team, user, string(g.Scope.Kind), sc.argoProject, sc.clusterID,
			sc.namespace, nullStr(g.Scope.Environment), actionStrings(g.Actions), g.ExpiresAt, g.Note, g.CreatedBy).Scan(&id)
		if err != nil {
			return mapErr(err, "create grant")
		}
		return putWorkloads(ctx, tx, id, g.Scope.Workloads)
	})
	if err != nil {
		return domain.Grant{}, err
	}
	return r.Get(ctx, id)
}

func (r grantRepo) Update(ctx context.Context, g domain.Grant) error {
	return withTx(ctx, r.q, func(tx dbtx) error {
		sc, err := resolveScope(ctx, tx, g.Scope)
		if err != nil {
			return err
		}
		team, user := holderCols(g)
		tag, err := tx.Exec(ctx, `
			UPDATE grants SET holder_kind = $2, team_id = $3, user_id = $4, scope_kind = $5,
				argo_project = $6, cluster_id = $7, namespace = $8, environment = $9, actions = $10,
				expires_at = $11, note = $12, updated_at = now(), updated_by = $13
			WHERE id = $1`,
			g.ID, string(g.HolderKind), team, user, string(g.Scope.Kind), sc.argoProject, sc.clusterID,
			sc.namespace, nullStr(g.Scope.Environment), actionStrings(g.Actions), g.ExpiresAt, g.Note, g.UpdatedBy)
		if err := affected(tag, err, "update grant"); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM grant_workloads WHERE grant_id = $1`, g.ID); err != nil {
			return mapErr(err, "update grant workloads")
		}
		return putWorkloads(ctx, tx, g.ID, g.Scope.Workloads)
	})
}

func (r grantRepo) Delete(ctx context.Context, id int64) error {
	tag, err := r.q.Exec(ctx, `DELETE FROM grants WHERE id = $1`, id)
	return affected(tag, err, "delete grant")
}
