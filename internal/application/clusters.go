package application

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"

	"tiphys/internal/domain"
	"tiphys/internal/monitoring"
)

const (
	// clusterHealthInterval is how often a healthy cluster is checked;
	// clusterRetryInterval how often one that failed its last check is, so a
	// cluster that comes back (VPN reconnects, credential finishes loading) is
	// noticed within seconds. clusterTick is how often the monitor looks for
	// clusters that are due.
	clusterHealthInterval    = 30 * time.Second
	clusterRetryInterval     = 10 * time.Second
	clusterTick              = 5 * time.Second
	clusterPrivilegeInterval = 5 * time.Minute
	// unreachableAfter is the number of consecutive failed health checks
	// after which a cluster is marked unreachable (about 90 s).
	unreachableAfter = 3
)

// PrivilegeError is returned when a pasted kubeconfig is over-privileged.
type PrivilegeError struct{ Violations []string }

func (e *PrivilegeError) Error() string {
	return "the credential is over-privileged: " + strings.Join(e.Violations, "; ")
}

// ClusterRuntime is the live state of a cluster that is not stored.
type ClusterRuntime struct {
	PrivilegeViolations []string
	Usable              bool
	// CredentialLoaded is false when the cluster's credential (annotated
	// Secret) is missing or was rejected, so the cluster cannot be used.
	CredentialLoaded bool
	// EnvironmentFromCredential is true when the cluster's Secret sets the
	// environment (tiphys/environment), which then cannot be changed in the UI.
	EnvironmentFromCredential bool
}

// ClusterService registers clusters and monitors their health: every 30 s
// /version (3 failures in a row = unreachable) and every 5 minutes the
// privilege check. A cluster failing the privilege check is refused by
// kubeaccess.
type ClusterService struct {
	store  Store
	kube   ClusterAccess
	creds  CredentialStore
	audit  *AuditService
	policy *PolicyService
	clock  Clock
	logger *zap.Logger

	mu          sync.Mutex
	failures    map[string]int
	runtime     map[string]ClusterRuntime
	lastPriv    map[string]time.Time
	lastRun     map[string]time.Time // start of the last check, per cluster
	envFromCred map[string]bool      // the credential sets the environment

	credsWritable bool
}

func NewClusterService(store Store, kube ClusterAccess, creds CredentialStore, audit *AuditService, policy *PolicyService, clock Clock, logger *zap.Logger) *ClusterService {
	return &ClusterService{
		store: store, kube: kube, creds: creds, audit: audit, policy: policy, clock: clock, logger: logger,
		failures: map[string]int{}, runtime: map[string]ClusterRuntime{}, lastPriv: map[string]time.Time{}, lastRun: map[string]time.Time{},
		envFromCred: map[string]bool{},
	}
}

// Runtime returns the live state of a cluster.
func (s *ClusterService) Runtime(name string) ClusterRuntime {
	s.mu.Lock()
	rt := s.runtime[name]
	rt.EnvironmentFromCredential = s.envFromCred[name]
	s.mu.Unlock()
	for _, n := range s.kube.Registered() {
		if n == name {
			rt.CredentialLoaded = true
			break
		}
	}
	return rt
}

// List returns the registered clusters (admin and auditor).
func (s *ClusterService) List(ctx context.Context, p Principal) ([]domain.Cluster, error) {
	if err := RequireRole(p, domain.RoleAdmin, domain.RoleAuditor); err != nil {
		return nil, err
	}
	cs, err := s.store.Repos().Clusters.List(ctx)
	sort.Slice(cs, func(i, j int) bool { return cs[i].Name < cs[j].Name })
	return cs, err
}

func credSecretName(cluster string) string { return "tiphys-cluster-" + cluster }

func (s *ClusterService) audited(ctx context.Context, p Principal, op, cluster string, before, after any, fn func(Repositories) error) error {
	var ev domain.AuditEvent
	err := s.store.InTx(ctx, func(r Repositories) error {
		if err := fn(r); err != nil {
			return err
		}
		e := s.audit.Event(p, domain.EventAdminChange)
		e.Result, e.Cluster = domain.ResultSucceeded, cluster
		e.Details = map[string]any{"entity": "cluster", "operation": op, "id": cluster, "before": before, "after": after}
		var err error
		ev, err = s.audit.RecordIn(ctx, r, e)
		return err
	})
	if err == nil {
		s.audit.Publish(ev)
		s.policy.Changed(ctx)
	}
	return err
}

