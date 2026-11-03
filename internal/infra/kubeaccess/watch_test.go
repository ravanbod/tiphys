package kubeaccess

import (
	"context"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"

	"tiphys/internal/application"
	"tiphys/internal/domain"
)

var apiRef = domain.WorkloadRef{Kind: domain.KindDeployment, Name: "api"}

// rolledOut is Deployment api at a revision with its rollout complete.
func rolledOut(revision string, replicas int32) *appsv1.Deployment {
	d := deployment("api")
	d.Annotations = map[string]string{deploymentRevision: revision}
	d.Spec.Replicas = ptr.To(replicas)
	d.Status = appsv1.DeploymentStatus{ObservedGeneration: 1, Replicas: replicas, UpdatedReplicas: replicas, ReadyReplicas: replicas, AvailableReplicas: replicas}
	return d
}

func startWatch(t *testing.T, r *rig) <-chan application.NamespaceChange {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	ch, err := r.WatchNamespace(ctx, actor, "c1", testNS)
	if err != nil {
		t.Fatalf("WatchNamespace: %v", err)
	}
	return ch
}

// next returns the next change, failing after a second without one.
func next(t *testing.T, ch <-chan application.NamespaceChange) application.NamespaceChange {
	t.Helper()
	select {
	case c, ok := <-ch:
		if !ok {
			t.Fatal("watch ended")
		}
		return c
	case <-time.After(time.Second):
		t.Fatal("no change")
	}
	return application.NamespaceChange{}
}

// nextOf skips changes of other types.
func nextOf(t *testing.T, ch <-chan application.NamespaceChange, typ application.ChangeType) application.NamespaceChange {
	t.Helper()
	for {
		if c := next(t, ch); c.Type == typ {
			return c
		}
	}
}

