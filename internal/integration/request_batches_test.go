package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"tiphys/internal/domain"
)

func batchOf(t *testing.T, raw []byte) []map[string]any {
	t.Helper()
	var l []map[string]any
	if err := json.Unmarshal(raw, &l); err != nil {
		t.Fatalf("decode batch: %v: %s", err, raw)
	}
	return l
}

func TestAccessRequestBatches(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	arash := e.login(e.otherID)
	admin := e.login(e.adminID)
	body := func(nss ...string) map[string]any {
		return map[string]any{"cluster": "prod-1", "namespaces": nss, "actions": []string{"logs"}, "durationHours": 24, "reason": "incident 42"}
	}
	own := func() int {
		_, l := arash.list("/api/v1/access-requests")
		return len(l)
	}

	// Refused as a whole: a protected namespace, too few, too many, someone else's team.
	if status, m, _ := arash.do("POST", "/api/v1/access-requests/batch", body("billing", "kube-system")); status != 422 {
		t.Fatalf("protected namespace in a batch: %d %v", status, m)
	}
	if status, _, _ := arash.do("POST", "/api/v1/access-requests/batch", body("billing", " billing ")); status != 422 {
		t.Fatalf("one namespace is not a batch: %d", status)
	}
	many := make([]string, 21)
	for i := range many {
		many[i] = fmt.Sprintf("ns-%d", i)
	}
	if status, _, _ := arash.do("POST", "/api/v1/access-requests/batch", body(many...)); status != 422 {
		t.Fatalf("too many namespaces: %d", status)
	}
	team := body("billing", "search-api")
	team["teamId"] = e.teamID
	if status, _, _ := arash.do("POST", "/api/v1/access-requests/batch", team); status != 403 {
		t.Fatalf("batch for a team arash does not manage: %d", status)
	}
	if n := own(); n != 0 {
		t.Fatalf("refused batches left %d requests", n)
	}

	status, _, raw := arash.do("POST", "/api/v1/access-requests/batch", body("search-api", "billing", "search-api"))
	if status != 201 {
		t.Fatalf("create batch: %d %s", status, raw)
	}
	b := batchOf(t, raw)
	if len(b) != 2 || b[0]["batchId"] == nil || b[0]["batchId"] != b[1]["batchId"] || b[0]["status"] != "pending" {
		t.Fatalf("batch: %v", b)
	}
	batch := b[0]["batchId"].(string)
	ids := map[string]string{}
	for _, r := range b {
		ids[r["scope"].(map[string]any)["namespace"].(string)] = itoa(int64(r["id"].(float64)))
	}
	if _, ov, _ := admin.get("/api/v1/admin/overview"); ov["pendingRequests"] != float64(1) {
		t.Fatalf("a batch counts once: %v", ov["pendingRequests"])
	}
	// A request of a batch is decided only with its batch.
	if status, _, _ := admin.do("POST", "/api/v1/access-requests/"+ids["billing"]+"/approve", nil); status != 409 {
		t.Fatalf("single approve of a batch request: %d", status)
	}
	if status, _, _ := admin.do("POST", "/api/v1/access-requests/"+ids["billing"]+"/reject", nil); status != 409 {
		t.Fatalf("single reject of a batch request: %d", status)
	}
	if status, _, _ := arash.do("POST", "/api/v1/access-requests/batch/"+batch+"/decide", nil); status != 403 {
		t.Fatalf("a user must not decide: %d", status)
	}
	if status, _, _ := admin.do("POST", "/api/v1/access-requests/batch/not-a-uuid/decide", nil); status != 404 {
		t.Fatalf("unknown batch: %d", status)
	}
	if status, _, _ := admin.do("POST", "/api/v1/access-requests/batch/"+batch+"/decide", map[string]any{"approve": []int{999999}}); status != 422 {
		t.Fatalf("approving a request outside the batch: %d", status)
	}

	// Approve one, reject the other.
	sid := int64(b[0]["id"].(float64))
	if ids["search-api"] != itoa(sid) {
		sid = int64(b[1]["id"].(float64))
	}
	status, _, raw = admin.do("POST", "/api/v1/access-requests/batch/"+batch+"/decide", map[string]any{"approve": []int64{sid}, "note": "only search"})
	if status != 200 {
		t.Fatalf("decide: %d %s", status, raw)
	}
	for _, r := range batchOf(t, raw) {
		ns := r["scope"].(map[string]any)["namespace"]
		want := map[any]string{"search-api": "approved", "billing": "rejected"}[ns]
		if r["status"] != want || r["decisionNote"] != "only search" || (want == "approved") != (r["resultingGrantId"] != nil) {
			t.Fatalf("decided %v: %v", ns, r)
		}
	}
	if status, _ := arash.list("/api/v1/clusters/prod-1/namespaces/search-api/workloads"); status != 200 {
		t.Fatalf("approved namespace: %d", status)
	}
	if status, _ := arash.list("/api/v1/clusters/prod-1/namespaces/billing/workloads"); status != 403 {
		t.Fatalf("rejected namespace: %d", status)
	}
	if status, _, _ := admin.do("POST", "/api/v1/access-requests/batch/"+batch+"/decide", nil); status != 409 {
		t.Fatalf("deciding twice: %d", status)
	}
	if _, ov, _ := admin.get("/api/v1/admin/overview"); ov["pendingRequests"] != float64(0) {
		t.Fatalf("pending after the decision: %v", ov["pendingRequests"])
	}

	// All or nothing: a request the previous release decided alone during a
	// rollout makes the whole decision fail and changes nothing.
	_, _, raw = arash.do("POST", "/api/v1/access-requests/batch", body("billing", "payments-api"))
	b = batchOf(t, raw)
	batch = b[0]["batchId"].(string)
	first, second := int64(b[0]["id"].(float64)), int64(b[1]["id"].(float64))
	if err := e.store.Repos().Requests.Decide(ctx, first, domain.RequestRejected, "old-release", "", nil, time.Now()); err != nil {
		t.Fatal(err)
	}
	if status, _, _ := admin.do("POST", "/api/v1/access-requests/batch/"+batch+"/decide", map[string]any{"approve": []int64{first, second}}); status != 409 {
		t.Fatalf("batch with a decided request: %d", status)
	}
	if r, err := e.store.Repos().Requests.Get(ctx, second); err != nil || r.Status != domain.RequestPending {
		t.Fatalf("the other request changed: %+v %v", r, err)
	}
	if status, _ := arash.list("/api/v1/clusters/prod-1/namespaces/billing/workloads"); status != 403 {
		t.Fatalf("nothing may be granted: %d", status)
	}
}
