package postgres

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"

	"github.com/jackc/pgx/v5"
	_ "github.com/jackc/pgx/v5/stdlib" // registers the "pgx" database/sql driver
	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/lock"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// Default database role names; see docs/database.md.
const (
	DefaultAppRole       = "tiphys_app"
	DefaultRetentionRole = "tiphys_retention"
)

// Roles names the application and retention roles that Migrate grants
// privileges to, when they exist.
type Roles struct {
	App       string
	Retention string
}

// MigrateOption customizes Migrate.
type MigrateOption func(*Roles)

// WithRoles overrides the role names; empty fields keep the defaults.
func WithRoles(r Roles) MigrateOption {
	return func(dst *Roles) {
		if r.App != "" {
			dst.App = r.App
		}
		if r.Retention != "" {
			dst.Retention = r.Retention
		}
	}
}

// Migrate applies the embedded migrations and then the privilege grants. dsn
// must connect as the owner (migration) role, not the application role. Safe
// to run concurrently from several replicas (advisory lock).
func Migrate(ctx context.Context, dsn string, opts ...MigrateOption) error {
	roles := Roles{App: DefaultAppRole, Retention: DefaultRetentionRole}
	for _, o := range opts {
		o(&roles)
	}

	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return fmt.Errorf("postgres: migrate: open: %w", err)
	}
	defer db.Close()

	sub, err := fs.Sub(migrationsFS, "migrations")
	if err != nil {
		return fmt.Errorf("postgres: migrate: %w", err)
	}
	locker, err := lock.NewPostgresSessionLocker()
	if err != nil {
		return fmt.Errorf("postgres: migrate: locker: %w", err)
	}
	p, err := goose.NewProvider(goose.DialectPostgres, db, sub, goose.WithSessionLocker(locker))
	if err != nil {
		return fmt.Errorf("postgres: migrate: provider: %w", err)
	}
	if _, err := p.Up(ctx); err != nil {
		return fmt.Errorf("postgres: migrate: up: %w", err)
	}

	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return fmt.Errorf("postgres: migrate: connect for grants: %w", err)
	}
	defer conn.Close(ctx)
	return applyGrants(ctx, conn, roles)
}
