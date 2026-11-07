// Package integration exercises the whole portal (HTTP, WebSocket,
// application services, PostgreSQL) with fake Kubernetes, Argo CD, recording
// storage and Keycloak. It needs PostgreSQL: set TIPHYS_TEST_DATABASE_URL
// (see deploy/sql/test-db.sh); tests skip otherwise.
package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/jackc/pgx/v5"
	"go.uber.org/zap"
	"golang.org/x/crypto/bcrypt"

	"tiphys/internal/application"
	"tiphys/internal/domain"
	"tiphys/internal/infra/postgres"
	"tiphys/internal/infra/sealer"
	"tiphys/internal/transport/serverhttp"
)

const publicURL = "http://tiphys.test"

var tehran, _ = time.LoadLocation("Asia/Tehran")

// ---- fakes ----

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time  { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *fakeClock) Set(t time.Time) { c.mu.Lock(); c.t = t; c.mu.Unlock() }

type memSink struct {
	mu     sync.Mutex
	events []domain.AuditEvent
}

func (s *memSink) Push(_ context.Context, e domain.AuditEvent) error {
	s.mu.Lock()
	s.events = append(s.events, e)
	s.mu.Unlock()
	return nil
}
func (s *memSink) count(eventType string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, e := range s.events {
		if e.EventType == eventType {
			n++
		}
	}
	return n
}

// fakeArgo serves fixed apps; a sync or rollback finishes at once unless
// busy is set.
type fakeArgo struct {
	apps []domain.ArgoApp

	mu        sync.Mutex
	busy      bool
	syncs     []string // "appNamespace/name by user"
	rollbacks []string // "appNamespace/name to id"
	runs      int
	autoSync  bool
	// manifests are the objects of each revision (old111 by default: the
	// Deployment and a ConfigMap).
	manifests map[string][]application.ArgoResourceRef
}

// firstRunStartedAt is when the first sync or rollback of fakeArgo starts;
// the current deploy (ID 2) records it.
var firstRunStartedAt = time.Date(2026, 10, 30, 9, 0, 1, 0, time.UTC)

func (f *fakeArgo) RollbackApplication(_ context.Context, appNamespace, name string, id int64) (application.ArgoOperation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case f.busy:
		return application.ArgoOperation{}, application.ErrSyncInProgress
	case f.autoSync:
		return application.ArgoOperation{}, application.ErrAutoSyncEnabled
	}
	f.rollbacks = append(f.rollbacks, fmt.Sprintf("%s/%s to %d", appNamespace, name, id))
	prev := f.operation()
	f.runs++
	return prev, nil
}

func (f *fakeArgo) Manifests(_ context.Context, _, _, revision string) ([]application.ArgoResourceRef, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if m, ok := f.manifests[revision]; ok {
		return m, nil
	}
	return []application.ArgoResourceRef{
		{Group: "apps", Kind: "Deployment", Name: "payments-api"},
		{Kind: "ConfigMap", Namespace: "payments-api", Name: "old-config"},
	}, nil
}

func (f *fakeArgo) rollbackCalls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.rollbacks...)
}

func (f *fakeArgo) ListApplications(context.Context) ([]domain.ArgoApp, error) { return f.apps, nil }
func (f *fakeArgo) ListProjects(context.Context) ([]string, error) {
	return []string{"payments", "search"}, nil
}

func (f *fakeArgo) SyncApplication(_ context.Context, appNamespace, name, triggeredBy string) (application.ArgoOperation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.busy {
		return application.ArgoOperation{}, application.ErrSyncInProgress
	}
	f.syncs = append(f.syncs, appNamespace+"/"+name+" by "+triggeredBy)
	prev := f.operation()
	f.runs++
	return prev, nil
}

func (f *fakeArgo) GetOperation(context.Context, string, string) (application.ArgoOperation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.operation(), nil
}

