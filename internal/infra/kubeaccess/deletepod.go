package kubeaccess

import (
	"context"
	"fmt"

	"go.uber.org/zap"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"tiphys/internal/application"
)

// DeletePod deletes one pod of a restartable workload for its controller to
// replace. The pod is re-read and its owner chain verified first; the delete
// carries that pod's UID as a precondition, so a pod recreated under the same
// name (StatefulSet) in between is never the one deleted. No grace period is
// set: the pod's own applies. The tiphys-pod-delete-only policy admits only
// pods with a ReplicaSet, StatefulSet or DaemonSet controller.
func (a *Access) DeletePod(ctx context.Context, actor application.KubeActor, w application.KubeWorkloadTarget, pod string) error {
	if !w.Workload.Kind.Restartable() {
		return fmt.Errorf("%w: kind %q has no pods to delete", application.ErrInvalidName, w.Workload.Kind)
	}
	c, err := a.openWorkload(actor, w)
	if err != nil {
		return err
	}
	if !a.PodDeleteReady(w.Cluster) {
		return application.ErrPodDeleteUnavailable
	}
	ctx, cancel := callCtx(ctx)
	defer cancel()
	p, err := a.verifyPod(ctx, c, w, pod)
	if err != nil {
		return err
	}
	uid := p.UID
	err = c.kube.CoreV1().Pods(w.Namespace).Delete(ctx, pod, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid}})
	switch {
	case err == nil:
		return nil
	case apierrors.IsConflict(err):
		a.logger.Debug("pod replaced before delete", zap.String("cluster", w.Cluster), zap.Error(err))
		return fmt.Errorf("%w: the pod was replaced in the meantime", application.ErrConflict)
	default:
		return a.fail("delete pod", w.Cluster, err)
	}
}
