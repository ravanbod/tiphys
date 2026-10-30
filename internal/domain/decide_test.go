package domain

import (
	"testing"
	"time"
)

var tehran = loadLocation("Asia/Tehran")

func at(y int, m time.Month, d, h, mi int) time.Time {
	return time.Date(y, m, d, h, mi, 0, 0, tehran)
}

func ptr[T any](v T) *T { return &v }

// Reference dates: 2026-10-21 is a Wednesday, 2026-10-24 a Saturday.
var (
	wed1801 = at(2026, 10, 21, 18, 1)
	wed1000 = at(2026, 10, 21, 10, 0)
	sat0900 = at(2026, 10, 24, 9, 0)
)

type fixture struct {
	grants   []Grant
	windows  []TimeWindow
	holidays []Holiday
	argo     ArgoSnapshot
	protect  []string
	nsEnv    map[ClusterNS]string
	prjEnv   map[string]string
	clEnv    map[string]string
}

func (f fixture) policy() *Policy {
	return NewPolicy(PolicyInput{
		Grants: f.grants, Windows: f.windows, Holidays: f.holidays,
		ClusterEnv: f.clEnv, NamespaceEnv: f.nsEnv, ProjectEnv: f.prjEnv,
		Protected: f.protect, Argo: f.argo,
	})
}

func testArgo(fetched time.Time) ArgoSnapshot {
	return NewArgoSnapshot(fetched, []string{"payments", "search"}, []ArgoApp{
		{Name: "payments-api", Project: "payments", Cluster: "prod-1", Namespace: "payments-api"},
		{Name: "payments-worker", Project: "payments", Cluster: "prod-1", Namespace: "payments-worker", ResourceNamespaces: []string{"shared"}},
		{Name: "search-indexer", Project: "search", Cluster: "prod-2", Namespace: "search-indexer"},
	})
}

func teamGrant(id, team int64, scope Scope, actions ...Action) Grant {
	return Grant{ID: id, HolderKind: HolderTeam, TeamID: team, Scope: scope, Actions: actions}
}

var dev = Subject{ID: 1, Role: RoleUser, TeamIDs: []int64{10}}

func TestDecide_Scopes(t *testing.T) {
	now := wed1000
	f := fixture{
		argo: testArgo(now.Add(-time.Minute)),
		grants: []Grant{
			teamGrant(1, 10, Scope{Kind: ScopeArgoProject, ArgoProject: "payments"}, ActionLogs),
			teamGrant(2, 10, Scope{Kind: ScopeWorkloads, Cluster: "prod-2", Namespace: "multi",
				Workloads: []WorkloadRef{{KindDeployment, "payments-api"}}}, ActionLogs, ActionExec),
		},
	}
	p := f.policy()

	cases := []struct {
		name   string
		target Target
		action Action
		allow  bool
		reason Reason
	}{
		{"project grant allows its app", Target{Cluster: "prod-1", Namespace: "payments-api"}, ActionLogs, true, ReasonAllowed},
		{"project covers resource namespaces", Target{Cluster: "prod-1", Namespace: "shared"}, ActionLogs, true, ReasonAllowed},
		{"project denies other project", Target{Cluster: "prod-2", Namespace: "search-indexer"}, ActionLogs, false, ReasonNoGrant},
		{"namespace name alone is not enough", Target{Cluster: "prod-2", Namespace: "payments-api"}, ActionLogs, false, ReasonNoGrant},
		{"logs-only denies exec", Target{Cluster: "prod-1", Namespace: "payments-api"}, ActionExec, false, ReasonNoGrant},
		{"workload grant allows listed", Target{Cluster: "prod-2", Namespace: "multi", Workload: WorkloadRef{KindDeployment, "payments-api"}}, ActionExec, true, ReasonAllowed},
		{"workload grant denies sibling", Target{Cluster: "prod-2", Namespace: "multi", Workload: WorkloadRef{KindDeployment, "payments-worker"}}, ActionLogs, false, ReasonNoGrant},
		{"workload grant denies other kind same name", Target{Cluster: "prod-2", Namespace: "multi", Workload: WorkloadRef{KindStatefulSet, "payments-api"}}, ActionLogs, false, ReasonNoGrant},
		{"workload grant does not cover namespace target", Target{Cluster: "prod-2", Namespace: "multi"}, ActionLogs, false, ReasonNoGrant},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := Decide(p, dev, c.target, c.action, now)
			if d.Allowed != c.allow || d.Reason != c.reason {
				t.Fatalf("got allowed=%v reason=%s, want allowed=%v reason=%s", d.Allowed, d.Reason, c.allow, c.reason)
			}
		})
	}
}