func clusterSummary(c domain.Cluster) map[string]any {
	return map[string]any{"name": c.Name, "environment": c.Environment, "kubernetesVersion": c.KubernetesVersion,
		"privilegeCheckOk": c.PrivilegeCheckOK, "admissionMode": c.AdmissionMode}
}

// Add validates a pasted kubeconfig (version, privilege check), writes the
// credential to the credential store and registers the cluster. The token is
// never shown again.
//
// Only reachable when registration from the UI is enabled
// (CLUSTER_REGISTRATION_FROM_UI), which config allows only with the dir store
// (local testing): the portal never writes Secrets in a cluster. Clusters can
// always be registered by creating an annotated Secret instead (see
// registerFromStore).
func (s *ClusterService) Add(ctx context.Context, p Principal, name, environment string, kubeconfig []byte) (domain.Cluster, error) {
	if err := RequireRole(p, domain.RoleAdmin); err != nil {
		return domain.Cluster{}, err
	}
	if !domain.ValidDNS1123Label(name) {
		return domain.Cluster{}, Invalid("name", "the name must match the cluster's name in Argo CD (lowercase letters, digits and dashes)")
	}
	if _, err := s.store.Repos().Clusters.Get(ctx, name); err == nil {
		return domain.Cluster{}, fmt.Errorf("%w: cluster %s already exists", ErrConflict, name)
	}
	probe, err := s.probeKubeconfig(ctx, kubeconfig)
	if err != nil {
		return domain.Cluster{}, err
	}
	now := s.clock.Now()
	c := domain.Cluster{
		Name: name, Environment: strings.TrimSpace(environment), KubeconfigSecret: credSecretName(name),
		KubernetesVersion: probe.Version, LastCheckAt: &now, Reachable: true, PrivilegeCheckOK: true,
		AdmissionMode: probe.AdmissionMode, UpdatedBy: p.User.Username,
	}
	// The row comes first: writing the credential makes the credential watch
	// run registerFromStore, which must find the row already there.
	err = s.audited(ctx, p, "create", name, nil, clusterSummary(c), func(r Repositories) error {
		var err error
		c, err = r.Clusters.Create(ctx, c)
		return err
	})
	if err != nil {
		return c, err
	}
	if err := s.creds.Put(ctx, name, kubeconfig); err != nil {
		if delErr := s.store.Repos().Clusters.Delete(ctx, name); delErr != nil {
			s.logger.Error("failed to remove the cluster row after the credential could not be stored", zap.String("cluster", name), zap.Error(delErr))
		}
		s.policy.Changed(ctx)
		return domain.Cluster{}, fmt.Errorf("store credential: %w", err)
	}
	s.setRuntime(name, ClusterRuntime{Usable: true})
	return c, nil
}

func (s *ClusterService) probeKubeconfig(ctx context.Context, kubeconfig []byte) (ClusterProbe, error) {
	if len(kubeconfig) == 0 {
		return ClusterProbe{}, Invalid("kubeconfig", "a kubeconfig is required")
	}
	pctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	probe, err := s.kube.ProbeKubeconfig(pctx, kubeconfig)
	if err != nil {
		return probe, Invalid("kubeconfig", "could not validate the kubeconfig: "+err.Error())
	}
	if !probe.PrivilegeOK {
		return probe, &PrivilegeError{Violations: probe.Violations}
	}
	return probe, nil
}

