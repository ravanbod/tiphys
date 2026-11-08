package postgres

import (
	"slices"
	"testing"
	"time"

	"tiphys/internal/application"
	"tiphys/internal/domain"
)

func TestUsers(t *testing.T) {
	f := newFixture(t)
	u, err := f.r.Users.Create(f.ctx, domain.User{Username: "Alice", DisplayName: "Alice A", Role: domain.RoleAdmin, KeycloakSub: "sub-1", UpdatedBy: "boot"})
	must(t, err)
	if u.ID == 0 || u.CreatedAt.IsZero() || u.Role != domain.RoleAdmin || u.KeycloakSub != "sub-1" || u.UpdatedBy != "boot" {
		t.Fatalf("unexpected user %+v", u)
	}

	for name, get := range map[string]func() (domain.User, error){
		"id":       func() (domain.User, error) { return f.r.Users.Get(f.ctx, u.ID) },
		"username": func() (domain.User, error) { return f.r.Users.GetByUsername(f.ctx, "aLiCe") },
		"sub":      func() (domain.User, error) { return f.r.Users.GetByKeycloakSub(f.ctx, "sub-1") },
	} {
		got, err := get()
		if err != nil || got.ID != u.ID || got.Username != "Alice" {
			t.Fatalf("get by %s: %+v %v", name, got, err)
		}
	}

	_, err = f.r.Users.Get(f.ctx, 9999)
	isErr(t, err, application.ErrNotFound)
	_, err = f.r.Users.GetByUsername(f.ctx, "nobody")
	isErr(t, err, application.ErrNotFound)
	_, err = f.r.Users.GetByKeycloakSub(f.ctx, "nope")
	isErr(t, err, application.ErrNotFound)

	// Usernames are unique case-insensitively; the sub is unique when set.
	_, err = f.r.Users.Create(f.ctx, domain.User{Username: "ALICE", Role: domain.RoleUser})
	isErr(t, err, application.ErrConflict)
	_, err = f.r.Users.Create(f.ctx, domain.User{Username: "other", Role: domain.RoleUser, KeycloakSub: "sub-1"})
	isErr(t, err, application.ErrConflict)
	// Several users without a sub are fine (NULL, not '').
	f.user("nosub1", domain.RoleUser)
	f.user("nosub2", domain.RoleAuditor)
	// Invalid role is a CHECK violation.
	_, err = f.r.Users.Create(f.ctx, domain.User{Username: "weird", Role: "root"})
	isCheckViolation(t, err)

	now := time.Now().UTC().Truncate(time.Microsecond)
	u.DisplayName, u.Role, u.Disabled, u.LastLoginAt, u.UpdatedBy = "Alice B", domain.RoleAuditor, true, &now, "admin"
	must(t, f.r.Users.Update(f.ctx, u))
	got, err := f.r.Users.Get(f.ctx, u.ID)
	must(t, err)
	if got.DisplayName != "Alice B" || got.Role != domain.RoleAuditor || !got.Disabled || got.LastLoginAt == nil || !got.LastLoginAt.Equal(now) || got.UpdatedBy != "admin" {
		t.Fatalf("update not stored: %+v", got)
	}
	// Renaming to another user's name conflicts.
	got.Username = "nosub1"
	isErr(t, f.r.Users.Update(f.ctx, got), application.ErrConflict)
	isErr(t, f.r.Users.Update(f.ctx, domain.User{ID: 9999, Username: "x", Role: domain.RoleUser}), application.ErrNotFound)

	us, err := f.r.Users.List(f.ctx)
	must(t, err)
	if len(us) != 3 {
		t.Fatalf("list: %d users", len(us))
	}
}