func TestGrantReaches(t *testing.T) {
	f := fixture{argo: testArgo(wed1000)}
	p := f.policy()
	project := teamGrant(1, 10, Scope{Kind: ScopeArgoProject, ArgoProject: "payments"}, ActionLogs)
	namespace := teamGrant(2, 10, Scope{Kind: ScopeNamespace, Cluster: "prod-2", Namespace: "multi"}, ActionLogs)
	workloads := teamGrant(3, 10, Scope{Kind: ScopeWorkloads, Cluster: "prod-2", Namespace: "multi",
		Workloads: []WorkloadRef{{KindDeployment, "api"}}}, ActionLogs)
	multi := Target{Cluster: "prod-2", Namespace: "multi"}
	api := Target{Cluster: "prod-2", Namespace: "multi", Workload: WorkloadRef{KindDeployment, "api"}}
	worker := Target{Cluster: "prod-2", Namespace: "multi", Workload: WorkloadRef{KindDeployment, "worker"}}

	cases := []struct {
		name   string
		grant  Grant
		target Target
		want   bool
	}{
		{"project reaches its namespace", project, Target{Cluster: "prod-1", Namespace: "shared"}, true},
		{"project misses other namespace", project, multi, false},
		{"namespace reaches namespace", namespace, multi, true},
		{"namespace reaches its workloads", namespace, worker, true},
		{"namespace misses other cluster", namespace, Target{Cluster: "prod-1", Namespace: "multi"}, false},
		{"workloads reach their namespace", workloads, multi, true},
		{"workloads reach listed workload", workloads, api, true},
		{"workloads miss sibling", workloads, worker, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := p.GrantReaches(c.grant, c.target); got != c.want {
				t.Fatalf("got %v, want %v", got, c.want)
			}
		})
	}
}

func TestGrantsReaching(t *testing.T) {
	now := wed1000
	ns := Scope{Kind: ScopeNamespace, Cluster: "c", Namespace: "ns"}
	f := fixture{grants: []Grant{
		{ID: 3, HolderKind: HolderTeam, TeamID: 10, Scope: ns, Actions: []Action{ActionLogs}},
		{ID: 1, HolderKind: HolderUser, UserID: 1, Scope: ns, Actions: []Action{ActionExec}},
		{ID: 2, HolderKind: HolderUser, UserID: 2, Scope: ns, Actions: []Action{ActionExec}, ExpiresAt: ptr(now.Add(-time.Hour))},
		{ID: 4, HolderKind: HolderUser, UserID: 1, Scope: Scope{Kind: ScopeNamespace, Cluster: "c", Namespace: "other"}, Actions: []Action{ActionLogs}},
	}}
	var ids []int64
	for _, g := range f.policy().GrantsReaching(Target{Cluster: "c", Namespace: "ns"}, now) {
		ids = append(ids, g.ID)
	}
	if len(ids) != 2 || ids[0] != 1 || ids[1] != 3 {
		t.Fatalf("got %v, want [1 3] (unexpired, reaching, by ID)", ids)
	}
}

func TestDecide_ExpiredGrant(t *testing.T) {
	now := wed1000
	f := fixture{grants: []Grant{
		{ID: 1, HolderKind: HolderUser, UserID: 1, Scope: Scope{Kind: ScopeNamespace, Cluster: "c", Namespace: "ns"},
			Actions: []Action{ActionExec}, ExpiresAt: ptr(now.Add(-time.Hour))},
	}}
	d := Decide(f.policy(), dev, Target{Cluster: "c", Namespace: "ns"}, ActionExec, now)
	if d.Allowed || d.Reason != ReasonGrantExpired {
		t.Fatalf("got %+v", d)
	}
}

func TestDecide_RolesAndProtected(t *testing.T) {
	now := wed1000
	f := fixture{protect: []string{"kube-system"}, grants: []Grant{
		{ID: 1, HolderKind: HolderUser, UserID: 1, Scope: Scope{Kind: ScopeNamespace, Cluster: "c", Namespace: "kube-system"}, Actions: []Action{ActionLogs}},
	}}
	p := f.policy()
	protected := Target{Cluster: "c", Namespace: "kube-system"}
	ordinary := Target{Cluster: "c", Namespace: "ns"}

	for _, role := range []Role{RoleUser, RoleAuditor, RoleAdmin} {
		d := Decide(p, Subject{ID: 1, Role: role}, protected, ActionLogs, now)
		if d.Allowed || d.Reason != ReasonProtectedNamespace {
			t.Errorf("role %s on protected: %+v", role, d)
		}
	}
	if d := Decide(p, Subject{ID: 2, Role: RoleAdmin}, ordinary, ActionExec, now); !d.Allowed || d.Reason != ReasonAdmin {
		t.Errorf("admin: %+v", d)
	}
	if d := Decide(p, Subject{ID: 3, Role: RoleAuditor}, ordinary, ActionLogs, now); d.Allowed || d.Reason != ReasonRole {
		t.Errorf("auditor: %+v", d)
	}
	if d := Decide(p, Subject{ID: 2, Role: RoleAdmin, Disabled: true}, ordinary, ActionLogs, now); d.Allowed || d.Reason != ReasonUserDisabled {
		t.Errorf("disabled admin: %+v", d)
	}
}

