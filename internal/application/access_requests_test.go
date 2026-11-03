package application

import (
	"testing"
	"time"

	"tiphys/internal/domain"
)

func TestMergeTarget(t *testing.T) {
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	at := func(h int) *time.Time { x := now.Add(time.Duration(h) * time.Hour); return &x }
	ns := domain.Scope{Kind: domain.ScopeNamespace, Cluster: "prod-1", Namespace: "shop"}
	wl := func(names ...string) domain.Scope {
		s := domain.Scope{Kind: domain.ScopeWorkloads, Cluster: "prod-1", Namespace: "shop"}
		for _, n := range names {
			s.Workloads = append(s.Workloads, domain.WorkloadRef{Kind: domain.KindDeployment, Name: n})
		}
		return s
	}
	own := func(id int64, s domain.Scope, exp *time.Time) domain.Grant {
		return domain.Grant{ID: id, HolderKind: domain.HolderUser, UserID: 7, Scope: s, ExpiresAt: exp}
	}
	grants := []domain.Grant{
		own(1, ns, at(48)),
		own(2, ns, at(24)),
		own(3, ns, at(-1)), // expired
		own(4, ns, nil),    // permanent
		{ID: 5, HolderKind: domain.HolderTeam, TeamID: 1, Scope: ns},
		own(6, wl("a", "b"), at(24)),
		{ID: 7, HolderKind: domain.HolderUser, UserID: 8, Scope: ns, ExpiresAt: at(24)},
	}
	for _, c := range []struct {
		name    string
		scope   domain.Scope
		expires *time.Time
		want    int64 // 0: none
	}{
		{"the longest that does not outlast the new access", ns, at(72), 1},
		{"one ending at the same time", ns, at(24), 2},
		{"none when every one outlasts it", ns, at(8), 0},
		{"permanent access takes the permanent grant", ns, nil, 4},
		{"workloads as a set", wl("b", "a"), at(24), 6},
		{"not a different set of workloads", wl("a"), at(24), 0},
	} {
		got, ok := mergeTarget(grants, 7, c.scope, c.expires, now)
		if (c.want == 0) == ok || (ok && got.ID != c.want) {
			t.Errorf("%s: got %d %v, want %d", c.name, got.ID, ok, c.want)
		}
	}
	if got := unionActions([]domain.Action{domain.ActionRestart, domain.ActionLogs}, []domain.Action{domain.ActionExec, domain.ActionLogs}); len(got) != 3 ||
		got[0] != domain.ActionLogs || got[1] != domain.ActionExec || got[2] != domain.ActionRestart {
		t.Errorf("union: %v", got)
	}
}
