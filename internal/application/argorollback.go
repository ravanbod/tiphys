package application

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"tiphys/internal/domain"
	"tiphys/internal/monitoring"
)

// maxRollbackResources bounds each resource list of a rollback plan.
const maxRollbackResources = 200

// ArgoRollbackPlan is what rolling an app back to a deploy of its history
// would do, and whether the principal may do it. The decision covers the
// namespaces and cluster-scoped resources the app manages now together with
// those of the deploy's revision, since a rollback applies the revision's
// manifests and never prunes.
type ArgoRollbackPlan struct {
	Deploy     ArgoDeploy
	Decision   domain.Decision
	DecisionAt domain.ClusterNS // the namespace the decision is about
	// AdminOnlyResources are the cluster-scoped resources that leave the
	// rollback to admins; set only when that is why it is denied.
	AdminOnlyResources []domain.ClusterResource
	// Added are the revision's resources the app does not manage now; Kept
	// are the ones it manages now that the revision lacks: they stay, since a
	// rollback never prunes.
	Added, Kept []ArgoResourceRef
	Truncated   bool // Added or Kept had more than maxRollbackResources

	app domain.ArgoApp // the app with the revision's resources included
}

// Rollback starts a rollback of the application (see
// ArgoClient.RollbackApplication).
func (s *ArgoService) Rollback(ctx context.Context, appNamespace, name string, id int64) (ArgoOperation, error) {
	return s.client.RollbackApplication(ctx, appNamespace, name, id)
}

// Manifests returns the objects the app's source generates at a revision.
func (s *ArgoService) Manifests(ctx context.Context, appNamespace, name, revision string) ([]ArgoResourceRef, error) {
	return s.client.Manifests(ctx, appNamespace, name, revision)
}

// AppRollbackPlan shows what a rollback to the deploy would do. It needs the
// rollback right on the app as it is now (a denial is audited); the plan's
// own decision, which also covers the revision's resources, is not.
func (s *WorkloadService) AppRollbackPlan(ctx context.Context, p Principal, cluster, ns, appNamespace, name string, id int64) (ArgoRollbackPlan, error) {
	app, err := s.appFor(ctx, p, cluster, ns, appNamespace, name)
	if err != nil {
		return ArgoRollbackPlan{}, err
	}
	d, err := s.access.AuthorizeApp(ctx, p, app, domain.ActionRollback)
	if err != nil {
		return ArgoRollbackPlan{}, err
	}
	return s.rollbackPlan(ctx, p, app, id, d.BreakGlassID)
}

