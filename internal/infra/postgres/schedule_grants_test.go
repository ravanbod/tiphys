package postgres

import (
	"reflect"
	"slices"
	"testing"
	"time"

	"tiphys/internal/application"
	"tiphys/internal/domain"
)

func TestGrants(t *testing.T) {
	f := newFixture(t)
	u := f.user("u", domain.RoleUser)
	tm := f.team("platform")
	f.cluster("c1")

	exp := time.Now().Add(time.Hour).UTC().Truncate(time.Microsecond)
	gTeam, err := f.r.Grants.Create(f.ctx, domain.Grant{
		HolderKind: domain.HolderTeam, TeamID: tm.ID,
		Scope:   domain.Scope{Kind: domain.ScopeArgoProject, ArgoProject: "payments"},
		Actions: []domain.Action{domain.ActionLogs, domain.ActionRestart}, Note: "n", CreatedBy: "admin",
	})
	must(t, err)
	if gTeam.ID == 0 || gTeam.TeamID != tm.ID || gTeam.UserID != 0 || gTeam.Scope.Cluster != "" ||
		gTeam.CreatedBy != "admin" || gTeam.UpdatedBy != "admin" || gTeam.ExpiresAt != nil || gTeam.CreatedAt.IsZero() {
		t.Fatalf("team grant: %+v", gTeam)
	}

	ws := []domain.WorkloadRef{{Kind: domain.KindStatefulSet, Name: "db"}, {Kind: domain.KindDeployment, Name: "api"}}
	gUser, err := f.r.Grants.Create(f.ctx, domain.Grant{
		HolderKind: domain.HolderUser, UserID: u.ID,
		Scope:   domain.Scope{Kind: domain.ScopeWorkloads, Cluster: "c1", Namespace: "pay", Workloads: ws},
		Actions: []domain.Action{domain.ActionExec}, ExpiresAt: &exp,
	})
	must(t, err)
	if gUser.Scope.Cluster != "c1" || gUser.ExpiresAt == nil || !gUser.ExpiresAt.Equal(exp) || len(gUser.Scope.Workloads) != 2 {
		t.Fatalf("user grant: %+v", gUser)
	}
	// Workloads come back ordered by kind, name.
	if gUser.Scope.Workloads[0].Kind != domain.KindDeployment {
		t.Fatalf("workload order: %+v", gUser.Scope.Workloads)
	}
	gNS, err := f.r.Grants.Create(f.ctx, domain.Grant{
		HolderKind: domain.HolderUser, UserID: u.ID, Scope: nsScope("c1", "pay"),
		Actions: []domain.Action{domain.ActionLogs},
	})
	must(t, err)
	if gNS.Scope.Workloads != nil {
		t.Fatalf("namespace grant has workloads: %+v", gNS.Scope.Workloads)
	}

	all, err := f.r.Grants.List(f.ctx)
	must(t, err)
	if len(all) != 3 || all[0].ID != gTeam.ID || len(all[1].Scope.Workloads) != 2 {
		t.Fatalf("list: %+v", all)
	}

	// Update replaces workloads, actions and scope.
	gUser.Scope.Workloads = []domain.WorkloadRef{{Kind: domain.KindCronJob, Name: "nightly"}}
	gUser.Actions = []domain.Action{domain.ActionRunJob}
	gUser.ExpiresAt = nil
	gUser.UpdatedBy = "boss"
	must(t, f.r.Grants.Update(f.ctx, gUser))
	got, err := f.r.Grants.Get(f.ctx, gUser.ID)
	must(t, err)
	if len(got.Scope.Workloads) != 1 || got.Scope.Workloads[0].Name != "nightly" || got.ExpiresAt != nil ||
		got.UpdatedBy != "boss" || got.CreatedBy != "" || !slices.Equal(got.Actions, []domain.Action{domain.ActionRunJob}) {
		t.Fatalf("after update: %+v", got)
	}
	// Switching to a namespace scope drops the workloads.
	got.Scope = nsScope("c1", "pay")
	must(t, f.r.Grants.Update(f.ctx, got))
	got, _ = f.r.Grants.Get(f.ctx, got.ID)
	if got.Scope.Workloads != nil || got.Scope.Kind != domain.ScopeNamespace {
		t.Fatalf("scope switch: %+v", got)
	}
	isErr(t, f.r.Grants.Update(f.ctx, domain.Grant{ID: 9999, HolderKind: domain.HolderUser, UserID: u.ID, Scope: nsScope("c1", "x"), Actions: []domain.Action{domain.ActionLogs}}), application.ErrNotFound)

	// Unknown cluster / team.
	_, err = f.r.Grants.Create(f.ctx, domain.Grant{HolderKind: domain.HolderUser, UserID: u.ID, Scope: nsScope("nope", "x"), Actions: []domain.Action{domain.ActionLogs}})
	isErr(t, err, application.ErrNotFound)
	_, err = f.r.Grants.Create(f.ctx, domain.Grant{HolderKind: domain.HolderTeam, TeamID: 9999, Scope: nsScope("c1", "x"), Actions: []domain.Action{domain.ActionLogs}})
	isErr(t, err, application.ErrConflict)
	_, err = f.r.Grants.Get(f.ctx, 9999)
	isErr(t, err, application.ErrNotFound)

	// A cluster with grants cannot be deleted.
	isErr(t, f.r.Clusters.Delete(f.ctx, "c1"), application.ErrConflict)

	// Deleting the team removes its grants.
	must(t, f.r.Teams.Delete(f.ctx, tm.ID))
	_, err = f.r.Grants.Get(f.ctx, gTeam.ID)
	isErr(t, err, application.ErrNotFound)

	must(t, f.r.Grants.Delete(f.ctx, gNS.ID))
	isErr(t, f.r.Grants.Delete(f.ctx, gNS.ID), application.ErrNotFound)
	// Workloads of a deleted grant are gone with it.
	must(t, f.r.Grants.Delete(f.ctx, gUser.ID))
	var n int
	must(t, f.owner.QueryRow(f.ctx, `SELECT count(*) FROM grant_workloads`).Scan(&n))
	if n != 0 {
		t.Fatalf("%d orphan grant_workloads", n)
	}
}