// ReplaceCredential rotates a cluster's credential the same way as Add.
func (s *ClusterService) ReplaceCredential(ctx context.Context, p Principal, name string, kubeconfig []byte) (domain.Cluster, error) {
	if err := RequireRole(p, domain.RoleAdmin); err != nil {
		return domain.Cluster{}, err
	}
	cur, err := s.store.Repos().Clusters.Get(ctx, name)
	if err != nil {
		return cur, err
	}
	probe, err := s.probeKubeconfig(ctx, kubeconfig)
	if err != nil {
		return cur, err
	}
	if err := s.creds.Put(ctx, name, kubeconfig); err != nil {
		return cur, fmt.Errorf("store credential: %w", err)
	}
	now := s.clock.Now()
	upd := cur
	upd.KubernetesVersion, upd.LastCheckAt, upd.Reachable, upd.PrivilegeCheckOK, upd.AdmissionMode = probe.Version, &now, true, true, probe.AdmissionMode
	upd.UpdatedBy = p.User.Username
	err = s.audited(ctx, p, "replace_credential", name, clusterSummary(cur), clusterSummary(upd), func(r Repositories) error {
		return r.Clusters.Update(ctx, upd)
	})
	if err == nil {
		s.mu.Lock()
		s.failures[name] = 0
		s.mu.Unlock()
		s.setRuntime(name, ClusterRuntime{Usable: true})
	}
	return upd, err
}

// SetEnvironment changes a cluster's environment label.
func (s *ClusterService) SetEnvironment(ctx context.Context, p Principal, name, environment string) (domain.Cluster, error) {
	if err := RequireRole(p, domain.RoleAdmin); err != nil {
		return domain.Cluster{}, err
	}
	cur, err := s.store.Repos().Clusters.Get(ctx, name)
	if err != nil {
		return cur, err
	}
	if env, ok, err := s.creds.Environment(ctx, name); err == nil && ok {
		return cur, fmt.Errorf("%w: the environment of %s is %q from its Secret's tiphys/environment annotation; change it there", ErrConflict, name, env)
	}
	upd := cur
	upd.Environment, upd.UpdatedBy = strings.TrimSpace(environment), p.User.Username
	err = s.audited(ctx, p, "set_environment", name, clusterSummary(cur), clusterSummary(upd), func(r Repositories) error {
		return r.Clusters.Update(ctx, upd)
	})
	return upd, err
}

// SetCredentialsWritable says whether the portal manages credentials itself
// (registration from the UI is enabled, which means the dir store). It changes
// Delete.
func (s *ClusterService) SetCredentialsWritable(writable bool) { s.credsWritable = writable }

// Delete removes a cluster from the portal. When the portal manages
// credentials (registration from the UI, dir store) its credential file is
// deleted with it. Otherwise the credential belongs to whoever created the
// Secret, and the portal never touches it, so Delete is refused while it exists (the cluster would be registered again at
// once): delete the Secret, or its `tiphys/cluster` annotation, first.
func (s *ClusterService) Delete(ctx context.Context, p Principal, name string) error {
	if err := RequireRole(p, domain.RoleAdmin); err != nil {
		return err
	}
	cur, err := s.store.Repos().Clusters.Get(ctx, name)
	if err != nil {
		return err
	}
	if s.credsWritable {
		if err := s.creds.Delete(ctx, name); err != nil && !errors.Is(err, ErrNotFound) {
			return fmt.Errorf("delete the cluster credential: %w", err)
		}
	} else {
		names, err := s.creds.List(ctx)
		if err != nil {
			return fmt.Errorf("check the cluster credential: %w", err)
		}
		for _, n := range names {
			if n == name {
				return fmt.Errorf("%w: the cluster's credential Secret still exists; delete it or remove its tiphys/cluster annotation first", ErrConflict)
			}
		}
	}
	return s.audited(ctx, p, "delete", name, clusterSummary(cur), nil, func(r Repositories) error {
		return r.Clusters.Delete(ctx, name)
	})
}

