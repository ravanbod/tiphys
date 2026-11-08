package integration

import (
	"context"
	"testing"

	"tiphys/internal/application"
	"tiphys/internal/domain"
)

func TestAdminCreatesGrantsOnSeveralNamespaces(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	admin := e.login(e.adminID)
	arash := e.login(e.otherID)
	body := func(nss ...string) map[string]any {
		return map[string]any{
			"holder": map[string]any{"kind": "user", "id": e.otherID}, "scope": map[string]any{"kind": "namespace", "cluster": "prod-1"},
			"actions": []string{"logs"}, "note": "on-call", "namespaces": nss,
		}
	}
	count := func() int {
		gs, _ := e.store.Repos().Grants.List(ctx)
		return len(gs)
	}
	before := count()

	if status, _, _ := arash.do("POST", "/api/v1/admin/grants/batch", body("billing", "search-api")); status != 403 {
		t.Fatalf("a user must not create grants: %d", status)
	}
	if status, m, _ := admin.do("POST", "/api/v1/admin/grants/batch", body("billing", "kube-system")); status != 422 {
		t.Fatalf("protected namespace: %d %v", status, m)
	}
	if status, _, _ := admin.do("POST", "/api/v1/admin/grants/batch", body("billing")); status != 422 {
		t.Fatalf("one namespace: %d", status)
	}
	if n := count(); n != before {
		t.Fatalf("refused batches created %d grants", n-before)
	}

	status, _, raw := admin.do("POST", "/api/v1/admin/grants/batch", body("billing", "search-api", "billing"))
	if status != 201 {
		t.Fatalf("create: %d %s", status, raw)
	}
	gs := batchOf(t, raw)
	if len(gs) != 2 || gs[0]["note"] != "on-call" || gs[0]["id"] == gs[1]["id"] {
		t.Fatalf("grants: %v", gs)
	}
	if n := count(); n != before+2 {
		t.Fatalf("grants created: %d", n-before)
	}
	for _, ns := range []string{"billing", "search-api"} {
		if status, _ := arash.list("/api/v1/clusters/prod-1/namespaces/" + ns + "/workloads"); status != 200 {
			t.Fatalf("%s after the batch: %d", ns, status)
		}
	}
	evs, _ := e.store.Repos().Audit.List(ctx, application.AuditFilter{EventType: domain.EventAdminChange})
	batch := map[any]int{}
	for _, ev := range evs {
		if id, ok := ev.Details["batchId"]; ok && ev.Details["entity"] == "grant" {
			batch[id]++
		}
	}
	if len(batch) != 1 {
		t.Fatalf("one batch of audit events expected: %v", batch)
	}
	for _, n := range batch {
		if n != 2 {
			t.Fatalf("one audit event per grant expected: %d", n)
		}
	}
}
