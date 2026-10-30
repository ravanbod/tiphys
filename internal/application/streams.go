package application

import (
	"context"
	"sync"
	"time"

	"go.uber.org/zap"

	"tiphys/internal/domain"
	"tiphys/internal/monitoring"
)

// EndReason says why a stream ended. The values are part of the API.
type EndReason string

const (
	EndClientClosed      EndReason = "client_closed"
	EndProcessExited     EndReason = "process_exited"
	EndIdleTimeout       EndReason = "idle_timeout"
	EndMaxDuration       EndReason = "max_duration"
	EndOutsideWindow     EndReason = "outside_window"
	EndHoliday           EndReason = "holiday"
	EndGrantRevoked      EndReason = "grant_revoked"
	EndGrantExpired      EndReason = "grant_expired"
	EndUserDisabled      EndReason = "user_disabled"
	EndRoleChanged       EndReason = "role_changed"
	EndTerminatedByAdmin EndReason = "terminated_by_admin"
	EndBreakGlassEnded   EndReason = "break_glass_ended"
	EndRecordingFailed   EndReason = "recording_failed"
	EndClusterError      EndReason = "cluster_error"
	EndPortalRestart     EndReason = "portal_restart"
	EndDenied            EndReason = "denied"
)

// StreamKind is the type of a long-lived stream.
type StreamKind string

const (
	StreamLogs  StreamKind = "logs"
	StreamExec  StreamKind = "exec"
	StreamWatch StreamKind = "watch"
)

// ClosingWarningLead is how long before a deadline the client is warned.
const ClosingWarningLead = 5 * time.Minute

// StreamEvalInterval is how often every stream is re-evaluated by Decide.
const StreamEvalInterval = 30 * time.Second

// StreamSpec describes a stream being registered.
type StreamSpec struct {
	Kind          StreamKind
	UserID        int64
	Username      string
	WebSessionID  string
	Target        domain.Target
	Action        domain.Action
	ExecSessionID string
	IdleTimeout   time.Duration // 0 = none
	MaxDuration   time.Duration // 0 = none
}

// StreamControl is a message for the client of a stream.
type StreamControl struct {
	Type     string // "warning" or "closes_at"
	ClosesAt *time.Time
	Reason   string
	Message  string
}

// Stream is a registered long-lived stream. Its context is cancelled when the
// supervisor ends it; Reason then says why.
type Stream struct {
	ID       string
	key      string
	Spec     StreamSpec
	Started  time.Time
	Controls chan StreamControl

	ctx    context.Context
	cancel context.CancelFunc
	mgr    *StreamManager

	mu           sync.Mutex
	closesAt     *time.Time
	deadline     *time.Time // closesAt or max duration, whichever is first
	deadlineWhy  string
	warnedFor    *time.Time
	lastActivity time.Time
	reason       EndReason
	closed       bool
}

// Context is cancelled when the stream must end.
func (s *Stream) Context() context.Context { return s.ctx }

// Touch records activity, resetting the idle timer.
func (s *Stream) Touch() {
	s.mu.Lock()
	s.lastActivity = s.mgr.clock.Now()
	s.mu.Unlock()
}

// ClosesAt is the decision's closes_at as last evaluated.
func (s *Stream) ClosesAt() *time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closesAt
}

// Reason is why the stream ended (empty while open).
func (s *Stream) Reason() EndReason {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reason
}

// Close ends the stream with the reason (the first reason wins) and
// unregisters it.
func (s *Stream) Close(reason EndReason) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	s.reason = reason
	s.mu.Unlock()
	s.cancel()
	s.mgr.remove(s)
}

func (s *Stream) send(c StreamControl) {
	select {
	case s.Controls <- c:
	default:
	}
}

// StreamManager keeps the open streams and ends them when the decision that
// allowed them no longer holds. It re-evaluates every StreamEvalInterval and
// whenever policy changes, enforces closes_at, idle timeout and maximum
// duration, and warns clients ClosingWarningLead before a deadline.
type StreamManager struct {
	access       *AccessService
	policy       *PolicyService
	sessionAlive func(ctx context.Context, webSessionID string) bool
	clock        Clock
	logger       *zap.Logger

	mu      sync.Mutex
	streams map[string]*Stream
	nextID  uint64
}

func NewStreamManager(access *AccessService, policy *PolicyService, sessionAlive func(context.Context, string) bool, clock Clock, logger *zap.Logger) *StreamManager {
	return &StreamManager{
		access: access, policy: policy, sessionAlive: sessionAlive, clock: clock, logger: logger,
		streams: map[string]*Stream{},
	}
}

