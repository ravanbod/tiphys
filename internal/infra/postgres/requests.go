package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"tiphys/internal/application"
	"tiphys/internal/domain"
)

type requestRepo struct{ q dbtx }

var _ application.AccessRequestRepository = requestRepo{}

const requestSelect = `
	SELECT r.id, r.user_id, u.username, r.scope_kind, COALESCE(r.argo_project, ''), COALESCE(c.name, ''),
		COALESCE(r.namespace, ''), r.workloads, r.actions, r.duration_hours, r.reason, r.status,
		COALESCE(r.decided_by, ''), r.decided_at, COALESCE(r.decision_note, ''), r.resulting_grant_id,
		r.created_at
	FROM access_requests r
	JOIN users u ON u.id = r.user_id
	LEFT JOIN clusters c ON c.id = r.cluster_id`

func scanRequest(row pgx.CollectableRow) (domain.AccessRequest, error) {
	var a domain.AccessRequest
	var scope, status string
	var actions []string
	var workloads []byte
	var duration *int32
	err := row.Scan(&a.ID, &a.UserID, &a.Username, &scope, &a.Scope.ArgoProject, &a.Scope.Cluster,
		&a.Scope.Namespace, &workloads, &actions, &duration, &a.Reason, &status,
		&a.DecidedBy, &a.DecidedAt, &a.DecisionNote, &a.ResultingGrantID, &a.CreatedAt)
	if err != nil {
		return a, err
	}
	a.Scope.Kind = domain.ScopeKind(scope)
	a.Status = domain.RequestStatus(status)
	a.Actions = actionsFrom(actions)
	if duration != nil {
		h := int(*duration)
		a.DurationHours = &h
	}
	if err := json.Unmarshal(workloads, &a.Scope.Workloads); err != nil {
		return a, fmt.Errorf("decode request workloads: %w", err)
	}
	if len(a.Scope.Workloads) == 0 {
		a.Scope.Workloads = nil
	}
	return a, nil
}

func (r requestRepo) Get(ctx context.Context, id int64) (domain.AccessRequest, error) {
	rows, err := r.q.Query(ctx, requestSelect+` WHERE r.id = $1`, id)
	if err != nil {
		return domain.AccessRequest{}, mapErr(err, "get access request")
	}
	a, err := pgx.CollectExactlyOneRow(rows, scanRequest)
	return a, mapErr(err, "get access request")
}

func (r requestRepo) List(ctx context.Context, userID int64, status domain.RequestStatus) ([]domain.AccessRequest, error) {
	rows, err := r.q.Query(ctx, requestSelect+`
		WHERE ($1::bigint = 0 OR r.user_id = $1) AND ($2::text = '' OR r.status = $2)
		ORDER BY r.id DESC`, userID, string(status))
	if err != nil {
		return nil, mapErr(err, "list access requests")
	}
	as, err := pgx.CollectRows(rows, scanRequest)
	return as, mapErr(err, "list access requests")
}

func (r requestRepo) Create(ctx context.Context, a domain.AccessRequest) (domain.AccessRequest, error) {
	ws := a.Scope.Workloads
	if ws == nil {
		ws = []domain.WorkloadRef{}
	}
	wj, err := json.Marshal(ws)
	if err != nil {
		return domain.AccessRequest{}, fmt.Errorf("create access request: %w", err)
	}
	var dur any
	if a.DurationHours != nil {
		dur = int32(*a.DurationHours)
	}
	var id int64
	err = withTx(ctx, r.q, func(tx dbtx) error {
		sc, err := resolveScope(ctx, tx, a.Scope)
		if err != nil {
			return err
		}
		err = tx.QueryRow(ctx, `
			INSERT INTO access_requests (user_id, scope_kind, argo_project, cluster_id, namespace,
				workloads, actions, duration_hours, reason)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
			RETURNING id`,
			a.UserID, string(a.Scope.Kind), sc.argoProject, sc.clusterID, sc.namespace,
			wj, actionStrings(a.Actions), dur, a.Reason).Scan(&id)
		return mapErr(err, "create access request")
	})
	if err != nil {
		return domain.AccessRequest{}, err
	}
	return r.Get(ctx, id)
}

// Decide only succeeds on a pending request (the UPDATE is conditional), so
// two admins deciding at once cannot both win.
func (r requestRepo) Decide(ctx context.Context, id int64, status domain.RequestStatus, decidedBy, note string, grantID *int64, at time.Time) error {
	switch status {
	case domain.RequestApproved, domain.RequestRejected, domain.RequestExpired:
	default:
		return fmt.Errorf("decide access request: invalid status %q", status)
	}
	if at.IsZero() {
		at = time.Now()
	}
	tag, err := r.q.Exec(ctx, `
		UPDATE access_requests SET status = $2, decided_by = $3, decision_note = $4,
			resulting_grant_id = $5, decided_at = $6
		WHERE id = $1 AND status = 'pending'`,
		id, string(status), decidedBy, note, grantID, at)
	if err != nil {
		return mapErr(err, "decide access request")
	}
	if tag.RowsAffected() == 1 {
		return nil
	}
	var exists bool
	if err := r.q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM access_requests WHERE id = $1)`, id).Scan(&exists); err != nil {
		return mapErr(err, "decide access request")
	}
	if !exists {
		return fmt.Errorf("decide access request: %w", application.ErrNotFound)
	}
	return fmt.Errorf("decide access request: request is not pending: %w", application.ErrConflict)
}

func (r requestRepo) ExpirePendingBefore(ctx context.Context, t time.Time) (int64, error) {
	tag, err := r.q.Exec(ctx, `
		UPDATE access_requests SET status = 'expired', decided_by = 'system', decided_at = now()
		WHERE status = 'pending' AND created_at < $1`, t)
	return tag.RowsAffected(), mapErr(err, "expire access requests")
}
