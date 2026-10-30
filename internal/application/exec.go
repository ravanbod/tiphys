package application

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"tiphys/internal/domain"
	"tiphys/internal/monitoring"
)

// hubReplayBytes is how much recent output a late watcher receives.
const hubReplayBytes = 64 << 10

// outputHub fans terminal output out to the session owner and live watchers.
type outputHub struct {
	mu     sync.Mutex
	subs   map[int]*hubSub
	next   int
	ring   []byte
	closed bool
}

type hubSub struct {
	ch       chan []byte
	blocking bool // the owner: never drop output
	done     <-chan struct{}
}

func newOutputHub() *outputHub { return &outputHub{subs: map[int]*hubSub{}} }

// Subscribe returns a channel of output chunks. A blocking subscriber
// applies back-pressure; others are dropped when they fall behind. done
// ends a blocking subscription.
func (h *outputHub) Subscribe(replay, blocking bool, done <-chan struct{}) (<-chan []byte, func()) {
	h.mu.Lock()
	defer h.mu.Unlock()
	sub := &hubSub{ch: make(chan []byte, 256), blocking: blocking, done: done}
	if h.closed {
		close(sub.ch)
		return sub.ch, func() {}
	}
	if replay && len(h.ring) > 0 {
		sub.ch <- append([]byte(nil), h.ring...)
	}
	id := h.next
	h.next++
	h.subs[id] = sub
	return sub.ch, func() {
		h.mu.Lock()
		if s, ok := h.subs[id]; ok {
			delete(h.subs, id)
			close(s.ch)
		}
		h.mu.Unlock()
	}
}

// Publish delivers a chunk to every subscriber.
func (h *outputHub) Publish(data []byte) {
	chunk := append([]byte(nil), data...)
	h.mu.Lock()
	h.ring = append(h.ring, chunk...)
	if len(h.ring) > hubReplayBytes {
		h.ring = h.ring[len(h.ring)-hubReplayBytes:]
	}
	subs := make([]*hubSub, 0, len(h.subs))
	ids := make([]int, 0, len(h.subs))
	for id, s := range h.subs {
		subs = append(subs, s)
		ids = append(ids, id)
	}
	h.mu.Unlock()

	for i, s := range subs {
		if s.blocking {
			select {
			case s.ch <- chunk:
			case <-s.done:
			}
			continue
		}
		select {
		case s.ch <- chunk:
		default:
			h.mu.Lock()
			if cur, ok := h.subs[ids[i]]; ok && cur == s {
				delete(h.subs, ids[i])
				close(s.ch)
			}
			h.mu.Unlock()
		}
	}
}

// Close ends every subscription.
func (h *outputHub) Close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.closed = true
	for id, s := range h.subs {
		delete(h.subs, id)
		close(s.ch)
	}
}

// liveExec is a running exec session.
type liveExec struct {
	session domain.ExecSession
	kube    ExecStream
	hub     *outputHub
	rec     *recorder
	stream  *Stream
	started domain.AuditEvent
	done    chan struct{}
	mu      sync.Mutex
	endedBy EndReason
	cols    uint16
	rows    uint16
}

// ExecRequest asks to open an exec session.
type ExecRequest struct {
	Cluster   string
	Namespace string
	Workload  domain.WorkloadRef
	Pod       string
	Container string
	Shell     string
	Cols      uint16
	Rows      uint16
	// Debug opens the shell in a new debug container that shares Container's
	// processes and the pod's network, instead of in Container itself.
	Debug bool
}

// ExecHandle is what the transport holds for an open session.
type ExecHandle struct {
	Session  domain.ExecSession
	Stream   *Stream
	Decision domain.Decision
	Settings domain.Settings
	live     *liveExec
	svc      *ExecService
	output   <-chan []byte
	cancel   func()
}

// Output is the terminal output channel for the session owner. It is closed
// when the session ends.
func (h *ExecHandle) Output() <-chan []byte { return h.output }

// Done is closed after the session has ended and been finalized (recording
// closed, database updated, audit written).
func (h *ExecHandle) Done() <-chan struct{} { return h.live.done }

