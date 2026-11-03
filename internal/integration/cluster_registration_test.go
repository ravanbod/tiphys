package integration

import (
	"context"
	"errors"
	"sort"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"

	"tiphys/internal/application"
	"tiphys/internal/domain"
	"tiphys/internal/transport/serverhttp"
)

// memCreds is a CredentialStore the test fills like an operator creating and
// deleting annotated Secrets.
type memCreds struct {
	mu    sync.Mutex
	names map[string]bool
	envs  map[string]string // tiphys/environment annotation, when set
	prot  map[string]bool   // tiphys/protected annotation
	subs  []func(string)
}

func newMemCreds() *memCreds {
	return &memCreds{names: map[string]bool{}, envs: map[string]string{}, prot: map[string]bool{}}
}

func (m *memCreds) Put(context.Context, string, []byte) error { return errors.New("read-only") }
func (m *memCreds) Delete(context.Context, string) error      { return errors.New("read-only") }
func (m *memCreds) Get(_ context.Context, name string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.names[name] {
		return nil, application.ErrNotFound
	}
	return []byte("kubeconfig"), nil
}
func (m *memCreds) List(context.Context) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []string
	for n := range m.names {
		out = append(out, n)
	}
	sort.Strings(out)
	return out, nil
}
func (m *memCreds) Environment(_ context.Context, name string) (string, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.names[name] {
		return "", false, application.ErrNotFound
	}
	env, ok := m.envs[name]
	return env, ok, nil
}

func (m *memCreds) Protected(_ context.Context, name string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.names[name] {
		return false, application.ErrNotFound
	}
	return m.prot[name], nil
}

// setEnv changes the Secret's tiphys/environment annotation ("" removes it).
func (m *memCreds) setEnv(name, env string) {
	m.mu.Lock()
	if env == "" {
		delete(m.envs, name)
	} else {
		m.envs[name] = env
	}
	subs := append([]func(string){}, m.subs...)
	m.mu.Unlock()
	for _, f := range subs {
		f(name)
	}
}

// setProtected changes the Secret's tiphys/protected annotation.
func (m *memCreds) setProtected(name string, v bool) {
	m.mu.Lock()
	m.prot[name] = v
	subs := append([]func(string){}, m.subs...)
	m.mu.Unlock()
	for _, f := range subs {
		f(name)
	}
}

func (m *memCreds) Watch(ctx context.Context, onChange func(string)) error {
	m.mu.Lock()
	m.subs = append(m.subs, onChange)
	var existing []string
	for n := range m.names {
		existing = append(existing, n)
	}
	m.mu.Unlock()
	for _, n := range existing { // credentials that exist at start are reported as adds
		onChange(n)
	}
	<-ctx.Done()
	return nil
}
func (m *memCreds) set(name string, present bool) {
	m.mu.Lock()
	m.names[name] = present
	if !present {
		delete(m.names, name)
	}
	subs := append([]func(string){}, m.subs...)
	m.mu.Unlock()
	for _, f := range subs {
		f(name)
	}
}

// loadedKube reports as registered exactly the clusters whose credential exists.
type loadedKube struct {
	*fakeKube
	creds *memCreds
}

func (l *loadedKube) Registered() []string {
	names, _ := l.creds.List(context.Background())
	return names
}

