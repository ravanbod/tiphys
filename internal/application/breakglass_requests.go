package application

import (
	"context"
	"fmt"
	"time"

	"go.uber.org/zap"

	"tiphys/internal/domain"
)

// Break-glass requests (BreakGlassApproval mode): a user or auditor asks, an
// admin approves (the break-glass starts then, for the requested or the
// admin's duration) or rejects, the requester may cancel, and a request
// pending longer than Settings.BreakGlassRequestMaxAgeMin expires.

// Request asks an admin for a break-glass of minutes (0: the default),
// starting when approved or at startsAt if later (see checkStart).
func (s *BreakGlassService) Request(ctx context.Context, p Principal, reason string, minutes int, startsAt *time.Time) (domain.BreakGlassRequest, error) {
	if err := RequireRole(p, domain.RoleUser, domain.RoleAuditor); err != nil {
		return domain.BreakGlassRequest{}, err
	}
	switch s.settings.Get().BreakGlassMode {
	case domain.BreakGlassOff:
		return domain.BreakGlassRequest{}, ErrBreakGlassDisabled
	case domain.BreakGlassSelf:
		return domain.BreakGlassRequest{}, fmt.Errorf("%w: break-glass starts without approval", ErrConflict)
	}
	reason, minutes, err := s.validateStart(reason, minutes)
	if err != nil {
		return domain.BreakGlassRequest{}, err
	}
	now := s.clock.Now()
	if startsAt, err = s.checkStart(startsAt, now); err != nil {
		return domain.BreakGlassRequest{}, err
	}
	var req domain.BreakGlassRequest
	var ev domain.AuditEvent
	err = s.store.InTx(ctx, func(r Repositories) error {
		if err := s.checkNoCurrent(ctx, r, p.User.ID, now, "you"); err != nil {
			return err
		}
		if _, err := r.BGRequests.ExpirePendingBefore(ctx, s.expiryCutoff(now)); err != nil {
			return err
		}
		pending, err := r.BGRequests.List(ctx, p.User.ID, domain.RequestPending, 1)
		if err != nil {
			return err
		}
		if len(pending) > 0 {
			return fmt.Errorf("%w: you already have a pending break-glass request", ErrConflict)
		}
		if req, err = r.BGRequests.Create(ctx, domain.BreakGlassRequest{UserID: p.User.ID, Reason: reason, Minutes: minutes, StartsAt: startsAt, CreatedAt: now}); err != nil {
			return err
		}
		e := s.audit.Event(p, domain.EventBreakGlassReq)
		e.Result, e.Reason = domain.ResultSucceeded, reason
		e.Details = map[string]any{"requestId": req.ID, "minutes": minutes}
		if startsAt != nil {
			e.Details["startsAt"] = *startsAt
		}
		ev, err = s.audit.RecordIn(ctx, r, e)
		return err
	})
	if err != nil {
		return domain.BreakGlassRequest{}, err
	}
	s.audit.Publish(ev)
	fields := []NotificationField{
		{Name: "Who", Value: p.User.Username}, {Name: "Reason", Value: reason},
		{Name: "Duration", Value: fmt.Sprintf("%d minutes", minutes)},
	}
	if startsAt != nil {
		fields = append(fields, NotificationField{Name: "Starts", Value: startsAt.Format(time.RFC3339)})
	}
	fields = append(fields, NotificationField{Name: "Expires if not decided", Value: s.expiresAt(req).Format(time.RFC3339)})
	s.notify.Notify(NotificationMessage{
		Event: domain.NotifyBreakGlassRequest, Title: "Break-glass requested", Fields: fields,
		Link: s.notify.Link("/requests"),
	})
	return req, nil
}