// EndReason is why the session ended (valid after Done).
func (h *ExecHandle) EndReason() EndReason {
	h.live.mu.Lock()
	defer h.live.mu.Unlock()
	return h.live.endedBy
}

// Input sends terminal input to the pod and records it.
func (h *ExecHandle) Input(data []byte) error {
	h.Stream.Touch()
	h.live.rec.Input(h.svc.clock.Now(), data)
	_, err := h.live.kube.Write(data)
	return err
}

// Resize resizes the pseudo-terminal and records the change.
func (h *ExecHandle) Resize(cols, rows uint16) {
	if cols == 0 || rows == 0 {
		return
	}
	h.Stream.Touch()
	h.live.kube.Resize(cols, rows)
	h.live.rec.Resize(h.svc.clock.Now(), cols, rows)
}

// Close ends the session because the client went away.
func (h *ExecHandle) Close() {
	h.cancel()
	h.Stream.Close(EndClientClosed)
}

// ExecService opens recorded exec sessions.
type ExecService struct {
	access   *AccessService
	kube     ClusterAccess
	rec      RecordingStore
	audit    *AuditService
	streams  *StreamManager
	store    Store
	settings *SettingsService
	notify   *NotificationService
	clock    Clock
	rootCtx  context.Context
	logger   *zap.Logger

	mu   sync.Mutex
	live map[string]*liveExec
}

func NewExecService(rootCtx context.Context, access *AccessService, kube ClusterAccess, rec RecordingStore, audit *AuditService,
	streams *StreamManager, store Store, settings *SettingsService, notify *NotificationService, clock Clock, logger *zap.Logger) *ExecService {
	return &ExecService{
		access: access, kube: kube, rec: rec, audit: audit, streams: streams, store: store,
		settings: settings, notify: notify, clock: clock, rootCtx: rootCtx, logger: logger,
		live: map[string]*liveExec{},
	}
}

func (s *ExecService) normalize(r *ExecRequest) error {
	if err := validateNames(r.Namespace, r.Workload); err != nil {
		return err
	}
	if !domain.ValidDNS1123Subdomain(r.Pod) {
		return Invalid("pod", "invalid pod name")
	}
	if r.Container != "" && !domain.ValidDNS1123Label(r.Container) {
		return Invalid("container", "invalid container name")
	}
	if r.Shell == "" {
		r.Shell = "/bin/sh"
	}
	if !domain.ValidShell(r.Shell) {
		return Invalid("shell", "shell must be one of "+strings.Join(domain.ExecShells, ", "))
	}
	if r.Debug && r.Container == "" {
		return Invalid("container", "choose the container to debug")
	}
	if r.Cols == 0 || r.Cols > 1000 {
		r.Cols = 80
	}
	if r.Rows == 0 || r.Rows > 1000 {
		r.Rows = 24
	}
	return nil
}

