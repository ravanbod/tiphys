package argocd

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"

	"tiphys/internal/domain"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// argoListFields are the application list fields Argo CD supports; it
// silently drops any other.
var argoListFields = map[string]bool{
	"metadata.name": true, "metadata.namespace": true, "metadata.annotations": true, "metadata.labels": true,
	"metadata.creationTimestamp": true, "metadata.deletionTimestamp": true, "spec": true, "operation.sync": true,
	"status.sync.status": true, "status.health": true, "status.summary": true, "status.resources": true,
	"status.operationState.phase": true, "status.operationState.finishedAt": true,
	"status.operationState.startedAt": true, "status.operationState.operation.sync": true,
}

// fakeArgo serves the fixtures; overrides replaces a path's status/body.
func fakeArgo(t *testing.T, overrides map[string]int) (*HTTPClient, *[]string) {
	t.Helper()
	var requests []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.URL.RequestURI())
		if r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if st, ok := overrides[r.URL.Path]; ok {
			w.WriteHeader(st)
			w.Write([]byte(`{"error":"boom","code":13}`))
			return
		}
		files := map[string]string{
			"/api/v1/applications": "applications.json",
			"/api/v1/projects":     "projects.json",
			"/api/v1/clusters":     "clusters.json",
		}
		f, ok := files[r.URL.Path]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		for _, field := range strings.Split(r.URL.Query().Get("fields"), ",") {
			if field != "" && !argoListFields[strings.TrimPrefix(field, "items.")] {
				t.Errorf("Argo CD would silently drop field %q", field)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(fixture(t, f))
	}))
	t.Cleanup(srv.Close)
	return NewHTTPClient(srv.URL+"/", "tok", srv.Client(), zap.NewNop()), &requests
}

func TestListApplications(t *testing.T) {
	c, requests := fakeArgo(t, nil)
	apps, err := c.ListApplications(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []domain.ArgoApp{
		{Name: "payments-api", Project: "payments", Cluster: "prod-1", Namespace: "payments",
			ResourceNamespaces: []string{"payments"}, Health: "Healthy", Sync: "Synced", AppNamespace: "argocd",
			LastSyncAt: time.Date(2026, 10, 27, 9, 0, 5, 0, time.UTC), LastSyncPhase: "Succeeded"},
		// multi-source app, destination by name, resources in two namespaces
		{Name: "payments-observability", Project: "payments", Cluster: "prod-1", Namespace: "payments-monitoring",
			ResourceNamespaces: []string{"payments", "payments-monitoring"}, Health: "Degraded", Sync: "OutOfSync",
			AppNamespace: "argocd", LastSyncPhase: "Running", ClusterResources: []string{"ClusterRole/payments-read"}}, // sync still running: no finishedAt
		// in-cluster by well-known server; the cluster-scoped Namespace is listed apart
		{Name: "platform-ingress", Project: "platform", Cluster: "in-cluster", Namespace: "ingress",
			ResourceNamespaces: []string{"ingress"}, Health: "Healthy", Sync: "Synced", AppNamespace: "argocd",
			ClusterResources: []string{"Namespace/ingress"}},
		// empty project -> default; server with trailing slash in the cluster list
		{Name: "stage-web", Project: "default", Cluster: "staging", Namespace: "web", Sync: "Unknown", AppNamespace: "argocd"},
		// unknown server: no cluster name, so it covers nothing
		{Name: "orphan", Project: "platform", Cluster: "", Namespace: "x",
			ResourceNamespaces: []string{"x"}, Health: "Missing", Sync: "Unknown", AppNamespace: "argocd"},
	}
	if !reflect.DeepEqual(apps, want) {
		t.Errorf("apps =\n%+v\nwant\n%+v", apps, want)
	}
	if got := apps[1].Pairs(); len(got) != 2 {
		t.Errorf("multi-namespace app pairs = %v", got)
	}
	if got := apps[4].Pairs(); len(got) != 0 {
		t.Errorf("unresolved app pairs = %v, want none", got)
	}
	if len(*requests) != 2 || !strings.HasPrefix((*requests)[1], "/api/v1/applications?fields=") ||
		!strings.Contains((*requests)[1], "items.spec%2C") {
		t.Errorf("requests = %v", *requests)
	}
}

func TestInClusterRenamed(t *testing.T) {
	// Argo can list the in-cluster destination under another name.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/clusters" {
			w.Write([]byte(`{"items":[{"server":"https://kubernetes.default.svc","name":"mgmt"}]}`))
			return
		}
		w.Write([]byte(`{"items":[{"metadata":{"name":"a"},"spec":{"project":"p","destination":{"server":"https://kubernetes.default.svc","namespace":"n"}}}]}`))
	}))
	defer srv.Close()
	apps, err := NewHTTPClient(srv.URL, "t", srv.Client(), zap.NewNop()).ListApplications(context.Background())
	if err != nil || len(apps) != 1 || apps[0].Cluster != "mgmt" {
		t.Fatalf("apps = %+v, err = %v", apps, err)
	}
}