func TestClusterAndEnvironmentGrants(t *testing.T) {
	f := newFixture(t)
	u := f.user("u", domain.RoleUser)
	f.cluster("c1")
	logs := []domain.Action{domain.ActionLogs}

	gc, err := f.r.Grants.Create(f.ctx, domain.Grant{HolderKind: domain.HolderUser, UserID: u.ID,
		Scope: domain.Scope{Kind: domain.ScopeCluster, Cluster: "c1"}, Actions: logs})
	must(t, err)
	if !reflect.DeepEqual(gc.Scope, domain.Scope{Kind: domain.ScopeCluster, Cluster: "c1"}) {
		t.Fatalf("cluster grant: %+v", gc.Scope)
	}
	ge, err := f.r.Grants.Create(f.ctx, domain.Grant{HolderKind: domain.HolderUser, UserID: u.ID,
		Scope: domain.Scope{Kind: domain.ScopeEnvironment, Environment: "prod"}, Actions: logs})
	must(t, err)
	if !reflect.DeepEqual(ge.Scope, domain.Scope{Kind: domain.ScopeEnvironment, Environment: "prod"}) {
		t.Fatalf("environment grant: %+v", ge.Scope)
	}
	// Switching kinds clears the other kind's columns.
	ge.Scope = nsScope("c1", "pay")
	must(t, f.r.Grants.Update(f.ctx, ge))
	got, err := f.r.Grants.Get(f.ctx, ge.ID)
	must(t, err)
	if got.Scope.Environment != "" || got.Scope.Kind != domain.ScopeNamespace {
		t.Fatalf("after switch: %+v", got.Scope)
	}
	_, err = f.r.Grants.Create(f.ctx, domain.Grant{HolderKind: domain.HolderUser, UserID: u.ID,
		Scope: domain.Scope{Kind: domain.ScopeCluster, Cluster: "nope"}, Actions: logs})
	isErr(t, err, application.ErrNotFound)
	// A cluster grant keeps the cluster from being deleted.
	must(t, f.r.Grants.Delete(f.ctx, ge.ID))
	isErr(t, f.r.Clusters.Delete(f.ctx, "c1"), application.ErrConflict)
	must(t, f.r.Grants.Delete(f.ctx, gc.ID))
	must(t, f.r.Clusters.Delete(f.ctx, "c1"))
}

