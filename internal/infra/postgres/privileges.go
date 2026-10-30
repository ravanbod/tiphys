package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// applyGrants gives the application and retention roles their privileges. It
// runs after every migration run (idempotent) so tables added by later
// migrations are covered, and it skips roles that do not exist: creating
// roles needs superuser and passwords, so that lives in deploy/sql/roles.sql.
//
//   - app role: normal DML everywhere, but only INSERT and SELECT on
//     audit_events (append-only), and nothing on goose's version table.
//   - retention role: SELECT and DELETE on audit_events only.
func applyGrants(ctx context.Context, conn *pgx.Conn, r Roles) error {
	if r.App == r.Retention {
		return errors.New("postgres: app and retention roles must differ")
	}
	exists := func(role string) (bool, error) {
		var ok bool
		err := conn.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = $1)`, role).Scan(&ok)
		return ok, err
	}

	if ok, err := exists(r.App); err != nil {
		return fmt.Errorf("postgres: grants: %w", err)
	} else if ok {
		q := pgx.Identifier{r.App}.Sanitize()
		stmts := []string{
			"GRANT USAGE ON SCHEMA public TO " + q,
			"GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO " + q,
			"GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO " + q,
			"REVOKE ALL ON TABLE audit_events FROM " + q,
			"GRANT SELECT, INSERT ON TABLE audit_events TO " + q,
			"REVOKE ALL ON TABLE goose_db_version FROM " + q,
		}
		if err := execAll(ctx, conn, stmts); err != nil {
			return err
		}
	}

	if ok, err := exists(r.Retention); err != nil {
		return fmt.Errorf("postgres: grants: %w", err)
	} else if ok {
		q := pgx.Identifier{r.Retention}.Sanitize()
		stmts := []string{
			"REVOKE ALL ON ALL TABLES IN SCHEMA public FROM " + q,
			"GRANT USAGE ON SCHEMA public TO " + q,
			"GRANT SELECT, DELETE ON TABLE audit_events TO " + q,
		}
		if err := execAll(ctx, conn, stmts); err != nil {
			return err
		}
	}
	return nil
}

func execAll(ctx context.Context, conn *pgx.Conn, stmts []string) error {
	for _, s := range stmts {
		if _, err := conn.Exec(ctx, s); err != nil {
			return fmt.Errorf("postgres: grants: %q: %w", s, err)
		}
	}
	return nil
}
