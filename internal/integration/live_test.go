package integration

import (
	"bufio"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"tiphys/internal/application"
	"tiphys/internal/domain"
)

type sseEvent struct {
	name string
	data map[string]any
}

// openSSE opens a Server-Sent Events stream and delivers its events until
// the test ends.
func (c *client) openSSE(path string) (int, <-chan sseEvent) {
	c.e.t.Helper()
	req, _ := http.NewRequest("GET", c.e.srv.URL+path, nil)
	req.AddCookie(&http.Cookie{Name: "tiphys_session", Value: c.token})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		c.e.t.Fatalf("GET %s: %v", path, err)
	}
	c.e.t.Cleanup(func() { resp.Body.Close() })
	out := make(chan sseEvent, 16)
	if resp.StatusCode != http.StatusOK {
		close(out)
		return resp.StatusCode, out
	}
	go func() {
		defer close(out)
		sc := bufio.NewScanner(resp.Body)
		var ev sseEvent
		for sc.Scan() {
			line := sc.Text()
			switch {
			case strings.HasPrefix(line, "event:"):
				ev.name = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			case strings.HasPrefix(line, "data:"):
				_ = json.Unmarshal([]byte(strings.TrimPrefix(line, "data:")), &ev.data)
			case line == "" && ev.name != "":
				out <- ev
				ev = sseEvent{}
			}
		}
	}()
	return resp.StatusCode, out
}

func nextSSE(t *testing.T, ch <-chan sseEvent) sseEvent {
	t.Helper()
	select {
	case ev, ok := <-ch:
		if !ok {
			t.Fatal("stream ended")
		}
		return ev
	case <-time.After(3 * time.Second):
		t.Fatal("no event")
	}
	return sseEvent{}
}

func TestLiveChanges(t *testing.T) {
	e := newEnv(t)
	admin := e.login(e.adminID)
	dev := e.login(e.devID)
	api := domain.WorkloadRef{Kind: domain.KindDeployment, Name: "payments-api"}
	worker := domain.WorkloadRef{Kind: domain.KindDeployment, Name: "payments-worker"}
	exit := int32(137)
	restart := func(ref domain.WorkloadRef) application.NamespaceChange {
		return application.NamespaceChange{Workload: ref, Type: application.ChangePodRestarted, Pod: ref.Name + "-abc",
			Container: "app", Reason: "OOMKilled", ExitCode: &exit, Phase: "Running"}
	}

	for _, bad := range []string{"", "?ns=prod-1", "?ns=prod-1/Bad_NS", "?ns=prod-1/a" + strings.Repeat("&ns=prod-1/n", 10)} {
		if status, _ := dev.openSSE("/api/v1/live" + bad); status != 422 {
			t.Fatalf("live%s: %d", bad, status)
		}
	}

	// The team's grant reaches payments-api, not search-api.
	status, devCh := dev.openSSE("/api/v1/live?ns=prod-1/payments-api&ns=prod-1/search-api")
	if status != 200 {
		t.Fatalf("live: %d", status)
	}
	ready := nextSSE(t, devCh)
	nss, _ := ready.data["namespaces"].([]any)
	if ready.name != "ready" || len(nss) != 2 || nss[0].(map[string]any)["error"] != nil || nss[1].(map[string]any)["error"] != "denied" {
		t.Fatalf("ready = %+v", ready)
	}
	e.kube.emit("payments-api", restart(api))
	ev := nextSSE(t, devCh)
	if ev.name != "change" || ev.data["type"] != "pod_restarted" || ev.data["name"] != "payments-api" || ev.data["cluster"] != "prod-1" ||
		ev.data["reason"] != "OOMKilled" || ev.data["exitCode"] != float64(137) || ev.data["pod"] != "payments-api-abc" {
		t.Fatalf("dev change = %+v", ev)
	}

	// Restart on one workload only: its pod details are hidden, the other
	// workload's changes are not sent, rollouts are.
	status, m, _ := admin.do("POST", "/api/v1/admin/grants", map[string]any{
		"holder": map[string]any{"kind": "user", "id": e.otherID},
		"scope": map[string]any{"kind": "workloads", "cluster": "prod-1", "namespace": "payments-api",
			"workloads": []map[string]any{{"kind": "Deployment", "name": "payments-api"}}},
		"actions": []string{"restart"}})
	if status != 201 && status != 200 {
		t.Fatalf("create workload grant: %d %v", status, m)
	}
	status, arashCh := e.login(e.otherID).openSSE("/api/v1/live?ns=prod-1/payments-api")
	if status != 200 || nextSSE(t, arashCh).name != "ready" {
		t.Fatalf("arash live: %d", status)
	}
	e.kube.emit("payments-api", restart(api))
	if ev := nextSSE(t, arashCh); ev.data["type"] != "workload" || ev.data["name"] != "payments-api" || ev.data["pod"] != nil || ev.data["reason"] != nil {
		t.Fatalf("arash restart = %+v", ev)
	}
	e.kube.emit("payments-api", restart(worker))
	e.kube.emit("payments-api", application.NamespaceChange{Workload: api, Type: application.ChangeRolloutStarted,
		Status: &application.RolloutStatus{Desired: 3, Updated: 1, Ready: 3}})
	ev = nextSSE(t, arashCh)
	if st, _ := ev.data["status"].(map[string]any); ev.data["type"] != "rollout_started" || st["updated"] != float64(1) {
		t.Fatalf("arash rollout = %+v", ev)
	}
	// dev still sees everything, in order.
	for _, want := range []string{"pod_restarted", "pod_restarted", "rollout_started"} {
		if ev := nextSSE(t, devCh); ev.data["type"] != want {
			t.Fatalf("dev change = %+v, want %s", ev, want)
		}
	}
}

func TestLiveStreamsPerUserAreLimited(t *testing.T) {
	e := newEnv(t)
	dev := e.login(e.devID)
	for i := 0; i < application.MaxLiveStreams; i++ {
		if status, _ := dev.openSSE("/api/v1/live?ns=prod-1/payments-api"); status != 200 {
			t.Fatalf("stream %d: %d", i, status)
		}
	}
	if status, _ := dev.openSSE("/api/v1/live?ns=prod-1/payments-api"); status != 429 {
		t.Fatalf("over the limit: %d", status)
	}
}
