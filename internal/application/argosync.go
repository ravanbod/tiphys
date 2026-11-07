package application

import (
	"context"
	"errors"
	"fmt"
	"time"

	"tiphys/internal/domain"
	"tiphys/internal/monitoring"
)

const (
	argoSyncDeadline = 10 * time.Minute
	argoSyncPoll     = 3 * time.Second
)

// NamespaceAppView is an Argo CD application on the namespace page, with
// whether the principal may sync it; SyncAt is the namespace that decision
// is about (the one that denied, or the destination). AdminOnlyResources
// names the cluster-scoped resources that leave the sync to admins, set only
// when that is why it was denied (domain.ReasonAppScope).
type NamespaceAppView struct {
	App                domain.ArgoApp
	Sync               domain.Decision
	SyncAt             domain.ClusterNS
	AdminOnlyResources []domain.ClusterResource
	// Rollback is the decision for rolling the app back as it is now; a
	// rollback to a deploy is decided again with the deploy's resources
	// (ArgoRollbackPlan).
	Rollback domain.Decision
}

// ArgoSyncProgress is the payload of a sync's progress events.
type ArgoSyncProgress struct {
	Phase   string `json:"phase"` // Pending, Running, Terminating, ...
	Message string `json:"message"`
	Synced  int    `json:"synced"`
	Failed  int    `json:"failed"`
	Total   int    `json:"total"`
}

// NamespaceApps lists the Argo CD applications deploying to a namespace (its
// destination or any of its resources), for a principal who may open it.
func (s *WorkloadService) NamespaceApps(ctx context.Context, p Principal, cluster, ns string) ([]NamespaceAppView, error) {
	if err := validateNames(ns, domain.WorkloadRef{}); err != nil {
		return nil, err
	}
	if err := s.requireAnyGrant(ctx, p, cluster, ns); err != nil {
		return nil, err
	}
	apps := s.argo.AppsFor(cluster, ns)
	out := make([]NamespaceAppView, len(apps))
	for i, a := range apps {
		d, at := s.access.DecideApp(p, a, domain.ActionSync)
		rb, _ := s.access.DecideApp(p, a, domain.ActionRollback)
		out[i] = NamespaceAppView{App: a, Sync: d, SyncAt: at, Rollback: rb}
		if d.Reason == domain.ReasonAppScope {
			out[i].AdminOnlyResources = s.access.AdminOnlyResources(a)
		}
	}
	return out, nil
}

// SyncApp syncs an Argo CD application that deploys to the namespace. It is
// allowed only when the principal may sync every namespace the app deploys
// to (domain.DecideApp). Like the workload actions, it does not run if its
// "started" audit event cannot be written; the sync is then followed in the
// background until Argo CD reports the operation finished.
func (s *WorkloadService) SyncApp(ctx context.Context, p Principal, cluster, ns, appNamespace, name string) (OperationStarted, error) {
	app, err := s.appFor(ctx, p, cluster, ns, appNamespace, name)
	if err != nil {
		return OperationStarted{}, err
	}
	d, err := s.access.AuthorizeApp(ctx, p, app, domain.ActionSync)
	if err != nil {
		return OperationStarted{}, err
	}
	namespaces := make([]string, 0, len(app.Pairs()))
	for _, pair := range app.Pairs() {
		namespaces = append(namespaces, pair.Namespace)
	}
	e := s.audit.Event(p, domain.EventArgoSync)
	e.Cluster, e.Namespace = app.Cluster, app.Namespace
	e.Action = string(domain.ActionSync)
	e.Environment = d.Environment
	e.BreakGlassID = d.BreakGlassID
	e.Result = domain.ResultAllowed
	e.Details = map[string]any{"app": app.Name, "appNamespace": app.AppNamespace, "project": app.Project, "namespaces": namespaces}
	// What the sync applies beyond namespaces: kinds the settings allow, or
	// anything for an admin.
	if rs := app.ForeignClusterResources(); len(rs) > 0 {
		names := make([]string, len(rs))
		for i, r := range rs {
			names[i] = r.String()
		}
		e.Details["clusterResources"] = names
	}
	started, err := s.audit.Record(ctx, e)
	if err != nil {
		monitoring.ActionsTotal.WithLabelValues(string(domain.ActionSync), "failed").Inc()
		return OperationStarted{}, fmt.Errorf("audit event could not be written, sync not run: %w", err)
	}

	prev, err := s.argo.Sync(ctx, app.AppNamespace, app.Name, p.Username())
	if err != nil {
		if errors.Is(err, ErrSyncInProgress) {
			s.outcome(started, domain.ResultFailed, "already_running", map[string]any{"app": app.Name, "outcome": "failed"})
			return OperationStarted{}, err
		}
		s.outcome(started, domain.ResultFailed, err.Error(), map[string]any{"app": app.Name, "outcome": "failed"})
		return OperationStarted{}, &ArgoError{Err: err}
	}
	op := s.ops.Create(p.User.ID, s.clock.Now())
	go s.followArgoOperation(op, app, prev, started, "Sync")
	return OperationStarted{OperationID: op.ID}, nil
}

