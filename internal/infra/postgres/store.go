// Package postgres is the PostgreSQL adapter for the persistence ports in
// package application. Schema migrations are embedded (see Migrate).
package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"tiphys/internal/application"
)

// Config configures the application's connection pool.
type Config struct {
	// DSN connects as the application role (tiphys_app), never the owner.
	DSN string
	// MaxConns caps the pool; zero keeps the pgx default.
	MaxConns int32
}

// Store implements application.Store on a pgx pool.
type Store struct {
	pool *pgxpool.Pool
}

var _ application.Store = (*Store)(nil)

// Open connects the pool and checks that the database answers.
func Open(ctx context.Context, cfg Config) (*Store, error) {
	pc, err := pgxpool.ParseConfig(cfg.DSN)
	if err != nil {
		return nil, fmt.Errorf("postgres: parse dsn: %w", err)
	}
	if cfg.MaxConns > 0 {
		pc.MaxConns = cfg.MaxConns
	}
	pool, err := pgxpool.NewWithConfig(ctx, pc)
	if err != nil {
		return nil, fmt.Errorf("postgres: open pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("postgres: ping: %w", err)
	}
	return &Store{pool: pool}, nil
}

// Close releases the pool.
func (s *Store) Close() { s.pool.Close() }

// Ping checks database connectivity.
func (s *Store) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }

// Repos returns repositories that run each call on its own pooled connection.
func (s *Store) Repos() application.Repositories { return newRepos(s.pool) }

// InTx runs fn with repositories bound to one transaction: commit when fn
// returns nil, rollback otherwise.
func (s *Store) InTx(ctx context.Context, fn func(application.Repositories) error) error {
	return withTx(ctx, s.pool, func(tx dbtx) error { return fn(newRepos(tx)) })
}

// dbtx is the part of *pgxpool.Pool and pgx.Tx the repositories use. Begin on
// a Tx opens a savepoint, so multi-statement repository methods work both
// standalone and inside Store.InTx.
type dbtx interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Begin(ctx context.Context) (pgx.Tx, error)
}

func withTx(ctx context.Context, d dbtx, fn func(dbtx) error) error {
	tx, err := d.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	// After a successful Commit this is a no-op returning ErrTxClosed.
	defer func() { _ = tx.Rollback(ctx) }()
	if err := fn(tx); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

func newRepos(d dbtx) application.Repositories {
	return application.Repositories{
		Users:        userRepo{d},
		Teams:        teamRepo{d},
		Grants:       grantRepo{d},
		Windows:      windowRepo{d},
		Holidays:     holidayRepo{d},
		Clusters:     clusterRepo{d},
		Requests:     requestRepo{d},
		BreakGlass:   breakGlassRepo{d},
		ExecSessions: execSessionRepo{d},
		Audit:        auditRepo{d},
		Settings:     settingsRepo{d},
		WebSessions:  webSessionRepo{d},
	}
}

// mapErr translates driver errors to the application's sentinel errors and
// adds context. Unique and foreign-key violations both mean "this change
// conflicts with existing rows".
func mapErr(err error, what string) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("%s: %w", what, application.ErrNotFound)
	}
	var pg *pgconn.PgError
	if errors.As(err, &pg) {
		switch pg.Code {
		case "23505", "23503": // unique_violation, foreign_key_violation
			return fmt.Errorf("%s: %w: %w", what, application.ErrConflict, err)
		}
	}
	return fmt.Errorf("%s: %w", what, err)
}

// affected turns "no row matched" into ErrNotFound.
func affected(tag pgconn.CommandTag, err error, what string) error {
	if err != nil {
		return mapErr(err, what)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%s: %w", what, application.ErrNotFound)
	}
	return nil
}

// nullStr stores an empty string as NULL.
func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// nullID stores a zero id as NULL.
func nullID(id int64) any {
	if id == 0 {
		return nil
	}
	return id
}
