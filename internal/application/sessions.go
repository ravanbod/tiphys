package application

import (
	"context"
	"io"

	"tiphys/internal/domain"
)

// SessionService reads exec sessions and their recordings (admin and auditor).
type SessionService struct {
	store Store
	rec   RecordingStore
	audit *AuditService
	exec  *ExecService
}

func NewSessionService(store Store, rec RecordingStore, audit *AuditService, exec *ExecService) *SessionService {
	return &SessionService{store: store, rec: rec, audit: audit, exec: exec}
}

// SessionDetail is a session with its command list.
type SessionDetail struct {
	Session  domain.ExecSession
	Commands []CommandEntry
	Active   bool
}

// List returns sessions matching the filter, newest first.
func (s *SessionService) List(ctx context.Context, p Principal, f ExecSessionFilter) ([]domain.ExecSession, error) {
	if err := RequireRole(p, domain.RoleAdmin, domain.RoleAuditor); err != nil {
		return nil, err
	}
	if f.Limit <= 0 || f.Limit > 500 {
		f.Limit = 100
	}
	return s.store.Repos().ExecSessions.List(ctx, f)
}

// Get returns a session and the best-effort command list from its recording.
func (s *SessionService) Get(ctx context.Context, p Principal, id string) (SessionDetail, error) {
	if err := RequireRole(p, domain.RoleAdmin, domain.RoleAuditor); err != nil {
		return SessionDetail{}, err
	}
	sess, err := s.store.Repos().ExecSessions.Get(ctx, id)
	if err != nil {
		return SessionDetail{}, err
	}
	d := SessionDetail{Session: sess, Active: sess.EndedAt == nil && s.exec.IsLive(id)}
	body, err := s.rec.Open(ctx, sess.RecordingKey)
	if err != nil {
		return d, nil // the session is still shown if the recording cannot be read
	}
	defer body.Close()
	d.Commands, _ = ExtractCommands(body, sess.StartedAt)
	return d, nil
}

// OpenRecording audits the read and returns the asciicast stream.
func (s *SessionService) OpenRecording(ctx context.Context, p Principal, id string) (io.ReadCloser, domain.ExecSession, error) {
	if err := RequireRole(p, domain.RoleAdmin, domain.RoleAuditor); err != nil {
		return nil, domain.ExecSession{}, err
	}
	sess, err := s.store.Repos().ExecSessions.Get(ctx, id)
	if err != nil {
		return nil, sess, err
	}
	e := s.audit.Event(p, domain.EventExecReplay)
	e.Cluster, e.Namespace, e.Pod, e.Environment, e.SessionID = sess.Cluster, sess.Namespace, sess.Pod, sess.Environment, id
	e.Result = domain.ResultAllowed
	e.Details = map[string]any{"sessionUser": sess.Username}
	if _, err := s.audit.Record(ctx, e); err != nil {
		return nil, sess, err
	}
	body, err := s.rec.Open(ctx, sess.RecordingKey)
	return body, sess, err
}