func TestGrantConstraints(t *testing.T) {
	f := newFixture(t)
	u := f.user("u", domain.RoleUser)
	tm := f.team("t")
	f.cluster("c1")
	logs := []domain.Action{domain.ActionLogs}
	argo := domain.Scope{Kind: domain.ScopeArgoProject, ArgoProject: "p"}

	cases := map[string]domain.Grant{
		"team and user both":         {HolderKind: domain.HolderTeam, TeamID: tm.ID, UserID: u.ID, Scope: argo, Actions: logs},
		"team holder without team":   {HolderKind: domain.HolderTeam, Scope: argo, Actions: logs},
		"user holder with team":      {HolderKind: domain.HolderUser, UserID: u.ID, TeamID: tm.ID, Scope: argo, Actions: logs},
		"bad holder kind":            {HolderKind: "robot", UserID: u.ID, Scope: argo, Actions: logs},
		"argo scope with namespace":  {HolderKind: domain.HolderUser, UserID: u.ID, Scope: domain.Scope{Kind: domain.ScopeArgoProject, ArgoProject: "p", Namespace: "x"}, Actions: logs},
		"argo scope with cluster":    {HolderKind: domain.HolderUser, UserID: u.ID, Scope: domain.Scope{Kind: domain.ScopeArgoProject, ArgoProject: "p", Cluster: "c1"}, Actions: logs},
		"argo scope without name":    {HolderKind: domain.HolderUser, UserID: u.ID, Scope: domain.Scope{Kind: domain.ScopeArgoProject}, Actions: logs},
		"namespace scope no ns":      {HolderKind: domain.HolderUser, UserID: u.ID, Scope: domain.Scope{Kind: domain.ScopeNamespace, Cluster: "c1"}, Actions: logs},
		"namespace scope no cluster": {HolderKind: domain.HolderUser, UserID: u.ID, Scope: domain.Scope{Kind: domain.ScopeNamespace, Namespace: "x"}, Actions: logs},
		"namespace scope w/ project": {HolderKind: domain.HolderUser, UserID: u.ID, Scope: domain.Scope{Kind: domain.ScopeNamespace, Cluster: "c1", Namespace: "x", ArgoProject: "p"}, Actions: logs},
		"bad scope kind":             {HolderKind: domain.HolderUser, UserID: u.ID, Scope: domain.Scope{Kind: "everything"}, Actions: logs},
		"cluster scope with ns":      {HolderKind: domain.HolderUser, UserID: u.ID, Scope: domain.Scope{Kind: domain.ScopeCluster, Cluster: "c1", Namespace: "x"}, Actions: logs},
		"cluster scope with env":     {HolderKind: domain.HolderUser, UserID: u.ID, Scope: domain.Scope{Kind: domain.ScopeCluster, Cluster: "c1", Environment: "prod"}, Actions: logs},
		"env scope without env":      {HolderKind: domain.HolderUser, UserID: u.ID, Scope: domain.Scope{Kind: domain.ScopeEnvironment}, Actions: logs},
		"env scope with cluster":     {HolderKind: domain.HolderUser, UserID: u.ID, Scope: domain.Scope{Kind: domain.ScopeEnvironment, Environment: "prod", Cluster: "c1"}, Actions: logs},
		"namespace scope with env":   {HolderKind: domain.HolderUser, UserID: u.ID, Scope: domain.Scope{Kind: domain.ScopeNamespace, Cluster: "c1", Namespace: "x", Environment: "prod"}, Actions: logs},
		"no actions":                 {HolderKind: domain.HolderUser, UserID: u.ID, Scope: argo},
		"unknown action":             {HolderKind: domain.HolderUser, UserID: u.ID, Scope: argo, Actions: []domain.Action{"delete"}},
	}
	for name, g := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := f.r.Grants.Create(f.ctx, g)
			isCheckViolation(t, err)
		})
	}
	t.Run("bad workload kind", func(t *testing.T) {
		_, err := f.r.Grants.Create(f.ctx, domain.Grant{HolderKind: domain.HolderUser, UserID: u.ID,
			Scope:   domain.Scope{Kind: domain.ScopeWorkloads, Cluster: "c1", Namespace: "x", Workloads: []domain.WorkloadRef{{Kind: "Pod", Name: "p"}}},
			Actions: logs})
		isCheckViolation(t, err)
	})
	// A failed create leaves nothing behind (grant row rolled back with it).
	gs, err := f.r.Grants.List(f.ctx)
	must(t, err)
	if len(gs) != 0 {
		t.Fatalf("%d grants left after failed creates", len(gs))
	}
}