// operation is the state after f.runs finished syncs (caller holds mu).
func (f *fakeArgo) operation() application.ArgoOperation {
	if f.runs == 0 {
		return application.ArgoOperation{}
	}
	return application.ArgoOperation{Phase: "Succeeded", Message: "successfully synced", Synced: 3, Total: 3,
		StartedAt: time.Date(2026, 10, 30, 9, 0, f.runs, 0, time.UTC)}
}

func (f *fakeArgo) GetAppDetails(context.Context, string, string) (application.ArgoAppDetails, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	started := firstRunStartedAt
	return application.ArgoAppDetails{Health: "Healthy", SyncStatus: "OutOfSync", SyncRevision: "abc123", AutoSync: f.autoSync,
		Resources: []application.ArgoResourceRef{{Group: "apps", Kind: "Deployment", Namespace: "payments-api", Name: "payments-api"},
			{Kind: "Service", Namespace: "payments-api", Name: "payments-api"}},
		Conditions: []application.ArgoCondition{{Type: "SyncError", Message: "one or more objects failed to apply"}},
		LastOperation: &application.ArgoOperationResult{Phase: "Failed", Revision: "abc123", Resources: []application.ArgoResourceResult{
			{Group: "apps", Kind: "Deployment", Namespace: "payments-api", Name: "payments-api", Status: "SyncFailed", Message: "field is immutable"}}},
		History: []application.ArgoDeploy{{ID: 0, Revision: "older0", Automated: true},
			{ID: 1, Revision: "old111", Automated: true, SameSource: true},
			{ID: 2, Revision: "abc123", InitiatedBy: "argocd-token", StartedAt: &started, SameSource: true}}}, nil
}

func (f *fakeArgo) ManagedResources(context.Context, string, string) ([]application.ArgoManagedResource, error) {
	return []application.ArgoManagedResource{
		{Group: "apps", Kind: "Deployment", Namespace: "payments-api", Name: "payments-api", Modified: true,
			Live: []byte(`{"spec":{"replicas":2}}`), Desired: []byte(`{"spec":{"replicas":3}}`)},
		{Kind: "Secret", Namespace: "payments-api", Name: "db", Modified: true,
			Live: []byte(`{"data":{"password":"b2xk"}}`), Desired: []byte(`{"data":{"password":"bmV3"}}`)},
	}, nil
}

func (f *fakeArgo) RevisionMetadata(_ context.Context, _, _, revision string) (application.ArgoRevision, error) {
	if revision != "abc123" {
		return application.ArgoRevision{}, errors.New("not a git revision")
	}
	return application.ArgoRevision{Author: "Sara <sara@example.com>", Message: "Bump replicas"}, nil
}

func (f *fakeArgo) syncCalls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.syncs...)
}

type fakeRecording struct {
	mu       sync.Mutex
	down     bool
	segments map[string][][]byte
	manifest map[string]application.RecordingMeta
}

func newFakeRecording() *fakeRecording {
	return &fakeRecording{segments: map[string][][]byte{}, manifest: map[string]application.RecordingMeta{}}
}
func (f *fakeRecording) setDown(v bool) { f.mu.Lock(); f.down = v; f.mu.Unlock() }
func (f *fakeRecording) Healthy(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.down {
		return fmt.Errorf("minio is down")
	}
	return nil
}
func (f *fakeRecording) PutSegment(_ context.Context, prefix string, seq int, data []byte, _ time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.down {
		return fmt.Errorf("minio is down")
	}
	segs := f.segments[prefix]
	for len(segs) <= seq {
		segs = append(segs, nil)
	}
	segs[seq] = append([]byte(nil), data...)
	f.segments[prefix] = segs
	return nil
}
func (f *fakeRecording) PutManifest(_ context.Context, prefix string, m application.RecordingMeta, _ time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.manifest[prefix] = m
	return nil
}
func (f *fakeRecording) Open(_ context.Context, prefix string) (io.ReadCloser, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	segs, ok := f.segments[prefix]
	if !ok {
		return nil, application.ErrNotFound
	}
	return io.NopCloser(bytes.NewReader(bytes.Join(segs, nil))), nil
}

