package application

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"sync"
	"time"

	"go.uber.org/zap"

	"tiphys/internal/domain"
	"tiphys/internal/monitoring"
)

// MaxLogDownloadBytes caps a log download.
const MaxLogDownloadBytes = 50 << 20

// restartDeadline is the default progressDeadlineSeconds.
const restartDeadline = 600 * time.Second

// jobWatchTimeout is how long a manual job's status is streamed.
const jobWatchTimeout = 10 * time.Minute

// WorkloadService serves the developer-facing verbs. Every method checks
// access through AccessService and calls the cluster only through the
// ClusterAccess port.
type WorkloadService struct {
	access   *AccessService
	kube     ClusterAccess
	argo     *ArgoService
	audit    *AuditService
	streams  *StreamManager
	ops      *Operations
	settings *SettingsService
	notify   *NotificationService
	clock    Clock
	rootCtx  context.Context
	logger   *zap.Logger

	nsMu    sync.Mutex
	nsCache map[string]liveNamespaces

	liveMu   sync.Mutex
	liveOpen map[int64]int // open live streams per user
}

// liveNamespaces is a cluster's namespace list from Kubernetes, cached briefly
// so pickers and the namespace view do not hit the API server on every request.
// A failed call is cached too (as empty), so an unreachable cluster is not
// retried on every request.
type liveNamespaces struct {
	names []string
	at    time.Time
}

const (
	liveNamespaceTTL     = 30 * time.Second
	liveNamespaceTimeout = 5 * time.Second
)

func NewWorkloadService(rootCtx context.Context, access *AccessService, kube ClusterAccess, argo *ArgoService, audit *AuditService,
	streams *StreamManager, ops *Operations, settings *SettingsService, notify *NotificationService, clock Clock, logger *zap.Logger) *WorkloadService {
	return &WorkloadService{
		access: access, kube: kube, argo: argo, audit: audit, streams: streams, ops: ops,
		settings: settings, notify: notify, clock: clock, rootCtx: rootCtx, logger: logger,
		nsCache: map[string]liveNamespaces{}, liveOpen: map[int64]int{},
	}
}

// AppView is an Argo application the user can use.
type AppView struct {
	App         domain.ArgoApp
	Environment string
	Access      []domain.Action
}

// NamespaceView is a namespace the user can use.
type NamespaceView struct {
	Cluster      string
	Namespace    string
	Environment  string
	Apps         []string
	ArgoProjects []string
	Access       []domain.Action
}

// WorkloadView is a workload with the user's decisions for it.
type WorkloadView struct {
	Info        WorkloadInfo
	Cluster     string
	Environment string
	Access      map[domain.Action]domain.Decision
}

// WorkloadDetailView adds detail to a WorkloadView.
type WorkloadDetailView struct {
	WorkloadView
	ArgoApp      string
	CronJobCount int
}

// CronJobView is a CronJob with its recent jobs.
type CronJobView struct {
	WorkloadView
	CronJobInfo CronJobInfo
}

// Apps lists the Argo applications the principal holds grants for, on the
// usable clusters only (Argo may deploy to clusters the portal does not know).
func (s *WorkloadService) Apps(p Principal) []AppView {
	usable := map[string]bool{}
	for _, cl := range s.access.UsableClusters() {
		usable[cl] = true
	}
	var out []AppView
	for _, a := range s.argo.Apps() {
		if !usable[a.Cluster] || s.access.IsProtected(a.Namespace) {
			continue
		}
		acts := s.access.GrantedActions(p, a.Cluster, a.Namespace)
		if len(acts) == 0 {
			continue
		}
		out = append(out, AppView{App: a, Environment: s.access.Environment(a.Cluster, a.Namespace), Access: acts})
	}
	return out
}

// liveNamespaces returns the namespace names of a cluster read directly from
// Kubernetes, or nil if the cluster cannot be read.
func (s *WorkloadService) liveNamespaces(ctx context.Context, p Principal, cluster string) []string {
	s.nsMu.Lock()
	if e, ok := s.nsCache[cluster]; ok && s.clock.Now().Sub(e.at) < liveNamespaceTTL {
		s.nsMu.Unlock()
		return e.names
	}
	s.nsMu.Unlock()

	cctx, cancel := context.WithTimeout(ctx, liveNamespaceTimeout)
	defer cancel()
	names, err := s.kube.ListNamespaces(cctx, p.KubeActor(nil), cluster)
	if err != nil {
		s.logger.Debug("could not list namespaces from the cluster", zap.String("cluster", cluster), zap.Error(err))
		names = nil
	}
	s.nsMu.Lock()
	s.nsCache[cluster] = liveNamespaces{names: names, at: s.clock.Now()}
	s.nsMu.Unlock()
	return names
}

