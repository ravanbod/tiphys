package application

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"tiphys/internal/domain"
)

const (
	// maxSyncResources bounds the resources a sync may name.
	maxSyncResources = 500
	// maxSyncRetries bounds a sync's retry limit; maxSyncBackoff its waits.
	maxSyncRetries = 10
	maxSyncBackoff = time.Hour
)

// syncOptionValues are the sync options of Argo CD's sync dialog. A sync may
// also pass on any option the app sets itself (spec.syncPolicy.syncOptions).
var syncOptionValues = map[string]bool{
	"Validate=false":                    true,
	"CreateNamespace=true":              true,
	"PruneLast=true":                    true,
	"ApplyOutOfSyncOnly=true":           true,
	"RespectIgnoreDifferences=true":     true,
	"ServerSideApply=true":              true,
	"Replace=true":                      true,
	"FailOnSharedResource=true":         true,
	"PrunePropagationPolicy=foreground": true,
	"PrunePropagationPolicy=background": true,
	"PrunePropagationPolicy=orphan":     true,
}

// validRevision admits Git refs and SHAs, and Helm chart versions and
// constraints.
var validRevision = regexp.MustCompile(`^[A-Za-z0-9._/@^~*<>=+-]{1,255}$`)

// ArgoSyncPlan is what a sync of the app would do, and whether the principal
// may do it.
//
// For the target revision the resources are Argo CD's managed resources,
// each with its YAML diff, and the decision is the app's sync decision. For
// another revision they are the revision's manifests placed like a
// rollback's, without diffs (Argo CD compares with the target revision
// only), and the decision covers the app together with the revision's
// namespaces and cluster-scoped resources and needs rollback as well as
// sync, since it deploys what the app's Git target does not.
type ArgoSyncPlan struct {
	Revision       string // "" for the target revision
	TargetRevision string
	AutoSync       bool
	Decision       domain.Decision
	// DecisionAction is the action the decision is about: sync, or rollback
	// when that is what a sync to another revision lacks.
	DecisionAction domain.Action
	DecisionAt     domain.ClusterNS // the namespace the decision is about
	// AdminOnlyResources are the cluster-scoped resources that leave the
	// sync to admins; set only when that is why it is denied.
	AdminOnlyResources []domain.ClusterResource
	Resources          []ArgoPlanResource
	Truncated          bool // more than maxDiffResources resources
	// SyncOptions and Retry are the app's own, which a sync starts from.
	SyncOptions []string
	Retry       *ArgoRetry

	app domain.ArgoApp     // with the revision's resources included
	all []ArgoPlanResource // Resources before truncation
}

// ArgoPlanResource is a resource of a sync plan. State is changed, missing
// (the sync creates it), extra (only in the cluster: a sync that prunes
// deletes it when Prunable), synced, or present (managed now and in the
// revision; whether it changes is not known).
type ArgoPlanResource struct {
	ArgoResourceDiff
	Prunable bool
}

func (r ArgoPlanResource) ref() ArgoResourceRef {
	return ArgoResourceRef{Group: r.Group, Kind: r.Kind, Namespace: r.Namespace, Name: r.Name}
}

// AppSyncPlan shows what a sync of the app to the revision ("" for its
// target) would do. It needs sync on the app as it is now (a denial is
// audited). For the target revision it shows manifests, so it is audited as
// argo.diff before Argo CD is asked for them, like the diff; the plan's own
// decision for another revision is not audited.
func (s *WorkloadService) AppSyncPlan(ctx context.Context, p Principal, cluster, ns, appNamespace, name, revision string) (ArgoSyncPlan, error) {
	app, err := s.appFor(ctx, p, cluster, ns, appNamespace, name)
	if err != nil {
		return ArgoSyncPlan{}, err
	}
	d, err := s.access.AuthorizeApp(ctx, p, app, domain.ActionSync)
	if err != nil {
		return ArgoSyncPlan{}, err
	}
	if revision != "" && !validRevision.MatchString(revision) {
		return ArgoSyncPlan{}, Invalid("revision", "invalid revision")
	}
	return s.syncPlan(ctx, p, app, revision, d, func() error {
		e := s.audit.Event(p, domain.EventArgoDiff)
		e.Cluster, e.Namespace = app.Cluster, app.Namespace
		e.Action = string(domain.ActionSync)
		e.Environment = d.Environment
		e.BreakGlassID = d.BreakGlassID
		e.Result = domain.ResultAllowed
		e.Details = map[string]any{"app": app.Name, "appNamespace": app.AppNamespace}
		if _, err := s.audit.Record(ctx, e); err != nil {
			return fmt.Errorf("audit event could not be written, diff not shown: %w", err)
		}
		return nil
	})
}

