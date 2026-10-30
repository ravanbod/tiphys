package kubeaccess

import (
	"context"
	"crypto/rand"
	"fmt"
	"maps"
	"math/big"
	"sort"
	"strings"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	"tiphys/internal/application"
	"tiphys/internal/domain"
)

const (
	recentJobsPerCronJob = 5
	maxNameLen           = 63
	nameSuffixLen        = 5
	nameAlphabet         = "abcdefghijklmnopqrstuvwxyz0123456789"
	createAttempts       = 3
)

// ListCronJobs lists the namespace's CronJobs with their most recent Jobs.
func (a *Access) ListCronJobs(ctx context.Context, actor application.KubeActor, cluster, namespace string) ([]application.CronJobInfo, error) {
	c, err := a.open(actor, cluster, namespace)
	if err != nil {
		return nil, err
	}
	ctx, cancel := callCtx(ctx)
	defer cancel()
	cjs, err := c.kube.BatchV1().CronJobs(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, a.fail("list cronjobs", cluster, err)
	}
	jobs, err := c.kube.BatchV1().Jobs(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, a.fail("list jobs", cluster, err)
	}
	out := make([]application.CronJobInfo, 0, len(cjs.Items))
	for i := range cjs.Items {
		cj := &cjs.Items[i]
		info := application.CronJobInfo{WorkloadInfo: cronJobInfo(cj), LastScheduleAt: timePtr(ptr.Deref(cj.Status.LastScheduleTime, metav1.Time{}))}
		owned := jobsOf(jobs.Items, cj.Name)
		sort.Slice(owned, func(i, j int) bool { return owned[i].CreationTimestamp.After(owned[j].CreationTimestamp.Time) })
		for k := range owned {
			ji := jobInfo(&owned[k])
			if ji.Status == "running" {
				info.ActiveJobs++
			}
			if k < recentJobsPerCronJob {
				info.RecentJobs = append(info.RecentJobs, ji)
			}
		}
		if len(info.RecentJobs) > 0 {
			info.LastResult = info.RecentJobs[0].Status
		}
		out = append(out, info)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Ref.Name < out[j].Ref.Name })
	return out, nil
}

// RunCronJob creates a Job from the CronJob's jobTemplate, the way
// kubectl create job --from does, and from nothing else.
func (a *Access) RunCronJob(ctx context.Context, actor application.KubeActor, cluster, namespace, cronJob string) (string, error) {
	if err := validObjectName("cronjob", cronJob); err != nil {
		return "", err
	}
	c, err := a.open(actor, cluster, namespace)
	if err != nil {
		return "", err
	}
	ctx, cancel := callCtx(ctx)
	defer cancel()

	cj, err := c.kube.BatchV1().CronJobs(namespace).Get(ctx, cronJob, metav1.GetOptions{})
	if err != nil {
		return "", a.fail("get cronjob", cluster, err)
	}
	if cj.Spec.ConcurrencyPolicy == batchv1.ForbidConcurrent {
		jobs, err := c.kube.BatchV1().Jobs(namespace).List(ctx, metav1.ListOptions{})
		if err != nil {
			return "", a.fail("list jobs", cluster, err)
		}
		for _, j := range jobsOf(jobs.Items, cj.Name) {
			if jobStatus(&j) == "running" {
				return "", application.ErrAlreadyRunning
			}
		}
	}
	for range createAttempts {
		name, err := manualJobName(cj.Name)
		if err != nil {
			return "", a.fail("generate job name", cluster, err)
		}
		job := buildJob(cj, name, actor.Username)
		_, err = c.kube.BatchV1().Jobs(namespace).Create(ctx, job, metav1.CreateOptions{})
		if apierrors.IsAlreadyExists(err) {
			continue
		}
		if err != nil {
			return "", a.fail("create job", cluster, err)
		}
		return name, nil
	}
	return "", a.fail("create job", cluster, fmt.Errorf("could not find a free job name"))
}

// buildJob copies spec.jobTemplate and adds the manual markers and the owner.
func buildJob(cj *batchv1.CronJob, name, username string) *batchv1.Job {
	tmpl := cj.Spec.JobTemplate.DeepCopy()
	annotations := maps.Clone(tmpl.Annotations)
	if annotations == nil {
		annotations = map[string]string{}
	}
	annotations[instantiateKey] = instantiateManual
	annotations[triggeredByKey] = username
	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Namespace:   cj.Namespace,
			Labels:      maps.Clone(tmpl.Labels),
			Annotations: annotations,
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion:         "batch/v1",
				Kind:               "CronJob",
				Name:               cj.Name,
				UID:                cj.UID,
				Controller:         ptr.To(true),
				BlockOwnerDeletion: ptr.To(true),
			}},
		},
		Spec: tmpl.Spec,
	}
}

