package credentials

import (
	"context"
	"sort"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"tiphys/internal/application"
)

type changes struct {
	mu  sync.Mutex
	got []string
}

func (c *changes) add(n string) { c.mu.Lock(); c.got = append(c.got, n); c.mu.Unlock() }
func (c *changes) has(n string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, g := range c.got {
		if g == n {
			return true
		}
	}
	return false
}
func (c *changes) count() int { c.mu.Lock(); defer c.mu.Unlock(); return len(c.got) }

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for range 600 {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// clusterSecret builds a Secret as an operator would write it.
func clusterSecret(name, cluster, kubeconfig string) *corev1.Secret {
	sec := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "tiphys"}}
	if cluster != "" {
		sec.Annotations = map[string]string{ClusterAnnotation: cluster}
	}
	if kubeconfig != "" {
		sec.Data = map[string][]byte{KubeconfigKey: []byte(kubeconfig)}
	}
	return sec
}

func TestSecretStoreEnvironmentAnnotation(t *testing.T) {
	ctx := context.Background()
	labeled := clusterSecret("a", "labeled", "kc")
	labeled.Annotations[EnvironmentAnnotation] = " production "
	blank := clusterSecret("b", "blank", "kc")
	blank.Annotations[EnvironmentAnnotation] = "  "
	s := NewSecretStore(fake.NewSimpleClientset(labeled, blank, clusterSecret("c", "plain", "kc")), "tiphys", 0, zap.NewNop())

	if env, ok, err := s.Environment(ctx, "labeled"); env != "production" || !ok || err != nil {
		t.Fatalf("labeled: %q %v %v", env, ok, err)
	}
	for _, c := range []string{"blank", "plain"} {
		if env, ok, err := s.Environment(ctx, c); env != "" || ok || err != nil {
			t.Fatalf("%s: %q %v %v", c, env, ok, err)
		}
	}
	if _, _, err := s.Environment(ctx, "missing"); err != application.ErrNotFound {
		t.Fatalf("missing: %v", err)
	}
}

func TestSecretStoreProtectedAnnotation(t *testing.T) {
	ctx := context.Background()
	on := clusterSecret("a", "on", "kc")
	on.Annotations[ProtectedAnnotation] = " true "
	off := clusterSecret("b", "off", "kc")
	off.Annotations[ProtectedAnnotation] = "false"
	junk := clusterSecret("c", "junk", "kc")
	junk.Annotations[ProtectedAnnotation] = "yes please"
	s := NewSecretStore(fake.NewSimpleClientset(on, off, junk, clusterSecret("d", "plain", "kc")), "tiphys", 0, zap.NewNop())

	if p, err := s.Protected(ctx, "on"); !p || err != nil {
		t.Fatalf("on: %v %v", p, err)
	}
	for _, c := range []string{"off", "junk", "plain"} {
		if p, err := s.Protected(ctx, c); p || err != nil {
			t.Fatalf("%s: %v %v", c, p, err)
		}
	}
	if _, err := s.Protected(ctx, "missing"); err != application.ErrNotFound {
		t.Fatalf("missing: %v", err)
	}
}

func TestSecretStoreReadsAnnotatedSecretsWhateverTheirName(t *testing.T) {
	ctx := context.Background()
	cs := fake.NewClientset(
		clusterSecret("anything-i-like", "prod-1", "kc-prod"),
		clusterSecret("stage-config", "stage", "kc-stage"),
		clusterSecret("unannotated", "", "kc-ignored"), // not a credential
		clusterSecret("no-kubeconfig", "empty", ""),    // annotated but nothing to use
	)
	s := NewSecretStore(cs, "tiphys", time.Minute, zap.NewNop())

	if b, err := s.Get(ctx, "prod-1"); err != nil || string(b) != "kc-prod" {
		t.Fatalf("get prod-1 = %q, %v", b, err)
	}
	if _, err := s.Get(ctx, "empty"); err != application.ErrNotFound {
		t.Fatalf("a Secret without a kubeconfig key: %v", err)
	}
	if _, err := s.Get(ctx, "unannotated"); err != application.ErrNotFound {
		t.Fatalf("an unannotated Secret must not register a cluster: %v", err)
	}
	names, err := s.List(ctx)
	sort.Strings(names)
	// "empty" is listed (the annotation says it is a cluster); Get explains why it is unusable.
	if err != nil || len(names) != 3 || names[0] != "empty" || names[1] != "prod-1" || names[2] != "stage" {
		t.Fatalf("list = %v, %v", names, err)
	}
}

