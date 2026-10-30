package application

import (
	"context"
	"sort"
	"sync"
	"time"

	"go.uber.org/zap"

	"tiphys/internal/domain"
	"tiphys/internal/monitoring"
)

// ArgoRefreshInterval is how often the Argo CD cache is refreshed.
const ArgoRefreshInterval = 60 * time.Second

// ArgoStatus describes the cache for the UI.
type ArgoStatus struct {
	FetchedAt    *time.Time
	AgeSeconds   *int64
	Stale        bool
	AppCount     int
	ProjectCount int
	LastError    string
}

// ArgoService caches Argo CD Applications and AppProjects. If Argo is
// unreachable the last good data is kept; Decide fails closed when it gets
// older than domain.DefaultArgoMaxAge.
type ArgoService struct {
	client   ArgoClient
	clock    Clock
	logger   *zap.Logger
	onChange func(context.Context)

	mu        sync.RWMutex
	apps      []domain.ArgoApp
	projects  []string
	snapshot  domain.ArgoSnapshot
	lastError string

	refreshMu sync.Mutex
}

func NewArgoService(client ArgoClient, clock Clock, logger *zap.Logger) *ArgoService {
	return &ArgoService{client: client, clock: clock, logger: logger}
}

// SetOnChange registers a callback run after every successful refresh.
func (s *ArgoService) SetOnChange(f func(context.Context)) { s.onChange = f }

// Refresh reads Applications and AppProjects. It keeps the old cache on error.
func (s *ArgoService) Refresh(ctx context.Context) error {
	s.refreshMu.Lock()
	defer s.refreshMu.Unlock()

	apps, err := s.client.ListApplications(ctx)
	if err == nil {
		var projects []string
		projects, err = s.client.ListProjects(ctx)
		if err == nil {
			s.store(apps, projects)
			if s.onChange != nil {
				s.onChange(ctx)
			}
			return nil
		}
	}
	s.mu.Lock()
	s.lastError = err.Error()
	s.mu.Unlock()
	s.logger.Error("argo refresh failed", zap.Error(err))
	return err
}

func (s *ArgoService) store(apps []domain.ArgoApp, projects []string) {
	now := s.clock.Now()
	sort.Slice(apps, func(i, j int) bool {
		if apps[i].Name != apps[j].Name {
			return apps[i].Name < apps[j].Name
		}
		return apps[i].Cluster < apps[j].Cluster
	})
	sort.Strings(projects)
	snap := domain.NewArgoSnapshot(now, projects, apps)
	s.mu.Lock()
	s.apps, s.projects, s.snapshot, s.lastError = apps, projects, snap, ""
	s.mu.Unlock()
}

// Run refreshes periodically until ctx is done.
func (s *ArgoService) Run(ctx context.Context) {
	_ = s.Refresh(ctx)
	t := time.NewTicker(ArgoRefreshInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			_ = s.Refresh(ctx)
			s.updateGauge()
		}
	}
}

func (s *ArgoService) updateGauge() {
	if st := s.Status(); st.AgeSeconds != nil {
		monitoring.ArgoCacheAge.Set(float64(*st.AgeSeconds))
	}
}

// Snapshot returns the current index for Decide.
func (s *ArgoService) Snapshot() domain.ArgoSnapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.snapshot
}

// Apps returns the cached applications.
func (s *ArgoService) Apps() []domain.ArgoApp {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.apps
}

// Projects returns the cached AppProject names.
func (s *ArgoService) Projects() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.projects
}

// AppsFor returns the applications deploying to the namespace.
func (s *ArgoService) AppsFor(cluster, namespace string) []domain.ArgoApp {
	var out []domain.ArgoApp
	for _, a := range s.Apps() {
		for _, p := range a.Pairs() {
			if p.Cluster == cluster && p.Namespace == namespace {
				out = append(out, a)
				break
			}
		}
	}
	return out
}

// Sync starts a sync of the application (see ArgoClient.SyncApplication).
func (s *ArgoService) Sync(ctx context.Context, appNamespace, name, triggeredBy string) (ArgoOperation, error) {
	return s.client.SyncApplication(ctx, appNamespace, name, triggeredBy)
}

// Operation returns the application's current operation state.
func (s *ArgoService) Operation(ctx context.Context, appNamespace, name string) (ArgoOperation, error) {
	return s.client.GetOperation(ctx, appNamespace, name)
}

// Details returns the application's conditions, last operation and history.
func (s *ArgoService) Details(ctx context.Context, appNamespace, name string) (ArgoAppDetails, error) {
	return s.client.GetAppDetails(ctx, appNamespace, name)
}

// Managed returns the application's resources with their states.
func (s *ArgoService) Managed(ctx context.Context, appNamespace, name string) ([]ArgoManagedResource, error) {
	return s.client.ManagedResources(ctx, appNamespace, name)
}

// Revision returns the commit behind a revision.
func (s *ArgoService) Revision(ctx context.Context, appNamespace, name, revision string) (ArgoRevision, error) {
	return s.client.RevisionMetadata(ctx, appNamespace, name, revision)
}

// Status describes the cache age for the UI.
func (s *ArgoService) Status() ArgoStatus {
	s.mu.RLock()
	defer s.mu.RUnlock()
	st := ArgoStatus{AppCount: len(s.apps), ProjectCount: len(s.projects), LastError: s.lastError}
	if s.snapshot.FetchedAt.IsZero() {
		st.Stale = true
		return st
	}
	t := s.snapshot.FetchedAt
	age := int64(s.clock.Now().Sub(t).Seconds())
	st.FetchedAt = &t
	st.AgeSeconds = &age
	st.Stale = s.clock.Now().Sub(t) > domain.DefaultArgoMaxAge
	return st
}
