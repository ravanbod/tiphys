package integration

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"tiphys/internal/application"
	"tiphys/internal/domain"
)

// fakeKube is an in-memory ClusterAccess. Namespaces "payments-api" and
// "search-api" exist on every cluster; payments-api has a Deployment
// payments-api (pod payments-api-abc) and payments-worker (pod
// payments-worker-xyz), and a CronJob reconcile.
type fakeKube struct {
	mu       sync.Mutex
	restarts []string
	jobs     []string
	execs    []*fakeExec
	debugs   []application.KubeDebugSpec
	actors   []application.KubeActor
	watches  []*fakeWatch
}

type fakeWatch struct {
	ns string
	ch chan application.NamespaceChange
}

func newFakeKube() *fakeKube { return &fakeKube{} }

var _ application.ClusterAccess = (*fakeKube)(nil)

func (k *fakeKube) record(a application.KubeActor) {
	k.mu.Lock()
	k.actors = append(k.actors, a)
	k.mu.Unlock()
}

func (k *fakeKube) ListNamespaces(_ context.Context, a application.KubeActor, cluster string) ([]string, error) {
	k.record(a)
	if cluster == "broken" {
		return nil, fmt.Errorf("cluster unreachable")
	}
	return []string{"billing", "payments-api", "search-api", "live-only"}, nil
}

func (k *fakeKube) ListWorkloads(_ context.Context, a application.KubeActor, cluster, ns string) ([]application.WorkloadInfo, error) {
	k.record(a)
	switch ns {
	case "payments-api":
		return []application.WorkloadInfo{
			{Ref: domain.WorkloadRef{Kind: domain.KindDeployment, Name: "payments-api"}, Namespace: ns, Desired: 3, Ready: 3},
			{Ref: domain.WorkloadRef{Kind: domain.KindDeployment, Name: "payments-worker"}, Namespace: ns, Desired: 2, Ready: 2},
			{Ref: domain.WorkloadRef{Kind: domain.KindCronJob, Name: "reconcile"}, Namespace: ns, Schedule: "*/15 * * * *"},
		}, nil
	case "search-api":
		return []application.WorkloadInfo{{Ref: domain.WorkloadRef{Kind: domain.KindDeployment, Name: "search-api"}, Namespace: ns, Desired: 1, Ready: 1}}, nil
	}
	return nil, nil
}

func (k *fakeKube) GetWorkload(ctx context.Context, a application.KubeActor, w application.KubeWorkloadTarget) (application.WorkloadInfo, error) {
	ws, _ := k.ListWorkloads(ctx, a, w.Cluster, w.Namespace)
	for _, x := range ws {
		if x.Ref == w.Workload {
			return x, nil
		}
	}
	return application.WorkloadInfo{}, application.ErrNotFound
}

func (k *fakeKube) podFor(w application.KubeWorkloadTarget) string {
	switch w.Workload.Name {
	case "payments-api":
		return "payments-api-abc"
	case "payments-worker":
		return "payments-worker-xyz"
	}
	return w.Workload.Name + "-pod"
}

func (k *fakeKube) ListPods(_ context.Context, a application.KubeActor, w application.KubeWorkloadTarget) ([]application.PodInfo, error) {
	k.record(a)
	return []application.PodInfo{{Name: k.podFor(w), Phase: "Running", ReadyCount: 1, TotalCount: 1,
		Containers: []application.ContainerInfo{{Name: "app", Ready: true, State: "running"}}}}, nil
}

func (k *fakeKube) Events(context.Context, application.KubeActor, application.KubeWorkloadTarget) ([]application.EventInfo, error) {
	return []application.EventInfo{{Type: "Normal", Reason: "Started", Message: "Started container app"}}, nil
}

func (k *fakeKube) NamespaceEvents(_ context.Context, _ application.KubeActor, _, ns string) ([]application.EventInfo, error) {
	return []application.EventInfo{
		{Type: "Warning", Reason: "FailedCreate", Message: "exceeded quota", Object: "ReplicaSet/payments-api-1"},
		{Type: "Normal", Reason: "Started", Message: "Started container app", Object: "Pod/payments-api-abc"},
	}, nil
}