func TestWatchNamespacePodChanges(t *testing.T) {
	ctx := context.Background()
	p := pod("api-1-a", ctrl("apps/v1", "ReplicaSet", "api-1"))
	p.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "app", Ready: true}}
	r := newRig(t, rolledOut("1", 1), replicaSet("api-1", ctrl("apps/v1", "Deployment", "api")), p,
		pod("bare", nil), // owned by no workload: never reported
	)
	ch := startWatch(t, r)

	p = p.DeepCopy()
	p.Status.ContainerStatuses[0].RestartCount = 1
	p.Status.ContainerStatuses[0].Ready = false
	p.Status.ContainerStatuses[0].LastTerminationState.Terminated = &corev1.ContainerStateTerminated{Reason: "OOMKilled", ExitCode: 137}
	if _, err := r.kube.CoreV1().Pods(testNS).UpdateStatus(ctx, p, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	c := next(t, ch)
	if c.Type != application.ChangePodRestarted || c.Workload != apiRef || c.Pod != "api-1-a" || c.Container != "app" ||
		c.Reason != "OOMKilled" || c.ExitCode == nil || *c.ExitCode != 137 {
		t.Fatalf("restart = %+v", c)
	}

	bare := pod("bare", nil)
	bare.Status.Phase = corev1.PodRunning
	if _, err := r.kube.CoreV1().Pods(testNS).UpdateStatus(ctx, bare, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.kube.CoreV1().Pods(testNS).Create(ctx, pod("api-1-b", ctrl("apps/v1", "ReplicaSet", "api-1")), metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if c := nextOf(t, ch, application.ChangePodAdded); c.Workload != apiRef || c.Pod != "api-1-b" {
		t.Fatalf("added = %+v", c)
	}
	if err := r.kube.CoreV1().Pods(testNS).Delete(ctx, "api-1-b", metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	if c := nextOf(t, ch, application.ChangePodRemoved); c.Workload != apiRef || c.Pod != "api-1-b" {
		t.Fatalf("removed = %+v", c)
	}
}

func TestWatchNamespaceCronJobPods(t *testing.T) {
	r := newRig(t, job("nightly-1", ctrl("batch/v1", "CronJob", "nightly"), false))
	ch := startWatch(t, r)
	if _, err := r.kube.CoreV1().Pods(testNS).Create(context.Background(), pod("nightly-1-x", ctrl("batch/v1", "Job", "nightly-1")), metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if c := next(t, ch); c.Type != application.ChangePodAdded || c.Workload != (domain.WorkloadRef{Kind: domain.KindCronJob, Name: "nightly"}) {
		t.Fatalf("change = %+v", c)
	}
}

func TestWatchNamespaceRollout(t *testing.T) {
	ctx := context.Background()
	r := newRig(t, rolledOut("1", 1), replicaSet("api-1", ctrl("apps/v1", "Deployment", "api")), replicaSet("api-2", ctrl("apps/v1", "Deployment", "api")),
		pod("api-1-a", ctrl("apps/v1", "ReplicaSet", "api-1")))
	ch := startWatch(t, r)

	d := rolledOut("2", 1)
	d.Generation, d.Status.UpdatedReplicas = 2, 0
	if _, err := r.kube.AppsV1().Deployments(testNS).Update(ctx, d, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.kube.CoreV1().Pods(testNS).Create(ctx, pod("api-2-a", ctrl("apps/v1", "ReplicaSet", "api-2")), metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	c := nextOf(t, ch, application.ChangeRolloutStarted)
	if c.Workload != apiRef || c.Status == nil || c.Status.Complete {
		t.Fatalf("started = %+v", c)
	}

	// No pod changes needed: a running rollout is polled.
	d = rolledOut("2", 1)
	d.Generation, d.Status.ObservedGeneration = 2, 2
	if _, err := r.kube.AppsV1().Deployments(testNS).Update(ctx, d, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if c := nextOf(t, ch, application.ChangeRolloutComplete); c.Workload != apiRef || !c.Status.Complete {
		t.Fatalf("complete = %+v", c)
	}
}

func TestWatchNamespaceScaleIsNoRollout(t *testing.T) {
	ctx := context.Background()
	r := newRig(t, rolledOut("1", 1), replicaSet("api-1", ctrl("apps/v1", "Deployment", "api")), pod("api-1-a", ctrl("apps/v1", "ReplicaSet", "api-1")))
	ch := startWatch(t, r)

	if _, err := r.kube.AppsV1().Deployments(testNS).Update(ctx, rolledOut("1", 2), metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.kube.CoreV1().Pods(testNS).Create(ctx, pod("api-1-b", ctrl("apps/v1", "ReplicaSet", "api-1")), metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if c := next(t, ch); c.Type != application.ChangePodAdded {
		t.Fatalf("first = %+v", c)
	}
	if c := next(t, ch); c.Type != application.ChangeWorkload || c.Status == nil || c.Status.Desired != 2 {
		t.Fatalf("second = %+v", c)
	}
}

// The controller counts a pod as ready after the pod changed, possibly after
// the first read: the status is read again for a while.
func TestWatchNamespaceSeesLateStatus(t *testing.T) {
	ctx := context.Background()
	d := rolledOut("1", 1)
	d.Status.ReadyReplicas, d.Status.AvailableReplicas = 0, 0
	p := pod("api-1-a", ctrl("apps/v1", "ReplicaSet", "api-1"))
	p.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "app"}}
	r := newRig(t, d, replicaSet("api-1", ctrl("apps/v1", "Deployment", "api")), p)
	ch := startWatch(t, r)

	p = p.DeepCopy()
	p.Status.ContainerStatuses[0].Ready = true
	if _, err := r.kube.CoreV1().Pods(testNS).UpdateStatus(ctx, p, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if c := next(t, ch); c.Type != application.ChangePodUpdated {
		t.Fatalf("first = %+v", c)
	}
	time.Sleep(50 * time.Millisecond) // several ticks read the old status
	if _, err := r.kube.AppsV1().Deployments(testNS).Update(ctx, rolledOut("1", 1), metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if c := next(t, ch); c.Type != application.ChangeWorkload || c.Status == nil || c.Status.Ready != 1 {
		t.Fatalf("second = %+v", c)
	}
}

func TestWatchNamespaceEvents(t *testing.T) {
	ctx := context.Background()
	r := newRig(t, replicaSet("api-1", ctrl("apps/v1", "Deployment", "api")), rolledOut("1", 1), pod("api-1-a", ctrl("apps/v1", "ReplicaSet", "api-1")))
	ch := startWatch(t, r)

	event := func(uid, typ, reason, apiVersion, kind, name string) {
		t.Helper()
		e := &corev1.Event{
			ObjectMeta:     metav1.ObjectMeta{Name: uid, Namespace: testNS, UID: types.UID(uid)},
			InvolvedObject: corev1.ObjectReference{APIVersion: apiVersion, Kind: kind, Name: name, Namespace: testNS},
			Type:           typ, Reason: reason, Message: reason + " happened", Count: 1,
			LastTimestamp: metav1.NewTime(testNow),
		}
		if _, err := r.kube.CoreV1().Events(testNS).Create(ctx, e, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	event("e1", "Normal", "Pulled", "v1", "Pod", "api-1-a")                     // routine: dropped
	event("e2", "Warning", "BackOff", "v1", "Pod", "unknown")                   // no known pod: dropped
	event("e3", "Normal", "SuccessfulCreate", "apps/v1", "ReplicaSet", "api-1") // notable only for CronJobs
	event("e4", "Warning", "BackOff", "v1", "Pod", "api-1-a")
	c := next(t, ch)
	if c.Type != application.ChangeEvent || c.Workload != apiRef || c.Pod != "api-1-a" || c.Event == nil ||
		c.Event.Reason != "BackOff" || c.Event.Object != "Pod/api-1-a" {
		t.Fatalf("event = %+v", c)
	}

	event("e5", "Normal", "SuccessfulCreate", "batch/v1", "CronJob", "nightly")
	if c := next(t, ch); c.Type != application.ChangeEvent || c.Workload != (domain.WorkloadRef{Kind: domain.KindCronJob, Name: "nightly"}) {
		t.Fatalf("cron event = %+v", c)
	}
	event("e6", "Warning", "FailedCreate", "apps/v1", "ReplicaSet", "api-1")
	if c := next(t, ch); c.Type != application.ChangeEvent || c.Workload != apiRef {
		t.Fatalf("replicaset event = %+v", c)
	}
}

func TestWatchNamespaceRefusesProtected(t *testing.T) {
	r := newRig(t)
	_, err := r.WatchNamespace(context.Background(), actor, "c1", "kube-system")
	wantErr(t, err, application.ErrProtectedNamespace)
}

func TestWatchNamespaceEndsWithContext(t *testing.T) {
	r := newRig(t)
	ctx, cancel := context.WithCancel(context.Background())
	ch, err := r.WatchNamespace(ctx, actor, "c1", testNS)
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case _, ok := <-ch:
		if ok {
			t.Fatal("unexpected change")
		}
	case <-time.After(time.Second):
		t.Fatal("watch did not end")
	}
}
