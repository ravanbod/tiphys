// Package loki implements application.AuditSink by pushing audit events to
// Loki, the copy of the audit trail that the portal cannot alter.
package loki

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"go.uber.org/zap"

	"tiphys/internal/domain"
)

const logBodyLimit = 1024

// Options are the optional credentials of the Loki endpoint. At most one of
// BearerToken and Username should be set; the portal's credentials must be
// able to push but not delete.
type Options struct {
	Username    string
	Password    string
	BearerToken string
	// TenantID is sent as X-Scope-OrgID when Loki runs multi-tenant.
	TenantID string
}

// HTTPSink pushes one JSON line per event to POST /loki/api/v1/push.
type HTTPSink struct {
	pushURL    string
	opts       Options
	httpClient *http.Client
	logger     *zap.Logger
}

func NewHTTPSink(baseURL string, opts Options, httpClient *http.Client, logger *zap.Logger) *HTTPSink {
	return &HTTPSink{
		pushURL:    strings.TrimRight(baseURL, "/") + "/loki/api/v1/push",
		opts:       opts,
		httpClient: httpClient,
		logger:     logger,
	}
}

// line is the JSON written to Loki. Field names are snake_case and match the
// audit_events columns.
type line struct {
	ID           int64          `json:"id,omitempty"`
	At           time.Time      `json:"at"`
	UserID       *int64         `json:"user_id,omitempty"`
	Username     string         `json:"username"`
	EventType    string         `json:"event_type"`
	Action       string         `json:"action,omitempty"`
	Cluster      string         `json:"cluster,omitempty"`
	Namespace    string         `json:"namespace,omitempty"`
	WorkloadKind string         `json:"workload_kind,omitempty"`
	WorkloadName string         `json:"workload_name,omitempty"`
	Pod          string         `json:"pod,omitempty"`
	Container    string         `json:"container,omitempty"`
	Environment  string         `json:"environment,omitempty"`
	Result       string         `json:"result"`
	Reason       string         `json:"reason,omitempty"`
	BreakGlassID *int64         `json:"break_glass_id,omitempty"`
	SessionID    string         `json:"session_id,omitempty"`
	Details      map[string]any `json:"details,omitempty"`
	SourceIP     string         `json:"source_ip,omitempty"`
	UserAgent    string         `json:"user_agent,omitempty"`
}

type pushRequest struct {
	Streams []stream `json:"streams"`
}

type stream struct {
	Stream map[string]string `json:"stream"`
	// Values are [unix-nanosecond-string, line] pairs.
	Values [][2]string `json:"values"`
}

// Push sends e. A 204 is success; anything else is an error and the caller
// retries.
func (s *HTTPSink) Push(ctx context.Context, e domain.AuditEvent) error {
	at := e.At
	if at.IsZero() {
		at = time.Now()
	}
	at = at.UTC()

	logLine, err := json.Marshal(line{
		ID: e.ID, At: at, UserID: e.UserID, Username: e.Username, EventType: e.EventType,
		Action: e.Action, Cluster: e.Cluster, Namespace: e.Namespace,
		WorkloadKind: e.WorkloadKind, WorkloadName: e.WorkloadName, Pod: e.Pod, Container: e.Container,
		Environment: e.Environment, Result: string(e.Result), Reason: e.Reason,
		BreakGlassID: e.BreakGlassID, SessionID: e.SessionID, Details: e.Details,
		SourceIP: e.SourceIP, UserAgent: e.UserAgent,
	})
	if err != nil {
		s.logger.Error("failed to encode audit event", zap.String("event_type", e.EventType), zap.Error(err))
		return fmt.Errorf("failed to encode audit event: %w", err)
	}

	// Only low-cardinality values become labels; everything else stays in the line.
	body, err := json.Marshal(pushRequest{Streams: []stream{{
		Stream: map[string]string{"app": "tiphys", "event_type": e.EventType},
		Values: [][2]string{{strconv.FormatInt(at.UnixNano(), 10), string(logLine)}},
	}}})
	if err != nil {
		return fmt.Errorf("failed to encode loki request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.pushURL, bytes.NewReader(body))
	if err != nil {
		s.logger.Error("failed to build loki request", zap.String("url", s.pushURL), zap.Error(err))
		return fmt.Errorf("failed to build loki request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	switch {
	case s.opts.BearerToken != "":
		req.Header.Set("Authorization", "Bearer "+s.opts.BearerToken)
	case s.opts.Username != "":
		req.SetBasicAuth(s.opts.Username, s.opts.Password)
	}
	if s.opts.TenantID != "" {
		req.Header.Set("X-Scope-OrgID", s.opts.TenantID)
	}

	resp, err := s.httpClient.Do(req)
	if err != nil {
		s.logger.Error("loki push failed", zap.String("url", s.pushURL), zap.Error(err))
		return fmt.Errorf("loki push failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, logBodyLimit))
		s.logger.Error("loki returned unexpected status", zap.String("url", s.pushURL), zap.Int("status", resp.StatusCode), zap.String("body", string(respBody)))
		return fmt.Errorf("loki returned status %d", resp.StatusCode)
	}
	io.Copy(io.Discard, resp.Body)
	return nil
}