// syncPlan builds the plan of a sync to the revision for a principal whose
// sync of the app as it is now was decided as d. A revision equal to the
// target is the target. With beforeDiff the target's resources carry their
// diffs, shown only once beforeDiff succeeded.
func (s *WorkloadService) syncPlan(ctx context.Context, p Principal, app domain.ArgoApp, revision string, d domain.Decision, beforeDiff func() error) (ArgoSyncPlan, error) {
	det, err := s.argo.Details(ctx, app.AppNamespace, app.Name)
	if err != nil {
		return ArgoSyncPlan{}, &ArgoError{Err: err}
	}
	plan := ArgoSyncPlan{TargetRevision: det.TargetRevision, AutoSync: app.AutoSync || det.AutoSync,
		SyncOptions: det.SyncOptions, Retry: det.Retry, app: app,
		Decision: d, DecisionAction: domain.ActionSync, DecisionAt: domain.ClusterNS{Cluster: app.Cluster, Namespace: app.Namespace}}
	if revision != "" && revision != det.TargetRevision {
		if det.MultiSource {
			return ArgoSyncPlan{}, Invalid("revision", "Multi-source applications can be synced to another revision in Argo CD only.")
		}
		plan.Revision = revision
	}
	if plan.Revision == "" && beforeDiff != nil {
		if err := beforeDiff(); err != nil {
			return ArgoSyncPlan{}, err
		}
	}
	items, err := s.argo.Managed(ctx, app.AppNamespace, app.Name)
	if err != nil {
		return ArgoSyncPlan{}, &ArgoError{Err: err}
	}
	if plan.Revision == "" {
		plan.all = targetResources(items, beforeDiff != nil)
	} else {
		resolved, withRevision, err := s.revisionResources(ctx, p, app, plan.Revision, d.BreakGlassID)
		if err != nil {
			return ArgoSyncPlan{}, err
		}
		plan.app = withRevision
		plan.Decision, plan.DecisionAction, plan.DecisionAt = s.decideSyncToRevision(p, withRevision)
		if plan.Decision.Reason == domain.ReasonAppScope {
			plan.AdminOnlyResources = s.access.AdminOnlyResources(withRevision)
		}
		plan.all = revisionPlanResources(resolved, items)
	}
	// What changes comes first, so a long list is cut among the synced.
	sort.SliceStable(plan.all, func(i, j int) bool {
		a, b := plan.all[i], plan.all[j]
		if as, bs := a.State == "synced" || a.State == "present", b.State == "synced" || b.State == "present"; as != bs {
			return bs
		}
		return lessResource(a.ArgoResourceDiff, b.ArgoResourceDiff)
	})
	plan.Resources = plan.all
	if len(plan.Resources) > maxDiffResources {
		plan.Resources, plan.Truncated = plan.Resources[:maxDiffResources], true
	}
	return plan, nil
}

// decideSyncToRevision decides a sync to another revision: sync and rollback
// on the app with the revision's resources. An allowed decision ends when
// either would.
func (s *WorkloadService) decideSyncToRevision(p Principal, app domain.ArgoApp) (domain.Decision, domain.Action, domain.ClusterNS) {
	d, at := s.access.DecideApp(p, app, domain.ActionSync)
	if !d.Allowed {
		return d, domain.ActionSync, at
	}
	rb, rat := s.access.DecideApp(p, app, domain.ActionRollback)
	if !rb.Allowed {
		return rb, domain.ActionRollback, rat
	}
	if rb.ClosesAt != nil && (d.ClosesAt == nil || rb.ClosesAt.Before(*d.ClosesAt)) {
		d.ClosesAt = rb.ClosesAt
	}
	if d.BreakGlassID == nil {
		d.BreakGlassID = rb.BreakGlassID
	}
	return d, domain.ActionSync, at
}

// targetResources are the app's resources (hooks left out) as a sync of its
// target revision finds them.
func targetResources(items []ArgoManagedResource, withDiff bool) []ArgoPlanResource {
	var out []ArgoPlanResource
	for _, it := range items {
		if it.Hook {
			continue
		}
		st := managedState(it)
		if st == "" {
			continue
		}
		out = append(out, ArgoPlanResource{ArgoResourceDiff: resourceDiff(it, st, withDiff), Prunable: st == "extra" && !it.NoPrune})
	}
	return out
}

