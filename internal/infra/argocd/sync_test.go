package argocd

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"

	"tiphys/internal/application"
)

// syncServer answers the sync and get calls with status and body.
func syncServer(t *testing.T, status int, body string, got *map[string]any, uri *string) *HTTPClient {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*uri = r.Method + " " + r.URL.RequestURI()
		if r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.Method == http.MethodPost {
			b, _ := io.ReadAll(r.Body)
			if err := json.Unmarshal(b, got); err != nil {
				t.Errorf("sync body: %v", err)
			}
		}
		w.WriteHeader(status)
		w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return NewHTTPClient(srv.URL, "tok", srv.Client(), zap.NewNop())
}

const finishedApp = `{"operation":null,"status":{"operationState":{"phase":"Succeeded","message":"successfully synced (all tasks run)",
  "startedAt":"2026-10-30T09:00:00Z","syncResult":{"resources":[{"status":"Synced"},{"status":"Synced"},{"status":"SyncFailed"},{"status":"PruneSkipped"}]}}}}`

func TestSyncApplicationSendsOnlyFixedOptions(t *testing.T) {
	var body map[string]any
	var uri string
	c := syncServer(t, http.StatusOK, finishedApp, &body, &uri)
	prev, err := c.SyncApplication(context.Background(), "argocd", "payments-api", "alice")
	if err != nil {
		t.Fatal(err)
	}
	if uri != "POST /api/v1/applications/payments-api/sync" {
		t.Fatalf("request = %s", uri)
	}
	if body["name"] != "payments-api" || body["appNamespace"] != "argocd" || body["prune"] != false || body["dryRun"] != false ||
		body["revision"] != nil || body["resources"] != nil || body["strategy"] != nil {
		t.Fatalf("body = %v", body)
	}
	infos, _ := body["infos"].([]any)
	if len(infos) != 1 || infos[0].(map[string]any)["value"] != "alice (tiphys)" {
		t.Fatalf("infos = %v", body["infos"])
	}
	want := application.ArgoOperation{Phase: "Succeeded", Message: "successfully synced (all tasks run)",
		StartedAt: time.Date(2026, 10, 30, 9, 0, 0, 0, time.UTC), Synced: 2, Failed: 1, Total: 4}
	if prev != want {
		t.Fatalf("prev = %+v", prev)
	}
}

func TestSyncApplicationInProgress(t *testing.T) {
	var body map[string]any
	var uri string
	c := syncServer(t, http.StatusBadRequest, `{"error":"another operation is already in progress","code":9,"message":"another operation is already in progress"}`, &body, &uri)
	if _, err := c.SyncApplication(context.Background(), "argocd", "payments-api", "alice"); !errors.Is(err, application.ErrSyncInProgress) {
		t.Fatalf("err = %v", err)
	}
}

func TestSyncApplicationRefusedShowsArgoMessage(t *testing.T) {
	var body map[string]any
	var uri string
	c := syncServer(t, http.StatusForbidden, `{"error":"permission denied","code":7,"message":"permission denied: applications, sync, payments/payments-api"}`, &body, &uri)
	_, err := c.SyncApplication(context.Background(), "argocd", "payments-api", "alice")
	if err == nil || errors.Is(err, application.ErrSyncInProgress) || !strings.Contains(err.Error(), "permission denied: applications, sync") {
		t.Fatalf("err = %v", err)
	}
}

func TestGetOperation(t *testing.T) {
	var body map[string]any
	var uri string
	c := syncServer(t, http.StatusOK, `{"operation":{"sync":{}},"status":{"operationState":{"phase":"Running","startedAt":"2026-10-30T09:05:00Z"}}}`, &body, &uri)
	op, err := c.GetOperation(context.Background(), "team-apps", "web")
	if err != nil {
		t.Fatal(err)
	}
	if uri != "GET /api/v1/applications/web?appNamespace=team-apps" {
		t.Fatalf("request = %s", uri)
	}
	if !op.Pending || op.Phase != "Running" || !op.StartedAt.Equal(time.Date(2026, 10, 30, 9, 5, 0, 0, time.UTC)) {
		t.Fatalf("op = %+v", op)
	}
	c = syncServer(t, http.StatusOK, `{"status":{}}`, &body, &uri)
	if op, err := c.GetOperation(context.Background(), "argocd", "never-synced"); err != nil || op != (application.ArgoOperation{}) {
		t.Fatalf("never synced: %+v %v", op, err)
	}
}