// namespaceApp finds an application deploying to the namespace in the cache.
func (s *WorkloadService) namespaceApp(cluster, ns, appNamespace, name string) (domain.ArgoApp, bool) {
	for _, a := range s.argo.AppsFor(cluster, ns) {
		if a.Name == name && a.AppNamespace == appNamespace {
			return a, true
		}
	}
	return domain.ArgoApp{}, false
}

// followArgoOperation polls the application until the sync or rollback
// (what) it started finishes (an operation that started after prev), then
// refreshes the Argo cache so the namespace page shows the new state. Failed
// polls are retried until the deadline. The outcome names the operation's
// startedAt, which Argo CD records on the deploy (argoDeployers).
func (s *WorkloadService) followArgoOperation(op *Operation, app domain.ArgoApp, prev ArgoOperation, started domain.AuditEvent, what string) {
	ctx, cancel := context.WithTimeout(s.rootCtx, argoSyncDeadline)
	defer cancel()
	defer func() { _ = s.argo.Refresh(s.rootCtx) }()
	details := map[string]any{"app": app.Name}
	var last ArgoSyncProgress
	for {
		cur, err := s.argo.Operation(ctx, app.AppNamespace, app.Name)
		if err == nil {
			p := ArgoSyncProgress{Phase: cur.Phase, Message: cur.Message, Synced: cur.Synced, Failed: cur.Failed, Total: cur.Total}
			ours := !cur.StartedAt.IsZero() && !cur.StartedAt.Equal(prev.StartedAt)
			if !ours {
				p = ArgoSyncProgress{Phase: "Pending"}
			}
			if p != last {
				last = p
				op.Publish(OpEvent{Name: "progress", Data: p})
			}
			if ours {
				details[detailOperationStartedAt] = operationStartedAt(cur.StartedAt)
				switch cur.Phase {
				case "Succeeded":
					op.Publish(OpEvent{Name: "done", Data: OpDone{Outcome: "succeeded", Message: orText(cur.Message, what+" succeeded")}})
					details["outcome"] = "succeeded"
					s.outcome(started, domain.ResultSucceeded, "", details)
					return
				case "Failed", "Error":
					op.Publish(OpEvent{Name: "done", Data: OpDone{Outcome: "failed", Message: orText(cur.Message, what+" failed")}})
					details["outcome"] = "failed"
					s.outcome(started, domain.ResultFailed, cur.Message, details)
					return
				}
			}
		}
		select {
		case <-ctx.Done():
			msg := what + " did not finish within " + argoSyncDeadline.String()
			op.Publish(OpEvent{Name: "done", Data: OpDone{Outcome: "timed_out", Message: msg}})
			details["outcome"] = "timed_out"
			s.outcome(started, domain.ResultFailed, "timed_out", details)
			return
		case <-time.After(argoSyncPoll):
		}
	}
}

func orText(s, def string) string {
	if s != "" {
		return s
	}
	return def
}
