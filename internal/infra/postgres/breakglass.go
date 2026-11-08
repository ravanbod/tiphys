package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"tiphys/internal/application"
	"tiphys/internal/domain"
)

type breakGlassRepo struct{ q dbtx }

var _ application.BreakGlassRepository = breakGlassRepo{}

const breakGlassSelect = `
	SELECT b.id, b.user_id, u.username, b.reason, b.started_at, b.expires_at, b.ended_at, b.ended_by
	FROM break_glass b JOIN users u ON u.id = b.user_id`

func scanBreakGlass(row pgx.CollectableRow) (domain.BreakGlass, error) {
	var b domain.BreakGlass
	err := row.Scan(&b.ID, &b.UserID, &b.Username, &b.Reason, &b.StartedAt, &b.ExpiresAt, &b.EndedAt, &b.EndedBy)
	return b, err
}

func (r breakGlassRepo) Get(ctx context.Context, id int64) (domain.BreakGlass, error) {
	rows, err := r.q.Query(ctx, breakGlassSelect+` WHERE b.id = $1`, id)
	if err != nil {
		return domain.BreakGlass{}, mapErr(err, "get break-glass")
	}
	b, err := pgx.CollectExactlyOneRow(rows, scanBreakGlass)
	return b, mapErr(err, "get break-glass")
}

// Create uses the database clock when StartedAt is zero.
func (r breakGlassRepo) Create(ctx context.Context, b domain.BreakGlass) (domain.BreakGlass, error) {
	var started any
	if !b.StartedAt.IsZero() {
		started = b.StartedAt
	}
	var id int64
	err := r.q.QueryRow(ctx, `
		INSERT INTO break_glass (user_id, reason, started_at, expires_at)
		VALUES ($1, $2, COALESCE($3, now()), $4) RETURNING id`,
		b.UserID, b.Reason, started, b.ExpiresAt).Scan(&id)
	if err != nil {
		return domain.BreakGlass{}, mapErr(err, "create break-glass")
	}
	return r.Get(ctx, id)
}

// End fails with ErrNotFound for an unknown id and ErrConflict if it already ended.
func (r breakGlassRepo) End(ctx context.Context, id int64, endedBy string, at time.Time) error {
	tag, err := r.q.Exec(ctx, `
		UPDATE break_glass SET ended_at = $2, ended_by = $3 WHERE id = $1 AND ended_at IS NULL`,
		id, at, endedBy)
	if err != nil {
		return mapErr(err, "end break-glass")
	}
	if tag.RowsAffected() == 1 {
		return nil
	}
	if _, err := r.Get(ctx, id); err != nil {
		return err
	}
	return fmt.Errorf("end break-glass: already ended: %w", application.ErrConflict)
}

// CurrentForUser returns the user's break-glass active or scheduled at t
// (the earliest-starting one), or ErrNotFound.
func (r breakGlassRepo) CurrentForUser(ctx context.Context, userID int64, t time.Time) (domain.BreakGlass, error) {
	rows, err := r.q.Query(ctx, breakGlassSelect+`
		WHERE b.user_id = $1 AND b.expires_at > $2 AND (b.ended_at IS NULL OR b.ended_at > $2)
		ORDER BY b.started_at, b.id LIMIT 1`, userID, t)
	if err != nil {
		return domain.BreakGlass{}, mapErr(err, "current break-glass")
	}
	b, err := pgx.CollectExactlyOneRow(rows, scanBreakGlass)
	return b, mapErr(err, "current break-glass")
}

// ListCurrent returns break-glasses active or scheduled at t: not expired and
// not ended, whatever their start.
func (r breakGlassRepo) ListCurrent(ctx context.Context, t time.Time) ([]domain.BreakGlass, error) {
	rows, err := r.q.Query(ctx, breakGlassSelect+`
		WHERE b.expires_at > $1 AND (b.ended_at IS NULL OR b.ended_at > $1)
		ORDER BY b.started_at, b.id`, t)
	if err != nil {
		return nil, mapErr(err, "list current break-glass")
	}
	bs, err := pgx.CollectRows(rows, scanBreakGlass)
	return bs, mapErr(err, "list current break-glass")
}

func (r breakGlassRepo) List(ctx context.Context, limit int) ([]domain.BreakGlass, error) {
	limit = clampLimit(limit)
	rows, err := r.q.Query(ctx, breakGlassSelect+` ORDER BY b.started_at DESC, b.id DESC LIMIT $1`, limit)
	if err != nil {
		return nil, mapErr(err, "list break-glass")
	}
	bs, err := pgx.CollectRows(rows, scanBreakGlass)
	return bs, mapErr(err, "list break-glass")
}

const (
	defaultLimit = 50
	maxLimit     = 1000
)

// clampLimit applies the default page size and the hard cap.
func clampLimit(n int) int {
	switch {
	case n <= 0:
		return defaultLimit
	case n > maxLimit:
		return maxLimit
	}
	return n
}
