package kubeaccess

import (
	"context"
	"errors"
	"reflect"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"tiphys/internal/application"
)

func ns(name string) *corev1.Namespace {
	return &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}}
}

func TestListNamespacesSortedWithoutProtected(t *testing.T) {
	r := newRig(t, ns("shop"), ns("billing"), ns("kube-system"))
	got, err := r.ListNamespaces(context.Background(), actor, "c1")
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"billing", "shop"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	// The call is made as the impersonated identity with the real username.
	if len(r.cfgs) != 1 || r.cfgs[0].Impersonate.UserName != ImpersonatedUser {
		t.Fatalf("impersonation: %+v", r.cfgs)
	}
}

func TestListNamespacesRefusesBadInputAndUnknownCluster(t *testing.T) {
	r := newRig(t, ns("shop"))
	ctx := context.Background()
	if _, err := r.ListNamespaces(ctx, actor, "Bad_Cluster"); !errors.Is(err, application.ErrInvalidName) {
		t.Fatalf("bad cluster name: %v", err)
	}
	if _, err := r.ListNamespaces(ctx, application.KubeActor{}, "c1"); !errors.Is(err, application.ErrInvalidName) {
		t.Fatalf("empty actor: %v", err)
	}
	if _, err := r.ListNamespaces(ctx, actor, "nope"); !errors.Is(err, application.ErrClusterUnknown) {
		t.Fatalf("unknown cluster: %v", err)
	}
	r.SetUsable("c1", false)
	if _, err := r.ListNamespaces(ctx, actor, "c1"); !errors.Is(err, application.ErrClusterUnusable) {
		t.Fatalf("unusable cluster: %v", err)
	}
}