// RollbackApp rolls an Argo CD application that deploys to the namespace back
// to a deploy of its history. It is allowed only when the principal may roll
// back every namespace the app deploys to now or the deploy's revision
// deploys to, and the cluster-scoped resources of both leave it to them
// (domain.DecideApp). Apps that sync automatically, multi-source apps and
// deploys of a source other than the current one are refused. Like sync, it
// does not run if its "started" audit event cannot be written, and is then
// followed until Argo CD reports the operation finished.
func (s *WorkloadService) RollbackApp(ctx context.Context, p Principal, cluster, ns, appNamespace, name string, id int64) (OperationStarted, error) {
	app, err := s.appFor(ctx, p, cluster, ns, appNamespace, name)
	if err != nil {
		return OperationStarted{}, err
	}
	d, err := s.access.AuthorizeApp(ctx, p, app, domain.ActionRollback)
	if err != nil {
		return OperationStarted{}, err
	}
	plan, err := s.rollbackPlan(ctx, p, app, id, d.BreakGlassID)
	if err != nil {
		return OperationStarted{}, err
	}
	d, err = s.access.AuthorizeApp(ctx, p, plan.app, domain.ActionRollback)
	if err != nil {
		return OperationStarted{}, err
	}
	pairs := plan.app.Pairs()
	namespaces := make([]string, len(pairs))
	for i, pair := range pairs {
		namespaces[i] = pair.Namespace
	}
	e := s.audit.Event(p, domain.EventArgoRollback)
	e.Cluster, e.Namespace = app.Cluster, app.Namespace
	e.Action = string(domain.ActionRollback)
	e.Environment = d.Environment
	e.BreakGlassID = d.BreakGlassID
	e.Result = domain.ResultAllowed
	e.Details = map[string]any{"app": app.Name, "appNamespace": app.AppNamespace, "project": app.Project, "namespaces": namespaces,
		"historyId": plan.Deploy.ID, "revision": plan.Deploy.Revision}
	if rs := plan.app.ForeignClusterResources(); len(rs) > 0 {
		names := make([]string, len(rs))
		for i, r := range rs {
			names[i] = r.String()
		}
		e.Details["clusterResources"] = names
	}
	started, err := s.audit.Record(ctx, e)
	if err != nil {
		monitoring.ActionsTotal.WithLabelValues(string(domain.ActionRollback), "failed").Inc()
		return OperationStarted{}, fmt.Errorf("audit event could not be written, rollback not run: %w", err)
	}

	prev, err := s.argo.Rollback(ctx, app.AppNamespace, app.Name, plan.Deploy.ID)
	if err != nil {
		details := map[string]any{"app": app.Name, "outcome": "failed"}
		switch {
		case errors.Is(err, ErrSyncInProgress):
			s.outcome(started, domain.ResultFailed, "already_running", details)
			return OperationStarted{}, err
		case errors.Is(err, ErrAutoSyncEnabled):
			s.outcome(started, domain.ResultFailed, "auto_sync_enabled", details)
			return OperationStarted{}, err
		}
		s.outcome(started, domain.ResultFailed, err.Error(), details)
		return OperationStarted{}, &ArgoError{Err: err}
	}
	op := s.ops.Create(p.User.ID, s.clock.Now())
	go s.followArgoOperation(op, app, prev, started, "Rollback")
	return OperationStarted{OperationID: op.ID}, nil
}

// rollbackPlan reads the deploy, the manifests of its revision and the
// cluster's resource scopes, and decides the rollback on the app with the
// revision's resources included.
func (s *WorkloadService) rollbackPlan(ctx context.Context, p Principal, app domain.ArgoApp, id int64, breakGlassID *int64) (ArgoRollbackPlan, error) {
	if app.AutoSync {
		return ArgoRollbackPlan{}, ErrAutoSyncEnabled
	}
	det, err := s.argo.Details(ctx, app.AppNamespace, app.Name)
	if err != nil {
		return ArgoRollbackPlan{}, &ArgoError{Err: err}
	}
	if det.AutoSync {
		return ArgoRollbackPlan{}, ErrAutoSyncEnabled
	}
	if det.MultiSource {
		return ArgoRollbackPlan{}, Invalid("id", "Multi-source applications cannot be rolled back here; use Argo CD.")
	}
	i := slices.IndexFunc(det.History, func(h ArgoDeploy) bool { return h.ID == id })
	switch {
	case i < 0:
		return ArgoRollbackPlan{}, Invalid("id", "This deploy is not in the application's history.")
	case i == len(det.History)-1:
		return ArgoRollbackPlan{}, Invalid("id", "This deploy is the one running now.")
	}
	dep := det.History[i]
	if dep.Revision == "" || !dep.SameSource {
		return ArgoRollbackPlan{}, Invalid("id", "The application's source changed since this deploy, so the portal cannot check what it would deploy; use Argo CD.")
	}
	objs, err := s.argo.Manifests(ctx, app.AppNamespace, app.Name, dep.Revision)
	if err != nil {
		return ArgoRollbackPlan{}, &ArgoError{Err: err}
	}
	scopes, err := s.kube.ResourceScopes(ctx, p.KubeActor(breakGlassID), app.Cluster)
	if err != nil {
		return ArgoRollbackPlan{}, s.kubeErr(app.Cluster, err)
	}
	resolved, namespaces, clusterResources := resolveManifests(objs, scopes, app.Namespace)
	plan := ArgoRollbackPlan{Deploy: dep, app: app.Including(namespaces, clusterResources)}
	plan.Decision, plan.DecisionAt = s.access.DecideApp(p, plan.app, domain.ActionRollback)
	if plan.Decision.Reason == domain.ReasonAppScope {
		plan.AdminOnlyResources = s.access.AdminOnlyResources(plan.app)
	}
	plan.Added = refsMissing(resolved, det.Resources)
	plan.Kept = refsMissing(det.Resources, resolved)
	for _, l := range []*[]ArgoResourceRef{&plan.Added, &plan.Kept} {
		if len(*l) > maxRollbackResources {
			*l, plan.Truncated = (*l)[:maxRollbackResources], true
		}
	}
	return plan, nil
}