func TestWindows(t *testing.T) {
	f := newFixture(t)
	tm := f.team("sre")
	w, err := f.r.Windows.Create(f.ctx, domain.TimeWindow{
		Name: "office", ScopeKind: domain.WindowEnvironment, ScopeValue: "prod",
		Actions: []domain.Action{domain.ActionExec, domain.ActionRestart},
		Days:    []time.Weekday{time.Saturday, time.Sunday, time.Monday},
		Start:   9*60 + 30, End: 24 * 60, Effect: domain.AllowInside, Enabled: true, UpdatedBy: "admin",
	})
	must(t, err)
	if w.Timezone != domain.DefaultTimezone || w.Start != 9*60+30 || w.End != 24*60 || !w.Enabled ||
		w.ScopeValue != "prod" || w.TeamID != 0 || !slices.Equal(w.Days, []time.Weekday{6, 0, 1}) {
		t.Fatalf("window: %+v", w)
	}

	g, err := f.r.Windows.Create(f.ctx, domain.TimeWindow{
		Name: "freeze", ScopeKind: domain.WindowGlobal, Actions: []domain.Action{domain.ActionRestart},
		Days: []time.Weekday{time.Friday}, Start: 0, End: 23*60 + 59, Timezone: "Europe/Berlin", Effect: domain.DenyInside,
	})
	must(t, err)
	if g.ScopeValue != "" || g.Timezone != "Europe/Berlin" || g.Enabled {
		t.Fatalf("global window: %+v", g)
	}

	// A team window is written by team name and reads back with the team id.
	tw, err := f.r.Windows.Create(f.ctx, domain.TimeWindow{
		Name: "sre only", ScopeKind: domain.WindowTeam, ScopeValue: "sre", Actions: []domain.Action{domain.ActionExec},
		Days: []time.Weekday{time.Monday}, Start: 60, End: 120, Effect: domain.AllowInside,
	})
	must(t, err)
	if tw.TeamID != tm.ID || tw.ScopeValue != "sre" {
		t.Fatalf("team window: %+v", tw)
	}
	_, err = f.r.Windows.Create(f.ctx, domain.TimeWindow{
		Name: "ghost", ScopeKind: domain.WindowTeam, ScopeValue: "ghosts", Actions: []domain.Action{domain.ActionExec},
		Days: []time.Weekday{time.Monday}, Start: 60, End: 120, Effect: domain.AllowInside,
	})
	isErr(t, err, application.ErrNotFound)

	// Renaming the team keeps its windows pointing at the new name.
	tm.Name = "sre2"
	must(t, f.r.Teams.Update(f.ctx, tm))
	got, err := f.r.Windows.Get(f.ctx, tw.ID)
	must(t, err)
	if got.ScopeValue != "sre2" {
		t.Fatalf("scope value after rename: %q", got.ScopeValue)
	}

	// Update, including moving a window to a different scope kind.
	w.Name, w.Enabled, w.Days, w.ScopeKind, w.ScopeValue = "office2", false, []time.Weekday{time.Tuesday}, domain.WindowCluster, "c9"
	w.UpdatedBy = "boss"
	must(t, f.r.Windows.Update(f.ctx, w))
	got, _ = f.r.Windows.Get(f.ctx, w.ID)
	if got.Name != "office2" || got.Enabled || got.ScopeKind != domain.WindowCluster || got.ScopeValue != "c9" ||
		!slices.Equal(got.Days, []time.Weekday{2}) || got.UpdatedBy != "boss" {
		t.Fatalf("after update: %+v", got)
	}
	isErr(t, f.r.Windows.Update(f.ctx, domain.TimeWindow{ID: 9999, Name: "x", ScopeKind: domain.WindowGlobal, Actions: []domain.Action{domain.ActionLogs}, Days: []time.Weekday{1}, Effect: domain.AllowInside}), application.ErrNotFound)

	all, err := f.r.Windows.List(f.ctx)
	must(t, err)
	if len(all) != 3 {
		t.Fatalf("list: %d", len(all))
	}

	// Deleting the team removes its windows.
	must(t, f.r.Teams.Delete(f.ctx, tm.ID))
	_, err = f.r.Windows.Get(f.ctx, tw.ID)
	isErr(t, err, application.ErrNotFound)
	must(t, f.r.Windows.Delete(f.ctx, g.ID))
	isErr(t, f.r.Windows.Delete(f.ctx, g.ID), application.ErrNotFound)
}

