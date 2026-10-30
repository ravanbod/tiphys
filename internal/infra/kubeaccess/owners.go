package kubeaccess

import (
	"context"
	"fmt"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"tiphys/internal/application"
	"tiphys/internal/domain"
)

// workloadGK is the group and kind a workload's ownerReference carries.
func workloadGK(k domain.WorkloadKind) schema.GroupKind {
	switch k {
	case domain.KindRollout:
		return schema.GroupKind{Group: "argoproj.io", Kind: "Rollout"}
	case domain.KindCronJob:
		return schema.GroupKind{Group: "batch", Kind: "CronJob"}
	default:
		return schema.GroupKind{Group: "apps", Kind: string(k)}
	}
}

// controlledBy reports whether obj's controller owner is group/kind with the name.
func controlledBy(obj metav1.Object, gk schema.GroupKind, name string) bool {
	ref := metav1.GetControllerOf(obj)
	if ref == nil || ref.Kind != gk.Kind || ref.Name != name {
		return false
	}
	gv, err := schema.ParseGroupVersion(ref.APIVersion)
	return err == nil && gv.Group == gk.Group
}

var (
	replicaSetGK = schema.GroupKind{Group: "apps", Kind: "ReplicaSet"}
	jobGK        = schema.GroupKind{Group: "batch", Kind: "Job"}
)

// verifyPod re-reads the pod and checks that its owner chain leads to the
// workload: Pod -> ReplicaSet -> Deployment/Rollout, Pod -> StatefulSet,
// Pod -> DaemonSet, Pod -> Job -> CronJob. It returns the pod it read.
func (a *Access) verifyPod(ctx context.Context, c *clients, w application.KubeWorkloadTarget, podName string) (*corev1.Pod, error) {
	if err := validObjectName("pod", podName); err != nil {
		return nil, err
	}
	pod, err := c.kube.CoreV1().Pods(w.Namespace).Get(ctx, podName, metav1.GetOptions{})
	if err != nil {
		return nil, a.fail("get pod", w.Cluster, err)
	}
	if err := a.podBelongs(ctx, c, w, pod); err != nil {
		return nil, err
	}
	return pod, nil
}

func (a *Access) podBelongs(ctx context.Context, c *clients, w application.KubeWorkloadTarget, pod *corev1.Pod) error {
	ref, gk := w.Workload, workloadGK(w.Workload.Kind)
	notIn := fmt.Errorf("%w: %s/%s is not owned by %s", application.ErrPodNotInWorkload, w.Namespace, pod.Name, ref)
	switch ref.Kind {
	case domain.KindStatefulSet, domain.KindDaemonSet:
		if controlledBy(pod, gk, ref.Name) {
			return nil
		}
	case domain.KindDeployment, domain.KindRollout:
		owner := metav1.GetControllerOf(pod)
		if owner == nil || owner.Kind != replicaSetGK.Kind || !controlledBy(pod, replicaSetGK, owner.Name) {
			return notIn
		}
		rs, err := c.kube.AppsV1().ReplicaSets(w.Namespace).Get(ctx, owner.Name, metav1.GetOptions{})
		if err != nil {
			return a.fail("get replicaset", w.Cluster, err)
		}
		if controlledBy(rs, gk, ref.Name) {
			return nil
		}
	case domain.KindCronJob:
		owner := metav1.GetControllerOf(pod)
		if owner == nil || owner.Kind != jobGK.Kind || !controlledBy(pod, jobGK, owner.Name) {
			return notIn
		}
		job, err := c.kube.BatchV1().Jobs(w.Namespace).Get(ctx, owner.Name, metav1.GetOptions{})
		if err != nil {
			return a.fail("get job", w.Cluster, err)
		}
		if controlledBy(job, gk, ref.Name) {
			return nil
		}
	}
	return notIn
}

// ownedPods lists the pods in the namespace and keeps those whose owner
// chain leads to the workload. Filtering happens here, not in a selector, so
// no label selector is ever derived from workload contents.
func (a *Access) ownedPods(ctx context.Context, c *clients, w application.KubeWorkloadTarget) ([]corev1.Pod, error) {
	ref, gk := w.Workload, workloadGK(w.Workload.Kind)
	pods, err := c.kube.CoreV1().Pods(w.Namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, a.fail("list pods", w.Cluster, err)
	}
	var intermediate map[string]bool // owned ReplicaSets or Jobs
	var intermediateGK schema.GroupKind
	switch ref.Kind {
	case domain.KindDeployment, domain.KindRollout:
		rss, err := c.kube.AppsV1().ReplicaSets(w.Namespace).List(ctx, metav1.ListOptions{})
		if err != nil {
			return nil, a.fail("list replicasets", w.Cluster, err)
		}
		intermediate, intermediateGK = map[string]bool{}, replicaSetGK
		for i := range rss.Items {
			if controlledBy(&rss.Items[i], gk, ref.Name) {
				intermediate[rss.Items[i].Name] = true
			}
		}
	case domain.KindCronJob:
		jobs, err := c.kube.BatchV1().Jobs(w.Namespace).List(ctx, metav1.ListOptions{})
		if err != nil {
			return nil, a.fail("list jobs", w.Cluster, err)
		}
		intermediate, intermediateGK = map[string]bool{}, jobGK
		for i := range jobs.Items {
			if controlledBy(&jobs.Items[i], gk, ref.Name) {
				intermediate[jobs.Items[i].Name] = true
			}
		}
	}
	var out []corev1.Pod
	for i := range pods.Items {
		p := &pods.Items[i]
		if intermediate == nil {
			if controlledBy(p, gk, ref.Name) {
				out = append(out, *p)
			}
			continue
		}
		if o := metav1.GetControllerOf(p); o != nil && intermediate[o.Name] && controlledBy(p, intermediateGK, o.Name) {
			out = append(out, *p)
		}
	}
	return out, nil
}

// jobsOf lists the Jobs a CronJob owns.
func jobsOf(jobs []batchv1.Job, cronJob string) []batchv1.Job {
	var out []batchv1.Job
	for i := range jobs {
		if controlledBy(&jobs[i], workloadGK(domain.KindCronJob), cronJob) {
			out = append(out, jobs[i])
		}
	}
	return out
}
