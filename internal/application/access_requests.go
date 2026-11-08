package application

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"go.uber.org/zap"

	"tiphys/internal/domain"
)

// MinBreakGlassReason is the shortest accepted break-glass reason.
const MinBreakGlassReason = 20

// PendingRequestMaxAge is how long a request may stay pending.
const PendingRequestMaxAge = 7 * 24 * time.Hour

// MaxBreakGlassLead is how far ahead a break-glass may be scheduled.
const MaxBreakGlassLead = 7 * 24 * time.Hour

// BreakGlassService starts and ends break-glass, and handles break-glass
// requests (see breakglass_requests.go). It overrides only time windows and
// holidays, never grants (Decide enforces that).
type BreakGlassService struct {
	store    Store
	audit    *AuditService
	policy   *PolicyService
	settings *SettingsService
	notify   *NotificationService
	clock    Clock
	logger   *zap.Logger
}

func NewBreakGlassService(store Store, audit *AuditService, policy *PolicyService, settings *SettingsService, notify *NotificationService, clock Clock, logger *zap.Logger) *BreakGlassService {
	return &BreakGlassService{store: store, audit: audit, policy: policy, settings: settings, notify: notify, clock: clock, logger: logger}
}

// Start begins a break-glass for a user or auditor (admins need none),
// without approval: only in BreakGlassSelf mode. With startsAt (see
// checkStart) it is scheduled: the row exists now and applies from then.
func (s *BreakGlassService) Start(ctx context.Context, p Principal, reason string, minutes int, startsAt *time.Time) (domain.BreakGlass, error) {
	if err := RequireRole(p, domain.RoleUser, domain.RoleAuditor); err != nil {
		return domain.BreakGlass{}, err
	}
	switch s.settings.Get().BreakGlassMode {
	case domain.BreakGlassOff:
		return domain.BreakGlass{}, ErrBreakGlassDisabled
	case domain.BreakGlassApproval:
		return domain.BreakGlass{}, ErrBreakGlassApprovalRequired
	}
	reason, minutes, err := s.validateStart(reason, minutes)
	if err != nil {
		return domain.BreakGlass{}, err
	}
	now := s.clock.Now()
	startsAt, err = s.checkStart(startsAt, now)
	if err != nil {
		return domain.BreakGlass{}, err
	}
	begin := now
	if startsAt != nil {
		begin = *startsAt
	}

	var bg domain.BreakGlass
	var ev domain.AuditEvent
	err = s.store.InTx(ctx, func(r Repositories) error {
		if err := s.checkNoCurrent(ctx, r, p.User.ID, now, "you"); err != nil {
			return err
		}
		var err error
		bg, err = r.BreakGlass.Create(ctx, domain.BreakGlass{
			UserID: p.User.ID, Username: p.User.Username, Reason: reason,
			StartedAt: begin, ExpiresAt: begin.Add(time.Duration(minutes) * time.Minute),
		})
		if err != nil {
			return err
		}
		e := s.audit.Event(p, domain.EventBreakGlassStart)
		e.Result, e.Reason = domain.ResultSucceeded, reason
		e.BreakGlassID = &bg.ID
		e.Details = map[string]any{"minutes": minutes, "expiresAt": bg.ExpiresAt}
		if startsAt != nil {
			e.Details["startsAt"] = bg.StartedAt
		}
		ev, err = s.audit.RecordIn(ctx, r, e)
		return err
	})
	if err != nil {
		return domain.BreakGlass{}, err
	}
	s.audit.Publish(ev)
	s.policy.Changed(ctx)
	s.notifyStarted(bg, "", now)
	return bg, nil
}

