package postgres

import (
	"errors"
	"testing"
	"time"

	"tiphys/internal/application"
	"tiphys/internal/domain"
)

func newRequest(f *fixture, userID int64) domain.AccessRequest {
	f.t.Helper()
	r, err := f.r.Requests.Create(f.ctx, domain.AccessRequest{
		UserID: userID, Reason: "need it",
		Scope: domain.Scope{Kind: domain.ScopeWorkloads, Cluster: "c1", Namespace: "pay",
			Workloads: []domain.WorkloadRef{{Kind: domain.KindDeployment, Name: "api"}}},
		Actions: []domain.Action{domain.ActionLogs, domain.ActionExec}, DurationHours: ptr(8),
	})
	must(f.t, err)
	return r
}

func TestAccessRequests(t *testing.T) {
	f := newFixture(t)
	u1, u2 := f.user("u1", domain.RoleUser), f.user("u2", domain.RoleUser)
	f.cluster("c1")

	r := newRequest(f, u1.ID)
	if r.Status != domain.RequestPending || r.Username != "u1" || r.DurationHours == nil || *r.DurationHours != 8 ||
		r.Scope.Cluster != "c1" || len(r.Scope.Workloads) != 1 || r.Scope.Workloads[0].Name != "api" || r.DecidedAt != nil {
		t.Fatalf("request: %+v", r)
	}
	perm, err := f.r.Requests.Create(f.ctx, domain.AccessRequest{UserID: u2.ID,
		Scope: domain.Scope{Kind: domain.ScopeArgoProject, ArgoProject: "p"}, Actions: []domain.Action{domain.ActionLogs}})
	must(t, err)
	if perm.DurationHours != nil || perm.Scope.Workloads != nil {
		t.Fatalf("permanent request: %+v", perm)
	}

	all, err := f.r.Requests.List(f.ctx, 0, "")
	must(t, err)
	if len(all) != 2 || all[0].ID != perm.ID { // newest first
		t.Fatalf("list all: %+v", all)
	}
	mine, _ := f.r.Requests.List(f.ctx, u1.ID, "")
	if len(mine) != 1 || mine[0].ID != r.ID {
		t.Fatalf("list by user: %+v", mine)
	}

	at := time.Now().UTC().Truncate(time.Microsecond)
	must(t, f.r.Requests.Decide(f.ctx, r.ID, domain.RequestRejected, "boss", "no", nil, at))
	got, err := f.r.Requests.Get(f.ctx, r.ID)
	must(t, err)
	if got.Status != domain.RequestRejected || got.DecidedBy != "boss" || got.DecisionNote != "no" || got.DecidedAt == nil || !got.DecidedAt.Equal(at) {
		t.Fatalf("decided: %+v", got)
	}
	// Second decision loses.
	isErr(t, f.r.Requests.Decide(f.ctx, r.ID, domain.RequestApproved, "boss2", "", nil, at), application.ErrConflict)
	isErr(t, f.r.Requests.Decide(f.ctx, 9999, domain.RequestApproved, "boss", "", nil, at), application.ErrNotFound)
	if err := f.r.Requests.Decide(f.ctx, perm.ID, domain.RequestPending, "boss", "", nil, at); err == nil {
		t.Fatal("deciding with status pending must fail")
	}
	rejected, _ := f.r.Requests.List(f.ctx, 0, domain.RequestRejected)
	pending, _ := f.r.Requests.List(f.ctx, 0, domain.RequestPending)
	if len(rejected) != 1 || len(pending) != 1 {
		t.Fatalf("status filter: %d rejected, %d pending", len(rejected), len(pending))
	}
	_, err = f.r.Requests.Get(f.ctx, 9999)
	isErr(t, err, application.ErrNotFound)

	// Only pending requests expire, by creation time.
	n, err := f.r.Requests.ExpirePendingBefore(f.ctx, time.Now().Add(-time.Hour))
	must(t, err)
	if n != 0 {
		t.Fatalf("expired %d recent requests", n)
	}
	n, err = f.r.Requests.ExpirePendingBefore(f.ctx, time.Now().Add(time.Hour))
	must(t, err)
	got, _ = f.r.Requests.Get(f.ctx, perm.ID)
	if n != 1 || got.Status != domain.RequestExpired || got.DecidedAt == nil {
		t.Fatalf("expire: n=%d %+v", n, got)
	}
	got, _ = f.r.Requests.Get(f.ctx, r.ID)
	if got.Status != domain.RequestRejected {
		t.Fatalf("rejected request touched by expiry: %+v", got)
	}
}

