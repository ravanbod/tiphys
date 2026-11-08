package integration

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"tiphys/internal/application"
	"tiphys/internal/domain"
)

const wl = "/api/v1/clusters/prod-1/namespaces/payments-api/workloads/deployments/payments-api"

func TestAuthentication(t *testing.T) {
	e := newEnv(t)
	c := &client{e: e, token: "nope", csrf: "x"}
	if status, _, _ := c.get("/api/v1/me"); status != 401 {
		t.Fatalf("anonymous /me: %d", status)
	}
	dev := e.login(e.devID)
	status, me, _ := dev.get("/api/v1/me")
	if status != 200 || me["username"] != "sara" || me["role"] != "user" {
		t.Fatalf("me: %d %v", status, me)
	}
	// CSRF is required on state-changing requests.
	bad := *dev
	bad.csrf = "wrong"
	if status, m, _ := bad.do("POST", wl+"/restart", nil); status != 403 || errCode(m) != "csrf" {
		t.Fatalf("csrf: %d %v", status, m)
	}
	// Auditors without grants see no apps; users cannot use admin endpoints.
	aud := e.login(e.audID)
	if status, l := aud.list("/api/v1/apps"); status != 200 || len(l) != 0 {
		t.Fatalf("auditor apps: %d %v", status, l)
	}
	if status, _, _ := dev.get("/api/v1/admin/users"); status != 403 {
		t.Fatalf("user admin users: %d", status)
	}
	if status, _ := aud.list("/api/v1/admin/users"); status != 200 {
		t.Fatalf("auditor reads config: %d", status)
	}
	if status, m, _ := aud.do("POST", "/api/v1/admin/teams", map[string]any{"name": "x"}); status != 403 {
		t.Fatalf("auditor must not write config: %d %v", status, m)
	}
}

func TestWorkloadsAndAccess(t *testing.T) {
	e := newEnv(t)
	dev := e.login(e.devID)

	status, apps := dev.list("/api/v1/apps")
	if status != 200 || len(apps) != 2 {
		t.Fatalf("apps: %d %v", status, apps)
	}
	for _, a := range apps {
		if a["project"] != "payments" {
			t.Fatalf("user must only see the payments project: %v", a)
		}
	}
	status, ws := dev.list("/api/v1/clusters/prod-1/namespaces/payments-api/workloads")
	if status != 200 || len(ws) != 3 {
		t.Fatalf("workloads: %d %v", status, ws)
	}
	if ws[0]["environment"] != "prod" {
		t.Fatalf("environment: %v", ws[0])
	}
	// Another project's namespace is invisible and denied with no_grant.
	status, m, _ := dev.get("/api/v1/clusters/prod-1/namespaces/search-api/workloads")
	if status != 403 || errCode(m) != "denied" || errDetailReason(m) != "no_grant" {
		t.Fatalf("search-api: %d %v", status, m)
	}
	// A pod outside the workload is refused (owner-chain check) and audited.
	status, m, _ = dev.get(wl + "/pods/payments-worker-xyz/logs/download")
	if status != 403 || errCode(m) != "pod_not_in_workload" {
		t.Fatalf("foreign pod: %d %v", status, m)
	}
	// Logs download works for the right pod; log content is never stored.
	status, _, raw := dev.get(wl + "/pods/payments-api-abc/logs/download?container=app&tailLines=100")
	if status != 200 || string(raw) != "line one\nline two\n" {
		t.Fatalf("download: %d %q", status, raw)
	}
	// Every kube call carried the real username.
	e.kube.mu.Lock()
	defer e.kube.mu.Unlock()
	for _, a := range e.kube.actors {
		if a.Username != "sara" {
			t.Fatalf("kube call made as %q", a.Username)
		}
	}
}

func TestDenialsAreAudited(t *testing.T) {
	e := newEnv(t)
	dev := e.login(e.devID)
	// Revoke exec by replacing the team grant with a logs-only one.
	ctx := context.Background()
	grants, _ := e.store.Repos().Grants.List(ctx)
	g := grants[0]
	g.Actions = []domain.Action{domain.ActionLogs}
	if err := e.store.Repos().Grants.Update(ctx, g); err != nil {
		t.Fatal(err)
	}
	e.policy.Changed(ctx)

	status, m, _ := dev.do("POST", wl+"/restart", nil)
	if status != 403 || errDetailReason(m) != "no_grant" {
		t.Fatalf("restart: %d %v", status, m)
	}
	events, err := e.store.Repos().Audit.List(ctx, application.AuditFilter{EventType: domain.EventDecisionDenied})
	if err != nil || len(events) != 1 {
		t.Fatalf("denial audit: %v %v", err, events)
	}
	ev := events[0]
	if ev.Username != "sara" || ev.Action != "restart" || ev.Reason != "no_grant" || ev.Result != domain.ResultDenied || ev.Cluster != "prod-1" {
		t.Fatalf("audit event: %+v", ev)
	}
	waitFor(t, 3*time.Second, "denial pushed to the sink", func() bool { return e.sink.count(domain.EventDecisionDenied) == 1 })
}

func TestRestartAndRunJob(t *testing.T) {
	e := newEnv(t)
	dev := e.login(e.devID)

	status, m, _ := dev.do("POST", wl+"/restart", nil)
	if status != 202 || m["operationId"] == nil {
		t.Fatalf("restart: %d %v", status, m)
	}
	opID := m["operationId"].(string)
	waitFor(t, 3*time.Second, "restart outcome audited", func() bool {
		evs, _ := e.store.Repos().Audit.List(context.Background(), application.AuditFilter{EventType: domain.EventRestart})
		for _, ev := range evs {
			if ev.Result == domain.ResultSucceeded {
				return true
			}
		}
		return false
	})
	status, _, raw := dev.get("/api/v1/operations/" + opID + "/stream")
	if status != 200 || !strings.Contains(string(raw), "event:done") || !strings.Contains(string(raw), "succeeded") {
		t.Fatalf("operation stream: %d %s", status, raw)
	}

	status, m, _ = dev.do("POST", "/api/v1/clusters/prod-1/namespaces/payments-api/cronjobs/reconcile/run", nil)
	if status != 202 || m["jobName"] != "reconcile-manual-abcde" {
		t.Fatalf("run: %d %v", status, m)
	}
	status, m, _ = dev.do("POST", "/api/v1/clusters/prod-1/namespaces/payments-api/cronjobs/reconcile/run", nil)
	if status != 409 || errCode(m) != "already_running" {
		t.Fatalf("second run: %d %v", status, m)
	}
}

func TestExecRecordedAndReplayable(t *testing.T) {
	e := newEnv(t)
	dev := e.login(e.devID)
	path := wl + "/pods/payments-api-abc/exec?container=app&shell=/bin/sh&cols=100&rows=30"

	ws, _, err := dev.dialWS(path)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, 3*time.Second, "ready", func() bool { return ws.control("ready") != nil })
	ready := ws.control("ready")
	sessionID, _ := ready["sessionId"].(string)
	if sessionID == "" || ready["environment"] != "prod" {
		t.Fatalf("ready: %v", ready)
	}
	ws.send([]byte("ls -la\r"))
	waitFor(t, 3*time.Second, "echoed output", func() bool { return strings.Contains(ws.output(), "ls -la") })
	ws.send([]byte("whoami\r"))
	waitFor(t, 3*time.Second, "second output", func() bool { return strings.Contains(ws.output(), "whoami") })
	ws.conn.Close()
	<-ws.closed

	// The session ends and is finalized.
	waitFor(t, 5*time.Second, "session ended", func() bool {
		s, err := e.store.Repos().ExecSessions.Get(context.Background(), sessionID)
		return err == nil && s.EndedAt != nil
	})
	s, _ := e.store.Repos().ExecSessions.Get(context.Background(), sessionID)
	if s.EndReason != "client_closed" || s.Username != "sara" || s.Environment != "prod" || s.RecordingBytes == 0 {
		t.Fatalf("session: %+v", s)
	}

	// Auditor reads the recording and the command list.
	aud := e.login(e.audID)
	status, m, _ := aud.get("/api/v1/sessions/" + sessionID)
	if status != 200 {
		t.Fatalf("session detail: %d %v", status, m)
	}
	cmds, _ := m["commands"].([]any)
	if len(cmds) != 2 || cmds[0].(map[string]any)["text"] != "ls -la" || cmds[1].(map[string]any)["text"] != "whoami" {
		t.Fatalf("commands: %v", m["commands"])
	}
	status, _, cast := aud.get("/api/v1/sessions/" + sessionID + "/recording")
	if status != 200 || !strings.HasPrefix(string(cast), `{"version":2,"width":100,"height":30`) || !strings.Contains(string(cast), `"o"`) || !strings.Contains(string(cast), `"i"`) {
		t.Fatalf("recording: %d %.200s", status, cast)
	}
	// Developers cannot read recordings.
	if status, _, _ := dev.get("/api/v1/sessions/" + sessionID + "/recording"); status != 403 {
		t.Fatalf("user recording: %d", status)
	}
	// Start, end and replay were all audited.
	for _, typ := range []string{domain.EventExecStart, domain.EventExecEnd, domain.EventExecReplay} {
		evs, _ := e.store.Repos().Audit.List(context.Background(), application.AuditFilter{EventType: typ})
		if len(evs) != 1 {
			t.Errorf("audit %s: %d events", typ, len(evs))
		}
	}
}

func TestExecRefusedWhenRecordingDown(t *testing.T) {
	e := newEnv(t)
	dev := e.login(e.devID)
	e.rec.setDown(true)
	ws, _, err := dev.dialWS(wl + "/pods/payments-api-abc/exec")
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, 3*time.Second, "refusal", func() bool { return ws.control("end") != nil })
	if end := ws.control("end"); end["reason"] != "recording_failed" || end["code"] != "recording_unavailable" {
		t.Fatalf("refusal: %v", end)
	}
	if len(e.kube.execs) != 0 {
		t.Fatal("exec must not reach the cluster")
	}
	evs, _ := e.store.Repos().Audit.List(context.Background(), application.AuditFilter{EventType: domain.EventExecStart})
	if len(evs) != 1 || evs[0].Result != domain.ResultFailed || evs[0].Reason != "recording_unavailable" {
		t.Fatalf("audit: %+v", evs)
	}
}

func TestExecWebSocketGuards(t *testing.T) {
	e := newEnv(t)
	dev := e.login(e.devID)
	u := "ws" + strings.TrimPrefix(e.srv.URL, "http") + wl + "/pods/payments-api-abc/exec"
	// Wrong origin.
	hdr := map[string][]string{"Origin": {"http://evil.test"}, "Cookie": {"tiphys_session=" + dev.token}}
	if _, resp, err := dialer().Dial(u+"?csrf="+dev.csrf, hdr); err == nil || resp.StatusCode != 403 {
		t.Fatalf("bad origin accepted: %v %v", err, resp)
	}
	// Missing CSRF.
	hdr["Origin"] = []string{publicURL}
	if _, resp, err := dialer().Dial(u, hdr); err == nil || resp.StatusCode != 403 {
		t.Fatalf("missing csrf accepted: %v %v", err, resp)
	}
}

