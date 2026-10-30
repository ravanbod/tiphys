package argocd

import (
	"context"
	"net/http"
	"testing"
	"time"
)

func TestGetAppDetails(t *testing.T) {
	var body map[string]any
	var uri string
	c := syncServer(t, http.StatusOK, `{
	  "spec":{"source":{"repoURL":"https://git.example.com/payments.git"}},
	  "status":{
	    "health":{"status":"Degraded","message":"Deployment exceeded its progress deadline"},
	    "sync":{"status":"OutOfSync","revision":"abc123"},
	    "conditions":[{"type":"SyncError","message":"one or more objects failed to apply","lastTransitionTime":"2026-10-30T09:01:00Z"}],
	    "operationState":{"phase":"Failed","message":"one or more objects failed to apply","startedAt":"2026-10-30T09:00:00Z","finishedAt":"2026-10-30T09:01:00Z",
	      "operation":{"initiatedBy":{"username":"tiphys"}},
	      "syncResult":{"revision":"abc123","resources":[
	        {"group":"apps","kind":"Deployment","namespace":"payments","name":"api","status":"SyncFailed","message":"field is immutable","syncPhase":"Sync"},
	        {"group":"batch","kind":"Job","namespace":"payments","name":"migrate","status":"Synced","hookType":"PreSync","hookPhase":"Succeeded","syncPhase":"PreSync"}]}},
	    "history":[
	      {"id":1,"revision":"old111","deployedAt":"2026-10-29T08:00:00Z","initiatedBy":{"automated":true},"source":{"repoURL":"https://git.example.com/payments.git"}},
	      {"id":2,"revisions":["r1","r2"],"deployedAt":"2026-10-30T08:00:00Z","initiatedBy":{"username":"alice"},
	       "sources":[{"repoURL":"https://charts.example.com","chart":"api"},{"repoURL":"https://git.example.com/values.git"}]}]}}`, &body, &uri)
	d, err := c.GetAppDetails(context.Background(), "argocd", "payments-api")
	if err != nil {
		t.Fatal(err)
	}
	if uri != "GET /api/v1/applications/payments-api?appNamespace=argocd" {
		t.Fatalf("request = %s", uri)
	}
	if d.Health != "Degraded" || d.HealthMessage == "" || d.SyncStatus != "OutOfSync" || d.SyncRevision != "abc123" ||
		len(d.Conditions) != 1 || d.Conditions[0].Type != "SyncError" || d.Conditions[0].At == nil {
		t.Fatalf("details = %+v", d)
	}
	op := d.LastOperation
	if op == nil || op.Phase != "Failed" || op.InitiatedBy != "tiphys" || op.Revision != "abc123" || op.FinishedAt == nil ||
		len(op.Resources) != 2 || op.Resources[0].Message != "field is immutable" || op.Resources[1].HookType != "PreSync" {
		t.Fatalf("operation = %+v", op)
	}
	h := d.History
	if len(h) != 2 || h[0].Revision != "old111" || !h[0].Automated || h[0].Source != "https://git.example.com/payments.git" ||
		h[1].InitiatedBy != "alice" || len(h[1].Revisions) != 2 ||
		h[1].Source != "https://charts.example.com (api), https://git.example.com/values.git" ||
		!h[1].DeployedAt.Equal(time.Date(2026, 10, 30, 8, 0, 0, 0, time.UTC)) {
		t.Fatalf("history = %+v", h)
	}
}

func TestManagedResourcesPrefersNormalizedAndPredicted(t *testing.T) {
	var body map[string]any
	var uri string
	c := syncServer(t, http.StatusOK, `{"items":[
	  {"group":"apps","kind":"Deployment","namespace":"p","name":"api","modified":true,
	   "liveState":"{\"live\":1}","normalizedLiveState":"{\"normalized\":1}","targetState":"{\"target\":1}","predictedLiveState":"{\"predicted\":1}"},
	  {"kind":"Service","namespace":"p","name":"new","liveState":"null","targetState":"{\"target\":1}","predictedLiveState":""},
	  {"kind":"ConfigMap","namespace":"p","name":"old","liveState":"{\"live\":1}","targetState":"null"}]}`, &body, &uri)
	items, err := c.ManagedResources(context.Background(), "argocd", "payments-api")
	if err != nil {
		t.Fatal(err)
	}
	if uri != "GET /api/v1/applications/payments-api/managed-resources?appNamespace=argocd" || len(items) != 3 {
		t.Fatalf("request %s, items %+v", uri, items)
	}
	if string(items[0].Live) != `{"normalized":1}` || string(items[0].Desired) != `{"predicted":1}` || !items[0].Modified {
		t.Fatalf("modified: %+v", items[0])
	}
	if items[1].Live != nil || string(items[1].Desired) != `{"target":1}` {
		t.Fatalf("missing: %+v", items[1])
	}
	if string(items[2].Live) != `{"live":1}` || items[2].Desired != nil {
		t.Fatalf("extra: %+v", items[2])
	}
}

func TestRevisionMetadata(t *testing.T) {
	var body map[string]any
	var uri string
	c := syncServer(t, http.StatusOK, `{"author":"Alice <alice@example.com>","date":"2026-10-30T07:55:00Z","message":"Bump api to 1.4","tags":["v1.4"]}`, &body, &uri)
	m, err := c.RevisionMetadata(context.Background(), "argocd", "payments-api", "abc123")
	if err != nil {
		t.Fatal(err)
	}
	if uri != "GET /api/v1/applications/payments-api/revisions/abc123/metadata?appNamespace=argocd" ||
		m.Author != "Alice <alice@example.com>" || m.Message != "Bump api to 1.4" || m.Date == nil || len(m.Tags) != 1 {
		t.Fatalf("request %s, metadata %+v", uri, m)
	}
	c = syncServer(t, http.StatusNotFound, `{"message":"not a git revision"}`, &body, &uri)
	if _, err := c.RevisionMetadata(context.Background(), "argocd", "payments-api", "1.4.0"); err == nil {
		t.Fatal("a failed lookup must be an error")
	}
}