// Open authorizes, records and opens an exec session. If recording storage
// is unavailable the session is refused.
func (s *ExecService) Open(ctx context.Context, p Principal, req ExecRequest) (*ExecHandle, error) {
	if err := s.normalize(&req); err != nil {
		return nil, err
	}
	t := wlTarget(req.Cluster, req.Namespace, req.Workload)
	t.Pod, t.Container = req.Pod, req.Container
	d, err := s.access.Authorize(ctx, p, t, domain.ActionExec)
	if err != nil {
		return nil, err
	}

	now := s.clock.Now()
	st := s.settings.Get()
	id := uuid.NewString()
	debugContainer := ""
	if req.Debug {
		debugContainer = domain.DebugContainerPrefix + id[:8]
	}
	prefix := recordingPrefix(id, now)
	retain := func() time.Time {
		return s.clock.Now().Add(time.Duration(s.settings.Get().RetentionRecordingsDays) * 24 * time.Hour)
	}

	startEvent := s.audit.Event(p, domain.EventExecStart)
	fillTarget(&startEvent, t)
	startEvent.Action = string(domain.ActionExec)
	startEvent.Environment = d.Environment
	startEvent.BreakGlassID = d.BreakGlassID
	startEvent.SessionID = id
	startEvent.Details = map[string]any{"shell": req.Shell}
	if req.Debug {
		startEvent.Details["debugContainer"] = debugContainer
	}

	refuse := func(reason string) {
		e := startEvent
		e.Result, e.Reason = domain.ResultFailed, reason
		s.audit.RecordBestEffort(ctx, e)
		monitoring.ActionsTotal.WithLabelValues("exec", "failed").Inc()
	}

	if err := s.rec.Healthy(ctx); err != nil {
		s.logger.Error("recording storage unhealthy, exec refused", zap.Error(err))
		refuse("recording_unavailable")
		return nil, ErrRecordingUnavailable
	}
	rec := newRecorder(s.rec, prefix, now, retain)
	title := fmt.Sprintf("%s %s/%s %s", p.User.Username, req.Cluster, req.Namespace, req.Pod)
	if req.Debug {
		title += " (debug)"
	}
	if err := rec.writeHeader(ctx, req.Cols, req.Rows, title, req.Shell); err != nil {
		s.logger.Error("could not start recording, exec refused", zap.Error(err))
		refuse("recording_unavailable")
		return nil, ErrRecordingUnavailable
	}

	started := startEvent
	started.Result = domain.ResultAllowed
	storedStart, err := s.audit.Record(ctx, started)
	if err != nil {
		monitoring.ActionsTotal.WithLabelValues("exec", "failed").Inc()
		return nil, fmt.Errorf("audit event could not be written, action not run: %w", err)
	}

	sess := domain.ExecSession{
		ID: id, UserID: p.User.ID, Username: p.User.Username, Cluster: req.Cluster, Namespace: req.Namespace,
		Pod: req.Pod, Container: req.Container, Shell: req.Shell, DebugContainer: debugContainer, WorkloadKind: req.Workload.Kind, WorkloadName: req.Workload.Name,
		Environment: d.Environment, BreakGlassID: d.BreakGlassID, StartedAt: now, RecordingKey: prefix,
	}
	if err := s.store.Repos().ExecSessions.Create(ctx, sess); err != nil {
		return nil, fmt.Errorf("create exec session: %w", err)
	}

	actor := p.KubeActor(d.BreakGlassID)
	kt := KubeWorkloadTarget{Cluster: req.Cluster, Namespace: req.Namespace, Workload: req.Workload}
	var kstream ExecStream
	if req.Debug {
		// The container outlives the longest session by a minute, then stops by itself.
		lifetime := time.Duration(st.ExecMaxSessionMinutes)*time.Minute + time.Minute
		kstream, err = s.kube.Debug(ctx, actor, kt, req.Pod,
			KubeDebugSpec{Name: debugContainer, Target: req.Container, Lifetime: lifetime}, req.Shell, req.Cols, req.Rows)
	} else {
		kstream, err = s.kube.Exec(ctx, actor, kt, req.Pod, req.Container, req.Shell, req.Cols, req.Rows)
	}
	if err != nil {
		s.finalizeFailedStart(sess, rec, storedStart, err)
		return nil, s.kubeErrCluster(req.Cluster, err)
	}

	stream := s.streams.Open(s.rootCtx, StreamSpec{
		Kind: StreamExec, UserID: p.User.ID, Username: p.User.Username, WebSessionID: p.SessionID,
		Target: t, Action: domain.ActionExec, ExecSessionID: id,
		IdleTimeout: time.Duration(st.ExecIdleTimeoutMinutes) * time.Minute,
		MaxDuration: time.Duration(st.ExecMaxSessionMinutes) * time.Minute,
	}, d)

	hub := newOutputHub()
	live := &liveExec{session: sess, kube: kstream, hub: hub, rec: rec, stream: stream, started: storedStart, done: make(chan struct{}), cols: req.Cols, rows: req.Rows}
	s.mu.Lock()
	s.live[id] = live
	s.mu.Unlock()

	out, cancelOut := hub.Subscribe(false, true, stream.Context().Done())
	h := &ExecHandle{Session: sess, Stream: stream, Decision: d, Settings: st, live: live, svc: s, output: out, cancel: cancelOut}
	go s.pump(live)
	go s.supervise(live)
	monitoring.ActionsTotal.WithLabelValues("exec", "allowed").Inc()

	if isProd(d.Environment) {
		title := "Exec opened in prod"
		if req.Debug {
			title = "Debug opened in prod"
		}
		s.notify.Notify(NotificationMessage{
			Event: domain.NotifyExecInProd, Title: title,
			Fields: []NotificationField{
				{"Who", p.User.Username}, {"Where", fmt.Sprintf("%s / %s / %s", req.Cluster, req.Namespace, req.Pod)},
				{"Environment", d.Environment}, {"Break-glass", boolText(d.BreakGlassID != nil)},
			},
			Link: s.notify.Link("/sessions/" + id),
		})
	}
	return h, nil
}