func TestDecide_AllowInsideWindow(t *testing.T) {
	f := fixture{
		clEnv: map[string]string{"prod-1": "prod", "stg-1": "staging"},
		grants: []Grant{
			{ID: 1, HolderKind: HolderUser, UserID: 1, Scope: Scope{Kind: ScopeNamespace, Cluster: "prod-1", Namespace: "a"}, Actions: []Action{ActionExec}},
			{ID: 2, HolderKind: HolderUser, UserID: 1, Scope: Scope{Kind: ScopeNamespace, Cluster: "stg-1", Namespace: "a"}, Actions: []Action{ActionExec}},
		},
		windows: []TimeWindow{{
			ID: 1, Name: "prod hours", ScopeKind: WindowEnvironment, ScopeValue: "prod", Actions: []Action{ActionExec},
			Days:  []time.Weekday{time.Saturday, time.Sunday, time.Monday, time.Tuesday, time.Wednesday},
			Start: 9 * 60, End: 18 * 60, Timezone: "Asia/Tehran", Effect: AllowInside, Enabled: true,
		}},
	}
	p := f.policy()
	prod := Target{Cluster: "prod-1", Namespace: "a"}
	stg := Target{Cluster: "stg-1", Namespace: "a"}

	d := Decide(p, dev, prod, ActionExec, wed1801)
	if d.Allowed || d.Reason != ReasonOutsideWindow {
		t.Fatalf("prod 18:01 Wed: %+v", d)
	}
	if d.ReopensAt == nil || !d.ReopensAt.Equal(sat0900) {
		t.Fatalf("reopens_at = %v, want %v", d.ReopensAt, sat0900)
	}
	if d := Decide(p, dev, stg, ActionExec, wed1801); !d.Allowed {
		t.Fatalf("staging should be allowed: %+v", d)
	}

	// Inside the window: allowed, closes at 18:00 the same day.
	d = Decide(p, dev, prod, ActionExec, wed1000)
	if !d.Allowed {
		t.Fatalf("prod 10:00 Wed: %+v", d)
	}
	if want := at(2026, 10, 21, 18, 0); d.ClosesAt == nil || !d.ClosesAt.Equal(want) {
		t.Fatalf("closes_at = %v, want %v", d.ClosesAt, want)
	}
	// Thursday is not a window day.
	if d := Decide(p, dev, prod, ActionExec, at(2026, 10, 22, 10, 0)); d.Allowed {
		t.Fatalf("prod Thursday: %+v", d)
	}
	// Window rule does not apply to other actions.
	f.grants[0].Actions = []Action{ActionExec, ActionLogs}
	if d := Decide(f.policy(), dev, prod, ActionLogs, wed1801); !d.Allowed {
		t.Fatalf("logs unaffected by exec window: %+v", d)
	}
}

func TestDecide_WindowCrossingMidnight(t *testing.T) {
	f := fixture{
		grants: []Grant{{ID: 1, HolderKind: HolderUser, UserID: 1, Scope: Scope{Kind: ScopeNamespace, Cluster: "c", Namespace: "a"}, Actions: []Action{ActionExec}}},
		windows: []TimeWindow{{
			ID: 1, Name: "night", ScopeKind: WindowGlobal, Actions: []Action{ActionExec},
			Days:  []time.Weekday{time.Wednesday}, // starts Wednesday 22:00, ends Thursday 06:00
			Start: 22 * 60, End: 6 * 60, Timezone: "Asia/Tehran", Effect: AllowInside, Enabled: true,
		}},
	}
	p := f.policy()
	tgt := Target{Cluster: "c", Namespace: "a"}
	cases := []struct {
		name  string
		when  time.Time
		allow bool
	}{
		{"Wed 23:00", at(2026, 10, 21, 23, 0), true},
		{"Thu 05:00", at(2026, 10, 22, 5, 0), true},
		{"Thu 06:00", at(2026, 10, 22, 6, 0), false},
		{"Wed 05:00 (window of Tuesday does not exist)", at(2026, 10, 21, 5, 0), false},
		{"Wed 21:59", at(2026, 10, 21, 21, 59), false},
		{"Thu 23:00", at(2026, 10, 22, 23, 0), false},
	}
	for _, c := range cases {
		if d := Decide(p, dev, tgt, ActionExec, c.when); d.Allowed != c.allow {
			t.Errorf("%s: allowed=%v want %v (%+v)", c.name, d.Allowed, c.allow, d)
		}
	}
	// 23:00 Wednesday: closes at 06:00 Thursday.
	d := Decide(p, dev, tgt, ActionExec, at(2026, 10, 21, 23, 0))
	if want := at(2026, 10, 22, 6, 0); d.ClosesAt == nil || !d.ClosesAt.Equal(want) {
		t.Errorf("closes_at = %v, want %v", d.ClosesAt, want)
	}
}

