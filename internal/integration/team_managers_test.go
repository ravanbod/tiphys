package integration

import (
	"context"
	"fmt"
	"slices"
	"testing"

	"tiphys/internal/application"
	"tiphys/internal/domain"
)

// teamChanges counts the team admin changes of an operation the actor made.
func (e *env) teamChanges(operation, actor string) int {
	e.t.Helper()
	evs, err := e.store.Repos().Audit.List(context.Background(), application.AuditFilter{EventType: domain.EventAdminChange})
	if err != nil {
		e.t.Fatal(err)
	}
	n := 0
	for _, ev := range evs {
		if ev.Username == actor && ev.Details["entity"] == "team" && ev.Details["operation"] == operation {
			n++
		}
	}
	return n
}

func (e *env) teamMembers(teamID int64) []int64 {
	e.t.Helper()
	t, err := e.store.Repos().Teams.Get(context.Background(), teamID)
	if err != nil {
		e.t.Fatal(err)
	}
	return t.MemberIDs
}

func TestTeamManagers(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	r := e.store.Repos()
	admin, dev, mgr := e.login(e.adminID), e.login(e.devID), e.login(e.otherID)
	team := fmt.Sprintf("/api/v1/my/teams/%d/members", e.teamID)

	other, err := r.Teams.Create(ctx, domain.Team{Name: "Search"})
	if err != nil {
		t.Fatal(err)
	}
	local, err := r.Users.Create(ctx, domain.User{Username: "ops-bot", Role: domain.RoleUser})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Users.SetPasswordHash(ctx, local.ID, "$2a$12$abcdefghijklmnopqrstuvabcdefghijklmnopqrstuvwxyz01234", "test"); err != nil {
		t.Fatal(err)
	}
	gone, err := r.Users.Create(ctx, domain.User{Username: "gone", Role: domain.RoleUser, Disabled: true})
	if err != nil {
		t.Fatal(err)
	}
	e.policy.Changed(ctx)

	// Nobody manages a team yet.
	if status, _ := mgr.list("/api/v1/my/teams"); status != 200 {
		t.Fatalf("my teams: %d", status)
	}
	if status, body, _ := mgr.do("POST", team, map[string]any{"userId": e.audID}); status != 403 {
		t.Fatalf("add before being a manager: %d %v", status, body)
	}
	if status, _, _ := mgr.get("/api/v1/my/teams/candidates"); status != 403 {
		t.Fatalf("candidates before being a manager: %d", status)
	}

	// Only admins choose managers, and never local users.
	managers := fmt.Sprintf("/api/v1/admin/teams/%d/managers", e.teamID)
	if status, _, _ := mgr.do("PUT", managers, map[string]any{"userIds": []int64{e.otherID}}); status != 403 {
		t.Fatalf("non-admin sets managers: %d", status)
	}
	if status, body, _ := admin.do("PUT", managers, map[string]any{"userIds": []int64{local.ID}}); status != 422 {
		t.Fatalf("local manager: %d %v", status, body)
	}
	if status, body, _ := admin.do("PUT", managers, map[string]any{"userIds": []int64{e.otherID}}); status != 204 {
		t.Fatalf("set managers: %d %v", status, body)
	}
	_, teams := admin.list("/api/v1/admin/teams")
	if ms := teams[0]["managers"].([]any); len(ms) != 1 || ms[0].(map[string]any)["username"] != "arash" {
		t.Fatalf("admin team view managers: %v", teams[0])
	}

	// The manager sees the team, its members and the grants a member receives.
	_, me, _ := mgr.get("/api/v1/me")
	if mt := me["managedTeams"].([]any); len(mt) != 1 || mt[0].(map[string]any)["name"] != "Payments" {
		t.Fatalf("me.managedTeams: %v", me["managedTeams"])
	}
	_, mine := mgr.list("/api/v1/my/teams")
	if len(mine) != 1 || len(mine[0]["members"].([]any)) != 1 || len(mine[0]["grants"].([]any)) != 1 {
		t.Fatalf("my teams: %v", mine)
	}
	_, cands := mgr.list("/api/v1/my/teams/candidates")
	for _, c := range cands {
		if c["username"] == "ops-bot" || c["username"] == "gone" {
			t.Fatalf("candidates list a local or disabled user: %v", cands)
		}
	}

	// Adding a member gives them the team's grants; adding again changes nothing.
	if status, body, _ := mgr.do("POST", team, map[string]any{"userId": e.audID}); status != 204 {
		t.Fatalf("add member: %d %v", status, body)
	}
	if status, _, _ := mgr.do("POST", team, map[string]any{"userId": e.audID}); status != 204 {
		t.Fatalf("add member again: %d", status)
	}
	if n := e.teamChanges("add_member", "arash"); n != 1 {
		t.Fatalf("add_member audit events: %d", n)
	}
	_, access, _ := e.login(e.audID).get("/api/v1/my/access")
	if ts := access["teams"].([]any); len(ts) != 1 || ts[0].(map[string]any)["name"] != "Payments" {
		t.Fatalf("new member's teams: %v", access["teams"])
	}

	// Refused: local and disabled users, unknown users, other teams, admin routes.
	for _, id := range []int64{local.ID, gone.ID, 9999} {
		if status, body, _ := mgr.do("POST", team, map[string]any{"userId": id}); status != 422 {
			t.Fatalf("add user %d: %d %v", id, status, body)
		}
	}
	if status, _, _ := mgr.do("POST", fmt.Sprintf("/api/v1/my/teams/%d/members", other.ID), map[string]any{"userId": e.audID}); status != 403 {
		t.Fatalf("add to a team not managed: %d", status)
	}
	if status, _, _ := mgr.do("POST", "/api/v1/my/teams/9999/members", map[string]any{"userId": e.audID}); status != 403 {
		t.Fatalf("add to an unknown team: %d", status)
	}
	if status, _, _ := mgr.do("PUT", fmt.Sprintf("/api/v1/admin/teams/%d/members", e.teamID), map[string]any{"userIds": []int64{}}); status != 403 {
		t.Fatalf("manager uses the admin route: %d", status)
	}
	if status, _, _ := dev.do("DELETE", fmt.Sprintf("%s/%d", team, e.audID), nil); status != 403 {
		t.Fatalf("member (not manager) removes: %d", status)
	}

	// Removing a member, also one an admin added.
	if status, _, _ := mgr.do("DELETE", fmt.Sprintf("%s/%d", team, e.devID), nil); status != 204 {
		t.Fatalf("remove member: %d", status)
	}
	if status, _, _ := mgr.do("DELETE", fmt.Sprintf("%s/%d", team, e.devID), nil); status != 204 {
		t.Fatalf("remove member again: %d", status)
	}
	if n := e.teamChanges("remove_member", "arash"); n != 1 {
		t.Fatalf("remove_member audit events: %d", n)
	}
	if got := e.teamMembers(e.teamID); !slices.Equal(got, []int64{e.audID}) {
		t.Fatalf("members: %v", got)
	}
	if _, access, _ := dev.get("/api/v1/my/access"); len(access["teams"].([]any)) != 0 {
		t.Fatalf("removed member still in the team: %v", access["teams"])
	}

	// No longer a manager: refused again.
	if status, _, _ := admin.do("PUT", managers, map[string]any{"userIds": []int64{}}); status != 204 {
		t.Fatalf("clear managers: %d", status)
	}
	if status, _, _ := mgr.do("POST", team, map[string]any{"userId": e.devID}); status != 403 {
		t.Fatalf("add after losing the manager role: %d", status)
	}
}