func TestLocalUsers(t *testing.T) {
	f := newFixture(t)
	local := f.user("bot", domain.RoleUser)
	must(t, f.r.Users.SetPasswordHash(f.ctx, local.ID, "$2a$12$hash", "admin"))
	got, err := f.r.Users.GetByUsername(f.ctx, "bot")
	must(t, err)
	hash, err := f.r.Users.PasswordHash(f.ctx, local.ID)
	must(t, err)
	if !got.Local || hash != "$2a$12$hash" || got.UpdatedBy != "admin" {
		t.Fatalf("local user %+v, hash %q", got, hash)
	}
	// Update keeps the hash; binding a Keycloak sub to a local user is refused.
	got.DisplayName = "Bot"
	must(t, f.r.Users.Update(f.ctx, got))
	if h, _ := f.r.Users.PasswordHash(f.ctx, local.ID); h != hash {
		t.Fatalf("hash after update: %q", h)
	}
	got.KeycloakSub = "sub-bot"
	isCheckViolation(t, f.r.Users.Update(f.ctx, got))

	// A Keycloak user never gets a password.
	kc, err := f.r.Users.Create(f.ctx, domain.User{Username: "kc", Role: domain.RoleUser, KeycloakSub: "sub-kc"})
	must(t, err)
	isErr(t, f.r.Users.SetPasswordHash(f.ctx, kc.ID, "$2a$12$hash", "admin"), application.ErrNotFound)
	if h, err := f.r.Users.PasswordHash(f.ctx, kc.ID); err != nil || h != "" {
		t.Fatalf("keycloak user hash %q %v", h, err)
	}
	if kc.Local {
		t.Fatal("keycloak user is local")
	}
}

func TestCountActiveAdmins(t *testing.T) {
	f := newFixture(t)
	n, err := f.r.Users.CountActiveAdmins(f.ctx)
	must(t, err)
	if n != 0 {
		t.Fatalf("want 0 got %d", n)
	}
	a1 := f.user("a1", domain.RoleAdmin)
	f.user("a2", domain.RoleAdmin)
	f.user("u1", domain.RoleUser)
	a1.Disabled = true
	must(t, f.r.Users.Update(f.ctx, a1))
	n, err = f.r.Users.CountActiveAdmins(f.ctx)
	must(t, err)
	if n != 1 {
		t.Fatalf("want 1 got %d", n)
	}
}

