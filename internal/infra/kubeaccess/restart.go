package kubeaccess

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"tiphys/internal/application"
	"tiphys/internal/domain"
)

// Restart sets the restart field of the workload and nothing else: the
// restartedAt pod-template annotation (strategic merge patch) or, for Argo
// Rollouts, spec.restartAt (merge patch).
func (a *Access) Restart(ctx context.Context, actor application.KubeActor, w application.KubeWorkloadTarget) error {
	if !w.Workload.Kind.Restartable() {
		return fmt.Errorf("%w: kind %q cannot be restarted", application.ErrInvalidName, w.Workload.Kind)
	}
	c, err := a.openWorkload(actor, w)
	if err != nil {
		return err
	}
	ctx, cancel := callCtx(ctx)
	defer cancel()

	now := a.clock.Now().UTC().Format(time.RFC3339)
	ns, name := w.Namespace, w.Workload.Name
	switch w.Workload.Kind {
	case domain.KindRollout:
		_, err = c.dyn.Resource(rolloutGVR).Namespace(ns).Patch(ctx, name, types.MergePatchType, restartAtPatch(now), metav1.PatchOptions{})
	case domain.KindDeployment:
		_, err = c.kube.AppsV1().Deployments(ns).Patch(ctx, name, types.StrategicMergePatchType, restartedAtPatch(now), metav1.PatchOptions{})
	case domain.KindStatefulSet:
		_, err = c.kube.AppsV1().StatefulSets(ns).Patch(ctx, name, types.StrategicMergePatchType, restartedAtPatch(now), metav1.PatchOptions{})
	case domain.KindDaemonSet:
		_, err = c.kube.AppsV1().DaemonSets(ns).Patch(ctx, name, types.StrategicMergePatchType, restartedAtPatch(now), metav1.PatchOptions{})
	}
	if err != nil {
		return a.fail("restart "+string(w.Workload.Kind), w.Cluster, err)
	}
	return nil
}

func restartedAtPatch(now string) []byte {
	b, _ := json.Marshal(map[string]any{"spec": map[string]any{"template": map[string]any{
		"metadata": map[string]any{"annotations": map[string]string{restartAnnotation: now}},
	}}})
	return b
}

func restartAtPatch(now string) []byte {
	b, _ := json.Marshal(map[string]any{"spec": map[string]any{"restartAt": now}})
	return b
}