// checkStart checks a requested start. nil, a time up to a minute ahead or
// up to five minutes past (clock skew) mean now and return nil; otherwise it
// must be within MaxBreakGlassLead.
func (s *BreakGlassService) checkStart(startsAt *time.Time, now time.Time) (*time.Time, error) {
	switch {
	case startsAt == nil:
		return nil, nil
	case startsAt.Before(now.Add(-5 * time.Minute)):
		return nil, Invalid("startsAt", "start time is in the past")
	case !startsAt.After(now.Add(time.Minute)):
		return nil, nil
	case startsAt.After(now.Add(MaxBreakGlassLead)):
		return nil, Invalid("startsAt", fmt.Sprintf("start time must be within %d days", int(MaxBreakGlassLead/(24*time.Hour))))
	}
	return startsAt, nil
}

// validateStart checks a reason and duration (0: the default) for a
// break-glass or a request for one.
func (s *BreakGlassService) validateStart(reason string, minutes int) (string, int, error) {
	reason = strings.TrimSpace(reason)
	if len([]rune(reason)) < MinBreakGlassReason {
		return "", 0, Invalid("reason", fmt.Sprintf("reason must be at least %d characters", MinBreakGlassReason))
	}
	st := s.settings.Get()
	if minutes == 0 {
		minutes = st.BreakGlassDefaultMin
	}
	if err := s.checkMinutes(minutes); err != nil {
		return "", 0, err
	}
	return reason, minutes, nil
}

func (s *BreakGlassService) checkMinutes(minutes int) error {
	if max := s.settings.Get().BreakGlassMaxMinutes; minutes < 1 || minutes > max {
		return Invalid("durationMinutes", fmt.Sprintf("duration must be between 1 and %d minutes", max))
	}
	return nil
}

// checkNoCurrent fails with ErrConflict when the user has a break-glass
// active or scheduled at now: a user has one at a time. who names the user
// in the message.
func (s *BreakGlassService) checkNoCurrent(ctx context.Context, r Repositories, userID int64, now time.Time, who string) error {
	cur, err := r.BreakGlass.CurrentForUser(ctx, userID, now)
	switch {
	case errors.Is(err, ErrNotFound):
		return nil
	case err != nil:
		return err
	case cur.ActiveAt(now):
		return fmt.Errorf("%w: %s already %s an active break-glass", ErrConflict, who, have(who))
	}
	return fmt.Errorf("%w: %s already %s a break-glass scheduled for %s", ErrConflict, who, have(who), cur.StartedAt.UTC().Format(time.RFC3339))
}

func have(who string) string {
	if who == "you" {
		return "have"
	}
	return "has"
}

// notifyStarted announces a break-glass, as scheduled when it starts after
// now; approvedBy is set when an admin approved a request for it.
func (s *BreakGlassService) notifyStarted(bg domain.BreakGlass, approvedBy string, now time.Time) {
	title := "Break-glass started"
	fields := []NotificationField{{Name: "Who", Value: bg.Username}, {Name: "Reason", Value: bg.Reason}}
	if bg.StartedAt.After(now) {
		title = "Break-glass scheduled"
		fields = append(fields, NotificationField{Name: "From", Value: bg.StartedAt.Format(time.RFC3339)})
	}
	fields = append(fields, NotificationField{Name: "Until", Value: bg.ExpiresAt.Format(time.RFC3339)})
	if approvedBy != "" {
		fields = append(fields, NotificationField{Name: "Approved by", Value: approvedBy})
	}
	s.notify.Notify(NotificationMessage{
		Event: domain.NotifyBreakGlassStarted, Title: title, Fields: fields,
		Link: s.notify.Link("/audit?breakGlassId=" + fmt.Sprint(bg.ID)),
	})
}

