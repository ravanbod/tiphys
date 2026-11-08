package postgres

import (
	"errors"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"tiphys/internal/application"
	"tiphys/internal/domain"
)

func TestAuditInsert(t *testing.T) {
	f := newFixture(t)
	uid, bg := int64(7), int64(3)
	before := time.Now().Add(-time.Second)
	e, err := f.r.Audit.Insert(f.ctx, domain.AuditEvent{
		UserID: &uid, Username: "alice", EventType: domain.EventExecStart, Action: "exec",
		Cluster: "c1", Namespace: "pay", WorkloadKind: "Deployment", WorkloadName: "api", Pod: "api-1",
		Container: "app", Environment: "prod", Result: domain.ResultAllowed, Reason: "debugging",
		BreakGlassID: &bg, SessionID: "11111111-1111-1111-1111-111111111111",
		Details:  map[string]any{"shell": "/bin/sh", "nested": map[string]any{"n": 2.0}, "list": []any{"a", "b"}},
		SourceIP: "10.1.2.3", UserAgent: "curl",
	})
	must(t, err)
	if e.ID == 0 || e.At.Before(before) || e.At.After(time.Now().Add(time.Second)) {
		t.Fatalf("id/at not filled: %+v", e)
	}
	got, err := f.r.Audit.List(f.ctx, application.AuditFilter{})
	must(t, err)
	if len(got) != 1 || !reflect.DeepEqual(got[0], e) {
		t.Fatalf("round trip:\n got %+v\nwant %+v", got, e)
	}

	// A minimal event: optional fields read back empty, details nil.
	min, err := f.r.Audit.Insert(f.ctx, domain.AuditEvent{EventType: domain.EventLogin, Result: domain.ResultSucceeded, At: t0})
	must(t, err)
	if !min.At.Equal(t0) {
		t.Fatalf("explicit At ignored: %v", min.At)
	}
	got, _ = f.r.Audit.List(f.ctx, application.AuditFilter{EventType: domain.EventLogin})
	if len(got) != 1 || got[0].UserID != nil || got[0].BreakGlassID != nil || got[0].Details != nil || got[0].Cluster != "" {
		t.Fatalf("minimal event: %+v", got)
	}

	_, err = f.r.Audit.Insert(f.ctx, domain.AuditEvent{EventType: "x", Result: "maybe"})
	isCheckViolation(t, err)
	_, err = f.r.Audit.Insert(f.ctx, domain.AuditEvent{EventType: "x"})
	isCheckViolation(t, err)
}