// registerFromStore creates the database row of a cluster whose credential
// has appeared in the credential store (an annotated Secret), and keeps its
// environment equal to the Secret's tiphys/environment annotation when it has
// one. Without the annotation the environment is set on the Clusters page (a
// new cluster starts unlabeled). A credential that disappears does not remove
// the row: the cluster stays, shown without a credential, so its grants and
// history are kept.
func (s *ClusterService) registerFromStore(ctx context.Context, name string) {
	repos := s.store.Repos()
	cur, err := repos.Clusters.Get(ctx, name)
	exists := err == nil
	if err != nil && !errors.Is(err, ErrNotFound) {
		s.logger.Error("cluster lookup failed while registering from its credential", zap.String("cluster", name), zap.Error(err))
		return
	}
	env, fromCred, err := s.creds.Environment(ctx, name)
	if errors.Is(err, ErrNotFound) {
		s.setEnvFromCred(name, false) // the Secret is gone: the row keeps its label, editable again
	}
	if err != nil {
		return
	}
	s.setEnvFromCred(name, fromCred)
	if exists {
		if fromCred && cur.Environment != env {
			s.environmentFromStore(ctx, cur, env)
		}
		return
	}
	if _, err := s.creds.Get(ctx, name); err != nil {
		return // removed again, or unusable: nothing to register
	}
	var ev domain.AuditEvent
	err = s.store.InTx(ctx, func(r Repositories) error {
		c, err := r.Clusters.Create(ctx, domain.Cluster{
			Name: name, Environment: env, KubeconfigSecret: "annotated Secret (tiphys/cluster)",
			AdmissionMode: domain.AdmissionNone, UpdatedBy: "credential store",
		})
		if err != nil {
			return err
		}
		e := s.audit.SystemEvent(domain.EventAdminChange)
		e.Result, e.Cluster = domain.ResultSucceeded, name
		e.Details = map[string]any{"entity": "cluster", "operation": "register_from_secret", "id": name, "before": nil, "after": clusterSummary(c)}
		ev, err = s.audit.RecordIn(ctx, r, e)
		return err
	})
	switch {
	case errors.Is(err, ErrConflict):
		return // registered concurrently by the other path
	case err != nil:
		s.logger.Error("could not register a cluster from its credential", zap.String("cluster", name), zap.Error(err))
		return
	}
	s.audit.Publish(ev)
	s.policy.Changed(ctx)
	s.logger.Info("cluster registered from an annotated Secret", zap.String("cluster", name))
}

func (s *ClusterService) setEnvFromCred(name string, v bool) {
	s.mu.Lock()
	s.envFromCred[name] = v
	s.mu.Unlock()
}

// environmentFromStore sets a cluster's environment to the one its Secret
// carries, audited as a system change.
func (s *ClusterService) environmentFromStore(ctx context.Context, cur domain.Cluster, env string) {
	upd := cur
	upd.Environment, upd.UpdatedBy = env, "credential store"
	var ev domain.AuditEvent
	err := s.store.InTx(ctx, func(r Repositories) error {
		if err := r.Clusters.Update(ctx, upd); err != nil {
			return err
		}
		e := s.audit.SystemEvent(domain.EventAdminChange)
		e.Result, e.Cluster = domain.ResultSucceeded, cur.Name
		e.Details = map[string]any{"entity": "cluster", "operation": "environment_from_secret", "id": cur.Name, "before": clusterSummary(cur), "after": clusterSummary(upd)}
		var err error
		ev, err = s.audit.RecordIn(ctx, r, e)
		return err
	})
	if err != nil {
		s.logger.Error("could not set a cluster's environment from its Secret", zap.String("cluster", cur.Name), zap.Error(err))
		return
	}
	s.audit.Publish(ev)
	s.policy.Changed(ctx)
	s.logger.Info("cluster environment set from its Secret", zap.String("cluster", cur.Name), zap.String("environment", env))
}

// syncFromStore registers every cluster the credential store lists. It backs
// up the watch, which reports changes as they happen.
func (s *ClusterService) syncFromStore(ctx context.Context) {
	names, err := s.creds.List(ctx)
	if err != nil {
		s.logger.Error("could not list cluster credentials", zap.Error(err))
		return
	}
	for _, n := range names {
		s.registerFromStore(ctx, n)
	}
}

// CheckNow runs the health and privilege check immediately.
func (s *ClusterService) CheckNow(ctx context.Context, p Principal, name string) (domain.Cluster, error) {
	if err := RequireRole(p, domain.RoleAdmin); err != nil {
		return domain.Cluster{}, err
	}
	if _, err := s.store.Repos().Clusters.Get(ctx, name); err != nil {
		return domain.Cluster{}, err
	}
	s.check(ctx, name, true)
	return s.store.Repos().Clusters.Get(ctx, name)
}

// ---- environment overrides and protected namespaces ----

func (s *ClusterService) Overrides(ctx context.Context, p Principal) ([]domain.EnvironmentOverride, error) {
	if err := RequireRole(p, domain.RoleAdmin, domain.RoleAuditor); err != nil {
		return nil, err
	}
	return s.store.Repos().Clusters.ListEnvironmentOverrides(ctx)
}