// End ends a break-glass early, or cancels a scheduled one before it
// starts. Only its owner or an admin may.
func (s *BreakGlassService) End(ctx context.Context, p Principal, id int64) error {
	bg, err := s.store.Repos().BreakGlass.Get(ctx, id)
	if err != nil {
		return err
	}
	if bg.UserID != p.User.ID && !p.IsAdmin() {
		return ErrForbiddenRole
	}
	now := s.clock.Now()
	if !bg.Current(now) {
		return fmt.Errorf("%w: break-glass is not active", ErrConflict)
	}
	var ev domain.AuditEvent
	err = s.store.InTx(ctx, func(r Repositories) error {
		if err := r.BreakGlass.End(ctx, id, p.User.Username, now); err != nil {
			return err
		}
		e := s.audit.Event(p, domain.EventBreakGlassEnd)
		e.Result, e.BreakGlassID = domain.ResultSucceeded, &id
		e.Details = map[string]any{"owner": bg.Username}
		if now.Before(bg.StartedAt) {
			e.Details["beforeStart"] = true
		}
		ev, err = s.audit.RecordIn(ctx, r, e)
		return err
	})
	if err != nil {
		return err
	}
	s.audit.Publish(ev)
	s.policy.Changed(ctx)
	return nil
}

// Active returns the user's active or scheduled break-glass, if any, as of
// the last policy load (callers check Current or ActiveAt).
func (s *BreakGlassService) Active(userID int64) *domain.BreakGlass {
	return s.policy.Current().BreakGlass[userID]
}

// List returns recent break-glasses (admin and auditor).
func (s *BreakGlassService) List(ctx context.Context, p Principal, limit int) ([]domain.BreakGlass, error) {
	if err := RequireRole(p, domain.RoleAdmin, domain.RoleAuditor); err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	return s.store.Repos().BreakGlass.List(ctx, limit)
}

// AccessRequestService handles requests for access and their decisions.
type AccessRequestService struct {
	store  Store
	audit  *AuditService
	policy *PolicyService
	argo   *ArgoService
	notify *NotificationService
	clock  Clock
}

func NewAccessRequestService(store Store, audit *AuditService, policy *PolicyService, argo *ArgoService, notify *NotificationService, clock Clock) *AccessRequestService {
	return &AccessRequestService{store: store, audit: audit, policy: policy, argo: argo, notify: notify, clock: clock}
}

// CreateRequest is the input of Create.
type CreateRequest struct {
	Scope         domain.Scope
	Actions       []domain.Action
	DurationHours *int
	Reason        string
	// TeamID asks for a team the principal manages instead of for themself.
	TeamID int64
}

// Create files a request for the principal, or with TeamID for a team they
// manage (checked in the transaction), which approval then grants.
func (s *AccessRequestService) Create(ctx context.Context, p Principal, in CreateRequest) (domain.AccessRequest, error) {
	if err := RequireRole(p, domain.RoleUser, domain.RoleAuditor); err != nil {
		return domain.AccessRequest{}, err
	}
	if err := validateRequest(in); err != nil {
		return domain.AccessRequest{}, err
	}
	snap := s.policy.Current()
	if in.Scope.Namespace != "" && snap.Policy.IsProtected(in.Scope.Namespace) {
		return domain.AccessRequest{}, Invalid("scope", "this namespace is protected and cannot be requested")
	}
	if in.Scope.Cluster != "" && snap.Clusters[in.Scope.Cluster].Protected {
		return domain.AccessRequest{}, Invalid("scope", "this cluster is protected and cannot be requested")
	}
	var req domain.AccessRequest
	var ev domain.AuditEvent
	err := s.store.InTx(ctx, func(r Repositories) error {
		if in.TeamID != 0 {
			if _, err := requireTeamManager(ctx, r, p, in.TeamID); err != nil {
				return err
			}
		}
		var err error
		req, err = r.Requests.Create(ctx, domain.AccessRequest{
			UserID: p.User.ID, Username: p.User.Username, TeamID: in.TeamID, Scope: in.Scope, Actions: in.Actions,
			DurationHours: in.DurationHours, Reason: strings.TrimSpace(in.Reason), Status: domain.RequestPending,
		})
		if err != nil {
			return err
		}
		e := s.audit.Event(p, domain.EventRequestCreate)
		e.Result, e.Reason = domain.ResultSucceeded, req.Reason
		e.Cluster, e.Namespace = in.Scope.Cluster, in.Scope.Namespace
		e.Details = map[string]any{"requestId": req.ID, "scope": in.Scope, "actions": in.Actions, "durationHours": in.DurationHours}
		if req.TeamID != 0 {
			e.Details["teamId"], e.Details["team"] = req.TeamID, req.TeamName
		}
		ev, err = s.audit.RecordIn(ctx, r, e)
		return err
	})
	if err != nil {
		return req, err
	}
	s.audit.Publish(ev)
	s.notify.Notify(NotificationMessage{
		Event: domain.NotifyRequestCreated, Title: "Access request created",
		Fields: append([]NotificationField{{Name: "Who", Value: p.User.Username}}, requestFields(req)...),
		Link:   s.notify.Link("/admin/requests"),
	})
	return req, nil
}

