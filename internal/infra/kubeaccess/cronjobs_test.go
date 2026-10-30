package kubeaccess

import (
	"context"
	"strings"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"

	"tiphys/internal/application"
)

func cronJob(name string, policy batchv1.ConcurrencyPolicy) *batchv1.CronJob {
	return &batchv1.CronJob{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNS, UID: types.UID("cj-uid")},
		Spec: batchv1.CronJobSpec{
			Schedule:          "0 3 * * *",
			ConcurrencyPolicy: policy,
			JobTemplate: batchv1.JobTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels:      map[string]string{"team": "data"},
					Annotations: map[string]string{"note": "from-template", triggeredByKey: "forged"},
				},
				Spec: batchv1.JobSpec{
					BackoffLimit: ptr.To(int32(2)),
					Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
						RestartPolicy: corev1.RestartPolicyNever,
						Containers:    []corev1.Container{{Name: "job", Image: "busybox:1.36", Command: []string{"sh", "-c", "echo hi"}}},
					}},
				},
			},
		},
	}
}

func createdJobs(t *testing.T, r *rig) []batchv1.Job {
	t.Helper()
	l, err := r.kube.BatchV1().Jobs(testNS).List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return l.Items
}

func TestRunCronJobBuildsJobFromTemplateOnly(t *testing.T) {
	cj := cronJob("report", batchv1.AllowConcurrent)
	r := newRig(t, cj)
	name, err := r.RunCronJob(context.Background(), application.KubeActor{Username: "alice"}, "c1", testNS, "report")
	if err != nil {
		t.Fatal(err)
	}
	jobs := createdJobs(t, r)
	if len(jobs) != 1 || jobs[0].Name != name {
		t.Fatalf("jobs = %v, returned %q", jobs, name)
	}
	j := jobs[0]
	if !strings.HasPrefix(name, "report-manual-") || len(name) != len("report-manual-")+5 {
		t.Fatalf("name = %q", name)
	}
	if j.Labels["team"] != "data" || j.Annotations["note"] != "from-template" {
		t.Fatalf("template labels/annotations not copied: %v %v", j.Labels, j.Annotations)
	}
	if j.Annotations[instantiateKey] != "manual" || j.Annotations[triggeredByKey] != "alice" {
		t.Fatalf("marker annotations = %v (the template's forged value must be overwritten)", j.Annotations)
	}
	if len(j.OwnerReferences) != 1 {
		t.Fatalf("owner refs = %v", j.OwnerReferences)
	}
	o := j.OwnerReferences[0]
	if o.Kind != "CronJob" || o.APIVersion != "batch/v1" || o.Name != "report" || o.UID != "cj-uid" ||
		!ptr.Deref(o.Controller, false) || !ptr.Deref(o.BlockOwnerDeletion, false) {
		t.Fatalf("owner ref = %+v", o)
	}
	if j.Spec.Template.Spec.Containers[0].Image != "busybox:1.36" || *j.Spec.BackoffLimit != 2 {
		t.Fatalf("spec was not copied from jobTemplate: %+v", j.Spec)
	}
	if cj.Spec.JobTemplate.Annotations[triggeredByKey] != "forged" {
		t.Fatal("the CronJob object was mutated")
	}
}

func TestManualJobNameFitsLimit(t *testing.T) {
	long := strings.Repeat("a", 52) // longest CronJob name Kubernetes allows
	for _, in := range []string{"report", long, strings.Repeat("a", 60) + "-b", strings.Repeat("a", 49) + "-" + "bbbbbb"} {
		n, err := manualJobName(in)
		if err != nil {
			t.Fatal(err)
		}
		if len(n) > 63 || !strings.Contains(n, "-manual-") || strings.Contains(n, "--") {
			t.Fatalf("name %q (len %d)", n, len(n))
		}
	}
	a, _ := manualJobName("x")
	b, _ := manualJobName("x")
	if a == b {
		t.Fatal("suffix is not random")
	}
}

func TestRunCronJobForbidRefusesWhileJobActive(t *testing.T) {
	owner := ctrl("batch/v1", "CronJob", "report")
	r := newRig(t, cronJob("report", batchv1.ForbidConcurrent), job("report-111", owner, false))
	_, err := r.RunCronJob(context.Background(), actor, "c1", testNS, "report")
	wantErr(t, err, application.ErrAlreadyRunning)
	if n := len(createdJobs(t, r)); n != 1 {
		t.Fatalf("a job was created despite Forbid: %d jobs", n)
	}
}

