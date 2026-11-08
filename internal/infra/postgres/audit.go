package postgres

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"

	"tiphys/internal/application"
	"tiphys/internal/domain"
)

type auditRepo struct{ q dbtx }

var _ application.AuditRepository = auditRepo{}

const auditCols = `id, at, user_id, username, event_type, COALESCE(action, ''), COALESCE(cluster, ''),
	COALESCE(namespace, ''), COALESCE(workload_kind, ''), COALESCE(workload_name, ''), COALESCE(pod, ''),
	COALESCE(container, ''), COALESCE(environment, ''), result, COALESCE(reason, ''), break_glass_id,
	COALESCE(session_id, ''), details, source_ip, user_agent`

func scanAudit(row pgx.CollectableRow) (domain.AuditEvent, error) {
	var e domain.AuditEvent
	var result string
	var details []byte
	err := row.Scan(&e.ID, &e.At, &e.UserID, &e.Username, &e.EventType, &e.Action, &e.Cluster,
		&e.Namespace, &e.WorkloadKind, &e.WorkloadName, &e.Pod, &e.Container, &e.Environment,
		&result, &e.Reason, &e.BreakGlassID, &e.SessionID, &details, &e.SourceIP, &e.UserAgent)
	if err != nil {
		return e, err
	}
	e.Result = domain.AuditResult(result)
	if details != nil {
		if err := json.Unmarshal(details, &e.Details); err != nil {
			return e, fmt.Errorf("decode audit details of event %d: %w", e.ID, err)
		}
	}
	return e, nil
}

// Insert uses the database clock when e.At is zero, and returns the event
// with the generated ID and stored At.
func (r auditRepo) Insert(ctx context.Context, e domain.AuditEvent) (domain.AuditEvent, error) {
	var at any
	if !e.At.IsZero() {
		at = e.At
	}
	var details any
	if e.Details != nil {
		b, err := json.Marshal(e.Details)
		if err != nil {
			return domain.AuditEvent{}, fmt.Errorf("insert audit event: encode details: %w", err)
		}
		details = b
	}
	err := r.q.QueryRow(ctx, `
		INSERT INTO audit_events (at, user_id, username, event_type, action, cluster, namespace,
			workload_kind, workload_name, pod, container, environment, result, reason, break_glass_id,
			session_id, details, source_ip, user_agent)
		VALUES (COALESCE($1, now()), $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15,
			$16, $17, $18, $19)
		RETURNING id, at`,
		at, e.UserID, e.Username, e.EventType, nullStr(e.Action), nullStr(e.Cluster), nullStr(e.Namespace),
		nullStr(e.WorkloadKind), nullStr(e.WorkloadName), nullStr(e.Pod), nullStr(e.Container),
		nullStr(e.Environment), string(e.Result), nullStr(e.Reason), e.BreakGlassID,
		nullStr(e.SessionID), details, e.SourceIP, e.UserAgent).Scan(&e.ID, &e.At)
	if err != nil {
		return domain.AuditEvent{}, mapErr(err, "insert audit event")
	}
	return e, nil
}

// List returns events newest first (by id). From is inclusive and To
// exclusive. Limit defaults to 50 and is capped at 1000.
func (r auditRepo) List(ctx context.Context, f application.AuditFilter) ([]domain.AuditEvent, error) {
	var w whereBuilder
	if !f.From.IsZero() {
		w.add("at >= ?", f.From)
	}
	if !f.To.IsZero() {
		w.add("at < ?", f.To)
	}
	if f.Username != "" {
		w.add("lower(username) = lower(?)", f.Username)
	}
	if f.UserID != 0 {
		w.add("user_id = ?", f.UserID)
	}
	if f.EventType != "" {
		w.add("event_type = ?", f.EventType)
	}
	if f.Action != "" {
		w.add("action = ?", f.Action)
	}
	if f.Result != "" {
		w.add("result = ?", string(f.Result))
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
	if f.BreakGlassID != 0 {
		w.add("break_glass_id = ?", f.BreakGlassID)
	}
	if f.BreakGlass {
		w.add("break_glass_id IS NOT NULL")
	}
	if f.SessionID != "" {
		w.add("session_id = ?", f.SessionID)
	}
	if f.Entity != "" {
		w.add("details->>'entity' = ?", f.Entity)
	}
	if f.EntityID != "" {
		w.add("details->>'id' = ?", f.EntityID)
	}
	if f.Text != "" {
		// strpos on lowercased values: a case-insensitive substring match
		// that needs no LIKE escaping.
		p := w.arg(f.Text)
		w.add("(strpos(lower(username), lower(" + p + ")) > 0" +
			" OR strpos(lower(pod), lower(" + p + ")) > 0" +
			" OR strpos(lower(workload_name), lower(" + p + ")) > 0" +
			" OR strpos(lower(reason), lower(" + p + ")) > 0)")
	}
	if f.BeforeID != 0 {
		w.add("id < ?", f.BeforeID)
	}
	lim := w.arg(clampLimit(f.Limit))
	rows, err := r.q.Query(ctx, `SELECT `+auditCols+` FROM audit_events`+w.sql()+
		` ORDER BY id DESC LIMIT `+lim, w.args...)
	if err != nil {
		return nil, mapErr(err, "list audit events")
	}
	es, err := pgx.CollectRows(rows, scanAudit)
	return es, mapErr(err, "list audit events")
}