func requestFields(r domain.AccessRequest) []NotificationField {
	acts := make([]string, len(r.Actions))
	for i, a := range r.Actions {
		acts[i] = string(a)
	}
	dur := "permanent"
	if r.DurationHours != nil {
		dur = fmt.Sprintf("%d hours", *r.DurationHours)
	}
	out := []NotificationField{
		{Name: "What", Value: scopeText(r.Scope) + " · " + strings.Join(acts, ", ")},
		{Name: "Duration", Value: dur}, {Name: "Reason", Value: r.Reason},
	}
	if r.TeamID != 0 {
		out = append([]NotificationField{{Name: "For team", Value: r.TeamName}}, out...)
	}
	return out
}

func scopeText(s domain.Scope) string {
	switch s.Kind {
	case domain.ScopeArgoProject:
		return "Argo project " + s.ArgoProject
	case domain.ScopeNamespace:
		return fmt.Sprintf("namespace %s/%s", s.Cluster, s.Namespace)
	case domain.ScopeCluster:
		return "cluster " + s.Cluster
	case domain.ScopeEnvironment:
		return "environment " + s.Environment
	}
	names := make([]string, len(s.Workloads))
	for i, w := range s.Workloads {
		names[i] = w.String()
	}
	return fmt.Sprintf("%s/%s: %s", s.Cluster, s.Namespace, strings.Join(names, ", "))
}

func validateRequest(in CreateRequest) error {
	if err := in.Scope.Validate(); err != nil {
		return Invalid("scope", err.Error())
	}
	if in.Scope.Kind == domain.ScopeCluster || in.Scope.Kind == domain.ScopeEnvironment {
		return Invalid("scope", "a whole cluster or environment is granted by an admin only, not requested")
	}
	if err := domain.ValidateScopeActions(in.Scope, in.Actions); err != nil {
		return Invalid("actions", err.Error())
	}
	if in.DurationHours != nil && (*in.DurationHours < 1 || *in.DurationHours > 24*365) {
		return Invalid("durationHours", "duration must be between 1 hour and 1 year")
	}
	if strings.TrimSpace(in.Reason) == "" {
		return Invalid("reason", "a reason is required")
	}
	return nil
}

// List returns the principal's own requests (role user) or everyone's.
func (s *AccessRequestService) List(ctx context.Context, p Principal, status domain.RequestStatus) ([]domain.AccessRequest, error) {
	var uid int64
	if p.User.Role == domain.RoleUser {
		uid = p.User.ID
	}
	return s.store.Repos().Requests.List(ctx, uid, status)
}

// ListOwn returns the principal's own requests, whatever their role (also
// those they filed for a team).
func (s *AccessRequestService) ListOwn(ctx context.Context, p Principal) ([]domain.AccessRequest, error) {
	return s.store.Repos().Requests.List(ctx, p.User.ID, "")
}

