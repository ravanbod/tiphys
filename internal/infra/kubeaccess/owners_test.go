package kubeaccess

import (
	"context"
	"io"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	"tiphys/internal/application"
	"tiphys/internal/domain"
)

func ownerFixtures() []runtime.Object {
	return []runtime.Object{
		deployment("web"), deployment("other"),
		replicaSet("web-abc", ctrl("apps/v1", "Deployment", "web")),
		replicaSet("other-abc", ctrl("apps/v1", "Deployment", "other")),
		replicaSet("canary-abc", ctrl("argoproj.io/v1alpha1", "Rollout", "canary")),
		replicaSet("fake-owner", ctrl("example.com/v1", "Deployment", "web")), // same name, wrong group
		pod("web-abc-1", ctrl("apps/v1", "ReplicaSet", "web-abc")),
		pod("web-abc-2", ctrl("apps/v1", "ReplicaSet", "web-abc")),
		pod("other-abc-1", ctrl("apps/v1", "ReplicaSet", "other-abc")),
		pod("canary-abc-1", ctrl("apps/v1", "ReplicaSet", "canary-abc")),
		pod("orphan", nil),
		pod("db-0", ctrl("apps/v1", "StatefulSet", "db")),
		pod("agent-x", ctrl("apps/v1", "DaemonSet", "agent")),
		pod("report-1-p", ctrl("batch/v1", "Job", "report-1")),
		pod("other-job-p", ctrl("batch/v1", "Job", "stray-1")),
		pod("sneaky", ctrl("apps/v1", "ReplicaSet", "fake-owner")),
		pod("wrong-group", ctrl("example.com/v1", "StatefulSet", "db")),
		job("report-1", ctrl("batch/v1", "CronJob", "report"), false),
		job("stray-1", nil, false),
	}
}

func TestPodOwnerChainVerification(t *testing.T) {
	r := newRig(t, ownerFixtures()...)
	cases := []struct {
		name string
		w    application.KubeWorkloadTarget
		pod  string
		ok   bool
	}{
		{"deployment pod", target(domain.KindDeployment, "web"), "web-abc-1", true},
		{"pod of another deployment", target(domain.KindDeployment, "web"), "other-abc-1", false},
		{"pod without owner", target(domain.KindDeployment, "web"), "orphan", false},
		{"replicaset owned by lookalike group", target(domain.KindDeployment, "web"), "sneaky", false},
		{"rollout pod", target(domain.KindRollout, "canary"), "canary-abc-1", true},
		{"rollout pod claimed as deployment", target(domain.KindDeployment, "canary"), "canary-abc-1", false},
		{"deployment pod claimed as rollout", target(domain.KindRollout, "web"), "web-abc-1", false},
		{"statefulset pod", target(domain.KindStatefulSet, "db"), "db-0", true},
		{"statefulset pod as other name", target(domain.KindStatefulSet, "db2"), "db-0", false},
		{"statefulset kind claimed as daemonset", target(domain.KindDaemonSet, "db"), "db-0", false},
		{"owner of wrong group", target(domain.KindStatefulSet, "db"), "wrong-group", false},
		{"daemonset pod", target(domain.KindDaemonSet, "agent"), "agent-x", true},
		{"cronjob pod via job", target(domain.KindCronJob, "report"), "report-1-p", true},
		{"cronjob pod of a job without cronjob", target(domain.KindCronJob, "report"), "other-job-p", false},
		{"cronjob claimed for deployment pod", target(domain.KindCronJob, "web"), "web-abc-1", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cl, err := r.open(actor, c.w.Cluster, c.w.Namespace)
			if err != nil {
				t.Fatal(err)
			}
			_, err = r.verifyPod(context.Background(), cl, c.w, c.pod)
			if c.ok && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !c.ok {
				wantErr(t, err, application.ErrPodNotInWorkload)
			}
		})
	}
}

func TestPodVerbsRefuseMismatchedPod(t *testing.T) {
	r := newRig(t, ownerFixtures()...)
	ctx := context.Background()
	w := target(domain.KindDeployment, "web")
	_, err := r.Logs(ctx, actor, w, "other-abc-1", application.LogOptions{Container: "app", TailLines: 100})
	wantErr(t, err, application.ErrPodNotInWorkload)
	_, err = r.Exec(ctx, actor, w, "other-abc-1", "app", "/bin/sh", 80, 24)
	wantErr(t, err, application.ErrPodNotInWorkload)
	_, err = r.Logs(ctx, actor, w, "no-such-pod", application.LogOptions{Container: "app", TailLines: 100})
	wantErr(t, err, application.ErrNotFound)
}

func TestLogsOpensStreamForOwnedPod(t *testing.T) {
	r := newRig(t, ownerFixtures()...)
	rc, err := r.Logs(context.Background(), actor, target(domain.KindDeployment, "web"), "web-abc-1",
		application.LogOptions{Container: "app", TailLines: 100, SinceSeconds: 300, Timestamps: true})
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	if b, _ := io.ReadAll(rc); len(b) == 0 {
		t.Fatal("empty log stream")
	}
	_, err = r.Logs(context.Background(), actor, target(domain.KindDeployment, "web"), "web-abc-1",
		application.LogOptions{Container: "sidecar", TailLines: 100})
	wantErr(t, err, application.ErrInvalidName)
}

func TestListPodsFiltersByOwnerChain(t *testing.T) {
	r := newRig(t, append(ownerFixtures(), &appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Name: "db", Namespace: testNS}},
		&batchv1.CronJob{ObjectMeta: metav1.ObjectMeta{Name: "report", Namespace: testNS}})...)
	names := func(w application.KubeWorkloadTarget) []string {
		pods, err := r.ListPods(context.Background(), actor, w)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, p := range pods {
			out = append(out, p.Name)
		}
		return out
	}
	if got := names(target(domain.KindDeployment, "web")); len(got) != 2 || got[0] != "web-abc-1" || got[1] != "web-abc-2" {
		t.Fatalf("deployment pods = %v", got)
	}
	if got := names(target(domain.KindStatefulSet, "db")); len(got) != 1 || got[0] != "db-0" {
		t.Fatalf("statefulset pods = %v", got)
	}
	if got := names(target(domain.KindCronJob, "report")); len(got) != 1 || got[0] != "report-1-p" {
		t.Fatalf("cronjob pods = %v", got)
	}
}
