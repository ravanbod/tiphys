package postgres

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"tiphys/internal/application"
	"tiphys/internal/domain"
)

func TestBreakGlass(t *testing.T) {
	f := newFixture(t)
	u1, u2 := f.user("u1", domain.RoleUser), f.user("u2", domain.RoleUser)

	if _, err := f.r.BreakGlass.LatestForUser(f.ctx, u1.ID); err == nil {
		t.Fatal("want ErrNotFound")
	} else {
		isErr(t, err, application.ErrNotFound)
	}

	old, err := f.r.BreakGlass.Create(f.ctx, domain.BreakGlass{UserID: u1.ID, Reason: "first reason that is long enough",
		StartedAt: t0, ExpiresAt: t0.Add(time.Hour)})
	must(t, err)
	if old.Username != "u1" || old.EndedAt != nil {
		t.Fatalf("break-glass: %+v", old)
	}
	// Zero StartedAt uses the database clock.
	cur, err := f.r.BreakGlass.Create(f.ctx, domain.BreakGlass{UserID: u1.ID, Reason: "second reason that is long enough",
		ExpiresAt: time.Now().Add(time.Hour)})
	must(t, err)
	if time.Since(cur.StartedAt) > time.Minute || cur.StartedAt.After(time.Now().Add(time.Second)) {
		t.Fatalf("started_at %v", cur.StartedAt)
	}
	other, err := f.r.BreakGlass.Create(f.ctx, domain.BreakGlass{UserID: u2.ID, Reason: "third reason that is long enough",
		StartedAt: t0, ExpiresAt: t0.Add(2 * time.Hour)})
	must(t, err)
	_, err = f.r.BreakGlass.Create(f.ctx, domain.BreakGlass{UserID: u1.ID, Reason: "x", StartedAt: t0, ExpiresAt: t0})
	isCheckViolation(t, err)

	latest, err := f.r.BreakGlass.LatestForUser(f.ctx, u1.ID)
	must(t, err)
	if latest.ID != cur.ID {
		t.Fatalf("latest: %+v", latest)
	}

	// Active at t0+30m: old and other; not cur (starts later).
	act, err := f.r.BreakGlass.ListActive(f.ctx, t0.Add(30*time.Minute))
	must(t, err)
	if len(act) != 2 {
		t.Fatalf("active: %+v", act)
	}
	// Ending a break-glass removes it from the active list from that time on.
	must(t, f.r.BreakGlass.End(f.ctx, other.ID, "u2", t0.Add(40*time.Minute)))
	act, _ = f.r.BreakGlass.ListActive(f.ctx, t0.Add(50*time.Minute))
	if len(act) != 1 || act[0].ID != old.ID {
		t.Fatalf("active after end: %+v", act)
	}
	got, _ := f.r.BreakGlass.Get(f.ctx, other.ID)
	if got.EndedAt == nil || got.EndedBy != "u2" || !got.EndedAt.Equal(t0.Add(40*time.Minute)) {
		t.Fatalf("ended: %+v", got)
	}
	// Expired ones are not active.
	act, _ = f.r.BreakGlass.ListActive(f.ctx, t0.Add(90*time.Minute))
	if len(act) != 0 {
		t.Fatalf("expired still active: %+v", act)
	}
	isErr(t, f.r.BreakGlass.End(f.ctx, other.ID, "u2", t0), application.ErrConflict)
	isErr(t, f.r.BreakGlass.End(f.ctx, 9999, "u2", t0), application.ErrNotFound)
	_, err = f.r.BreakGlass.Get(f.ctx, 9999)
	isErr(t, err, application.ErrNotFound)

	all, err := f.r.BreakGlass.List(f.ctx, 2)
	must(t, err)
	if len(all) != 2 || all[0].ID != cur.ID {
		t.Fatalf("list: %+v", all)
	}
}

func newExec(f *fixture, userID int64, mut func(*domain.ExecSession)) domain.ExecSession {
	f.t.Helper()
	s := domain.ExecSession{ID: uuid.NewString(), UserID: userID, Username: "u", Cluster: "c1", Namespace: "pay",
		Pod: "api-1", Container: "app", Shell: "/bin/sh", WorkloadKind: domain.KindDeployment, WorkloadName: "api",
		Environment: "prod", StartedAt: t0, RecordingKey: "sessions/2026/03/x"}
	if mut != nil {
		mut(&s)
	}
	must(f.t, f.r.ExecSessions.Create(f.ctx, s))
	return s
}

func TestDebugSessionKeepsItsContainer(t *testing.T) {
	f := newFixture(t)
	u := f.user("u1", domain.RoleUser)
	s := newExec(f, u.ID, func(s *domain.ExecSession) { s.DebugContainer = "tiphys-debug-1a2b3c4d" })
	got, err := f.r.ExecSessions.Get(f.ctx, s.ID)
	must(t, err)
	if got.DebugContainer != "tiphys-debug-1a2b3c4d" {
		t.Fatalf("session: %+v", got)
	}
	list, err := f.r.ExecSessions.List(f.ctx, application.ExecSessionFilter{})
	must(t, err)
	if len(list) != 1 || list[0].DebugContainer != "tiphys-debug-1a2b3c4d" {
		t.Fatalf("list: %+v", list)
	}
}

