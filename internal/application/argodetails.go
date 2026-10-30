package application

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/pmezard/go-difflib/difflib"
	"sigs.k8s.io/yaml"

	"tiphys/internal/domain"
)

const (
	// argoHistoryShown is how many deploys the history tab shows.
	argoHistoryShown = 10
	// maxDiffResources and maxDiffLines bound what a diff returns.
	maxDiffResources = 200
	maxDiffLines     = 2000
)

// ArgoHistoryEntry is a deploy with the commit behind it, when Argo CD knows it.
type ArgoHistoryEntry struct {
	ArgoDeploy
	Commit  *ArgoRevision
	Current bool // the deploy the app runs now
}

// ArgoResourceDiff is an out-of-sync resource of an app.
type ArgoResourceDiff struct {
	Group, Kind, Namespace, Name string
	// State is changed (both sides differ), missing (only in Git) or extra
	// (only in the cluster; a sync never prunes it).
	State     string
	Diff      string // unified diff of the YAML, live → desired
	Hidden    bool   // a Secret: its content is never shown
	Truncated bool
}

// ArgoDiff is what a sync of the app would change.
type ArgoDiff struct {
	Resources []ArgoResourceDiff
	Truncated bool // more than maxDiffResources resources differ
}

// appFor validates the names, checks the principal may see the namespace's
// apps (before the lookup, so a 404 reveals nothing) and finds an app
// deploying to the namespace.
func (s *WorkloadService) appFor(ctx context.Context, p Principal, cluster, ns, appNamespace, name string) (domain.ArgoApp, error) {
	if err := validateNames(ns, domain.WorkloadRef{}); err != nil {
		return domain.ArgoApp{}, err
	}
	if !domain.ValidDNS1123Label(appNamespace) || !domain.ValidDNS1123Subdomain(name) {
		return domain.ArgoApp{}, Invalid("app", "invalid application name")
	}
	if err := s.requireAnyGrant(ctx, p, cluster, ns); err != nil {
		return domain.ArgoApp{}, err
	}
	app, ok := s.namespaceApp(cluster, ns, appNamespace, name)
	if !ok {
		return domain.ArgoApp{}, ErrNotFound
	}
	return app, nil
}

// AppStatus returns an app's conditions and last operation, for anyone who
// may see the namespace's apps.
func (s *WorkloadService) AppStatus(ctx context.Context, p Principal, cluster, ns, appNamespace, name string) (ArgoAppDetails, error) {
	app, err := s.appFor(ctx, p, cluster, ns, appNamespace, name)
	if err != nil {
		return ArgoAppDetails{}, err
	}
	d, err := s.argo.Details(ctx, app.AppNamespace, app.Name)
	if err != nil {
		return ArgoAppDetails{}, &ArgoError{Err: err}
	}
	return d, nil
}

// AppHistory returns an app's last deploys, newest first, with their commits
// (looked up in parallel; a revision Argo CD cannot describe, e.g. a Helm
// chart version, has none).
func (s *WorkloadService) AppHistory(ctx context.Context, p Principal, cluster, ns, appNamespace, name string) ([]ArgoHistoryEntry, error) {
	d, err := s.AppStatus(ctx, p, cluster, ns, appNamespace, name)
	if err != nil {
		return nil, err
	}
	n := min(len(d.History), argoHistoryShown)
	out := make([]ArgoHistoryEntry, n)
	var wg sync.WaitGroup
	for i := range n {
		h := d.History[len(d.History)-1-i]
		out[i] = ArgoHistoryEntry{ArgoDeploy: h, Current: i == 0}
		if h.Revision == "" {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			if c, err := s.argo.Revision(ctx, appNamespace, name, h.Revision); err == nil {
				out[i].Commit = &c
			}
		}()
	}
	wg.Wait()
	return out, nil
}