// Approve starts the requested break-glass now, or schedules it for the
// requested start if that is later, for minutes (0: as requested, otherwise
// shorter or longer up to the maximum).
func (s *BreakGlassService) Approve(ctx context.Context, p Principal, id int64, note string, minutes int) (domain.BreakGlassRequest, error) {
	if err := RequireRole(p, domain.RoleAdmin); err != nil {
		return domain.BreakGlassRequest{}, err
	}
	if !s.settings.Get().BreakGlassMode.Enabled() {
		return domain.BreakGlassRequest{}, ErrBreakGlassDisabled
	}
	now := s.clock.Now()
	var req domain.BreakGlassRequest
	var bg domain.BreakGlass
	var ev domain.AuditEvent
	err := s.store.InTx(ctx, func(r Repositories) error {
		if _, err := r.BGRequests.ExpirePendingBefore(ctx, s.expiryCutoff(now)); err != nil {
			return err
		}
		var err error
		if req, err = r.BGRequests.Get(ctx, id); err != nil {
			return err
		}
		if req.Status != domain.RequestPending {
			return fmt.Errorf("%w: request is already %s", ErrConflict, req.Status)
		}
		if minutes == 0 {
			minutes = req.Minutes
		}
		if err := s.checkMinutes(minutes); err != nil {
			return err
		}
		if err := s.checkNoCurrent(ctx, r, req.UserID, now, req.Username); err != nil {
			return err
		}
		begin := now
		if req.StartsAt != nil && req.StartsAt.After(now) {
			begin = *req.StartsAt
		}
		bg, err = r.BreakGlass.Create(ctx, domain.BreakGlass{
			UserID: req.UserID, Username: req.Username, Reason: req.Reason,
			StartedAt: begin, ExpiresAt: begin.Add(time.Duration(minutes) * time.Minute),
		})
		if err != nil {
			return err
		}
		if err := r.BGRequests.Decide(ctx, id, domain.RequestApproved, p.User.Username, note, &minutes, &bg.ID, now); err != nil {
			return err
		}
		req.Status, req.DecidedBy, req.DecidedAt, req.DecisionNote = domain.RequestApproved, p.User.Username, &now, note
		req.ApprovedMinutes, req.BreakGlassID = &minutes, &bg.ID
		e := s.audit.Event(p, domain.EventBreakGlassStart)
		e.Result, e.Reason = domain.ResultSucceeded, req.Reason
		e.BreakGlassID = &bg.ID
		e.Details = map[string]any{"minutes": minutes, "expiresAt": bg.ExpiresAt, "owner": req.Username,
			"requestId": id, "requestedMinutes": req.Minutes, "note": note}
		if begin.After(now) {
			e.Details["startsAt"] = begin
		}
		ev, err = s.audit.RecordIn(ctx, r, e)
		return err
	})
	if err != nil {
		return domain.BreakGlassRequest{}, err
	}
	s.audit.Publish(ev)
	s.policy.Changed(ctx)
	s.notifyStarted(bg, p.User.Username, now)
	s.notifyDecided(req, "approved")
	return req, nil
}

// Reject refuses a pending request.
func (s *BreakGlassService) Reject(ctx context.Context, p Principal, id int64, note string) (domain.BreakGlassRequest, error) {
	if err := RequireRole(p, domain.RoleAdmin); err != nil {
		return domain.BreakGlassRequest{}, err
	}
	req, err := s.close(ctx, p, id, domain.RequestRejected, note, domain.EventBreakGlassReject)
	if err != nil {
		return req, err
	}
	s.notifyDecided(req, "rejected")
	return req, nil
}

// Cancel withdraws the principal's own pending request.
func (s *BreakGlassService) Cancel(ctx context.Context, p Principal, id int64) (domain.BreakGlassRequest, error) {
	req, err := s.store.Repos().BGRequests.Get(ctx, id)
	if err != nil {
		return req, err
	}
	if req.UserID != p.User.ID {
		return domain.BreakGlassRequest{}, ErrForbiddenRole
	}
	return s.close(ctx, p, id, domain.RequestCancelled, "", domain.EventBreakGlassCancel)
}

// close ends a pending request without a break-glass, audited as event.
func (s *BreakGlassService) close(ctx context.Context, p Principal, id int64, status domain.RequestStatus, note, event string) (domain.BreakGlassRequest, error) {
	now := s.clock.Now()
	var req domain.BreakGlassRequest
	var ev domain.AuditEvent
	err := s.store.InTx(ctx, func(r Repositories) error {
		var err error
		if req, err = r.BGRequests.Get(ctx, id); err != nil {
			return err
		}
		if err := r.BGRequests.Decide(ctx, id, status, p.User.Username, note, nil, nil, now); err != nil {
			return err
		}
		req.Status, req.DecidedBy, req.DecidedAt, req.DecisionNote = status, p.User.Username, &now, note
		e := s.audit.Event(p, event)
		e.Result = domain.ResultSucceeded
		e.Details = map[string]any{"requestId": id, "owner": req.Username, "note": note}
		ev, err = s.audit.RecordIn(ctx, r, e)
		return err
	})
	if err != nil {
		return domain.BreakGlassRequest{}, err
	}
	s.audit.Publish(ev)
	return req, nil
}

