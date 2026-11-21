package kubeaccess

import (
	"context"
	"fmt"
	"io"

	corev1 "k8s.io/api/core/v1"

	"tiphys/internal/application"
	"tiphys/internal/domain"
)

var (
	allowedTailLines    = []int64{0, 100, 1000, 10000} // 0 = whole log (downloads)
	allowedSinceSeconds = []int64{0, 300, 3600, 86400}
)

func oneOf(v int64, allowed []int64) bool {
	for _, a := range allowed {
		if a == v {
			return true
		}
	}
	return false
}

// Logs verifies pod ownership and opens the log stream. The stream is bound
// to ctx and has no call timeout, so follow works.
func (a *Access) Logs(ctx context.Context, actor application.KubeActor, w application.KubeWorkloadTarget, pod string, opts application.LogOptions) (io.ReadCloser, error) {
	po, err := podLogOptions(opts)
	if err != nil {
		return nil, err
	}
	c, err := a.openWorkload(actor, w)
	if err != nil {
		return nil, err
	}
	p, err := func() (*corev1.Pod, error) {
		vctx, cancel := callCtx(ctx)
		defer cancel()
		return a.verifyPod(vctx, c, w, pod)
	}()
	if err != nil {
		return nil, err
	}
	return a.streamLogs(ctx, c, w.Cluster, p, po)
}

// OtherPodLogs is Logs for a pod whose owner chain leads to no workload; a
// workload's pod is refused, so its logs are always read (and audited) as
// the workload's.
func (a *Access) OtherPodLogs(ctx context.Context, actor application.KubeActor, cluster, namespace, pod string, opts application.LogOptions) (io.ReadCloser, error) {
	po, err := podLogOptions(opts)
	if err != nil {
		return nil, err
	}
	if err := validObjectName("pod", pod); err != nil {
		return nil, err
	}
	c, err := a.open(actor, cluster, namespace)
	if err != nil {
		return nil, err
	}
	p, err := func() (*corev1.Pod, error) {
		vctx, cancel := callCtx(ctx)
		defer cancel()
		return a.verifyOtherPod(vctx, c, cluster, namespace, pod)
	}()
	if err != nil {
		return nil, err
	}
	return a.streamLogs(ctx, c, cluster, p, po)
}

// podLogOptions checks the options against the allowed values.
func podLogOptions(opts application.LogOptions) (*corev1.PodLogOptions, error) {
	if !domain.ValidDNS1123Subdomain(opts.Container) {
		return nil, fmt.Errorf("%w: container", application.ErrInvalidName)
	}
	if !oneOf(opts.TailLines, allowedTailLines) || !oneOf(opts.SinceSeconds, allowedSinceSeconds) || opts.LimitBytes < 0 {
		return nil, fmt.Errorf("%w: log options", application.ErrInvalidName)
	}
	po := &corev1.PodLogOptions{
		Container:  opts.Container,
		Previous:   opts.Previous,
		Follow:     opts.Follow,
		Timestamps: opts.Timestamps,
	}
	if opts.TailLines > 0 {
		po.TailLines = &opts.TailLines
	}
	if opts.SinceSeconds > 0 {
		po.SinceSeconds = &opts.SinceSeconds
	}
	if opts.LimitBytes > 0 {
		po.LimitBytes = &opts.LimitBytes
	}
	return po, nil
}

// streamLogs opens the log stream of a verified pod's container.
func (a *Access) streamLogs(ctx context.Context, c *clients, cluster string, p *corev1.Pod, po *corev1.PodLogOptions) (io.ReadCloser, error) {
	if !containerNames(p, true)[po.Container] {
		return nil, fmt.Errorf("%w: container not in pod", application.ErrInvalidName)
	}
	rc, err := c.kube.CoreV1().Pods(p.Namespace).GetLogs(p.Name, po).Stream(ctx)
	if err != nil {
		return nil, a.fail("open logs", cluster, err)
	}
	return rc, nil
}
