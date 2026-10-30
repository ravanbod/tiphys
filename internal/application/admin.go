package application

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"go.uber.org/zap"

	"tiphys/internal/domain"
)

// AdminService is configuration management: users, teams, grants, time
// rules, settings. Every mutation runs in a transaction together with its
// audit event (before and after values in details), then reloads the policy
// so the change takes effect at once, including for open streams.
type AdminService struct {
	store    Store
	audit    *AuditService
	policy   *PolicyService
	settings *SettingsService
	notify   *NotificationService
	auth     *AuthService
	streams  *StreamManager
	access   *AccessService
	clock    Clock
	logger   *zap.Logger
}

func NewAdminService(store Store, audit *AuditService, policy *PolicyService, settings *SettingsService, notify *NotificationService,
	auth *AuthService, streams *StreamManager, access *AccessService, clock Clock, logger *zap.Logger) *AdminService {
	return &AdminService{store: store, audit: audit, policy: policy, settings: settings, notify: notify, auth: auth,
		streams: streams, access: access, clock: clock, logger: logger}
}

// change describes one audited mutation.
type change struct {
	entity    string
	operation string
	id        any
	before    any
	after     any
	// notifyAdmin sends the "admin changes to roles or grants" notification.
	notifyAdmin bool
	summary     string
}

// mutate runs fn and the audit write in one transaction, then publishes the
// event, reloads policy and notifies.
func (s *AdminService) mutate(ctx context.Context, p Principal, c *change, fn func(Repositories) error) error {
	var ev domain.AuditEvent
	err := s.store.InTx(ctx, func(r Repositories) error {
		if err := fn(r); err != nil {
			return err
		}
		e := s.audit.Event(p, domain.EventAdminChange)
		e.Result = domain.ResultSucceeded
		e.Details = map[string]any{"entity": c.entity, "operation": c.operation, "id": c.id, "before": c.before, "after": c.after}
		var err error
		ev, err = s.audit.RecordIn(ctx, r, e)
		return err
	})
	if err != nil {
		return err
	}
	s.audit.Publish(ev)
	s.policy.Changed(ctx)
	if c.notifyAdmin {
		s.notify.Notify(NotificationMessage{
			Event: domain.NotifyAdminChanges, Title: "Admin change: " + c.entity + " " + c.operation,
			Fields: []NotificationField{{Name: "Who", Value: p.User.Username}, {Name: "What", Value: c.summary}},
			Link:   s.notify.Link("/audit?eventType=admin.change"),
		})
	}
	return nil
}

func (s *AdminService) requireAdmin(p Principal) error { return RequireRole(p, domain.RoleAdmin) }

// ---- Users ----

// UserWithTeams is a user with their team ids.
type UserWithTeams struct {
	User    domain.User
	TeamIDs []int64
}

// Users lists users (admin and auditor).
func (s *AdminService) Users(p Principal) ([]UserWithTeams, error) {
	if err := RequireRole(p, domain.RoleAdmin, domain.RoleAuditor); err != nil {
		return nil, err
	}
	snap := s.policy.Current()
	out := make([]UserWithTeams, 0, len(snap.Users))
	for _, u := range snap.Users {
		out = append(out, UserWithTeams{User: u, TeamIDs: snap.TeamIDsByUser[u.ID]})
	}
	sort.Slice(out, func(i, j int) bool {
		return strings.ToLower(out[i].User.Username) < strings.ToLower(out[j].User.Username)
	})
	return out, nil
}

// CreateUser pre-creates a user by Keycloak username; the sub is bound at
// first login. With a password it creates a local user instead, who logs in
// with that password and is never bound to Keycloak.
func (s *AdminService) CreateUser(ctx context.Context, p Principal, username, displayName string, role domain.Role, password string) (domain.User, error) {
	if err := s.requireAdmin(p); err != nil {
		return domain.User{}, err
	}
	username = strings.TrimSpace(username)
	if username == "" || strings.ContainsAny(username, " \t\r\n/") {
		return domain.User{}, Invalid("username", "a Keycloak username is required")
	}
	if !role.Valid() {
		return domain.User{}, Invalid("role", "invalid role")
	}
	if displayName == "" {
		displayName = username
	}
	local := password != ""
	var hash string
	if local {
		var err error
		if hash, err = s.localPasswordHash(password); err != nil {
			return domain.User{}, err
		}
	}
	var u domain.User
	err := s.mutate(ctx, p, &change{entity: "user", operation: "create", id: username, after: map[string]any{"username": username, "role": role, "local": local}, notifyAdmin: true,
		summary: fmt.Sprintf("created user %s (%s)", username, role)}, func(r Repositories) error {
		var err error
		u, err = r.Users.Create(ctx, domain.User{Username: username, DisplayName: displayName, Role: role, UpdatedBy: p.User.Username})
		if err != nil || !local {
			return err
		}
		u.Local = true
		return r.Users.SetPasswordHash(ctx, u.ID, hash, p.User.Username)
	})
	return u, err
}