func TestTeams(t *testing.T) {
	f := newFixture(t)
	u1, u2, u3 := f.user("u1", domain.RoleUser), f.user("u2", domain.RoleUser), f.user("u3", domain.RoleUser)
	a := f.team("alpha")
	b := f.team("beta")
	_, err := f.r.Teams.Create(f.ctx, domain.Team{Name: "alpha"})
	isErr(t, err, application.ErrConflict)

	must(t, f.r.Teams.SetMembers(f.ctx, a.ID, []int64{u1.ID, u2.ID}))
	must(t, f.r.Teams.SetMembers(f.ctx, b.ID, []int64{u2.ID}))
	ts, err := f.r.Teams.List(f.ctx)
	must(t, err)
	if len(ts) != 2 || !slices.Equal(ts[0].MemberIDs, []int64{u1.ID, u2.ID}) || !slices.Equal(ts[1].MemberIDs, []int64{u2.ID}) {
		t.Fatalf("list members: %+v", ts)
	}
	// Replacing the set removes u1 and adds u3.
	must(t, f.r.Teams.SetMembers(f.ctx, a.ID, []int64{u2.ID, u3.ID, u3.ID}))
	got, err := f.r.Teams.Get(f.ctx, a.ID)
	must(t, err)
	if !slices.Equal(got.MemberIDs, []int64{u2.ID, u3.ID}) {
		t.Fatalf("members %v", got.MemberIDs)
	}
	ids, err := f.r.Teams.TeamIDsOfUser(f.ctx, u2.ID)
	must(t, err)
	if !slices.Equal(ids, []int64{a.ID, b.ID}) {
		t.Fatalf("team ids of u2: %v", ids)
	}
	must(t, f.r.Teams.SetMembers(f.ctx, a.ID, nil))
	got, _ = f.r.Teams.Get(f.ctx, a.ID)
	if got.MemberIDs == nil || len(got.MemberIDs) != 0 {
		t.Fatalf("want empty non-nil members, got %#v", got.MemberIDs)
	}

	isErr(t, f.r.Teams.SetMembers(f.ctx, b.ID, []int64{u2.ID, 9999}), application.ErrConflict)
	// The failed call must not have changed anything.
	got, _ = f.r.Teams.Get(f.ctx, b.ID)
	if !slices.Equal(got.MemberIDs, []int64{u2.ID}) {
		t.Fatalf("failed SetMembers modified the team: %v", got.MemberIDs)
	}
	isErr(t, f.r.Teams.SetMembers(f.ctx, 9999, nil), application.ErrNotFound)
	_, err = f.r.Teams.Get(f.ctx, 9999)
	isErr(t, err, application.ErrNotFound)

	// Managers are kept apart from members.
	must(t, f.r.Teams.SetManagers(f.ctx, b.ID, []int64{u3.ID, u1.ID}))
	got, _ = f.r.Teams.Get(f.ctx, b.ID)
	if !slices.Equal(got.ManagerIDs, []int64{u1.ID, u3.ID}) || !slices.Equal(got.MemberIDs, []int64{u2.ID}) {
		t.Fatalf("managers %v members %v", got.ManagerIDs, got.MemberIDs)
	}
	isErr(t, f.r.Teams.SetManagers(f.ctx, b.ID, []int64{9999}), application.ErrConflict)
	got, _ = f.r.Teams.Get(f.ctx, a.ID)
	if got.ManagerIDs == nil || len(got.ManagerIDs) != 0 {
		t.Fatalf("want empty non-nil managers, got %#v", got.ManagerIDs)
	}

	// One member at a time, reporting whether anything changed.
	added, err := f.r.Teams.AddMember(f.ctx, b.ID, u3.ID)
	must(t, err)
	again, err := f.r.Teams.AddMember(f.ctx, b.ID, u3.ID)
	must(t, err)
	if !added || again {
		t.Fatalf("add member: %v then %v", added, again)
	}
	removed, err := f.r.Teams.RemoveMember(f.ctx, b.ID, u2.ID)
	must(t, err)
	again, err = f.r.Teams.RemoveMember(f.ctx, b.ID, u2.ID)
	must(t, err)
	if !removed || again {
		t.Fatalf("remove member: %v then %v", removed, again)
	}
	got, _ = f.r.Teams.Get(f.ctx, b.ID)
	if !slices.Equal(got.MemberIDs, []int64{u3.ID}) {
		t.Fatalf("members after add/remove: %v", got.MemberIDs)
	}
	_, err = f.r.Teams.AddMember(f.ctx, b.ID, 9999)
	isErr(t, err, application.ErrConflict)
	_, err = f.r.Teams.AddMember(f.ctx, 9999, u1.ID)
	isErr(t, err, application.ErrNotFound)
	_, err = f.r.Teams.RemoveMember(f.ctx, 9999, u1.ID)
	isErr(t, err, application.ErrNotFound)
	must(t, f.r.Teams.SetMembers(f.ctx, b.ID, []int64{u2.ID}))

	b.Description, b.UpdatedBy = "second", "admin"
	must(t, f.r.Teams.Update(f.ctx, b))
	got, _ = f.r.Teams.Get(f.ctx, b.ID)
	if got.Description != "second" || got.UpdatedBy != "admin" {
		t.Fatalf("update: %+v", got)
	}
	isErr(t, f.r.Teams.Update(f.ctx, domain.Team{ID: 9999, Name: "x"}), application.ErrNotFound)
	b.Name = "alpha"
	isErr(t, f.r.Teams.Update(f.ctx, b), application.ErrConflict)

	must(t, f.r.Teams.Delete(f.ctx, b.ID))
	isErr(t, f.r.Teams.Delete(f.ctx, b.ID), application.ErrNotFound)
	ids, _ = f.r.Teams.TeamIDsOfUser(f.ctx, u2.ID)
	if len(ids) != 0 { // a was emptied above, b is deleted
		t.Fatalf("membership of deleted team remains: %v", ids)
	}
}
