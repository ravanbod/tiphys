package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"tiphys/internal/application"
	"tiphys/internal/domain"
)

type breakGlassRequestRepo struct{ q dbtx }

var _ application.BreakGlassRequestRepository = breakGlassRequestRepo{}

const breakGlassRequestSelect = `
	SELECT r.id, r.user_id, u.username, r.reason, r.minutes, r.starts_at, r.status, COALESCE(r.decided_by, ''),
		r.decided_at, COALESCE(r.decision_note, ''), r.approved_minutes, r.break_glass_id, r.created_at
	FROM break_glass_requests r JOIN users u ON u.id = r.user_id`

func scanBreakGlassRequest(row pgx.CollectableRow) (domain.BreakGlassRequest, error) {
	var b domain.BreakGlassRequest
	var status string
	var minutes int32
	var approved *int32
	err := row.Scan(&b.ID, &b.UserID, &b.Username, &b.Reason, &minutes, &b.StartsAt, &status, &b.DecidedBy,
		&b.DecidedAt, &b.DecisionNote, &approved, &b.BreakGlassID, &b.CreatedAt)
	if err != nil {
		return b, err
	}
	b.Minutes, b.Status = int(minutes), domain.RequestStatus(status)
	if approved != nil {
		m := int(*approved)
		b.ApprovedMinutes = &m
	}
	return b, nil
}

func (r breakGlassRequestRepo) Get(ctx context.Context, id int64) (domain.BreakGlassRequest, error) {
	rows, err := r.q.Query(ctx, breakGlassRequestSelect+` WHERE r.id = $1`, id)
	if err != nil {
		return domain.BreakGlassRequest{}, mapErr(err, "get break-glass request")
	}
	b, err := pgx.CollectExactlyOneRow(rows, scanBreakGlassRequest)
	return b, mapErr(err, "get break-glass request")
}

// Create fails with ErrConflict when the user already has a pending
// request. It uses the database clock when CreatedAt is zero.
func (r breakGlassRequestRepo) Create(ctx context.Context, b domain.BreakGlassRequest) (domain.BreakGlassRequest, error) {
	var created any
	if !b.CreatedAt.IsZero() {
		created = b.CreatedAt
	}
	var id int64
	err := r.q.QueryRow(ctx, `
		INSERT INTO break_glass_requests (user_id, reason, minutes, starts_at, created_at)
		VALUES ($1, $2, $3, $4, COALESCE($5, now())) RETURNING id`,
		b.UserID, b.Reason, int32(b.Minutes), b.StartsAt, created).Scan(&id)
	if err != nil {
		return domain.BreakGlassRequest{}, mapErr(err, "create break-glass request")
	}
	return r.Get(ctx, id)
}

func (r breakGlassRequestRepo) List(ctx context.Context, userID int64, status domain.RequestStatus, limit int) ([]domain.BreakGlassRequest, error) {
	rows, err := r.q.Query(ctx, breakGlassRequestSelect+`
		WHERE ($1::bigint = 0 OR r.user_id = $1) AND ($2::text = '' OR r.status = $2)
		ORDER BY r.id DESC LIMIT $3`, userID, string(status), clampLimit(limit))
	if err != nil {
		return nil, mapErr(err, "list break-glass requests")
	}
	bs, err := pgx.CollectRows(rows, scanBreakGlassRequest)
	return bs, mapErr(err, "list break-glass requests")
}

// Decide only succeeds on a pending request (the UPDATE is conditional), so
// two admins deciding at once cannot both win.
func (r breakGlassRequestRepo) Decide(ctx context.Context, id int64, status domain.RequestStatus, decidedBy, note string, approvedMinutes *int, breakGlassID *int64, at time.Time) error {
	switch status {
	case domain.RequestApproved, domain.RequestRejected, domain.RequestCancelled, domain.RequestExpired:
	default:
		return fmt.Errorf("decide break-glass request: invalid status %q", status)
	}
	var approved any
	if approvedMinutes != nil {
		approved = int32(*approvedMinutes)
	}
	tag, err := r.q.Exec(ctx, `
		UPDATE break_glass_requests SET status = $2, decided_by = $3, decision_note = $4,
			approved_minutes = $5, break_glass_id = $6, decided_at = $7
		WHERE id = $1 AND status = 'pending'`,
		id, string(status), decidedBy, note, approved, breakGlassID, at)
	if err != nil {
		return mapErr(err, "decide break-glass request")
	}
	if tag.RowsAffected() == 1 {
		return nil
	}
	if _, err := r.Get(ctx, id); err != nil {
		return err
	}
	return fmt.Errorf("decide break-glass request: request is not pending: %w", application.ErrConflict)
}

// ExpirePendingBefore expires pending requests whose created_at, or later
// starts_at, is before t.
func (r breakGlassRequestRepo) ExpirePendingBefore(ctx context.Context, t time.Time) (int64, error) {
	tag, err := r.q.Exec(ctx, `
		UPDATE break_glass_requests SET status = 'expired', decided_by = 'system', decided_at = now()
		WHERE status = 'pending' AND GREATEST(created_at, starts_at) < $1`, t)
	return tag.RowsAffected(), mapErr(err, "expire break-glass requests")
}
