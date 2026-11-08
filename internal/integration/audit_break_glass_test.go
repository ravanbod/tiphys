package integration

import (
	"context"
	"strings"
	"testing"
	"time"

	"tiphys/internal/domain"
)

// "Break-glass only" is a server filter: a break-glass event older than a
// whole page of other events is still on the first page, and in the export.
func TestAuditBreakGlassFilter(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	dev := e.login(e.devID)
	admin := e.login(e.adminID)
	status, m, _ := dev.do("POST", "/api/v1/break-glass", map[string]any{"reason": "production incident INC-77, payments down", "durationMinutes": 30})
	if status != 201 {
		t.Fatalf("break-glass: %d %v", status, m)
	}
	bgID := m["id"].(float64)
	for i := 0; i < 5; i++ {
		if _, err := e.store.Repos().Audit.Insert(ctx, domain.AuditEvent{
			At: time.Now(), Username: "arash", EventType: domain.EventLogsView, Action: "logs", Result: domain.ResultAllowed,
			Cluster: "prod-1", Namespace: "payments-api", Environment: "prod",
		}); err != nil {
			t.Fatal(err)
		}
	}
	_, page, _ := admin.get("/api/v1/audit?limit=2")
	if items := page["items"].([]any); len(items) != 2 || items[0].(map[string]any)["breakGlassId"] != nil {
		t.Fatalf("newest events are not break-glass ones: %v", items)
	}
	_, page, _ = admin.get("/api/v1/audit?breakGlass=true&limit=2")
	items := page["items"].([]any)
	if len(items) == 0 {
		t.Fatal("breakGlass=true found nothing on the first page")
	}
	for _, it := range items {
		if it.(map[string]any)["breakGlassId"] != bgID {
			t.Fatalf("not a break-glass event: %v", it)
		}
	}
	_, _, raw := admin.get("/api/v1/audit/export.csv?breakGlass=true")
	csv := string(raw)
	if !strings.Contains(csv, "break_glass.start") || strings.Contains(csv, "logs.view") {
		t.Fatalf("export with breakGlass=true:\n%s", csv)
	}
}
