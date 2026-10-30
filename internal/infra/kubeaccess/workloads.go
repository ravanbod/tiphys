package kubeaccess

import (
	"context"
	"sort"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"tiphys/internal/application"
	"tiphys/internal/domain"
)

var rolloutGVR = schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "rollouts"}

// ListWorkloads lists Deployments, StatefulSets, DaemonSets, Argo Rollouts
// and CronJobs of a namespace. A cluster without the Rollout CRD has none.
func (a *Access) ListWorkloads(ctx context.Context, actor application.KubeActor, cluster, namespace string) ([]application.WorkloadInfo, error) {
	c, err := a.open(actor, cluster, namespace)
	if err != nil {
		return nil, err
	}
	ctx, cancel := callCtx(ctx)
	defer cancel()

	var out []application.WorkloadInfo
	deps, err := c.kube.AppsV1().Deployments(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, a.fail("list deployments", cluster, err)
	}
	for i := range deps.Items {
		out = append(out, deploymentInfo(&deps.Items[i]))
	}
	sts, err := c.kube.AppsV1().StatefulSets(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, a.fail("list statefulsets", cluster, err)
	}
	for i := range sts.Items {
		out = append(out, statefulSetInfo(&sts.Items[i]))
	}
	dss, err := c.kube.AppsV1().DaemonSets(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, a.fail("list daemonsets", cluster, err)
	}
	for i := range dss.Items {
		out = append(out, daemonSetInfo(&dss.Items[i]))
	}
	rollouts, err := c.dyn.Resource(rolloutGVR).Namespace(namespace).List(ctx, metav1.ListOptions{})
	switch {
	case apierrors.IsNotFound(err):
		// Argo Rollouts is not installed on this cluster.
	case err != nil:
		return nil, a.fail("list rollouts", cluster, err)
	default:
		for i := range rollouts.Items {
			out = append(out, rolloutInfo(&rollouts.Items[i]))
		}
	}
	cjs, err := c.kube.BatchV1().CronJobs(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, a.fail("list cronjobs", cluster, err)
	}
	for i := range cjs.Items {
		out = append(out, cronJobInfo(&cjs.Items[i]))
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Ref.Kind != out[j].Ref.Kind {
			return out[i].Ref.Kind < out[j].Ref.Kind
		}
		return out[i].Ref.Name < out[j].Ref.Name
	})
	return out, nil
}

func (a *Access) GetWorkload(ctx context.Context, actor application.KubeActor, w application.KubeWorkloadTarget) (application.WorkloadInfo, error) {
	c, err := a.openWorkload(actor, w)
	if err != nil {
		return application.WorkloadInfo{}, err
	}
	ctx, cancel := callCtx(ctx)
	defer cancel()
	return a.getWorkload(ctx, c, w)
}

func (a *Access) getWorkload(ctx context.Context, c *clients, w application.KubeWorkloadTarget) (application.WorkloadInfo, error) {
	ns, name := w.Namespace, w.Workload.Name
	switch w.Workload.Kind {
	case domain.KindDeployment:
		o, err := c.kube.AppsV1().Deployments(ns).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return application.WorkloadInfo{}, a.fail("get deployment", w.Cluster, err)
		}
		return deploymentInfo(o), nil
	case domain.KindStatefulSet:
		o, err := c.kube.AppsV1().StatefulSets(ns).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return application.WorkloadInfo{}, a.fail("get statefulset", w.Cluster, err)
		}
		return statefulSetInfo(o), nil
	case domain.KindDaemonSet:
		o, err := c.kube.AppsV1().DaemonSets(ns).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return application.WorkloadInfo{}, a.fail("get daemonset", w.Cluster, err)
		}
		return daemonSetInfo(o), nil
	case domain.KindRollout:
		o, err := c.dyn.Resource(rolloutGVR).Namespace(ns).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return application.WorkloadInfo{}, a.fail("get rollout", w.Cluster, err)
		}
		return rolloutInfo(o), nil
	default: // CronJob; the kind was validated by the caller
		o, err := c.kube.BatchV1().CronJobs(ns).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return application.WorkloadInfo{}, a.fail("get cronjob", w.Cluster, err)
		}
		return cronJobInfo(o), nil
	}
}

func replicasOrOne(p *int32) int32 {
	if p == nil {
		return 1
	}
	return *p
}

func images(spec *corev1.PodSpec) []string {
	out := make([]string, 0, len(spec.Containers))
	for _, c := range spec.Containers {
		out = append(out, c.Image)
	}
	return out
}

func timePtr(t metav1.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	v := t.Time
	return &v
}

func parseRFC3339(s string) *time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return nil
	}
	return &t
}