func TestExecSessions(t *testing.T) {
	f := newFixture(t)
	u1, u2 := f.user("u1", domain.RoleUser), f.user("u2", domain.RoleUser)
	bg, err := f.r.BreakGlass.Create(f.ctx, domain.BreakGlass{UserID: u1.ID, Reason: "r", StartedAt: t0, ExpiresAt: t0.Add(time.Hour)})
	must(t, err)

	s1 := newExec(f, u1.ID, func(s *domain.ExecSession) { s.Username = "u1"; s.BreakGlassID = &bg.ID })
	s2 := newExec(f, u2.ID, func(s *domain.ExecSession) {
		s.Username = "u2"
		s.Cluster = "c2"
		s.Namespace = "other"
		s.Environment = "stage"
		s.StartedAt = t0.Add(time.Hour)
	})
	s3 := newExec(f, u1.ID, func(s *domain.ExecSession) { s.Username = "u1"; s.StartedAt = t0.Add(2 * time.Hour) })

	got, err := f.r.ExecSessions.Get(f.ctx, s1.ID)
	must(t, err)
	if got.ID != s1.ID || got.WorkloadKind != domain.KindDeployment || got.WorkloadName != "api" || got.Environment != "prod" ||
		got.BreakGlassID == nil || *got.BreakGlassID != bg.ID || got.EndedAt != nil || got.RecordingKey != s1.RecordingKey || got.Username != "u1" {
		t.Fatalf("session: %+v", got)
	}
	if got.DebugContainer != "" {
		t.Fatalf("exec session has a debug container: %+v", got)
	}
	_, err = f.r.ExecSessions.Get(f.ctx, uuid.NewString())
	isErr(t, err, application.ErrNotFound)
	_, err = f.r.ExecSessions.Get(f.ctx, "not-a-uuid")
	isErr(t, err, application.ErrNotFound)
	if err := f.r.ExecSessions.Create(f.ctx, domain.ExecSession{ID: "bad", UserID: u1.ID}); err == nil {
		t.Fatal("invalid id must fail")
	}
	// Same id twice.
	isErr(t, f.r.ExecSessions.Create(f.ctx, s1), application.ErrConflict)

	end := t0.Add(10 * time.Minute)
	must(t, f.r.ExecSessions.End(f.ctx, s1.ID, end, "closed", 1234))
	got, _ = f.r.ExecSessions.Get(f.ctx, s1.ID)
	if got.EndedAt == nil || !got.EndedAt.Equal(end) || got.EndReason != "closed" || got.RecordingBytes != 1234 {
		t.Fatalf("ended: %+v", got)
	}
	isErr(t, f.r.ExecSessions.End(f.ctx, s1.ID, end, "again", 1), application.ErrConflict)
	isErr(t, f.r.ExecSessions.End(f.ctx, uuid.NewString(), end, "x", 1), application.ErrNotFound)

	ids := func(f2 application.ExecSessionFilter) []string {
		ss, err := f.r.ExecSessions.List(f.ctx, f2)
		must(t, err)
		out := make([]string, len(ss))
		for i, s := range ss {
			out[i] = s.ID
		}
		return out
	}
	eq := func(name string, got []string, want ...string) {
		t.Helper()
		if len(got) != len(want) {
			t.Fatalf("%s: got %v want %v", name, got, want)
		}
		for i := range got {
			if got[i] != want[i] {
				t.Fatalf("%s: got %v want %v", name, got, want)
			}
		}
	}
	eq("all newest first", ids(application.ExecSessionFilter{}), s3.ID, s2.ID, s1.ID)
	eq("user id", ids(application.ExecSessionFilter{UserID: u1.ID}), s3.ID, s1.ID)
	eq("username ci", ids(application.ExecSessionFilter{Username: "U2"}), s2.ID)
	eq("cluster", ids(application.ExecSessionFilter{Cluster: "c2"}), s2.ID)
	eq("namespace", ids(application.ExecSessionFilter{Namespace: "other"}), s2.ID)
	eq("environment", ids(application.ExecSessionFilter{Environment: "prod"}), s3.ID, s1.ID)
	eq("active", ids(application.ExecSessionFilter{ActiveOnly: true}), s3.ID, s2.ID)
	eq("ended", ids(application.ExecSessionFilter{EndedOnly: true}), s1.ID)
	eq("break-glass", ids(application.ExecSessionFilter{BreakGlass: true}), s1.ID)
	eq("from/to", ids(application.ExecSessionFilter{From: t0.Add(30 * time.Minute), To: t0.Add(90 * time.Minute)}), s2.ID)
	eq("before start", ids(application.ExecSessionFilter{BeforeStart: t0.Add(time.Hour)}), s1.ID)
	eq("limit", ids(application.ExecSessionFilter{Limit: 1}), s3.ID)

	// Startup cleanup closes only the open sessions, with a fixed reason.
	at := t0.Add(5 * time.Hour)
	n, err := f.r.ExecSessions.EndAbandoned(f.ctx, at)
	must(t, err)
	if n != 2 {
		t.Fatalf("abandoned %d", n)
	}
	for _, id := range []string{s2.ID, s3.ID} {
		got, _ := f.r.ExecSessions.Get(f.ctx, id)
		if got.EndedAt == nil || !got.EndedAt.Equal(at) || got.EndReason != "portal_restart" {
			t.Fatalf("abandoned session: %+v", got)
		}
	}
	got, _ = f.r.ExecSessions.Get(f.ctx, s1.ID)
	if got.EndReason != "closed" {
		t.Fatalf("closed session overwritten: %+v", got)
	}
	n, _ = f.r.ExecSessions.EndAbandoned(f.ctx, at)
	if n != 0 {
		t.Fatalf("second EndAbandoned changed %d", n)
	}
}

