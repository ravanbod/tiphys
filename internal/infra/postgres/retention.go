package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"tiphys/internal/application"
)

// AuditRetention deletes audit rows past retention. It connects as the
// retention role, the only role besides the owner that may DELETE from
// audit_events.
type AuditRetention struct {
	pool *pgxpool.Pool
}

var _ application.AuditRetention = (*AuditRetention)(nil)

// NewAuditRetention opens a small pool for the retention role's dsn.
func NewAuditRetention(ctx context.Context, dsn string) (*AuditRetention, error) {
	pc, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("postgres: retention: parse dsn: %w", err)
	}
	pc.MaxConns = 2
	pool, err := pgxpool.NewWithConfig(ctx, pc)
	if err != nil {
		return nil, fmt.Errorf("postgres: retention: open pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("postgres: retention: ping: %w", err)
	}
	return &AuditRetention{pool: pool}, nil
}

// Close releases the pool.
func (r *AuditRetention) Close() { r.pool.Close() }

// DeleteAuditBefore deletes events with at < before and returns how many.
func (r *AuditRetention) DeleteAuditBefore(ctx context.Context, before time.Time) (int64, error) {
	tag, err := r.pool.Exec(ctx, `DELETE FROM audit_events WHERE at < $1`, before)
	if err != nil {
		return 0, fmt.Errorf("delete audit events: %w", err)
	}
	return tag.RowsAffected(), nil
}