func deploymentInfo(d *appsv1.Deployment) application.WorkloadInfo {
	info := application.WorkloadInfo{
		Ref:       domain.WorkloadRef{Kind: domain.KindDeployment, Name: d.Name},
		Namespace: d.Namespace,
		Desired:   replicasOrOne(d.Spec.Replicas),
		Ready:     d.Status.ReadyReplicas,
		Updated:   d.Status.UpdatedReplicas,
		Available: d.Status.AvailableReplicas,
		Strategy:  string(d.Spec.Strategy.Type),
		Images:    images(&d.Spec.Template.Spec),
		CreatedAt: d.CreationTimestamp.Time,
	}
	for _, c := range d.Status.Conditions {
		if c.Type == appsv1.DeploymentProgressing {
			info.LastRolloutAt = timePtr(c.LastUpdateTime)
		}
	}
	return info
}

func statefulSetInfo(s *appsv1.StatefulSet) application.WorkloadInfo {
	return application.WorkloadInfo{
		Ref:           domain.WorkloadRef{Kind: domain.KindStatefulSet, Name: s.Name},
		Namespace:     s.Namespace,
		Desired:       replicasOrOne(s.Spec.Replicas),
		Ready:         s.Status.ReadyReplicas,
		Updated:       s.Status.UpdatedReplicas,
		Available:     s.Status.AvailableReplicas,
		Strategy:      string(s.Spec.UpdateStrategy.Type),
		Images:        images(&s.Spec.Template.Spec),
		CreatedAt:     s.CreationTimestamp.Time,
		LastRolloutAt: parseRFC3339(s.Spec.Template.Annotations[restartAnnotation]),
	}
}

func daemonSetInfo(d *appsv1.DaemonSet) application.WorkloadInfo {
	return application.WorkloadInfo{
		Ref:           domain.WorkloadRef{Kind: domain.KindDaemonSet, Name: d.Name},
		Namespace:     d.Namespace,
		Desired:       d.Status.DesiredNumberScheduled,
		Ready:         d.Status.NumberReady,
		Updated:       d.Status.UpdatedNumberScheduled,
		Available:     d.Status.NumberAvailable,
		Strategy:      string(d.Spec.UpdateStrategy.Type),
		Images:        images(&d.Spec.Template.Spec),
		CreatedAt:     d.CreationTimestamp.Time,
		LastRolloutAt: parseRFC3339(d.Spec.Template.Annotations[restartAnnotation]),
	}
}

func cronJobInfo(c *batchv1.CronJob) application.WorkloadInfo {
	info := application.WorkloadInfo{
		Ref:               domain.WorkloadRef{Kind: domain.KindCronJob, Name: c.Name},
		Namespace:         c.Namespace,
		Images:            images(&c.Spec.JobTemplate.Spec.Template.Spec),
		CreatedAt:         c.CreationTimestamp.Time,
		Schedule:          c.Spec.Schedule,
		ConcurrencyPolicy: string(c.Spec.ConcurrencyPolicy),
	}
	if c.Spec.Suspend != nil {
		info.Suspended = *c.Spec.Suspend
	}
	if info.ConcurrencyPolicy == "" {
		info.ConcurrencyPolicy = string(batchv1.AllowConcurrent)
	}
	return info
}

func rolloutInfo(o *unstructured.Unstructured) application.WorkloadInfo {
	info := application.WorkloadInfo{
		Ref:       domain.WorkloadRef{Kind: domain.KindRollout, Name: o.GetName()},
		Namespace: o.GetNamespace(),
		Desired:   rolloutDesired(o),
		Ready:     int32(nestedInt(o, "status", "readyReplicas")),
		Updated:   int32(nestedInt(o, "status", "updatedReplicas")),
		Available: int32(nestedInt(o, "status", "availableReplicas")),
		CreatedAt: o.GetCreationTimestamp().Time,
	}
	if _, ok, _ := unstructured.NestedMap(o.Object, "spec", "strategy", "canary"); ok {
		info.Strategy = "Canary"
	} else if _, ok, _ := unstructured.NestedMap(o.Object, "spec", "strategy", "blueGreen"); ok {
		info.Strategy = "BlueGreen"
	}
	containers, _, _ := unstructured.NestedSlice(o.Object, "spec", "template", "spec", "containers")
	for _, c := range containers {
		if m, ok := c.(map[string]any); ok {
			if img, ok := m["image"].(string); ok {
				info.Images = append(info.Images, img)
			}
		}
	}
	if s, ok, _ := unstructured.NestedString(o.Object, "status", "restartedAt"); ok {
		info.LastRolloutAt = parseRFC3339(s)
	}
	return info
}

func nestedInt(o *unstructured.Unstructured, fields ...string) int64 {
	v, _, _ := unstructured.NestedInt64(o.Object, fields...)
	return v
}

func rolloutDesired(o *unstructured.Unstructured) int32 {
	if v, ok, _ := unstructured.NestedInt64(o.Object, "spec", "replicas"); ok {
		return int32(v)
	}
	return 1
}
