package integration

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"go.uber.org/zap"

	"tiphys/internal/application"
)

// flakyKube lets a test make the cluster checks fail.
type flakyKube struct {
	*fakeKube
	probeErr   error
	versionErr error
}

func (f *flakyKube) Probe(ctx context.Context, c string) (application.ClusterProbe, error) {
	if f.probeErr != nil {
		return application.ClusterProbe{}, f.probeErr
	}
	return f.fakeKube.Probe(ctx, c)
}

func (f *flakyKube) Version(ctx context.Context, c string) (string, error) {
	if f.versionErr != nil {
		return "", f.versionErr
	}
	return f.fakeKube.Version(ctx, c)
}

func TestClusterMonitorRecoversByItself(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	flaky := &flakyKube{fakeKube: e.kube}
	svc := application.NewClusterService(e.store, flaky, noCreds{}, e.audit, e.policy, e.clock, zap.NewNop())
	reachable := func() bool {
		t.Helper()
		c, err := e.store.Repos().Clusters.Get(ctx, "prod-1")
		if err != nil {
			t.Fatal(err)
		}
		return c.Reachable
	}
	advance := func(d time.Duration) { e.clock.Set(e.clock.Now().Add(d)); svc.CheckDue(ctx) }

	// Right after start the credential may not be loaded yet: that alone must
	// not mark the cluster unreachable, and the next try comes soon.
	flaky.probeErr = fmt.Errorf("cluster prod-1: %w", application.ErrClusterUnknown)
	svc.CheckDue(ctx)
	if !reachable() {
		t.Fatal("a not-yet-loaded credential must not mark the cluster unreachable")
	}
	flaky.probeErr = nil
	advance(11 * time.Second)
	if !reachable() || svc.Runtime("prod-1").Usable != true {
		t.Fatal("expected the cluster to be checked again after 10 s and be usable")
	}

	// A healthy cluster is checked every 30 s, not before.
	flaky.versionErr = errors.New("connection refused")
	advance(20 * time.Second)
	if !reachable() {
		t.Fatal("a healthy cluster must not be re-checked within 30 s")
	}
	advance(11 * time.Second) // failure 1
	advance(11 * time.Second) // failure 2, retried every 10 s once failing
	if !reachable() {
		t.Fatal("two failures must not mark the cluster unreachable")
	}
	advance(11 * time.Second) // failure 3
	if reachable() {
		t.Fatal("three failures in a row must mark the cluster unreachable")
	}

	// When it answers again, the next 10 s retry brings it back, with no manual check.
	flaky.versionErr = nil
	advance(11 * time.Second)
	if !reachable() {
		t.Fatal("the cluster must recover on its own within one retry interval")
	}
}