// ForTeam returns the requests filed for a team, by any of its managers.
// ErrForbiddenRole unless the principal manages the team.
func (s *AccessRequestService) ForTeam(ctx context.Context, p Principal, teamID int64) ([]domain.AccessRequest, error) {
	if t, ok := s.policy.Current().Teams[teamID]; !ok || p.User.Local || !t.ManagedBy(p.User.ID) {
		return nil, ErrForbiddenRole
	}
	return s.store.Repos().Requests.ListByTeam(ctx, teamID)
}

// ApproveOptions are an admin's choices when approving: Duration replaces
// the requested one when OverrideDuration is set (nil: permanent); Actions,
// when given, replace the requested ones (fewer or more, within what the
// scope allows); Separate creates a new grant instead of merging.
type ApproveOptions struct {
	Note             string
	OverrideDuration bool
	Duration         *int
	Actions          []domain.Action
	Separate         bool
}

// Approve grants the request and marks it approved in one transaction. The
// grant goes to the requester, or to the team for a team's request. The
// actions are added to that holder's own grant on the same scope when one
// lasts no longer than the approved access (see mergeTarget), which then
// takes the new expiry; otherwise, or with Separate, a new grant is created.
// A request of a batch is decided only with its batch (DecideBatch).
func (s *AccessRequestService) Approve(ctx context.Context, p Principal, id int64, o ApproveOptions) (domain.AccessRequest, error) {
	if err := RequireRole(p, domain.RoleAdmin); err != nil {
		return domain.AccessRequest{}, err
	}
	now := s.clock.Now()
	var out domain.AccessRequest
	var ev domain.AuditEvent
	var merged bool
	err := s.store.InTx(ctx, func(r Repositories) error {
		req, err := r.Requests.Get(ctx, id)
		if err != nil {
			return err
		}
		if req.BatchID != "" {
			return errInBatch
		}
		out, ev, merged, err = s.approveIn(ctx, r, p, req, o, now)
		return err
	})
	if err != nil {
		return out, err
	}
	s.audit.Publish(ev)
	s.policy.Changed(ctx)
	verdict := "approved"
	if merged {
		verdict = fmt.Sprintf("approved (added to grant #%d)", *out.ResultingGrantID)
	}
	s.notifyDecided(out, verdict)
	return out, nil
}

var errInBatch = fmt.Errorf("%w: this request is part of a batch; decide the whole batch", ErrConflict)

