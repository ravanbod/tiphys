package argocd

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"tiphys/internal/application"
)

func TestRollbackApplicationSendsOnlyFixedOptions(t *testing.T) {
	var body map[string]any
	var uri string
	c := syncServer(t, http.StatusOK, finishedApp, &body, &uri)
	prev, err := c.RollbackApplication(context.Background(), "argocd", "payments-api", 7)
	if err != nil {
		t.Fatal(err)
	}
	if uri != "POST /api/v1/applications/payments-api/rollback" {
		t.Fatalf("request = %s", uri)
	}
	if len(body) != 5 || body["name"] != "payments-api" || body["appNamespace"] != "argocd" || body["id"] != float64(7) ||
		body["prune"] != false || body["dryRun"] != false {
		t.Fatalf("body = %v", body)
	}
	if !prev.StartedAt.Equal(time.Date(2026, 10, 30, 9, 0, 0, 0, time.UTC)) {
		t.Fatalf("prev = %+v", prev)
	}
}

func TestRollbackApplicationRefusals(t *testing.T) {
	for msg, want := range map[string]error{
		"another operation is already in progress":               application.ErrSyncInProgress,
		"rollback cannot be initiated when auto-sync is enabled": application.ErrAutoSyncEnabled,
	} {
		var body map[string]any
		var uri string
		c := syncServer(t, http.StatusBadRequest, `{"code":9,"message":"`+msg+`"}`, &body, &uri)
		if _, err := c.RollbackApplication(context.Background(), "argocd", "payments-api", 1); !errors.Is(err, want) {
			t.Fatalf("%s: err = %v", msg, err)
		}
	}
}

func TestManifests(t *testing.T) {
	var body map[string]any
	var uri string
	c := syncServer(t, http.StatusOK, `{"manifests":[
	  "{\"apiVersion\":\"apps/v1\",\"kind\":\"Deployment\",\"metadata\":{\"name\":\"api\"}}",
	  "{\"apiVersion\":\"v1\",\"kind\":\"ConfigMap\",\"metadata\":{\"name\":\"cfg\",\"namespace\":\"payments\"}}",
	  "{\"apiVersion\":\"rbac.authorization.k8s.io/v1\",\"kind\":\"ClusterRole\",\"metadata\":{\"name\":\"read\"}}"],
	  "revision":"old111"}`, &body, &uri)
	objs, err := c.Manifests(context.Background(), "argocd", "payments-api", "old111")
	if err != nil {
		t.Fatal(err)
	}
	if uri != "GET /api/v1/applications/payments-api/manifests?appNamespace=argocd&revision=old111" {
		t.Fatalf("request = %s", uri)
	}
	want := []application.ArgoResourceRef{
		{Group: "apps", Kind: "Deployment", Name: "api"},
		{Kind: "ConfigMap", Namespace: "payments", Name: "cfg"},
		{Group: "rbac.authorization.k8s.io", Kind: "ClusterRole", Name: "read"},
	}
	if len(objs) != len(want) {
		t.Fatalf("objs = %+v", objs)
	}
	for i := range want {
		if objs[i] != want[i] {
			t.Fatalf("objs[%d] = %+v, want %+v", i, objs[i], want[i])
		}
	}

	c = syncServer(t, http.StatusOK, `{"manifests":["{\"metadata\":{\"name\":\"x\"}}"]}`, &body, &uri)
	if _, err := c.Manifests(context.Background(), "argocd", "payments-api", "old111"); err == nil {
		t.Fatal("a manifest without kind must fail")
	}
}

func TestGetAppDetailsRollbackFields(t *testing.T) {
	var body map[string]any
	var uri string
	c := syncServer(t, http.StatusOK, `{
	  "spec":{"source":{"repoURL":"https://git.example.com/p.git","path":"deploy","targetRevision":"main"},
	    "syncPolicy":{"automated":{"prune":true}}},
	  "status":{
	    "resources":[{"group":"apps","kind":"Deployment","namespace":"payments","name":"api"}],
	    "history":[
	      {"id":1,"revision":"a","deployedAt":"2026-10-29T08:00:00Z","source":{"repoURL":"https://git.example.com/p.git","path":"old"}},
	      {"id":2,"revision":"b","deployedAt":"2026-10-30T08:00:00Z","deployStartedAt":"2026-10-30T07:59:58Z",
	       "source":{"repoURL":"https://git.example.com/p.git","path":"deploy","targetRevision":"v1.2"}}]}}`, &body, &uri)
	d, err := c.GetAppDetails(context.Background(), "argocd", "payments-api")
	if err != nil {
		t.Fatal(err)
	}
	if !d.AutoSync || d.MultiSource || len(d.Resources) != 1 || d.Resources[0].Name != "api" {
		t.Fatalf("details = %+v", d)
	}
	h := d.History
	if h[0].SameSource || h[0].StartedAt != nil || !h[1].SameSource || h[1].StartedAt == nil ||
		!h[1].StartedAt.Equal(time.Date(2026, 10, 30, 7, 59, 58, 0, time.UTC)) {
		t.Fatalf("history = %+v", h)
	}
}

func TestSyncPolicyAutomated(t *testing.T) {
	off, on := false, true
	for _, tc := range []struct {
		p    *syncPolicy
		want bool
	}{
		{nil, false},
		{&syncPolicy{}, false},
		{&syncPolicy{Automated: &struct {
			Enabled *bool `json:"enabled"`
		}{}}, true},
		{&syncPolicy{Automated: &struct {
			Enabled *bool `json:"enabled"`
		}{Enabled: &off}}, false},
		{&syncPolicy{Automated: &struct {
			Enabled *bool `json:"enabled"`
		}{Enabled: &on}}, true},
	} {
		if got := tc.p.automated(); got != tc.want {
			t.Fatalf("%+v: %v", tc.p, got)
		}
	}
}