func TestSecretStoreSeveralSecretsForOneClusterUsesTheFirstByName(t *testing.T) {
	cs := fake.NewClientset(clusterSecret("b-copy", "prod-1", "kc-b"), clusterSecret("a-copy", "prod-1", "kc-a"))
	s := NewSecretStore(cs, "tiphys", time.Minute, zap.NewNop())
	if b, err := s.Get(context.Background(), "prod-1"); err != nil || string(b) != "kc-a" {
		t.Fatalf("get = %q, %v", b, err)
	}
	if names, _ := s.List(context.Background()); len(names) != 1 {
		t.Fatalf("a cluster must be listed once: %v", names)
	}
}

func TestSecretStoreIsReadOnly(t *testing.T) {
	cs := fake.NewClientset(clusterSecret("operator-made", "prod-1", "kc"))
	s := NewSecretStore(cs, "tiphys", time.Minute, zap.NewNop())
	if err := s.Put(context.Background(), "prod-1", []byte("x")); !errorsIs(err, ErrReadOnly) {
		t.Fatalf("Put: %v", err)
	}
	if err := s.Delete(context.Background(), "prod-1"); !errorsIs(err, ErrReadOnly) {
		t.Fatalf("Delete: %v", err)
	}
	// Nothing was written: the client saw no create, update, patch or delete.
	for _, a := range cs.Actions() {
		switch a.GetVerb() {
		case "create", "update", "patch", "delete", "delete-collection":
			t.Fatalf("the store wrote to the cluster: %v", a)
		}
	}
}

func TestSecretStoreIgnoresInvalidClusterNames(t *testing.T) {
	for _, bad := range []string{"Prod", "a/b", "x_y", "a b", string(make([]byte, 64))} {
		s := NewSecretStore(fake.NewClientset(clusterSecret("s", bad, "kc")), "tiphys", time.Minute, zap.NewNop())
		if names, err := s.List(context.Background()); err != nil || len(names) != 0 {
			t.Fatalf("annotation %q: list = %v, %v", bad, names, err)
		}
	}
	s := NewSecretStore(fake.NewClientset(), "tiphys", time.Minute, zap.NewNop())
	for _, n := range []string{"", "Prod", "a/b"} {
		if _, err := s.Get(context.Background(), n); err == nil || !isInvalid(err) {
			t.Fatalf("Get(%q) = %v", n, err)
		}
	}
}

func isInvalid(err error) bool { return errorsIs(err, application.ErrInvalidName) }

func TestSecretStoreWatch(t *testing.T) {
	cs := fake.NewClientset(clusterSecret("existing-secret", "existing", "kc"))
	s := NewSecretStore(cs, "tiphys", 50*time.Millisecond, zap.NewNop())
	ctx, cancel := context.WithCancel(context.Background())
	secrets := cs.CoreV1().Secrets("tiphys")
	var c changes
	done := make(chan error, 1)
	go func() { done <- s.Watch(ctx, c.add) }()

	waitFor(t, "initial add", func() bool { return c.has("existing") })
	if _, err := secrets.Create(ctx, clusterSecret("new-secret", "new", "kc"), metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "add", func() bool { return c.has("new") })

	// A Secret without the annotation is not reported.
	secrets.Create(ctx, clusterSecret("unrelated", "", "kc"), metav1.CreateOptions{})
	time.Sleep(150 * time.Millisecond)
	if c.has("") {
		t.Fatal("an unannotated Secret was reported")
	}

	n := c.count()
	upd := clusterSecret("new-secret", "new", "kc-v2")
	secrets.Update(ctx, upd, metav1.UpdateOptions{})
	waitFor(t, "update", func() bool { return c.count() > n })

	// Moving the annotation to another cluster reports both names.
	renamed := clusterSecret("new-secret", "renamed", "kc-v2")
	secrets.Update(ctx, renamed, metav1.UpdateOptions{})
	waitFor(t, "new name", func() bool { return c.has("renamed") })
	n = c.count()
	// Removing the annotation reports the cluster the Secret used to register.
	secrets.Update(ctx, clusterSecret("new-secret", "", "kc-v2"), metav1.UpdateOptions{})
	waitFor(t, "annotation removed", func() bool { return c.count() > n })

	n = c.count()
	secrets.Delete(ctx, "existing-secret", metav1.DeleteOptions{})
	waitFor(t, "delete", func() bool { return c.count() > n })
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Watch did not return after cancel")
	}
}
