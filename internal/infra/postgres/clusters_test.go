package postgres

import (
	"slices"
	"testing"
	"time"

	"tiphys/internal/application"
	"tiphys/internal/domain"
)

func TestClusters(t *testing.T) {
	f := newFixture(t)
	c, err := f.r.Clusters.Create(f.ctx, domain.Cluster{Name: "prod-1", Environment: "prod", KubeconfigSecret: "kc-prod-1", UpdatedBy: "admin"})
	must(t, err)
	if c.ID == 0 || c.AdmissionMode != domain.AdmissionNone || c.Reachable || c.UpdatedBy != "admin" {
		t.Fatalf("cluster: %+v", c)
	}
	_, err = f.r.Clusters.Create(f.ctx, domain.Cluster{Name: "prod-1"})
	isErr(t, err, application.ErrConflict)
	_, err = f.r.Clusters.Create(f.ctx, domain.Cluster{Name: "bad", AdmissionMode: "magic"})
	isCheckViolation(t, err)
	_, err = f.r.Clusters.Get(f.ctx, "nope")
	isErr(t, err, application.ErrNotFound)

	now := t0
	c.KubernetesVersion, c.LastCheckAt, c.Reachable, c.PrivilegeCheckOK, c.AdmissionMode = "v1.31.2", &now, true, true, domain.AdmissionVAP
	c.UpdatedBy = "" // a health check does not name an admin
	must(t, f.r.Clusters.Update(f.ctx, c))
	got, err := f.r.Clusters.Get(f.ctx, "prod-1")
	must(t, err)
	if got.KubernetesVersion != "v1.31.2" || !got.Reachable || !got.PrivilegeCheckOK || got.AdmissionMode != domain.AdmissionVAP ||
		got.LastCheckAt == nil || !got.LastCheckAt.Equal(now) || got.UpdatedBy != "admin" || got.KubeconfigSecret != "kc-prod-1" {
		t.Fatalf("after update: %+v", got)
	}
	// UpdateHealth leaves the admin-edited columns alone.
	later := now.Add(time.Minute)
	must(t, f.r.Clusters.UpdateHealth(f.ctx, "prod-1", application.ClusterHealth{
		KubernetesVersion: "v1.32.0", LastCheckAt: later, Reachable: false, PrivilegeCheckOK: true, AdmissionMode: domain.AdmissionKyverno}))
	got2, err := f.r.Clusters.Get(f.ctx, "prod-1")
	must(t, err)
	if got2.KubernetesVersion != "v1.32.0" || got2.Reachable || got2.AdmissionMode != domain.AdmissionKyverno ||
		!got2.LastCheckAt.Equal(later) || got2.Environment != "prod" || got2.KubeconfigSecret != "kc-prod-1" ||
		!got2.UpdatedAt.Equal(got.UpdatedAt) {
		t.Fatalf("after UpdateHealth: %+v", got2)
	}
	isErr(t, f.r.Clusters.UpdateHealth(f.ctx, "nope", application.ClusterHealth{AdmissionMode: domain.AdmissionNone}), application.ErrNotFound)
	isErr(t, f.r.Clusters.Update(f.ctx, domain.Cluster{Name: "nope", AdmissionMode: domain.AdmissionNone}), application.ErrNotFound)

	f.cluster("a-cluster")
	cs, err := f.r.Clusters.List(f.ctx)
	must(t, err)
	if len(cs) != 2 || cs[0].Name != "a-cluster" {
		t.Fatalf("list: %+v", cs)
	}

	must(t, f.r.Clusters.Delete(f.ctx, "a-cluster"))
	isErr(t, f.r.Clusters.Delete(f.ctx, "a-cluster"), application.ErrNotFound)
}

