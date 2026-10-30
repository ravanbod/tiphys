package kubeaccess

import (
	"context"
	"testing"

	admissionv1 "k8s.io/api/admissionregistration/v1"
	authv1 "k8s.io/api/authorization/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	clienttesting "k8s.io/client-go/testing"

	"tiphys/internal/domain"
)

// allow decides every SelfSubjectAccessReview from the review's attributes.
func (r *rig) allow(decide func(a authv1.ResourceAttributes) bool) {
	r.kube.PrependReactor("create", "selfsubjectaccessreviews", func(a clienttesting.Action) (bool, runtime.Object, error) {
		ssar := a.(clienttesting.CreateAction).GetObject().(*authv1.SelfSubjectAccessReview)
		ssar.Status.Allowed = decide(*ssar.Spec.ResourceAttributes)
		return true, ssar, nil
	})
}

func connectorOnly(a authv1.ResourceAttributes) bool {
	return a.Verb == "impersonate" && a.Resource == "users" && a.Name == "tiphys:user" ||
		a.Verb == "impersonate" && a.Resource == "groups" && a.Name == "tiphys:users"
}

func TestProbePassesForTheConnectorRole(t *testing.T) {
	r := newRig(t)
	r.allow(connectorOnly)
	p, err := r.Probe(context.Background(), "c1")
	if err != nil {
		t.Fatal(err)
	}
	if !p.PrivilegeOK || len(p.Violations) != 0 || p.Version == "" || p.AdmissionMode != domain.AdmissionNone {
		t.Fatalf("probe = %+v", p)
	}
}

func TestProbeFlagsOverPrivilegedCredentialAndRefusesCluster(t *testing.T) {
	r := newRig(t)
	r.allow(func(authv1.ResourceAttributes) bool { return true }) // cluster-admin
	p, err := r.Probe(context.Background(), "c1")
	if err != nil {
		t.Fatal(err)
	}
	if p.PrivilegeOK || len(p.Violations) < 4 {
		t.Fatalf("probe = %+v", p)
	}
	_, err = r.ListWorkloads(context.Background(), actor, "c1", testNS)
	if err == nil || err.Error() != "cluster is unreachable or over-privileged" {
		t.Fatalf("err = %v", err)
	}
}

func TestProbeFlagsEachDeniedCheckSeparately(t *testing.T) {
	for _, extra := range []authv1.ResourceAttributes{
		{Verb: "get", Resource: "secrets", Namespace: "kube-system"},
		{Verb: "create", Resource: "pods"},
		{Verb: "update", Resource: "pods", Subresource: "ephemeralcontainers"},
		{Verb: "impersonate", Resource: "users", Name: "system:admin"},
		{Verb: "impersonate", Resource: "groups", Name: "system:masters"},
	} {
		r := newRig(t)
		r.allow(func(a authv1.ResourceAttributes) bool { return connectorOnly(a) || a == withVersion(extra) })
		p, err := r.Probe(context.Background(), "c1")
		if err != nil || p.PrivilegeOK || len(p.Violations) != 1 {
			t.Fatalf("%+v: probe %+v err %v", extra, p, err)
		}
	}
}

func withVersion(a authv1.ResourceAttributes) authv1.ResourceAttributes { a.Version = "*"; return a }

func TestProbeFlagsConnectorThatCannotImpersonate(t *testing.T) {
	r := newRig(t)
	r.allow(func(authv1.ResourceAttributes) bool { return false })
	p, _ := r.Probe(context.Background(), "c1")
	if p.PrivilegeOK || len(p.Violations) != 1 {
		t.Fatalf("probe = %+v", p)
	}
}

