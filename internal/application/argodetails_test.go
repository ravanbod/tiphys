package application

import (
	"strings"
	"testing"
)

func TestDiffResources(t *testing.T) {
	live := `{"apiVersion":"apps/v1","kind":"Deployment","metadata":{"name":"web","namespace":"shop","resourceVersion":"9",
	  "managedFields":[{"manager":"argocd"}],"annotations":{"kubectl.kubernetes.io/last-applied-configuration":"{}"}},
	  "spec":{"replicas":2},"status":{"readyReplicas":2}}`
	desired := `{"apiVersion":"apps/v1","kind":"Deployment","metadata":{"name":"web","namespace":"shop","resourceVersion":"9"},
	  "spec":{"replicas":3},"status":{"readyReplicas":2}}`
	got := diffResources([]ArgoManagedResource{
		{Kind: "Deployment", Group: "apps", Namespace: "shop", Name: "web", Live: []byte(live), Desired: []byte(desired), Modified: true},
		{Kind: "ConfigMap", Namespace: "shop", Name: "same", Live: []byte(`{"data":{"a":"1"}}`), Desired: []byte(`{"data":{"a":"1"}}`)},
		{Kind: "Secret", Namespace: "shop", Name: "db", Live: []byte(`{"data":{"password":"c2VjcmV0"}}`), Desired: []byte(`{"data":{"password":"bmV3"}}`), Modified: true},
		{Kind: "Service", Namespace: "shop", Name: "new", Desired: []byte(`{"spec":{"type":"ClusterIP"}}`)},
		{Kind: "ConfigMap", Namespace: "shop", Name: "old", Live: []byte(`{"data":{}}`)},
		{Kind: "Job", Group: "batch", Namespace: "shop", Name: "migrate", Desired: []byte(`{}`), Hook: true},
	})
	if got.Truncated || len(got.Resources) != 4 {
		t.Fatalf("resources = %+v", got.Resources)
	}
	byName := map[string]ArgoResourceDiff{}
	for _, r := range got.Resources {
		byName[r.Name] = r
	}
	web := byName["web"]
	if web.State != "changed" || !strings.Contains(web.Diff, "-  replicas: 2") || !strings.Contains(web.Diff, "+  replicas: 3") {
		t.Fatalf("web diff:\n%s", web.Diff)
	}
	for _, noise := range []string{"managedFields", "resourceVersion", "last-applied", "readyReplicas"} {
		if strings.Contains(web.Diff, noise) {
			t.Fatalf("web diff shows %s:\n%s", noise, web.Diff)
		}
	}
	if db := byName["db"]; !db.Hidden || db.Diff != "" || db.State != "changed" {
		t.Fatalf("secret: %+v", db)
	}
	if byName["new"].State != "missing" || byName["old"].State != "extra" {
		t.Fatalf("states: %+v %+v", byName["new"], byName["old"])
	}
	if got.Resources[0].Kind != "ConfigMap" || got.Resources[3].Kind != "Service" {
		t.Fatalf("not sorted by kind: %+v", got.Resources)
	}
}