// Open registers a stream that a decision just allowed.
func (m *StreamManager) Open(parent context.Context, spec StreamSpec, d domain.Decision) *Stream {
	ctx, cancel := context.WithCancel(parent)
	now := m.clock.Now()
	m.mu.Lock()
	m.nextID++
	key := "k" + itoa(int(m.nextID))
	id := spec.ExecSessionID
	if id == "" {
		id = key
	}
	s := &Stream{
		ID: id, key: key, Spec: spec, Started: now, Controls: make(chan StreamControl, 8),
		ctx: ctx, cancel: cancel, mgr: m, lastActivity: now,
	}
	s.setClosesAt(d.ClosesAt, now)
	m.streams[key] = s
	m.mu.Unlock()
	monitoring.OpenStreams.WithLabelValues(string(spec.Kind)).Inc()
	return s
}

func (m *StreamManager) remove(s *Stream) {
	m.mu.Lock()
	if m.streams[s.key] == s {
		delete(m.streams, s.key)
		monitoring.OpenStreams.WithLabelValues(string(s.Spec.Kind)).Dec()
	}
	m.mu.Unlock()
}

func (s *Stream) setClosesAt(closes *time.Time, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closesAt = closes
	s.deadline, s.deadlineWhy = nil, ""
	if closes != nil {
		d := *closes
		s.deadline, s.deadlineWhy = &d, ""
	}
	if s.Spec.MaxDuration > 0 {
		max := s.Started.Add(s.Spec.MaxDuration)
		if s.deadline == nil || max.Before(*s.deadline) {
			s.deadline, s.deadlineWhy = &max, string(EndMaxDuration)
		}
	}
	if s.warnedFor != nil && (s.deadline == nil || !s.deadline.Equal(*s.warnedFor)) {
		s.warnedFor = nil
	}
}

// TerminateExecSession ends the stream(s) of an exec session (the owner's
// and live watchers').
func (m *StreamManager) TerminateExecSession(execSessionID string, reason EndReason) bool {
	var hit []*Stream
	m.mu.Lock()
	for _, s := range m.streams {
		if s.Spec.ExecSessionID == execSessionID {
			hit = append(hit, s)
		}
	}
	m.mu.Unlock()
	for _, s := range hit {
		s.Close(reason)
	}
	return len(hit) > 0
}

// TerminateWebSession ends every stream opened with a login session.
func (m *StreamManager) TerminateWebSession(webSessionID string, reason EndReason) {
	m.terminateWhere(func(s *Stream) bool { return s.Spec.WebSessionID == webSessionID }, reason)
}

// TerminateUser ends every stream of a user.
func (m *StreamManager) TerminateUser(userID int64, reason EndReason) {
	m.terminateWhere(func(s *Stream) bool { return s.Spec.UserID == userID }, reason)
}

func (m *StreamManager) terminateWhere(pred func(*Stream) bool, reason EndReason) {
	var hit []*Stream
	m.mu.Lock()
	for _, s := range m.streams {
		if pred(s) {
			hit = append(hit, s)
		}
	}
	m.mu.Unlock()
	for _, s := range hit {
		s.Close(reason)
	}
}

// CloseAll ends every stream, e.g. at shutdown.
func (m *StreamManager) CloseAll(reason EndReason) {
	m.terminateWhere(func(*Stream) bool { return true }, reason)
}

// Counts returns the number of open streams per kind.
func (m *StreamManager) Counts() map[StreamKind]int {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[StreamKind]int{}
	for _, s := range m.streams {
		out[s.Spec.Kind]++
	}
	return out
}

func (m *StreamManager) snapshot() []*Stream {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*Stream, 0, len(m.streams))
	for _, s := range m.streams {
		out = append(out, s)
	}
	return out
}

// Run supervises the streams until ctx is done.
func (m *StreamManager) Run(ctx context.Context) {
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	changes, cancel := m.policy.Subscribe()
	defer cancel()
	lastEval := m.clock.Now()
	for {
		select {
		case <-ctx.Done():
			m.CloseAll(EndPortalRestart)
			return
		case <-changes:
			m.evaluateAll(ctx)
			lastEval = m.clock.Now()
		case <-tick.C:
			now := m.clock.Now()
			if now.Sub(lastEval) >= StreamEvalInterval {
				m.evaluateAll(ctx)
				lastEval = now
				continue
			}
			m.enforceDeadlines(now)
		}
	}
}

