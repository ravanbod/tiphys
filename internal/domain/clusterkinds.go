package domain

import (
	"regexp"
	"strings"
)

// ClusterResource is a cluster-scoped resource an Argo CD application
// manages (status.resources without a namespace).
type ClusterResource struct {
	Group string // API group; empty for the core group
	Kind  string
	Name  string
}

// KindKey names the resource's kind as kubectl does: "Kind.group", or just
// "Kind" in the core group. Settings.ArgoSyncClusterKinds lists these.
func (r ClusterResource) KindKey() string {
	if r.Group == "" {
		return r.Kind
	}
	return r.Kind + "." + r.Group
}

// String is "Kind.group/name", e.g. "ClusterInput.fluentbit.fluent.io/tail".
func (r ClusterResource) String() string { return r.KindKey() + "/" + r.Name }

// Kinds that can never be allowed for sync without an admin: with them,
// whoever syncs could grant themselves access, change what the API server
// admits or serves, or reach storage and scheduling of the whole cluster.
var (
	neverSyncGroups = map[string]struct{}{
		"":                             {}, // core: Namespace, PersistentVolume, Node, ...
		"rbac.authorization.k8s.io":    {},
		"admissionregistration.k8s.io": {},
		"apiextensions.k8s.io":         {},
		"apiregistration.k8s.io":       {},
	}
	neverSyncKinds = map[string]struct{}{
		"StorageClass.storage.k8s.io":     {},
		"PriorityClass.scheduling.k8s.io": {},
	}
	syncKindRe  = regexp.MustCompile(`^[A-Z][A-Za-z0-9]*$`)
	syncGroupRe = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)+$`)
)

// MaxSyncClusterKinds bounds Settings.ArgoSyncClusterKinds.
const MaxSyncClusterKinds = 50

// CheckSyncClusterKind explains why entry cannot be in
// Settings.ArgoSyncClusterKinds ("" when it can). An entry is "Kind.group"
// of a non-core group, without wildcards.
func CheckSyncClusterKind(entry string) string {
	kind, group, ok := strings.Cut(entry, ".")
	if !ok || !syncKindRe.MatchString(kind) || !syncGroupRe.MatchString(group) {
		if syncKindRe.MatchString(entry) {
			return "core kinds cannot be allowed"
		}
		return "must be Kind.group, e.g. ClusterInput.fluentbit.fluent.io"
	}
	if _, never := neverSyncGroups[group]; never {
		return "kinds of " + group + " cannot be allowed"
	}
	if _, never := neverSyncKinds[entry]; never {
		return entry + " cannot be allowed"
	}
	return ""
}

// AdminOnlyResources returns the app's cluster-scoped resources that leave
// syncing it to admins (ReasonAppScope): all but the Namespace objects of its
// own namespaces and the kinds the settings allow.
func (p *Policy) AdminOnlyResources(app ArgoApp) []ClusterResource {
	var out []ClusterResource
	for _, r := range app.ForeignClusterResources() {
		key := r.KindKey()
		// The check repeats the settings validation, so a stored entry can
		// never open a kind that must stay with admins.
		if _, ok := p.syncClusterKinds[key]; !ok || CheckSyncClusterKind(key) != "" {
			out = append(out, r)
		}
	}
	return out
}