func TestProbeRestoresClusterAfterFix(t *testing.T) {
	r := newRig(t)
	r.clusters["c1"].privilegeOK.Store(false)
	r.allow(connectorOnly)
	if _, err := r.Probe(context.Background(), "c1"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.ListWorkloads(context.Background(), actor, "c1", testNS); err != nil {
		t.Fatal(err)
	}
}

func TestAdmissionModeDetection(t *testing.T) {
	vap := &admissionv1.ValidatingAdmissionPolicy{ObjectMeta: metav1.ObjectMeta{Name: "tiphys-restart-only"}}
	other := &admissionv1.ValidatingAdmissionPolicy{ObjectMeta: metav1.ObjectMeta{Name: "unrelated"}}
	ky := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "kyverno.io/v1", "kind": "ClusterPolicy", "metadata": map[string]any{"name": "tiphys-restart-only"}}}

	r := newRig(t, other)
	r.allow(connectorOnly)
	if p, _ := r.Probe(context.Background(), "c1"); p.AdmissionMode != domain.AdmissionNone {
		t.Fatalf("mode = %s", p.AdmissionMode)
	}
	r = newRig(t, vap, other)
	r.allow(connectorOnly)
	if p, _ := r.Probe(context.Background(), "c1"); p.AdmissionMode != domain.AdmissionVAP {
		t.Fatalf("mode = %s", p.AdmissionMode)
	}
	r = newRig(t)
	r.dyn.Tracker().Add(ky)
	r.allow(connectorOnly)
	if p, _ := r.Probe(context.Background(), "c1"); p.AdmissionMode != domain.AdmissionKyverno {
		t.Fatalf("mode = %s", p.AdmissionMode)
	}
}

func TestDebugPolicyDetection(t *testing.T) {
	vap := func(name string) *admissionv1.ValidatingAdmissionPolicy {
		return &admissionv1.ValidatingAdmissionPolicy{ObjectMeta: metav1.ObjectMeta{Name: name}}
	}
	ky := func(name string) *unstructured.Unstructured {
		return &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "kyverno.io/v1", "kind": "ClusterPolicy", "metadata": map[string]any{"name": name}}}
	}
	ctx := context.Background()

	r := newRig(t, vap("tiphys-restart-only"))
	r.allow(connectorOnly)
	if p, _ := r.Probe(ctx, "c1"); p.DebugReady || r.clusters["c1"].debugReady.Load() {
		t.Fatalf("debug ready without the debug policy: %+v", p)
	}
	r = newRig(t, vap("tiphys-restart-only"), vap("tiphys-debug-only"))
	r.allow(connectorOnly)
	if p, _ := r.Probe(ctx, "c1"); !p.DebugReady || !r.clusters["c1"].debugReady.Load() {
		t.Fatalf("debug policy not detected: %+v", p)
	}
	// There is no Kyverno version of the debug policy.
	r = newRig(t)
	r.dyn.Tracker().Add(ky("tiphys-restart-only"))
	r.dyn.Tracker().Add(ky("tiphys-debug-only"))
	r.allow(connectorOnly)
	if p, _ := r.Probe(ctx, "c1"); p.AdmissionMode != domain.AdmissionKyverno || p.DebugReady {
		t.Fatalf("kyverno: %+v", p)
	}
}

func TestAdmissionDetectionTreatsForbiddenAsNone(t *testing.T) {
	r := newRig(t)
	r.allow(connectorOnly)
	r.kube.PrependReactor("list", "validatingadmissionpolicies", func(clienttesting.Action) (bool, runtime.Object, error) {
		return true, nil, errForbidden
	})
	if p, err := r.Probe(context.Background(), "c1"); err != nil || p.AdmissionMode != domain.AdmissionNone {
		t.Fatalf("probe %+v err %v", p, err)
	}
}

func TestProbeKubeconfigRejectsBeforeConnecting(t *testing.T) {
	r := newRig(t)
	_, err := r.ProbeKubeconfig(context.Background(), kubeconfigYAML(goodCluster+"\n    insecure-skip-tls-verify: true", goodUser))
	wantErr(t, err, ErrKubeconfigRejected)
	if len(r.cfgs) != 0 {
		t.Fatal("a client was built for a rejected kubeconfig")
	}
	r.allow(connectorOnly)
	p, err := r.ProbeKubeconfig(context.Background(), kubeconfigYAML(goodCluster, goodUser))
	if err != nil || !p.PrivilegeOK {
		t.Fatalf("probe %+v err %v", p, err)
	}
}
