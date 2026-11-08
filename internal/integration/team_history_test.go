package integration

import (
	"fmt"
	"testing"
)

// A team's change history is the audit's admin changes of that team (by id),
// its creation and a rename included, and nothing of other teams.
func TestTeamChangeHistory(t *testing.T) {
	e := newEnv(t)
	admin := e.login(e.adminID)
	status, created, _ := admin.do("POST", "/api/v1/admin/teams", map[string]any{"name": "Billing"})
	if status != 201 && status != 200 {
		t.Fatalf("create team: %d %v", status, created)
	}
	id := int64(created["id"].(float64))
	if status, body, _ := admin.do("PATCH", fmt.Sprintf("/api/v1/admin/teams/%d", id), map[string]any{"name": "Billing ops"}); status != 200 {
		t.Fatalf("rename: %d %v", status, body)
	}
	if status, body, _ := admin.do("PUT", fmt.Sprintf("/api/v1/admin/teams/%d/members", e.teamID), map[string]any{"userIds": []int64{e.devID}}); status != 200 && status != 204 {
		t.Fatalf("other team's members: %d %v", status, body)
	}

	_, page, _ := admin.get(fmt.Sprintf("/api/v1/audit?eventType=admin.change&entity=team&entityId=%d", id))
	var ops []string
	for _, it := range page["items"].([]any) {
		d := it.(map[string]any)["details"].(map[string]any)
		if d["entity"] != "team" || d["id"] != float64(id) {
			t.Fatalf("not a change of team %d: %v", id, d)
		}
		ops = append(ops, d["operation"].(string))
	}
	if fmt.Sprint(ops) != "[update create]" {
		t.Fatalf("history of team %d: %v", id, ops)
	}
}
