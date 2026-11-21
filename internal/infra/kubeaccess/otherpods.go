package kubeaccess

import (
	"context"
	"errors"
	"fmt"
	"sort"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"tiphys/internal/application"
	"tiphys/internal/domain"
)

// controllerOfFunc reads the controller of a pod's ReplicaSet or Job; nil
// when the object or its controller is missing.
type controllerOfFunc func(gk schema.GroupKind, name string) (*metav1.OwnerReference, error)

// workloadOf resolves the workload a pod's owner chain leads to, like
// podBelongs, from owner references only: Pod -> StatefulSet or DaemonSet,
// Pod -> ReplicaSet -> Deployment or Rollout, Pod -> Job -> CronJob. ok is
// false for a pod no workload owns.
func workloadOf(p *corev1.Pod, controllerOf controllerOfFunc) (ref domain.WorkloadRef, ok bool, err error) {
	o := metav1.GetControllerOf(p)
	if o == nil {
		return domain.WorkloadRef{}, false, nil
	}
	via := func(gk schema.GroupKind, kinds ...domain.WorkloadKind) (domain.WorkloadRef, bool, error) {
		owner, err := controllerOf(gk, o.Name)
		if err != nil || owner == nil {
			return domain.WorkloadRef{}, false, err
		}
		ref, ok := directRef(owner.APIVersion, owner.Kind, owner.Name, kinds...)
		return ref, ok, nil
	}
	switch {
	case controlledBy(p, replicaSetGK, o.Name):
		return via(replicaSetGK, domain.KindDeployment, domain.KindRollout)
	case controlledBy(p, jobGK, o.Name):
		return via(jobGK, domain.KindCronJob)
	}
	ref, ok = directRef(o.APIVersion, o.Kind, o.Name, domain.KindStatefulSet, domain.KindDaemonSet)
	return ref, ok, nil
}

// podOwnerOf is the pod's controller, nil when it has none.
func podOwnerOf(p *corev1.Pod) *application.PodOwner {
	o := metav1.GetControllerOf(p)
	if o == nil {
		return nil
	}
	gv, _ := schema.ParseGroupVersion(o.APIVersion)
	return &application.PodOwner{Group: gv.Group, Kind: o.Kind, Name: o.Name}
}

// ListOtherPods lists the namespace's pods whose owner chain leads to no
// workload (operator-managed pods such as CloudNativePG instances, bare
// pods, Jobs no CronJob owns), sorted by owner and name.
func (a *Access) ListOtherPods(ctx context.Context, actor application.KubeActor, cluster, namespace string) ([]application.OtherPod, error) {
	c, err := a.open(actor, cluster, namespace)
	if err != nil {
		return nil, err
	}
	ctx, cancel := callCtx(ctx)
	defer cancel()
	pods, err := c.kube.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, a.fail("list pods", cluster, err)
	}
	controllers := map[string]*metav1.OwnerReference{} // "ReplicaSet/x", "Job/x"
	rss, err := c.kube.AppsV1().ReplicaSets(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, a.fail("list replicasets", cluster, err)
	}
	for i := range rss.Items {
		controllers[replicaSetGK.Kind+"/"+rss.Items[i].Name] = metav1.GetControllerOf(&rss.Items[i])
	}
	jobs, err := c.kube.BatchV1().Jobs(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, a.fail("list jobs", cluster, err)
	}
	for i := range jobs.Items {
		controllers[jobGK.Kind+"/"+jobs.Items[i].Name] = metav1.GetControllerOf(&jobs.Items[i])
	}
	lookup := func(gk schema.GroupKind, name string) (*metav1.OwnerReference, error) {
		return controllers[gk.Kind+"/"+name], nil
	}
	var out []application.OtherPod
	for i := range pods.Items {
		p := &pods.Items[i]
		if _, owned, _ := workloadOf(p, lookup); !owned {
			out = append(out, application.OtherPod{PodInfo: podInfo(p), Owner: podOwnerOf(p)})
		}
	}
	key := func(o application.OtherPod) string {
		if o.Owner == nil {
			return ""
		}
		return o.Owner.Kind + "." + o.Owner.Group + "/" + o.Owner.Name
	}
	sort.Slice(out, func(i, j int) bool {
		if ki, kj := key(out[i]), key(out[j]); ki != kj {
			return ki < kj
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

// verifyOtherPod reads the pod and checks that no workload owns it.
func (a *Access) verifyOtherPod(ctx context.Context, c *clients, cluster, namespace, name string) (*corev1.Pod, error) {
	p, err := c.kube.CoreV1().Pods(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, a.fail("get pod", cluster, err)
	}
	ref, owned, err := workloadOf(p, func(gk schema.GroupKind, name string) (*metav1.OwnerReference, error) {
		var obj metav1.Object
		var gerr error
		if gk == replicaSetGK {
			obj, gerr = c.kube.AppsV1().ReplicaSets(namespace).Get(ctx, name, metav1.GetOptions{})
		} else {
			obj, gerr = c.kube.BatchV1().Jobs(namespace).Get(ctx, name, metav1.GetOptions{})
		}
		if gerr = a.fail("get "+gk.Kind, cluster, gerr); gerr != nil {
			if errors.Is(gerr, application.ErrNotFound) {
				return nil, nil
			}
			return nil, gerr
		}
		return metav1.GetControllerOf(obj), nil
	})
	if err != nil {
		return nil, err
	}
	if owned {
		return nil, fmt.Errorf("%w: %s/%s is owned by %s", application.ErrPodHasWorkload, namespace, name, ref)
	}
	return p, nil
}