func TestAuditList(t *testing.T) {
	f := newFixture(t)
	u1, u2, bg := int64(1), int64(2), int64(9)
	events := []domain.AuditEvent{
		{At: t0, UserID: &u1, Username: "Alice", EventType: domain.EventLogsView, Action: "logs", Cluster: "c1", Namespace: "pay", Environment: "prod", Result: domain.ResultAllowed, Pod: "api-7d9f", WorkloadName: "api"},
		{At: t0.Add(time.Hour), UserID: &u2, Username: "bob", EventType: domain.EventExecStart, Action: "exec", Cluster: "c1", Namespace: "pay", Environment: "prod", Result: domain.ResultSucceeded, SessionID: "s-1", BreakGlassID: &bg, Reason: "Disk 100% full_now"},
		{At: t0.Add(2 * time.Hour), UserID: &u1, Username: "Alice", EventType: domain.EventDecisionDenied, Action: "restart", Cluster: "c2", Namespace: "other", Environment: "stage", Result: domain.ResultDenied, Reason: "protected_namespace", WorkloadName: "worker"},
		{At: t0.Add(3 * time.Hour), UserID: &u2, Username: "bob", EventType: domain.EventLogin, Result: domain.ResultSucceeded},
	}
	ids := map[string]int64{}
	for _, e := range events {
		got, err := f.r.Audit.Insert(f.ctx, e)
		must(t, err)
		ids[got.EventType] = got.ID
	}
	list := func(flt application.AuditFilter) []int64 {
		es, err := f.r.Audit.List(f.ctx, flt)
		must(t, err)
		out := make([]int64, len(es))
		for i, e := range es {
			out[i] = e.ID
		}
		return out
	}
	check := func(name string, flt application.AuditFilter, want ...string) {
		t.Helper()
		got := list(flt)
		var w []int64
		for _, ev := range want {
			w = append(w, ids[ev])
		}
		if !slices.Equal(got, w) {
			t.Fatalf("%s: got %v want %v (%v)", name, got, w, want)
		}
	}
	const (
		login, logs, exec, denied = domain.EventLogin, domain.EventLogsView, domain.EventExecStart, domain.EventDecisionDenied
	)
	check("newest first", application.AuditFilter{}, login, denied, exec, logs)
	check("from", application.AuditFilter{From: t0.Add(time.Hour)}, login, denied, exec)
	check("to exclusive", application.AuditFilter{To: t0.Add(time.Hour)}, logs)
	check("range", application.AuditFilter{From: t0.Add(time.Hour), To: t0.Add(3 * time.Hour)}, denied, exec)
	check("username ci", application.AuditFilter{Username: "alice"}, denied, logs)
	check("user id", application.AuditFilter{UserID: 2}, login, exec)
	check("event type", application.AuditFilter{EventType: domain.EventExecStart}, exec)
	check("action", application.AuditFilter{Action: "restart"}, denied)
	check("result", application.AuditFilter{Result: domain.ResultDenied}, denied)
	check("cluster", application.AuditFilter{Cluster: "c1"}, exec, logs)
	check("namespace", application.AuditFilter{Namespace: "other"}, denied)
	check("environment", application.AuditFilter{Environment: "prod"}, exec, logs)
	check("break-glass", application.AuditFilter{BreakGlassID: 9}, exec)
	check("any break-glass", application.AuditFilter{BreakGlass: true}, exec)
	check("any break-glass with limit 1", application.AuditFilter{BreakGlass: true, Environment: "prod", Limit: 1}, exec)
	check("session", application.AuditFilter{SessionID: "s-1"}, exec)
	check("combined", application.AuditFilter{Username: "alice", Cluster: "c1"}, logs)
	check("none", application.AuditFilter{Cluster: "nope"})
	// Text: case-insensitive substring over username, pod, workload name, reason.
	check("text username", application.AuditFilter{Text: "ALIC"}, denied, logs)
	check("text pod", application.AuditFilter{Text: "api-7D"}, logs)
	check("text workload", application.AuditFilter{Text: "WORK"}, denied)
	check("text reason", application.AuditFilter{Text: "protected"}, denied)
	// LIKE wildcards in the text are literal.
	check("text percent", application.AuditFilter{Text: "100%"}, exec)
	check("text underscore", application.AuditFilter{Text: "full_now"}, exec)
	check("text wildcard is literal", application.AuditFilter{Text: "%"}, exec)
	check("text underscore wildcard literal", application.AuditFilter{Text: "u_l"}) // would match "ull" if it were a LIKE wildcard
	// Keyset pagination.
	check("before id", application.AuditFilter{BeforeID: ids[exec]}, logs)
	check("limit", application.AuditFilter{Limit: 2}, login, denied)
	check("page 2", application.AuditFilter{Limit: 2, BeforeID: ids[denied]}, exec, logs)

	// Admin changes by details.entity and details.id.
	var changes []int64
	for _, d := range []map[string]any{
		{"entity": "team", "operation": "update", "id": 12},
		{"entity": "team", "operation": "create", "id": 3},
		{"entity": "grant", "operation": "create", "id": 12},
	} {
		got, err := f.r.Audit.Insert(f.ctx, domain.AuditEvent{Username: "root", EventType: domain.EventAdminChange, Result: domain.ResultSucceeded, Details: d})
		must(t, err)
		changes = append(changes, got.ID)
	}
	for name, c := range map[string]struct {
		flt  application.AuditFilter
		want []int64
	}{
		"entity and id": {application.AuditFilter{Entity: "team", EntityID: "12"}, []int64{changes[0]}},
		"entity":        {application.AuditFilter{Entity: "team"}, []int64{changes[1], changes[0]}},
		"id":            {application.AuditFilter{EntityID: "12"}, []int64{changes[2], changes[0]}},
		"no such id":    {application.AuditFilter{Entity: "team", EntityID: "1"}, nil},
	} {
		if got := list(c.flt); !slices.Equal(got, c.want) {
			t.Fatalf("%s: got %v want %v", name, got, c.want)
		}
	}
}

func TestAuditPagination(t *testing.T) {
	f := newFixture(t)
	// 1100 rows in one statement.
	_, err := f.owner.Exec(f.ctx, `INSERT INTO audit_events (username, event_type, result)
		SELECT 'u', 'bulk', 'allowed' FROM generate_series(1, 1100)`)
	must(t, err)

	es, err := f.r.Audit.List(f.ctx, application.AuditFilter{})
	must(t, err)
	if len(es) != 50 {
		t.Fatalf("default limit: %d", len(es))
	}
	es, _ = f.r.Audit.List(f.ctx, application.AuditFilter{Limit: 5000})
	if len(es) != 1000 {
		t.Fatalf("max limit: %d", len(es))
	}
	// Walking pages by BeforeID visits every row exactly once, newest first.
	seen := 0
	var last int64
	for {
		page, err := f.r.Audit.List(f.ctx, application.AuditFilter{Limit: 400, BeforeID: last})
		must(t, err)
		for i, e := range page {
			if (last != 0 && e.ID >= last) || (i > 0 && e.ID >= page[i-1].ID) {
				t.Fatalf("not strictly descending at %d", e.ID)
			}
		}
		seen += len(page)
		if len(page) < 400 {
			break
		}
		last = page[len(page)-1].ID
	}
	if seen != 1100 {
		t.Fatalf("walked %d rows", seen)
	}
}

