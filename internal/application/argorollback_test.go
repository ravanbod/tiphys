package application

import (
	"fmt"
	"testing"

	"tiphys/internal/domain"
)

func TestResolveManifests(t *testing.T) {
	scopes := map[KubeGroupKind]bool{
		{Group: "apps", Kind: "Deployment"}: true,
		{Kind: "ConfigMap"}:                 true,
		{Kind: "Namespace"}:                 false,
	}
	objs := []ArgoResourceRef{
		{Group: "apps", Kind: "Deployment", Name: "api"},                    // to the destination
		{Kind: "ConfigMap", Namespace: "other", Name: "cfg"},                // its own namespace
		{Kind: "Namespace", Namespace: "ignored", Name: "shop"},             // cluster-scoped, namespace dropped
		{Group: "example.io", Kind: "Widget", Namespace: "shop", Name: "w"}, // not served: cluster-scoped
	}
	resolved, namespaces, cluster := resolveManifests(objs, scopes, "shop")
	if fmt.Sprint(namespaces) != "[shop other]" {
		t.Fatalf("namespaces = %v", namespaces)
	}
	if fmt.Sprint(cluster) != "[Namespace/shop Widget.example.io/w]" {
		t.Fatalf("cluster resources = %v", cluster)
	}
	if resolved[0].Namespace != "shop" || resolved[2].Namespace != "" || resolved[3].Namespace != "" {
		t.Fatalf("resolved = %v", resolved)
	}
	if _, ns, _ := resolveManifests(objs[:1], scopes, ""); fmt.Sprint(ns) != "[default]" {
		t.Fatalf("no destination: %v", ns)
	}
}

func TestIncludingWidensTheApp(t *testing.T) {
	app := domain.ArgoApp{Cluster: "c", Namespace: "shop", ResourceNamespaces: []string{"shop"},
		ClusterResources: []domain.ClusterResource{{Kind: "Namespace", Name: "shop"}}}
	got := app.Including([]string{"other", "shop"}, []domain.ClusterResource{{Kind: "Namespace", Name: "shop"}, {Group: "x.io", Kind: "W", Name: "w"}})
	if fmt.Sprint(got.Pairs()) != "[{c shop} {c other}]" || len(got.ClusterResources) != 2 {
		t.Fatalf("including = %+v", got)
	}
	if len(app.ResourceNamespaces) != 1 || len(app.ClusterResources) != 1 {
		t.Fatalf("the original changed: %+v", app)
	}
	if fmt.Sprint(got.ForeignClusterResources()) != "[W.x.io/w]" {
		t.Fatalf("foreign = %v", got.ForeignClusterResources())
	}
}