func (s *ClusterService) AddOverride(ctx context.Context, p Principal, o domain.EnvironmentOverride) (domain.EnvironmentOverride, error) {
	if err := RequireRole(p, domain.RoleAdmin); err != nil {
		return o, err
	}
	o.Environment = strings.TrimSpace(o.Environment)
	if o.Environment == "" {
		return o, Invalid("environment", "an environment is required")
	}
	switch o.Kind {
	case domain.OverrideNamespace:
		if o.Cluster == "" || !domain.ValidDNS1123Label(o.Namespace) || o.ArgoProject != "" {
			return o, Invalid("override", "a namespace override needs a cluster and a namespace")
		}
	case domain.OverrideArgoProject:
		if o.ArgoProject == "" || o.Cluster != "" || o.Namespace != "" {
			return o, Invalid("override", "an Argo project override needs only a project")
		}
	default:
		return o, Invalid("kind", "kind must be argo_project or namespace")
	}
	err := s.audited(ctx, p, "override_create", o.Cluster, nil, o, func(r Repositories) error {
		var err error
		o, err = r.Clusters.CreateEnvironmentOverride(ctx, o)
		return err
	})
	return o, err
}

func (s *ClusterService) DeleteOverride(ctx context.Context, p Principal, id int64) error {
	if err := RequireRole(p, domain.RoleAdmin); err != nil {
		return err
	}
	return s.audited(ctx, p, "override_delete", "", id, nil, func(r Repositories) error {
		return r.Clusters.DeleteEnvironmentOverride(ctx, id)
	})
}

func (s *ClusterService) ProtectedNamespaces(ctx context.Context, p Principal) ([]string, error) {
	if err := RequireRole(p, domain.RoleAdmin, domain.RoleAuditor); err != nil {
		return nil, err
	}
	l, err := s.store.Repos().Clusters.ListProtectedNamespaces(ctx)
	sort.Strings(l)
	return l, err
}

func (s *ClusterService) AddProtected(ctx context.Context, p Principal, ns string) error {
	if err := RequireRole(p, domain.RoleAdmin); err != nil {
		return err
	}
	if !domain.ValidDNS1123Label(ns) {
		return Invalid("namespace", "invalid namespace name")
	}
	return s.audited(ctx, p, "protected_add", "", nil, ns, func(r Repositories) error {
		return r.Clusters.AddProtectedNamespace(ctx, ns)
	})
}

func (s *ClusterService) RemoveProtected(ctx context.Context, p Principal, ns string) error {
	if err := RequireRole(p, domain.RoleAdmin); err != nil {
		return err
	}
	return s.audited(ctx, p, "protected_remove", "", ns, nil, func(r Repositories) error {
		return r.Clusters.RemoveProtectedNamespace(ctx, ns)
	})
}

// ---- health monitor ----

func (s *ClusterService) setRuntime(name string, rt ClusterRuntime) {
	s.mu.Lock()
	s.runtime[name] = rt
	s.lastPriv[name] = s.clock.Now()
	s.mu.Unlock()
	s.kube.SetUsable(name, rt.Usable)
	b := 0.0
	if rt.Usable {
		b = 1
	}
	monitoring.ClusterPrivilegeOK.WithLabelValues(name).Set(b)
}

// Run checks every registered cluster in the background until ctx is done:
// healthy clusters every 30 s, ones that failed their last check every 10 s.
func (s *ClusterService) Run(ctx context.Context) {
	// Clusters exist because their credential (annotated Secret) exists: watch
	// the store, and list it periodically as a safety net.
	go func() {
		if err := s.creds.Watch(ctx, func(name string) { s.registerFromStore(ctx, name) }); err != nil && ctx.Err() == nil {
			s.logger.Error("credential watch stopped; relying on the periodic sync", zap.Error(err))
		}
	}()
	s.syncFromStore(ctx)
	s.checkAll(ctx, true)
	lastSync := s.clock.Now()
	t := time.NewTicker(clusterTick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if s.clock.Now().Sub(lastSync) >= clusterHealthInterval {
				s.syncFromStore(ctx)
				lastSync = s.clock.Now()
			}
			s.CheckDue(ctx)
		}
	}
}

