package integration

import (
	"context"
	"encoding/json"
	"testing"

	"tiphys/internal/application"
	"tiphys/internal/domain"
)

// loginAs runs the real login flow (BeginLogin, CompleteLogin) with the claims
// Keycloak would return, and returns the session cookie token.
func (e *env) loginAs(claims application.OIDCClaims) (string, error) {
	e.t.Helper()
	e.oidc.claims = claims
	_, sealed, err := e.auth.BeginLogin("/")
	if err != nil {
		e.t.Fatal(err)
	}
	raw, err := e.sealer.Open(sealed)
	if err != nil {
		e.t.Fatal(err)
	}
	var tx struct {
		State string `json:"s"`
	}
	if err := json.Unmarshal(raw, &tx); err != nil {
		e.t.Fatal(err)
	}
	token, _, err := e.auth.CompleteLogin(context.Background(), "code", tx.State, sealed, "127.0.0.1", "test")
	return token, err
}

func (e *env) roleOf(username string) domain.Role {
	e.t.Helper()
	u, err := e.store.Repos().Users.GetByUsername(context.Background(), username)
	if err != nil {
		e.t.Fatal(err)
	}
	return u.Role
}

func TestAdminGroupsPromoteAtEveryLogin(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()

	// A new user in an admin group is admin from the first login.
	if _, err := e.loginAs(application.OIDCClaims{Subject: "sub-new", PreferredUsername: "newadmin", Groups: []string{"/tiphys-admins"}}); err != nil {
		t.Fatal(err)
	}
	if got := e.roleOf("newadmin"); got != domain.RoleAdmin {
		t.Fatalf("new group member: role %s", got)
	}

	// An existing user who logged in before the group existed is promoted at
	// the next login.
	if _, err := e.loginAs(application.OIDCClaims{Subject: "sub-sara", PreferredUsername: "sara", Groups: []string{"devs"}}); err != nil {
		t.Fatal(err)
	}
	if got := e.roleOf("sara"); got != domain.RoleUser {
		t.Fatalf("non-member promoted: %s", got)
	}
	token, err := e.loginAs(application.OIDCClaims{Subject: "sub-sara", PreferredUsername: "sara", Groups: []string{"devs", "/platform/sre"}})
	if err != nil {
		t.Fatal(err)
	}
	if got := e.roleOf("sara"); got != domain.RoleAdmin {
		t.Fatalf("existing group member: role %s", got)
	}
	// The session works as an admin right away.
	c := &client{e: e, token: token}
	status, me, _ := c.get("/api/v1/me")
	if status != 200 || me["role"] != "admin" {
		t.Fatalf("me after promotion: %d %v", status, me)
	}
	c.csrf, _ = me["csrfToken"].(string)
	if status, _, _ := c.get("/api/v1/admin/users"); status != 200 {
		t.Fatalf("admin API: %d", status)
	}

	// Matching ignores case and a leading slash; an unrelated group does not match.
	if _, err := e.loginAs(application.OIDCClaims{Subject: "sub-arash", PreferredUsername: "arash", Groups: []string{"/Tiphys-Admins-Extra"}}); err != nil {
		t.Fatal(err)
	}
	if got := e.roleOf("arash"); got != domain.RoleUser {
		t.Fatalf("near-miss group promoted: %s", got)
	}
	if _, err := e.loginAs(application.OIDCClaims{Subject: "sub-arash", PreferredUsername: "arash", Groups: []string{"TIPHYS-ADMINS"}}); err != nil {
		t.Fatal(err)
	}
	if got := e.roleOf("arash"); got != domain.RoleAdmin {
		t.Fatalf("case-insensitive match: %s", got)
	}

	// Leaving the group never demotes.
	if _, err := e.loginAs(application.OIDCClaims{Subject: "sub-arash", PreferredUsername: "arash"}); err != nil {
		t.Fatal(err)
	}
	if got := e.roleOf("arash"); got != domain.RoleAdmin {
		t.Fatalf("demoted by missing group: %s", got)
	}

	// Each promotion is an audited admin change with before and after.
	evs, _ := e.store.Repos().Audit.List(ctx, application.AuditFilter{EventType: domain.EventAdminChange})
	promotions := 0
	for _, ev := range evs {
		if ev.Details["operation"] == "promote_by_group" {
			promotions++
			if ev.Details["before"] == nil || ev.Details["after"] == nil || ev.Details["group"] == nil {
				t.Fatalf("promotion event lacks details: %+v", ev.Details)
			}
		}
	}
	if promotions != 3 {
		t.Fatalf("promotion audit events: %d, want 3", promotions)
	}
}