// stubOIDC answers Exchange with whatever claims the test sets.
type stubOIDC struct{ claims application.OIDCClaims }

func (*stubOIDC) AuthCodeURL(state, nonce, challenge string) string {
	return "http://keycloak.test/auth"
}
func (s *stubOIDC) Exchange(context.Context, string, string, string) (application.OIDCTokens, application.OIDCClaims, error) {
	return application.OIDCTokens{AccessToken: "a", RefreshToken: "r", IDToken: "i"}, s.claims, nil
}
func (*stubOIDC) Refresh(context.Context, string) (application.OIDCTokens, error) {
	return application.OIDCTokens{AccessToken: "a", RefreshToken: "r"}, nil
}
func (*stubOIDC) LogoutURL(string, string) string { return "" }

type noCreds struct{}

func (noCreds) Put(context.Context, string, []byte) error   { return nil }
func (noCreds) Get(context.Context, string) ([]byte, error) { return nil, application.ErrNotFound }
func (noCreds) Delete(context.Context, string) error        { return nil }
func (noCreds) List(context.Context) ([]string, error)      { return nil, nil }
func (noCreds) Environment(context.Context, string) (string, bool, error) {
	return "", false, application.ErrNotFound
}
func (noCreds) Protected(context.Context, string) (bool, error) {
	return false, application.ErrNotFound
}
func (noCreds) Watch(ctx context.Context, _ func(string)) error { <-ctx.Done(); return ctx.Err() }

// ---- environment ----

type env struct {
	t       *testing.T
	srv     *httptest.Server
	store   application.Store
	clock   *fakeClock
	kube    *fakeKube
	rec     *fakeRecording
	sink    *memSink
	policy  *application.PolicyService
	argo    *application.ArgoService
	argoAPI *fakeArgo
	cancel  context.CancelFunc
	oidc    *stubOIDC
	audit   *application.AuditService
	auth    *application.AuthService
	sealer  application.Sealer
	adminID int64
	devID   int64
	audID   int64
	teamID  int64
	otherID int64
}

func newEnv(t *testing.T) *env { return newEnvWith(t, nil) }

// newEnvWith is newEnv with the HTTP options adjusted by tune (may be nil).
func newEnvWith(t *testing.T, tune func(*serverhttp.Options)) *env { return newEnvAuth(t, tune, nil) }