func (k *fakeKube) NamespaceTraffic(_ context.Context, _ application.KubeActor, _, ns string) (application.TrafficGraph, error) {
	return application.TrafficGraph{
		Nodes: []application.TrafficNode{
			{ID: "Address//10.0.0.1", Kind: "Address", Name: "10.0.0.1"},
			{ID: "Ingress/" + ns + "/api", Kind: "Ingress", Namespace: ns, Name: "api"},
			{ID: "Service/" + ns + "/api", Kind: "Service", Namespace: ns, Name: "api", Ready: 2, Total: 2},
			{ID: "Deployment/" + ns + "/payments-api", Kind: "Deployment", Namespace: ns, Name: "payments-api", Ready: 2, Total: 2},
		},
		Edges: []application.TrafficEdge{
			{From: "Address//10.0.0.1", To: "Ingress/" + ns + "/api"},
			{From: "Ingress/" + ns + "/api", To: "Service/" + ns + "/api", Labels: []string{"api.example.com/"}},
			{From: "Service/" + ns + "/api", To: "Deployment/" + ns + "/payments-api"},
		},
	}, nil
}

func (k *fakeKube) ListCronJobs(_ context.Context, _ application.KubeActor, _, ns string) ([]application.CronJobInfo, error) {
	if ns != "payments-api" {
		return nil, nil
	}
	return []application.CronJobInfo{{WorkloadInfo: application.WorkloadInfo{Ref: domain.WorkloadRef{Kind: domain.KindCronJob, Name: "reconcile"}, Namespace: ns, ConcurrencyPolicy: "Forbid"}}}, nil
}

func (k *fakeKube) Logs(_ context.Context, a application.KubeActor, w application.KubeWorkloadTarget, pod string, _ application.LogOptions) (io.ReadCloser, error) {
	k.record(a)
	if pod != k.podFor(w) {
		return nil, application.ErrPodNotInWorkload
	}
	return io.NopCloser(strings.NewReader("line one\nline two\n")), nil
}

type fakeExec struct {
	out   *io.PipeReader
	outW  *io.PipeWriter
	done  chan struct{}
	once  sync.Once
	cols  uint16
	rows  uint16
	mu    sync.Mutex
	stdin strings.Builder
}

func (f *fakeExec) Write(p []byte) (int, error) {
	f.mu.Lock()
	f.stdin.Write(p)
	f.mu.Unlock()
	// A tiny "shell": echo the input back.
	_, err := f.outW.Write(append([]byte("$ "), p...))
	return len(p), err
}
func (f *fakeExec) Output() io.Reader        { return f.out }
func (f *fakeExec) Resize(cols, rows uint16) { f.mu.Lock(); f.cols, f.rows = cols, rows; f.mu.Unlock() }
func (f *fakeExec) Done() <-chan struct{}    { return f.done }
func (f *fakeExec) Err() error               { return nil }
func (f *fakeExec) Close() error {
	f.once.Do(func() { close(f.done); f.outW.Close() })
	return nil
}

func (k *fakeKube) Exec(_ context.Context, a application.KubeActor, w application.KubeWorkloadTarget, pod, container, shell string, cols, rows uint16) (application.ExecStream, error) {
	k.record(a)
	if pod != k.podFor(w) {
		return nil, application.ErrPodNotInWorkload
	}
	pr, pw := io.Pipe()
	f := &fakeExec{out: pr, outW: pw, done: make(chan struct{}), cols: cols, rows: rows}
	k.mu.Lock()
	k.execs = append(k.execs, f)
	k.mu.Unlock()
	return f, nil
}