// CatalogNamespaces lists a cluster's namespaces for pickers: the ones read
// from Kubernetes, protected namespaces removed; none of a protected cluster
// for non-admins.
func (s *WorkloadService) CatalogNamespaces(ctx context.Context, p Principal, cluster string) []string {
	out := []string{}
	if s.access.HiddenCluster(p, cluster) {
		return out
	}
	for _, ns := range s.liveNamespaces(ctx, p, cluster) {
		if !s.access.IsProtected(ns) {
			out = append(out, ns)
		}
	}
	sort.Strings(out)
	return out
}

// Namespaces lists the namespaces the principal holds grants for. The
// universe is every namespace read from the usable clusters and those named by
// the principal's grants; Argo CD only annotates them with apps and projects.
func (s *WorkloadService) Namespaces(ctx context.Context, p Principal) []NamespaceView {
	type key = domain.ClusterNS
	byKey := map[key]*NamespaceView{}
	add := func(cluster, ns string) *NamespaceView {
		k := key{Cluster: cluster, Namespace: ns}
		if v, ok := byKey[k]; ok {
			return v
		}
		if s.access.IsProtected(ns) {
			return nil
		}
		acts := s.access.GrantedActions(p, cluster, ns)
		if len(acts) == 0 {
			return nil
		}
		v := &NamespaceView{Cluster: cluster, Namespace: ns, Environment: s.access.Environment(cluster, ns), Access: acts}
		byKey[k] = v
		return v
	}
	// Namespaces read directly from each usable cluster (concurrently).
	clusters := s.access.UsableClusters()
	live := make([][]string, len(clusters))
	var wg sync.WaitGroup
	for i, cl := range clusters {
		wg.Add(1)
		go func() {
			defer wg.Done()
			live[i] = s.liveNamespaces(ctx, p, cl)
		}()
	}
	wg.Wait()
	for i, cl := range clusters {
		for _, ns := range live[i] {
			add(cl, ns)
		}
	}
	// Namespaces named by namespace/workload grants but not seen elsewhere, on
	// registered clusters.
	snap := s.access.policy.Current()
	for _, g := range snap.Policy.UserGrants(s.access.subjectOrZero(p.User.ID)) {
		if _, ok := snap.Clusters[g.Scope.Cluster]; ok && g.Scope.Namespace != "" {
			add(g.Scope.Cluster, g.Scope.Namespace)
		}
	}
	// Argo CD apps on clusters or in namespaces not found above are left out.
	for _, a := range s.argo.Apps() {
		for _, pair := range a.Pairs() {
			if v := byKey[pair]; v != nil {
				v.Apps = append(v.Apps, a.Name)
				if !containsStr(v.ArgoProjects, a.Project) {
					v.ArgoProjects = append(v.ArgoProjects, a.Project)
				}
			}
		}
	}
	out := make([]NamespaceView, 0, len(byKey))
	for _, v := range byKey {
		sort.Strings(v.Apps)
		sort.Strings(v.ArgoProjects)
		out = append(out, *v)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Cluster != out[j].Cluster {
			return out[i].Cluster < out[j].Cluster
		}
		return out[i].Namespace < out[j].Namespace
	})
	return out
}

func containsStr(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

func (s *WorkloadService) kubeErr(cluster string, err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, ErrProtectedNamespace), errors.Is(err, ErrPodNotInWorkload), errors.Is(err, ErrInvalidName),
		errors.Is(err, ErrAlreadyRunning), errors.Is(err, ErrNotFound), errors.Is(err, context.Canceled):
		return err
	}
	return &ClusterError{Cluster: cluster, Err: err}
}

func nsTarget(cluster, ns string) domain.Target {
	return domain.Target{Cluster: cluster, Namespace: ns}
}

