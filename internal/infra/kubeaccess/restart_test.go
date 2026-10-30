package kubeaccess

import (
	"context"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	clienttesting "k8s.io/client-go/testing"

	"tiphys/internal/application"
	"tiphys/internal/domain"
)

func lastPatch(t *testing.T, actions []clienttesting.Action) clienttesting.PatchAction {
	t.Helper()
	for i := len(actions) - 1; i >= 0; i-- {
		if p, ok := actions[i].(clienttesting.PatchAction); ok {
			return p
		}
	}
	t.Fatal("no patch was sent")
	return nil
}

func TestRestartPatchBodies(t *testing.T) {
	const ts = "2026-10-20T12:00:00Z"
	const tmpl = `{"spec":{"template":{"metadata":{"annotations":{"kubectl.kubernetes.io/restartedAt":"` + ts + `"}}}}}`
	r := newRig(t,
		deployment("web"),
		&appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Name: "db", Namespace: testNS}},
		&appsv1.DaemonSet{ObjectMeta: metav1.ObjectMeta{Name: "agent", Namespace: testNS}},
	)
	for _, kind := range []domain.WorkloadKind{domain.KindDeployment, domain.KindStatefulSet, domain.KindDaemonSet} {
		name := map[domain.WorkloadKind]string{domain.KindDeployment: "web", domain.KindStatefulSet: "db", domain.KindDaemonSet: "agent"}[kind]
		r.kube.ClearActions()
		if err := r.Restart(context.Background(), actor, target(kind, name)); err != nil {
			t.Fatalf("%s: %v", kind, err)
		}
		p := lastPatch(t, r.kube.Actions())
		if p.GetPatchType() != types.StrategicMergePatchType || string(p.GetPatch()) != tmpl || p.GetName() != name {
			t.Fatalf("%s: patch %s %s on %s", kind, p.GetPatchType(), p.GetPatch(), p.GetName())
		}
	}
}

func TestRestartRolloutPatchBody(t *testing.T) {
	r := newRig(t)
	r.dyn.Tracker().Add(rollout("canary", map[string]any{"replicas": int64(1)}))
	if err := r.Restart(context.Background(), actor, target(domain.KindRollout, "canary")); err != nil {
		t.Fatal(err)
	}
	p := lastPatch(t, r.dyn.Actions())
	if p.GetPatchType() != types.MergePatchType || string(p.GetPatch()) != `{"spec":{"restartAt":"2026-10-20T12:00:00Z"}}` {
		t.Fatalf("patch %s %s", p.GetPatchType(), p.GetPatch())
	}
}

func TestRestartMissingWorkload(t *testing.T) {
	r := newRig(t)
	wantErr(t, r.Restart(context.Background(), actor, target(domain.KindDeployment, "gone")), application.ErrNotFound)
}

func TestDeploymentStatus(t *testing.T) {
	d := deployment("web")
	d.Generation, d.Status.ObservedGeneration = 2, 1
	if s := deploymentStatus(d); s.Complete || s.Failed {
		t.Fatalf("generation not observed: %+v", s)
	}
	d.Status.ObservedGeneration, d.Status.UpdatedReplicas, d.Status.Replicas = 2, 1, 3
	if s := deploymentStatus(d); s.Complete || s.Updated != 1 || s.Desired != 2 {
		t.Fatalf("partially updated: %+v", s)
	}
	d.Status.UpdatedReplicas, d.Status.Replicas, d.Status.AvailableReplicas = 2, 3, 2
	if s := deploymentStatus(d); s.Complete {
		t.Fatalf("old replicas still present: %+v", s)
	}
	d.Status.Replicas, d.Status.ReadyReplicas = 2, 2
	if s := deploymentStatus(d); !s.Complete || s.Available != 2 {
		t.Fatalf("done: %+v", s)
	}
	d.Status.Conditions = []appsv1.DeploymentCondition{{Type: appsv1.DeploymentProgressing, Reason: "ProgressDeadlineExceeded"}}
	if s := deploymentStatus(d); !s.Failed || s.Complete {
		t.Fatalf("deadline exceeded: %+v", s)
	}
}