// newEnvAuth is newEnvWith with the auth config adjusted by tuneAuth (may be nil).
func newEnvAuth(t *testing.T, tune func(*serverhttp.Options), tuneAuth func(*application.AuthConfig)) *env {
	t.Helper()
	base := os.Getenv("TIPHYS_TEST_DATABASE_URL")
	if base == "" {
		t.Skip("TIPHYS_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	dsn := freshDatabase(t, ctx, base)
	if err := postgres.Migrate(ctx, dsn); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	store, err := postgres.Open(ctx, postgres.Config{DSN: dsn})
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(store.Close)

	logger := zap.NewNop()
	clock := &fakeClock{t: time.Date(2026, 10, 21, 10, 0, 0, 0, tehran)} // a Wednesday
	sink := &memSink{}
	rec := newFakeRecording()
	kube := newFakeKube()
	sl, err := sealer.New("00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff")
	if err != nil {
		t.Fatal(err)
	}

	argoClient := &fakeArgo{apps: []domain.ArgoApp{
		{Name: "payments-api", Project: "payments", Cluster: "prod-1", Namespace: "payments-api", Health: "Healthy", Sync: "Synced",
			AppNamespace: "argocd", LastSyncAt: time.Date(2026, 10, 27, 9, 0, 5, 0, time.UTC), LastSyncPhase: "Succeeded"},
		// a second app with resources in the same namespace
		{Name: "payments-monitoring", Project: "observability", Cluster: "prod-1", Namespace: "monitoring", ResourceNamespaces: []string{"payments-api"},
			Health: "Degraded", Sync: "OutOfSync", LastSyncPhase: "Failed", AppNamespace: "argocd"},
		{Name: "payments-api", Project: "payments", Cluster: "stg-1", Namespace: "payments-api", Health: "Healthy", Sync: "Synced"},
		{Name: "search-api", Project: "search", Cluster: "prod-1", Namespace: "search-api", Health: "Healthy", Sync: "Synced"},
		// a cluster Argo deploys to but the portal does not know
		{Name: "elsewhere", Project: "payments", Cluster: "argo-only", Namespace: "payments-api", Health: "Healthy", Sync: "Synced"},
	}}
	argo := application.NewArgoService(argoClient, clock, logger)
	audit := application.NewAuditService(store, sink, clock, logger)
	policy := application.NewPolicyService(store, argo, clock, logger)
	argo.SetOnChange(policy.Changed)
	settings := application.NewSettingsService(store, audit, logger)
	if err := settings.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	policy.SetBreakGlassEnabled(func() bool { return settings.Get().BreakGlassMode.Enabled() })
	policy.SetSyncClusterKinds(func() []string { return settings.Get().ArgoSyncClusterKinds })
	access := application.NewAccessService(policy, audit, clock)
	notify := application.NewNotificationService(nopNotifier{}, settings, audit, publicURL, logger)
	oidcStub := &stubOIDC{}
	// The lowest bcrypt cost: cost 12 takes seconds per hash under -race.
	authCfg := application.AuthConfig{AdminGroups: []string{"tiphys-admins", "/platform/sre"}, BootstrapAdmins: []string{"Boot.Admin", " mina "},
		PasswordCost: bcrypt.MinCost}
	if tuneAuth != nil {
		tuneAuth(&authCfg)
	}
	auth := application.NewAuthService(oidcStub, sl, store, policy, audit, clock, authCfg, logger)
	alive := func(ctx context.Context, id string) bool {
		_, err := store.Repos().WebSessions.Get(ctx, id)
		return err == nil
	}
	streams := application.NewStreamManager(access, policy, alive, clock, logger)
	auth.SetOnSessionEnded(func(id string) { streams.TerminateWebSession(id, application.EndUserDisabled) })
	ops := application.NewOperations()
	workloads := application.NewWorkloadService(ctx, access, kube, argo, audit, streams, ops, settings, notify, clock, logger)
	execSvc := application.NewExecService(ctx, access, kube, rec, audit, streams, store, settings, notify, clock, logger)
	sessions := application.NewSessionService(store, rec, audit, execSvc)
	bg := application.NewBreakGlassService(store, audit, policy, settings, notify, clock, logger)
	reqs := application.NewAccessRequestService(store, audit, policy, argo, notify, clock)
	admin := application.NewAdminService(store, audit, policy, settings, notify, auth, streams, access, clock, logger)
	clusters := application.NewClusterService(store, kube, noCreds{}, audit, policy, clock, logger)

	go audit.Run(ctx)
	go streams.Run(ctx)

	e := &env{t: t, store: store, clock: clock, kube: kube, rec: rec, sink: sink, policy: policy, argo: argo, argoAPI: argoClient, cancel: cancel, sealer: sl, oidc: oidcStub, auth: auth, audit: audit}
	e.seed(ctx)
	if err := argo.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	policy.Changed(ctx)

	h := serverhttp.NewHandler(serverhttp.Services{
		Auth: auth, Access: access, Workloads: workloads, Exec: execSvc, Sessions: sessions, Audit: audit, BreakGlass: bg,
		Requests: reqs, Admin: admin, Clusters: clusters, Argo: argo, Settings: settings, Streams: streams, Policy: policy,
		Store: store, Kube: kube, Notify: notify,
	}, httpOptions(tune, t.TempDir()), logger)
	e.srv = httptest.NewServer(serverhttp.NewRouter(h, logger, "release"))
	t.Cleanup(e.srv.Close)
	return e
}

type nopNotifier struct{}

func (nopNotifier) Send(context.Context, string, string, application.NotificationMessage) error {
	return nil
}

func freshDatabase(t *testing.T, ctx context.Context, base string) string {
	t.Helper()
	conn, err := pgx.Connect(ctx, base)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close(ctx)
	name := fmt.Sprintf("integ_%d", time.Now().UnixNano())
	if _, err := conn.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatalf("create database: %v", err)
	}
	t.Cleanup(func() {
		c, err := pgx.Connect(context.Background(), base)
		if err == nil {
			_, _ = c.Exec(context.Background(), "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)")
			c.Close(context.Background())
		}
	})
	u, err := url.Parse(base)
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + name
	return u.String()
}

func (e *env) seed(ctx context.Context) {
	r := e.store.Repos()
	must := func(err error) {
		e.t.Helper()
		if err != nil {
			e.t.Fatal(err)
		}
	}
	admin, err := r.Users.Create(ctx, domain.User{Username: "reza", DisplayName: "Reza Ahmadi", Role: domain.RoleAdmin})
	must(err)
	dev, err := r.Users.Create(ctx, domain.User{Username: "sara", DisplayName: "Sara Moradi", Role: domain.RoleUser})
	must(err)
	aud, err := r.Users.Create(ctx, domain.User{Username: "leila", DisplayName: "Leila Safavi", Role: domain.RoleAuditor})
	must(err)
	other, err := r.Users.Create(ctx, domain.User{Username: "arash", DisplayName: "Arash Rahimi", Role: domain.RoleUser})
	must(err)
	e.adminID, e.devID, e.audID, e.otherID = admin.ID, dev.ID, aud.ID, other.ID
	team, err := r.Teams.Create(ctx, domain.Team{Name: "Payments"})
	must(err)
	must(r.Teams.SetMembers(ctx, team.ID, []int64{dev.ID}))
	e.teamID = team.ID
	for _, c := range []domain.Cluster{{Name: "prod-1", Environment: "prod"}, {Name: "stg-1", Environment: "staging"}} {
		c.Reachable, c.PrivilegeCheckOK, c.AdmissionMode = true, true, domain.AdmissionVAP
		_, err := r.Clusters.Create(ctx, c)
		must(err)
	}
	_, err = r.Grants.Create(ctx, domain.Grant{HolderKind: domain.HolderTeam, TeamID: team.ID,
		Scope:   domain.Scope{Kind: domain.ScopeArgoProject, ArgoProject: "payments"},
		Actions: []domain.Action{domain.ActionLogs, domain.ActionExec, domain.ActionRestart, domain.ActionRunJob}, CreatedBy: "test"})
	must(err)
}

// ---- HTTP client helpers ----

type client struct {
	e      *env
	token  string
	csrf   string
	userID int64
}

// login creates a web session for the user and returns an API client.
func (e *env) login(userID int64) *client {
	e.t.Helper()
	token := fmt.Sprintf("token-%d-%d", userID, time.Now().UnixNano())
	sealed := func(s string) string {
		v, err := e.sealer.Seal([]byte(s))
		if err != nil {
			e.t.Fatal(err)
		}
		return v
	}
	csrf := fmt.Sprintf("csrf-%d", userID)
	now := e.clock.Now()
	err := e.store.Repos().WebSessions.Create(context.Background(), application.WebSession{
		ID: application.SessionID(token), UserID: userID, CSRFToken: csrf,
		AccessToken: sealed("a"), RefreshToken: sealed("r"), IDToken: sealed("i"),
		CreatedAt: now, LastRefreshAt: now, ExpiresAt: now.Add(24 * time.Hour),
	})
	if err != nil {
		e.t.Fatal(err)
	}
	return &client{e: e, token: token, csrf: csrf, userID: userID}
}

func (c *client) do(method, path string, body any) (int, map[string]any, []byte) {
	c.e.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, c.e.srv.URL+path, rd)
	req.AddCookie(&http.Cookie{Name: "tiphys_session", Value: c.token})
	req.Header.Set("X-CSRF-Token", c.csrf)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		c.e.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	return resp.StatusCode, m, raw
}

