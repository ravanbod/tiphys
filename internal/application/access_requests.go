package application

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"tiphys/internal/domain"
)

// MinBreakGlassReason is the shortest accepted break-glass reason.
const MinBreakGlassReason = 20

// PendingRequestMaxAge is how long a request may stay pending.
const PendingRequestMaxAge = 7 * 24 * time.Hour

// BreakGlassService starts and ends break-glass. It overrides only time
// windows and holidays, never grants (Decide enforces that).
type BreakGlassService struct {
	store    Store
	audit    *AuditService
	policy   *PolicyService
	settings *SettingsService
	notify   *NotificationService
	clock    Clock
}

func NewBreakGlassService(store Store, audit *AuditService, policy *PolicyService, settings *SettingsService, notify *NotificationService, clock Clock) *BreakGlassService {
	return &BreakGlassService{store: store, audit: audit, policy: policy, settings: settings, notify: notify, clock: clock}
}

// Start begins a break-glass for a user with role user. No approval.
func (s *BreakGlassService) Start(ctx context.Context, p Principal, reason string, minutes int) (domain.BreakGlass, error) {
	if err := RequireRole(p, domain.RoleUser); err != nil {
		return domain.BreakGlass{}, err
	}
	if !s.settings.Get().BreakGlassEnabled {
		return domain.BreakGlass{}, ErrBreakGlassDisabled
	}
	reason = strings.TrimSpace(reason)
	if len([]rune(reason)) < MinBreakGlassReason {
		return domain.BreakGlass{}, Invalid("reason", fmt.Sprintf("reason must be at least %d characters", MinBreakGlassReason))
	}
	st := s.settings.Get()
	if minutes == 0 {
		minutes = st.BreakGlassDefaultMin
	}
	if minutes < 1 || minutes > st.BreakGlassMaxMinutes {
		return domain.BreakGlass{}, Invalid("durationMinutes", fmt.Sprintf("duration must be between 1 and %d minutes", st.BreakGlassMaxMinutes))
	}
	now := s.clock.Now()
	if cur, err := s.store.Repos().BreakGlass.LatestForUser(ctx, p.User.ID); err == nil && cur.ActiveAt(now) {
		return domain.BreakGlass{}, fmt.Errorf("%w: break-glass is already active", ErrConflict)
	}

	var bg domain.BreakGlass
	var ev domain.AuditEvent
	err := s.store.InTx(ctx, func(r Repositories) error {
		var err error
		bg, err = r.BreakGlass.Create(ctx, domain.BreakGlass{
			UserID: p.User.ID, Username: p.User.Username, Reason: reason,
			StartedAt: now, ExpiresAt: now.Add(time.Duration(minutes) * time.Minute),
		})
		if err != nil {
			return err
		}
		e := s.audit.Event(p, domain.EventBreakGlassStart)
		e.Result, e.Reason = domain.ResultSucceeded, reason
		e.BreakGlassID = &bg.ID
		e.Details = map[string]any{"minutes": minutes, "expiresAt": bg.ExpiresAt}
		ev, err = s.audit.RecordIn(ctx, r, e)
		return err
	})
	if err != nil {
		return domain.BreakGlass{}, err
	}
	s.audit.Publish(ev)
	s.policy.Changed(ctx)
	s.notify.Notify(NotificationMessage{
		Event: domain.NotifyBreakGlassStarted, Title: "Break-glass started",
		Fields: []NotificationField{
			{Name: "Who", Value: p.User.Username}, {Name: "Reason", Value: reason},
			{Name: "Until", Value: bg.ExpiresAt.Format(time.RFC3339)},
		},
		Link: s.notify.Link("/audit?breakGlassId=" + fmt.Sprint(bg.ID)),
	})
	return bg, nil
}