func TestDecide_FullDayWindow(t *testing.T) {
	f := fixture{
		grants: []Grant{{ID: 1, HolderKind: HolderUser, UserID: 1, Scope: Scope{Kind: ScopeNamespace, Cluster: "c", Namespace: "a"}, Actions: []Action{ActionExec}}},
		windows: []TimeWindow{{
			ID: 1, Name: "thursday", ScopeKind: WindowGlobal, Actions: []Action{ActionExec},
			Days:  []time.Weekday{time.Thursday}, // start == end: the whole of Thursday
			Start: 0, End: 0, Timezone: "Asia/Tehran", Effect: DenyInside, Enabled: true,
		}},
	}
	p := f.policy()
	tgt := Target{Cluster: "c", Namespace: "a"}
	cases := []struct {
		name  string
		when  time.Time
		allow bool
	}{
		{"Wed 23:59", at(2026, 10, 21, 23, 59), true},
		{"Thu 00:00", at(2026, 10, 22, 0, 0), false},
		{"Thu 23:59", at(2026, 10, 22, 23, 59), false},
		{"Fri 00:00", at(2026, 10, 23, 0, 0), true},
	}
	for _, c := range cases {
		if d := Decide(p, dev, tgt, ActionExec, c.when); d.Allowed != c.allow {
			t.Errorf("%s: allowed=%v want %v (%+v)", c.name, d.Allowed, c.allow, d)
		}
	}
	d := Decide(p, dev, tgt, ActionExec, at(2026, 10, 22, 12, 0))
	if want := at(2026, 10, 23, 0, 0); d.ReopensAt == nil || !d.ReopensAt.Equal(want) {
		t.Errorf("reopens_at = %v, want %v", d.ReopensAt, want)
	}
}

func TestDecide_DenyInside(t *testing.T) {
	f := fixture{
		grants: []Grant{{ID: 1, HolderKind: HolderUser, UserID: 1, Scope: Scope{Kind: ScopeNamespace, Cluster: "c", Namespace: "a"}, Actions: []Action{ActionRestart}}},
		windows: []TimeWindow{{
			ID: 1, Name: "freeze", ScopeKind: WindowCluster, ScopeValue: "c", Actions: []Action{ActionRestart},
			Days:  []time.Weekday{time.Wednesday},
			Start: 12 * 60, End: 14 * 60, Timezone: "Asia/Tehran", Effect: DenyInside, Enabled: true,
		}},
	}
	p := f.policy()
	tgt := Target{Cluster: "c", Namespace: "a"}
	if d := Decide(p, dev, tgt, ActionRestart, at(2026, 10, 21, 13, 0)); d.Allowed || d.Reason != ReasonOutsideWindow {
		t.Fatalf("inside deny window: %+v", d)
	} else if want := at(2026, 10, 21, 14, 0); d.ReopensAt == nil || !d.ReopensAt.Equal(want) {
		t.Fatalf("reopens_at = %v want %v", d.ReopensAt, want)
	}
	d := Decide(p, dev, tgt, ActionRestart, at(2026, 10, 21, 11, 0))
	if !d.Allowed {
		t.Fatalf("before deny window: %+v", d)
	}
	if want := at(2026, 10, 21, 12, 0); d.ClosesAt == nil || !d.ClosesAt.Equal(want) {
		t.Fatalf("closes_at = %v want %v", d.ClosesAt, want)
	}
}

func TestDecide_TeamAndProjectWindows(t *testing.T) {
	now := wed1000
	f := fixture{
		argo: testArgo(now),
		grants: []Grant{
			teamGrant(1, 10, Scope{Kind: ScopeArgoProject, ArgoProject: "payments"}, ActionExec),
		},
		windows: []TimeWindow{
			{ID: 1, Name: "team freeze", ScopeKind: WindowTeam, ScopeValue: "payments", TeamID: 10, Actions: []Action{ActionExec},
				Days: []time.Weekday{time.Wednesday}, Start: 9 * 60, End: 11 * 60, Effect: DenyInside, Enabled: true},
		},
	}
	tgt := Target{Cluster: "prod-1", Namespace: "payments-api"}
	if d := Decide(f.policy(), dev, tgt, ActionExec, now); d.Allowed {
		t.Fatalf("team window should close: %+v", d)
	}
	other := Subject{ID: 1, Role: RoleUser}
	f.grants = []Grant{{ID: 2, HolderKind: HolderUser, UserID: 1, Scope: Scope{Kind: ScopeArgoProject, ArgoProject: "payments"}, Actions: []Action{ActionExec}}}
	if d := Decide(f.policy(), other, tgt, ActionExec, now); !d.Allowed {
		t.Fatalf("user outside the team is unaffected: %+v", d)
	}
	f.windows[0].Enabled = false
	if d := Decide(f.policy(), dev, tgt, ActionExec, now); !d.Allowed {
		t.Fatalf("disabled rule must be ignored: %+v", d)
	}
}

