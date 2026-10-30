package kubeaccess

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	"tiphys/internal/application"
	"tiphys/internal/domain"
)

func (e *e2e) needDebug(t *testing.T) {
	t.Helper()
	if e.a.debugImage == "" {
		t.Skip("TIPHYS_TEST_DEBUG_IMAGE not set")
	}
}

func debugName() string {
	return fmt.Sprintf("%s%08x", domain.DebugContainerPrefix, time.Now().UnixNano()&0xffffffff)
}

func TestE2EDebug(t *testing.T) {
	e := newE2E(t)
	e.needDebug(t)
	if en, _ := e.a.entry("kind"); !en.debugReady.Load() {
		t.Fatal("the probe did not find the tiphys-debug-only policy")
	}
	p := e.runningPod(t, domain.KindDeployment, "web")
	spec := application.KubeDebugSpec{Name: debugName(), Target: "app", Lifetime: 10 * time.Minute}
	s, err := e.a.Debug(context.Background(), e2eActor, e.target(domain.KindDeployment, "web"), p.Name, spec, "/bin/sh", 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var mu sync.Mutex
	var acc bytes.Buffer
	go func() {
		buf := make([]byte, 1024)
		for {
			n, err := s.Output().Read(buf)
			mu.Lock()
			acc.Write(buf[:n])
			mu.Unlock()
			if err != nil {
				return
			}
		}
	}()
	output := func() string { mu.Lock(); defer mu.Unlock(); return acc.String() }
	// The target's processes are visible: its loop prints hello-from-web.
	io.WriteString(s, "echo tiphys-$((6*7)); ps -o args\n")
	eventually2(t, 20*time.Second, "debug output", func() bool {
		return strings.Contains(output(), "tiphys-42") && strings.Contains(output(), "hello-from-web")
	})
	io.WriteString(s, "exit\n")
	select {
	case <-s.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("debug shell did not end after exit")
	}
	pod, err := e.asUser.CoreV1().Pods(e.ns).Get(context.Background(), p.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ec := pod.Spec.EphemeralContainers[len(pod.Spec.EphemeralContainers)-1]
	if ec.Name != spec.Name || ec.Image != e.a.debugImage || strings.Join(ec.Command, " ") != "sleep 600" || ec.TargetContainerName != "app" {
		t.Fatalf("debug container: %+v", ec)
	}

	// Another workload's pod is refused before anything is added.
	other := e.runningPod(t, domain.KindDeployment, "other")
	spec.Name = debugName()
	_, err = e.a.Debug(context.Background(), e2eActor, e.target(domain.KindDeployment, "web"), other.Name, spec, "/bin/sh", 80, 24)
	wantErr(t, err, application.ErrPodNotInWorkload)
}

// ---- Defense in depth: add ephemeral containers directly as tiphys:user ----

func TestE2EAdmissionAllowsOnlyLockedDownDebugContainers(t *testing.T) {
	e := newE2E(t)
	e.needDebug(t)
	ctx := context.Background()
	podName := e.runningPod(t, domain.KindDeployment, "other").Name
	good := func() corev1.EphemeralContainer {
		return corev1.EphemeralContainer{TargetContainerName: "app", EphemeralContainerCommon: corev1.EphemeralContainerCommon{
			Name: debugName(), Image: e.a.debugImage, Command: []string{"sleep", "60"}}}
	}
	add := func(ns, pod string, ecs ...corev1.EphemeralContainer) error {
		p, err := e.asUser.CoreV1().Pods(ns).Get(ctx, pod, metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		p.Spec.EphemeralContainers = append(p.Spec.EphemeralContainers, ecs...)
		_, err = e.asUser.CoreV1().Pods(ns).UpdateEphemeralContainers(ctx, pod, p, metav1.UpdateOptions{})
		return err
	}
	tokenVolume := func() string {
		p, err := e.asUser.CoreV1().Pods(e.ns).Get(ctx, podName, metav1.GetOptions{})
		if err != nil || len(p.Spec.Volumes) == 0 {
			t.Fatalf("pod volumes: %v", err)
		}
		return p.Spec.Volumes[0].Name
	}()

	for name, mut := range map[string]func(*corev1.EphemeralContainer){
		"other name":       func(c *corev1.EphemeralContainer) { c.Name = "debugger" },
		"other image":      func(c *corev1.EphemeralContainer) { c.Image = "busybox:1.36" },
		"other command":    func(c *corev1.EphemeralContainer) { c.Command = []string{"sh", "-c", "sleep 60"} },
		"args":             func(c *corev1.EphemeralContainer) { c.Args = []string{"60"} },
		"too long a sleep": func(c *corev1.EphemeralContainer) { c.Command = []string{"sleep", "999999999"} },
		"env from a secret": func(c *corev1.EphemeralContainer) {
			c.EnvFrom = []corev1.EnvFromSource{{SecretRef: &corev1.SecretEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: "db"}}}}
		},
		"env": func(c *corev1.EphemeralContainer) { c.Env = []corev1.EnvVar{{Name: "A", Value: "b"}} },
		"privileged": func(c *corev1.EphemeralContainer) {
			c.SecurityContext = &corev1.SecurityContext{Privileged: ptr.To(true)}
		},
		"added capability": func(c *corev1.EphemeralContainer) {
			c.SecurityContext = &corev1.SecurityContext{Capabilities: &corev1.Capabilities{Add: []corev1.Capability{"SYS_ADMIN"}}}
		},
		"volume mount": func(c *corev1.EphemeralContainer) {
			c.VolumeMounts = []corev1.VolumeMount{{Name: tokenVolume, MountPath: "/token"}}
		},
		"tty": func(c *corev1.EphemeralContainer) { c.Stdin, c.TTY = true, true },
	} {
		t.Run(name, func(t *testing.T) {
			c := good()
			mut(&c)
			mustBeAdmissionDenied(t, add(e.ns, podName, c), "tiphys-debug-only")
		})
	}
	t.Run("two at once", func(t *testing.T) {
		mustBeAdmissionDenied(t, add(e.ns, podName, good(), good()), "tiphys-debug-only")
	})
	t.Run("protected namespace", func(t *testing.T) {
		pods, err := e.asUser.CoreV1().Pods("kube-system").List(ctx, metav1.ListOptions{})
		if err != nil || len(pods.Items) == 0 {
			t.Fatalf("kube-system pods: %v", err)
		}
		c := good()
		c.TargetContainerName = pods.Items[0].Spec.Containers[0].Name
		mustBeAdmissionDenied(t, add("kube-system", pods.Items[0].Name, c), "tiphys-protected-namespaces")
	})
	// The portal's container passes (the policy is not a blanket deny).
	if err := add(e.ns, podName, good()); err != nil {
		t.Fatalf("locked-down debug container rejected: %v", err)
	}
}