func TestWindowConstraints(t *testing.T) {
	f := newFixture(t)
	f.team("sre")
	base := domain.TimeWindow{Name: "w", ScopeKind: domain.WindowGlobal, Actions: []domain.Action{domain.ActionLogs},
		Days: []time.Weekday{time.Monday}, Start: 0, End: 60, Effect: domain.AllowInside}
	mut := func(fn func(w *domain.TimeWindow)) domain.TimeWindow { w := base; fn(&w); return w }
	cases := map[string]domain.TimeWindow{
		"global with value":    mut(func(w *domain.TimeWindow) { w.ScopeValue = "x" }),
		"environment no value": mut(func(w *domain.TimeWindow) { w.ScopeKind = domain.WindowEnvironment }),
		"bad scope kind":       mut(func(w *domain.TimeWindow) { w.ScopeKind = "planet"; w.ScopeValue = "x" }),
		"bad effect":           mut(func(w *domain.TimeWindow) { w.Effect = "maybe" }),
		"no actions":           mut(func(w *domain.TimeWindow) { w.Actions = nil }),
		"bad action":           mut(func(w *domain.TimeWindow) { w.Actions = []domain.Action{"nuke"} }),
		"no days":              mut(func(w *domain.TimeWindow) { w.Days = nil }),
		"day 7":                mut(func(w *domain.TimeWindow) { w.Days = []time.Weekday{7} }),
	}
	for name, w := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := f.r.Windows.Create(f.ctx, w)
			isCheckViolation(t, err)
		})
	}
	// The team_id/scope_value pairing is also enforced by the table itself.
	_, err := f.owner.Exec(f.ctx, `INSERT INTO time_windows (name, scope_kind, scope_value, actions, days_of_week, start_time, end_time, effect)
		VALUES ('x', 'team', 'sre', '{logs}', '{1}', '01:00', '02:00', 'allow_inside')`)
	isCheckViolation(t, err)
}

func TestHolidays(t *testing.T) {
	f := newFixture(t)
	// A time in a far-east zone must keep its calendar day.
	loc := time.FixedZone("x", 14*3600)
	h, err := f.r.Holidays.Create(f.ctx, domain.Holiday{Date: time.Date(2026, 3, 21, 0, 30, 0, 0, loc), Title: "Nowruz"})
	must(t, err)
	if h.Date.Format("2006-01-02") != "2026-03-21" || h.Environments == nil || len(h.Environments) != 0 {
		t.Fatalf("holiday: %+v", h)
	}
	h2, err := f.r.Holidays.Create(f.ctx, domain.Holiday{Date: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), Title: "NY", Environments: []string{"prod"}})
	must(t, err)
	all, err := f.r.Holidays.List(f.ctx)
	must(t, err)
	if len(all) != 2 || all[0].ID != h2.ID { // ordered by date
		t.Fatalf("list: %+v", all)
	}
	h.Title, h.Environments = "Nowruz day", []string{"prod", "stage"}
	must(t, f.r.Holidays.Update(f.ctx, h))
	got, err := f.r.Holidays.Get(f.ctx, h.ID)
	must(t, err)
	if got.Title != "Nowruz day" || !slices.Equal(got.Environments, []string{"prod", "stage"}) {
		t.Fatalf("update: %+v", got)
	}
	isErr(t, f.r.Holidays.Update(f.ctx, domain.Holiday{ID: 9999, Date: t0}), application.ErrNotFound)
	_, err = f.r.Holidays.Get(f.ctx, 9999)
	isErr(t, err, application.ErrNotFound)
	must(t, f.r.Holidays.Delete(f.ctx, h.ID))
	isErr(t, f.r.Holidays.Delete(f.ctx, h.ID), application.ErrNotFound)
}
