package kubeaccess

import (
	"context"
	"encoding/base64"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"
	authv1 "k8s.io/api/authorization/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
	clienttesting "k8s.io/client-go/testing"

	"tiphys/internal/application"
)

func base64Std(b []byte) string { return base64.StdEncoding.EncodeToString(b) }

// memStore is a CredentialStore whose changes tests trigger by hand.
type memStore struct {
	mu       sync.Mutex
	creds    map[string][]byte
	onChange func(string)
}

func (m *memStore) Put(_ context.Context, c string, kc []byte) error {
	m.mu.Lock()
	if m.creds == nil {
		m.creds = map[string][]byte{}
	}
	m.creds[c] = kc
	cb := m.onChange
	m.mu.Unlock()
	if cb != nil {
		cb(c)
	}
	return nil
}
func (m *memStore) Get(_ context.Context, c string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if b, ok := m.creds[c]; ok {
		return b, nil
	}
	return nil, application.ErrNotFound
}
func (m *memStore) Delete(_ context.Context, c string) error {
	m.mu.Lock()
	delete(m.creds, c)
	cb := m.onChange
	m.mu.Unlock()
	if cb != nil {
		cb(c)
	}
	return nil
}
func (m *memStore) List(context.Context) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []string
	for n := range m.creds {
		out = append(out, n)
	}
	return out, nil
}
func (m *memStore) Environment(ctx context.Context, c string) (string, bool, error) {
	_, err := m.Get(ctx, c)
	return "", false, err
}
func (m *memStore) Protected(ctx context.Context, c string) (bool, error) {
	_, err := m.Get(ctx, c)
	return false, err
}
func (m *memStore) Watch(ctx context.Context, f func(string)) error {
	m.mu.Lock()
	m.onChange = f
	m.mu.Unlock()
	<-ctx.Done()
	return nil
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for range 400 {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// fakeRegistry builds clients from fakes whose privilege answer the test chooses.
func fakeRegistry(store application.CredentialStore, allowAll *bool) *Registry {
	reg := NewRegistry(store, zap.NewNop())
	reg.retry = 10 * time.Millisecond
	reg.factory = func(cfg *rest.Config) (*clients, error) {
		kube := fake.NewClientset()
		kube.PrependReactor("create", "selfsubjectaccessreviews", func(a clienttesting.Action) (bool, runtime.Object, error) {
			s := a.(clienttesting.CreateAction).GetObject().(*authv1.SelfSubjectAccessReview)
			s.Status.Allowed = *allowAll || connectorOnly(*s.Spec.ResourceAttributes)
			return true, s, nil
		})
		dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
			map[schema.GroupVersionResource]string{kyvernoClusterPolicies: "ClusterPolicyList"})
		return &clients{kube: kube, dyn: dyn, cfg: cfg}, nil
	}
	return reg
}

func TestRegistryFollowsTheStore(t *testing.T) {
	good := kubeconfigYAML(goodCluster, goodUser)
	store := &memStore{creds: map[string][]byte{"a": good}}
	over := false
	reg := fakeRegistry(store, &over)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go reg.Run(ctx)

	usable := func(c string) bool { _, err := reg.lookup(c); return err == nil }
	eventually(t, "initial cluster", func() bool { return usable("a") })

	store.Put(ctx, "b", good)
	eventually(t, "added cluster", func() bool { return usable("b") })
	if got := len(reg.Registered()); got != 2 {
		t.Fatalf("registered = %v", reg.Registered())
	}

	store.Delete(ctx, "b")
	eventually(t, "removed cluster", func() bool { return len(reg.Registered()) == 1 })
	if _, err := reg.lookup("b"); err != application.ErrClusterUnknown {
		t.Fatalf("err = %v", err)
	}

	// Replacing a credential with one that is not acceptable removes the cluster.
	store.Put(ctx, "a", kubeconfigYAML(goodCluster+"\n    insecure-skip-tls-verify: true", goodUser))
	eventually(t, "rejected replacement", func() bool { return len(reg.Registered()) == 0 })
}

func TestRegistryRefusesOverPrivilegedCredential(t *testing.T) {
	store := &memStore{creds: map[string][]byte{"admin": kubeconfigYAML(goodCluster, goodUser)}}
	over := true
	reg := fakeRegistry(store, &over)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go reg.Run(ctx)
	eventually(t, "registration", func() bool { return len(reg.Registered()) == 1 })
	time.Sleep(50 * time.Millisecond)
	if _, err := reg.lookup("admin"); err != application.ErrClusterUnusable {
		t.Fatalf("err = %v", err)
	}
	reg.SetUsable("admin", true)
	if _, err := reg.lookup("admin"); err != application.ErrClusterUnusable {
		t.Fatalf("SetUsable overrode the privilege check: %v", err)
	}
	// Rotating to a least-privilege credential makes it usable again.
	over = false
	store.Put(ctx, "admin", kubeconfigYAML(goodCluster, "    token: another-token"))
	eventually(t, "recovery", func() bool { _, err := reg.lookup("admin"); return err == nil })
}
