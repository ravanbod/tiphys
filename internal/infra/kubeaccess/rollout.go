package kubeaccess

import (
	"context"
	"fmt"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"tiphys/internal/application"
	"tiphys/internal/domain"
)

const maxPollErrors = 3

// WatchRollout polls the workload and emits a status whenever it changes,
// until the rollout completes, fails, ctx is done or the deadline passes. At
// the deadline a last status with Message "deadline exceeded" is sent (neither
// Complete nor Failed); a channel that closes without Complete or Failed
// therefore means timed out or cancelled. A zero deadline means the
// workload's progressDeadlineSeconds, or 600 seconds.
func (a *Access) WatchRollout(ctx context.Context, actor application.KubeActor, w application.KubeWorkloadTarget, deadline time.Duration) (<-chan application.RolloutStatus, error) {
	if !w.Workload.Kind.Restartable() {
		return nil, fmt.Errorf("%w: kind %q has no rollout", application.ErrInvalidName, w.Workload.Kind)
	}
	c, err := a.openWorkload(actor, w)
	if err != nil {
		return nil, err
	}
	first, err := a.rolloutStatus(ctx, c, w)
	if err != nil {
		return nil, err
	}
	if deadline <= 0 {
		deadline = time.Duration(a.progressDeadline(ctx, c, w)) * time.Second
	}
	out := make(chan application.RolloutStatus, 8)
	go func() {
		defer close(out)
		timeout := time.NewTimer(deadline)
		defer timeout.Stop()
		tick := time.NewTicker(a.pollEvery)
		defer tick.Stop()
		cur, errs := first, 0
		send := func(s application.RolloutStatus) bool {
			select {
			case out <- s:
				return true
			case <-ctx.Done():
				return false
			}
		}
		if !send(cur) || cur.Complete || cur.Failed {
			return
		}
		for {
			select {
			case <-ctx.Done():
				return
			case <-timeout.C:
				cur.Message = "deadline exceeded"
				send(cur)
				return
			case <-tick.C:
			}
			s, err := a.rolloutStatus(ctx, c, w)
			if err != nil {
				if errs++; errs >= maxPollErrors {
					cur.Failed, cur.Message = true, "status unavailable: "+err.Error()
					send(cur)
					return
				}
				continue
			}
			errs = 0
			if s != cur {
				cur = s
				if !send(cur) {
					return
				}
			}
			if cur.Complete || cur.Failed {
				return
			}
		}
	}()
	return out, nil
}

func (a *Access) progressDeadline(ctx context.Context, c *clients, w application.KubeWorkloadTarget) int32 {
	if w.Workload.Kind == domain.KindDeployment {
		cctx, cancel := callCtx(ctx)
		defer cancel()
		if d, err := c.kube.AppsV1().Deployments(w.Namespace).Get(cctx, w.Workload.Name, metav1.GetOptions{}); err == nil && d.Spec.ProgressDeadlineSeconds != nil {
			return *d.Spec.ProgressDeadlineSeconds
		}
	}
	return defaultProgressSecs
}

func (a *Access) rolloutStatus(ctx context.Context, c *clients, w application.KubeWorkloadTarget) (application.RolloutStatus, error) {
	ctx, cancel := callCtx(ctx)
	defer cancel()
	ns, name := w.Namespace, w.Workload.Name
	switch w.Workload.Kind {
	case domain.KindDeployment:
		o, err := c.kube.AppsV1().Deployments(ns).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return application.RolloutStatus{}, a.fail("get deployment", w.Cluster, err)
		}
		return deploymentStatus(o), nil
	case domain.KindStatefulSet:
		o, err := c.kube.AppsV1().StatefulSets(ns).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return application.RolloutStatus{}, a.fail("get statefulset", w.Cluster, err)
		}
		return statefulSetStatus(o), nil
	case domain.KindDaemonSet:
		o, err := c.kube.AppsV1().DaemonSets(ns).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return application.RolloutStatus{}, a.fail("get daemonset", w.Cluster, err)
		}
		return daemonSetStatus(o), nil
	default: // Rollout
		o, err := c.dyn.Resource(rolloutGVR).Namespace(ns).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return application.RolloutStatus{}, a.fail("get rollout", w.Cluster, err)
		}
		return rolloutStatusOf(o), nil
	}
}

// The status functions follow kubectl rollout status.