func wlTarget(cluster, ns string, ref domain.WorkloadRef) domain.Target {
	return domain.Target{Cluster: cluster, Namespace: ns, Workload: ref}
}

func validateNames(ns string, ref domain.WorkloadRef) error {
	if !domain.ValidDNS1123Label(ns) {
		return Invalid("namespace", "invalid namespace name")
	}
	if ref.Name != "" {
		if !ref.Kind.Valid() {
			return Invalid("kind", "unknown workload kind")
		}
		if !domain.ValidDNS1123Subdomain(ref.Name) {
			return Invalid("name", "invalid workload name")
		}
	}
	return nil
}

// requireAnyGrant denies (and audits) when the principal has no usable
// grant for the namespace, so a probe of a namespace gives a clear reason.
func (s *WorkloadService) requireAnyGrant(ctx context.Context, p Principal, cluster, ns string) error {
	if len(s.access.GrantedActions(p, cluster, ns)) > 0 {
		return nil
	}
	_, err := s.access.Authorize(ctx, p, nsTarget(cluster, ns), domain.ActionLogs)
	if err == nil {
		return &DeniedError{Decision: domain.Decision{Reason: domain.ReasonNoGrant}, Target: nsTarget(cluster, ns), Action: domain.ActionLogs}
	}
	return err
}

// requireWorkloadGrant returns the principal's access to a workload, and
// denies (and audits) when it holds no grant for any action on it.
func (s *WorkloadService) requireWorkloadGrant(ctx context.Context, p Principal, t domain.Target) (map[domain.Action]domain.Decision, error) {
	am := s.access.AccessMap(p, t)
	if holdsAny(am) {
		return am, nil
	}
	_, err := s.access.Authorize(ctx, p, t, domain.ActionLogs)
	if err == nil {
		err = &DeniedError{Decision: am[domain.ActionLogs], Target: t, Action: domain.ActionLogs}
	}
	return nil, err
}

// WhoHasAccess lists the unexpired grants that reach a namespace or workload,
// for a principal who may open it (the same check as Workloads and Workload).
func (s *WorkloadService) WhoHasAccess(ctx context.Context, p Principal, t domain.Target) ([]domain.Grant, error) {
	if err := validateNames(t.Namespace, t.Workload); err != nil {
		return nil, err
	}
	var err error
	if t.HasWorkload() {
		_, err = s.requireWorkloadGrant(ctx, p, t)
	} else {
		err = s.requireAnyGrant(ctx, p, t.Cluster, t.Namespace)
	}
	if err != nil {
		return nil, err
	}
	return s.access.GrantsReaching(t), nil
}

func (s *WorkloadService) view(p Principal, cluster string, info WorkloadInfo) WorkloadView {
	t := wlTarget(cluster, info.Namespace, info.Ref)
	return WorkloadView{
		Info: info, Cluster: cluster, Environment: s.access.Environment(cluster, info.Namespace),
		Access: s.access.AccessMap(p, t),
	}
}

func holdsAny(m map[domain.Action]domain.Decision) bool {
	for _, d := range m {
		if d.HoldsGrant() {
			return true
		}
	}
	return false
}

// Workloads lists the workloads of a namespace the principal may use.
func (s *WorkloadService) Workloads(ctx context.Context, p Principal, cluster, ns string) ([]WorkloadView, error) {
	if err := validateNames(ns, domain.WorkloadRef{}); err != nil {
		return nil, err
	}
	if err := s.requireAnyGrant(ctx, p, cluster, ns); err != nil {
		return nil, err
	}
	infos, err := s.kube.ListWorkloads(ctx, p.KubeActor(nil), cluster, ns)
	if err != nil {
		return nil, s.kubeErr(cluster, err)
	}
	var out []WorkloadView
	for _, info := range infos {
		info.Namespace = ns
		v := s.view(p, cluster, info)
		if holdsAny(v.Access) {
			out = append(out, v)
		}
	}
	return out, nil
}

