package kubeaccess

import (
	"context"
	"io"
	"testing"

	"tiphys/internal/application"
)

func TestListOtherPods(t *testing.T) {
	r := newRig(t, append(ownerFixtures(), pod("pg-main-1", ctrl("postgresql.cnpg.io/v1", "Cluster", "pg-main"), "postgres"))...)
	got, err := r.ListOtherPods(context.Background(), actor, "c1", testNS)
	if err != nil {
		t.Fatal(err)
	}
	// Sorted by owner (none first), then name; workloads' pods are left out.
	want := []struct{ name, owner string }{
		{"orphan", ""},
		{"pg-main-1", "postgresql.cnpg.io/Cluster/pg-main"},
		{"other-job-p", "batch/Job/stray-1"},
		{"sneaky", "apps/ReplicaSet/fake-owner"},
		{"wrong-group", "example.com/StatefulSet/db"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d pods: %+v", len(got), got)
	}
	for i, w := range want {
		owner := ""
		if o := got[i].Owner; o != nil {
			owner = o.Group + "/" + o.Kind + "/" + o.Name
		}
		if got[i].Name != w.name || owner != w.owner {
			t.Errorf("pod %d = %s (%s), want %s (%s)", i, got[i].Name, owner, w.name, w.owner)
		}
	}
	if _, err := r.ListOtherPods(context.Background(), actor, "c1", "kube-system"); err == nil {
		t.Fatal("protected namespace listed")
	}
}

func TestOtherPodLogs(t *testing.T) {
	r := newRig(t, append(ownerFixtures(), pod("pg-main-1", ctrl("postgresql.cnpg.io/v1", "Cluster", "pg-main"), "postgres"))...)
	ctx := context.Background()
	opts := func(c string) application.LogOptions { return application.LogOptions{Container: c, TailLines: 100} }

	rc, err := r.OtherPodLogs(ctx, actor, "c1", testNS, "pg-main-1", opts("postgres"))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(rc)
	rc.Close()
	if len(b) == 0 {
		t.Fatal("no log")
	}
	for _, p := range []string{"orphan", "other-job-p", "sneaky"} {
		if rc, err := r.OtherPodLogs(ctx, actor, "c1", testNS, p, opts("app")); err != nil {
			t.Fatalf("%s: %v", p, err)
		} else {
			rc.Close()
		}
	}
	// A workload's pod is read only as the workload's.
	for _, p := range []string{"web-abc-1", "canary-abc-1", "db-0", "agent-x", "report-1-p"} {
		_, err := r.OtherPodLogs(ctx, actor, "c1", testNS, p, opts("app"))
		wantErr(t, err, application.ErrPodHasWorkload)
	}
	_, err = r.OtherPodLogs(ctx, actor, "c1", testNS, "pg-main-1", opts("app"))
	wantErr(t, err, application.ErrInvalidName)
	_, err = r.OtherPodLogs(ctx, actor, "c1", testNS, "no-such-pod", opts("app"))
	wantErr(t, err, application.ErrNotFound)
	_, err = r.OtherPodLogs(ctx, actor, "c1", "kube-system", "pg-main-1", opts("postgres"))
	wantErr(t, err, application.ErrProtectedNamespace)
	_, err = r.OtherPodLogs(ctx, actor, "c1", testNS, "pg-main-1", application.LogOptions{Container: "postgres", TailLines: 7})
	wantErr(t, err, application.ErrInvalidName)
}
