package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"testing"
	"time"

	"go.uber.org/zap"

	"tiphys/internal/application"
	"tiphys/internal/domain"
)

func withLocalUsers(c *application.AuthConfig) { c.LocalUsers = true }

// localLogin posts the login form as the browser would and returns the status,
// the body and the session cookie token.
func (e *env) localLogin(username, password, origin string) (int, map[string]any, string) {
	e.t.Helper()
	b, _ := json.Marshal(map[string]string{"username": username, "password": password, "next": "/workloads"})
	req, _ := http.NewRequest("POST", e.srv.URL+"/auth/local-login", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	var token string
	for _, ck := range resp.Cookies() {
		if ck.Name == "tiphys_session" {
			token = ck.Value
		}
	}
	return resp.StatusCode, m, token
}

// meAs returns the /me status and body for a session token.
func (e *env) meAs(token string) (int, map[string]any) {
	e.t.Helper()
	status, me, _ := (&client{e: e, token: token}).get("/api/v1/me")
	return status, me
}

func TestLocalUsers(t *testing.T) {
	e := newEnvAuth(t, nil, withLocalUsers)
	ctx := context.Background()
	admin := e.login(e.adminID)
	const pw = "correct horse battery"

	if status, _, raw := (&client{e: e}).get("/auth/options"); status != 200 || !bytes.Contains(raw, []byte(`"localLogin":true`)) {
		t.Fatalf("options: %d %s", status, raw)
	}

	// Admins create a local user with a password; short passwords are refused.
	if status, body, _ := admin.do("POST", "/api/v1/admin/users", map[string]any{"username": "ops-bot", "role": "user", "password": "short"}); status != 422 {
		t.Fatalf("short password: %d %v", status, body)
	}
	status, body, _ := admin.do("POST", "/api/v1/admin/users", map[string]any{"username": "ops-bot", "role": "user", "password": pw})
	if status != 201 || body["local"] != true || body["keycloakBound"] != false {
		t.Fatalf("create local user: %d %v", status, body)
	}
	id := int64(body["id"].(float64))
	evs, _ := e.store.Repos().Audit.List(ctx, application.AuditFilter{EventType: domain.EventAdminChange})
	for _, ev := range evs {
		if raw, _ := json.Marshal(ev.Details); bytes.Contains(raw, []byte(pw)) || bytes.Contains(raw, []byte("$2a$")) {
			t.Fatalf("password or hash in audit details: %s", raw)
		}
	}

	// Login: only the portal's own origin, and the right password.
	if status, _, _ := e.localLogin("ops-bot", pw, ""); status != 403 {
		t.Fatalf("no origin: %d", status)
	}
	if status, _, _ := e.localLogin("ops-bot", pw, "http://evil.test"); status != 403 {
		t.Fatalf("foreign origin: %d", status)
	}
	if status, m, tok := e.localLogin("ops-bot", "wrong password!!", publicURL); status != 401 || errCode(m) != "invalid_credentials" || tok != "" {
		t.Fatalf("wrong password: %d %v", status, m)
	}
	status, m, token := e.localLogin("OPS-BOT", pw, publicURL)
	if status != 200 || m["redirect"] != "/workloads" || token == "" {
		t.Fatalf("login: %d %v", status, m)
	}
	if status, me := e.meAs(token); status != 200 || me["username"] != "ops-bot" {
		t.Fatalf("me: %d %v", status, me)
	}
	logins, _ := e.store.Repos().Audit.List(ctx, application.AuditFilter{EventType: domain.EventLogin})
	var ok, denied bool
	for _, ev := range logins {
		if ev.Username == "ops-bot" && ev.Details["method"] == "local" {
			ok = ok || ev.Result == domain.ResultSucceeded
			denied = denied || (ev.Result == domain.ResultDenied && ev.Reason == "invalid_credentials")
		}
	}
	if !ok || !denied {
		t.Fatalf("login audit: succeeded %v, denied %v", ok, denied)
	}

	// Unknown users and Keycloak users fail the same way.
	for _, u := range []string{"nobody", "sara"} {
		if status, m, _ := e.localLogin(u, pw, publicURL); status != 401 || errCode(m) != "invalid_credentials" {
			t.Fatalf("%s: %d %v", u, status, m)
		}
	}

	// The session survives the 5-minute re-check without Keycloak, and ends
	// after 8 hours.
	start := e.clock.Now()
	e.clock.Set(start.Add(6 * time.Minute))
	if status, _ := e.meAs(token); status != 200 {
		t.Fatalf("after refresh: %d", status)
	}

	// A Keycloak login with the same username is not bound to the local user.
	if _, err := e.loginAs(application.OIDCClaims{Subject: "sub-ops", PreferredUsername: "ops-bot"}); err == nil {
		t.Fatal("Keycloak login took over a local user")
	}

	// Setting a password: only for local users; it ends the user's sessions.
	if status, m, _ := admin.do("PUT", fmt.Sprintf("/api/v1/admin/users/%d/password", e.devID), map[string]any{"password": pw}); status != 422 {
		t.Fatalf("password for a Keycloak user: %d %v", status, m)
	}
	const pw2 = "another long passphrase"
	if status, m, _ := admin.do("PUT", fmt.Sprintf("/api/v1/admin/users/%d/password", id), map[string]any{"password": pw2}); status != 200 {
		t.Fatalf("set password: %d %v", status, m)
	}
	if status, _ := e.meAs(token); status != 401 {
		t.Fatalf("session after password change: %d", status)
	}
	if status, _, _ := e.localLogin("ops-bot", pw, publicURL); status != 401 {
		t.Fatalf("old password: %d", status)
	}
	status, _, token = e.localLogin("ops-bot", pw2, publicURL)
	if status != 200 {
		t.Fatalf("new password: %d", status)
	}
	e.clock.Set(e.clock.Now().Add(8*time.Hour + time.Minute))
	if status, _ := e.meAs(token); status != 401 {
		t.Fatalf("after 8 hours: %d", status)
	}

	// A disabled user is told so only with the right password.
	if status, m, _ := admin.do("PATCH", fmt.Sprintf("/api/v1/admin/users/%d", id), map[string]any{"disabled": true}); status != 200 {
		t.Fatalf("disable: %d %v", status, m)
	}
	if status, m, _ := e.localLogin("ops-bot", pw2, publicURL); status != 403 || errCode(m) != "user_disabled" {
		t.Fatalf("disabled: %d %v", status, m)
	}

	// Five attempts per username per minute.
	for i := 0; i < 5; i++ {
		e.localLogin("guess", "not the password", publicURL)
	}
	if status, m, _ := e.localLogin("guess", "not the password", publicURL); status != 429 {
		t.Fatalf("rate limit: %d %v", status, m)
	}
}

func TestLocalUsersOff(t *testing.T) {
	e := newEnv(t)
	admin := e.login(e.adminID)
	if status, _, raw := (&client{e: e}).get("/auth/options"); status != 200 || !bytes.Contains(raw, []byte(`"localLogin":false`)) {
		t.Fatalf("options: %d %s", status, raw)
	}
	if status, _, _ := admin.do("POST", "/api/v1/admin/users", map[string]any{"username": "ops-bot", "role": "user", "password": "correct horse battery"}); status != 422 {
		t.Fatalf("create local user while off: %d", status)
	}
	if status, _, _ := e.localLogin("ops-bot", "correct horse battery", publicURL); status != 404 {
		t.Fatalf("login route while off: %d", status)
	}
}

// A local user's open session ends at the next re-check once the feature is
// turned off (simulated with a second portal on the same database).
func TestLocalSessionEndsWhenTurnedOff(t *testing.T) {
	on := newEnvAuth(t, nil, withLocalUsers)
	admin := on.login(on.adminID)
	if status, m, _ := admin.do("POST", "/api/v1/admin/users", map[string]any{"username": "ops-bot", "role": "user", "password": "correct horse battery"}); status != 201 {
		t.Fatalf("create: %d %v", status, m)
	}
	status, _, token := on.localLogin("ops-bot", "correct horse battery", publicURL)
	if status != 200 {
		t.Fatalf("login: %d", status)
	}
	off := application.NewAuthService(&stubOIDC{}, on.sealer, on.store, on.policy, on.audit, on.clock, application.AuthConfig{}, zap.NewNop())
	on.clock.Set(on.clock.Now().Add(6 * time.Minute))
	if _, err := off.Authenticate(context.Background(), token, "127.0.0.1", "test"); err == nil {
		t.Fatal("local session kept with local users off")
	}
}
