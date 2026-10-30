package kubeaccess

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	clienttesting "k8s.io/client-go/testing"

	"tiphys/internal/application"
	"tiphys/internal/domain"
)

const testDebugImage = "nicolaka/netshoot@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

var debugSpec = application.KubeDebugSpec{Name: "tiphys-debug-1a2b3c4d", Target: "app", Lifetime: 2 * time.Hour}

var podsGVR = corev1.SchemeGroupVersion.WithResource("pods")

// debugRig is a rig whose cluster has the debug policy and whose fake API
// server applies ephemeral container updates, then reports the new container
// in the given state (nil: no status yet).
func debugRig(t *testing.T, state *corev1.ContainerState) (*rig, *[]*corev1.Pod) {
	t.Helper()
	r := newRig(t, ownerFixtures()...)
	r.debugImage = testDebugImage
	r.clusters["c1"].debugReady.Store(true)
	var updates []*corev1.Pod
	r.kube.PrependReactor("update", "pods", func(a clienttesting.Action) (bool, runtime.Object, error) {
		if a.GetSubresource() != "ephemeralcontainers" {
			return false, nil, nil
		}
		p := a.(clienttesting.UpdateAction).GetObject().(*corev1.Pod).DeepCopy()
		updates = append(updates, p.DeepCopy())
		if state != nil {
			for _, c := range p.Spec.EphemeralContainers {
				p.Status.EphemeralContainerStatuses = append(p.Status.EphemeralContainerStatuses,
					corev1.ContainerStatus{Name: c.Name, State: *state})
			}
		}
		return true, p, r.kube.Tracker().Update(podsGVR, p, p.Namespace)
	})
	return r, &updates
}

var running = &corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}

// The fake clientset has no REST client for the exec stream, so this runs the
// steps before it; the E2E test covers the whole verb.
func TestDebugAddsLockedDownContainer(t *testing.T) {
	r, updates := debugRig(t, running)
	ctx := context.Background()
	w := target(domain.KindDeployment, "web")
	c, err := r.openWorkload(actor, w)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.addDebugContainer(ctx, c, w, "web-abc-1", debugSpec); err != nil {
		t.Fatal(err)
	}
	if err := r.waitDebugRunning(ctx, c, w, "web-abc-1", debugSpec.Name); err != nil {
		t.Fatal(err)
	}
	if len(*updates) != 1 {
		t.Fatalf("updates: %d", len(*updates))
	}
	ecs := (*updates)[0].Spec.EphemeralContainers
	if len(ecs) != 1 {
		t.Fatalf("ephemeral containers: %+v", ecs)
	}
	got := ecs[0]
	want := corev1.EphemeralContainer{
		TargetContainerName: "app",
		EphemeralContainerCommon: corev1.EphemeralContainerCommon{
			Name: "tiphys-debug-1a2b3c4d", Image: testDebugImage, ImagePullPolicy: corev1.PullIfNotPresent,
			Command: []string{"sleep", "7200"},
		},
	}
	if got.String() != want.String() {
		t.Fatalf("container:\n got %s\nwant %s", got.String(), want.String())
	}
	if h := r.cfgs[len(r.cfgs)-1].Impersonate; h.UserName != ImpersonatedUser {
		t.Fatalf("not impersonated: %+v", h)
	}
}

func TestDebugRefusedWhenNotSetUp(t *testing.T) {
	ctx := context.Background()
	w := target(domain.KindDeployment, "web")

	r, updates := debugRig(t, running)
	r.debugImage = ""
	_, err := r.Debug(ctx, actor, w, "web-abc-1", debugSpec, "/bin/sh", 80, 24)
	wantErr(t, err, application.ErrDebugUnavailable)

	r, updates = debugRig(t, running)
	r.clusters["c1"].debugReady.Store(false)
	_, err = r.Debug(ctx, actor, w, "web-abc-1", debugSpec, "/bin/sh", 80, 24)
	wantErr(t, err, application.ErrDebugUnavailable)
	if len(*updates) != 0 || len(r.kube.Actions()) != 0 {
		t.Fatalf("kubernetes was called: %v", r.kube.Actions())
	}
}

