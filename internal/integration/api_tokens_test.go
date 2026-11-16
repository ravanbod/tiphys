package integration

import (
	"context"
	"strings"
	"testing"
	"time"

	"tiphys/internal/application"
	"tiphys/internal/domain"
)

// createToken creates an API token as the admin and returns its id and value.
func (e *env) createToken(admin *client, body map[string]any) (int64, string) {
	e.t.Helper()
	status, m, _ := admin.do("POST", "/api/v1/admin/api-tokens", body)
	if status != 201 {
		e.t.Fatalf("create token: %d %v", status, m)
	}
	return int64(m["id"].(float64)), m["token"].(string)
}

func TestAPITokens(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	admin := e.login(e.adminID)
	auditor := e.login(e.audID)

	// Only an admin creates tokens; unknown permissions are refused.
	body := map[string]any{"name": "ci", "permissions": []string{"time_windows:read", "time_windows:create", "settings:read", "settings.break_glass:write"}}
	if status, _, _ := auditor.do("POST", "/api/v1/admin/api-tokens", body); status != 403 {
		t.Fatalf("auditor create: %d", status)
	}
	if status, m, _ := admin.do("POST", "/api/v1/admin/api-tokens", map[string]any{"name": "x", "permissions": []string{"grants:write"}}); status != 422 {
		t.Fatalf("unknown permission: %d %v", status, m)
	}
	id, value := e.createToken(admin, body)
	if !strings.HasPrefix(value, application.APITokenPrefix) {
		t.Fatalf("token value %q", value)
	}
	if status, m, _ := admin.do("POST", "/api/v1/admin/api-tokens", body); status != 422 {
		t.Fatalf("duplicate name: %d %v", status, m)
	}
	// The list (auditors may read it) never holds the value.
	status, list := auditor.list("/api/v1/admin/api-tokens")
	if status != 200 || len(list) != 1 || list[0]["token"] != nil || list[0]["status"] != "active" || !strings.HasPrefix(value, list[0]["prefix"].(string)) {
		t.Fatalf("list: %d %v", status, list)
	}

	tok := &client{e: e, bearer: value}
	// Allowed: read and create windows, without a CSRF token.
	if status, _ := tok.list("/api/v1/admin/time-windows"); status != 200 {
		t.Fatalf("token list windows: %d", status)
	}
	status, m, _ := tok.do("POST", "/api/v1/admin/time-windows", map[string]any{
		"name": "from ci", "scopeKind": "environment", "scopeValue": "prod", "actions": []string{"exec"},
		"daysOfWeek": []int{0}, "startTime": "09:00", "endTime": "18:00", "timezone": "Asia/Tehran",
		"effect": "allow_inside", "enabled": true})
	if status != 201 {
		t.Fatalf("token create window: %d %v", status, m)
	}
	winID := int64(m["id"].(float64))
	// Not granted: delete.
	if status, m, _ := tok.do("DELETE", "/api/v1/admin/time-windows/"+itoa(winID), nil); status != 403 || errCode(m) != "missing_permission" {
		t.Fatalf("token delete window: %d %v", status, m)
	}
	// Routes outside the token's catalog are refused whatever its permissions.
	for _, path := range []string{"/api/v1/me", "/api/v1/admin/users", "/api/v1/admin/api-tokens", "/api/v1/apps", "/api/v1/audit"} {
		if status, m, _ := tok.get(path); status != 403 || errCode(m) != "token_not_allowed" {
			t.Fatalf("token GET %s: %d %v", path, status, m)
		}
	}
	if status, m, _ := tok.do("POST", "/api/v1/admin/api-tokens", body); status != 403 || errCode(m) != "token_not_allowed" {
		t.Fatalf("token creates token: %d %v", status, m)
	}
	if status, _, _ := tok.do("PUT", "/api/v1/admin/settings", map[string]any{}); status != 403 {
		t.Fatalf("token PUT settings: %d", status)
	}

	// Settings: only the sections the token may write.
	if status, m, _ := tok.do("PATCH", "/api/v1/admin/settings", map[string]any{"breakGlassMode": "approval"}); status != 200 || m["breakGlassMode"] != "approval" {
		t.Fatalf("patch break-glass mode: %d %v", status, m)
	}
	if status, m, _ := tok.do("PATCH", "/api/v1/admin/settings", map[string]any{"breakGlassMode": "self", "retentionAuditDays": 10}); status != 403 || errCode(m) != "missing_permission" {
		t.Fatalf("patch retention: %d %v", status, m)
	}
	if status, m, _ := tok.do("PATCH", "/api/v1/admin/settings", map[string]any{"argoSyncClusterKinds": []string{}}); status != 422 {
		t.Fatalf("patch unknown field: %d %v", status, m)
	}
	if status, m, _ := tok.do("PATCH", "/api/v1/admin/settings", map[string]any{"breakGlassMode": "sometimes"}); status != 422 {
		t.Fatalf("patch invalid mode: %d %v", status, m)
	}
	if status, m, _ := tok.get("/api/v1/admin/settings"); status != 200 || m["breakGlassMode"] != "approval" || m["retentionAuditDays"].(float64) == 10 {
		t.Fatalf("settings after patch: %d %v", status, m)
	}

	// Its changes are audited under the token's name.
	evs, err := e.store.Repos().Audit.List(ctx, application.AuditFilter{EventType: domain.EventAdminChange})
	if err != nil {
		t.Fatal(err)
	}
	var byToken int
	for _, ev := range evs {
		if ev.Username == "token:ci" {
			byToken++
			if ev.UserID != nil || ev.Details["tokenId"] != float64(id) {
				t.Fatalf("token event: %+v", ev)
			}
		}
	}
	if byToken != 2 {
		t.Fatalf("events by the token: %d", byToken)
	}

	// Revoked: refused at once, and only once.
	if status, _, _ := auditor.do("POST", "/api/v1/admin/api-tokens/"+itoa(id)+"/revoke", nil); status != 403 {
		t.Fatalf("auditor revoke: %d", status)
	}
	if status, _, _ := admin.do("POST", "/api/v1/admin/api-tokens/"+itoa(id)+"/revoke", nil); status != 204 {
		t.Fatalf("revoke: %d", status)
	}
	if status, _, _ := admin.do("POST", "/api/v1/admin/api-tokens/"+itoa(id)+"/revoke", nil); status != 409 {
		t.Fatalf("revoke twice: %d", status)
	}
	if status, _ := tok.list("/api/v1/admin/time-windows"); status != 401 {
		t.Fatalf("revoked token: %d", status)
	}
	// The name is free again once revoked.
	e.createToken(admin, body)

	// Expiry, and values that were never issued.
	exp := e.clock.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	_, short := e.createToken(admin, map[string]any{"name": "short", "permissions": []string{"holidays:read"}, "expiresAt": exp})
	if status, _ := (&client{e: e, bearer: short}).list("/api/v1/admin/holidays"); status != 200 {
		t.Fatalf("short-lived token: %d", status)
	}
	e.clock.Set(e.clock.Now().Add(2 * time.Hour))
	if status, _ := (&client{e: e, bearer: short}).list("/api/v1/admin/holidays"); status != 401 {
		t.Fatalf("expired token: %d", status)
	}
	if status, _ := (&client{e: e, bearer: application.APITokenPrefix + "nope"}).list("/api/v1/admin/holidays"); status != 401 {
		t.Fatalf("unknown token: %d", status)
	}
}