// revisionPlanResources compares the revision's objects with what the app
// manages now (its live, non-hook resources).
func revisionPlanResources(resolved []ArgoResourceRef, items []ArgoManagedResource) []ArgoPlanResource {
	now := map[ArgoResourceRef]ArgoManagedResource{}
	for _, it := range items {
		if !it.Hook && it.Live != nil {
			now[ArgoResourceRef{Group: it.Group, Kind: it.Kind, Namespace: it.Namespace, Name: it.Name}] = it
		}
	}
	inRevision := map[ArgoResourceRef]bool{}
	var out []ArgoPlanResource
	for _, r := range resolved {
		if inRevision[r] {
			continue
		}
		inRevision[r] = true
		st := "missing"
		if _, ok := now[r]; ok {
			st = "present"
		}
		out = append(out, ArgoPlanResource{ArgoResourceDiff: ArgoResourceDiff{Group: r.Group, Kind: r.Kind, Namespace: r.Namespace, Name: r.Name, State: st}})
	}
	for r, it := range now {
		if !inRevision[r] {
			out = append(out, ArgoPlanResource{ArgoResourceDiff: ArgoResourceDiff{Group: r.Group, Kind: r.Kind, Namespace: r.Namespace, Name: r.Name, State: "extra"},
				Prunable: !it.NoPrune})
		}
	}
	return out
}

// checkSyncOptions checks what can be checked without the plan: the
// revision's form and the retry's bounds.
func checkSyncOptions(o ArgoSyncOptions) error {
	if o.Revision != "" && !validRevision.MatchString(o.Revision) {
		return Invalid("revision", "invalid revision")
	}
	if r := o.Retry; r != nil {
		if r.Limit < 0 || r.Limit > maxSyncRetries {
			return Invalid("retry", fmt.Sprintf("The retry limit must be between 0 and %d.", maxSyncRetries))
		}
		if b := r.Backoff; b != nil {
			for _, v := range []string{b.Duration, b.MaxDuration} {
				if v == "" {
					continue
				}
				if d, err := time.ParseDuration(v); err != nil || d <= 0 || d > maxSyncBackoff {
					return Invalid("retry", "Retry durations must be like 5s or 3m, at most 1h.")
				}
			}
			if b.Factor < 0 || b.Factor > 10 {
				return Invalid("retry", "The backoff factor must be at most 10.")
			}
		}
	}
	if len(o.Resources) > maxSyncResources {
		return Invalid("resources", fmt.Sprintf("At most %d resources can be synced at once.", maxSyncResources))
	}
	return nil
}

// check checks the sync options against the plan: only the dialog's sync
// options or the app's own, one prune propagation policy, and only the
// app's resources.
func (p ArgoSyncPlan) check(o ArgoSyncOptions) error {
	policies := 0
	for i, opt := range o.SyncOptions {
		if !syncOptionValues[opt] && !slices.Contains(p.SyncOptions, opt) {
			return Invalid("syncOptions", "Unknown sync option "+opt+".")
		}
		if slices.Contains(o.SyncOptions[:i], opt) {
			return Invalid("syncOptions", "Sync option "+opt+" is given twice.")
		}
		if strings.HasPrefix(opt, "PrunePropagationPolicy=") {
			policies++
		}
	}
	if policies > 1 {
		return Invalid("syncOptions", "Choose one prune propagation policy.")
	}
	known := make(map[ArgoResourceRef]bool, len(p.all))
	for _, r := range p.all {
		known[r.ref()] = true
	}
	for i, r := range o.Resources {
		if !known[r] {
			return Invalid("resources", r.String()+" is not a resource of this sync.")
		}
		if slices.Contains(o.Resources[:i], r) {
			return Invalid("resources", r.String()+" is given twice.")
		}
	}
	return nil
}

// toPrune lists the resources a pruning sync with these options deletes.
func (p ArgoSyncPlan) toPrune(o ArgoSyncOptions) []ArgoResourceRef {
	var out []ArgoResourceRef
	for _, r := range p.all {
		if r.State == "extra" && r.Prunable && (len(o.Resources) == 0 || slices.Contains(o.Resources, r.ref())) {
			out = append(out, r.ref())
		}
	}
	return out
}

// syncAuditDetails records a sync's options in its argo.sync event.
func syncAuditDetails(o ArgoSyncOptions, plan ArgoSyncPlan) map[string]any {
	out := map[string]any{"prune": o.Prune, "dryRun": o.DryRun, "applyOnly": o.ApplyOnly, "force": o.Force}
	if o.Revision != "" {
		out["revision"] = o.Revision
		out["targetRevision"] = plan.TargetRevision
	}
	if o.SyncOptions != nil {
		out["syncOptions"] = o.SyncOptions
	}
	if r := o.Retry; r != nil {
		retry := map[string]any{"limit": r.Limit}
		if b := r.Backoff; b != nil {
			retry["backoff"] = map[string]any{"duration": b.Duration, "factor": b.Factor, "maxDuration": b.MaxDuration}
		}
		out["retry"] = retry
	}
	if len(o.Resources) > 0 {
		out["resources"] = refNames(o.Resources)
	}
	if o.Prune {
		if rs := plan.toPrune(o); len(rs) > 0 {
			if len(rs) > maxDiffResources {
				rs = rs[:maxDiffResources]
			}
			out["toPrune"] = refNames(rs)
		}
	}
	return out
}