// approveIn approves a pending request inside the transaction r: it merges
// into or creates the grant, marks the request approved and records the
// audit event, which the caller publishes after the commit.
func (s *AccessRequestService) approveIn(ctx context.Context, r Repositories, p Principal, req domain.AccessRequest, o ApproveOptions, now time.Time) (domain.AccessRequest, domain.AuditEvent, bool, error) {
	if req.Status != domain.RequestPending {
		return req, domain.AuditEvent{}, false, fmt.Errorf("%w: request #%d is already %s", ErrConflict, req.ID, req.Status)
	}
	hours := req.DurationHours
	if o.OverrideDuration {
		hours = o.Duration
	}
	requested := req.Actions
	if len(o.Actions) > 0 {
		if err := domain.ValidateScopeActions(req.Scope, o.Actions); err != nil {
			return req, domain.AuditEvent{}, false, Invalid("actions", err.Error())
		}
		req.Actions = o.Actions
	}
	var exp *time.Time
	if hours != nil {
		t := now.Add(time.Duration(*hours) * time.Hour)
		exp = &t
	}
	line := fmt.Sprintf("Request #%d: %s", req.ID, req.Reason)
	holder := domain.Grant{HolderKind: domain.HolderUser, UserID: req.UserID}
	if req.TeamID != 0 {
		line = fmt.Sprintf("Request #%d by %s: %s", req.ID, req.Username, req.Reason)
		holder = domain.Grant{HolderKind: domain.HolderTeam, TeamID: req.TeamID}
	}
	var g, before domain.Grant
	merged := false
	if !o.Separate {
		all, err := r.Grants.List(ctx)
		if err != nil {
			return req, domain.AuditEvent{}, false, err
		}
		before, merged = mergeTarget(all, holder, req.Scope, exp, now)
	}
	if merged {
		g = before
		g.Actions = unionActions(before.Actions, req.Actions)
		g.ExpiresAt, g.UpdatedBy = exp, p.User.Username
		g.Note = strings.TrimSpace(g.Note + "\n" + line)
		if err := g.Validate(); err != nil {
			return req, domain.AuditEvent{}, false, Invalid("grant", err.Error())
		}
		if err := r.Grants.Update(ctx, g); err != nil {
			return req, domain.AuditEvent{}, false, err
		}
	} else {
		g = domain.Grant{
			HolderKind: holder.HolderKind, UserID: holder.UserID, TeamID: holder.TeamID, Scope: req.Scope, Actions: req.Actions, ExpiresAt: exp,
			Note: line, CreatedBy: p.User.Username, UpdatedBy: p.User.Username,
		}
		if err := g.Validate(); err != nil {
			return req, domain.AuditEvent{}, false, Invalid("grant", err.Error())
		}
		var err error
		if g, err = r.Grants.Create(ctx, g); err != nil {
			return req, domain.AuditEvent{}, false, err
		}
	}
	if err := r.Requests.Decide(ctx, req.ID, domain.RequestApproved, p.User.Username, o.Note, &g.ID, now); err != nil {
		return req, domain.AuditEvent{}, false, err
	}
	req.Status, req.DecidedBy, req.DecidedAt, req.DecisionNote, req.ResultingGrantID = domain.RequestApproved, p.User.Username, &now, o.Note, &g.ID
	e := s.audit.Event(p, domain.EventRequestDecide)
	e.Result, e.Reason = domain.ResultSucceeded, "approved"
	e.Cluster, e.Namespace = req.Scope.Cluster, req.Scope.Namespace
	e.Details = map[string]any{"requestId": req.ID, "requester": req.Username, "grantId": g.ID, "note": o.Note, "expiresAt": g.ExpiresAt,
		"requestedActions": requested, "actions": req.Actions, "merged": merged}
	if req.TeamID != 0 {
		e.Details["teamId"], e.Details["team"] = req.TeamID, req.TeamName
	}
	if req.BatchID != "" {
		e.Details["batchId"] = req.BatchID
	}
	if merged {
		e.Details["before"] = map[string]any{"actions": before.Actions, "expiresAt": before.ExpiresAt}
		e.Details["after"] = map[string]any{"actions": g.Actions, "expiresAt": g.ExpiresAt}
	}
	ev, err := s.audit.RecordIn(ctx, r, e)
	return req, ev, merged, err
}

// mergeTarget picks the grant that approved access on scope, lasting until
// expires (nil: permanent), is added to: an unexpired grant of exactly the
// holder (HolderKind with UserID or TeamID; a user's request never touches a
// team's grant, a team's never the requester's) on exactly the same scope
// that expires no later than the new access, so its expiry can move to the
// new one without the new actions lasting longer than approved. Of several,
// the one lasting longest. web/src/lib/requestMerge.ts mirrors this for the
// preview.
func mergeTarget(grants []domain.Grant, holder domain.Grant, scope domain.Scope, expires *time.Time, now time.Time) (domain.Grant, bool) {
	var best domain.Grant
	found := false
	for _, g := range grants {
		if g.HolderKind != holder.HolderKind || g.UserID != holder.UserID || g.TeamID != holder.TeamID ||
			g.Expired(now) || !g.Scope.Same(scope) {
			continue
		}
		if expires != nil && (g.ExpiresAt == nil || g.ExpiresAt.After(*expires)) {
			continue // it outlasts the new access
		}
		if !found || laterExpiry(g.ExpiresAt, best.ExpiresAt) {
			best, found = g, true
		}
	}
	return best, found
}