func TestRunCronJobForbidAllowsWhenOnlyFinishedOrForeignJobs(t *testing.T) {
	r := newRig(t, cronJob("report", batchv1.ForbidConcurrent),
		job("report-old", ctrl("batch/v1", "CronJob", "report"), true),
		job("other-active", ctrl("batch/v1", "CronJob", "other"), false))
	if _, err := r.RunCronJob(context.Background(), actor, "c1", testNS, "report"); err != nil {
		t.Fatal(err)
	}
}

func TestRunCronJobAllowConcurrentIgnoresActiveJobs(t *testing.T) {
	r := newRig(t, cronJob("report", batchv1.AllowConcurrent), job("report-111", ctrl("batch/v1", "CronJob", "report"), false))
	if _, err := r.RunCronJob(context.Background(), actor, "c1", testNS, "report"); err != nil {
		t.Fatal(err)
	}
}

func TestRunCronJobMissingCronJob(t *testing.T) {
	r := newRig(t)
	_, err := r.RunCronJob(context.Background(), actor, "c1", testNS, "nope")
	wantErr(t, err, application.ErrNotFound)
}

func TestNamespaceEventsListsEveryObjectNewestFirst(t *testing.T) {
	ev := func(name, kind, obj string, at time.Time) *corev1.Event {
		return &corev1.Event{
			ObjectMeta:     metav1.ObjectMeta{Name: name, Namespace: testNS},
			InvolvedObject: corev1.ObjectReference{Kind: kind, Name: obj},
			Type:           "Warning", Reason: name, LastTimestamp: metav1.NewTime(at),
		}
	}
	now := time.Now()
	other := ev("other", "Pod", "x", now.Add(-time.Hour))
	other.Namespace = "other-ns"
	r := newRig(t, ev("old", "Pod", "web-1", now.Add(-time.Minute)), ev("new", "ReplicaSet", "web-2", now), other)
	got, err := r.NamespaceEvents(context.Background(), actor, "c1", testNS)
	if err != nil || len(got) != 2 {
		t.Fatalf("got %v, err %v", got, err)
	}
	if got[0].Reason != "new" || got[0].Object != "ReplicaSet/web-2" || got[1].Reason != "old" {
		t.Fatalf("events = %+v", got)
	}
}

func TestListCronJobsFlagsManualJobs(t *testing.T) {
	manual := job("report-manual-abcde", ctrl("batch/v1", "CronJob", "report"), true)
	manual.Annotations = map[string]string{instantiateKey: "manual", triggeredByKey: "alice"}
	r := newRig(t, cronJob("report", batchv1.AllowConcurrent), manual, job("report-2", ctrl("batch/v1", "CronJob", "report"), false))
	got, err := r.ListCronJobs(context.Background(), actor, "c1", testNS)
	if err != nil || len(got) != 1 {
		t.Fatalf("got %v, err %v", got, err)
	}
	cj := got[0]
	if cj.Schedule != "0 3 * * *" || len(cj.RecentJobs) != 2 || cj.ActiveJobs != 1 {
		t.Fatalf("cronjob = %+v", cj)
	}
	var found bool
	for _, j := range cj.RecentJobs {
		if j.Name == manual.Name {
			found = true
			if !j.Manual || j.TriggeredBy != "alice" || j.Status != "succeeded" || j.CronJob != "report" {
				t.Fatalf("manual job = %+v", j)
			}
		}
	}
	if !found {
		t.Fatal("manual job missing")
	}
}

func TestWatchJobRequiresCronJobOwnerAndEndsWhenFinished(t *testing.T) {
	r := newRig(t, job("stray", nil, false), job("report-1", ctrl("batch/v1", "CronJob", "report"), false))
	_, err := r.WatchJob(context.Background(), actor, "c1", testNS, "stray", 1e9)
	wantErr(t, err, application.ErrPodNotInWorkload)

	ch, err := r.WatchJob(context.Background(), actor, "c1", testNS, "report-1", 5e9)
	if err != nil {
		t.Fatal(err)
	}
	first := <-ch
	if first.Status != "running" || first.CronJob != "report" {
		t.Fatalf("first = %+v", first)
	}
	j, _ := r.kube.BatchV1().Jobs(testNS).Get(context.Background(), "report-1", metav1.GetOptions{})
	j.Status.Succeeded = 1
	j.Status.Conditions = []batchv1.JobCondition{{Type: batchv1.JobComplete, Status: corev1.ConditionTrue}}
	if _, err := r.kube.BatchV1().Jobs(testNS).UpdateStatus(context.Background(), j, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	var last application.JobInfo
	for s := range ch {
		last = s
	}
	if last.Status != "succeeded" {
		t.Fatalf("last = %+v", last)
	}
}