func deploymentStatus(d *appsv1.Deployment) application.RolloutStatus {
	desired := replicasOrOne(d.Spec.Replicas)
	s := application.RolloutStatus{Desired: desired, Updated: d.Status.UpdatedReplicas, Ready: d.Status.ReadyReplicas, Available: d.Status.AvailableReplicas}
	for _, c := range d.Status.Conditions {
		if c.Type == appsv1.DeploymentProgressing && c.Reason == "ProgressDeadlineExceeded" {
			s.Failed, s.Message = true, "progress deadline exceeded"
			return s
		}
	}
	switch {
	case d.Generation > d.Status.ObservedGeneration:
		s.Message = "waiting for the rollout to start"
	case d.Status.UpdatedReplicas < desired:
		s.Message = fmt.Sprintf("%d of %d replicas updated", d.Status.UpdatedReplicas, desired)
	case d.Status.Replicas > d.Status.UpdatedReplicas:
		s.Message = "old replicas are pending termination"
	case d.Status.AvailableReplicas < d.Status.UpdatedReplicas:
		s.Message = fmt.Sprintf("%d of %d updated replicas available", d.Status.AvailableReplicas, d.Status.UpdatedReplicas)
	default:
		s.Complete, s.Message = true, "rollout complete"
	}
	return s
}

func statefulSetStatus(o *appsv1.StatefulSet) application.RolloutStatus {
	desired := replicasOrOne(o.Spec.Replicas)
	s := application.RolloutStatus{Desired: desired, Updated: o.Status.UpdatedReplicas, Ready: o.Status.ReadyReplicas, Available: o.Status.AvailableReplicas}
	switch {
	case o.Generation > o.Status.ObservedGeneration:
		s.Message = "waiting for the rollout to start"
	case o.Status.UpdatedReplicas < desired:
		s.Message = fmt.Sprintf("%d of %d replicas updated", o.Status.UpdatedReplicas, desired)
	case o.Status.ReadyReplicas < desired:
		s.Message = fmt.Sprintf("%d of %d replicas ready", o.Status.ReadyReplicas, desired)
	case o.Status.UpdateRevision != "" && o.Status.CurrentRevision != o.Status.UpdateRevision:
		s.Message = "waiting for the update revision to become current"
	default:
		s.Complete, s.Message = true, "rollout complete"
	}
	return s
}

func daemonSetStatus(o *appsv1.DaemonSet) application.RolloutStatus {
	desired := o.Status.DesiredNumberScheduled
	s := application.RolloutStatus{Desired: desired, Updated: o.Status.UpdatedNumberScheduled, Ready: o.Status.NumberReady, Available: o.Status.NumberAvailable}
	switch {
	case o.Generation > o.Status.ObservedGeneration:
		s.Message = "waiting for the rollout to start"
	case o.Status.UpdatedNumberScheduled < desired:
		s.Message = fmt.Sprintf("%d of %d pods updated", o.Status.UpdatedNumberScheduled, desired)
	case o.Status.NumberAvailable < desired:
		s.Message = fmt.Sprintf("%d of %d pods available", o.Status.NumberAvailable, desired)
	default:
		s.Complete, s.Message = true, "rollout complete"
	}
	return s
}

// rolloutStatusOf reads an Argo Rollout. A restart is done only when the
// controller has recorded status.restartedAt at or after spec.restartAt;
// phase Healthy alone may still describe the state before the restart began.
func rolloutStatusOf(o *unstructured.Unstructured) application.RolloutStatus {
	desired := rolloutDesired(o)
	s := application.RolloutStatus{
		Desired:   desired,
		Updated:   int32(nestedInt(o, "status", "updatedReplicas")),
		Ready:     int32(nestedInt(o, "status", "readyReplicas")),
		Available: int32(nestedInt(o, "status", "availableReplicas")),
	}
	phase, _, _ := unstructured.NestedString(o.Object, "status", "phase")
	msg, _, _ := unstructured.NestedString(o.Object, "status", "message")
	if phase == "Degraded" {
		s.Failed = true
		s.Message = orDefault(msg, "rollout degraded")
		return s
	}
	want, _, _ := unstructured.NestedString(o.Object, "spec", "restartAt")
	got, _, _ := unstructured.NestedString(o.Object, "status", "restartedAt")
	wantT, gotT := parseRFC3339(want), parseRFC3339(got)
	restartPending := wantT != nil && (gotT == nil || gotT.Before(*wantT))
	switch {
	case restartPending:
		s.Message = "waiting for the controller to restart the pods"
	case phase != "Healthy":
		s.Message = orDefault(msg, "phase "+phase)
	case s.Updated < desired || s.Available < desired:
		s.Message = fmt.Sprintf("%d of %d replicas available", s.Available, desired)
	default:
		s.Complete, s.Message = true, "rollout complete"
	}
	return s
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}