func TestDecide_Holiday(t *testing.T) {
	f := fixture{
		clEnv: map[string]string{"prod-1": "prod", "stg-1": "staging"},
		grants: []Grant{
			{ID: 1, HolderKind: HolderUser, UserID: 1, Scope: Scope{Kind: ScopeNamespace, Cluster: "prod-1", Namespace: "a"}, Actions: []Action{ActionExec}},
			{ID: 2, HolderKind: HolderUser, UserID: 1, Scope: Scope{Kind: ScopeNamespace, Cluster: "stg-1", Namespace: "a"}, Actions: []Action{ActionExec}},
		},
		holidays: []Holiday{{ID: 1, Date: time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC), Title: "Holiday", Environments: []string{"prod"}}},
	}
	p := f.policy()
	d := Decide(p, dev, Target{Cluster: "prod-1", Namespace: "a"}, ActionExec, wed1000)
	if d.Allowed || d.Reason != ReasonHoliday {
		t.Fatalf("prod on holiday: %+v", d)
	}
	if want := at(2026, 10, 22, 0, 0); d.ReopensAt == nil || !d.ReopensAt.Equal(want) {
		t.Fatalf("reopens_at = %v want %v", d.ReopensAt, want)
	}
	if d := Decide(p, dev, Target{Cluster: "stg-1", Namespace: "a"}, ActionExec, wed1000); !d.Allowed {
		t.Fatalf("staging on prod holiday: %+v", d)
	}
	// Day before: allowed, closes at midnight when the holiday starts.
	d = Decide(p, dev, Target{Cluster: "prod-1", Namespace: "a"}, ActionExec, at(2026, 10, 20, 12, 0))
	if !d.Allowed {
		t.Fatalf("day before holiday: %+v", d)
	}
	if want := at(2026, 10, 21, 0, 0); d.ClosesAt == nil || !d.ClosesAt.Equal(want) {
		t.Fatalf("closes_at = %v want %v", d.ClosesAt, want)
	}
	// An empty environment list closes everything.
	f.holidays[0].Environments = nil
	if d := Decide(f.policy(), dev, Target{Cluster: "stg-1", Namespace: "a"}, ActionExec, wed1000); d.Allowed {
		t.Fatalf("all-environment holiday: %+v", d)
	}
}

func TestDecide_BreakGlass(t *testing.T) {
	now := wed1801
	f := fixture{
		grants: []Grant{{ID: 1, HolderKind: HolderUser, UserID: 1, Scope: Scope{Kind: ScopeNamespace, Cluster: "c", Namespace: "a"}, Actions: []Action{ActionExec}}},
		windows: []TimeWindow{{ID: 1, Name: "hours", ScopeKind: WindowGlobal, Actions: []Action{ActionExec},
			Days:  []time.Weekday{time.Wednesday},
			Start: 9 * 60, End: 18 * 60, Effect: AllowInside, Enabled: true}},
	}
	p := f.policy()
	bg := &BreakGlass{ID: 7, UserID: 1, StartedAt: now.Add(-10 * time.Minute), ExpiresAt: now.Add(50 * time.Minute)}
	sub := dev
	sub.BreakGlass = bg

	d := Decide(p, sub, Target{Cluster: "c", Namespace: "a"}, ActionExec, now)
	if !d.Allowed || d.BreakGlassID == nil || *d.BreakGlassID != 7 {
		t.Fatalf("break-glass should override window: %+v", d)
	}
	if d.ClosesAt == nil || !d.ClosesAt.Equal(bg.ExpiresAt) {
		t.Fatalf("closes_at = %v want %v", d.ClosesAt, bg.ExpiresAt)
	}
	// It never grants a scope the user lacks.
	if d := Decide(p, sub, Target{Cluster: "c", Namespace: "other"}, ActionExec, now); d.Allowed || d.Reason != ReasonNoGrant {
		t.Fatalf("break-glass must not grant scope: %+v", d)
	}
	// Nor an action the user lacks.
	if d := Decide(p, sub, Target{Cluster: "c", Namespace: "a"}, ActionRestart, now); d.Allowed {
		t.Fatalf("break-glass must not grant action: %+v", d)
	}
	// Ended or expired break-glass does nothing.
	ended := *bg
	ended.EndedAt = ptr(now.Add(-time.Minute))
	sub.BreakGlass = &ended
	if d := Decide(p, sub, Target{Cluster: "c", Namespace: "a"}, ActionExec, now); d.Allowed {
		t.Fatalf("ended break-glass: %+v", d)
	}
	// Inside the window the break-glass is not tagged.
	sub.BreakGlass = bg
	d = Decide(p, sub, Target{Cluster: "c", Namespace: "a"}, ActionExec, wed1000.Add(-0))
	if d.BreakGlassID != nil {
		// wed1000 is before bg.StartedAt so bg is inactive; either way no tag expected.
		t.Fatalf("unexpected break-glass tag: %+v", d)
	}
}

