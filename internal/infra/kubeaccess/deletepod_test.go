package kubeaccess

import (
	"context"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	clienttesting "k8s.io/client-go/testing"

	"tiphys/internal/application"
	"tiphys/internal/domain"
)

// podDeleteRig is a rig whose cluster has the pod delete policy.
func podDeleteRig(t *testing.T) *rig {
	t.Helper()
	objs := ownerFixtures()
	for _, o := range objs {
		if p, ok := o.(interface{ SetUID(types.UID) }); ok {
			p.SetUID("uid-1")
		}
	}
	r := newRig(t, objs...)
	r.clusters["c1"].podDeleteReady.Store(true)
	return r
}

func deletes(r *rig) []clienttesting.DeleteActionImpl {
	var out []clienttesting.DeleteActionImpl
	for _, a := range r.kube.Actions() {
		if d, ok := a.(clienttesting.DeleteActionImpl); ok {
			out = append(out, d)
		}
	}
	return out
}

func TestDeletePodDeletesTheVerifiedPod(t *testing.T) {
	r := podDeleteRig(t)
	if err := r.DeletePod(context.Background(), actor, target(domain.KindDeployment, "web"), "web-abc-1"); err != nil {
		t.Fatal(err)
	}
	d := deletes(r)
	if len(d) != 1 || d[0].GetName() != "web-abc-1" || d[0].GetNamespace() != testNS {
		t.Fatalf("deletes: %+v", d)
	}
	opts := d[0].GetDeleteOptions()
	if opts.Preconditions == nil || opts.Preconditions.UID == nil || *opts.Preconditions.UID != "uid-1" || opts.GracePeriodSeconds != nil {
		t.Fatalf("delete options: %+v", opts)
	}
}

func TestDeletePodRefusals(t *testing.T) {
	ctx := context.Background()
	r := podDeleteRig(t)
	cases := []struct {
		name string
		w    application.KubeWorkloadTarget
		pod  string
		want error
	}{
		{"other workload's pod", target(domain.KindDeployment, "web"), "other-abc-1", application.ErrPodNotInWorkload},
		{"orphan pod", target(domain.KindDeployment, "web"), "orphan", application.ErrPodNotInWorkload},
		{"cronjob pod", target(domain.KindCronJob, "report"), "report-1-p", application.ErrInvalidName},
		{"bad pod name", target(domain.KindDeployment, "web"), "Web_1", application.ErrInvalidName},
		{"protected namespace", application.KubeWorkloadTarget{Cluster: "c1", Namespace: "kube-system", Workload: domain.WorkloadRef{Kind: domain.KindDeployment, Name: "web"}}, "web-abc-1", application.ErrProtectedNamespace},
		{"missing pod", target(domain.KindDeployment, "web"), "web-abc-9", application.ErrNotFound},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			wantErr(t, r.DeletePod(ctx, actor, c.w, c.pod), c.want)
		})
	}
	if d := deletes(r); len(d) != 0 {
		t.Fatalf("refused deletes reached the API: %+v", d)
	}
}

func TestDeletePodNeedsThePolicy(t *testing.T) {
	r := newRig(t, ownerFixtures()...)
	wantErr(t, r.DeletePod(context.Background(), actor, target(domain.KindDeployment, "web"), "web-abc-1"), application.ErrPodDeleteUnavailable)
	if d := deletes(r); len(d) != 0 {
		t.Fatalf("deletes: %+v", d)
	}
}

func TestDeletePodReplacedInBetween(t *testing.T) {
	r := podDeleteRig(t)
	r.kube.PrependReactor("delete", "pods", func(clienttesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewConflict(schema.GroupResource{Resource: "pods"}, "db-0", nil)
	})
	wantErr(t, r.DeletePod(context.Background(), actor, target(domain.KindStatefulSet, "db"), "db-0"), application.ErrConflict)
}
