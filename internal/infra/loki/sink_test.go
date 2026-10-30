package loki

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"go.uber.org/zap"

	"tiphys/internal/domain"
)

type captured struct {
	method, path string
	header       http.Header
	user, pass   string
	hasBasic     bool
	body         pushRequest
}

func fakeLoki(t *testing.T, status int) (*httptest.Server, *captured) {
	t.Helper()
	c := &captured{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.method, c.path, c.header = r.Method, r.URL.Path, r.Header.Clone()
		c.user, c.pass, c.hasBasic = r.BasicAuth()
		b, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(b, &c.body); err != nil {
			t.Errorf("body is not JSON: %v", err)
		}
		w.WriteHeader(status)
	}))
	t.Cleanup(srv.Close)
	return srv, c
}

func TestPush(t *testing.T) {
	srv, c := fakeLoki(t, http.StatusNoContent)
	sink := NewHTTPSink(srv.URL+"/", Options{Username: "u", Password: "p", TenantID: "t1"}, srv.Client(), zap.NewNop())

	uid, bg := int64(7), int64(3)
	at := time.Date(2026, 10, 20, 12, 30, 0, 123, time.FixedZone("x", 3600*3+1800))
	err := sink.Push(context.Background(), domain.AuditEvent{
		ID: 42, At: at, UserID: &uid, Username: "alice", EventType: domain.EventExecStart,
		Action: "exec", Cluster: "prod-1", Namespace: "payments", Pod: "api-1", Result: domain.ResultAllowed,
		BreakGlassID: &bg, Details: map[string]any{"shell": "/bin/sh"}, SourceIP: "10.0.0.1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if c.method != http.MethodPost || c.path != "/loki/api/v1/push" {
		t.Errorf("request = %s %s", c.method, c.path)
	}
	if !c.hasBasic || c.user != "u" || c.pass != "p" || c.header.Get("X-Scope-OrgID") != "t1" {
		t.Errorf("auth/tenant headers wrong: %v basic=%v", c.header, c.hasBasic)
	}
	if len(c.body.Streams) != 1 || len(c.body.Streams[0].Values) != 1 {
		t.Fatalf("body = %+v", c.body)
	}
	st := c.body.Streams[0]
	if len(st.Stream) != 2 || st.Stream["app"] != "tiphys" || st.Stream["event_type"] != "exec.start" {
		t.Errorf("labels = %v", st.Stream)
	}
	if st.Values[0][0] != strconv.FormatInt(at.UnixNano(), 10) {
		t.Errorf("timestamp = %s, want %d", st.Values[0][0], at.UnixNano())
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(st.Values[0][1]), &got); err != nil {
		t.Fatal(err)
	}
	for k, want := range map[string]any{
		"id": 42.0, "user_id": 7.0, "username": "alice", "event_type": "exec.start", "action": "exec",
		"cluster": "prod-1", "namespace": "payments", "pod": "api-1", "result": "allowed",
		"break_glass_id": 3.0, "source_ip": "10.0.0.1", "at": "2026-10-20T09:00:00.000000123Z",
	} {
		if got[k] != want {
			t.Errorf("line[%q] = %v, want %v", k, got[k], want)
		}
	}
	if d, _ := got["details"].(map[string]any); d["shell"] != "/bin/sh" {
		t.Errorf("details = %v", got["details"])
	}
	if _, ok := got["container"]; ok {
		t.Error("empty optional field container should be omitted")
	}
}

func TestPushBearerAndZeroTime(t *testing.T) {
	srv, c := fakeLoki(t, http.StatusNoContent)
	sink := NewHTTPSink(srv.URL, Options{BearerToken: "tok"}, srv.Client(), zap.NewNop())
	before := time.Now()
	if err := sink.Push(context.Background(), domain.AuditEvent{Username: "bob", EventType: domain.EventLogin, Result: domain.ResultSucceeded}); err != nil {
		t.Fatal(err)
	}
	if c.header.Get("Authorization") != "Bearer tok" || c.hasBasic || c.header.Get("X-Scope-OrgID") != "" {
		t.Errorf("headers = %v", c.header)
	}
	ns, _ := strconv.ParseInt(c.body.Streams[0].Values[0][0], 10, 64)
	if time.Unix(0, ns).Before(before.Add(-time.Second)) {
		t.Error("zero event time should default to now")
	}
}

func TestPushErrors(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusBadRequest, http.StatusTooManyRequests, http.StatusInternalServerError} {
		srv, _ := fakeLoki(t, status)
		sink := NewHTTPSink(srv.URL, Options{}, srv.Client(), zap.NewNop())
		if err := sink.Push(context.Background(), domain.AuditEvent{EventType: "x"}); err == nil {
			t.Errorf("status %d: want error", status)
		}
	}
	srv, _ := fakeLoki(t, http.StatusNoContent)
	url := srv.URL
	srv.Close()
	if err := NewHTTPSink(url, Options{}, http.DefaultClient, zap.NewNop()).Push(context.Background(), domain.AuditEvent{}); err == nil {
		t.Error("unreachable server: want error")
	}
}