// Workload returns one workload with the user's access to it.
func (s *WorkloadService) Workload(ctx context.Context, p Principal, cluster, ns string, ref domain.WorkloadRef) (WorkloadDetailView, error) {
	if err := validateNames(ns, ref); err != nil {
		return WorkloadDetailView{}, err
	}
	t := wlTarget(cluster, ns, ref)
	am, err := s.requireWorkloadGrant(ctx, p, t)
	if err != nil {
		return WorkloadDetailView{}, err
	}
	info, err := s.kube.GetWorkload(ctx, p.KubeActor(nil), KubeWorkloadTarget{Cluster: cluster, Namespace: ns, Workload: ref})
	if err != nil {
		return WorkloadDetailView{}, s.kubeErr(cluster, err)
	}
	info.Namespace = ns
	d := WorkloadDetailView{WorkloadView: WorkloadView{Info: info, Cluster: cluster, Environment: s.access.Environment(cluster, ns), Access: am}}
	if apps := s.argo.AppsFor(cluster, ns); len(apps) > 0 {
		d.ArgoApp = apps[0].Name
	}
	if crons, err := s.kube.ListCronJobs(ctx, p.KubeActor(nil), cluster, ns); err == nil {
		d.CronJobCount = len(crons)
	}
	return d, nil
}

// Pods lists a workload's pods (needs logs).
func (s *WorkloadService) Pods(ctx context.Context, p Principal, cluster, ns string, ref domain.WorkloadRef) ([]PodInfo, error) {
	if err := validateNames(ns, ref); err != nil {
		return nil, err
	}
	d, err := s.access.Authorize(ctx, p, wlTarget(cluster, ns, ref), domain.ActionLogs)
	if err != nil {
		return nil, err
	}
	pods, err := s.kube.ListPods(ctx, p.KubeActor(d.BreakGlassID), KubeWorkloadTarget{Cluster: cluster, Namespace: ns, Workload: ref})
	return pods, s.kubeErr(cluster, err)
}

// Events lists a workload's events (needs logs).
func (s *WorkloadService) Events(ctx context.Context, p Principal, cluster, ns string, ref domain.WorkloadRef) ([]EventInfo, error) {
	if err := validateNames(ns, ref); err != nil {
		return nil, err
	}
	d, err := s.access.Authorize(ctx, p, wlTarget(cluster, ns, ref), domain.ActionLogs)
	if err != nil {
		return nil, err
	}
	ev, err := s.kube.Events(ctx, p.KubeActor(d.BreakGlassID), KubeWorkloadTarget{Cluster: cluster, Namespace: ns, Workload: ref})
	return ev, s.kubeErr(cluster, err)
}

// NamespaceEvents lists the events of the whole namespace (needs logs on the
// namespace, so a grant on single workloads is not enough: it would reveal
// events of workloads the principal has no access to).
func (s *WorkloadService) NamespaceEvents(ctx context.Context, p Principal, cluster, ns string) ([]EventInfo, error) {
	if err := validateNames(ns, domain.WorkloadRef{}); err != nil {
		return nil, err
	}
	d, err := s.access.Authorize(ctx, p, nsTarget(cluster, ns), domain.ActionLogs)
	if err != nil {
		return nil, err
	}
	ev, err := s.kube.NamespaceEvents(ctx, p.KubeActor(d.BreakGlassID), cluster, ns)
	return ev, s.kubeErr(cluster, err)
}

// NamespaceTraffic shows how traffic reaches the namespace's pods. Like
// NamespaceEvents it needs logs on the whole namespace: the graph names every
// exposed workload, Service and host there.
func (s *WorkloadService) NamespaceTraffic(ctx context.Context, p Principal, cluster, ns string) (TrafficGraph, error) {
	if err := validateNames(ns, domain.WorkloadRef{}); err != nil {
		return TrafficGraph{}, err
	}
	d, err := s.access.Authorize(ctx, p, nsTarget(cluster, ns), domain.ActionLogs)
	if err != nil {
		return TrafficGraph{}, err
	}
	g, err := s.kube.NamespaceTraffic(ctx, p.KubeActor(d.BreakGlassID), cluster, ns)
	return g, s.kubeErr(cluster, err)
}