// AppDiff shows what a sync of the app would change. It reveals the app's
// manifests, so it needs the same right as the sync itself (sync on every
// namespace the app deploys to) and is audited as argo.diff before Argo CD
// is asked.
func (s *WorkloadService) AppDiff(ctx context.Context, p Principal, cluster, ns, appNamespace, name string) (ArgoDiff, error) {
	app, err := s.appFor(ctx, p, cluster, ns, appNamespace, name)
	if err != nil {
		return ArgoDiff{}, err
	}
	d, err := s.access.AuthorizeApp(ctx, p, app, domain.ActionSync)
	if err != nil {
		return ArgoDiff{}, err
	}
	e := s.audit.Event(p, domain.EventArgoDiff)
	e.Cluster, e.Namespace = app.Cluster, app.Namespace
	e.Action = string(domain.ActionSync)
	e.Environment = d.Environment
	e.BreakGlassID = d.BreakGlassID
	e.Result = domain.ResultAllowed
	e.Details = map[string]any{"app": app.Name, "appNamespace": app.AppNamespace}
	if _, err := s.audit.Record(ctx, e); err != nil {
		return ArgoDiff{}, fmt.Errorf("audit event could not be written, diff not shown: %w", err)
	}
	items, err := s.argo.Managed(ctx, app.AppNamespace, app.Name)
	if err != nil {
		return ArgoDiff{}, &ArgoError{Err: err}
	}
	return diffResources(items), nil
}

// diffResources lists the resources a sync would change, sorted by kind,
// namespace and name, with a YAML diff of each (Secrets without content).
// Hooks are left out: they run on every sync and are not compared.
func diffResources(items []ArgoManagedResource) ArgoDiff {
	var out ArgoDiff
	for _, it := range items {
		if it.Hook {
			continue
		}
		var st string
		switch {
		case it.Live == nil && it.Desired == nil:
			continue
		case it.Live == nil:
			st = "missing"
		case it.Desired == nil:
			st = "extra"
		case it.Modified:
			st = "changed"
		default:
			continue
		}
		d := ArgoResourceDiff{Group: it.Group, Kind: it.Kind, Namespace: it.Namespace, Name: it.Name, State: st}
		if it.Group == "" && it.Kind == "Secret" {
			d.Hidden = true
		} else {
			d.Diff, d.Truncated = unifiedDiff(manifestYAML(it.Live), manifestYAML(it.Desired))
		}
		out.Resources = append(out.Resources, d)
	}
	sort.Slice(out.Resources, func(i, j int) bool {
		a, b := out.Resources[i], out.Resources[j]
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		if a.Namespace != b.Namespace {
			return a.Namespace < b.Namespace
		}
		return a.Name < b.Name
	})
	if len(out.Resources) > maxDiffResources {
		out.Resources, out.Truncated = out.Resources[:maxDiffResources], true
	}
	return out
}

// manifestYAML renders a state as YAML without the fields that are noise in
// a diff (status, managed fields, server-set metadata).
func manifestYAML(raw []byte) string {
	if raw == nil {
		return ""
	}
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil {
		return string(raw)
	}
	delete(obj, "status")
	if md, ok := obj["metadata"].(map[string]any); ok {
		for _, f := range []string{"managedFields", "resourceVersion", "uid", "generation", "creationTimestamp", "selfLink"} {
			delete(md, f)
		}
		if ann, ok := md["annotations"].(map[string]any); ok {
			delete(ann, "kubectl.kubernetes.io/last-applied-configuration")
			if len(ann) == 0 {
				delete(md, "annotations")
			}
		}
	}
	b, err := yaml.Marshal(obj)
	if err != nil {
		return string(raw)
	}
	return string(b)
}

// unifiedDiff diffs live against desired, at most maxDiffLines lines.
func unifiedDiff(live, desired string) (string, bool) {
	text, _ := difflib.GetUnifiedDiffString(difflib.UnifiedDiff{
		A: difflib.SplitLines(live), B: difflib.SplitLines(desired),
		FromFile: "live", ToFile: "desired", Context: 3,
	})
	lines := strings.SplitAfter(text, "\n")
	if len(lines) > maxDiffLines {
		return strings.Join(lines[:maxDiffLines], ""), true
	}
	return text, false
}