// laterExpiry reports whether a ends after b (nil never ends).
func laterExpiry(a, b *time.Time) bool {
	switch {
	case a == nil:
		return b != nil
	case b == nil:
		return false
	}
	return a.After(*b)
}

// unionActions is every action of a or b, in display order.
func unionActions(a, b []domain.Action) []domain.Action {
	var out []domain.Action
	for _, x := range domain.AllActions {
		if slices.Contains(a, x) || slices.Contains(b, x) {
			out = append(out, x)
		}
	}
	return out
}

// Reject marks a request rejected. A request of a batch is decided only with
// its batch (DecideBatch).
func (s *AccessRequestService) Reject(ctx context.Context, p Principal, id int64, note string) (domain.AccessRequest, error) {
	if err := RequireRole(p, domain.RoleAdmin); err != nil {
		return domain.AccessRequest{}, err
	}
	now := s.clock.Now()
	var out domain.AccessRequest
	var ev domain.AuditEvent
	err := s.store.InTx(ctx, func(r Repositories) error {
		req, err := r.Requests.Get(ctx, id)
		if err != nil {
			return err
		}
		if req.BatchID != "" {
			return errInBatch
		}
		out, ev, err = s.rejectIn(ctx, r, p, req, note, now)
		return err
	})
	if err != nil {
		return out, err
	}
	s.audit.Publish(ev)
	s.notifyDecided(out, "rejected")
	return out, nil
}

// rejectIn rejects a request inside the transaction r and records the audit
// event, which the caller publishes after the commit.
func (s *AccessRequestService) rejectIn(ctx context.Context, r Repositories, p Principal, req domain.AccessRequest, note string, now time.Time) (domain.AccessRequest, domain.AuditEvent, error) {
	if err := r.Requests.Decide(ctx, req.ID, domain.RequestRejected, p.User.Username, note, nil, now); err != nil {
		return req, domain.AuditEvent{}, err
	}
	req.Status, req.DecidedBy, req.DecidedAt, req.DecisionNote = domain.RequestRejected, p.User.Username, &now, note
	e := s.audit.Event(p, domain.EventRequestDecide)
	e.Result, e.Reason = domain.ResultSucceeded, "rejected"
	e.Details = map[string]any{"requestId": req.ID, "requester": req.Username, "note": note}
	if req.BatchID != "" {
		e.Cluster, e.Namespace = req.Scope.Cluster, req.Scope.Namespace
		e.Details["batchId"] = req.BatchID
	}
	ev, err := s.audit.RecordIn(ctx, r, e)
	return req, ev, err
}

func (s *AccessRequestService) notifyDecided(r domain.AccessRequest, verdict string) {
	fields := append([]NotificationField{{Name: "Requester", Value: r.Username}, {Name: "Decision", Value: verdict + " by " + r.DecidedBy}}, requestFields(r)...)
	if r.DecisionNote != "" {
		fields = append(fields, NotificationField{Name: "Note", Value: r.DecisionNote})
	}
	s.notify.Notify(NotificationMessage{
		Event: domain.NotifyRequestDecided, Title: "Access request " + verdict, Fields: fields,
		Link: s.notify.Link("/my-access"), DirectTo: r.Username,
	})
}

// ExpireOld marks pending requests older than PendingRequestMaxAge as expired.
func (s *AccessRequestService) ExpireOld(ctx context.Context) (int64, error) {
	return s.store.Repos().Requests.ExpirePendingBefore(ctx, s.clock.Now().Add(-PendingRequestMaxAge))
}

// Pending counts pending requests, a batch once.
func (s *AccessRequestService) Pending(ctx context.Context) (int, error) {
	reqs, err := s.store.Repos().Requests.List(ctx, 0, domain.RequestPending)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return 0, err
	}
	return countRequests(reqs), nil
}