func TestAccessRequestConstraints(t *testing.T) {
	f := newFixture(t)
	u := f.user("u", domain.RoleUser)
	f.cluster("c1")
	_, err := f.r.Requests.Create(f.ctx, domain.AccessRequest{UserID: u.ID, Actions: []domain.Action{domain.ActionLogs},
		Scope: domain.Scope{Kind: domain.ScopeNamespace, Namespace: "x"}})
	isCheckViolation(t, err)
	_, err = f.r.Requests.Create(f.ctx, domain.AccessRequest{UserID: u.ID, Actions: []domain.Action{"x"}, Scope: nsScope("c1", "x")})
	isCheckViolation(t, err)
	_, err = f.r.Requests.Create(f.ctx, domain.AccessRequest{UserID: u.ID, Actions: []domain.Action{domain.ActionLogs}, Scope: nsScope("c1", "x"), DurationHours: ptr(0)})
	isCheckViolation(t, err)
	_, err = f.r.Requests.Create(f.ctx, domain.AccessRequest{UserID: 9999, Actions: []domain.Action{domain.ActionLogs}, Scope: nsScope("c1", "x")})
	isErr(t, err, application.ErrConflict)
}

// approve is what the application's approval does: create the grant and
// decide the request in one transaction.
func approve(f *fixture, reqID, userID int64, failAfterGrant error) (domain.Grant, error) {
	var g domain.Grant
	err := f.store.InTx(f.ctx, func(r application.Repositories) error {
		req, err := r.Requests.Get(f.ctx, reqID)
		if err != nil {
			return err
		}
		g, err = r.Grants.Create(f.ctx, domain.Grant{HolderKind: domain.HolderUser, UserID: userID,
			Scope: req.Scope, Actions: req.Actions, CreatedBy: "boss"})
		if err != nil {
			return err
		}
		if failAfterGrant != nil {
			return failAfterGrant
		}
		return r.Requests.Decide(f.ctx, reqID, domain.RequestApproved, "boss", "ok", &g.ID, time.Now())
	})
	return g, err
}

func TestApproveRequestAtomic(t *testing.T) {
	f := newFixture(t)
	u := f.user("u", domain.RoleUser)
	f.cluster("c1")

	// Commit: grant exists and the request points at it.
	req := newRequest(f, u.ID)
	g, err := approve(f, req.ID, u.ID, nil)
	must(t, err)
	got, _ := f.r.Requests.Get(f.ctx, req.ID)
	if got.Status != domain.RequestApproved || got.ResultingGrantID == nil || *got.ResultingGrantID != g.ID {
		t.Fatalf("approved request: %+v", got)
	}
	if _, err := f.r.Grants.Get(f.ctx, g.ID); err != nil {
		t.Fatal(err)
	}

	// A failure after the grant insert rolls the grant back.
	req2 := newRequest(f, u.ID)
	boom := errors.New("boom")
	_, err = approve(f, req2.ID, u.ID, boom)
	isErr(t, err, boom)
	gs, _ := f.r.Grants.List(f.ctx)
	got, _ = f.r.Requests.Get(f.ctx, req2.ID)
	if len(gs) != 1 || got.Status != domain.RequestPending {
		t.Fatalf("rollback left %d grants, request %s", len(gs), got.Status)
	}

	// Approving an already decided request fails and its grant is rolled back.
	_, err = approve(f, req.ID, u.ID, nil)
	isErr(t, err, application.ErrConflict)
	gs, _ = f.r.Grants.List(f.ctx)
	if len(gs) != 1 {
		t.Fatalf("conflicting approval left a grant: %d grants", len(gs))
	}

	// Two concurrent approvals: exactly one wins, one grant exists.
	req3 := newRequest(f, u.ID)
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() { _, err := approve(f, req3.ID, u.ID, nil); errs <- err }()
	}
	var ok, conflict int
	for i := 0; i < 2; i++ {
		switch err := <-errs; {
		case err == nil:
			ok++
		case errors.Is(err, application.ErrConflict):
			conflict++
		default:
			t.Fatal(err)
		}
	}
	gs, _ = f.r.Grants.List(f.ctx)
	if ok != 1 || conflict != 1 || len(gs) != 2 {
		t.Fatalf("ok=%d conflict=%d grants=%d", ok, conflict, len(gs))
	}
}