func TestTeamAccessRequests(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	r := e.store.Repos()
	admin, mgr, dev := e.login(e.adminID), e.login(e.otherID), e.login(e.devID)
	if status, _, _ := admin.do("PUT", fmt.Sprintf("/api/v1/admin/teams/%d/managers", e.teamID), map[string]any{"userIds": []int64{e.otherID}}); status != 204 {
		t.Fatalf("set managers: %d", status)
	}
	other, err := r.Teams.Create(ctx, domain.Team{Name: "Search"})
	if err != nil {
		t.Fatal(err)
	}
	e.policy.Changed(ctx)
	ask := func(c *client, teamID int64, ns string) (int, map[string]any) {
		status, m, _ := c.do("POST", "/api/v1/access-requests", map[string]any{
			"scope":   map[string]any{"kind": "namespace", "cluster": "prod-1", "namespace": ns},
			"actions": []string{"logs"}, "durationHours": 24, "reason": "the team needs it", "teamId": teamID})
		return status, m
	}

	// Only a manager of that team may ask for it.
	if status, m := ask(dev, e.teamID, "search-api"); status != 403 {
		t.Fatalf("member (not manager) asks for the team: %d %v", status, m)
	}
	if status, m := ask(mgr, other.ID, "search-api"); status != 403 {
		t.Fatalf("manager asks for another team: %d %v", status, m)
	}
	status, m := ask(mgr, e.teamID, "search-api")
	if status != 201 || m["team"].(map[string]any)["name"] != "Payments" || m["username"] != "arash" {
		t.Fatalf("team request: %d %v", status, m)
	}
	id := fmt.Sprint(int64(m["id"].(float64)))

	// The team's managers see it on My teams.
	_, mine := mgr.list("/api/v1/my/teams")
	if reqs := mine[0]["requests"].([]any); len(reqs) != 1 || reqs[0].(map[string]any)["status"] != "pending" {
		t.Fatalf("my teams requests: %v", mine[0]["requests"])
	}

	// Approving grants the team, so its members get access; the manager does not.
	status, m, _ = admin.do("POST", "/api/v1/access-requests/"+id+"/approve", map[string]any{"note": "ok"})
	if status != 200 || m["status"] != "approved" {
		t.Fatalf("approve: %d %v", status, m)
	}
	g, err := r.Grants.Get(ctx, int64(m["resultingGrantId"].(float64)))
	if err != nil {
		t.Fatal(err)
	}
	if g.HolderKind != domain.HolderTeam || g.TeamID != e.teamID || g.UserID != 0 {
		t.Fatalf("grant holder: %+v", g)
	}
	for _, c := range []struct {
		who  *client
		want bool
	}{{dev, true}, {mgr, false}} {
		_, access, _ := c.who.get("/api/v1/my/access")
		found := false
		for _, x := range access["grants"].([]any) {
			if int64(x.(map[string]any)["id"].(float64)) == g.ID {
				found = true
			}
		}
		if found != c.want {
			t.Fatalf("user %d has the team grant: %v, want %v", c.who.userID, found, c.want)
		}
	}

	// A second request on the same scope adds to the team's grant.
	_, m = ask(mgr, e.teamID, "search-api")
	id = fmt.Sprint(int64(m["id"].(float64)))
	status, m, _ = admin.do("POST", "/api/v1/access-requests/"+id+"/approve", map[string]any{"actions": []string{"logs", "exec"}, "durationHours": 48})
	if status != 200 || int64(m["resultingGrantId"].(float64)) != g.ID {
		t.Fatalf("merge into the team grant: %d %v", status, m)
	}

	_, m = ask(mgr, e.teamID, "billing")
	reqID := int64(m["id"].(float64))

	// Deleting the team removes its requests.
	if status, _, _ := admin.do("DELETE", fmt.Sprintf("/api/v1/admin/teams/%d", e.teamID), nil); status != 204 {
		t.Fatalf("delete team: %d", status)
	}
	if _, err := r.Requests.Get(ctx, reqID); err == nil {
		t.Fatal("team request survived its team")
	}
}