// resolveManifests places each object as the cluster would: a namespaced
// object without a namespace goes to the destination (or "default" when the
// app has none), a cluster-scoped one has no namespace. A kind the cluster
// does not serve counts as cluster-scoped, so it can only make the decision
// stricter.
func resolveManifests(objs []ArgoResourceRef, scopes map[KubeGroupKind]bool, destination string) ([]ArgoResourceRef, []string, []domain.ClusterResource) {
	if destination == "" {
		destination = "default"
	}
	resolved := make([]ArgoResourceRef, 0, len(objs))
	var namespaces []string
	var clusterResources []domain.ClusterResource
	for _, o := range objs {
		namespaced, known := scopes[KubeGroupKind{Group: o.Group, Kind: o.Kind}]
		switch {
		case known && namespaced:
			if o.Namespace == "" {
				o.Namespace = destination
			}
			if !slices.Contains(namespaces, o.Namespace) {
				namespaces = append(namespaces, o.Namespace)
			}
		default:
			o.Namespace = ""
			clusterResources = append(clusterResources, domain.ClusterResource{Group: o.Group, Kind: o.Kind, Name: o.Name})
		}
		resolved = append(resolved, o)
	}
	return resolved, namespaces, clusterResources
}

// refsMissing returns the resources of a that are not in b.
func refsMissing(a, b []ArgoResourceRef) []ArgoResourceRef {
	in := make(map[ArgoResourceRef]struct{}, len(b))
	for _, r := range b {
		in[r] = struct{}{}
	}
	var out []ArgoResourceRef
	for _, r := range a {
		if _, ok := in[r]; !ok {
			out = append(out, r)
		}
	}
	return out
}

// argoDeployers finds who started the app's deploys through the portal: the
// outcome events of argo.sync and argo.rollback carry the operation's
// startedAt, which Argo CD records as the deploy's. Keyed by startedAt.
func (s *WorkloadService) argoDeployers(ctx context.Context, app domain.ArgoApp, since time.Time) map[string]argoDeployer {
	out := map[string]argoDeployer{}
	for _, t := range []string{domain.EventArgoSync, domain.EventArgoRollback} {
		evs, err := s.audit.Query(ctx, AuditFilter{EventType: t, Cluster: app.Cluster, Namespace: app.Namespace, From: since, Limit: 500})
		if err != nil {
			continue // the history still shows Argo CD's own initiator
		}
		for _, e := range evs {
			at, _ := e.Details[detailOperationStartedAt].(string)
			if at == "" || e.Details["app"] != app.Name {
				continue
			}
			if _, ok := out[at]; !ok {
				out[at] = argoDeployer{Username: e.Username, Action: t}
			}
		}
	}
	return out
}

type argoDeployer struct {
	Username string
	Action   string // the audit event type
}

// detailOperationStartedAt is the outcome detail naming the Argo CD
// operation's startedAt (RFC 3339, seconds, as Argo CD keeps it).
const detailOperationStartedAt = "operationStartedAt"

func operationStartedAt(t time.Time) string { return t.UTC().Format(time.RFC3339) }