func (c *client) get(path string) (int, map[string]any, []byte) { return c.do("GET", path, nil) }

// list decodes a JSON array response.
func (c *client) list(path string) (int, []map[string]any) {
	c.e.t.Helper()
	status, _, raw := c.get(path)
	var l []map[string]any
	_ = json.Unmarshal(raw, &l)
	return status, l
}

func errCode(m map[string]any) string {
	if e, ok := m["error"].(map[string]any); ok {
		s, _ := e["code"].(string)
		return s
	}
	return ""
}

func errDetailReason(m map[string]any) string {
	if e, ok := m["error"].(map[string]any); ok {
		if d, ok := e["details"].(map[string]any); ok {
			if dec, ok := d["decision"].(map[string]any); ok {
				s, _ := dec["reason"].(string)
				return s
			}
		}
	}
	return ""
}

// ---- WebSocket helpers ----

type wsClient struct {
	conn     *websocket.Conn
	mu       sync.Mutex
	binary   bytes.Buffer
	controls []map[string]any
	closed   chan struct{}
}

func (c *client) dialWS(path string) (*wsClient, *http.Response, error) {
	c.e.t.Helper()
	u := "ws" + strings.TrimPrefix(c.e.srv.URL, "http") + path
	if strings.Contains(u, "?") {
		u += "&csrf=" + c.csrf
	} else {
		u += "?csrf=" + c.csrf
	}
	hdr := http.Header{}
	hdr.Set("Origin", publicURL)
	hdr.Set("Cookie", "tiphys_session="+c.token)
	conn, resp, err := websocket.DefaultDialer.Dial(u, hdr)
	if err != nil {
		return nil, resp, err
	}
	w := &wsClient{conn: conn, closed: make(chan struct{})}
	go func() {
		defer close(w.closed)
		for {
			typ, data, err := conn.ReadMessage()
			if err != nil {
				return
			}
			w.mu.Lock()
			if typ == websocket.BinaryMessage {
				w.binary.Write(data)
			} else {
				var m map[string]any
				_ = json.Unmarshal(data, &m)
				w.controls = append(w.controls, m)
			}
			w.mu.Unlock()
		}
	}()
	c.e.t.Cleanup(func() { conn.Close() })
	return w, resp, nil
}

func (w *wsClient) send(data []byte) { _ = w.conn.WriteMessage(websocket.BinaryMessage, data) }

func (w *wsClient) output() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.binary.String()
}

func (w *wsClient) control(typ string) map[string]any {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, c := range w.controls {
		if c["type"] == typ {
			return c
		}
	}
	return nil
}

func waitFor(t *testing.T, d time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("timed out after %s waiting for %s", d, what)
}

// setTime moves the fake clock and refreshes the Argo cache, as the real
// 60-second refresh would have done by then.
func (e *env) setTime(t time.Time) {
	e.t.Helper()
	e.clock.Set(t)
	if err := e.argo.Refresh(context.Background()); err != nil {
		e.t.Fatal(err)
	}
}

func httpOptions(tune func(*serverhttp.Options), webDir string) serverhttp.Options {
	o := serverhttp.Options{PublicURL: publicURL, RateLimitPerMinute: 1000, WebDir: webDir, ArgoPublicURL: "https://argo.example.com"}
	if tune != nil {
		tune(&o)
	}
	return o
}