// CronJobs lists the namespace's CronJobs the principal may use, with recent jobs.
func (s *WorkloadService) CronJobs(ctx context.Context, p Principal, cluster, ns string) ([]CronJobView, error) {
	if err := validateNames(ns, domain.WorkloadRef{}); err != nil {
		return nil, err
	}
	if err := s.requireAnyGrant(ctx, p, cluster, ns); err != nil {
		return nil, err
	}
	crons, err := s.kube.ListCronJobs(ctx, p.KubeActor(nil), cluster, ns)
	if err != nil {
		return nil, s.kubeErr(cluster, err)
	}
	var out []CronJobView
	for _, c := range crons {
		c.Namespace = ns
		v := s.view(p, cluster, c.WorkloadInfo)
		if holdsAny(v.Access) {
			out = append(out, CronJobView{WorkloadView: v, CronJobInfo: c})
		}
	}
	return out, nil
}

// start authorizes an action, then records its "started" audit event. The
// action must not run if that event cannot be written.
func (s *WorkloadService) start(ctx context.Context, p Principal, t domain.Target, a domain.Action, eventType string, details map[string]any) (domain.Decision, domain.AuditEvent, error) {
	d, err := s.access.Authorize(ctx, p, t, a)
	if err != nil {
		return d, domain.AuditEvent{}, err
	}
	e := s.audit.Event(p, eventType)
	fillTarget(&e, t)
	e.Action = string(a)
	e.Environment = d.Environment
	e.BreakGlassID = d.BreakGlassID
	e.Result = domain.ResultAllowed
	e.Details = details
	stored, err := s.audit.Record(ctx, e)
	if err != nil {
		monitoring.ActionsTotal.WithLabelValues(string(a), "failed").Inc()
		return d, stored, fmt.Errorf("audit event could not be written, action not run: %w", err)
	}
	return d, stored, nil
}

// outcome records how an action ended.
func (s *WorkloadService) outcome(started domain.AuditEvent, result domain.AuditResult, reason string, details map[string]any) {
	e := started
	e.ID, e.At = 0, time.Time{}
	e.Result, e.Reason = result, reason
	e.Details = details
	s.audit.RecordBestEffort(s.rootCtx, e)
	monitoring.ActionsTotal.WithLabelValues(started.Action, string(result)).Inc()
}

// OperationStarted is returned by Restart and RunCronJob.
type OperationStarted struct {
	OperationID string
	JobName     string
}

// Restart patches the workload and follows the rollout in the background.
func (s *WorkloadService) Restart(ctx context.Context, p Principal, cluster, ns string, ref domain.WorkloadRef) (OperationStarted, error) {
	if err := validateNames(ns, ref); err != nil {
		return OperationStarted{}, err
	}
	if !ref.Kind.Restartable() {
		return OperationStarted{}, Invalid("kind", "this kind cannot be restarted")
	}
	t := wlTarget(cluster, ns, ref)
	d, started, err := s.start(ctx, p, t, domain.ActionRestart, domain.EventRestart, nil)
	if err != nil {
		return OperationStarted{}, err
	}
	actor := p.KubeActor(d.BreakGlassID)
	kt := KubeWorkloadTarget{Cluster: cluster, Namespace: ns, Workload: ref}
	if err := s.kube.Restart(ctx, actor, kt); err != nil {
		s.outcome(started, domain.ResultFailed, err.Error(), map[string]any{"outcome": "failed"})
		return OperationStarted{}, s.kubeErr(cluster, err)
	}

	op := s.ops.Create(p.User.ID, s.clock.Now())
	go s.followRollout(op, actor, kt, started)
	return OperationStarted{OperationID: op.ID}, nil
}

func (s *WorkloadService) followRollout(op *Operation, actor KubeActor, kt KubeWorkloadTarget, started domain.AuditEvent) {
	ch, err := s.kube.WatchRollout(s.rootCtx, actor, kt, restartDeadline)
	if err != nil {
		op.Publish(OpEvent{Name: "done", Data: OpDone{Outcome: "failed", Message: err.Error()}})
		s.outcome(started, domain.ResultFailed, err.Error(), map[string]any{"outcome": "failed"})
		return
	}
	var last RolloutStatus
	for st := range ch {
		last = st
		op.Publish(OpEvent{Name: "progress", Data: RolloutProgress{Desired: st.Desired, Updated: st.Updated, Ready: st.Ready, Available: st.Available}})
	}
	switch {
	case last.Complete:
		op.Publish(OpEvent{Name: "done", Data: OpDone{Outcome: "succeeded", Message: "Rollout complete"}})
		s.outcome(started, domain.ResultSucceeded, "", map[string]any{"outcome": "succeeded"})
	case last.Failed:
		op.Publish(OpEvent{Name: "done", Data: OpDone{Outcome: "failed", Message: last.Message}})
		s.outcome(started, domain.ResultFailed, last.Message, map[string]any{"outcome": "failed"})
	default:
		msg := "Rollout did not finish within " + restartDeadline.String()
		op.Publish(OpEvent{Name: "done", Data: OpDone{Outcome: "timed_out", Message: msg}})
		s.outcome(started, domain.ResultFailed, "timed_out", map[string]any{"outcome": "timed_out"})
	}
}

