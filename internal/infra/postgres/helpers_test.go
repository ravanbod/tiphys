package postgres

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"tiphys/internal/application"
	"tiphys/internal/domain"
)

// The tests need a PostgreSQL superuser DSN in TIPHYS_TEST_DATABASE_URL (see
// deploy/sql/test-db.sh) and skip without it. TestMain migrates one template
// database; every test gets its own copy, so tests do not interfere.

const (
	testAppRole       = "tiphys_test_app"
	testRetentionRole = "tiphys_test_retention"
	testRolePassword  = "pw"
)

var (
	adminDSN   string
	templateDB string
	dbCounter  atomic.Int64
)

func testRoles() Roles { return Roles{App: testAppRole, Retention: testRetentionRole} }

func TestMain(m *testing.M) {
	adminDSN = os.Getenv("TIPHYS_TEST_DATABASE_URL")
	if adminDSN == "" {
		os.Exit(m.Run())
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, adminDSN)
	if err != nil {
		fmt.Fprintln(os.Stderr, "connect:", err)
		os.Exit(1)
	}
	for _, role := range []string{testAppRole, testRetentionRole} {
		// CREATE ROLE has no IF NOT EXISTS.
		_, err := admin.Exec(ctx, fmt.Sprintf(`DO $$ BEGIN
			IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = '%s') THEN
				CREATE ROLE %s LOGIN PASSWORD '%s';
			END IF; END $$`, role, role, testRolePassword))
		if err != nil {
			fmt.Fprintln(os.Stderr, "create role:", err)
			os.Exit(1)
		}
	}
	templateDB = fmt.Sprintf("tiphys_tpl_%d", os.Getpid())
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+templateDB); err != nil {
		fmt.Fprintln(os.Stderr, "create template:", err)
		os.Exit(1)
	}
	code := 1
	if err := Migrate(ctx, dsnFor(adminDSN, templateDB, "", ""), WithRoles(testRoles())); err != nil {
		fmt.Fprintln(os.Stderr, "migrate template:", err)
	} else {
		code = m.Run()
	}
	_, _ = admin.Exec(ctx, "DROP DATABASE IF EXISTS "+templateDB+" WITH (FORCE)")
	_ = admin.Close(ctx)
	os.Exit(code)
}

// dsnFor rewrites base to another database and, when user is set, another login.
func dsnFor(base, db, user, password string) string {
	u, err := url.Parse(base)
	if err != nil {
		panic(err)
	}
	u.Path = "/" + db
	if user != "" {
		u.User = url.UserPassword(user, password)
	}
	return u.String()
}

type fixture struct {
	t        *testing.T
	ctx      context.Context
	db       string
	ownerDSN string
	appDSN   string
	retDSN   string
	owner    *pgxpool.Pool // superuser connection to the test database
	store    *Store        // application role
	r        application.Repositories
}

// newFixture creates a fresh database from the migrated template.
func newFixture(t *testing.T) *fixture {
	t.Helper()
	if adminDSN == "" {
		t.Skip("TIPHYS_TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	name := fmt.Sprintf("tiphys_t_%d_%d", os.Getpid(), dbCounter.Add(1))
	admin, err := pgx.Connect(ctx, adminDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(ctx)
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+name+" TEMPLATE "+templateDB); err != nil {
		t.Fatal(err)
	}
	f := &fixture{t: t, ctx: ctx, db: name,
		ownerDSN: dsnFor(adminDSN, name, "", ""),
		appDSN:   dsnFor(adminDSN, name, testAppRole, testRolePassword),
		retDSN:   dsnFor(adminDSN, name, testRetentionRole, testRolePassword),
	}
	f.owner, err = pgxpool.New(ctx, f.ownerDSN)
	if err != nil {
		t.Fatal(err)
	}
	f.store, err = Open(ctx, Config{DSN: f.appDSN, MaxConns: 8})
	if err != nil {
		t.Fatal(err)
	}
	f.r = f.store.Repos()
	t.Cleanup(func() {
		f.store.Close()
		f.owner.Close()
		a, err := pgx.Connect(ctx, adminDSN)
		if err != nil {
			return
		}
		defer a.Close(ctx)
		_, _ = a.Exec(ctx, "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)")
	})
	return f
}

func (f *fixture) user(name string, role domain.Role) domain.User {
	f.t.Helper()
	u, err := f.r.Users.Create(f.ctx, domain.User{Username: name, DisplayName: name, Role: role})
	if err != nil {
		f.t.Fatal(err)
	}
	return u
}

func (f *fixture) team(name string) domain.Team {
	f.t.Helper()
	tm, err := f.r.Teams.Create(f.ctx, domain.Team{Name: name})
	if err != nil {
		f.t.Fatal(err)
	}
	return tm
}

func (f *fixture) cluster(name string) domain.Cluster {
	f.t.Helper()
	c, err := f.r.Clusters.Create(f.ctx, domain.Cluster{Name: name, Environment: "prod"})
	if err != nil {
		f.t.Fatal(err)
	}
	return c
}

func nsScope(cluster, ns string) domain.Scope {
	return domain.Scope{Kind: domain.ScopeNamespace, Cluster: cluster, Namespace: ns}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func isErr(t *testing.T, err, target error) {
	t.Helper()
	if !errors.Is(err, target) {
		t.Fatalf("got error %v, want %v", err, target)
	}
}

// isCheckViolation asserts a CHECK constraint failure (not mapped to a
// sentinel: it is invalid input, not a conflict).
func isCheckViolation(t *testing.T, err error) {
	t.Helper()
	var pg *pgconn.PgError
	if !errors.As(err, &pg) || pg.Code != "23514" {
		t.Fatalf("got error %v, want check violation", err)
	}
	if errors.Is(err, application.ErrConflict) {
		t.Fatalf("check violation must not map to ErrConflict: %v", err)
	}
}

func ptr[T any](v T) *T { return &v }

var t0 = time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