func TestEnvironmentOverrides(t *testing.T) {
	f := newFixture(t)
	f.cluster("c1")
	f.cluster("c2")

	ns, err := f.r.Clusters.CreateEnvironmentOverride(f.ctx, domain.EnvironmentOverride{Kind: domain.OverrideNamespace, Cluster: "c1", Namespace: "pay", Environment: "stage"})
	must(t, err)
	if ns.ID == 0 || ns.Cluster != "c1" || ns.Namespace != "pay" || ns.ArgoProject != "" || ns.CreatedAt.IsZero() {
		t.Fatalf("ns override: %+v", ns)
	}
	pr, err := f.r.Clusters.CreateEnvironmentOverride(f.ctx, domain.EnvironmentOverride{Kind: domain.OverrideArgoProject, ArgoProject: "payments", Environment: "prod"})
	must(t, err)
	if pr.Cluster != "" || pr.ArgoProject != "payments" {
		t.Fatalf("project override: %+v", pr)
	}

	// One override per (cluster, namespace) and per project.
	_, err = f.r.Clusters.CreateEnvironmentOverride(f.ctx, domain.EnvironmentOverride{Kind: domain.OverrideNamespace, Cluster: "c1", Namespace: "pay", Environment: "dev"})
	isErr(t, err, application.ErrConflict)
	_, err = f.r.Clusters.CreateEnvironmentOverride(f.ctx, domain.EnvironmentOverride{Kind: domain.OverrideArgoProject, ArgoProject: "payments", Environment: "dev"})
	isErr(t, err, application.ErrConflict)
	// The same namespace on another cluster is fine.
	_, err = f.r.Clusters.CreateEnvironmentOverride(f.ctx, domain.EnvironmentOverride{Kind: domain.OverrideNamespace, Cluster: "c2", Namespace: "pay", Environment: "dev"})
	must(t, err)

	// Kind namespace requires a cluster; fields must match the kind.
	for name, o := range map[string]domain.EnvironmentOverride{
		"namespace without cluster": {Kind: domain.OverrideNamespace, Namespace: "x", Environment: "dev"},
		"namespace without ns":      {Kind: domain.OverrideNamespace, Cluster: "c1", Environment: "dev"},
		"project with namespace":    {Kind: domain.OverrideArgoProject, ArgoProject: "p", Namespace: "x", Environment: "dev"},
		"project with cluster":      {Kind: domain.OverrideArgoProject, ArgoProject: "p", Cluster: "c1", Environment: "dev"},
		"project without name":      {Kind: domain.OverrideArgoProject, Environment: "dev"},
		"empty environment":         {Kind: domain.OverrideArgoProject, ArgoProject: "p2"},
		"bad kind":                  {Kind: "galaxy", Environment: "dev"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := f.r.Clusters.CreateEnvironmentOverride(f.ctx, o)
			isCheckViolation(t, err)
		})
	}
	_, err = f.r.Clusters.CreateEnvironmentOverride(f.ctx, domain.EnvironmentOverride{Kind: domain.OverrideNamespace, Cluster: "ghost", Namespace: "x", Environment: "dev"})
	isErr(t, err, application.ErrNotFound)

	all, err := f.r.Clusters.ListEnvironmentOverrides(f.ctx)
	must(t, err)
	if len(all) != 3 {
		t.Fatalf("list: %+v", all)
	}
	must(t, f.r.Clusters.DeleteEnvironmentOverride(f.ctx, pr.ID))
	isErr(t, f.r.Clusters.DeleteEnvironmentOverride(f.ctx, pr.ID), application.ErrNotFound)

	// Deleting a cluster removes its namespace overrides.
	must(t, f.r.Clusters.Delete(f.ctx, "c1"))
	all, _ = f.r.Clusters.ListEnvironmentOverrides(f.ctx)
	if len(all) != 1 || all[0].Cluster != "c2" {
		t.Fatalf("after cluster delete: %+v", all)
	}
}

func TestProtectedNamespaces(t *testing.T) {
	f := newFixture(t)
	ns, err := f.r.Clusters.ListProtectedNamespaces(f.ctx)
	must(t, err)
	if !slices.Contains(ns, "kube-system") {
		t.Fatalf("defaults missing: %v", ns)
	}
	must(t, f.r.Clusters.AddProtectedNamespace(f.ctx, "argocd"))
	isErr(t, f.r.Clusters.AddProtectedNamespace(f.ctx, "argocd"), application.ErrConflict)
	must(t, f.r.Clusters.RemoveProtectedNamespace(f.ctx, "argocd"))
	isErr(t, f.r.Clusters.RemoveProtectedNamespace(f.ctx, "argocd"), application.ErrNotFound)
	ns2, _ := f.r.Clusters.ListProtectedNamespaces(f.ctx)
	if !slices.Equal(ns, ns2) {
		t.Fatalf("list changed: %v vs %v", ns, ns2)
	}
}