// manualJobName returns <cronjob>-manual-<5 random chars>, shortening the
// CronJob part so the whole name fits 63 characters.
func manualJobName(cronJob string) (string, error) {
	suffix := make([]byte, nameSuffixLen)
	for i := range suffix {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(nameAlphabet))))
		if err != nil {
			return "", err
		}
		suffix[i] = nameAlphabet[n.Int64()]
	}
	const mid = "-manual-"
	prefix := cronJob
	if max := maxNameLen - len(mid) - nameSuffixLen; len(prefix) > max {
		prefix = strings.TrimRight(prefix[:max], "-.")
	}
	return prefix + mid + string(suffix), nil
}

// WatchJob polls a Job and emits its status on every change until it
// finishes, ctx is done or the timeout passes. Only Jobs owned by a CronJob
// can be watched; JobInfo.CronJob names it so the caller can match its
// authorization.
func (a *Access) WatchJob(ctx context.Context, actor application.KubeActor, cluster, namespace, job string, timeout time.Duration) (<-chan application.JobInfo, error) {
	if err := validObjectName("job", job); err != nil {
		return nil, err
	}
	c, err := a.open(actor, cluster, namespace)
	if err != nil {
		return nil, err
	}
	first, err := a.jobSnapshot(ctx, c, cluster, namespace, job)
	if err != nil {
		return nil, err
	}
	out := make(chan application.JobInfo, 8)
	go func() {
		defer close(out)
		deadline := time.NewTimer(timeout)
		defer deadline.Stop()
		tick := time.NewTicker(a.pollEvery)
		defer tick.Stop()
		cur, errs := first, 0
		send := func(j application.JobInfo) bool {
			select {
			case out <- j:
				return true
			case <-ctx.Done():
				return false
			}
		}
		if !send(cur) || cur.Status != "running" {
			return
		}
		for {
			select {
			case <-ctx.Done():
				return
			case <-deadline.C:
				return
			case <-tick.C:
			}
			j, err := a.jobSnapshot(ctx, c, cluster, namespace, job)
			if err != nil {
				if errs++; errs >= maxPollErrors {
					cur.Status = "failed"
					send(cur)
					return
				}
				continue
			}
			errs = 0
			if !sameJob(j, cur) {
				cur = j
				if !send(cur) {
					return
				}
			}
			if cur.Status != "running" {
				return
			}
		}
	}()
	return out, nil
}

func (a *Access) jobSnapshot(ctx context.Context, c *clients, cluster, namespace, name string) (application.JobInfo, error) {
	ctx, cancel := callCtx(ctx)
	defer cancel()
	j, err := c.kube.BatchV1().Jobs(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return application.JobInfo{}, a.fail("get job", cluster, err)
	}
	if o := metav1.GetControllerOf(j); o == nil || !controlledBy(j, workloadGK(domain.KindCronJob), o.Name) {
		return application.JobInfo{}, fmt.Errorf("%w: job %s is not owned by a CronJob", application.ErrPodNotInWorkload, name)
	}
	info := jobInfo(j)
	pods, err := c.kube.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{LabelSelector: "job-name=" + name})
	if err != nil {
		return application.JobInfo{}, a.fail("list job pods", cluster, err)
	}
	for i := range pods.Items {
		if controlledBy(&pods.Items[i], jobGK, name) {
			info.PodNames = append(info.PodNames, pods.Items[i].Name)
		}
	}
	sort.Strings(info.PodNames)
	return info, nil
}

func sameJob(a, b application.JobInfo) bool {
	return a.Status == b.Status && a.Active == b.Active && a.Succeeded == b.Succeeded && a.Failed == b.Failed &&
		strings.Join(a.PodNames, ",") == strings.Join(b.PodNames, ",")
}

func jobStatus(j *batchv1.Job) string {
	for _, c := range j.Status.Conditions {
		if c.Status != corev1.ConditionTrue {
			continue
		}
		switch c.Type {
		case batchv1.JobComplete, batchv1.JobSuccessCriteriaMet:
			return "succeeded"
		case batchv1.JobFailed:
			return "failed"
		}
	}
	return "running"
}

func jobInfo(j *batchv1.Job) application.JobInfo {
	info := application.JobInfo{
		Name:        j.Name,
		TriggeredBy: j.Annotations[triggeredByKey],
		Manual:      j.Annotations[instantiateKey] == instantiateManual,
		Active:      j.Status.Active,
		Succeeded:   j.Status.Succeeded,
		Failed:      j.Status.Failed,
		Completions: ptr.Deref(j.Spec.Completions, 1),
		Status:      jobStatus(j),
	}
	if o := metav1.GetControllerOf(j); o != nil && o.Kind == "CronJob" {
		info.CronJob = o.Name
	}
	if j.Status.StartTime != nil {
		info.StartedAt = timePtr(*j.Status.StartTime)
	} else {
		info.StartedAt = timePtr(j.CreationTimestamp)
	}
	if j.Status.CompletionTime != nil {
		info.FinishedAt = timePtr(*j.Status.CompletionTime)
	}
	if info.Status != "running" && info.FinishedAt == nil {
		for _, c := range j.Status.Conditions {
			if c.Status == corev1.ConditionTrue {
				info.FinishedAt = timePtr(c.LastTransitionTime)
			}
		}
	}
	return info
}