func TestRevokingGrantTerminatesExec(t *testing.T) {
	e := newEnv(t)
	dev := e.login(e.devID)
	ws, _, err := dev.dialWS(wl + "/pods/payments-api-abc/exec")
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, 3*time.Second, "ready", func() bool { return ws.control("ready") != nil })

	ctx := context.Background()
	grants, _ := e.store.Repos().Grants.List(ctx)
	if err := e.store.Repos().Grants.Delete(ctx, grants[0].ID); err != nil {
		t.Fatal(err)
	}
	e.policy.Changed(ctx)

	waitFor(t, 5*time.Second, "end message", func() bool { return ws.control("end") != nil })
	if r := ws.control("end")["reason"]; r != "grant_revoked" {
		t.Fatalf("end reason: %v", r)
	}
}

func TestDisablingUserTerminatesStreamsAndSessions(t *testing.T) {
	e := newEnv(t)
	dev := e.login(e.devID)
	admin := e.login(e.adminID)
	ws, _, err := dev.dialWS(wl + "/pods/payments-api-abc/exec")
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, 3*time.Second, "ready", func() bool { return ws.control("ready") != nil })

	start := time.Now()
	status, m, _ := admin.do("PATCH", "/api/v1/admin/users/"+itoa(e.devID), map[string]any{"disabled": true})
	if status != 200 {
		t.Fatalf("disable: %d %v", status, m)
	}
	waitFor(t, 5*time.Second, "end message", func() bool { return ws.control("end") != nil })
	if time.Since(start) > 5*time.Second {
		t.Fatalf("took %s", time.Since(start))
	}
	if r := ws.control("end")["reason"]; r != "user_disabled" {
		t.Fatalf("reason: %v", r)
	}
	if status, _, _ := dev.get("/api/v1/me"); status != 401 {
		t.Fatalf("disabled user's session still works: %d", status)
	}
}

func TestWindowWarningAndClose(t *testing.T) {
	e := newEnv(t)
	admin := e.login(e.adminID)
	dev := e.login(e.devID)
	// Exec on prod is allowed 09:00-18:00 Sat-Wed Tehran time.
	status, m, _ := admin.do("POST", "/api/v1/admin/time-windows", map[string]any{
		"name": "prod hours", "scopeKind": "environment", "scopeValue": "prod", "actions": []string{"exec"},
		"daysOfWeek": []int{6, 0, 1, 2, 3}, "startTime": "09:00", "endTime": "18:00", "timezone": "Asia/Tehran",
		"effect": "allow_inside", "enabled": true})
	if status != 201 {
		t.Fatalf("create window: %d %v", status, m)
	}
	// 17:30: inside, closes in 30 minutes: no warning yet.
	e.setTime(time.Date(2026, 10, 21, 17, 30, 0, 0, tehran))
	ws, _, err := dev.dialWS(wl + "/pods/payments-api-abc/exec")
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, 3*time.Second, "ready", func() bool { return ws.control("ready") != nil })
	if got := ws.control("ready")["closesAt"]; got == nil {
		t.Fatal("closesAt missing")
	}
	// 17:56: within 5 minutes of closing: warned.
	e.setTime(time.Date(2026, 10, 21, 17, 56, 0, 0, tehran))
	waitFor(t, 4*time.Second, "warning", func() bool { return ws.control("warning") != nil })
	if r := ws.control("warning")["reason"]; r != "outside_window" {
		t.Fatalf("warning reason: %v", r)
	}
	// 18:00: the session ends with outside_window.
	e.setTime(time.Date(2026, 10, 21, 18, 0, 1, 0, tehran))
	waitFor(t, 4*time.Second, "end", func() bool { return ws.control("end") != nil })
	if r := ws.control("end")["reason"]; r != "outside_window" {
		t.Fatalf("end reason: %v", r)
	}
	// After hours the open is refused with reopensAt = Saturday 09:00.
	denied, _, err := dev.dialWS(wl + "/pods/payments-api-abc/exec")
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, 3*time.Second, "refusal", func() bool { return denied.control("end") != nil })
	end := denied.control("end")
	dec, _ := end["decision"].(map[string]any)
	if end["reason"] != "denied" || dec["reason"] != "outside_window" || dec["reopensAt"] == nil {
		t.Fatalf("after-hours exec must be denied with reopensAt: %v", end)
	}
	// Staging is unaffected (no window there).
	stg := strings.Replace(wl, "prod-1", "stg-1", 1)
	if ws2, _, err := dev.dialWS(stg + "/pods/payments-api-abc/exec"); err != nil {
		t.Fatalf("staging exec: %v", err)
	} else {
		waitFor(t, 3*time.Second, "staging ready", func() bool { return ws2.control("ready") != nil })
	}
}

func TestBreakGlassOverridesWindowOnly(t *testing.T) {
	e := newEnv(t)
	admin := e.login(e.adminID)
	dev := e.login(e.devID)
	admin.do("POST", "/api/v1/admin/time-windows", map[string]any{
		"name": "prod hours", "scopeKind": "environment", "scopeValue": "prod", "actions": []string{"exec"},
		"daysOfWeek": []int{6, 0, 1, 2, 3}, "startTime": "09:00", "endTime": "18:00", "timezone": "Asia/Tehran",
		"effect": "allow_inside", "enabled": true})
	e.setTime(time.Date(2026, 10, 21, 20, 0, 0, 0, tehran))

	pre, _, err := dev.dialWS(wl + "/pods/payments-api-abc/exec")
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, 3*time.Second, "refusal", func() bool { return pre.control("end") != nil })
	if pre.control("end")["reason"] != "denied" {
		t.Fatalf("expected denial before break-glass: %v", pre.control("end"))
	}
	// Short reasons are rejected.
	if status, m, _ := dev.do("POST", "/api/v1/break-glass", map[string]any{"reason": "too short"}); status != 422 {
		t.Fatalf("short reason: %d %v", status, m)
	}
	status, m, _ := dev.do("POST", "/api/v1/break-glass", map[string]any{"reason": "production incident INC-1234, payments down", "durationMinutes": 30})
	if status != 201 {
		t.Fatalf("break-glass: %d %v", status, m)
	}
	bgID := int64(m["id"].(float64))
	ws, _, err := dev.dialWS(wl + "/pods/payments-api-abc/exec")
	if err != nil {
		t.Fatalf("exec under break-glass: %v", err)
	}
	waitFor(t, 3*time.Second, "ready", func() bool { return ws.control("ready") != nil })
	if ws.control("ready")["breakGlassId"] == nil {
		t.Fatal("break-glass id must be tagged on the session")
	}
	// It never grants a scope the user lacks.
	status, m, _ = dev.get("/api/v1/clusters/prod-1/namespaces/search-api/workloads")
	if status != 403 || errDetailReason(m) != "no_grant" {
		t.Fatalf("break-glass must not add scope: %d %v", status, m)
	}
	// The start and the tagged action are audited.
	evs, _ := e.store.Repos().Audit.List(context.Background(), application.AuditFilter{BreakGlassID: bgID})
	types := map[string]bool{}
	for _, ev := range evs {
		types[ev.EventType] = true
	}
	if !types[domain.EventBreakGlassStart] || !types[domain.EventExecStart] {
		t.Fatalf("break-glass audit: %v", types)
	}
	// Ending it ends the session.
	if status, _, _ := dev.do("POST", "/api/v1/break-glass/"+itoa(bgID)+"/end", nil); status != 204 {
		t.Fatalf("end break-glass: %d", status)
	}
	waitFor(t, 5*time.Second, "session ended", func() bool { return ws.control("end") != nil })
	if r := ws.control("end")["reason"]; r != "outside_window" {
		t.Fatalf("end reason: %v", r)
	}
}

func TestAdminChangesAreAuditedWithBeforeAfter(t *testing.T) {
	e := newEnv(t)
	admin := e.login(e.adminID)
	status, m, _ := admin.do("POST", "/api/v1/admin/grants", map[string]any{
		"holder":  map[string]any{"kind": "user", "id": e.otherID},
		"scope":   map[string]any{"kind": "namespace", "cluster": "prod-1", "namespace": "search-api"},
		"actions": []string{"logs"}, "note": "temp"})
	if status != 201 {
		t.Fatalf("create grant: %d %v", status, m)
	}
	id := int64(m["id"].(float64))
	status, m, _ = admin.do("PUT", "/api/v1/admin/grants/"+itoa(id), map[string]any{
		"holder":  map[string]any{"kind": "user", "id": e.otherID},
		"scope":   map[string]any{"kind": "namespace", "cluster": "prod-1", "namespace": "search-api"},
		"actions": []string{"logs", "exec"}, "note": "temp"})
	if status != 200 {
		t.Fatalf("update grant: %d %v", status, m)
	}
	evs, _ := e.store.Repos().Audit.List(context.Background(), application.AuditFilter{EventType: domain.EventAdminChange})
	var upd *domain.AuditEvent
	for i := range evs {
		if evs[i].Details["operation"] == "update" && evs[i].Details["entity"] == "grant" {
			upd = &evs[i]
		}
	}
	if upd == nil || upd.Details["before"] == nil || upd.Details["after"] == nil {
		t.Fatalf("update event lacks before/after: %+v", upd)
	}
	// The new grant takes effect immediately.
	arash := e.login(e.otherID)
	if status, _ := arash.list("/api/v1/clusters/prod-1/namespaces/search-api/workloads"); status != 200 {
		t.Fatalf("new grant not effective: %d", status)
	}
	// Protected namespaces cannot be granted.
	admin.do("POST", "/api/v1/admin/protected-namespaces", map[string]any{"namespace": "search-api"})
	status, m, _ = arash.get("/api/v1/clusters/prod-1/namespaces/search-api/workloads")
	if status != 403 || errDetailReason(m) != "protected_namespace" {
		t.Fatalf("protected: %d %v", status, m)
	}
	status, m, _ = admin.get("/api/v1/clusters/prod-1/namespaces/search-api/workloads")
	if status != 403 || errDetailReason(m) != "protected_namespace" {
		t.Fatalf("protected namespace must deny admins too: %d %v", status, m)
	}
}

func TestLastAdminCannotBeDemotedOrDisabled(t *testing.T) {
	e := newEnv(t)
	admin := e.login(e.adminID)
	id := itoa(e.adminID)
	if status, m, _ := admin.do("PATCH", "/api/v1/admin/users/"+id, map[string]any{"role": "user"}); status != 409 {
		t.Fatalf("demote last admin: %d %v", status, m)
	}
	if status, m, _ := admin.do("PATCH", "/api/v1/admin/users/"+id, map[string]any{"disabled": true}); status != 409 {
		t.Fatalf("disable last admin: %d %v", status, m)
	}
}