func TestClustersAreRegisteredFromTheirCredentials(t *testing.T) {
	e := newEnv(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	creds := newMemCreds()
	creds.set("from-secret", true) // exists before the portal starts
	svc := application.NewClusterService(e.store, &loadedKube{fakeKube: e.kube, creds: creds}, creds, e.audit, e.policy, e.clock, zap.NewNop())
	admin := application.Principal{User: domain.User{ID: e.adminID, Username: "reza", Role: domain.RoleAdmin}}
	go svc.Run(ctx)

	row := func(name string) (domain.Cluster, error) { return e.store.Repos().Clusters.Get(ctx, name) }

	// A credential that exists at start registers its cluster, with no environment yet.
	waitFor(t, 5*time.Second, "cluster from an existing Secret", func() bool { _, err := row("from-secret"); return err == nil })
	c, _ := row("from-secret")
	if c.Environment != "" {
		t.Fatalf("environment must start empty (set in the UI): %q", c.Environment)
	}
	// A Secret created later is picked up from the watch.
	creds.set("later", true)
	waitFor(t, 5*time.Second, "cluster from a new Secret", func() bool { _, err := row("later"); return err == nil })

	// Registration is audited, by the system, with before and after.
	evs, _ := e.store.Repos().Audit.List(ctx, application.AuditFilter{EventType: domain.EventAdminChange})
	registered := 0
	for _, ev := range evs {
		if ev.Details["operation"] == "register_from_secret" && ev.Username == "system" && ev.Details["after"] != nil {
			registered++
		}
	}
	if registered != 2 {
		t.Fatalf("register_from_secret audit events: %d, want 2", registered)
	}
	if rt := svc.Runtime("later"); !rt.CredentialLoaded {
		t.Fatalf("runtime: %+v", rt)
	}

	// Deleting the Secret keeps the cluster, shown without a credential.
	creds.set("later", false)
	if _, err := row("later"); err != nil {
		t.Fatalf("the cluster must stay when its Secret goes: %v", err)
	}
	if rt := svc.Runtime("later"); rt.CredentialLoaded {
		t.Fatalf("a cluster without its Secret must report no credential: %+v", rt)
	}

	// Deleting the row is refused while the Secret exists, allowed after.
	if err := svc.Delete(ctx, admin, "from-secret"); !errors.Is(err, application.ErrConflict) {
		t.Fatalf("delete while the Secret exists: %v", err)
	}
	if err := svc.Delete(ctx, admin, "later"); err != nil {
		t.Fatalf("delete without a Secret: %v", err)
	}
	if _, err := row("later"); !errors.Is(err, application.ErrNotFound) {
		t.Fatalf("row after delete: %v", err)
	}
	// Its credential is gone, so it is not registered again.
	time.Sleep(300 * time.Millisecond)
	if _, err := row("later"); err == nil {
		t.Fatal("a deleted cluster without a Secret must not come back")
	}
}

func TestClusterEnvironmentFromTheSecretAnnotation(t *testing.T) {
	e := newEnv(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	creds := newMemCreds()
	creds.envs["annotated"] = "staging"
	creds.set("annotated", true)
	svc := application.NewClusterService(e.store, &loadedKube{fakeKube: e.kube, creds: creds}, creds, e.audit, e.policy, e.clock, zap.NewNop())
	admin := application.Principal{User: domain.User{ID: e.adminID, Username: "reza", Role: domain.RoleAdmin}}
	go svc.Run(ctx)

	env := func() string { c, _ := e.store.Repos().Clusters.Get(ctx, "annotated"); return c.Environment }

	// A new cluster takes its environment from the annotation.
	waitFor(t, 5*time.Second, "cluster registered with the annotated environment", func() bool { return env() == "staging" })
	if rt := svc.Runtime("annotated"); !rt.EnvironmentFromCredential {
		t.Fatalf("runtime: %+v", rt)
	}
	// The annotation wins: the Clusters page cannot change it.
	if _, err := svc.SetEnvironment(ctx, admin, "annotated", "production"); !errors.Is(err, application.ErrConflict) {
		t.Fatalf("set environment of an annotated cluster: %v", err)
	}
	// Changing the annotation changes the cluster, audited by the system with before and after.
	creds.setEnv("annotated", "production")
	waitFor(t, 5*time.Second, "environment follows the annotation", func() bool { return env() == "production" })
	evs, _ := e.store.Repos().Audit.List(ctx, application.AuditFilter{EventType: domain.EventAdminChange})
	audited := false
	for _, ev := range evs {
		if ev.Details["operation"] == "environment_from_secret" && ev.Username == "system" && ev.Details["before"] != nil {
			audited = true
		}
	}
	if !audited {
		t.Fatal("environment_from_secret was not audited")
	}
	// Without the annotation the label stays and is editable again.
	creds.setEnv("annotated", "")
	waitFor(t, 5*time.Second, "environment editable again", func() bool { return !svc.Runtime("annotated").EnvironmentFromCredential })
	if env() != "production" {
		t.Fatalf("removing the annotation must keep the label: %q", env())
	}
	if _, err := svc.SetEnvironment(ctx, admin, "annotated", "staging"); err != nil {
		t.Fatalf("set environment without the annotation: %v", err)
	}
}

func TestClusterProtectedFromTheSecretAnnotation(t *testing.T) {
	e := newEnv(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	creds := newMemCreds()
	creds.prot["hidden"] = true
	creds.set("hidden", true)
	svc := application.NewClusterService(e.store, &loadedKube{fakeKube: e.kube, creds: creds}, creds, e.audit, e.policy, e.clock, zap.NewNop())
	go svc.Run(ctx)

	protected := func() bool { c, _ := e.store.Repos().Clusters.Get(ctx, "hidden"); return c.Protected }
	inPolicy := func() bool { return e.policy.Current().Clusters["hidden"].Protected }

	// A new cluster starts protected, and the policy snapshot knows.
	waitFor(t, 5*time.Second, "cluster registered protected", func() bool { return protected() && inPolicy() })
	// Removing the annotation lifts it, audited by the system with before and after.
	creds.setProtected("hidden", false)
	waitFor(t, 5*time.Second, "protection follows the annotation", func() bool { return !protected() && !inPolicy() })
	evs, _ := e.store.Repos().Audit.List(ctx, application.AuditFilter{EventType: domain.EventAdminChange})
	audited := false
	for _, ev := range evs {
		if ev.Details["operation"] == "protected_from_secret" && ev.Username == "system" && ev.Details["before"] != nil {
			audited = true
		}
	}
	if !audited {
		t.Fatal("protected_from_secret was not audited")
	}
	creds.setProtected("hidden", true)
	waitFor(t, 5*time.Second, "protected again", protected)
	// A Secret that goes away leaves the cluster as it was.
	creds.set("hidden", false)
	time.Sleep(300 * time.Millisecond)
	if !protected() {
		t.Fatal("removing the Secret must keep the cluster protected")
	}
}

func TestAddingAClusterFromThePortalIsOffByDefault(t *testing.T) {
	e := newEnv(t)
	admin := e.login(e.adminID)
	for _, path := range []string{"/api/v1/admin/clusters", "/api/v1/admin/clusters/prod-1/credential"} {
		status, m, _ := admin.do("POST", path, map[string]any{"name": "x", "kubeconfig": "y"})
		if status != 404 && status != 405 {
			t.Fatalf("POST %s: %d %v", path, status, m)
		}
	}
	// The UI is told, so it hides the buttons.
	_, me, _ := admin.get("/api/v1/me")
	if s, _ := me["settings"].(map[string]any); s["clusterRegistration"] != false {
		t.Fatalf("me.settings: %v", me["settings"])
	}
	// Editing the environment and listing clusters still work.
	if status, _ := admin.list("/api/v1/admin/clusters"); status != 200 {
		t.Fatalf("list: %d", status)
	}
	if status, _, _ := admin.do("PUT", "/api/v1/admin/clusters/prod-1", map[string]any{"environment": "production"}); status != 200 {
		t.Fatalf("set environment: %d", status)
	}
}

func TestAddingAClusterFromThePortalWhenEnabled(t *testing.T) {
	e := newEnvWith(t, func(o *serverhttp.Options) { o.ClusterRegistrationFromUI = true })
	admin := e.login(e.adminID)
	_, me, _ := admin.get("/api/v1/me")
	if s, _ := me["settings"].(map[string]any); s["clusterRegistration"] != true {
		t.Fatalf("me.settings: %v", me["settings"])
	}
	status, m, _ := admin.do("POST", "/api/v1/admin/clusters", map[string]any{"name": "added-in-ui", "environment": "staging", "kubeconfig": "apiVersion: v1"})
	if status != 201 || m["name"] != "added-in-ui" || m["environment"] != "staging" {
		t.Fatalf("add: %d %v", status, m)
	}
	// A second add of the same name is a conflict.
	if status, _, _ := admin.do("POST", "/api/v1/admin/clusters", map[string]any{"name": "added-in-ui", "kubeconfig": "apiVersion: v1"}); status != 409 {
		t.Fatalf("duplicate add: %d", status)
	}
	if status, _, _ := admin.do("POST", "/api/v1/admin/clusters/added-in-ui/credential", map[string]any{"kubeconfig": "apiVersion: v1"}); status != 200 {
		t.Fatalf("replace credential: %d", status)
	}
	// The change is audited with before/after.
	evs, _ := e.store.Repos().Audit.List(context.Background(), application.AuditFilter{EventType: domain.EventAdminChange})
	found := false
	for _, ev := range evs {
		if ev.Details["entity"] == "cluster" && ev.Details["operation"] == "create" && ev.Username == "reza" {
			found = true
		}
	}
	if !found {
		t.Fatal("cluster creation was not audited")
	}
}

func TestAddedClusterIsNotRegisteredTwice(t *testing.T) {
	// Adding from the UI writes the Secret, which the credential watch then
	// reports. The row is created first, so registerFromStore must find it and do nothing.
	e := newEnv(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	creds := newMemCreds()
	kube := &loadedKube{fakeKube: e.kube, creds: creds}
	svc := application.NewClusterService(e.store, kube, &putCreds{memCreds: creds}, e.audit, e.policy, e.clock, zap.NewNop())
	svc.SetCredentialsWritable(true)
	admin := application.Principal{User: domain.User{ID: e.adminID, Username: "reza", Role: domain.RoleAdmin}}
	go svc.Run(ctx)
	time.Sleep(200 * time.Millisecond)

	if _, err := svc.Add(ctx, admin, "ui-cluster", "staging", []byte("apiVersion: v1")); err != nil {
		t.Fatalf("add: %v", err)
	}
	time.Sleep(300 * time.Millisecond) // let the watch event arrive
	evs, _ := e.store.Repos().Audit.List(ctx, application.AuditFilter{EventType: domain.EventAdminChange})
	creates, registers := 0, 0
	for _, ev := range evs {
		switch ev.Details["operation"] {
		case "create":
			creates++
		case "register_from_secret":
			if ev.Cluster == "ui-cluster" {
				registers++
			}
		}
	}
	if creates != 1 || registers != 0 {
		t.Fatalf("create events %d, register_from_secret for the new cluster %d; want 1 and 0", creates, registers)
	}
	// Deleting it also deletes its credential, since the portal manages it here.
	if err := svc.Delete(ctx, admin, "ui-cluster"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if names, _ := creds.List(ctx); len(names) != 0 {
		t.Fatalf("credential left behind: %v", names)
	}
}

// putCreds is a memCreds whose Put and Delete work, like a writable Secret store.
type putCreds struct{ *memCreds }

func (p *putCreds) Put(_ context.Context, name string, _ []byte) error {
	p.memCreds.set(name, true)
	return nil
}
func (p *putCreds) Delete(_ context.Context, name string) error {
	p.memCreds.mu.Lock()
	ok := p.memCreds.names[name]
	p.memCreds.mu.Unlock()
	if !ok {
		return application.ErrNotFound
	}
	p.memCreds.set(name, false)
	return nil
}
