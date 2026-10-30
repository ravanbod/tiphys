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
	if !domain.ValidDNS1123Subdomain(opts.Container) {
		return nil, fmt.Errorf("%w: container", application.ErrInvalidName)
	}
	if !oneOf(opts.TailLines, allowedTailLines) || !oneOf(opts.SinceSeconds, allowedSinceSeconds) || opts.LimitBytes < 0 {
		return nil, fmt.Errorf("%w: log options", application.ErrInvalidName)
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
	if !containerNames(p, true)[opts.Container] {
		return nil, fmt.Errorf("%w: container not in pod", application.ErrInvalidName)
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
	rc, err := c.kube.CoreV1().Pods(w.Namespace).GetLogs(pod, po).Stream(ctx)
	if err != nil {
		return nil, a.fail("open logs", w.Cluster, err)
	}
	return rc, nil
}