func TestTeamAccessRequests(t *testing.T) {
	f := newFixture(t)
	u := f.user("u1", domain.RoleUser)
	tm := f.team("payments")
	f.cluster("c1")
	scope := domain.Scope{Kind: domain.ScopeNamespace, Cluster: "c1", Namespace: "pay"}
	create := func() domain.AccessRequest {
		r, err := f.r.Requests.Create(f.ctx, domain.AccessRequest{UserID: u.ID, TeamID: tm.ID, Reason: "team",
			Scope: scope, Actions: []domain.Action{domain.ActionLogs}})
		must(t, err)
		return r
	}
	r := create()
	if r.TeamID != tm.ID || r.TeamName != "payments" || r.UserID != u.ID {
		t.Fatalf("team request: %+v", r)
	}
	own, err := f.r.Requests.Create(f.ctx, domain.AccessRequest{UserID: u.ID, Reason: "me", Scope: scope, Actions: []domain.Action{domain.ActionLogs}})
	must(t, err)
	if own.TeamID != 0 || own.TeamName != "" {
		t.Fatalf("own request: %+v", own)
	}
	byTeam, err := f.r.Requests.ListByTeam(f.ctx, tm.ID)
	must(t, err)
	if len(byTeam) != 1 || byTeam[0].ID != r.ID {
		t.Fatalf("list by team: %+v", byTeam)
	}

	// The previous release approves without approved_for_team; the CHECK
	// refuses that for a team's request, and Decide sets it.
	if _, err := f.store.pool.Exec(f.ctx, `UPDATE access_requests SET status = 'approved', decided_at = now() WHERE id = $1`, r.ID); err == nil {
		t.Fatal("team request approved without approved_for_team")
	}
	if _, err := f.store.pool.Exec(f.ctx, `UPDATE access_requests SET status = 'approved', decided_at = now() WHERE id = $1`, own.ID); err != nil {
		t.Fatalf("own request as the previous release approves it: %v", err)
	}
	must(t, f.r.Requests.Decide(f.ctx, r.ID, domain.RequestApproved, "boss", "", nil, time.Now()))

	// Deleting the team removes its requests.
	other := create()
	must(t, f.r.Teams.Delete(f.ctx, tm.ID))
	_, err = f.r.Requests.Get(f.ctx, other.ID)
	isErr(t, err, application.ErrNotFound)
}

func TestAccessRequestBatch(t *testing.T) {
	f := newFixture(t)
	u := f.user("u1", domain.RoleUser)
	f.cluster("c1")
	batch := "6f1f8c2e-6d1a-4c55-9a43-1f5a2c3b4d5e"
	var ids []int64
	for _, ns := range []string{"a", "b"} {
		r, err := f.r.Requests.Create(f.ctx, domain.AccessRequest{UserID: u.ID, Reason: "x", BatchID: batch,
			Scope: domain.Scope{Kind: domain.ScopeNamespace, Cluster: "c1", Namespace: ns}, Actions: []domain.Action{domain.ActionLogs}})
		must(t, err)
		if r.BatchID != batch {
			t.Fatalf("batch id: %+v", r)
		}
		ids = append(ids, r.ID)
	}
	single := newRequest(f, u.ID)
	if single.BatchID != "" {
		t.Fatalf("single request has a batch: %q", single.BatchID)
	}
	got, err := f.r.Requests.ListBatch(f.ctx, batch)
	must(t, err)
	if len(got) != 2 || got[0].ID != ids[0] || got[1].ID != ids[1] {
		t.Fatalf("batch: %+v", got)
	}
	_, err = f.r.Requests.ListBatch(f.ctx, "00000000-0000-0000-0000-000000000000")
	isErr(t, err, application.ErrNotFound)
	_, err = f.r.Requests.ListBatch(f.ctx, "nope")
	isErr(t, err, application.ErrNotFound)
	_, err = f.r.Requests.Create(f.ctx, domain.AccessRequest{UserID: u.ID, Reason: "x", BatchID: "nope",
		Scope: domain.Scope{Kind: domain.ScopeNamespace, Cluster: "c1", Namespace: "a"}, Actions: []domain.Action{domain.ActionLogs}})
	if err == nil {
		t.Fatal("malformed batch id accepted")
	}
}