func TestWebSessions(t *testing.T) {
	f := newFixture(t)
	u := f.user("u", domain.RoleUser)
	other := f.user("o", domain.RoleUser)
	now := time.Now().UTC().Truncate(time.Microsecond)

	mk := func(id string, uid int64, refreshed, expires time.Time) application.WebSession {
		s := application.WebSession{ID: id, UserID: uid, CSRFToken: "csrf-" + id, AccessToken: "sealed-a", RefreshToken: "sealed-r",
			IDToken: "sealed-i", CreatedAt: refreshed, LastRefreshAt: refreshed, ExpiresAt: expires, SourceIP: "10.0.0.1", UserAgent: "ua"}
		must(t, f.r.WebSessions.Create(f.ctx, s))
		return s
	}
	a := mk("a", u.ID, now.Add(-10*time.Minute), now.Add(time.Hour))
	mk("b", u.ID, now.Add(-time.Minute), now.Add(time.Hour))
	mk("c", other.ID, now.Add(-20*time.Minute), now.Add(-time.Minute)) // expired
	mk("d", other.ID, now.Add(-30*time.Minute), now.Add(time.Hour))

	got, err := f.r.WebSessions.Get(f.ctx, "a")
	must(t, err)
	if got != a && (got.ID != a.ID || got.CSRFToken != "csrf-a" || got.AccessToken != "sealed-a" || got.SourceIP != "10.0.0.1" ||
		!got.CreatedAt.Equal(a.CreatedAt) || !got.ExpiresAt.Equal(a.ExpiresAt)) {
		t.Fatalf("session: %+v", got)
	}
	_, err = f.r.WebSessions.Get(f.ctx, "zzz")
	isErr(t, err, application.ErrNotFound)
	isErr(t, f.r.WebSessions.Create(f.ctx, a), application.ErrConflict)
	isErr(t, f.r.WebSessions.Create(f.ctx, application.WebSession{ID: "x", UserID: 9999, ExpiresAt: now}), application.ErrConflict)

	due, err := f.r.WebSessions.ListRefreshDue(f.ctx, now.Add(-5*time.Minute))
	must(t, err)
	if len(due) != 2 || due[0].ID != "d" || due[1].ID != "a" { // oldest first, expired "c" excluded
		t.Fatalf("due: %+v", due)
	}

	must(t, f.r.WebSessions.UpdateTokens(f.ctx, "a", "A2", "R2", "I2", now, now.Add(2*time.Hour)))
	got, _ = f.r.WebSessions.Get(f.ctx, "a")
	if got.AccessToken != "A2" || got.RefreshToken != "R2" || got.IDToken != "I2" || !got.LastRefreshAt.Equal(now) || !got.ExpiresAt.Equal(now.Add(2*time.Hour)) || got.CSRFToken != "csrf-a" {
		t.Fatalf("after update tokens: %+v", got)
	}
	isErr(t, f.r.WebSessions.UpdateTokens(f.ctx, "zzz", "", "", "", now, now), application.ErrNotFound)

	n, err := f.r.WebSessions.DeleteExpired(f.ctx, now)
	must(t, err)
	if n != 1 {
		t.Fatalf("expired deleted: %d", n)
	}
	must(t, f.r.WebSessions.Delete(f.ctx, "b"))
	must(t, f.r.WebSessions.Delete(f.ctx, "b")) // idempotent
	n, err = f.r.WebSessions.DeleteByUser(f.ctx, other.ID)
	must(t, err)
	if n != 1 {
		t.Fatalf("by user deleted: %d", n)
	}
	left, _ := f.r.WebSessions.ListRefreshDue(f.ctx, now.Add(time.Hour))
	if len(left) != 1 || left[0].ID != "a" {
		t.Fatalf("left: %+v", left)
	}
}
