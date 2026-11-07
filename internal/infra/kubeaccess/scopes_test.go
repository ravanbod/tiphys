package kubeaccess

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"tiphys/internal/application"
)

func TestResourceScopes(t *testing.T) {
	r := newRig(t)
	r.kube.Resources = []*metav1.APIResourceList{
		{GroupVersion: "v1", APIResources: []metav1.APIResource{
			{Name: "configmaps", Kind: "ConfigMap", Namespaced: true},
			{Name: "namespaces", Kind: "Namespace"},
			{Name: "pods/exec", Kind: "PodExecOptions", Namespaced: true},
		}},
		{GroupVersion: "apps/v1", APIResources: []metav1.APIResource{{Name: "deployments", Kind: "Deployment", Namespaced: true}}},
		{GroupVersion: "rbac.authorization.k8s.io/v1", APIResources: []metav1.APIResource{{Name: "clusterroles", Kind: "ClusterRole"}}},
	}
	got, err := r.ResourceScopes(context.Background(), actor, "c1")
	if err != nil {
		t.Fatal(err)
	}
	want := map[application.KubeGroupKind]bool{
		{Kind: "ConfigMap"}: true, {Kind: "Namespace"}: false, {Group: "apps", Kind: "Deployment"}: true,
		{Group: "rbac.authorization.k8s.io", Kind: "ClusterRole"}: false,
	}
	if len(got) != len(want) {
		t.Fatalf("scopes = %v", got)
	}
	for k, v := range want {
		if n, ok := got[k]; !ok || n != v {
			t.Fatalf("%v = %v, %v", k, n, ok)
		}
	}
	// Made as the impersonated user, with a timeout.
	cfg := r.cfgs[len(r.cfgs)-1]
	if cfg.Impersonate.UserName != ImpersonatedUser || cfg.Timeout != defaultCallTimeout {
		t.Fatalf("config = %+v", cfg)
	}
	if _, err := r.ResourceScopes(context.Background(), actor, "unknown"); err == nil {
		t.Fatal("an unknown cluster must fail")
	}
}
