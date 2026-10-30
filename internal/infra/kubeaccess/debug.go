package kubeaccess

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"go.uber.org/zap"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"tiphys/internal/application"
	"tiphys/internal/domain"
)

const (
	// defaultDebugWait bounds the wait for a debug container to run. It stays
	// under common ingress timeouts because the WebSocket upgrade waits too.
	defaultDebugWait = 45 * time.Second
	// maxDebugLifetime must not exceed the chart's debug.maxLifetimeSeconds.
	maxDebugLifetime = 25 * time.Hour
)

// waitingFailures are container waiting reasons that will not resolve while
// the user waits.
var waitingFailures = map[string]bool{
	"ErrImagePull": true, "ImagePullBackOff": true, "InvalidImageName": true,
	"CreateContainerConfigError": true, "CreateContainerError": true, "RunContainerError": true,
}

// Debug adds an ephemeral container running the debug image to a pod of the
// workload, waits until it runs and opens a TTY shell in it. The container's
// only process is `sleep <lifetime>`, so it stops by itself; its spec has
// nothing else (no env, volumes, ports or securityContext), which the chart's
// tiphys-debug-only policy enforces for tiphys:user as well.
func (a *Access) Debug(ctx context.Context, actor application.KubeActor, w application.KubeWorkloadTarget, pod string, spec application.KubeDebugSpec, shell string, cols, rows uint16) (application.ExecStream, error) {
	if a.debugImage == "" {
		return nil, application.ErrDebugUnavailable
	}
	if !domain.ValidShell(shell) {
		return nil, fmt.Errorf("%w: shell", application.ErrInvalidName)
	}
	if !strings.HasPrefix(spec.Name, domain.DebugContainerPrefix) || !domain.ValidDNS1123Label(spec.Name) {
		return nil, fmt.Errorf("%w: debug container", application.ErrInvalidName)
	}
	if !domain.ValidDNS1123Label(spec.Target) {
		return nil, fmt.Errorf("%w: container", application.ErrInvalidName)
	}
	if spec.Lifetime < time.Minute || spec.Lifetime > maxDebugLifetime {
		return nil, fmt.Errorf("%w: debug container lifetime", application.ErrInvalidName)
	}
	c, err := a.openWorkload(actor, w)
	if err != nil {
		return nil, err
	}
	if e, err := a.entry(w.Cluster); err != nil || !e.debugReady.Load() {
		return nil, application.ErrDebugUnavailable
	}
	if err := a.addDebugContainer(ctx, c, w, pod, spec); err != nil {
		return nil, err
	}
	if err := a.waitDebugRunning(ctx, c, w, pod, spec.Name); err != nil {
		return nil, err
	}
	return a.openShell(ctx, c, w.Cluster, w.Namespace, pod, spec.Name, shell, cols, rows)
}

// addDebugContainer verifies the pod and the target container, then adds the
// container through the ephemeralcontainers subresource (one retry when the
// pod changed in between).
func (a *Access) addDebugContainer(ctx context.Context, c *clients, w application.KubeWorkloadTarget, podName string, spec application.KubeDebugSpec) error {
	ctx, cancel := callCtx(ctx)
	defer cancel()
	for attempt := 0; ; attempt++ {
		p, err := a.verifyPod(ctx, c, w, podName)
		if err != nil {
			return err
		}
		if !containerNames(p, false)[spec.Target] {
			return fmt.Errorf("%w: container not in pod", application.ErrInvalidName)
		}
		p.Spec.EphemeralContainers = append(p.Spec.EphemeralContainers, corev1.EphemeralContainer{
			TargetContainerName: spec.Target,
			EphemeralContainerCommon: corev1.EphemeralContainerCommon{
				Name:            spec.Name,
				Image:           a.debugImage,
				ImagePullPolicy: corev1.PullIfNotPresent,
				Command:         []string{"sleep", strconv.Itoa(int(spec.Lifetime / time.Second))},
			},
		})
		_, err = c.kube.CoreV1().Pods(w.Namespace).UpdateEphemeralContainers(ctx, podName, p, metav1.UpdateOptions{})
		switch {
		case err == nil:
			return nil
		case apierrors.IsConflict(err) && attempt == 0:
			continue
		case apierrors.IsForbidden(err), apierrors.IsInvalid(err):
			// Admission, Pod Security or RBAC said no; the message says why.
			a.logger.Debug("debug container refused", zap.String("cluster", w.Cluster), zap.Error(err))
			return &application.DebugError{Reason: err.Error()}
		default:
			return a.fail("add debug container", w.Cluster, err)
		}
	}
}

// waitDebugRunning polls the pod until the debug container runs, fails, or
// debugWait passes.
func (a *Access) waitDebugRunning(ctx context.Context, c *clients, w application.KubeWorkloadTarget, podName, name string) error {
	wctx, cancel := context.WithTimeout(ctx, a.debugWait)
	defer cancel()
	tick := time.NewTicker(a.pollEvery)
	defer tick.Stop()
	for {
		p, err := c.kube.CoreV1().Pods(w.Namespace).Get(wctx, podName, metav1.GetOptions{})
		switch {
		case err == nil:
			if done, err := debugStatus(p, name); done {
				return err
			}
		case wctx.Err() == nil:
			return a.fail("get pod", w.Cluster, err)
		}
		select {
		case <-wctx.Done():
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return &application.DebugError{Reason: fmt.Sprintf(
				"the debug container did not start within %s; the image may still be pulling, try again shortly", a.debugWait)}
		case <-tick.C:
		}
	}
}

// debugStatus reports whether the debug container has reached a final
// state for the wait: running (nil) or failed (*DebugError).
func debugStatus(p *corev1.Pod, name string) (bool, error) {
	for _, s := range p.Status.EphemeralContainerStatuses {
		if s.Name != name {
			continue
		}
		switch st := s.State; {
		case st.Running != nil:
			return true, nil
		case st.Terminated != nil:
			return true, &application.DebugError{Reason: strings.TrimSpace(fmt.Sprintf("the debug container exited (%s) %s", st.Terminated.Reason, st.Terminated.Message))}
		case st.Waiting != nil && waitingFailures[st.Waiting.Reason]:
			return true, &application.DebugError{Reason: fmt.Sprintf("%s: %s", st.Waiting.Reason, st.Waiting.Message)}
		}
	}
	return false, nil
}
