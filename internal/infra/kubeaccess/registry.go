package kubeaccess

import (
	"context"
	"crypto/sha256"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"go.uber.org/zap"
	"k8s.io/client-go/rest"

	"tiphys/internal/application"
	"tiphys/internal/domain"
)

const (
	checkTimeout  = 15 * time.Second
	checkRetryGap = 30 * time.Second
)

// Registry holds one base rest.Config per registered cluster and keeps it in
// step with the credential store. A cluster is usable only when its last
// privilege check passed (set by the registry) and the portal has not marked
// it unusable (SetUsable, for health).
type Registry struct {
	store   application.CredentialStore
	logger  *zap.Logger
	factory clientFactory
	retry   time.Duration

	mu       sync.RWMutex
	clusters map[string]*clusterEntry
	gen      map[string]uint64
}

type clusterEntry struct {
	base        *rest.Config
	sum         [sha256.Size]byte
	privilegeOK atomic.Bool // set by the registry's own privilege check
	enabled     atomic.Bool // set by SetUsable
	debugReady  atomic.Bool // the debug admission policy is installed (privilege check)
}

func (e *clusterEntry) usable() bool { return e.privilegeOK.Load() && e.enabled.Load() }

func NewRegistry(store application.CredentialStore, logger *zap.Logger) *Registry {
	return &Registry{
		store:    store,
		logger:   logger,
		factory:  newClients,
		retry:    checkRetryGap,
		clusters: map[string]*clusterEntry{},
		gen:      map[string]uint64{},
	}
}

// Run loads every stored credential and then follows the store until ctx is
// done. Each load builds the client config and runs the privilege check; a
// cluster whose check has not passed refuses every verb.
func (r *Registry) Run(ctx context.Context) error {
	names, err := r.store.List(ctx)
	if err != nil {
		r.logger.Error("list cluster credentials failed", zap.Error(err))
		return err
	}
	var wg sync.WaitGroup
	for _, n := range names {
		wg.Add(1)
		go func() { defer wg.Done(); r.reload(ctx, n) }()
	}
	wg.Wait()
	return r.store.Watch(ctx, func(name string) { go r.reload(ctx, name) })
}

// reload applies the store's current state of one cluster.
func (r *Registry) reload(ctx context.Context, name string) {
	r.mu.Lock()
	r.gen[name]++
	gen := r.gen[name]
	r.mu.Unlock()

	if !domain.ValidDNS1123Subdomain(name) {
		r.logger.Warn("ignoring credential with invalid cluster name", zap.String("cluster", name))
		return
	}
	kubeconfig, err := r.store.Get(ctx, name)
	if errors.Is(err, application.ErrNotFound) {
		r.remove(name, gen)
		return
	}
	if err != nil {
		r.logger.Error("read cluster credential failed", zap.String("cluster", name), zap.Error(err))
		return
	}
	sum := sha256.Sum256(kubeconfig)
	cfg, err := parseKubeconfig(kubeconfig)
	if err != nil {
		// A credential that cannot be used must not leave the previous one active.
		r.logger.Error("cluster credential rejected, cluster removed", zap.String("cluster", name), zap.Error(err))
		r.remove(name, gen)
		return
	}

	e := &clusterEntry{base: cfg, sum: sum}
	e.enabled.Store(true)
	r.mu.Lock()
	if r.gen[name] != gen {
		r.mu.Unlock()
		return
	}
	if old := r.clusters[name]; old != nil {
		if old.sum == sum {
			r.mu.Unlock()
			return
		}
		e.enabled.Store(old.enabled.Load())
	}
	r.clusters[name] = e
	r.mu.Unlock()

	r.checkUntilDone(ctx, name, e)
}

func (r *Registry) remove(name string, gen uint64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.gen[name] == gen {
		delete(r.clusters, name)
	}
}

// checkUntilDone runs the privilege check, retrying while the cluster cannot
// be reached, so a cluster that is down at startup becomes usable on its own.
func (r *Registry) checkUntilDone(ctx context.Context, name string, e *clusterEntry) {
	for {
		cctx, cancel := context.WithTimeout(ctx, checkTimeout)
		p, err := r.probeEntry(cctx, e)
		cancel()
		if err == nil {
			if !p.PrivilegeOK {
				r.logger.Error("cluster credential is over-privileged, cluster refused",
					zap.String("cluster", name), zap.Strings("violations", p.Violations))
			}
			return
		}
		r.logger.Error("privilege check failed, will retry", zap.String("cluster", name), zap.Error(err))
		select {
		case <-ctx.Done():
			return
		case <-time.After(r.retry):
		}
		r.mu.RLock()
		current := r.clusters[name] == e
		r.mu.RUnlock()
		if !current {
			return
		}
	}
}

func (r *Registry) entry(cluster string) (*clusterEntry, error) {
	r.mu.RLock()
	e := r.clusters[cluster]
	r.mu.RUnlock()
	if e == nil {
		return nil, application.ErrClusterUnknown
	}
	return e, nil
}

// lookup returns a usable cluster or the matching sentinel error.
func (r *Registry) lookup(cluster string) (*clusterEntry, error) {
	e, err := r.entry(cluster)
	if err != nil {
		return nil, err
	}
	if !e.usable() {
		return nil, application.ErrClusterUnusable
	}
	return e, nil
}

// Registered lists the clusters with a credential loaded.
func (r *Registry) Registered() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.clusters))
	for n := range r.clusters {
		out = append(out, n)
	}
	return out
}

// SetUsable marks a cluster usable or not (health). It cannot override a
// failed privilege check: an over-privileged cluster stays refused.
func (r *Registry) SetUsable(cluster string, usable bool) {
	if e, err := r.entry(cluster); err == nil {
		e.enabled.Store(usable)
	}
}