// RunCronJob creates a Job from the CronJob and follows its status.
func (s *WorkloadService) RunCronJob(ctx context.Context, p Principal, cluster, ns, name string) (OperationStarted, error) {
	ref := domain.WorkloadRef{Kind: domain.KindCronJob, Name: name}
	if err := validateNames(ns, ref); err != nil {
		return OperationStarted{}, err
	}
	t := wlTarget(cluster, ns, ref)
	d, started, err := s.start(ctx, p, t, domain.ActionRunJob, domain.EventRunJob, nil)
	if err != nil {
		return OperationStarted{}, err
	}
	actor := p.KubeActor(d.BreakGlassID)
	job, err := s.kube.RunCronJob(ctx, actor, cluster, ns, name)
	if err != nil {
		reason := err.Error()
		if errors.Is(err, ErrAlreadyRunning) {
			reason = "already_running"
		}
		s.outcome(started, domain.ResultFailed, reason, map[string]any{"outcome": "failed"})
		return OperationStarted{}, s.kubeErr(cluster, err)
	}
	op := s.ops.Create(p.User.ID, s.clock.Now())
	go s.followJob(op, actor, cluster, ns, job, started)
	return OperationStarted{OperationID: op.ID, JobName: job}, nil
}

func (s *WorkloadService) followJob(op *Operation, actor KubeActor, cluster, ns, job string, started domain.AuditEvent) {
	ch, err := s.kube.WatchJob(s.rootCtx, actor, cluster, ns, job, jobWatchTimeout)
	if err != nil {
		op.Publish(OpEvent{Name: "done", Data: OpDone{Outcome: "failed", Message: err.Error()}})
		s.outcome(started, domain.ResultFailed, err.Error(), map[string]any{"outcome": "failed", "job": job})
		return
	}
	var last JobInfo
	for ji := range ch {
		last = ji
		op.Publish(OpEvent{Name: "progress", Data: ji})
	}
	details := map[string]any{"job": job}
	switch last.Status {
	case "succeeded":
		op.Publish(OpEvent{Name: "done", Data: OpDone{Outcome: "succeeded", Message: "Job succeeded"}})
		details["outcome"] = "succeeded"
		s.outcome(started, domain.ResultSucceeded, "", details)
	case "failed":
		op.Publish(OpEvent{Name: "done", Data: OpDone{Outcome: "failed", Message: "Job failed"}})
		details["outcome"] = "failed"
		s.outcome(started, domain.ResultFailed, "job failed", details)
	default:
		op.Publish(OpEvent{Name: "done", Data: OpDone{Outcome: "timed_out", Message: "Job still running after " + jobWatchTimeout.String()}})
		details["outcome"] = "timed_out"
		s.outcome(started, domain.ResultFailed, "timed_out", details)
	}
}

// LogRequest are the validated-by-service options for logs.
type LogRequest struct {
	Cluster      string
	Namespace    string
	Workload     domain.WorkloadRef
	Pod          string
	Container    string
	Previous     bool
	Follow       bool
	TailLines    int64
	SinceSeconds int64
	Timestamps   bool
}

func (r *LogRequest) normalize() error {
	if err := validateNames(r.Namespace, r.Workload); err != nil {
		return err
	}
	if !domain.ValidDNS1123Subdomain(r.Pod) {
		return Invalid("pod", "invalid pod name")
	}
	if r.Container != "" && !domain.ValidDNS1123Label(r.Container) {
		return Invalid("container", "invalid container name")
	}
	switch r.TailLines {
	case 0:
		r.TailLines = 100
	case 100, 1000, 10000:
	default:
		return Invalid("tailLines", "must be 100, 1000 or 10000")
	}
	switch r.SinceSeconds {
	case 0, 300, 3600, 86400:
	default:
		return Invalid("sinceSeconds", "must be 300, 3600 or 86400")
	}
	return nil
}