func TestDecide_GrantExpiryClosesAt(t *testing.T) {
	now := wed1000
	exp := now.Add(2 * time.Hour)
	f := fixture{grants: []Grant{{ID: 1, HolderKind: HolderUser, UserID: 1,
		Scope: Scope{Kind: ScopeNamespace, Cluster: "c", Namespace: "a"}, Actions: []Action{ActionLogs}, ExpiresAt: &exp}}}
	d := Decide(f.policy(), dev, Target{Cluster: "c", Namespace: "a"}, ActionLogs, now)
	if !d.Allowed || d.ClosesAt == nil || !d.ClosesAt.Equal(exp) {
		t.Fatalf("got %+v", d)
	}
}

func TestDecide_EnvironmentPrecedence(t *testing.T) {
	now := wed1000
	f := fixture{
		argo:   testArgo(now),
		clEnv:  map[string]string{"prod-1": "cluster-env"},
		prjEnv: map[string]string{"payments": "project-env"},
		nsEnv:  map[ClusterNS]string{{"prod-1", "payments-api"}: "ns-env"},
	}
	p := f.policy()
	if got := p.Environment("prod-1", "payments-api"); got != "ns-env" {
		t.Errorf("namespace override: %s", got)
	}
	if got := p.Environment("prod-1", "payments-worker"); got != "project-env" {
		t.Errorf("project override: %s", got)
	}
	if got := p.Environment("prod-1", "unknown"); got != "cluster-env" {
		t.Errorf("cluster label: %s", got)
	}
	if got := p.Environment("prod-2", "unknown"); got != UnlabeledEnvironment {
		t.Errorf("unlabeled: %s", got)
	}
}

func TestDecide_StaleArgoCache(t *testing.T) {
	now := wed1000
	grants := []Grant{teamGrant(1, 10, Scope{Kind: ScopeArgoProject, ArgoProject: "payments"}, ActionLogs),
		teamGrant(2, 10, Scope{Kind: ScopeNamespace, Cluster: "prod-1", Namespace: "direct"}, ActionLogs)}
	tgt := Target{Cluster: "prod-1", Namespace: "payments-api"}

	fresh := fixture{grants: grants, argo: testArgo(now.Add(-14 * time.Minute))}
	if d := Decide(fresh.policy(), dev, tgt, ActionLogs, now); !d.Allowed {
		t.Fatalf("14 min old cache should still match: %+v", d)
	}
	stale := fixture{grants: grants, argo: testArgo(now.Add(-16 * time.Minute))}
	d := Decide(stale.policy(), dev, tgt, ActionLogs, now)
	if d.Allowed || d.Reason != ReasonArgoStale {
		t.Fatalf("16 min old cache: %+v", d)
	}
	// Namespace grants are unaffected by Argo staleness.
	if d := Decide(stale.policy(), dev, Target{Cluster: "prod-1", Namespace: "direct"}, ActionLogs, now); !d.Allowed {
		t.Fatalf("namespace grant with stale argo: %+v", d)
	}
	// Never fetched.
	empty := fixture{grants: grants}
	if d := Decide(empty.policy(), dev, tgt, ActionLogs, now); d.Allowed {
		t.Fatalf("no cache: %+v", d)
	}
}

func TestScopeValidate(t *testing.T) {
	bad := []Scope{
		{Kind: ScopeArgoProject},
		{Kind: ScopeArgoProject, ArgoProject: "p", Namespace: "x"},
		{Kind: ScopeNamespace, Cluster: "c"},
		{Kind: ScopeNamespace, Cluster: "c", Namespace: "Bad_NS"},
		{Kind: ScopeWorkloads, Cluster: "c", Namespace: "n"},
		{Kind: ScopeWorkloads, Cluster: "c", Namespace: "n", Workloads: []WorkloadRef{{"Pod", "x"}}},
		{Kind: "nope"},
	}
	for i, s := range bad {
		if s.Validate() == nil {
			t.Errorf("case %d should be invalid: %+v", i, s)
		}
	}
	good := Scope{Kind: ScopeWorkloads, Cluster: "c", Namespace: "n", Workloads: []WorkloadRef{{KindCronJob, "nightly"}}}
	if err := good.Validate(); err != nil {
		t.Errorf("valid scope: %v", err)
	}
}