func TestAdminGroupDoesNotPromoteDisabledUsers(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	u, _ := e.store.Repos().Users.GetByUsername(ctx, "arash")
	u.Disabled = true
	if err := e.store.Repos().Users.Update(ctx, u); err != nil {
		t.Fatal(err)
	}
	if _, err := e.loginAs(application.OIDCClaims{Subject: "sub-arash", PreferredUsername: "arash", Groups: []string{"tiphys-admins"}}); err == nil {
		t.Fatal("a disabled user must not log in")
	}
	if got := e.roleOf("arash"); got != domain.RoleUser {
		t.Fatalf("disabled user promoted: %s", got)
	}
}

func TestBootstrapAdminsPromoteAtEveryLogin(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()

	// A listed user who already logged in as a regular user is promoted at the
	// next login; matching ignores case and surrounding spaces in the list.
	if _, err := e.loginAs(application.OIDCClaims{Subject: "sub-mina", PreferredUsername: "Mina"}); err != nil {
		t.Fatal(err)
	}
	if got := e.roleOf("mina"); got != domain.RoleAdmin {
		t.Fatalf("listed user: role %s", got)
	}

	// A user created as a regular user and listed later is promoted too.
	if _, err := e.loginAs(application.OIDCClaims{Subject: "sub-late", PreferredUsername: "late"}); err != nil {
		t.Fatal(err)
	}
	if got := e.roleOf("late"); got != domain.RoleUser {
		t.Fatalf("unlisted user promoted: %s", got)
	}
	u, _ := e.store.Repos().Users.GetByUsername(ctx, "mina")
	u.Role = domain.RoleUser
	if err := e.store.Repos().Users.Update(ctx, u); err != nil {
		t.Fatal(err)
	}
	if _, err := e.loginAs(application.OIDCClaims{Subject: "sub-mina", PreferredUsername: "mina"}); err != nil {
		t.Fatal(err)
	}
	if got := e.roleOf("mina"); got != domain.RoleAdmin {
		t.Fatalf("demoted listed user not promoted again: %s", got)
	}

	// A new listed user is admin from the first login.
	if _, err := e.loginAs(application.OIDCClaims{Subject: "sub-boot", PreferredUsername: "boot.admin"}); err != nil {
		t.Fatal(err)
	}
	if got := e.roleOf("boot.admin"); got != domain.RoleAdmin {
		t.Fatalf("new listed user: role %s", got)
	}

	evs, _ := e.store.Repos().Audit.List(ctx, application.AuditFilter{EventType: domain.EventAdminChange})
	promotions := 0
	for _, ev := range evs {
		if ev.Details["operation"] == "promote_by_username" {
			promotions++
			if ev.Details["before"] == nil || ev.Details["after"] == nil || ev.Details["group"] != nil {
				t.Fatalf("promotion event details: %+v", ev.Details)
			}
		}
	}
	if promotions != 3 {
		t.Fatalf("promotion audit events: %d, want 3", promotions)
	}
}

func TestBootstrapAdminDoesNotPromoteDisabledUsers(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	if _, err := e.loginAs(application.OIDCClaims{Subject: "sub-boot", PreferredUsername: "boot.admin"}); err != nil {
		t.Fatal(err)
	}
	u, _ := e.store.Repos().Users.GetByUsername(ctx, "boot.admin")
	u.Role, u.Disabled = domain.RoleUser, true
	if err := e.store.Repos().Users.Update(ctx, u); err != nil {
		t.Fatal(err)
	}
	if _, err := e.loginAs(application.OIDCClaims{Subject: "sub-boot", PreferredUsername: "boot.admin"}); err == nil {
		t.Fatal("a disabled user must not log in")
	}
	if got := e.roleOf("boot.admin"); got != domain.RoleUser {
		t.Fatalf("disabled user promoted: %s", got)
	}
}
