package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"tiphys/internal/application"
	"tiphys/internal/domain"
)

type execSessionRepo struct{ q dbtx }

var _ application.ExecSessionRepository = execSessionRepo{}

const execCols = `id::text, user_id, username, cluster, namespace, pod, container, shell, workload_kind,
	workload_name, environment, break_glass_id, started_at, ended_at, end_reason, recording_key,
	recording_bytes, debug_container`

func scanExec(row pgx.CollectableRow) (domain.ExecSession, error) {
	var s domain.ExecSession
	var kind string
	err := row.Scan(&s.ID, &s.UserID, &s.Username, &s.Cluster, &s.Namespace, &s.Pod, &s.Container,
		&s.Shell, &kind, &s.WorkloadName, &s.Environment, &s.BreakGlassID, &s.StartedAt, &s.EndedAt,
		&s.EndReason, &s.RecordingKey, &s.RecordingBytes, &s.DebugContainer)
	s.WorkloadKind = domain.WorkloadKind(kind)
	return s, err
}

func (r execSessionRepo) Get(ctx context.Context, id string) (domain.ExecSession, error) {
	if _, err := uuid.Parse(id); err != nil {
		return domain.ExecSession{}, fmt.Errorf("get exec session %q: %w", id, application.ErrNotFound)
	}
	rows, err := r.q.Query(ctx, `SELECT `+execCols+` FROM exec_sessions WHERE id = $1`, id)
	if err != nil {
		return domain.ExecSession{}, mapErr(err, "get exec session")
	}
	s, err := pgx.CollectExactlyOneRow(rows, scanExec)
	return s, mapErr(err, "get exec session")
}

// Create stores a new session; the caller generates the UUID because it is
// also the recording prefix. A zero StartedAt uses the database clock.
func (r execSessionRepo) Create(ctx context.Context, s domain.ExecSession) error {
	if _, err := uuid.Parse(s.ID); err != nil {
		return fmt.Errorf("create exec session: invalid id %q: %w", s.ID, err)
	}
	var started any
	if !s.StartedAt.IsZero() {
		started = s.StartedAt
	}
	_, err := r.q.Exec(ctx, `
		INSERT INTO exec_sessions (id, user_id, username, cluster, namespace, pod, container, shell,
			workload_kind, workload_name, environment, break_glass_id, started_at, recording_key, debug_container)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, COALESCE($13, now()), $14, $15)`,
		s.ID, s.UserID, s.Username, s.Cluster, s.Namespace, s.Pod, s.Container, s.Shell,
		string(s.WorkloadKind), s.WorkloadName, s.Environment, s.BreakGlassID, started, s.RecordingKey, s.DebugContainer)
	return mapErr(err, "create exec session")
}

// End fails with ErrNotFound for an unknown id and ErrConflict if the session
// already ended (the first end wins).
func (r execSessionRepo) End(ctx context.Context, id string, endedAt time.Time, reason string, recordingBytes int64) error {
	if _, err := uuid.Parse(id); err != nil {
		return fmt.Errorf("end exec session %q: %w", id, application.ErrNotFound)
	}
	tag, err := r.q.Exec(ctx, `
		UPDATE exec_sessions SET ended_at = $2, end_reason = $3, recording_bytes = $4
		WHERE id = $1 AND ended_at IS NULL`, id, endedAt, reason, recordingBytes)
	if err != nil {
		return mapErr(err, "end exec session")
	}
	if tag.RowsAffected() == 1 {
		return nil
	}
	if _, err := r.Get(ctx, id); err != nil {
		return err
	}
	return fmt.Errorf("end exec session: already ended: %w", application.ErrConflict)
}

func (r execSessionRepo) List(ctx context.Context, f application.ExecSessionFilter) ([]domain.ExecSession, error) {
	var w whereBuilder
	if f.UserID != 0 {
		w.add("user_id = ?", f.UserID)
	}
	if f.Username != "" {
		w.add("lower(username) = lower(?)", f.Username)
	}
	if f.Cluster != "" {
		w.add("cluster = ?", f.Cluster)
	}
	if f.Namespace != "" {
		w.add("namespace = ?", f.Namespace)
	}
	if f.Environment != "" {
		w.add("environment = ?", f.Environment)
	}
	if f.ActiveOnly {
		w.add("ended_at IS NULL")
	}
	if f.EndedOnly {
		w.add("ended_at IS NOT NULL")
	}
	if f.BreakGlass {
		w.add("break_glass_id IS NOT NULL")
	}
	if !f.From.IsZero() {
		w.add("started_at >= ?", f.From)
	}
	if !f.To.IsZero() {
		w.add("started_at < ?", f.To)
	}
	if !f.BeforeStart.IsZero() {
		w.add("started_at < ?", f.BeforeStart)
	}
	lim := w.arg(clampLimit(f.Limit))
	rows, err := r.q.Query(ctx, `SELECT `+execCols+` FROM exec_sessions`+w.sql()+
		` ORDER BY started_at DESC, id DESC LIMIT `+lim, w.args...)
	if err != nil {
		return nil, mapErr(err, "list exec sessions")
	}
	ss, err := pgx.CollectRows(rows, scanExec)
	return ss, mapErr(err, "list exec sessions")
}

func (r execSessionRepo) EndAbandoned(ctx context.Context, at time.Time) (int64, error) {
	tag, err := r.q.Exec(ctx, `
		UPDATE exec_sessions SET ended_at = $1, end_reason = 'portal_restart' WHERE ended_at IS NULL`, at)
	return tag.RowsAffected(), mapErr(err, "end abandoned exec sessions")
}