func BenchmarkDecide(b *testing.B) {
	now := wed1000
	var grants []Grant
	for i := 0; i < 200; i++ {
		grants = append(grants, teamGrant(int64(i), 10, Scope{Kind: ScopeNamespace, Cluster: "prod-1", Namespace: "ns"}, ActionLogs))
	}
	f := fixture{
		grants: grants, argo: testArgo(now),
		clEnv: map[string]string{"prod-1": "prod"},
		windows: []TimeWindow{{ID: 1, Name: "w", ScopeKind: WindowEnvironment, ScopeValue: "prod", Actions: []Action{ActionLogs},
			Days:  []time.Weekday{0, 1, 2, 3, 4, 5, 6},
			Start: 9 * 60, End: 18 * 60, Effect: AllowInside, Enabled: true}},
	}
	p := f.policy()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		Decide(p, dev, Target{Cluster: "prod-1", Namespace: "ns"}, ActionLogs, now)
	}
}

func TestDecide_LogsAreNeverLimitedByTimeRules(t *testing.T) {
	f := fixture{
		clEnv: map[string]string{"prod-1": "prod"},
		grants: []Grant{{ID: 1, HolderKind: HolderUser, UserID: 1, Scope: Scope{Kind: ScopeNamespace, Cluster: "prod-1", Namespace: "a"},
			Actions: []Action{ActionLogs, ActionExec}}},
		windows: []TimeWindow{
			// Even a rule that lists logs must not close them.
			{ID: 1, Name: "prod hours", ScopeKind: WindowEnvironment, ScopeValue: "prod", Actions: []Action{ActionLogs, ActionExec},
				Days: []time.Weekday{time.Saturday}, Start: 9 * 60, End: 18 * 60, Effect: AllowInside, Enabled: true},
			{ID: 2, Name: "freeze", ScopeKind: WindowGlobal, Actions: []Action{ActionLogs}, Days: []time.Weekday{time.Wednesday},
				Start: 0, End: 23*60 + 59, Effect: DenyInside, Enabled: true},
		},
		holidays: []Holiday{{ID: 1, Date: time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC), Title: "Holiday"}},
	}
	p := f.policy()
	tgt := Target{Cluster: "prod-1", Namespace: "a"}

	// Wednesday 18:01, a holiday, inside a deny rule and outside the allow window: logs stay open.
	d := Decide(p, dev1, tgt, ActionLogs, wed1801)
	if !d.Allowed || d.Reason != ReasonAllowed || d.ReopensAt != nil || d.ClosesAt != nil {
		t.Fatalf("logs: %+v", d)
	}
	// Exec is closed by the very same rules.
	if d := Decide(p, dev1, tgt, ActionExec, wed1801); d.Allowed || d.Reason != ReasonHoliday {
		t.Fatalf("exec: %+v", d)
	}
}

var dev1 = Subject{ID: 1, Role: RoleUser}