// SetPassword replaces a local user's password and ends their sessions and
// streams. Keycloak users have no password here.
func (s *AdminService) SetPassword(ctx context.Context, p Principal, id int64, password string) (domain.User, error) {
	if err := s.requireAdmin(p); err != nil {
		return domain.User{}, err
	}
	hash, err := s.localPasswordHash(password)
	if err != nil {
		return domain.User{}, err
	}
	var u domain.User
	c := &change{entity: "user", operation: "set_password", id: id, notifyAdmin: true}
	err = s.mutate(ctx, p, c, func(r Repositories) error {
		var err error
		if u, err = r.Users.Get(ctx, id); err != nil {
			return err
		}
		if !u.Local {
			return Invalid("password", "only local users have a password; this user logs in through Keycloak")
		}
		c.after = map[string]any{"username": u.Username}
		c.summary = "set the password of " + u.Username
		return r.Users.SetPasswordHash(ctx, id, hash, p.User.Username)
	})
	if err != nil {
		return domain.User{}, err
	}
	s.auth.EndUserSessions(ctx, id)
	s.streams.TerminateUser(id, EndTerminatedByAdmin)
	return u, nil
}

func (s *AdminService) localPasswordHash(password string) (string, error) {
	if !s.auth.LocalUsersEnabled() {
		return "", Invalid("password", "local users are turned off (LOCAL_USERS_ENABLED)")
	}
	return hashPassword(password)
}

// UserPatch is a partial update of a user.
type UserPatch struct {
	Role        *domain.Role
	Disabled    *bool
	DisplayName *string
}

// UpdateUser changes role, disabled flag or display name. The last active
// admin can be neither demoted nor disabled. Disabling ends the user's
// sessions and streams.
func (s *AdminService) UpdateUser(ctx context.Context, p Principal, id int64, patch UserPatch) (domain.User, error) {
	if err := s.requireAdmin(p); err != nil {
		return domain.User{}, err
	}
	var after domain.User
	var disabledNow bool
	var before domain.User
	var ev domain.AuditEvent
	err := s.store.InTx(ctx, func(r Repositories) error {
		u, err := r.Users.Get(ctx, id)
		if err != nil {
			return err
		}
		before = u
		if patch.Role != nil {
			if !patch.Role.Valid() {
				return Invalid("role", "invalid role")
			}
			u.Role = *patch.Role
		}
		if patch.Disabled != nil {
			u.Disabled = *patch.Disabled
		}
		if patch.DisplayName != nil {
			u.DisplayName = *patch.DisplayName
		}
		wasActiveAdmin := before.Role == domain.RoleAdmin && !before.Disabled
		isActiveAdmin := u.Role == domain.RoleAdmin && !u.Disabled
		if wasActiveAdmin && !isActiveAdmin {
			n, err := r.Users.CountActiveAdmins(ctx)
			if err != nil {
				return err
			}
			if n <= 1 {
				return ErrLastAdmin
			}
		}
		u.UpdatedBy = p.User.Username
		if err := r.Users.Update(ctx, u); err != nil {
			return err
		}
		after = u
		disabledNow = u.Disabled && !before.Disabled

		e := s.audit.Event(p, domain.EventAdminChange)
		e.Result = domain.ResultSucceeded
		e.Details = map[string]any{"entity": "user", "operation": "update", "id": id,
			"before": userSummary(before), "after": userSummary(after)}
		ev, err = s.audit.RecordIn(ctx, r, e)
		return err
	})
	if err != nil {
		return domain.User{}, err
	}
	s.audit.Publish(ev)
	s.policy.Changed(ctx)
	if disabledNow {
		s.auth.EndUserSessions(ctx, id)
		s.streams.TerminateUser(id, EndUserDisabled)
	}
	if before.Role != after.Role || before.Disabled != after.Disabled {
		s.notify.Notify(NotificationMessage{
			Event: domain.NotifyAdminChanges, Title: "Admin change: user update",
			Fields: []NotificationField{{Name: "Who", Value: p.User.Username},
				{Name: "What", Value: fmt.Sprintf("%s: role %s→%s, disabled %v→%v", after.Username, before.Role, after.Role, before.Disabled, after.Disabled)}},
			Link: s.notify.Link("/admin/users"),
		})
	}
	return after, nil
}