func TestAccessRequestApprovalCreatesGrant(t *testing.T) {
	e := newEnv(t)
	arash := e.login(e.otherID)
	admin := e.login(e.adminID)
	status, m, _ := arash.do("POST", "/api/v1/access-requests", map[string]any{
		"scope":   map[string]any{"kind": "namespace", "cluster": "prod-1", "namespace": "search-api"},
		"actions": []string{"logs"}, "durationHours": 24, "reason": "debugging search latency"})
	if status != 201 {
		t.Fatalf("request: %d %v", status, m)
	}
	id := itoa(int64(m["id"].(float64)))
	if status, _ := arash.list("/api/v1/clusters/prod-1/namespaces/search-api/workloads"); status != 403 {
		t.Fatalf("before approval: %d", status)
	}
	if status, _, _ := arash.do("POST", "/api/v1/access-requests/"+id+"/approve", nil); status != 403 {
		t.Fatalf("a user must not approve: %d", status)
	}
	status, m, _ = admin.do("POST", "/api/v1/access-requests/"+id+"/approve", map[string]any{"note": "ok"})
	if status != 200 || m["status"] != "approved" || m["resultingGrantId"] == nil {
		t.Fatalf("approve: %d %v", status, m)
	}
	if status, _ := arash.list("/api/v1/clusters/prod-1/namespaces/search-api/workloads"); status != 200 {
		t.Fatalf("after approval: %d", status)
	}
	if status, _, _ := admin.do("POST", "/api/v1/access-requests/"+id+"/approve", nil); status != 409 {
		t.Fatalf("double approve: %d", status)
	}
}

func TestProtectedClusterIsHiddenFromRequests(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	admin := e.login(e.adminID)
	dev := e.login(e.devID)
	c, err := e.store.Repos().Clusters.Get(ctx, "prod-1")
	if err != nil {
		t.Fatal(err)
	}
	c.Protected = true
	if err := e.store.Repos().Clusters.Update(ctx, c); err != nil {
		t.Fatal(err)
	}
	e.policy.Changed(ctx)

	// Pickers: gone for users, still there for admins.
	if _, l := dev.list("/api/v1/catalog/clusters"); namesOf(l, "name")["prod-1"] {
		t.Fatalf("user sees a protected cluster: %v", l)
	}
	if _, l := admin.list("/api/v1/catalog/clusters"); !namesOf(l, "name")["prod-1"] {
		t.Fatalf("admin must see a protected cluster: %v", l)
	}
	if status, _, raw := dev.get("/api/v1/catalog/clusters/prod-1/namespaces"); status != 200 || strings.TrimSpace(string(raw)) != "[]" {
		t.Fatalf("user namespaces of a protected cluster: %d %s", status, raw)
	}
	if status, _, raw := admin.get("/api/v1/catalog/clusters/prod-1/namespaces"); status != 200 || !strings.Contains(string(raw), `"payments-api"`) {
		t.Fatalf("admin namespaces of a protected cluster: %d %s", status, raw)
	}
	if status, _, _ := dev.get("/api/v1/catalog/clusters/prod-1/namespaces/payments-api/workloads"); status != 404 {
		t.Fatalf("user workloads of a protected cluster: %d", status)
	}
	// Requests are refused.
	status, m, _ := dev.do("POST", "/api/v1/access-requests", map[string]any{
		"scope":   map[string]any{"kind": "namespace", "cluster": "prod-1", "namespace": "search-api"},
		"actions": []string{"logs"}, "reason": "debugging"})
	if status != 422 {
		t.Fatalf("request on a protected cluster: %d %v", status, m)
	}
	// Grants still work.
	if status, _ := dev.list("/api/v1/clusters/prod-1/namespaces/payments-api/workloads"); status != 200 {
		t.Fatalf("grant on a protected cluster: %d", status)
	}
	if _, l := dev.list("/api/v1/namespaces"); !namesOf(l, "namespace")["payments-api"] {
		t.Fatalf("granted namespace of a protected cluster: %v", l)
	}
}

func TestStaleArgoCacheFailsClosed(t *testing.T) {
	e := newEnv(t)
	dev := e.login(e.devID)
	e.clock.Set(e.clock.Now().Add(16 * time.Minute))
	e.policy.Changed(context.Background())
	status, m, _ := dev.get("/api/v1/clusters/prod-1/namespaces/payments-api/workloads")
	if status != 403 || errDetailReason(m) != "argo_stale" {
		t.Fatalf("stale argo: %d %v", status, m)
	}
	status, me, _ := dev.get("/api/v1/me")
	if status != 200 {
		t.Fatalf("me: %d", status)
	}
	if argo := me["argo"].(map[string]any); argo["stale"] != true {
		t.Fatalf("me.argo: %v", argo)
	}
}

func TestAccessChecker(t *testing.T) {
	e := newEnv(t)
	admin := e.login(e.adminID)
	status, m, _ := admin.do("POST", "/api/v1/admin/access-check", map[string]any{
		"userId": e.devID, "cluster": "prod-1", "namespace": "payments-api", "action": "exec"})
	if status != 200 || m["allowed"] != true || m["environment"] != "prod" {
		t.Fatalf("check: %d %v", status, m)
	}
	if grants, _ := m["matchedGrants"].([]any); len(grants) != 1 {
		t.Fatalf("matched grants: %v", m["matchedGrants"])
	}
	status, m, _ = admin.do("POST", "/api/v1/admin/access-check", map[string]any{
		"userId": e.devID, "cluster": "prod-1", "namespace": "search-api", "action": "exec"})
	if status != 200 || m["allowed"] != false || m["reason"] != "no_grant" {
		t.Fatalf("check deny: %d %v", status, m)
	}
}

func TestGrantsCovering(t *testing.T) {
	e := newEnv(t)
	admin := e.login(e.adminID)
	status, l := admin.list("/api/v1/admin/grants/covering?cluster=prod-1&namespace=payments-api&kind=Deployment&name=payments-api")
	if status != 200 || len(l) != 1 || l[0]["scope"].(map[string]any)["argoProject"] != "payments" {
		t.Fatalf("covering: %d %v", status, l)
	}
	if status, l = admin.list("/api/v1/admin/grants/covering?cluster=prod-1&namespace=search-api"); status != 200 || len(l) != 0 {
		t.Fatalf("not covered: %d %v", status, l)
	}
	if status, _, _ := admin.get("/api/v1/admin/grants/covering?cluster=prod-1"); status != 422 {
		t.Fatalf("missing namespace: %d", status)
	}
	if status, _, _ := e.login(e.devID).get("/api/v1/admin/grants/covering?cluster=prod-1&namespace=payments-api"); status != 403 {
		t.Fatalf("user: %d", status)
	}
}

func TestWhoHasAccess(t *testing.T) {
	e := newEnv(t)
	dev := e.login(e.devID)
	status, l := dev.list("/api/v1/clusters/prod-1/namespaces/payments-api/access?kind=Deployment&name=payments-api")
	if status != 200 || len(l) != 1 {
		t.Fatalf("workload: %d %v", status, l)
	}
	holder := l[0]["holder"].(map[string]any)
	if holder["kind"] != "team" || holder["name"] != "Payments" || fmt.Sprint(holder["members"]) != "[Sara Moradi]" {
		t.Fatalf("holder: %v", holder)
	}
	if _, ok := l[0]["note"]; ok {
		t.Fatalf("note is shown to developers: %v", l[0])
	}
	if status, l = dev.list("/api/v1/clusters/prod-1/namespaces/payments-api/access"); status != 200 || len(l) != 1 {
		t.Fatalf("namespace: %d %v", status, l)
	}
	for _, path := range []string{"/namespaces/search-api/access", "/namespaces/search-api/access?kind=Deployment&name=search-api"} {
		if status, _, _ := dev.get("/api/v1/clusters/prod-1" + path); status != 403 {
			t.Fatalf("no grant %s: %d", path, status)
		}
	}
	if status, _, _ := e.login(e.otherID).get("/api/v1/clusters/prod-1/namespaces/payments-api/access"); status != 403 {
		t.Fatalf("other user: %d", status)
	}
	if status, _, _ := dev.get("/api/v1/clusters/prod-1/namespaces/payments-api/access?kind=Pod&name=x"); status != 422 {
		t.Fatalf("bad kind: %d", status)
	}
	if status, _, _ := e.login(e.audID).get("/api/v1/clusters/prod-1/namespaces/payments-api/access"); status != 403 {
		t.Fatalf("auditor: %d", status)
	}
}

func TestNamespaceEvents(t *testing.T) {
	e := newEnv(t)
	admin := e.login(e.adminID)
	dev := e.login(e.devID)
	const path = "/api/v1/clusters/prod-1/namespaces/payments-api/events"

	// The team's project grant reaches the namespace, so its events are listed.
	status, l := dev.list(path)
	if status != 200 || len(l) != 2 || l[0]["reason"] != "FailedCreate" {
		t.Fatalf("namespace events: %d %v", status, l)
	}
	if status, _, _ := dev.get("/api/v1/clusters/prod-1/namespaces/search-api/events"); status != 403 {
		t.Fatalf("namespace without grant: %d", status)
	}
	if status, _, _ := dev.get("/api/v1/clusters/prod-1/namespaces/Bad_NS/events"); status != 422 {
		t.Fatalf("bad namespace: %d", status)
	}

	// A grant on a single workload is not enough: the events of the other
	// workloads would show. The workload's own events stay available.
	status, m, _ := admin.do("POST", "/api/v1/admin/grants", map[string]any{
		"holder": map[string]any{"kind": "user", "id": e.otherID},
		"scope": map[string]any{"kind": "workloads", "cluster": "prod-1", "namespace": "payments-api",
			"workloads": []map[string]any{{"kind": "Deployment", "name": "payments-api"}}},
		"actions": []string{"logs"}})
	if status != 201 && status != 200 {
		t.Fatalf("create workload grant: %d %v", status, m)
	}
	arash := e.login(e.otherID)
	if status, _, _ := arash.get(path); status != 403 {
		t.Fatalf("workload grant must not list namespace events: %d", status)
	}
	if status, _ := arash.list(wl + "/events"); status != 200 {
		t.Fatalf("workload events: %d", status)
	}
	if status, _, _ := e.login(e.audID).get(path); status != 403 {
		t.Fatalf("auditor: %d", status)
	}
}

func TestNamespaceTraffic(t *testing.T) {
	e := newEnv(t)
	admin := e.login(e.adminID)
	dev := e.login(e.devID)
	const path = "/api/v1/clusters/prod-1/namespaces/payments-api/traffic"

	status, m, _ := dev.get(path)
	if status != 200 {
		t.Fatalf("traffic: %d %v", status, m)
	}
	nodes, _ := m["nodes"].([]any)
	edges, _ := m["edges"].([]any)
	if len(nodes) != 4 || len(edges) != 3 {
		t.Fatalf("traffic graph: %v", m)
	}
	if n := nodes[0].(map[string]any); n["kind"] != "Address" || n["details"] == nil {
		t.Fatalf("first node: %v", n)
	}
	if status, _, _ := dev.get("/api/v1/clusters/prod-1/namespaces/search-api/traffic"); status != 403 {
		t.Fatalf("namespace without grant: %d", status)
	}
	if status, _, _ := dev.get("/api/v1/clusters/prod-1/namespaces/Bad_NS/traffic"); status != 422 {
		t.Fatalf("bad namespace: %d", status)
	}

	// Like namespace events, a grant on a single workload is not enough.
	status, m, _ = admin.do("POST", "/api/v1/admin/grants", map[string]any{
		"holder": map[string]any{"kind": "user", "id": e.otherID},
		"scope": map[string]any{"kind": "workloads", "cluster": "prod-1", "namespace": "payments-api",
			"workloads": []map[string]any{{"kind": "Deployment", "name": "payments-api"}}},
		"actions": []string{"logs"}})
	if status != 201 && status != 200 {
		t.Fatalf("create workload grant: %d %v", status, m)
	}
	if status, _, _ := e.login(e.otherID).get(path); status != 403 {
		t.Fatalf("workload grant must not show namespace traffic: %d", status)
	}
	if status, _, _ := e.login(e.audID).get(path); status != 403 {
		t.Fatalf("auditor: %d", status)
	}
}