func sqlState(err error) string {
	var pg *pgconn.PgError
	if errors.As(err, &pg) {
		return pg.Code
	}
	return ""
}

// The application role must be unable to alter or remove audit rows, the
// retention role must be able to delete them, and neither may do more.
func TestAuditPrivileges(t *testing.T) {
	f := newFixture(t)
	e1, err := f.r.Audit.Insert(f.ctx, domain.AuditEvent{EventType: "old", Result: domain.ResultAllowed, At: t0})
	must(t, err)
	_, err = f.r.Audit.Insert(f.ctx, domain.AuditEvent{EventType: "new", Result: domain.ResultAllowed, At: t0.Add(48 * time.Hour)})
	must(t, err)

	const denied = "42501" // insufficient_privilege

	app, err := pgx.Connect(f.ctx, f.appDSN)
	must(t, err)
	defer app.Close(f.ctx)
	for _, stmt := range []string{
		`UPDATE audit_events SET username = 'forged'`,
		`UPDATE audit_events SET reason = 'x' WHERE id = 1`,
		`DELETE FROM audit_events`,
		`DELETE FROM audit_events WHERE id = 1`,
		`TRUNCATE audit_events`,
		`ALTER TABLE audit_events DISABLE TRIGGER ALL`,
		`DROP TABLE audit_events`,
		`CREATE TABLE evil (id int)`,
		`SELECT * FROM goose_db_version`,
	} {
		if _, err := app.Exec(f.ctx, stmt); sqlState(err) != denied {
			t.Errorf("app role: %q: got %v, want permission denied", stmt, err)
		}
	}
	// ... but it can still do its job, including on every other table.
	if _, err := app.Exec(f.ctx, `INSERT INTO audit_events (event_type, result) VALUES ('x', 'allowed')`); err != nil {
		t.Errorf("app role insert audit: %v", err)
	}
	if _, err := app.Exec(f.ctx, `SELECT count(*) FROM audit_events`); err != nil {
		t.Errorf("app role select audit: %v", err)
	}
	if _, err := app.Exec(f.ctx, `DELETE FROM settings`); err != nil {
		t.Errorf("app role delete settings: %v", err)
	}

	ret, err := pgx.Connect(f.ctx, f.retDSN)
	must(t, err)
	defer ret.Close(f.ctx)
	for _, stmt := range []string{
		`UPDATE audit_events SET username = 'forged'`,
		`INSERT INTO audit_events (event_type, result) VALUES ('x', 'allowed')`,
		`TRUNCATE audit_events`,
		`SELECT * FROM users`,
		`DELETE FROM users`,
		`SELECT * FROM settings`,
	} {
		if _, err := ret.Exec(f.ctx, stmt); sqlState(err) != denied {
			t.Errorf("retention role: %q: got %v, want permission denied", stmt, err)
		}
	}

	rt, err := NewAuditRetention(f.ctx, f.retDSN)
	must(t, err)
	defer rt.Close()
	n, err := rt.DeleteAuditBefore(f.ctx, t0.Add(24*time.Hour))
	must(t, err)
	if n != 1 {
		t.Fatalf("retention deleted %d rows, want 1", n)
	}
	left, _ := f.r.Audit.List(f.ctx, application.AuditFilter{EventType: "old"})
	if len(left) != 0 {
		t.Fatalf("old event survived: %+v (id %d)", left, e1.ID)
	}
	n, err = rt.DeleteAuditBefore(f.ctx, t0.Add(24*time.Hour))
	must(t, err)
	if n != 0 {
		t.Fatalf("second run deleted %d", n)
	}

	// The app role cannot use the retention job's entry point.
	bad, err := NewAuditRetention(f.ctx, f.appDSN)
	must(t, err)
	defer bad.Close()
	if _, err := bad.DeleteAuditBefore(f.ctx, time.Now()); sqlState(err) != denied {
		t.Fatalf("app role delete through retention: %v", err)
	}
}

func TestMigrate(t *testing.T) {
	f := newFixture(t) // the template was migrated once already
	must(t, Migrate(f.ctx, f.ownerDSN, WithRoles(testRoles())))

	// Roles that do not exist are skipped: Migrate still succeeds, and the
	// real roles' grants are left as they were.
	must(t, Migrate(f.ctx, f.ownerDSN, WithRoles(Roles{App: "no_such_app_role", Retention: "no_such_retention_role"})))

	if err := Migrate(f.ctx, f.ownerDSN, WithRoles(Roles{App: "same", Retention: "same"})); err == nil {
		t.Fatal("identical roles must be rejected")
	}

	var version int64
	must(t, f.owner.QueryRow(f.ctx, `SELECT max(version_id) FROM goose_db_version`).Scan(&version))
	if version != 12 {
		t.Fatalf("version %d", version)
	}
	// The app role keeps working after a second migration run.
	_, err := f.r.Audit.Insert(f.ctx, domain.AuditEvent{EventType: "x", Result: domain.ResultAllowed})
	must(t, err)
}