func userSummary(u domain.User) map[string]any {
	return map[string]any{"username": u.Username, "role": u.Role, "disabled": u.Disabled, "displayName": u.DisplayName}
}

// ---- Teams ----

// Teams lists teams with members (admin and auditor).
func (s *AdminService) Teams(p Principal) ([]domain.Team, error) {
	if err := RequireRole(p, domain.RoleAdmin, domain.RoleAuditor); err != nil {
		return nil, err
	}
	snap := s.policy.Current()
	out := make([]domain.Team, 0, len(snap.Teams))
	for _, t := range snap.Teams {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (s *AdminService) CreateTeam(ctx context.Context, p Principal, name, description string) (domain.Team, error) {
	if err := s.requireAdmin(p); err != nil {
		return domain.Team{}, err
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return domain.Team{}, Invalid("name", "a name is required")
	}
	var t domain.Team
	err := s.mutate(ctx, p, &change{entity: "team", operation: "create", id: name, after: map[string]any{"name": name, "description": description}},
		func(r Repositories) error {
			var err error
			t, err = r.Teams.Create(ctx, domain.Team{Name: name, Description: description, UpdatedBy: p.User.Username})
			return err
		})
	return t, err
}

func (s *AdminService) UpdateTeam(ctx context.Context, p Principal, id int64, name, description *string) (domain.Team, error) {
	if err := s.requireAdmin(p); err != nil {
		return domain.Team{}, err
	}
	var t domain.Team
	c := &change{entity: "team", operation: "update", id: id}
	err := s.mutate(ctx, p, c, func(r Repositories) error {
		cur, err := r.Teams.Get(ctx, id)
		if err != nil {
			return err
		}
		c.before = map[string]any{"name": cur.Name, "description": cur.Description}
		t = cur
		if name != nil {
			if strings.TrimSpace(*name) == "" {
				return Invalid("name", "a name is required")
			}
			t.Name = strings.TrimSpace(*name)
		}
		if description != nil {
			t.Description = *description
		}
		t.UpdatedBy = p.User.Username
		c.after = map[string]any{"name": t.Name, "description": t.Description}
		return r.Teams.Update(ctx, t)
	})
	return t, err
}

func (s *AdminService) DeleteTeam(ctx context.Context, p Principal, id int64) error {
	if err := s.requireAdmin(p); err != nil {
		return err
	}
	c := &change{entity: "team", operation: "delete", id: id, notifyAdmin: true, summary: fmt.Sprintf("deleted team #%d", id)}
	return s.mutate(ctx, p, c, func(r Repositories) error {
		before, err := r.Teams.Get(ctx, id)
		if err != nil {
			return err
		}
		c.before = map[string]any{"name": before.Name, "description": before.Description, "memberIds": before.MemberIDs}
		return r.Teams.Delete(ctx, id)
	})
}

// SetTeamMembers replaces the members of a team.
func (s *AdminService) SetTeamMembers(ctx context.Context, p Principal, id int64, userIDs []int64) error {
	if err := s.requireAdmin(p); err != nil {
		return err
	}
	c := &change{entity: "team", operation: "set_members", id: id, after: userIDs, notifyAdmin: true,
		summary: fmt.Sprintf("changed members of team #%d", id)}
	return s.mutate(ctx, p, c, func(r Repositories) error {
		t, err := r.Teams.Get(ctx, id)
		if err != nil {
			return err
		}
		c.before = t.MemberIDs
		return r.Teams.SetMembers(ctx, id, userIDs)
	})
}

// ---- Grants ----

// Grants lists all grants (admin and auditor).
func (s *AdminService) Grants(ctx context.Context, p Principal) ([]domain.Grant, error) {
	if err := RequireRole(p, domain.RoleAdmin, domain.RoleAuditor); err != nil {
		return nil, err
	}
	return s.store.Repos().Grants.List(ctx)
}

// GrantsCovering lists the grants that reach a namespace or workload, expired
// ones included (admin and auditor).
func (s *AdminService) GrantsCovering(ctx context.Context, p Principal, t domain.Target) ([]domain.Grant, error) {
	if err := RequireRole(p, domain.RoleAdmin, domain.RoleAuditor); err != nil {
		return nil, err
	}
	if t.Cluster == "" || t.Namespace == "" {
		return nil, Invalid("namespace", "cluster and namespace are required")
	}
	if t.HasWorkload() && !t.Workload.Kind.Valid() {
		return nil, Invalid("kind", "invalid workload kind")
	}
	all, err := s.store.Repos().Grants.List(ctx)
	if err != nil {
		return nil, err
	}
	pol := s.policy.Current().Policy
	var out []domain.Grant
	for _, g := range all {
		if pol.GrantReaches(g, t) {
			out = append(out, g)
		}
	}
	return out, nil
}

func (s *AdminService) validateGrant(g domain.Grant) error {
	if err := g.Validate(); err != nil {
		return Invalid("grant", err.Error())
	}
	pol := s.policy.Current().Policy
	if g.Scope.Namespace != "" && pol.IsProtected(g.Scope.Namespace) {
		return Invalid("scope", "this namespace is protected and cannot be granted")
	}
	snap := s.policy.Current()
	switch g.HolderKind {
	case domain.HolderTeam:
		if _, ok := snap.Teams[g.TeamID]; !ok {
			return Invalid("holder", "unknown team")
		}
	case domain.HolderUser:
		if _, ok := snap.Users[g.UserID]; !ok {
			return Invalid("holder", "unknown user")
		}
	}
	if g.Scope.Cluster != "" {
		if _, ok := snap.Clusters[g.Scope.Cluster]; !ok {
			return Invalid("scope", "unknown cluster "+g.Scope.Cluster)
		}
	}
	return nil
}

func (s *AdminService) CreateGrant(ctx context.Context, p Principal, g domain.Grant) (domain.Grant, error) {
	if err := s.requireAdmin(p); err != nil {
		return g, err
	}
	g.CreatedBy, g.UpdatedBy = p.User.Username, p.User.Username
	if err := s.validateGrant(g); err != nil {
		return g, err
	}
	err := s.mutate(ctx, p, &change{entity: "grant", operation: "create", after: grantSummary(g), notifyAdmin: true,
		summary: "created a grant: " + scopeText(g.Scope)}, func(r Repositories) error {
		var err error
		g, err = r.Grants.Create(ctx, g)
		return err
	})
	return g, err
}

func (s *AdminService) UpdateGrant(ctx context.Context, p Principal, g domain.Grant) (domain.Grant, error) {
	if err := s.requireAdmin(p); err != nil {
		return g, err
	}
	g.UpdatedBy = p.User.Username
	if err := s.validateGrant(g); err != nil {
		return g, err
	}
	c := &change{entity: "grant", operation: "update", id: g.ID, after: grantSummary(g), notifyAdmin: true,
		summary: fmt.Sprintf("updated grant #%d", g.ID)}
	err := s.mutate(ctx, p, c, func(r Repositories) error {
		before, err := r.Grants.Get(ctx, g.ID)
		if err != nil {
			return err
		}
		c.before = grantSummary(before)
		g.CreatedBy, g.CreatedAt = before.CreatedBy, before.CreatedAt
		return r.Grants.Update(ctx, g)
	})
	return g, err
}

func (s *AdminService) DeleteGrant(ctx context.Context, p Principal, id int64) error {
	if err := s.requireAdmin(p); err != nil {
		return err
	}
	c := &change{entity: "grant", operation: "delete", id: id, notifyAdmin: true, summary: fmt.Sprintf("deleted grant #%d", id)}
	return s.mutate(ctx, p, c, func(r Repositories) error {
		before, err := r.Grants.Get(ctx, id)
		if err != nil {
			return err
		}
		c.before = grantSummary(before)
		return r.Grants.Delete(ctx, id)
	})
}

func grantSummary(g domain.Grant) map[string]any {
	return map[string]any{"holderKind": g.HolderKind, "teamId": g.TeamID, "userId": g.UserID, "scope": g.Scope,
		"actions": g.Actions, "expiresAt": g.ExpiresAt, "note": g.Note}
}

// ---- Time windows and holidays ----

func (s *AdminService) Windows(ctx context.Context, p Principal) ([]domain.TimeWindow, error) {
	if err := RequireRole(p, domain.RoleAdmin, domain.RoleAuditor); err != nil {
		return nil, err
	}
	return s.store.Repos().Windows.List(ctx)
}

func (s *AdminService) normalizeWindow(w *domain.TimeWindow) error {
	if w.Timezone == "" {
		w.Timezone = domain.DefaultTimezone
	}
	if err := w.Validate(); err != nil {
		return Invalid("window", err.Error())
	}
	if w.ScopeKind == domain.WindowTeam {
		found := false
		for _, t := range s.policy.Current().Teams {
			if t.Name == w.ScopeValue {
				w.TeamID, found = t.ID, true
			}
		}
		if !found {
			return Invalid("scopeValue", "unknown team "+w.ScopeValue)
		}
	}
	return nil
}

func (s *AdminService) CreateWindow(ctx context.Context, p Principal, w domain.TimeWindow) (domain.TimeWindow, error) {
	if err := s.requireAdmin(p); err != nil {
		return w, err
	}
	if err := s.normalizeWindow(&w); err != nil {
		return w, err
	}
	w.UpdatedBy = p.User.Username
	err := s.mutate(ctx, p, &change{entity: "time_window", operation: "create", after: w}, func(r Repositories) error {
		var err error
		w, err = r.Windows.Create(ctx, w)
		return err
	})
	return w, err
}

func (s *AdminService) UpdateWindow(ctx context.Context, p Principal, w domain.TimeWindow) (domain.TimeWindow, error) {
	if err := s.requireAdmin(p); err != nil {
		return w, err
	}
	if err := s.normalizeWindow(&w); err != nil {
		return w, err
	}
	w.UpdatedBy = p.User.Username
	c := &change{entity: "time_window", operation: "update", id: w.ID, after: w}
	err := s.mutate(ctx, p, c, func(r Repositories) error {
		before, err := r.Windows.Get(ctx, w.ID)
		if err != nil {
			return err
		}
		c.before = before
		return r.Windows.Update(ctx, w)
	})
	return w, err
}

func (s *AdminService) DeleteWindow(ctx context.Context, p Principal, id int64) error {
	if err := s.requireAdmin(p); err != nil {
		return err
	}
	c := &change{entity: "time_window", operation: "delete", id: id}
	return s.mutate(ctx, p, c, func(r Repositories) error {
		before, err := r.Windows.Get(ctx, id)
		if err != nil {
			return err
		}
		c.before = before
		return r.Windows.Delete(ctx, id)
	})
}

func (s *AdminService) Holidays(ctx context.Context, p Principal) ([]domain.Holiday, error) {
	if err := RequireRole(p, domain.RoleAdmin, domain.RoleAuditor); err != nil {
		return nil, err
	}
	return s.store.Repos().Holidays.List(ctx)
}

func validHoliday(h domain.Holiday) error {
	if h.Date.IsZero() {
		return Invalid("date", "a date is required")
	}
	if strings.TrimSpace(h.Title) == "" {
		return Invalid("title", "a title is required")
	}
	return nil
}

func (s *AdminService) CreateHoliday(ctx context.Context, p Principal, h domain.Holiday) (domain.Holiday, error) {
	if err := s.requireAdmin(p); err != nil {
		return h, err
	}
	if err := validHoliday(h); err != nil {
		return h, err
	}
	err := s.mutate(ctx, p, &change{entity: "holiday", operation: "create", after: h}, func(r Repositories) error {
		var err error
		h, err = r.Holidays.Create(ctx, h)
		return err
	})
	return h, err
}

func (s *AdminService) UpdateHoliday(ctx context.Context, p Principal, h domain.Holiday) (domain.Holiday, error) {
	if err := s.requireAdmin(p); err != nil {
		return h, err
	}
	if err := validHoliday(h); err != nil {
		return h, err
	}
	c := &change{entity: "holiday", operation: "update", id: h.ID, after: h}
	err := s.mutate(ctx, p, c, func(r Repositories) error {
		before, err := r.Holidays.Get(ctx, h.ID)
		if err != nil {
			return err
		}
		c.before = before
		return r.Holidays.Update(ctx, h)
	})
	return h, err
}

func (s *AdminService) DeleteHoliday(ctx context.Context, p Principal, id int64) error {
	if err := s.requireAdmin(p); err != nil {
		return err
	}
	c := &change{entity: "holiday", operation: "delete", id: id}
	return s.mutate(ctx, p, c, func(r Repositories) error {
		before, err := r.Holidays.Get(ctx, id)
		if err != nil {
			return err
		}
		c.before = before
		return r.Holidays.Delete(ctx, id)
	})
}

// ---- Settings ----

func (s *AdminService) Settings(p Principal) (domain.Settings, error) {
	if err := RequireRole(p, domain.RoleAdmin, domain.RoleAuditor); err != nil {
		return domain.Settings{}, err
	}
	return s.settings.Get(), nil
}

func (s *AdminService) UpdateSettings(ctx context.Context, p Principal, v domain.Settings) (domain.Settings, error) {
	if err := s.requireAdmin(p); err != nil {
		return v, err
	}
	if err := ValidateSettings(v); err != nil {
		return v, err
	}
	if v.Rocket.Events == nil {
		v.Rocket.Events = map[domain.NotificationEvent]bool{}
	}
	before := s.settings.Get()
	err := s.mutate(ctx, p, &change{entity: "settings", operation: "update", before: redactSettings(before), after: redactSettings(v)},
		func(r Repositories) error { return r.Settings.Save(ctx, v, p.User.Username) })
	if err != nil {
		return v, err
	}
	if err := s.settings.Reload(ctx); err != nil {
		s.logger.Error("settings reload after save failed", zap.Error(err))
	}
	// The policy depends on a setting (break-glass on or off): reload it now so
	// open streams are re-evaluated at once.
	s.policy.Changed(ctx)
	return s.settings.Get(), nil
}

// redactSettings hides the webhook URL, which embeds a secret token.
func redactSettings(v domain.Settings) domain.Settings {
	if v.Rocket.WebhookURL != "" {
		v.Rocket.WebhookURL = "(set)"
	}
	return v
}

// ---- Access checker ----

// AccessCheckInput is the access checker's question.
type AccessCheckInput struct {
	UserID int64
	Target domain.Target
	Action domain.Action
	At     time.Time
}

// AccessCheckResult explains a decision.
type AccessCheckResult struct {
	Decision      domain.Decision
	User          domain.User
	TeamNames     []string
	MatchedGrants []domain.Grant
	EvaluatedAt   time.Time
}

// AccessCheck evaluates Decide for any user (admin and auditor), at any time.
func (s *AdminService) AccessCheck(p Principal, in AccessCheckInput) (AccessCheckResult, error) {
	if err := RequireRole(p, domain.RoleAdmin, domain.RoleAuditor); err != nil {
		return AccessCheckResult{}, err
	}
	snap := s.policy.Current()
	u, ok := snap.Users[in.UserID]
	if !ok {
		return AccessCheckResult{}, ErrNotFound
	}
	if !in.Action.Valid() {
		return AccessCheckResult{}, Invalid("action", "invalid action")
	}
	at := in.At
	if at.IsZero() {
		at = s.clock.Now()
	}
	d := s.access.DecideFor(in.UserID, in.Target, in.Action, at)
	res := AccessCheckResult{Decision: d, User: u, EvaluatedAt: at}
	for _, tid := range snap.TeamIDsByUser[u.ID] {
		res.TeamNames = append(res.TeamNames, snap.Teams[tid].Name)
	}
	sort.Strings(res.TeamNames)
	if len(d.GrantIDs) > 0 {
		if all, err := s.store.Repos().Grants.List(context.Background()); err == nil {
			ids := map[int64]bool{}
			for _, id := range d.GrantIDs {
				ids[id] = true
			}
			for _, g := range all {
				if ids[g.ID] {
					res.MatchedGrants = append(res.MatchedGrants, g)
				}
			}
		}
	}
	return res, nil
}
