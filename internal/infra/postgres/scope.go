package postgres

import (
	"context"
	"fmt"

	"tiphys/internal/domain"
)

// scopeCols is a domain.Scope in the column form grants and access requests
// share. Unset fields are NULL so the table CHECK constraints apply.
type scopeCols struct {
	argoProject any
	clusterID   any
	namespace   any
}

func resolveScope(ctx context.Context, q dbtx, s domain.Scope) (scopeCols, error) {
	c := scopeCols{argoProject: nullStr(s.ArgoProject), namespace: nullStr(s.Namespace)}
	if s.Cluster != "" {
		id, err := clusterID(ctx, q, s.Cluster)
		if err != nil {
			return scopeCols{}, err
		}
		c.clusterID = id
	}
	return c, nil
}

// clusterID resolves a cluster name; unknown names are ErrNotFound.
func clusterID(ctx context.Context, q dbtx, name string) (int64, error) {
	var id int64
	err := q.QueryRow(ctx, `SELECT id FROM clusters WHERE name = $1`, name).Scan(&id)
	if err != nil {
		return 0, mapErr(err, fmt.Sprintf("cluster %q", name))
	}
	return id, nil
}

func actionStrings(as []domain.Action) []string {
	out := make([]string, len(as))
	for i, a := range as {
		out[i] = string(a)
	}
	return out
}

func actionsFrom(ss []string) []domain.Action {
	out := make([]domain.Action, len(ss))
	for i, s := range ss {
		out[i] = domain.Action(s)
	}
	return out
}
