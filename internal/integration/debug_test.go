package integration

import (
	"context"
	"strings"
	"testing"
	"time"

	"tiphys/internal/application"
	"tiphys/internal/domain"
	"tiphys/internal/transport/serverhttp"
)

func withDebug(o *serverhttp.Options) { o.DebugEnabled = true }

func TestDebugSessionIsAnExecSession(t *testing.T) {
	e := newEnvWith(t, withDebug)
	dev := e.login(e.devID)
	if _, m, _ := dev.get("/api/v1/me"); m["settings"].(map[string]any)["debugEnabled"] != true {
		t.Fatalf("me.settings: %v", m["settings"])
	}

	ws, _, err := dev.dialWS(wl + "/pods/payments-api-abc/debug?container=app&shell=/bin/bash")
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, 3*time.Second, "ready", func() bool { return ws.control("ready") != nil })
	sessionID, _ := ws.control("ready")["sessionId"].(string)
	ws.send([]byte("dig payments-api\r"))
	waitFor(t, 3*time.Second, "echoed output", func() bool { return strings.Contains(ws.output(), "dig payments-api") })
	ws.conn.Close()
	<-ws.closed

	e.kube.mu.Lock()
	debugs := e.kube.debugs
	e.kube.mu.Unlock()
	want := domain.DebugContainerPrefix + sessionID[:8]
	// Default max session is 120 minutes; the container outlives it by one.
	if len(debugs) != 1 || debugs[0].Name != want || debugs[0].Target != "app" || debugs[0].Lifetime != 121*time.Minute {
		t.Fatalf("debug specs: %+v", debugs)
	}
	waitFor(t, 5*time.Second, "session ended", func() bool {
		s, err := e.store.Repos().ExecSessions.Get(context.Background(), sessionID)
		return err == nil && s.EndedAt != nil
	})
	s, _ := e.store.Repos().ExecSessions.Get(context.Background(), sessionID)
	if s.DebugContainer != want || s.Container != "app" || s.Shell != "/bin/bash" {
		t.Fatalf("session: %+v", s)
	}
	aud := e.login(e.audID)
	if status, m, _ := aud.get("/api/v1/sessions/" + sessionID); status != 200 || m["debugContainer"] != want {
		t.Fatalf("session detail: %d %v", status, m)
	}
	evs, _ := e.store.Repos().Audit.List(context.Background(), application.AuditFilter{EventType: domain.EventExecStart})
	if len(evs) != 1 || evs[0].Action != "exec" || evs[0].Details["debugContainer"] != want {
		t.Fatalf("audit: %+v", evs)
	}
}

func TestDebugNeedsExec(t *testing.T) {
	e := newEnvWith(t, withDebug)
	dev := e.login(e.devID)
	ctx := context.Background()
	grants, _ := e.store.Repos().Grants.List(ctx)
	g := grants[0]
	g.Actions = []domain.Action{domain.ActionLogs}
	if err := e.store.Repos().Grants.Update(ctx, g); err != nil {
		t.Fatal(err)
	}
	e.policy.Changed(ctx)

	ws, _, err := dev.dialWS(wl + "/pods/payments-api-abc/debug?container=app")
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, 3*time.Second, "refusal", func() bool { return ws.control("end") != nil })
	if end := ws.control("end"); end["code"] != "denied" {
		t.Fatalf("refusal: %v", end)
	}
	if len(e.kube.debugs) != 0 {
		t.Fatal("debug must not reach the cluster")
	}
}

func TestDebugFailureIsShownAndAudited(t *testing.T) {
	e := newEnvWith(t, withDebug)
	dev := e.login(e.devID)
	ws, _, err := dev.dialWS(wl + "/pods/payments-api-abc/debug?container=broken")
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, 3*time.Second, "refusal", func() bool { return ws.control("end") != nil })
	end := ws.control("end")
	if end["code"] != "debug_failed" || !strings.Contains(end["message"].(string), "ImagePullBackOff") {
		t.Fatalf("refusal: %v", end)
	}
	evs, _ := e.store.Repos().Audit.List(context.Background(), application.AuditFilter{EventType: domain.EventExecEnd})
	if len(evs) != 1 || evs[0].Result != domain.ResultFailed || !strings.Contains(evs[0].Reason, "ImagePullBackOff") {
		t.Fatalf("audit: %+v", evs)
	}
}

func TestDebugRouteOffWithoutImage(t *testing.T) {
	e := newEnv(t)
	dev := e.login(e.devID)
	if _, m, _ := dev.get("/api/v1/me"); m["settings"].(map[string]any)["debugEnabled"] != false {
		t.Fatalf("me.settings: %v", m["settings"])
	}
	if _, resp, err := dev.dialWS(wl + "/pods/payments-api-abc/debug?container=app"); err == nil || resp == nil || resp.StatusCode != 404 {
		t.Fatalf("debug without DEBUG_IMAGE: %v %v", err, resp)
	}
}