func TestUnmatchedDestinationsAreExplained(t *testing.T) {
	for _, tc := range []struct{ clusters, wantHint, wantDest string }{
		// Argo filters the cluster list by RBAC instead of refusing it.
		{`{"items":[]}`, "clusters, get", "https://k8s-a:6443 (2 apps)"},
		{`{"items":[{"server":"https://10.0.0.1:6443","name":"a"}]}`, "differ", "https://k8s-a:6443 (2 apps)"},
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/api/v1/clusters" {
				w.Write([]byte(tc.clusters))
				return
			}
			w.Write([]byte(`{"items":[
				{"metadata":{"name":"x"},"spec":{"destination":{"server":"https://k8s-a:6443/","namespace":"n"}}},
				{"metadata":{"name":"y"},"spec":{"destination":{"server":"https://k8s-a:6443","namespace":"n"}}}]}`))
		}))
		core, logs := observer.New(zap.WarnLevel)
		if _, err := NewHTTPClient(srv.URL, "t", srv.Client(), zap.New(core)).ListApplications(context.Background()); err != nil {
			t.Fatal(err)
		}
		srv.Close()
		if logs.Len() != 1 {
			t.Fatalf("%s: logs = %v", tc.clusters, logs.All())
		}
		f := logs.All()[0].ContextMap()
		if !strings.Contains(f["hint"].(string), tc.wantHint) || !reflect.DeepEqual(f["unmatched_destinations"], []any{tc.wantDest}) {
			t.Errorf("%s: fields = %v", tc.clusters, f)
		}
	}
}

func TestListProjects(t *testing.T) {
	c, _ := fakeArgo(t, nil)
	got, err := c.ListProjects(context.Background())
	if err != nil || !reflect.DeepEqual(got, []string{"default", "payments", "platform"}) {
		t.Fatalf("projects = %v, err = %v", got, err)
	}
}

func TestNon200IsError(t *testing.T) {
	for _, path := range []string{"/api/v1/applications", "/api/v1/clusters"} {
		for _, status := range []int{http.StatusForbidden, http.StatusInternalServerError, http.StatusNoContent} {
			c, _ := fakeArgo(t, map[string]int{path: status})
			if apps, err := c.ListApplications(context.Background()); err == nil || apps != nil {
				t.Errorf("%s %d: apps = %v, err = %v; want nil, error", path, status, apps, err)
			}
		}
	}
	c, _ := fakeArgo(t, map[string]int{"/api/v1/projects": http.StatusBadGateway})
	if got, err := c.ListProjects(context.Background()); err == nil || got != nil {
		t.Errorf("projects = %v, err = %v", got, err)
	}
}

func TestBadTokenAndBadJSON(t *testing.T) {
	c, _ := fakeArgo(t, nil)
	c.token = "wrong"
	if _, err := c.ListProjects(context.Background()); err == nil {
		t.Error("wrong token: want error")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte(`{"items":[`)) }))
	defer srv.Close()
	if _, err := NewHTTPClient(srv.URL, "t", srv.Client(), zap.NewNop()).ListProjects(context.Background()); err == nil {
		t.Error("truncated JSON: want error")
	}
}

func TestContextCancelled(t *testing.T) {
	c, _ := fakeArgo(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.ListApplications(ctx); err == nil {
		t.Error("cancelled ctx: want error")
	}
}