func (r LogRequest) target() domain.Target {
	t := wlTarget(r.Cluster, r.Namespace, r.Workload)
	t.Pod, t.Container = r.Pod, r.Container
	return t
}

func (r LogRequest) kubeOpts() LogOptions {
	return LogOptions{Container: r.Container, Previous: r.Previous, Follow: r.Follow,
		TailLines: r.TailLines, SinceSeconds: r.SinceSeconds, Timestamps: r.Timestamps}
}

// LogStream is an open log stream.
type LogStream struct {
	Body     io.ReadCloser
	Stream   *Stream
	Decision domain.Decision
}

// OpenLogs authorizes, audits and opens a live log stream. Log content is
// passed through unchanged and never stored; only the view is audited.
func (s *WorkloadService) OpenLogs(ctx context.Context, p Principal, req LogRequest) (*LogStream, error) {
	if err := req.normalize(); err != nil {
		return nil, err
	}
	t := req.target()
	d, _, err := s.start(ctx, p, t, domain.ActionLogs, domain.EventLogsView, map[string]any{
		"previous": req.Previous, "follow": req.Follow, "tailLines": req.TailLines,
		"sinceSeconds": req.SinceSeconds, "timestamps": req.Timestamps,
	})
	if err != nil {
		return nil, err
	}
	body, err := s.kube.Logs(ctx, p.KubeActor(d.BreakGlassID),
		KubeWorkloadTarget{Cluster: req.Cluster, Namespace: req.Namespace, Workload: req.Workload}, req.Pod, req.kubeOpts())
	if err != nil {
		return nil, s.kubeErr(req.Cluster, err)
	}
	st := s.streams.Open(s.rootCtx, StreamSpec{
		Kind: StreamLogs, UserID: p.User.ID, Username: p.User.Username, WebSessionID: p.SessionID,
		Target: t, Action: domain.ActionLogs,
	}, d)
	return &LogStream{Body: body, Stream: st, Decision: d}, nil
}

// LogDownload is a capped, non-following log read.
type LogDownload struct {
	Data      []byte
	Truncated bool
}

// DownloadLogs reads logs without follow, capped at MaxLogDownloadBytes.
func (s *WorkloadService) DownloadLogs(ctx context.Context, p Principal, req LogRequest) (LogDownload, error) {
	req.Follow = false
	if err := req.normalize(); err != nil {
		return LogDownload{}, err
	}
	t := req.target()
	d, _, err := s.start(ctx, p, t, domain.ActionLogs, domain.EventLogsDownload, map[string]any{
		"previous": req.Previous, "tailLines": req.TailLines, "sinceSeconds": req.SinceSeconds, "timestamps": req.Timestamps,
	})
	if err != nil {
		return LogDownload{}, err
	}
	opts := req.kubeOpts()
	opts.LimitBytes = MaxLogDownloadBytes
	body, err := s.kube.Logs(ctx, p.KubeActor(d.BreakGlassID),
		KubeWorkloadTarget{Cluster: req.Cluster, Namespace: req.Namespace, Workload: req.Workload}, req.Pod, opts)
	if err != nil {
		return LogDownload{}, s.kubeErr(req.Cluster, err)
	}
	defer body.Close()
	var buf bytes.Buffer
	n, err := io.Copy(&buf, io.LimitReader(body, MaxLogDownloadBytes+1))
	if err != nil && !errors.Is(err, io.EOF) {
		return LogDownload{}, s.kubeErr(req.Cluster, err)
	}
	out := LogDownload{Data: buf.Bytes()}
	if n > MaxLogDownloadBytes {
		out.Data, out.Truncated = out.Data[:MaxLogDownloadBytes], true
	}
	return out, nil
}

// Operation returns an operation if the principal may follow it.
func (s *WorkloadService) Operation(p Principal, id string) (*Operation, error) {
	op, ok := s.ops.Get(id)
	if !ok {
		return nil, ErrNotFound
	}
	if op.OwnerID != p.User.ID && !p.IsAdmin() {
		return nil, ErrForbiddenRole
	}
	return op, nil
}