func TestNamespaceApps(t *testing.T) {
	e := newEnv(t)
	dev := e.login(e.devID)
	status, l := dev.list("/api/v1/clusters/prod-1/namespaces/payments-api/apps")
	if status != 200 || len(l) != 2 {
		t.Fatalf("apps: %d %v", status, l)
	}
	if a := l[0]; a["name"] != "payments-api" || a["sync"] != "Synced" || a["health"] != "Healthy" || a["appNamespace"] != "argocd" ||
		a["lastSyncAt"] != "2026-10-27T09:00:05Z" || a["lastSyncPhase"] != "Succeeded" {
		t.Fatalf("destination app: %v", a)
	}
	// deploys resources into the namespace from another destination; never finished a sync
	if a := l[1]; a["name"] != "payments-monitoring" || a["sync"] != "OutOfSync" || a["lastSyncAt"] != nil || a["lastSyncPhase"] != "Failed" {
		t.Fatalf("resource app: %v", a)
	}
	if status, l = dev.list("/api/v1/clusters/stg-1/namespaces/payments-api/apps"); status != 200 || len(l) != 1 {
		t.Fatalf("other cluster: %d %v", status, l)
	}
	if status, _, _ := dev.get("/api/v1/clusters/prod-1/namespaces/search-api/apps"); status != 403 {
		t.Fatalf("no grant: %d", status)
	}
	if status, _, _ := e.login(e.audID).get("/api/v1/clusters/prod-1/namespaces/payments-api/apps"); status != 403 {
		t.Fatalf("auditor: %d", status)
	}
	_, me, _ := dev.get("/api/v1/me")
	if u := me["settings"].(map[string]any)["argoUrl"]; u != "https://argo.example.com" {
		t.Fatalf("argoUrl: %v", u)
	}
}