func TestDebugRefusesBadSpec(t *testing.T) {
	r, updates := debugRig(t, running)
	w := target(domain.KindDeployment, "web")
	for _, spec := range []application.KubeDebugSpec{
		{Name: "debugger", Target: "app", Lifetime: time.Hour},
		{Name: "tiphys-debug-X", Target: "app", Lifetime: time.Hour},
		{Name: "tiphys-debug-1", Target: "app;id", Lifetime: time.Hour},
		{Name: "tiphys-debug-1", Target: "", Lifetime: time.Hour},
		{Name: "tiphys-debug-1", Target: "app", Lifetime: time.Second},
		{Name: "tiphys-debug-1", Target: "app", Lifetime: 26 * time.Hour},
	} {
		_, err := r.Debug(context.Background(), actor, w, "web-abc-1", spec, "/bin/sh", 80, 24)
		wantErr(t, err, application.ErrInvalidName)
	}
	_, err := r.Debug(context.Background(), actor, w, "web-abc-1", debugSpec, "/bin/zsh", 80, 24)
	wantErr(t, err, application.ErrInvalidName)
	if len(*updates) != 0 {
		t.Fatal("a debug container was added")
	}
}

func TestDebugChecksPodAndTarget(t *testing.T) {
	r, updates := debugRig(t, running)
	ctx := context.Background()
	w := target(domain.KindDeployment, "web")
	_, err := r.Debug(ctx, actor, w, "other-abc-1", debugSpec, "/bin/sh", 80, 24)
	wantErr(t, err, application.ErrPodNotInWorkload)
	sidecar := debugSpec
	sidecar.Target = "sidecar"
	_, err = r.Debug(ctx, actor, w, "web-abc-1", sidecar, "/bin/sh", 80, 24)
	wantErr(t, err, application.ErrInvalidName)
	if len(*updates) != 0 {
		t.Fatal("a debug container was added")
	}
}

func TestDebugReportsAdmissionRefusal(t *testing.T) {
	r, _ := debugRig(t, running)
	r.kube.PrependReactor("update", "pods", func(clienttesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: "pods"}, "web-abc-1",
			errors.New(`violates PodSecurity "restricted:latest"`))
	})
	_, err := r.Debug(context.Background(), actor, target(domain.KindDeployment, "web"), "web-abc-1", debugSpec, "/bin/sh", 80, 24)
	var de *application.DebugError
	if !errors.As(err, &de) || !strings.Contains(de.Reason, "PodSecurity") {
		t.Fatalf("err = %v", err)
	}
}

func TestDebugReportsContainerThatCannotStart(t *testing.T) {
	r, _ := debugRig(t, &corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "ImagePullBackOff", Message: "back-off pulling image"}})
	_, err := r.Debug(context.Background(), actor, target(domain.KindDeployment, "web"), "web-abc-1", debugSpec, "/bin/sh", 80, 24)
	var de *application.DebugError
	if !errors.As(err, &de) || !strings.HasPrefix(de.Reason, "ImagePullBackOff") {
		t.Fatalf("err = %v", err)
	}
}

func TestDebugGivesUpWhenContainerDoesNotStart(t *testing.T) {
	r, _ := debugRig(t, &corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "ContainerCreating"}})
	r.debugWait = 30 * time.Millisecond
	_, err := r.Debug(context.Background(), actor, target(domain.KindDeployment, "web"), "web-abc-1", debugSpec, "/bin/sh", 80, 24)
	var de *application.DebugError
	if !errors.As(err, &de) || !strings.Contains(de.Reason, "did not start") {
		t.Fatalf("err = %v", err)
	}
}