// End ends a break-glass early. Only its owner or an admin may.
func (s *BreakGlassService) End(ctx context.Context, p Principal, id int64) error {
	bg, err := s.store.Repos().BreakGlass.Get(ctx, id)
	if err != nil {
		return err
	}
	if bg.UserID != p.User.ID && !p.IsAdmin() {
		return ErrForbiddenRole
	}
	now := s.clock.Now()
	if !bg.ActiveAt(now) {
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

// Active returns the user's active break-glass, if any.
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
}

// Create files a request for the principal.
func (s *AccessRequestService) Create(ctx context.Context, p Principal, in CreateRequest) (domain.AccessRequest, error) {
	if err := RequireRole(p, domain.RoleUser); err != nil {
		return domain.AccessRequest{}, err
	}
	if err := validateRequest(in); err != nil {
		return domain.AccessRequest{}, err
	}
	pol := s.policy.Current().Policy
	if in.Scope.Namespace != "" && pol.IsProtected(in.Scope.Namespace) {
		return domain.AccessRequest{}, Invalid("scope", "this namespace is protected and cannot be requested")
	}
	var req domain.AccessRequest
	var ev domain.AuditEvent
	err := s.store.InTx(ctx, func(r Repositories) error {
		var err error
		req, err = r.Requests.Create(ctx, domain.AccessRequest{
			UserID: p.User.ID, Username: p.User.Username, Scope: in.Scope, Actions: in.Actions,
			DurationHours: in.DurationHours, Reason: strings.TrimSpace(in.Reason), Status: domain.RequestPending,
		})
		if err != nil {
			return err
		}
		e := s.audit.Event(p, domain.EventRequestCreate)
		e.Result, e.Reason = domain.ResultSucceeded, req.Reason
		e.Cluster, e.Namespace = in.Scope.Cluster, in.Scope.Namespace
		e.Details = map[string]any{"requestId": req.ID, "scope": in.Scope, "actions": in.Actions, "durationHours": in.DurationHours}
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
	return []NotificationField{
		{Name: "What", Value: scopeText(r.Scope) + " · " + strings.Join(acts, ", ")},
		{Name: "Duration", Value: dur}, {Name: "Reason", Value: r.Reason},
	}
}

func scopeText(s domain.Scope) string {
	switch s.Kind {
	case domain.ScopeArgoProject:
		return "Argo project " + s.ArgoProject
	case domain.ScopeNamespace:
		return fmt.Sprintf("namespace %s/%s", s.Cluster, s.Namespace)
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

// Approve creates the grant and marks the request approved in one
// transaction. duration overrides the requested one when non-nil with
// override set.
func (s *AccessRequestService) Approve(ctx context.Context, p Principal, id int64, note string, overrideDuration bool, duration *int, narrowTo []domain.Action) (domain.AccessRequest, error) {
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
		if req.Status != domain.RequestPending {
			return fmt.Errorf("%w: request is already %s", ErrConflict, req.Status)
		}
		hours := req.DurationHours
		if overrideDuration {
			hours = duration
		}
		actions := req.Actions
		if len(narrowTo) > 0 {
			actions = nil
			for _, a := range narrowTo {
				if !containsAction(req.Actions, a) {
					return Invalid("actions", "approved actions must be a subset of the requested ones")
				}
				actions = append(actions, a)
			}
			req.Actions = actions
		}
		g := domain.Grant{
			HolderKind: domain.HolderUser, UserID: req.UserID, Scope: req.Scope, Actions: actions,
			Note: fmt.Sprintf("Request #%d: %s", req.ID, req.Reason), CreatedBy: p.User.Username, UpdatedBy: p.User.Username,
		}
		if hours != nil {
			exp := now.Add(time.Duration(*hours) * time.Hour)
			g.ExpiresAt = &exp
		}
		if err := g.Validate(); err != nil {
			return Invalid("grant", err.Error())
		}
		g, err = r.Grants.Create(ctx, g)
		if err != nil {
			return err
		}
		if err := r.Requests.Decide(ctx, id, domain.RequestApproved, p.User.Username, note, &g.ID, now); err != nil {
			return err
		}
		req.Status, req.DecidedBy, req.DecidedAt, req.DecisionNote, req.ResultingGrantID = domain.RequestApproved, p.User.Username, &now, note, &g.ID
		out = req
		e := s.audit.Event(p, domain.EventRequestDecide)
		e.Result, e.Reason = domain.ResultSucceeded, "approved"
		e.Cluster, e.Namespace = req.Scope.Cluster, req.Scope.Namespace
		e.Details = map[string]any{"requestId": id, "requester": req.Username, "grantId": g.ID, "note": note, "expiresAt": g.ExpiresAt}
		ev, err = s.audit.RecordIn(ctx, r, e)
		return err
	})
	if err != nil {
		return out, err
	}
	s.audit.Publish(ev)
	s.policy.Changed(ctx)
	s.notifyDecided(out, "approved")
	return out, nil
}

// Reject marks a request rejected.
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
		if err := r.Requests.Decide(ctx, id, domain.RequestRejected, p.User.Username, note, nil, now); err != nil {
			return err
		}
		req.Status, req.DecidedBy, req.DecidedAt, req.DecisionNote = domain.RequestRejected, p.User.Username, &now, note
		out = req
		e := s.audit.Event(p, domain.EventRequestDecide)
		e.Result, e.Reason = domain.ResultSucceeded, "rejected"
		e.Details = map[string]any{"requestId": id, "requester": req.Username, "note": note}
		ev, err = s.audit.RecordIn(ctx, r, e)
		return err
	})
	if err != nil {
		return out, err
	}
	s.audit.Publish(ev)
	s.notifyDecided(out, "rejected")
	return out, nil
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

// Pending counts pending requests.
func (s *AccessRequestService) Pending(ctx context.Context) (int, error) {
	reqs, err := s.store.Repos().Requests.List(ctx, 0, domain.RequestPending)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return 0, err
	}
	return len(reqs), nil
}

func containsAction(list []domain.Action, a domain.Action) bool {
	for _, x := range list {
		if x == a {
			return true
		}
	}
	return false
}
