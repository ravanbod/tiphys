package kubeaccess

import (
	"errors"
	"testing"
	"time"

	"go.uber.org/zap"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
	"k8s.io/utils/ptr"

	"tiphys/internal/application"
	"tiphys/internal/domain"
)

const testNS = "shop"

type protectedSet map[string]bool

func (p protectedSet) IsProtected(ns string) bool { return p[ns] }

type fixedClock time.Time

func (c fixedClock) Now() time.Time { return time.Time(c) }

var testNow = time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)

var actor = application.KubeActor{Username: "alice"}

// rig is an Access with one usable cluster "c1" backed by fake clients.
type rig struct {
	*Access
	kube *fake.Clientset
	dyn  *dynamicfake.FakeDynamicClient
	cfgs []*rest.Config // configs the factory was asked to build
}

func newRig(t *testing.T, objs ...runtime.Object) *rig {
	t.Helper()
	scheme := runtime.NewScheme()
	r := &rig{
		kube: fake.NewClientset(objs...),
		dyn: dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, map[schema.GroupVersionResource]string{
			rolloutGVR:             "RolloutList",
			kyvernoClusterPolicies: "ClusterPolicyList",
			httpRouteGVRs[0]:       "HTTPRouteList",
			httpRouteGVRs[1]:       "HTTPRouteList",
			grpcRouteGVRs[0]:       "GRPCRouteList",
			grpcRouteGVRs[1]:       "GRPCRouteList",
			gatewayGVRs[0]:         "GatewayList",
			gatewayGVRs[1]:         "GatewayList",
		}),
	}
	reg := NewRegistry(nil, zap.NewNop())
	reg.factory = func(cfg *rest.Config) (*clients, error) {
		r.cfgs = append(r.cfgs, cfg)
		return &clients{kube: r.kube, dyn: r.dyn, cfg: cfg}, nil
	}
	e := &clusterEntry{base: &rest.Config{Host: "https://c1.example"}}
	e.privilegeOK.Store(true)
	e.enabled.Store(true)
	reg.clusters["c1"] = e
	r.Access = New(reg, protectedSet{"kube-system": true}, zap.NewNop(), WithClock(fixedClock(testNow)))
	r.pollEvery = 5 * time.Millisecond
	return r
}

func target(kind domain.WorkloadKind, name string) application.KubeWorkloadTarget {
	return application.KubeWorkloadTarget{Cluster: "c1", Namespace: testNS, Workload: domain.WorkloadRef{Kind: kind, Name: name}}
}

func ctrl(apiVersion, kind, name string) []metav1.OwnerReference {
	return []metav1.OwnerReference{{APIVersion: apiVersion, Kind: kind, Name: name, Controller: ptr.To(true)}}
}

func pod(name string, owners []metav1.OwnerReference, containers ...string) *corev1.Pod {
	p := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNS, OwnerReferences: owners}}
	if len(containers) == 0 {
		containers = []string{"app"}
	}
	for _, c := range containers {
		p.Spec.Containers = append(p.Spec.Containers, corev1.Container{Name: c, Image: "img"})
	}
	return p
}

func replicaSet(name string, owners []metav1.OwnerReference) *appsv1.ReplicaSet {
	return &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNS, OwnerReferences: owners}}
}

func deployment(name string) *appsv1.Deployment {
	return &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNS, Generation: 1}, Spec: appsv1.DeploymentSpec{Replicas: ptr.To(int32(2))}}
}

func job(name string, owners []metav1.OwnerReference, done bool) *batchv1.Job {
	j := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNS, OwnerReferences: owners}}
	if done {
		j.Status.Conditions = []batchv1.JobCondition{{Type: batchv1.JobComplete, Status: corev1.ConditionTrue}}
	}
	return j
}

func rollout(name string, spec map[string]any) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "argoproj.io/v1alpha1", "kind": "Rollout",
		"metadata": map[string]any{"name": name, "namespace": testNS},
		"spec":     spec,
	}}
}

func wantErr(t *testing.T, got, want error) {
	t.Helper()
	if !errors.Is(got, want) {
		t.Fatalf("error = %v, want %v", got, want)
	}
}

var errForbidden = apierrors.NewForbidden(schema.GroupResource{Resource: "validatingadmissionpolicies"}, "", errors.New("no"))