// CheckDue checks the clusters whose interval has elapsed.
func (s *ClusterService) CheckDue(ctx context.Context) { s.checkAll(ctx, false) }

// due reports whether a cluster should be checked now.
func (s *ClusterService) due(c domain.Cluster) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	interval := clusterHealthInterval
	if s.failures[c.Name] > 0 || !c.Reachable {
		interval = clusterRetryInterval
	}
	last, ok := s.lastRun[c.Name]
	return !ok || s.clock.Now().Sub(last) >= interval
}

func (s *ClusterService) checkAll(ctx context.Context, force bool) {
	clusters, err := s.store.Repos().Clusters.List(ctx)
	if err != nil {
		s.logger.Error("cluster monitor could not list clusters", zap.Error(err))
		return
	}
	var wg sync.WaitGroup
	for _, c := range clusters {
		if !force && !s.due(c) {
			continue
		}
		wg.Add(1)
		go func(name string) {
			defer wg.Done()
			s.check(ctx, name, force)
		}(c.Name)
	}
	wg.Wait()
}

// check runs the 30 s health check and, when due or forced, the privilege
// check, then stores the result.
func (s *ClusterService) check(ctx context.Context, name string, forcePrivilege bool) {
	cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	s.mu.Lock()
	s.lastRun[name] = s.clock.Now()
	duePriv := forcePrivilege || s.clock.Now().Sub(s.lastPriv[name]) >= clusterPrivilegeInterval
	_, known := s.runtime[name]
	s.mu.Unlock()

	var (
		version string
		probe   *ClusterProbe
	)
	var err error
	if duePriv || !known {
		var pr ClusterProbe
		pr, err = s.kube.Probe(cctx, name)
		if err == nil {
			probe, version = &pr, pr.Version
		}
	} else {
		version, err = s.kube.Version(cctx, name)
	}

	repos := s.store.Repos()
	c, gerr := repos.Clusters.Get(ctx, name)
	if gerr != nil {
		return
	}
	now := s.clock.Now()
	c.LastCheckAt = &now

	s.mu.Lock()
	if err != nil {
		s.failures[name]++
		// "Credential not loaded yet" (right after start or Add) counts like any
		// other failure: it must not mark the cluster unreachable by itself.
		if s.failures[name] >= unreachableAfter {
			c.Reachable = false
		}
	} else {
		s.failures[name] = 0
		c.Reachable = true
		c.KubernetesVersion = version
	}
	s.mu.Unlock()

	if probe != nil {
		c.PrivilegeCheckOK = probe.PrivilegeOK
		c.AdmissionMode = probe.AdmissionMode
		s.setRuntime(name, ClusterRuntime{PrivilegeViolations: probe.Violations, Usable: probe.PrivilegeOK})
		if !probe.PrivilegeOK {
			s.logger.Error("cluster credential failed the privilege check, cluster disabled",
				zap.String("cluster", name), zap.Strings("violations", probe.Violations))
		}
	}
	if err != nil {
		s.logger.Warn("cluster health check failed", zap.String("cluster", name), zap.Int("consecutive_failures", s.failureCount(name)), zap.Error(err))
	}
	b := 0.0
	if c.Reachable {
		b = 1
	}
	monitoring.ClusterReachable.WithLabelValues(name).Set(b)

	h := ClusterHealth{KubernetesVersion: c.KubernetesVersion, LastCheckAt: now, Reachable: c.Reachable,
		PrivilegeCheckOK: c.PrivilegeCheckOK, AdmissionMode: c.AdmissionMode}
	if err := repos.Clusters.UpdateHealth(ctx, name, h); err != nil {
		s.logger.Error("failed to store cluster health", zap.String("cluster", name), zap.Error(err))
		return
	}
}

func (s *ClusterService) failureCount(name string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.failures[name]
}

// UnreachableCount is the number of clusters currently unreachable.
func (s *ClusterService) UnreachableCount(ctx context.Context) int {
	cs, err := s.store.Repos().Clusters.List(ctx)
	if err != nil {
		return 0
	}
	n := 0
	for _, c := range cs {
		if !c.Reachable {
			n++
		}
	}
	return n
}