// Debug behaves like Exec; target container "broken" fails like a cluster
// that refused the debug container.
func (k *fakeKube) Debug(ctx context.Context, a application.KubeActor, w application.KubeWorkloadTarget, pod string, spec application.KubeDebugSpec, shell string, cols, rows uint16) (application.ExecStream, error) {
	if spec.Target == "broken" {
		return nil, &application.DebugError{Reason: "ImagePullBackOff: back-off pulling image"}
	}
	s, err := k.Exec(ctx, a, w, pod, spec.Name, shell, cols, rows)
	if err == nil {
		k.mu.Lock()
		k.debugs = append(k.debugs, spec)
		k.mu.Unlock()
	}
	return s, err
}

func (k *fakeKube) Restart(_ context.Context, a application.KubeActor, w application.KubeWorkloadTarget) error {
	k.record(a)
	k.mu.Lock()
	k.restarts = append(k.restarts, w.Workload.String())
	k.mu.Unlock()
	return nil
}

func (k *fakeKube) WatchRollout(context.Context, application.KubeActor, application.KubeWorkloadTarget, time.Duration) (<-chan application.RolloutStatus, error) {
	ch := make(chan application.RolloutStatus, 2)
	ch <- application.RolloutStatus{Desired: 3, Updated: 1, Ready: 2, Available: 2}
	ch <- application.RolloutStatus{Desired: 3, Updated: 3, Ready: 3, Available: 3, Complete: true}
	close(ch)
	return ch, nil
}

func (k *fakeKube) RunCronJob(_ context.Context, a application.KubeActor, cluster, ns, cj string) (string, error) {
	k.record(a)
	k.mu.Lock()
	defer k.mu.Unlock()
	if len(k.jobs) > 0 {
		return "", application.ErrAlreadyRunning
	}
	name := fmt.Sprintf("%s-manual-abcde", cj)
	k.jobs = append(k.jobs, name)
	return name, nil
}

func (k *fakeKube) WatchJob(context.Context, application.KubeActor, string, string, string, time.Duration) (<-chan application.JobInfo, error) {
	ch := make(chan application.JobInfo, 1)
	ch <- application.JobInfo{Name: "reconcile-manual-abcde", Status: "succeeded", Succeeded: 1, Completions: 1}
	close(ch)
	return ch, nil
}

// WatchNamespace follows a namespace until ctx is done; emit sends to it.
func (k *fakeKube) WatchNamespace(ctx context.Context, a application.KubeActor, cluster, ns string) (<-chan application.NamespaceChange, error) {
	k.record(a)
	if cluster == "broken" {
		return nil, fmt.Errorf("cluster unreachable")
	}
	w := &fakeWatch{ns: ns, ch: make(chan application.NamespaceChange, 8)}
	k.mu.Lock()
	k.watches = append(k.watches, w)
	k.mu.Unlock()
	go func() {
		<-ctx.Done()
		k.mu.Lock()
		defer k.mu.Unlock()
		for i, x := range k.watches {
			if x == w {
				k.watches = append(k.watches[:i], k.watches[i+1:]...)
				break
			}
		}
		close(w.ch)
	}()
	return w.ch, nil
}

// emit sends a change to every watch of the namespace.
func (k *fakeKube) emit(ns string, c application.NamespaceChange) {
	k.mu.Lock()
	defer k.mu.Unlock()
	for _, w := range k.watches {
		if w.ns == ns {
			w.ch <- c
		}
	}
}

func (k *fakeKube) ProbeKubeconfig(context.Context, []byte) (application.ClusterProbe, error) {
	return application.ClusterProbe{Version: "v1.31.0", PrivilegeOK: true, AdmissionMode: domain.AdmissionVAP}, nil
}
func (k *fakeKube) Probe(context.Context, string) (application.ClusterProbe, error) {
	return application.ClusterProbe{Version: "v1.31.0", PrivilegeOK: true, AdmissionMode: domain.AdmissionVAP}, nil
}
func (k *fakeKube) Version(context.Context, string) (string, error) { return "v1.31.0", nil }
func (k *fakeKube) Registered() []string                            { return []string{"prod-1", "stg-1"} }
func (k *fakeKube) SetUsable(string, bool)                          {}
