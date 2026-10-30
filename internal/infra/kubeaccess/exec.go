package kubeaccess

import (
	"context"
	"fmt"
	"io"
	"sync"
	"sync/atomic"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/remotecommand"

	"tiphys/internal/application"
	"tiphys/internal/domain"
)

// Exec opens a TTY shell in a container over SPDY. The command is the
// allow-listed shell and nothing else.
func (a *Access) Exec(ctx context.Context, actor application.KubeActor, w application.KubeWorkloadTarget, pod, container, shell string, cols, rows uint16) (application.ExecStream, error) {
	if !domain.ValidShell(shell) {
		return nil, fmt.Errorf("%w: shell", application.ErrInvalidName)
	}
	if !domain.ValidDNS1123Subdomain(container) {
		return nil, fmt.Errorf("%w: container", application.ErrInvalidName)
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
	if !containerNames(p, false)[container] {
		return nil, fmt.Errorf("%w: container not in pod", application.ErrInvalidName)
	}
	return a.openShell(ctx, c, w.Cluster, w.Namespace, pod, container, shell, cols, rows)
}

// openShell starts the allow-listed shell in a container that was checked by
// the caller.
func (a *Access) openShell(ctx context.Context, c *clients, cluster, namespace, pod, container, shell string, cols, rows uint16) (application.ExecStream, error) {
	req := c.kube.CoreV1().RESTClient().Post().
		Resource("pods").Namespace(namespace).Name(pod).SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{
			Container: container,
			Command:   []string{shell},
			Stdin:     true,
			Stdout:    true,
			Stderr:    false, // TTY merges stderr into stdout
			TTY:       true,
		}, scheme.ParameterCodec)
	exec, err := remotecommand.NewSPDYExecutor(c.cfg, "POST", req.URL())
	if err != nil {
		return nil, a.fail("build exec", cluster, err)
	}
	return startExec(ctx, exec, cols, rows), nil
}

// execStream adapts a remotecommand stream to application.ExecStream.
type execStream struct {
	stdin  *io.PipeWriter
	out    *io.PipeReader
	sizes  chan remotecommand.TerminalSize
	cancel context.CancelFunc
	done   chan struct{}
	err    error
	once   sync.Once
	closed atomic.Bool
}

func startExec(ctx context.Context, exec remotecommand.Executor, cols, rows uint16) *execStream {
	ctx, cancel := context.WithCancel(ctx)
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	s := &execStream{
		stdin:  inW,
		out:    outR,
		sizes:  make(chan remotecommand.TerminalSize, 8),
		cancel: cancel,
		done:   make(chan struct{}),
	}
	s.sizes <- remotecommand.TerminalSize{Width: cols, Height: rows}
	go func() {
		err := exec.StreamWithContext(ctx, remotecommand.StreamOptions{
			Stdin:             inR,
			Stdout:            discardOnClose{outW},
			Tty:               true,
			TerminalSizeQueue: sizeQueue{ch: s.sizes, ctx: ctx},
		})
		if s.closed.Load() {
			err = nil
		}
		s.err = err
		outW.CloseWithError(io.EOF)
		inR.CloseWithError(io.EOF)
		close(s.done)
	}()
	return s
}

// discardOnClose keeps client-go from logging a failed copy when the reader of
// the output pipe has gone away: the output is dropped instead.
type discardOnClose struct{ w *io.PipeWriter }

func (d discardOnClose) Write(p []byte) (int, error) {
	d.w.Write(p)
	return len(p), nil
}

type sizeQueue struct {
	ch  <-chan remotecommand.TerminalSize
	ctx context.Context
}

func (q sizeQueue) Next() *remotecommand.TerminalSize {
	select {
	case sz := <-q.ch:
		return &sz
	case <-q.ctx.Done():
		return nil
	}
}

func (s *execStream) Write(p []byte) (int, error) { return s.stdin.Write(p) }
func (s *execStream) Output() io.Reader           { return s.out }
func (s *execStream) Done() <-chan struct{}       { return s.done }

// Err is valid once Done is closed. A session ended by Close reports nil.
func (s *execStream) Err() error {
	select {
	case <-s.done:
		return s.err
	default:
		return nil
	}
}

// Resize drops the update when the queue is full; the next one carries the
// current size anyway.
func (s *execStream) Resize(cols, rows uint16) {
	select {
	case s.sizes <- remotecommand.TerminalSize{Width: cols, Height: rows}:
	default:
	}
}

func (s *execStream) Close() error {
	s.once.Do(func() {
		s.closed.Store(true)
		s.cancel()
		s.stdin.Close()
		s.out.Close()
	})
	return nil
}
