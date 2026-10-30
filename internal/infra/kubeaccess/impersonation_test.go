package kubeaccess

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"go.uber.org/zap"

	"tiphys/internal/application"
	"tiphys/internal/domain"
)

// recordingAPIServer is a TLS server that answers list calls with empty
// lists (Rollouts with 404, as when the CRD is missing) and records headers.
type recordingAPIServer struct {
	*httptest.Server
	mu   sync.Mutex
	reqs []http.Header
	auth []string
}

func newRecordingAPIServer(t *testing.T) *recordingAPIServer {
	t.Helper()
	s := &recordingAPIServer{}
	s.Server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.reqs = append(s.reqs, r.Header.Clone())
		s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(r.URL.Path, "/rollouts"):
			w.WriteHeader(http.StatusNotFound)
			json.NewEncoder(w).Encode(map[string]any{"kind": "Status", "apiVersion": "v1", "status": "Failure", "reason": "NotFound", "code": 404})
		default:
			json.NewEncoder(w).Encode(map[string]any{"kind": "List", "apiVersion": "v1", "items": []any{}, "metadata": map[string]any{}})
		}
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *recordingAPIServer) kubeconfig(token string) []byte {
	ca := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: s.Certificate().Raw})
	b64 := base64Std(ca)
	return kubeconfigYAML("    server: "+s.URL+"\n    certificate-authority-data: "+b64, "    token: "+token)
}

func TestImpersonationHeadersReachTheAPIServer(t *testing.T) {
	srv := newRecordingAPIServer(t)
	store := &memStore{creds: map[string][]byte{"c1": srv.kubeconfig("connector-token")}}
	reg := NewRegistry(store, zap.NewNop())
	a := New(reg, protectedSet{}, zap.NewNop())
	// Skip the privilege check: this test is about the identity on the wire.
	cfg, err := parseKubeconfig(store.creds["c1"])
	if err != nil {
		t.Fatal(err)
	}
	e := &clusterEntry{base: cfg}
	e.privilegeOK.Store(true)
	e.enabled.Store(true)
	reg.clusters["c1"] = e

	bg := int64(42)
	if _, err := a.ListWorkloads(context.Background(), application.KubeActor{Username: "alice", BreakGlassID: &bg}, "c1", "shop"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.ListWorkloads(context.Background(), application.KubeActor{Username: "bob"}, "c1", "shop"); err != nil {
		t.Fatal(err)
	}
	srv.mu.Lock()
	defer srv.mu.Unlock()
	var sawAlice, sawBob bool
	for _, h := range srv.reqs {
		if got := h.Get("Impersonate-User"); got != "tiphys:user" {
			t.Fatalf("Impersonate-User = %q", got)
		}
		if got := h.Values("Impersonate-Group"); len(got) != 1 || got[0] != "tiphys:users" {
			t.Fatalf("Impersonate-Group = %v", got)
		}
		if got := h.Get("Authorization"); got != "Bearer connector-token" {
			t.Fatalf("Authorization = %q", got)
		}
		// client-go escapes "/" in the extra's name as %2f.
		user := h.Get("Impersonate-Extra-" + "Portal.example.com%2fusername")
		bgv := h.Values("Impersonate-Extra-Portal.example.com%2fbreak-glass")
		switch user {
		case "alice":
			sawAlice = true
			if len(bgv) != 1 || bgv[0] != "42" {
				t.Fatalf("break-glass = %v", bgv)
			}
		case "bob":
			sawBob = true
			if len(bgv) != 1 || bgv[0] != "" {
				t.Fatalf("break-glass for bob = %v, want one empty value", bgv)
			}
		default:
			t.Fatalf("username extra = %q in %v", user, h)
		}
	}
	if !sawAlice || !sawBob {
		t.Fatalf("requests did not carry both actors: %v", srv.reqs)
	}
}

func TestExtraDomainIsConfigurable(t *testing.T) {
	srv := newRecordingAPIServer(t)
	cfg, _ := parseKubeconfig(srv.kubeconfig("t"))
	reg := NewRegistry(&memStore{}, zap.NewNop())
	e := &clusterEntry{base: cfg}
	e.privilegeOK.Store(true)
	e.enabled.Store(true)
	reg.clusters["c1"] = e
	a := New(reg, protectedSet{}, zap.NewNop(), WithExtraDomain("tiphys.corp.example"))
	if _, err := a.ListWorkloads(context.Background(), actor, "c1", "shop"); err != nil {
		t.Fatal(err)
	}
	srv.mu.Lock()
	defer srv.mu.Unlock()
	if got := srv.reqs[0].Get("Impersonate-Extra-Tiphys.corp.example%2fusername"); got != "alice" {
		t.Fatalf("headers = %v", srv.reqs[0])
	}
	if _, ok := srv.reqs[0]["Impersonate-Extra-Portal.example.com%2fusername"]; ok {
		t.Fatal("default domain still sent")
	}
}

func TestPerCallIdentityDoesNotLeakBetweenActors(t *testing.T) {
	// Two different actors, interleaved: each request carries only its own name.
	srv := newRecordingAPIServer(t)
	cfg, _ := parseKubeconfig(srv.kubeconfig("t"))
	reg := NewRegistry(&memStore{}, zap.NewNop())
	e := &clusterEntry{base: cfg}
	e.privilegeOK.Store(true)
	e.enabled.Store(true)
	reg.clusters["c1"] = e
	a := New(reg, protectedSet{}, zap.NewNop())
	var wg sync.WaitGroup
	for _, u := range []string{"u1", "u2", "u3", "u4"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			a.GetWorkload(context.Background(), application.KubeActor{Username: u}, application.KubeWorkloadTarget{
				Cluster: "c1", Namespace: "shop", Workload: domain.WorkloadRef{Kind: domain.KindDeployment, Name: "x"}})
		}()
	}
	wg.Wait()
	srv.mu.Lock()
	defer srv.mu.Unlock()
	seen := map[string]int{}
	for _, h := range srv.reqs {
		seen[h.Get("Impersonate-Extra-Portal.example.com%2fusername")]++
	}
	if len(seen) != 4 {
		t.Fatalf("seen = %v", seen)
	}
	if base := e.base.Impersonate; base.UserName != "" || len(base.Extra) != 0 {
		t.Fatalf("base config was mutated: %+v", base)
	}
}
