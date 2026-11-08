package postgres

import (
	"testing"
	"time"

	"tiphys/internal/application"
	"tiphys/internal/domain"
)

func TestBreakGlassRequests(t *testing.T) {
	f := newFixture(t)
	u1, u2 := f.user("u1", domain.RoleUser), f.user("u2", domain.RoleUser)

	r1, err := f.r.BGRequests.Create(f.ctx, domain.BreakGlassRequest{UserID: u1.ID, Reason: "first reason that is long enough", Minutes: 30, CreatedAt: t0})
	must(t, err)
	if r1.Username != "u1" || r1.Status != domain.RequestPending || r1.Minutes != 30 || !r1.CreatedAt.Equal(t0) || r1.DecidedAt != nil {
		t.Fatalf("request: %+v", r1)
	}
	// One pending request per user.
	_, err = f.r.BGRequests.Create(f.ctx, domain.BreakGlassRequest{UserID: u1.ID, Reason: "again", Minutes: 30})
	isErr(t, err, application.ErrConflict)
	r2, err := f.r.BGRequests.Create(f.ctx, domain.BreakGlassRequest{UserID: u2.ID, Reason: "second reason that is long enough", Minutes: 60, CreatedAt: t0.Add(time.Hour)})
	must(t, err)

	// Approving needs the break-glass it created.
	isCheckViolation(t, f.r.BGRequests.Decide(f.ctx, r1.ID, domain.RequestApproved, "admin", "", nil, nil, t0))
	bg, err := f.r.BreakGlass.Create(f.ctx, domain.BreakGlass{UserID: u1.ID, Reason: r1.Reason, StartedAt: t0, ExpiresAt: t0.Add(time.Hour)})
	must(t, err)
	mins := 60
	must(t, f.r.BGRequests.Decide(f.ctx, r1.ID, domain.RequestApproved, "admin", "ok", &mins, &bg.ID, t0.Add(time.Minute)))
	got, err := f.r.BGRequests.Get(f.ctx, r1.ID)
	must(t, err)
	if got.Status != domain.RequestApproved || got.DecidedBy != "admin" || got.DecisionNote != "ok" || *got.ApprovedMinutes != 60 || *got.BreakGlassID != bg.ID {
		t.Fatalf("approved: %+v", got)
	}
	// Deciding twice conflicts; an unknown id is not found.
	isErr(t, f.r.BGRequests.Decide(f.ctx, r1.ID, domain.RequestRejected, "admin", "", nil, nil, t0), application.ErrConflict)
	isErr(t, f.r.BGRequests.Decide(f.ctx, 9999, domain.RequestRejected, "admin", "", nil, nil, t0), application.ErrNotFound)
	// The user can request again once nothing is pending.
	r3, err := f.r.BGRequests.Create(f.ctx, domain.BreakGlassRequest{UserID: u1.ID, Reason: "third reason that is long enough", Minutes: 15, CreatedAt: t0.Add(2 * time.Hour)})
	must(t, err)

	// Expiry marks only pending requests created before the cutoff.
	n, err := f.r.BGRequests.ExpirePendingBefore(f.ctx, t0.Add(90*time.Minute))
	must(t, err)
	if n != 1 {
		t.Fatalf("expired %d", n)
	}
	if got, _ := f.r.BGRequests.Get(f.ctx, r2.ID); got.Status != domain.RequestExpired || got.DecidedBy != "system" {
		t.Fatalf("expired: %+v", got)
	}

	// A scheduled request expires counting from its start.
	starts := t0.Add(5 * time.Hour)
	r4, err := f.r.BGRequests.Create(f.ctx, domain.BreakGlassRequest{UserID: u2.ID, Reason: "fourth reason that is long enough", Minutes: 15, StartsAt: &starts, CreatedAt: t0.Add(2 * time.Hour)})
	must(t, err)
	if r4.StartsAt == nil || !r4.StartsAt.Equal(starts) {
		t.Fatalf("starts_at: %+v", r4)
	}
	if n, err := f.r.BGRequests.ExpirePendingBefore(f.ctx, t0.Add(4*time.Hour)); err != nil || n != 1 {
		t.Fatalf("expire before the start: %d %v", n, err)
	}
	if got, _ := f.r.BGRequests.Get(f.ctx, r4.ID); got.Status != domain.RequestPending {
		t.Fatalf("scheduled request expired before its start: %+v", got)
	}
	if got, _ := f.r.BGRequests.Get(f.ctx, r3.ID); got.Status != domain.RequestExpired {
		t.Fatalf("unscheduled request not expired: %+v", got)
	}
	if n, err := f.r.BGRequests.ExpirePendingBefore(f.ctx, t0.Add(6*time.Hour)); err != nil || n != 1 {
		t.Fatalf("expire after the start: %d %v", n, err)
	}
	r5, err := f.r.BGRequests.Create(f.ctx, domain.BreakGlassRequest{UserID: u1.ID, Reason: "fifth reason that is long enough", Minutes: 15, CreatedAt: t0.Add(7 * time.Hour)})
	must(t, err)

	pending, err := f.r.BGRequests.List(f.ctx, 0, domain.RequestPending, 10)
	must(t, err)
	if len(pending) != 1 || pending[0].ID != r5.ID {
		t.Fatalf("pending: %+v", pending)
	}
	own, err := f.r.BGRequests.List(f.ctx, u1.ID, "", 10)
	must(t, err)
	if len(own) != 3 || own[0].ID != r5.ID || own[1].ID != r3.ID || own[2].ID != r1.ID {
		t.Fatalf("own, newest first: %+v", own)
	}
}