// enforceDeadlines applies idle timeout, maximum duration, closes_at and the
// closing warning; it makes no database calls.
func (m *StreamManager) enforceDeadlines(now time.Time) {
	for _, s := range m.snapshot() {
		s.mu.Lock()
		idle := s.Spec.IdleTimeout > 0 && now.Sub(s.lastActivity) >= s.Spec.IdleTimeout
		deadline, why, warned := s.deadline, s.deadlineWhy, s.warnedFor
		s.mu.Unlock()

		if idle {
			s.Close(EndIdleTimeout)
			continue
		}
		if deadline == nil {
			continue
		}
		if !now.Before(*deadline) {
			s.Close(m.deadlineReason(s, *deadline, why))
			continue
		}
		if deadline.Sub(now) <= ClosingWarningLead && (warned == nil || !warned.Equal(*deadline)) {
			reason := string(m.deadlineReason(s, *deadline, why))
			s.mu.Lock()
			d := *deadline
			s.warnedFor = &d
			s.mu.Unlock()
			s.send(StreamControl{
				Type: "warning", ClosesAt: deadline, Reason: reason,
				Message: warningMessage(EndReason(reason)),
			})
		}
	}
}

// deadlineReason works out why access ends at the deadline.
func (m *StreamManager) deadlineReason(s *Stream, deadline time.Time, why string) EndReason {
	if why != "" {
		return EndReason(why)
	}
	if s.Spec.Kind == StreamWatch {
		return EndDenied
	}
	if sub, ok := m.policy.Current().Subject(s.Spec.UserID); ok && sub.BreakGlass != nil && sub.BreakGlass.EffectiveEnd().Equal(deadline) {
		return EndBreakGlassEnded
	}
	d := m.access.DecideFor(s.Spec.UserID, s.Spec.Target, s.Spec.Action, deadline)
	if d.Allowed {
		return EndDenied
	}
	return reasonToEnd(d.Reason)
}

func warningMessage(r EndReason) string {
	switch r {
	case EndOutsideWindow:
		return "The access window closes soon; the session will be ended."
	case EndHoliday:
		return "A holiday starts soon; the session will be ended."
	case EndGrantExpired:
		return "Your grant expires soon; the session will be ended."
	case EndBreakGlassEnded:
		return "Break-glass ends soon; the session will be ended."
	case EndMaxDuration:
		return "The maximum session length is reached soon; the session will be ended."
	}
	return "Access ends soon; the session will be ended."
}

func reasonToEnd(r domain.Reason) EndReason {
	switch r {
	case domain.ReasonUserDisabled:
		return EndUserDisabled
	case domain.ReasonOutsideWindow:
		return EndOutsideWindow
	case domain.ReasonHoliday:
		return EndHoliday
	case domain.ReasonGrantExpired:
		return EndGrantExpired
	case domain.ReasonRole:
		return EndRoleChanged
	case domain.ReasonNoGrant, domain.ReasonArgoStale, domain.ReasonProtectedNamespace:
		return EndGrantRevoked
	}
	return EndDenied
}

// evaluateAll re-runs Decide for every stream, ends the ones now denied and
// refreshes the others' closes_at. It also drops streams whose login
// session no longer exists.
func (m *StreamManager) evaluateAll(ctx context.Context) {
	now := m.clock.Now()
	snap := m.policy.Current()
	for _, s := range m.snapshot() {
		if m.sessionAlive != nil && s.Spec.WebSessionID != "" && !m.sessionAlive(ctx, s.Spec.WebSessionID) {
			s.Close(EndUserDisabled)
			continue
		}
		if s.Spec.Kind == StreamWatch {
			u, ok := snap.Users[s.Spec.UserID]
			if !ok || u.Disabled {
				s.Close(EndUserDisabled)
			} else if u.Role != domain.RoleAdmin && u.Role != domain.RoleAuditor {
				s.Close(EndRoleChanged)
			}
			continue
		}
		d := m.access.DecideFor(s.Spec.UserID, s.Spec.Target, s.Spec.Action, now)
		if !d.Allowed {
			s.Close(reasonToEnd(d.Reason))
			continue
		}
		s.mu.Lock()
		changed := !equalTimePtr(s.closesAt, d.ClosesAt)
		s.mu.Unlock()
		if changed {
			s.setClosesAt(d.ClosesAt, now)
			s.send(StreamControl{Type: "closes_at", ClosesAt: d.ClosesAt})
		}
	}
	m.enforceDeadlines(now)
}

func equalTimePtr(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.Equal(*b)
}