func boolText(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

func isProd(env string) bool {
	return strings.EqualFold(env, "prod") || strings.EqualFold(env, "production")
}

func (s *ExecService) kubeErrCluster(cluster string, err error) error {
	switch {
	case errors.Is(err, ErrProtectedNamespace), errors.Is(err, ErrPodNotInWorkload), errors.Is(err, ErrInvalidName), errors.Is(err, ErrNotFound),
		errors.Is(err, ErrDebugUnavailable), errors.As(err, new(*DebugError)):
		return err
	}
	return &ClusterError{Cluster: cluster, Err: err}
}

func (s *ExecService) finalizeFailedStart(sess domain.ExecSession, rec *recorder, started domain.AuditEvent, cause error) {
	ctx := s.rootCtx
	now := s.clock.Now()
	bytes, _ := rec.Close(ctx, sess.ID, now, "cluster_error")
	if err := s.store.Repos().ExecSessions.End(ctx, sess.ID, now, string(EndClusterError), bytes); err != nil {
		s.logger.Error("failed to end exec session record", zap.Error(err))
	}
	e := started
	e.ID, e.At = 0, time.Time{}
	e.EventType = domain.EventExecEnd
	e.Result, e.Reason = domain.ResultFailed, cause.Error()
	s.audit.RecordBestEffort(ctx, e)
}

// pump copies pod output to the recording and the hub.
func (s *ExecService) pump(l *liveExec) {
	buf := make([]byte, 32*1024)
	r := l.kube.Output()
	for {
		n, err := r.Read(buf)
		if n > 0 {
			l.rec.Output(s.clock.Now(), buf[:n])
			l.hub.Publish(buf[:n])
		}
		if err != nil {
			return
		}
	}
}

// supervise waits for the session to end, then finalizes it.
func (s *ExecService) supervise(l *liveExec) {
	recCtx, stopRec := context.WithCancel(s.rootCtx)
	go l.rec.Run(recCtx, func(err error) {
		s.logger.Error("recording failed, terminating session", zap.String("session_id", l.session.ID), zap.Error(err))
		l.stream.Close(EndRecordingFailed)
	})

	var reason EndReason
	var detail string
	select {
	case <-l.kube.Done():
		reason = EndProcessExited
		if err := l.kube.Err(); err != nil {
			detail = err.Error()
			if errors.Is(err, context.Canceled) {
				detail = ""
			}
		}
		if r := l.stream.Reason(); r != "" {
			reason = r
		}
		l.stream.Close(reason)
	case <-l.stream.Context().Done():
		reason = l.stream.Reason()
		if reason == "" {
			reason = EndClientClosed
		}
	}
	_ = l.kube.Close()
	stopRec()
	l.hub.Close()

	ctx, cancel := context.WithTimeout(s.rootCtx, 30*time.Second)
	defer cancel()
	now := s.clock.Now()
	bytes, recErr := l.rec.Close(ctx, l.session.ID, now, string(reason))
	if recErr != nil {
		s.logger.Error("failed to finalize recording", zap.String("session_id", l.session.ID), zap.Error(recErr))
	}
	if err := s.store.Repos().ExecSessions.End(ctx, l.session.ID, now, string(reason), bytes); err != nil {
		s.logger.Error("failed to end exec session record", zap.String("session_id", l.session.ID), zap.Error(err))
	}

	e := l.started
	e.ID, e.At = 0, time.Time{}
	e.EventType = domain.EventExecEnd
	e.Result, e.Reason = domain.ResultSucceeded, string(reason)
	e.Details = map[string]any{"durationSeconds": int(now.Sub(l.session.StartedAt).Seconds()), "recordingBytes": bytes, "detail": detail}
	if reason == EndRecordingFailed || recErr != nil {
		e.Result = domain.ResultFailed
	}
	s.audit.RecordBestEffort(ctx, e)

	l.mu.Lock()
	l.endedBy = reason
	l.mu.Unlock()
	s.mu.Lock()
	delete(s.live, l.session.ID)
	s.mu.Unlock()
	close(l.done)
}

// Watch attaches a read-only watcher to an open session (admin or auditor).
type WatchHandle struct {
	Session domain.ExecSession
	Stream  *Stream
	Output  <-chan []byte
	Cols    uint16
	Rows    uint16
	cancel  func()
	done    <-chan struct{}
}

// Done is closed when the watched session ends.
func (w *WatchHandle) Done() <-chan struct{} { return w.done }

// Close detaches the watcher.
func (w *WatchHandle) Close() {
	w.cancel()
	w.Stream.Close(EndClientClosed)
}

// Watch attaches the principal as a read-only watcher.
func (s *ExecService) Watch(ctx context.Context, p Principal, sessionID string) (*WatchHandle, error) {
	if err := RequireRole(p, domain.RoleAdmin, domain.RoleAuditor); err != nil {
		return nil, err
	}
	s.mu.Lock()
	l, ok := s.live[sessionID]
	s.mu.Unlock()
	if !ok {
		return nil, ErrNotFound
	}
	e := s.audit.Event(p, domain.EventExecWatch)
	e.Cluster, e.Namespace, e.Pod, e.Container = l.session.Cluster, l.session.Namespace, l.session.Pod, l.session.Container
	e.Environment, e.SessionID, e.Result = l.session.Environment, sessionID, domain.ResultAllowed
	e.Details = map[string]any{"sessionUser": l.session.Username}
	if _, err := s.audit.Record(ctx, e); err != nil {
		return nil, fmt.Errorf("audit event could not be written, action not run: %w", err)
	}
	stream := s.streams.Open(s.rootCtx, StreamSpec{
		Kind: StreamWatch, UserID: p.User.ID, Username: p.User.Username, WebSessionID: p.SessionID,
		ExecSessionID: sessionID,
	}, domain.Decision{Allowed: true})
	out, cancel := l.hub.Subscribe(true, false, stream.Context().Done())
	l.mu.Lock()
	cols, rows := l.cols, l.rows
	l.mu.Unlock()
	return &WatchHandle{Session: l.session, Stream: stream, Output: out, Cols: cols, Rows: rows, cancel: cancel, done: l.done}, nil
}

// Terminate ends an open session at an admin's request.
func (s *ExecService) Terminate(ctx context.Context, p Principal, sessionID string) error {
	if err := RequireRole(p, domain.RoleAdmin); err != nil {
		return err
	}
	s.mu.Lock()
	l, ok := s.live[sessionID]
	s.mu.Unlock()
	if !ok {
		return ErrNotFound
	}
	e := s.audit.Event(p, domain.EventExecTerminate)
	e.Cluster, e.Namespace, e.Pod, e.Environment, e.SessionID = l.session.Cluster, l.session.Namespace, l.session.Pod, l.session.Environment, sessionID
	e.Result = domain.ResultSucceeded
	e.Details = map[string]any{"sessionUser": l.session.Username}
	s.audit.RecordBestEffort(ctx, e)
	l.stream.Close(EndTerminatedByAdmin)
	return nil
}

// ActiveCount is the number of open exec sessions.
func (s *ExecService) ActiveCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.live)
}

// ActiveByUser counts open exec sessions per user id.
func (s *ExecService) ActiveByUser() map[int64]int {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[int64]int{}
	for _, l := range s.live {
		out[l.session.UserID]++
	}
	return out
}

// IsLive reports whether a session is still open.
func (s *ExecService) IsLive(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.live[id]
	return ok
}