func TestSyncApp(t *testing.T) {
	e := newEnv(t)
	admin := e.login(e.adminID)
	dev := e.login(e.devID)
	const apps = "/api/v1/clusters/prod-1/namespaces/payments-api/apps"
	const syncPath = apps + "/argocd/payments-api/sync"
	syncAccess := func(app map[string]any) map[string]any {
		return app["access"].(map[string]any)["sync"].(map[string]any)
	}
	grant := func(scope map[string]any) (int, map[string]any) {
		status, m, _ := admin.do("POST", "/api/v1/admin/grants", map[string]any{
			"holder": map[string]any{"kind": "team", "id": e.teamID}, "scope": scope, "actions": []string{"sync"}})
		return status, m
	}

	// The team's project grant does not include sync.
	_, l := dev.list(apps)
	if a := syncAccess(l[0]); a["allowed"] != false || a["reason"] != "no_grant" {
		t.Fatalf("sync access without grant: %v", a)
	}
	if status, m, _ := dev.do("POST", syncPath, nil); status != 403 || errCode(m) != "denied" {
		t.Fatalf("sync without grant: %d %v", status, m)
	}
	// Sync is for whole applications, so a workloads scope cannot hold it.
	if status, m := grant(map[string]any{"kind": "workloads", "cluster": "prod-1", "namespace": "payments-api",
		"workloads": []map[string]any{{"kind": "Deployment", "name": "payments-api"}}}); status != 422 {
		t.Fatalf("sync on workloads scope: %d %v", status, m)
	}
	if status, m := grant(map[string]any{"kind": "namespace", "cluster": "prod-1", "namespace": "payments-api"}); status != 201 && status != 200 {
		t.Fatalf("create sync grant: %d %v", status, m)
	}

	_, l = dev.list(apps)
	if a := syncAccess(l[0]); a["allowed"] != true {
		t.Fatalf("payments-api sync access: %v", a)
	}
	// payments-monitoring deploys to monitoring too, which the grant does not cover.
	if a := syncAccess(l[1]); a["allowed"] != false || a["reason"] != "no_grant" {
		t.Fatalf("payments-monitoring sync access: %v", a)
	}

	status, m, _ := dev.do("POST", syncPath, nil)
	if status != 202 || m["operationId"] == nil {
		t.Fatalf("sync: %d %v", status, m)
	}
	waitFor(t, 5*time.Second, "sync outcome audited", func() bool {
		evs, _ := e.store.Repos().Audit.List(context.Background(), application.AuditFilter{EventType: domain.EventArgoSync})
		for _, ev := range evs {
			if ev.Result == domain.ResultSucceeded && ev.Namespace == "payments-api" && ev.Action == "sync" {
				return true
			}
		}
		return false
	})
	if calls := e.argoAPI.syncCalls(); len(calls) != 1 || calls[0] != "argocd/payments-api by sara" {
		t.Fatalf("argo syncs: %v", calls)
	}
	status, _, raw := dev.get("/api/v1/operations/" + m["operationId"].(string) + "/stream")
	if status != 200 || !strings.Contains(string(raw), "event:done") || !strings.Contains(string(raw), "succeeded") {
		t.Fatalf("operation stream: %d %s", status, raw)
	}

	// Denied for the namespace the user lacks, and never sent to Argo CD.
	if status, m, _ := dev.do("POST", apps+"/argocd/payments-monitoring/sync", nil); status != 403 || errCode(m) != "denied" {
		t.Fatalf("sync of an app reaching another namespace: %d %v", status, m)
	}
	evs, _ := e.store.Repos().Audit.List(context.Background(), application.AuditFilter{EventType: domain.EventDecisionDenied})
	found := false
	for _, ev := range evs {
		if ev.Action == "sync" && ev.Namespace == "monitoring" && ev.Details["app"] == "payments-monitoring" {
			found = true
		}
	}
	if !found || len(e.argoAPI.syncCalls()) != 1 {
		t.Fatalf("denial audit for monitoring: %v, syncs %v", evs, e.argoAPI.syncCalls())
	}

	// Cluster-scoped resources other than its own Namespace leave the sync to
	// admins, and the list names them.
	setClusterResources := func(r ...domain.ClusterResource) {
		for i, a := range e.argoAPI.apps {
			if a.Name == "payments-api" && a.Cluster == "prod-1" {
				e.argoAPI.apps[i].ClusterResources = r
			}
		}
		if err := e.argo.Refresh(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	clusterRole := domain.ClusterResource{Group: "rbac.authorization.k8s.io", Kind: "ClusterRole", Name: "payments-read"}
	clusterInput := domain.ClusterResource{Group: "fluentbit.fluent.io", Kind: "ClusterInput", Name: "payments-tail"}
	setClusterResources(domain.ClusterResource{Kind: "Namespace", Name: "payments-api"}, clusterRole)
	_, l = dev.list(apps)
	if a := syncAccess(l[0]); a["allowed"] != false || a["reason"] != "app_scope" ||
		fmt.Sprint(l[0]["adminOnlyResources"]) != "[ClusterRole.rbac.authorization.k8s.io/payments-read]" {
		t.Fatalf("app_scope: %v", l[0])
	}
	if status, m, _ := dev.do("POST", syncPath, nil); status != 403 || errCode(m) != "denied" {
		t.Fatalf("sync of a cluster-scoped app: %d %v", status, m)
	}
	_, l = admin.list(apps)
	if a := syncAccess(l[0]); a["allowed"] != true || fmt.Sprint(l[0]["adminOnlyResources"]) != "[]" {
		t.Fatalf("admin app_scope: %v", l[0])
	}

	// Kinds the settings allow do not need an admin; others still do.
	putKinds := func(kinds any) (int, map[string]any) {
		_, cur, _ := admin.get("/api/v1/admin/settings")
		if kinds == nil {
			delete(cur, "argoSyncClusterKinds")
		} else {
			cur["argoSyncClusterKinds"] = kinds
		}
		status, m, _ := admin.do("PUT", "/api/v1/admin/settings", cur)
		return status, m
	}
	if status, m := putKinds([]string{"ClusterRole.rbac.authorization.k8s.io"}); status != 422 || errCode(m) != "invalid" {
		t.Fatalf("never-allowed kind: %d %v", status, m)
	}
	if status, m := putKinds([]string{"ClusterInput.fluentbit.fluent.io"}); status != 200 {
		t.Fatalf("allow kind: %d %v", status, m)
	}
	// A client that does not know the setting keeps it.
	if status, m := putKinds(nil); status != 200 || fmt.Sprint(m["argoSyncClusterKinds"]) != "[ClusterInput.fluentbit.fluent.io]" {
		t.Fatalf("settings without the field: %d %v", status, m)
	}
	setClusterResources(clusterInput, clusterRole)
	if _, l = dev.list(apps); fmt.Sprint(l[0]["adminOnlyResources"]) != "[ClusterRole.rbac.authorization.k8s.io/payments-read]" {
		t.Fatalf("only the other kind is admin-only: %v", l[0])
	}
	setClusterResources(domain.ClusterResource{Kind: "Namespace", Name: "payments-api"}, clusterInput)
	if _, l = dev.list(apps); syncAccess(l[0])["allowed"] != true {
		t.Fatalf("allowed kind: %v", l[0])
	}
	if status, m, _ := dev.do("POST", syncPath, nil); status != 202 {
		t.Fatalf("sync with an allowed kind: %d %v", status, m)
	}
	evs, _ = e.store.Repos().Audit.List(context.Background(), application.AuditFilter{EventType: domain.EventArgoSync})
	found = false
	for _, ev := range evs {
		if fmt.Sprint(ev.Details["clusterResources"]) == "[ClusterInput.fluentbit.fluent.io/payments-tail]" {
			found = true
		}
	}
	if !found {
		t.Fatalf("sync audit names the cluster resources: %v", evs)
	}
	setClusterResources()
	waitFor(t, 5*time.Second, "allowed-kind sync finished", func() bool {
		evs, _ := e.store.Repos().Audit.List(context.Background(), application.AuditFilter{EventType: domain.EventArgoSync})
		n := 0
		for _, ev := range evs {
			if ev.Result == domain.ResultSucceeded {
				n++
			}
		}
		return n >= 2
	})

	e.argoAPI.mu.Lock()
	e.argoAPI.busy = true
	e.argoAPI.mu.Unlock()
	if status, m, _ := dev.do("POST", syncPath, nil); status != 409 || errCode(m) != "already_running" {
		t.Fatalf("sync while busy: %d %v", status, m)
	}
	if status, _, _ := dev.do("POST", apps+"/argocd/unknown/sync", nil); status != 404 {
		t.Fatalf("unknown app: %d", status)
	}
	if status, _, _ := e.login(e.audID).do("POST", syncPath, nil); status != 403 {
		t.Fatalf("auditor: %d", status)
	}
}

func TestAppDetails(t *testing.T) {
	e := newEnv(t)
	admin := e.login(e.adminID)
	dev := e.login(e.devID)
	const app = "/api/v1/clusters/prod-1/namespaces/payments-api/apps/argocd/payments-api"

	// Status and history: anyone who sees the namespace's apps.
	status, m, _ := dev.get(app + "/status")
	if status != 200 || m["syncRevision"] != "abc123" || len(m["conditions"].([]any)) != 1 {
		t.Fatalf("status: %d %v", status, m)
	}
	op := m["lastOperation"].(map[string]any)
	if r := op["resources"].([]any)[0].(map[string]any); op["phase"] != "Failed" || r["message"] != "field is immutable" {
		t.Fatalf("last operation: %v", op)
	}
	status, h := dev.list(app + "/history")
	if status != 200 || len(h) != 3 || h[0]["revision"] != "abc123" || h[0]["current"] != true || h[1]["current"] != false {
		t.Fatalf("history: %d %v", status, h)
	}
	if c, _ := h[0]["commit"].(map[string]any); c["message"] != "Bump replicas" || h[1]["commit"] != nil {
		t.Fatalf("commits: %v / %v", h[0]["commit"], h[1]["commit"])
	}
	if status, _, _ := dev.get("/api/v1/clusters/prod-1/namespaces/search-api/apps/argocd/search-api/status"); status != 403 {
		t.Fatalf("status without grant: %d", status)
	}
	if status, _, _ := dev.get("/api/v1/clusters/prod-1/namespaces/payments-api/apps/argocd/unknown/status"); status != 404 {
		t.Fatalf("unknown app: %d", status)
	}

	// The diff shows manifests: it needs sync on the app, like the sync itself.
	if status, m, _ := dev.get(app + "/diff"); status != 403 || errCode(m) != "denied" {
		t.Fatalf("diff without sync: %d %v", status, m)
	}
	status, m, _ = admin.do("POST", "/api/v1/admin/grants", map[string]any{
		"holder": map[string]any{"kind": "team", "id": e.teamID},
		"scope":  map[string]any{"kind": "namespace", "cluster": "prod-1", "namespace": "payments-api"}, "actions": []string{"sync"}})
	if status != 201 && status != 200 {
		t.Fatalf("create sync grant: %d %v", status, m)
	}
	status, m, _ = dev.get(app + "/diff")
	if status != 200 {
		t.Fatalf("diff: %d %v", status, m)
	}
	res := m["resources"].([]any)
	dep, sec := res[0].(map[string]any), res[1].(map[string]any)
	if dep["kind"] != "Deployment" || dep["state"] != "changed" || !strings.Contains(dep["diff"].(string), "+  replicas: 3") {
		t.Fatalf("deployment diff: %v", dep)
	}
	if sec["kind"] != "Secret" || sec["hidden"] != true || sec["diff"] != "" {
		t.Fatalf("secret must be hidden: %v", sec)
	}
	evs, _ := e.store.Repos().Audit.List(context.Background(), application.AuditFilter{EventType: domain.EventArgoDiff})
	if len(evs) != 1 || evs[0].Username != "sara" || evs[0].Details["app"] != "payments-api" {
		t.Fatalf("diff audit: %v", evs)
	}
}

func TestRollbackApp(t *testing.T) {
	e := newEnv(t)
	admin := e.login(e.adminID)
	dev := e.login(e.devID)
	const apps = "/api/v1/clusters/prod-1/namespaces/payments-api/apps"
	const app = apps + "/argocd/payments-api"
	const rollback = app + "/history/1/rollback"
	grant := func(scope map[string]any, actions ...string) {
		t.Helper()
		status, m, _ := admin.do("POST", "/api/v1/admin/grants", map[string]any{
			"holder": map[string]any{"kind": "team", "id": e.teamID}, "scope": scope, "actions": actions})
		if status != 201 && status != 200 {
			t.Fatalf("grant %v: %d %v", actions, status, m)
		}
	}
	rollbackAccess := func() map[string]any {
		_, l := dev.list(apps)
		return l[0]["access"].(map[string]any)["rollback"].(map[string]any)
	}
	payments := map[string]any{"kind": "namespace", "cluster": "prod-1", "namespace": "payments-api"}

	// Sync does not include rollback.
	grant(payments, "sync")
	if a := rollbackAccess(); a["allowed"] != false || a["reason"] != "no_grant" {
		t.Fatalf("rollback access with only sync: %v", a)
	}
	if status, m, _ := dev.do("POST", rollback, nil); status != 403 || errCode(m) != "denied" {
		t.Fatalf("rollback without grant: %d %v", status, m)
	}
	// Like sync, rollback is for whole applications.
	status, m, _ := admin.do("POST", "/api/v1/admin/grants", map[string]any{
		"holder": map[string]any{"kind": "team", "id": e.teamID}, "actions": []string{"rollback"},
		"scope": map[string]any{"kind": "workloads", "cluster": "prod-1", "namespace": "payments-api",
			"workloads": []map[string]any{{"kind": "Deployment", "name": "payments-api"}}}})
	if status != 422 {
		t.Fatalf("rollback on workloads scope: %d %v", status, m)
	}
	grant(payments, "rollback")
	if a := rollbackAccess(); a["allowed"] != true {
		t.Fatalf("rollback access: %v", a)
	}

	// The history says which deploys can be rolled back to.
	_, h := dev.list(app + "/history")
	if h[0]["rollbackBlocked"] != "current" || h[1]["rollbackBlocked"] != nil || h[2]["rollbackBlocked"] != "source_changed" {
		t.Fatalf("rollbackBlocked: %v", h)
	}

	// The plan names what the revision adds and what it leaves in place; the
	// Deployment without a namespace lands in the destination.
	status, m, _ = dev.get(rollback)
	if status != 200 || m["revision"] != "old111" || m["decision"].(map[string]any)["allowed"] != true {
		t.Fatalf("plan: %d %v", status, m)
	}
	if fmt.Sprint(m["added"]) != "[map[group: kind:ConfigMap name:old-config namespace:payments-api]]" ||
		fmt.Sprint(m["kept"]) != "[map[group: kind:Service name:payments-api namespace:payments-api]]" {
		t.Fatalf("plan resources: %v / %v", m["added"], m["kept"])
	}
	for _, path := range []string{app + "/history/2/rollback", app + "/history/0/rollback", app + "/history/9/rollback"} {
		if status, m, _ := dev.do("POST", path, nil); status != 422 || errCode(m) != "invalid" {
			t.Fatalf("%s: %d %v", path, status, m)
		}
	}

	// A revision that deploys into another namespace needs rollback there too,
	// and one with a cluster-scoped resource (or a kind the cluster does not
	// serve) needs an admin.
	setManifests := func(objs ...application.ArgoResourceRef) {
		e.argoAPI.mu.Lock()
		e.argoAPI.manifests = map[string][]application.ArgoResourceRef{"old111": objs}
		e.argoAPI.mu.Unlock()
	}
	deployment := application.ArgoResourceRef{Group: "apps", Kind: "Deployment", Name: "payments-api"}
	setManifests(deployment, application.ArgoResourceRef{Kind: "ConfigMap", Namespace: "monitoring", Name: "scrape"})
	if status, m, _ := dev.get(rollback); status != 200 || m["decision"].(map[string]any)["allowed"] != false || m["decisionNamespace"] != "monitoring" {
		t.Fatalf("plan into monitoring: %d %v", status, m)
	}
	if status, m, _ := dev.do("POST", rollback, nil); status != 403 || errCode(m) != "denied" {
		t.Fatalf("rollback into monitoring: %d %v", status, m)
	}
	setManifests(deployment, application.ArgoResourceRef{Group: "example.io", Kind: "Widget", Name: "w"})
	if status, m, _ := dev.get(rollback); status != 200 || m["decision"].(map[string]any)["reason"] != "app_scope" ||
		fmt.Sprint(m["adminOnlyResources"]) != "[Widget.example.io/w]" {
		t.Fatalf("plan with an unknown kind: %d %v", status, m)
	}
	setManifests(deployment, application.ArgoResourceRef{Group: "rbac.authorization.k8s.io", Kind: "ClusterRole", Name: "payments-read"})
	if status, m, _ := dev.do("POST", rollback, nil); status != 403 || errCode(m) != "denied" {
		t.Fatalf("rollback with a ClusterRole: %d %v", status, m)
	}
	if len(e.argoAPI.rollbackCalls()) != 0 {
		t.Fatalf("denied rollbacks reached Argo CD: %v", e.argoAPI.rollbackCalls())
	}
	setManifests(deployment)

	status, m, _ = dev.do("POST", rollback, nil)
	if status != 202 || m["operationId"] == nil {
		t.Fatalf("rollback: %d %v", status, m)
	}
	waitFor(t, 5*time.Second, "rollback outcome audited", func() bool {
		evs, _ := e.store.Repos().Audit.List(context.Background(), application.AuditFilter{EventType: domain.EventArgoRollback})
		for _, ev := range evs {
			if ev.Result == domain.ResultSucceeded && ev.Action == "rollback" && ev.Details["operationStartedAt"] == "2026-10-30T09:00:01Z" {
				return true
			}
		}
		return false
	})
	if calls := e.argoAPI.rollbackCalls(); len(calls) != 1 || calls[0] != "argocd/payments-api to 1" {
		t.Fatalf("argo rollbacks: %v", calls)
	}
	evs, _ := e.store.Repos().Audit.List(context.Background(), application.AuditFilter{EventType: domain.EventArgoRollback, Result: domain.ResultAllowed})
	if len(evs) != 1 || evs[0].Username != "sara" || evs[0].Details["revision"] != "old111" || evs[0].Details["historyId"] != float64(1) {
		t.Fatalf("rollback started audit: %v", evs)
	}
	status, _, raw := dev.get("/api/v1/operations/" + m["operationId"].(string) + "/stream")
	if status != 200 || !strings.Contains(string(raw), "event:done") || !strings.Contains(string(raw), "succeeded") {
		t.Fatalf("operation stream: %d %s", status, raw)
	}
	// The deploy it made names the portal user, not the token's account.
	_, h = dev.list(app + "/history")
	if h[0]["portalUser"] != "sara" || h[0]["portalAction"] != "argo.rollback" || h[1]["portalUser"] != nil {
		t.Fatalf("history users: %v", h)
	}

	// Apps that sync automatically cannot be rolled back.
	e.argoAPI.mu.Lock()
	e.argoAPI.autoSync = true
	e.argoAPI.mu.Unlock()
	if status, m, _ := dev.do("POST", rollback, nil); status != 409 || errCode(m) != "auto_sync_enabled" {
		t.Fatalf("rollback with auto-sync: %d %v", status, m)
	}
	if _, h = dev.list(app + "/history"); h[1]["rollbackBlocked"] != "auto_sync" {
		t.Fatalf("rollbackBlocked with auto-sync: %v", h[1])
	}
	if status, _, _ := e.login(e.audID).do("POST", rollback, nil); status != 403 {
		t.Fatalf("auditor: %d", status)
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

func dialer() *websocket.Dialer { return websocket.DefaultDialer }

func namesOf(l []map[string]any, key string) map[string]bool {
	out := map[string]bool{}
	for _, m := range l {
		if s, ok := m[key].(string); ok {
			out[s] = true
		}
	}
	return out
}

func TestNamespacesAreReadFromTheCluster(t *testing.T) {
	e := newEnv(t)
	admin := e.login(e.adminID)
	dev := e.login(e.devID)

	// Pickers: only the namespaces read from the cluster, never Argo's
	// ("monitoring" is only an Argo destination).
	status, _, raw := admin.get("/api/v1/catalog/clusters/prod-1/namespaces")
	if status != 200 || !strings.Contains(string(raw), `"live-only"`) || !strings.Contains(string(raw), `"payments-api"`) ||
		strings.Contains(string(raw), `"monitoring"`) {
		t.Fatalf("catalog: %d %s", status, raw)
	}
	// A cluster call that fails lists nothing.
	if status, _, raw := admin.get("/api/v1/catalog/clusters/broken/namespaces"); status != 200 || strings.TrimSpace(string(raw)) != "[]" {
		t.Fatalf("broken cluster: %d %s", status, raw)
	}

	// "By namespace": an admin sees every live namespace, a developer only
	// the ones their grants cover.
	_, adminNS := admin.list("/api/v1/namespaces")
	if !namesOf(adminNS, "namespace")["live-only"] || !namesOf(adminNS, "namespace")["billing"] {
		t.Fatalf("admin namespaces: %v", adminNS)
	}
	// Argo only annotates them: nothing from clusters the portal does not know
	// or namespaces no cluster reported.
	for _, n := range adminNS {
		if n["cluster"] == "argo-only" || n["namespace"] == "monitoring" {
			t.Fatalf("namespace from Argo only: %v", n)
		}
		if n["cluster"] == "prod-1" && n["namespace"] == "payments-api" && fmt.Sprint(n["apps"]) != "[payments-api payments-monitoring]" {
			t.Fatalf("apps of payments-api: %v", n["apps"])
		}
	}
	if _, apps := admin.list("/api/v1/apps"); namesOf(apps, "cluster")["argo-only"] {
		t.Fatalf("app on a cluster the portal does not know: %v", apps)
	}
	_, devNS := dev.list("/api/v1/namespaces")
	got := namesOf(devNS, "namespace")
	if got["live-only"] || got["billing"] || !got["payments-api"] {
		t.Fatalf("developer namespaces: %v", got)
	}

	// Protected namespaces disappear everywhere (the cache is per cluster, so
	// change the list and look at a fresh answer from the picker).
	if status, _, _ := admin.do("POST", "/api/v1/admin/protected-namespaces", map[string]any{"namespace": "billing"}); status != 204 {
		t.Fatalf("protect: %d", status)
	}
	_, raw2 := admin.list("/api/v1/namespaces")
	if namesOf(raw2, "namespace")["billing"] {
		t.Fatalf("protected namespace still listed: %v", raw2)
	}
	status, _, raw = admin.get("/api/v1/catalog/clusters/prod-1/namespaces")
	if status != 200 || strings.Contains(string(raw), `"billing"`) {
		t.Fatalf("protected namespace in picker: %s", raw)
	}
}

func TestBreakGlassCanBeTurnedOffAndLogsStayOpen(t *testing.T) {
	e := newEnv(t)
	admin := e.login(e.adminID)
	dev := e.login(e.devID)
	// Prod is open for exec and logs only 09:00-18:00 Sat-Wed. Logs are listed on purpose:
	// the rule must not close them.
	admin.do("POST", "/api/v1/admin/time-windows", map[string]any{
		"name": "prod hours", "scopeKind": "environment", "scopeValue": "prod", "actions": []string{"exec", "logs"},
		"daysOfWeek": []int{6, 0, 1, 2, 3}, "startTime": "09:00", "endTime": "18:00", "timezone": "Asia/Tehran",
		"effect": "allow_inside", "enabled": true})
	e.setTime(time.Date(2026, 10, 21, 20, 0, 0, 0, tehran))
	logs := wl + "/pods/payments-api-abc/logs/download?container=app"

	setBreakGlass := func(on bool) {
		t.Helper()
		mode := "off"
		if on {
			mode = "self"
		}
		setBreakGlassMode(t, admin, mode, nil)
	}
	denied := func(path string) bool {
		ws, _, err := dev.dialWS(path)
		if err != nil {
			t.Fatal(err)
		}
		waitFor(t, 3*time.Second, "refusal or ready", func() bool { return ws.control("end") != nil || ws.control("ready") != nil })
		return ws.control("end") != nil
	}

	// Logs are open outside the window with break-glass on...
	if status, _, _ := dev.get(logs); status != 200 {
		t.Fatalf("logs outside the window: %d", status)
	}
	// ...and exec is closed.
	if !denied(wl + "/pods/payments-api-abc/exec") {
		t.Fatal("exec must be closed outside the window")
	}

	// With break-glass on, a user can start one and exec works.
	status, m, _ := dev.do("POST", "/api/v1/break-glass", map[string]any{"reason": "production incident INC-9, payments down", "durationMinutes": 30})
	if status != 201 {
		t.Fatalf("start: %d %v", status, m)
	}
	ws, _, err := dev.dialWS(wl + "/pods/payments-api-abc/exec")
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, 3*time.Second, "ready under break-glass", func() bool { return ws.control("ready") != nil })

	// Turning the feature off ends the session that only break-glass allowed.
	setBreakGlass(false)
	waitFor(t, 6*time.Second, "session ended", func() bool { return ws.control("end") != nil })
	if r := ws.control("end")["reason"]; r != "outside_window" {
		t.Fatalf("end reason: %v", r)
	}
	// Nobody can start a new one, and the UI is told.
	if status, m, _ := dev.do("POST", "/api/v1/break-glass", map[string]any{"reason": "production incident INC-10, again", "durationMinutes": 30}); status != 403 || errCode(m) != "break_glass_disabled" {
		t.Fatalf("start while off: %d %v", status, m)
	}
	_, me, _ := dev.get("/api/v1/me")
	if s, _ := me["settings"].(map[string]any); s["breakGlassMode"] != "off" || me["breakGlass"] != nil {
		t.Fatalf("me while off: %v / %v", me["settings"], me["breakGlass"])
	}
	// Exec is closed outside the window, logs still open.
	if !denied(wl + "/pods/payments-api-abc/exec") {
		t.Fatal("exec must stay closed outside the window while break-glass is off")
	}
	if status, _, _ := dev.get(logs); status != 200 {
		t.Fatalf("logs while off: %d", status)
	}

	// Turning it back on makes break-glass usable again (the old one had been overridden meanwhile
	// and is still within its time, so exec opens straight away).
	setBreakGlass(true)
	waitFor(t, 6*time.Second, "exec open again", func() bool {
		ws2, _, err := dev.dialWS(wl + "/pods/payments-api-abc/exec")
		if err != nil {
			return false
		}
		time.Sleep(300 * time.Millisecond)
		return ws2.control("ready") != nil
	})
}

func TestScheduledBreakGlass(t *testing.T) {
	e := newEnv(t)
	admin := e.login(e.adminID)
	dev := e.login(e.devID)
	admin.do("POST", "/api/v1/admin/time-windows", map[string]any{
		"name": "prod hours", "scopeKind": "environment", "scopeValue": "prod", "actions": []string{"exec"},
		"daysOfWeek": []int{6, 0, 1, 2, 3}, "startTime": "09:00", "endTime": "18:00", "timezone": "Asia/Tehran",
		"effect": "allow_inside", "enabled": true})
	// A namespace grant, so closes_at is not cut short by the Argo cache
	// being stale at future instants (project grants).
	if status, m, _ := admin.do("POST", "/api/v1/admin/grants", map[string]any{
		"holder": map[string]any{"kind": "user", "id": e.devID},
		"scope":  map[string]any{"kind": "namespace", "cluster": "prod-1", "namespace": "payments-api"}, "actions": []string{"exec"}}); status != 201 && status != 200 {
		t.Fatalf("grant: %d %v", status, m)
	}
	now := time.Date(2026, 10, 21, 17, 30, 0, 0, tehran)
	e.setTime(now)
	exec := wl + "/pods/payments-api-abc/exec"
	reason := "planned migration CHG-77, needs exec after hours"
	start := func(c *client, startsAt time.Time) (int, map[string]any) {
		status, m, _ := c.do("POST", "/api/v1/break-glass", map[string]any{"reason": reason, "durationMinutes": 60, "startsAt": startsAt.Format(time.RFC3339)})
		return status, m
	}
	ready := func() map[string]any {
		ws, _, err := dev.dialWS(exec)
		if err != nil {
			t.Fatal(err)
		}
		waitFor(t, 3*time.Second, "refusal or ready", func() bool { return ws.control("end") != nil || ws.control("ready") != nil })
		_ = ws.conn.Close()
		return ws.control("ready")
	}
	at := func(m map[string]any, key string) time.Time {
		got, err := time.Parse(time.RFC3339, fmt.Sprint(m[key]))
		if err != nil {
			t.Fatalf("%s: %v", key, m)
		}
		return got
	}

	// The start must lie ahead, within seven days.
	if status, m := start(dev, now.Add(-time.Hour)); status != 422 {
		t.Fatalf("start in the past: %d %v", status, m)
	}
	if status, m := start(dev, now.Add(8*24*time.Hour)); status != 422 {
		t.Fatalf("start beyond seven days: %d %v", status, m)
	}
	// Scheduled from 18:00, when the window closes: exec now stays open until 19:00.
	six := time.Date(2026, 10, 21, 18, 0, 0, 0, tehran)
	status, bg := start(dev, six)
	if status != 201 || !at(bg, "startedAt").Equal(six) || !at(bg, "expiresAt").Equal(six.Add(time.Hour)) {
		t.Fatalf("schedule: %d %v", status, bg)
	}
	waitFor(t, 6*time.Second, "policy with the scheduled break-glass", func() bool {
		r := ready()
		return r != nil && r["closesAt"] != nil && at(r, "closesAt").Equal(six.Add(time.Hour))
	})
	// One at a time.
	if status, m := start(dev, six.Add(2*time.Hour)); status != 409 {
		t.Fatalf("second break-glass: %d %v", status, m)
	}
	// After the window closes it overrides it, tagged.
	e.setTime(six.Add(10 * time.Minute))
	waitFor(t, 6*time.Second, "exec under the scheduled break-glass", func() bool {
		r := ready()
		return r != nil && r["breakGlassId"] == bg["id"]
	})
	if status, _, _ := dev.do("POST", "/api/v1/break-glass/"+fmt.Sprint(bg["id"])+"/end", nil); status != 204 {
		t.Fatalf("end: %d", status)
	}

	// A later one is cancelled before its start and never applies.
	e.setTime(six.Add(20 * time.Minute))
	nine := time.Date(2026, 10, 21, 21, 0, 0, 0, tehran)
	status, bg = start(dev, nine)
	if status != 201 {
		t.Fatalf("schedule 21:00: %d %v", status, bg)
	}
	if status, _, _ := dev.do("POST", "/api/v1/break-glass/"+fmt.Sprint(bg["id"])+"/end", nil); status != 204 {
		t.Fatalf("cancel before start: %d", status)
	}
	_, page, _ := admin.get(fmt.Sprintf("/api/v1/audit?breakGlassId=%v", bg["id"]))
	items, _ := page["items"].([]any)
	types := map[string]map[string]any{}
	for _, it := range items {
		ev := it.(map[string]any)
		types[ev["eventType"].(string)], _ = ev["details"].(map[string]any)
	}
	if d := types[domain.EventBreakGlassStart]; d == nil || !at(d, "startsAt").Equal(nine) {
		t.Fatalf("break_glass.start: %v", types)
	}
	if d := types[domain.EventBreakGlassEnd]; d == nil || d["beforeStart"] != true {
		t.Fatalf("break_glass.end: %v", types)
	}
	e.setTime(nine.Add(5 * time.Minute))
	if ready() != nil {
		t.Fatal("a cancelled scheduled break-glass must not open exec")
	}

	// Approval mode: the request names the start, expires counting from it,
	// and the approved break-glass starts then.
	setBreakGlassMode(t, admin, "approval", map[string]any{"breakGlassRequestMaxAgeMinutes": 30})
	ten := nine.Add(time.Hour)
	status, req, _ := dev.do("POST", "/api/v1/break-glass/requests", map[string]any{"reason": reason, "durationMinutes": 45, "startsAt": ten.Format(time.RFC3339)})
	if status != 201 || !at(req, "startsAt").Equal(ten) || !at(req, "expiresAt").Equal(ten.Add(30*time.Minute)) {
		t.Fatalf("scheduled request: %d %v", status, req)
	}
	e.setTime(nine.Add(40 * time.Minute)) // past created + 30 min, still pending
	status, m, _ := admin.do("POST", fmt.Sprintf("/api/v1/break-glass/requests/%v/approve", req["id"]), nil)
	if status != 200 || m["status"] != "approved" {
		t.Fatalf("approve: %d %v", status, m)
	}
	got, err := e.store.Repos().BreakGlass.Get(context.Background(), int64(m["breakGlassId"].(float64)))
	if err != nil || !got.StartedAt.Equal(ten) || !got.ExpiresAt.Equal(ten.Add(45*time.Minute)) {
		t.Fatalf("approved break-glass: %+v %v", got, err)
	}
	if ready() != nil {
		t.Fatal("exec open before the approved start")
	}
	e.setTime(ten.Add(time.Minute))
	waitFor(t, 6*time.Second, "exec under the approved scheduled break-glass", func() bool { return ready() != nil })
}

func setBreakGlassMode(t *testing.T, admin *client, mode string, more map[string]any) {
	t.Helper()
	_, cur, _ := admin.get("/api/v1/admin/settings")
	cur["breakGlassMode"] = mode
	for k, v := range more {
		cur[k] = v
	}
	if status, m, _ := admin.do("PUT", "/api/v1/admin/settings", cur); status != 200 || m["breakGlassMode"] != mode {
		t.Fatalf("set breakGlassMode=%s: %d %v", mode, status, m)
	}
}

func TestBreakGlassNeedsApproval(t *testing.T) {
	e := newEnv(t)
	admin := e.login(e.adminID)
	dev := e.login(e.devID)
	other := e.login(e.otherID)
	admin.do("POST", "/api/v1/admin/time-windows", map[string]any{
		"name": "prod hours", "scopeKind": "environment", "scopeValue": "prod", "actions": []string{"exec"},
		"daysOfWeek": []int{6, 0, 1, 2, 3}, "startTime": "09:00", "endTime": "18:00", "timezone": "Asia/Tehran",
		"effect": "allow_inside", "enabled": true})
	start := time.Date(2026, 10, 21, 20, 0, 0, 0, tehran)
	e.setTime(start)
	setBreakGlassMode(t, admin, "approval", map[string]any{"breakGlassRequestMaxAgeMinutes": 30})
	exec := wl + "/pods/payments-api-abc/exec"
	execReady := func() bool {
		ws, _, err := dev.dialWS(exec)
		if err != nil {
			t.Fatal(err)
		}
		waitFor(t, 3*time.Second, "refusal or ready", func() bool { return ws.control("end") != nil || ws.control("ready") != nil })
		return ws.control("ready") != nil
	}
	request := func(c *client) (int, map[string]any) {
		status, m, _ := c.do("POST", "/api/v1/break-glass/requests", map[string]any{"reason": "production incident INC-9, payments down", "durationMinutes": 30})
		return status, m
	}
	decide := func(id any, verb string, body map[string]any) (int, map[string]any) {
		status, m, _ := admin.do("POST", fmt.Sprintf("/api/v1/break-glass/requests/%v/%s", id, verb), body)
		return status, m
	}
	pendingID := func() any {
		_, me, _ := dev.get("/api/v1/me")
		r, _ := me["breakGlassRequest"].(map[string]any)
		if r == nil {
			return nil
		}
		return r["id"]
	}

	// Starting one directly is refused; it must be requested.
	if status, m, _ := dev.do("POST", "/api/v1/break-glass", map[string]any{"reason": "production incident INC-9, payments down"}); status != 409 || errCode(m) != "break_glass_approval_required" {
		t.Fatalf("start in approval mode: %d %v", status, m)
	}
	if status, m, _ := dev.do("POST", "/api/v1/break-glass/requests", map[string]any{"reason": "too short"}); status != 422 {
		t.Fatalf("short reason: %d %v", status, m)
	}
	status, req := request(dev)
	if status != 201 || req["status"] != "pending" || req["durationMinutes"] != float64(30) {
		t.Fatalf("request: %d %v", status, req)
	}
	if status, m := request(dev); status != 409 {
		t.Fatalf("second pending request: %d %v", status, m)
	}
	if id := pendingID(); id != req["id"] {
		t.Fatalf("me.breakGlassRequest: %v", id)
	}
	if execReady() {
		t.Fatal("a pending request must not open exec")
	}
	if _, ov, _ := admin.get("/api/v1/admin/overview"); ov["pendingBreakGlassRequests"] != float64(1) {
		t.Fatalf("overview: %v", ov)
	}
	if status, _, _ := dev.do("POST", fmt.Sprintf("/api/v1/break-glass/requests/%v/approve", req["id"]), nil); status != 403 {
		t.Fatalf("developer approving: %d", status)
	}

	// The admin approves it for longer than asked (at most the maximum); it starts now.
	if status, m := decide(req["id"], "approve", map[string]any{"durationMinutes": 241}); status != 422 {
		t.Fatalf("approving beyond the maximum: %d %v", status, m)
	}
	status, m := decide(req["id"], "approve", map[string]any{"durationMinutes": 90, "note": "go"})
	if status != 200 || m["status"] != "approved" || m["approvedMinutes"] != float64(90) || m["breakGlassId"] == nil {
		t.Fatalf("approve: %d %v", status, m)
	}
	if status, _ := decide(req["id"], "approve", nil); status != 409 {
		t.Fatalf("approving twice: %d", status)
	}
	if pendingID() != nil {
		t.Fatal("me.breakGlassRequest after approval")
	}
	waitFor(t, 6*time.Second, "exec open under the approved break-glass", execReady)
	_, page, _ := admin.get(fmt.Sprintf("/api/v1/audit?breakGlassId=%v&eventType=break_glass.start", m["breakGlassId"]))
	items, _ := page["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("break_glass.start events: %v", page)
	}
	ev := items[0].(map[string]any)
	if d := ev["details"].(map[string]any); ev["username"] != "reza" || d["owner"] != "sara" || d["minutes"] != float64(90) || d["requestedMinutes"] != float64(30) {
		t.Fatalf("break_glass.start: %v", ev)
	}
	if got, _ := time.Parse(time.RFC3339, fmt.Sprint(ev["details"].(map[string]any)["expiresAt"])); !got.Equal(start.Add(90 * time.Minute)) {
		t.Fatalf("expiresAt: %v", ev["details"])
	}
	// No new request while it is active.
	if status, _ := request(dev); status != 409 {
		t.Fatalf("request while active: %d", status)
	}
	if status, _, _ := dev.do("POST", fmt.Sprintf("/api/v1/break-glass/%v/end", m["breakGlassId"]), nil); status != 204 {
		t.Fatalf("end: %d", status)
	}

	// Rejected.
	_, req = request(dev)
	if status, m := decide(req["id"], "reject", map[string]any{"note": "use the runbook"}); status != 200 || m["status"] != "rejected" || m["decisionNote"] != "use the runbook" {
		t.Fatalf("reject: %d %v", status, m)
	}
	if pendingID() != nil || execReady() {
		t.Fatal("rejected request still pending or opened exec")
	}

	// Cancelled by the requester, never by someone else.
	_, req = request(dev)
	cancel := fmt.Sprintf("/api/v1/break-glass/requests/%v/cancel", req["id"])
	if status, _, _ := other.do("POST", cancel, nil); status != 403 {
		t.Fatalf("someone else cancelling: %d", status)
	}
	if status, m, _ := dev.do("POST", cancel, nil); status != 200 || m["status"] != "cancelled" {
		t.Fatalf("cancel: %d %v", status, m)
	}

	// Expired after the admin-set age: gone from me, cannot be approved.
	_, req = request(dev)
	e.setTime(start.Add(31 * time.Minute))
	if pendingID() != nil {
		t.Fatal("expired request still pending")
	}
	if status, _ := decide(req["id"], "approve", nil); status != 409 {
		t.Fatalf("approving an expired request: %d", status)
	}
	if _, list := admin.list("/api/v1/break-glass/requests"); len(list) != 4 || list[0]["status"] != "expired" {
		t.Fatalf("requests: %v", list)
	}
	if status, list := admin.list("/api/v1/break-glass/requests?status=pending"); status != 200 || len(list) != 0 {
		t.Fatalf("pending requests: %d %v", status, list)
	}

	// Switching to off refuses requests and approvals.
	_, req = request(dev)
	setBreakGlassMode(t, admin, "off", nil)
	if status, m := decide(req["id"], "approve", nil); status != 403 || errCode(m) != "break_glass_disabled" {
		t.Fatalf("approve while off: %d %v", status, m)
	}
	if status, m := request(other); status != 403 || errCode(m) != "break_glass_disabled" {
		t.Fatalf("request while off: %d %v", status, m)
	}
}

func TestClusterAndEnvironmentGrants(t *testing.T) {
	e := newEnv(t)
	admin := e.login(e.adminID)
	arash := e.login(e.otherID)
	ws := "/api/v1/clusters/prod-1/namespaces/search-api/workloads"
	if status, _ := arash.list(ws); status != 403 {
		t.Fatalf("before any grant: %d", status)
	}
	grant := func(scope map[string]any) string {
		t.Helper()
		status, m, _ := admin.do("POST", "/api/v1/admin/grants", map[string]any{
			"holder": map[string]any{"kind": "user", "id": e.otherID}, "scope": scope, "actions": []string{"logs"}})
		if status != 201 && status != 200 {
			t.Fatalf("create grant %v: %d %v", scope, status, m)
		}
		return itoa(int64(m["id"].(float64)))
	}

	// prod-1 is labeled prod: an environment grant covers all its namespaces,
	// and they show under "By namespace" without an empty namespace entry.
	id := grant(map[string]any{"kind": "environment", "environment": "prod"})
	if status, _ := arash.list(ws); status != 200 {
		t.Fatalf("environment grant: %d", status)
	}
	_, nss := arash.list("/api/v1/namespaces")
	got := namesOf(nss, "namespace")
	if !got["live-only"] || !got["search-api"] || got[""] {
		t.Fatalf("namespaces with environment grant: %v", got)
	}
	if status, _ := arash.list("/api/v1/clusters/stg-1/namespaces/search-api/workloads"); status != 403 {
		t.Fatalf("environment grant reached staging: %d", status)
	}
	if status, _, _ := admin.do("DELETE", "/api/v1/admin/grants/"+id, nil); status != 204 {
		t.Fatalf("delete: %d", status)
	}
	if status, _ := arash.list(ws); status != 403 {
		t.Fatalf("after delete: %d", status)
	}

	grant(map[string]any{"kind": "cluster", "cluster": "prod-1"})
	if status, _ := arash.list(ws); status != 200 {
		t.Fatalf("cluster grant: %d", status)
	}
	if status, m, _ := admin.do("POST", "/api/v1/admin/grants", map[string]any{
		"holder": map[string]any{"kind": "user", "id": e.otherID}, "scope": map[string]any{"kind": "environment", "environment": "unlabeled"},
		"actions": []string{"logs"}}); status != 422 {
		t.Fatalf("unlabeled environment: %d %v", status, m)
	}

	// Users cannot request them.
	for _, scope := range []map[string]any{{"kind": "cluster", "cluster": "prod-1"}, {"kind": "environment", "environment": "prod"}} {
		status, m, _ := arash.do("POST", "/api/v1/access-requests", map[string]any{
			"scope": scope, "actions": []string{"logs"}, "durationHours": 24, "reason": "need the whole thing"})
		if status != 422 {
			t.Fatalf("request %v: %d %v", scope, status, m)
		}
	}
}

// An auditor works like a user (grants, requests, break-glass) and also reads
// audit data; only admins decide requests.
func TestAuditorWorksLikeAUser(t *testing.T) {
	e := newEnv(t)
	admin := e.login(e.adminID)
	arash := e.login(e.otherID)
	aud := e.login(e.audID)
	ws := "/api/v1/clusters/prod-1/namespaces/search-api/workloads"
	if status, m, _ := aud.get(ws); status != 403 || errDetailReason(m) != "no_grant" {
		t.Fatalf("auditor without grant: %d %v", status, m)
	}
	// Another user's request is not on the auditor's My access page.
	if status, m, _ := arash.do("POST", "/api/v1/access-requests", map[string]any{
		"scope":   map[string]any{"kind": "namespace", "cluster": "prod-1", "namespace": "billing"},
		"actions": []string{"logs"}, "durationHours": 1, "reason": "someone else's request"}); status != 201 {
		t.Fatalf("other request: %d %v", status, m)
	}
	status, m, _ := aud.do("POST", "/api/v1/access-requests", map[string]any{
		"scope":   map[string]any{"kind": "namespace", "cluster": "prod-1", "namespace": "search-api"},
		"actions": []string{"logs", "restart"}, "durationHours": 24, "reason": "checking search after the incident"})
	if status != 201 {
		t.Fatalf("auditor request: %d %v", status, m)
	}
	id := itoa(int64(m["id"].(float64)))
	if status, _, _ := aud.do("POST", "/api/v1/access-requests/"+id+"/approve", nil); status != 403 {
		t.Fatalf("auditor must not approve, even their own: %d", status)
	}
	if status, _, _ := aud.do("POST", "/api/v1/access-requests/"+id+"/reject", nil); status != 403 {
		t.Fatalf("auditor must not reject: %d", status)
	}
	status, my, _ := aud.get("/api/v1/my/access")
	if status != 200 || len(my["requests"].([]any)) != 1 {
		t.Fatalf("auditor my access: %d %v", status, my)
	}
	if status, m, _ := admin.do("POST", "/api/v1/access-requests/"+id+"/approve", map[string]any{"note": "ok"}); status != 200 {
		t.Fatalf("approve: %d %v", status, m)
	}
	// With the grant the auditor works like a user, within the grant only.
	if status, l := aud.list(ws); status != 200 || len(l) == 0 {
		t.Fatalf("auditor with grant: %d %v", status, l)
	}
	if status, _, _ := aud.do("POST", wl+"/restart", nil); status != 403 {
		t.Fatalf("auditor outside the grant: %d", status)
	}
	if status, _ := aud.list("/api/v1/audit"); status != 200 {
		t.Fatalf("auditor still reads audit: %d", status)
	}
	if status, _, _ := aud.do("POST", "/api/v1/admin/teams", map[string]any{"name": "x"}); status != 403 {
		t.Fatalf("auditor must not write config: %d", status)
	}
	// Break-glass is open to auditors too.
	if status, m, _ := aud.do("POST", "/api/v1/break-glass", map[string]any{"reason": "production incident 4711, need exec now", "minutes": 30}); status != 201 && status != 200 {
		t.Fatalf("auditor break-glass: %d %v", status, m)
	}
}

// An admin may grant more (or fewer) actions than requested, within what the
// scope allows; the audit event records both.
func TestApproveAddsToTheSameScopeGrant(t *testing.T) {
	e := newEnv(t)
	arash := e.login(e.otherID)
	admin := e.login(e.adminID)
	scope := map[string]any{"kind": "namespace", "cluster": "prod-1", "namespace": "search-api"}
	approve := func(actions []string, hours int, body map[string]any) map[string]any {
		t.Helper()
		status, m, _ := arash.do("POST", "/api/v1/access-requests", map[string]any{
			"scope": scope, "actions": actions, "durationHours": hours, "reason": "debugging search latency"})
		if status != 201 {
			t.Fatalf("request: %d %v", status, m)
		}
		status, m, _ = admin.do("POST", "/api/v1/access-requests/"+itoa(int64(m["id"].(float64)))+"/approve", body)
		if status != 200 {
			t.Fatalf("approve: %d %v", status, m)
		}
		return m
	}
	grant := func(id any) map[string]any {
		t.Helper()
		_, l := admin.list("/api/v1/admin/grants/covering?cluster=prod-1&namespace=search-api")
		for _, g := range l {
			if g["id"] == id {
				return g
			}
		}
		t.Fatalf("no grant %v in %v", id, l)
		return nil
	}

	first := approve([]string{"logs"}, 24, nil)["resultingGrantId"]
	// Lasting longer: added to that grant, which now lasts as long.
	m := approve([]string{"restart"}, 48, nil)
	g := grant(first)
	if m["resultingGrantId"] != first || fmt.Sprint(g["actions"]) != "[logs restart]" ||
		!strings.HasPrefix(fmt.Sprint(g["expiresAt"]), e.clock.Now().Add(48*time.Hour).UTC().Format("2006-01-02T15")) {
		t.Fatalf("merged: %v, grant %v", m, g)
	}
	_, page, _ := admin.get("/api/v1/audit?eventType=access_request.decide")
	details := page["items"].([]any)[0].(map[string]any)["details"].(map[string]any)
	if details["merged"] != true || fmt.Sprint(details["before"].(map[string]any)["actions"]) != "[logs]" ||
		fmt.Sprint(details["after"].(map[string]any)["actions"]) != "[logs restart]" {
		t.Fatalf("audit details: %v", details)
	}
	// Shorter: exec must not last 48 h, so it gets its own grant.
	if m := approve([]string{"exec"}, 8, nil); m["resultingGrantId"] == first || fmt.Sprint(grant(m["resultingGrantId"])["actions"]) != "[exec]" {
		t.Fatalf("shorter: %v", m)
	}
	// The admin may ask for a separate grant.
	if m := approve([]string{"run-job"}, 72, map[string]any{"separate": true}); m["resultingGrantId"] == first {
		t.Fatalf("separate: %v", m)
	}
	if fmt.Sprint(grant(first)["actions"]) != "[logs restart]" {
		t.Fatalf("first grant changed: %v", grant(first))
	}
}

func TestApproveWithOtherActions(t *testing.T) {
	e := newEnv(t)
	arash := e.login(e.otherID)
	admin := e.login(e.adminID)
	request := func(scope map[string]any) string {
		t.Helper()
		status, m, _ := arash.do("POST", "/api/v1/access-requests", map[string]any{
			"scope": scope, "actions": []string{"logs"}, "durationHours": 24, "reason": "debugging search latency"})
		if status != 201 {
			t.Fatalf("request: %d %v", status, m)
		}
		return itoa(int64(m["id"].(float64)))
	}

	id := request(map[string]any{"kind": "namespace", "cluster": "prod-1", "namespace": "search-api"})
	status, m, _ := admin.do("POST", "/api/v1/access-requests/"+id+"/approve", map[string]any{"actions": []string{"logs", "sync"}})
	if status != 200 || fmt.Sprint(m["actions"]) != "[logs sync]" {
		t.Fatalf("approve with sync: %d %v", status, m)
	}
	status, l := admin.list("/api/v1/admin/grants/covering?cluster=prod-1&namespace=search-api")
	found := false
	for _, gr := range l {
		if gr["id"] == m["resultingGrantId"] {
			found = fmt.Sprint(gr["actions"]) == "[logs sync]"
		}
	}
	if status != 200 || !found {
		t.Fatalf("grant actions: %d %v", status, l)
	}
	_, page, _ := admin.get("/api/v1/audit?eventType=access_request.decide")
	items, _ := page["items"].([]any)
	if len(items) == 0 {
		t.Fatalf("audit: %v", page)
	}
	details := items[0].(map[string]any)["details"].(map[string]any)
	if fmt.Sprint(details["requestedActions"]) != "[logs]" || fmt.Sprint(details["actions"]) != "[logs sync]" {
		t.Fatalf("audit details: %v", details)
	}

	// sync never fits a workloads scope, approved or not.
	id = request(map[string]any{"kind": "workloads", "cluster": "prod-1", "namespace": "search-api",
		"workloads": []map[string]any{{"kind": "Deployment", "name": "search-api"}}})
	if status, m, _ := admin.do("POST", "/api/v1/access-requests/"+id+"/approve", map[string]any{"actions": []string{"logs", "sync"}}); status != 422 {
		t.Fatalf("sync on workloads: %d %v", status, m)
	}
}