func TestDecideApp(t *testing.T) {
	now := wed1000
	ns := func(cluster, namespace string) Scope {
		return Scope{Kind: ScopeNamespace, Cluster: cluster, Namespace: namespace}
	}
	worker := ArgoApp{Name: "payments-worker", Project: "payments", Cluster: "prod-1", Namespace: "payments-worker", ResourceNamespaces: []string{"shared"}}
	cases := []struct {
		name   string
		grants []Grant
		sub    Subject
		app    ArgoApp
		allow  bool
		reason Reason
		at     ClusterNS
	}{
		{"every namespace granted", []Grant{teamGrant(1, 10, ns("prod-1", "payments-worker"), ActionSync), teamGrant(2, 10, ns("prod-1", "shared"), ActionSync)},
			dev, worker, true, ReasonAllowed, ClusterNS{"prod-1", "payments-worker"}},
		{"a resource namespace without the grant denies", []Grant{teamGrant(1, 10, ns("prod-1", "payments-worker"), ActionSync)},
			dev, worker, false, ReasonNoGrant, ClusterNS{"prod-1", "shared"}},
		{"project grant covers all its app's namespaces", []Grant{teamGrant(1, 10, Scope{Kind: ScopeArgoProject, ArgoProject: "payments"}, ActionSync)},
			dev, worker, true, ReasonAllowed, ClusterNS{"prod-1", "payments-worker"}},
		{"another action does not count", []Grant{teamGrant(1, 10, Scope{Kind: ScopeArgoProject, ArgoProject: "payments"}, ActionRestart)},
			dev, worker, false, ReasonNoGrant, ClusterNS{"prod-1", "payments-worker"}},
		{"own Namespace objects are fine", []Grant{teamGrant(1, 10, Scope{Kind: ScopeArgoProject, ArgoProject: "payments"}, ActionSync)},
			dev, ArgoApp{Name: "a", Project: "payments", Cluster: "prod-1", Namespace: "payments-api", ClusterResources: []string{"Namespace/payments-api"}},
			true, ReasonAllowed, ClusterNS{"prod-1", "payments-api"}},
		{"other cluster-scoped resources need an admin", []Grant{teamGrant(1, 10, Scope{Kind: ScopeArgoProject, ArgoProject: "payments"}, ActionSync)},
			dev, ArgoApp{Name: "a", Project: "payments", Cluster: "prod-1", Namespace: "payments-api", ClusterResources: []string{"ClusterRole/x"}},
			false, ReasonAppScope, ClusterNS{"prod-1", "payments-api"}},
		{"admin may sync cluster-scoped resources", nil,
			Subject{ID: 2, Role: RoleAdmin}, ArgoApp{Name: "a", Project: "payments", Cluster: "prod-1", Namespace: "payments-api", ClusterResources: []string{"ClusterRole/x"}},
			true, ReasonAdmin, ClusterNS{"prod-1", "payments-api"}},
		{"an app deploying nowhere known is denied", nil,
			Subject{ID: 2, Role: RoleAdmin}, ArgoApp{Name: "orphan", Project: "payments", Namespace: "x"},
			false, ReasonAppScope, ClusterNS{"", "x"}},
		{"auditor", nil, Subject{ID: 3, Role: RoleAuditor}, worker, false, ReasonRole, ClusterNS{"prod-1", "payments-worker"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := fixture{argo: testArgo(now.Add(-time.Minute)), grants: c.grants}.policy()
			d, pair := DecideApp(p, c.sub, c.app, ActionSync, now)
			if d.Allowed != c.allow || d.Reason != c.reason || pair != c.at {
				t.Fatalf("got %v %s at %v, want %v %s at %v", d.Allowed, d.Reason, pair, c.allow, c.reason, c.at)
			}
		})
	}

	t.Run("protected namespace denies even admins", func(t *testing.T) {
		p := fixture{argo: testArgo(now.Add(-time.Minute)), protect: []string{"kube-system"}}.policy()
		app := ArgoApp{Name: "a", Cluster: "prod-1", Namespace: "payments-api", ResourceNamespaces: []string{"kube-system"}}
		if d, pair := DecideApp(p, Subject{ID: 2, Role: RoleAdmin}, app, ActionSync, now); d.Allowed || d.Reason != ReasonProtectedNamespace || pair.Namespace != "kube-system" {
			t.Fatalf("got %+v at %v", d, pair)
		}
	})
	t.Run("stale Argo cache denies", func(t *testing.T) {
		p := fixture{argo: testArgo(now.Add(-time.Hour)), grants: []Grant{teamGrant(1, 10, ns("prod-1", "payments-worker"), ActionSync), teamGrant(2, 10, ns("prod-1", "shared"), ActionSync)}}.policy()
		if d, _ := DecideApp(p, dev, worker, ActionSync, now); d.Allowed || d.Reason != ReasonArgoStale {
			t.Fatalf("got %+v", d)
		}
	})
	t.Run("earliest close and break-glass of any namespace", func(t *testing.T) {
		soon, later := now.Add(time.Hour), now.Add(2*time.Hour)
		g1 := teamGrant(1, 10, ns("prod-1", "payments-worker"), ActionSync)
		g1.ExpiresAt = &later
		g2 := teamGrant(2, 10, ns("prod-1", "shared"), ActionSync)
		g2.ExpiresAt = &soon
		p := fixture{argo: testArgo(now.Add(-time.Minute)), grants: []Grant{g1, g2}}.policy()
		d, _ := DecideApp(p, dev, worker, ActionSync, now)
		if !d.Allowed || d.ClosesAt == nil || !d.ClosesAt.Equal(soon) || len(d.GrantIDs) != 2 {
			t.Fatalf("got %+v", d)
		}
	})
}

func TestValidateScopeActions(t *testing.T) {
	wl := Scope{Kind: ScopeWorkloads, Cluster: "c", Namespace: "n", Workloads: []WorkloadRef{{KindDeployment, "x"}}}
	if err := ValidateScopeActions(wl, []Action{ActionLogs, ActionSync}); err == nil {
		t.Fatal("sync on a workloads scope must be refused")
	}
	if err := ValidateScopeActions(Scope{Kind: ScopeNamespace, Cluster: "c", Namespace: "n"}, []Action{ActionSync}); err != nil {
		t.Fatal(err)
	}
	if err := ValidateScopeActions(wl, nil); err == nil {
		t.Fatal("no actions must be refused")
	}
	if err := ValidateScopeActions(wl, []Action{"delete"}); err == nil {
		t.Fatal("unknown action must be refused")
	}
}