func TestRolloutStatusWaitsForRestartedAt(t *testing.T) {
	o := rollout("canary", map[string]any{"replicas": int64(1), "restartAt": "2026-10-20T12:00:00Z"})
	o.Object["status"] = map[string]any{"phase": "Healthy", "updatedReplicas": int64(1), "availableReplicas": int64(1)}
	if s := rolloutStatusOf(o); s.Complete {
		t.Fatalf("Healthy before the controller restarted the pods must not count: %+v", s)
	}
	o.Object["status"].(map[string]any)["restartedAt"] = "2026-10-20T12:00:03Z"
	if s := rolloutStatusOf(o); !s.Complete {
		t.Fatalf("restart recorded: %+v", s)
	}
	o.Object["status"].(map[string]any)["phase"] = "Degraded"
	if s := rolloutStatusOf(o); !s.Failed {
		t.Fatalf("degraded: %+v", s)
	}
}

func TestStatefulSetAndDaemonSetStatus(t *testing.T) {
	two := int32(2)
	ss := &appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Generation: 1}, Spec: appsv1.StatefulSetSpec{Replicas: &two}}
	ss.Status = appsv1.StatefulSetStatus{ObservedGeneration: 1, UpdatedReplicas: 2, ReadyReplicas: 1, CurrentRevision: "a", UpdateRevision: "b"}
	if s := statefulSetStatus(ss); s.Complete {
		t.Fatalf("%+v", s)
	}
	ss.Status.ReadyReplicas, ss.Status.CurrentRevision = 2, "b"
	if s := statefulSetStatus(ss); !s.Complete {
		t.Fatalf("%+v", s)
	}
	ds := &appsv1.DaemonSet{ObjectMeta: metav1.ObjectMeta{Generation: 3}}
	ds.Status = appsv1.DaemonSetStatus{ObservedGeneration: 3, DesiredNumberScheduled: 2, UpdatedNumberScheduled: 2, NumberAvailable: 1}
	if s := daemonSetStatus(ds); s.Complete {
		t.Fatalf("%+v", s)
	}
	ds.Status.NumberAvailable = 2
	if s := daemonSetStatus(ds); !s.Complete {
		t.Fatalf("%+v", s)
	}
}

func TestWatchRolloutEmitsUntilComplete(t *testing.T) {
	d := deployment("web")
	d.Status.ObservedGeneration = 1
	d.Status.Replicas, d.Status.UpdatedReplicas, d.Status.AvailableReplicas, d.Status.ReadyReplicas = 3, 1, 2, 2
	r := newRig(t, d)
	ch, err := r.WatchRollout(context.Background(), actor, target(domain.KindDeployment, "web"), 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if s := <-ch; s.Complete || s.Updated != 1 {
		t.Fatalf("first = %+v", s)
	}
	go func() {
		time.Sleep(20 * time.Millisecond)
		d.Status.Replicas, d.Status.UpdatedReplicas, d.Status.AvailableReplicas, d.Status.ReadyReplicas = 2, 2, 2, 2
		r.kube.AppsV1().Deployments(testNS).UpdateStatus(context.Background(), d, metav1.UpdateOptions{})
	}()
	var last application.RolloutStatus
	for s := range ch {
		last = s
	}
	if !last.Complete || last.Updated != 2 {
		t.Fatalf("last = %+v", last)
	}
}

func TestWatchRolloutDeadline(t *testing.T) {
	r := newRig(t, deployment("web")) // observedGeneration 0 < generation: never completes
	ch, err := r.WatchRollout(context.Background(), actor, target(domain.KindDeployment, "web"), 50*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	var last application.RolloutStatus
	for s := range ch {
		last = s
	}
	if last.Complete || last.Failed || last.Message != "deadline exceeded" {
		t.Fatalf("last = %+v", last)
	}
}

func TestWatchRolloutStopsWithContext(t *testing.T) {
	r := newRig(t, deployment("web"))
	ctx, cancel := context.WithCancel(context.Background())
	ch, err := r.WatchRollout(ctx, actor, target(domain.KindDeployment, "web"), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	<-ch
	cancel()
	select {
	case _, ok := <-ch:
		for ok {
			_, ok = <-ch
		}
	case <-time.After(2 * time.Second):
		t.Fatal("channel not closed after cancel")
	}
}