func (s *BreakGlassService) notifyDecided(r domain.BreakGlassRequest, verdict string) {
	minutes := r.Minutes
	if r.ApprovedMinutes != nil {
		minutes = *r.ApprovedMinutes
	}
	fields := []NotificationField{
		{Name: "Requester", Value: r.Username}, {Name: "Decision", Value: verdict + " by " + r.DecidedBy},
		{Name: "Duration", Value: fmt.Sprintf("%d minutes", minutes)}, {Name: "Reason", Value: r.Reason},
	}
	if r.StartsAt != nil {
		fields = append(fields, NotificationField{Name: "Starts", Value: r.StartsAt.Format(time.RFC3339)})
	}
	if r.DecisionNote != "" {
		fields = append(fields, NotificationField{Name: "Note", Value: r.DecisionNote})
	}
	s.notify.Notify(NotificationMessage{
		Event: domain.NotifyRequestDecided, Title: "Break-glass request " + verdict, Fields: fields,
		Link: s.notify.Link("/access"), DirectTo: r.Username,
	})
}

// Requests lists break-glass requests (admin and auditor), newest first.
func (s *BreakGlassService) Requests(ctx context.Context, p Principal, status domain.RequestStatus) ([]domain.BreakGlassRequest, error) {
	if err := RequireRole(p, domain.RoleAdmin, domain.RoleAuditor); err != nil {
		return nil, err
	}
	reqs, err := s.store.Repos().BGRequests.List(ctx, 0, status, 200)
	if err != nil {
		return nil, err
	}
	return s.current(reqs, status), nil
}

// PendingFor returns the user's pending request, if any.
func (s *BreakGlassService) PendingFor(ctx context.Context, userID int64) (*domain.BreakGlassRequest, error) {
	reqs, err := s.store.Repos().BGRequests.List(ctx, userID, domain.RequestPending, 1)
	if err != nil {
		return nil, err
	}
	if reqs = s.current(reqs, domain.RequestPending); len(reqs) == 0 {
		return nil, nil
	}
	return &reqs[0], nil
}

// PendingCount counts pending requests.
func (s *BreakGlassService) PendingCount(ctx context.Context) (int, error) {
	reqs, err := s.store.Repos().BGRequests.List(ctx, 0, domain.RequestPending, 1000)
	return len(s.current(reqs, domain.RequestPending)), err
}

// current shows pending requests past their age as expired (ExpireOld marks
// them every 30 s) and keeps only those still matching status ("" = all).
func (s *BreakGlassService) current(reqs []domain.BreakGlassRequest, status domain.RequestStatus) []domain.BreakGlassRequest {
	now := s.clock.Now()
	out := reqs[:0]
	for _, r := range reqs {
		if r.Status == domain.RequestPending && !now.Before(s.expiresAt(r)) {
			r.Status = domain.RequestExpired
		}
		if status == "" || r.Status == status {
			out = append(out, r)
		}
	}
	return out
}

// expiresAt is when a pending request expires.
func (s *BreakGlassService) expiresAt(r domain.BreakGlassRequest) time.Time {
	return r.ExpiresAt(time.Duration(s.settings.Get().BreakGlassRequestMaxAgeMin) * time.Minute)
}

func (s *BreakGlassService) expiryCutoff(now time.Time) time.Time {
	return now.Add(-time.Duration(s.settings.Get().BreakGlassRequestMaxAgeMin) * time.Minute)
}

// ExpireOld marks pending requests past their age as expired.
func (s *BreakGlassService) ExpireOld(ctx context.Context) (int64, error) {
	return s.store.Repos().BGRequests.ExpirePendingBefore(ctx, s.expiryCutoff(s.clock.Now()))
}

// Run expires old requests every 30 s until ctx is done.
func (s *BreakGlassService) Run(ctx context.Context) {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if n, err := s.ExpireOld(ctx); err != nil {
				s.logger.Error("failed to expire break-glass requests", zap.Error(err))
			} else if n > 0 {
				s.logger.Info("expired break-glass requests", zap.Int64("count", n))
			}
		}
	}
}
