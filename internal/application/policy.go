package application

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"go.uber.org/zap"

	"tiphys/internal/domain"
)

// PolicyReloadInterval is the safety-net reload period. Changes made through
// this process reload immediately; the timer covers other replicas.
const PolicyReloadInterval = 5 * time.Second

// PolicySnapshot is everything authorization needs, loaded from PostgreSQL
// and the Argo cache. It is immutable once published.
type PolicySnapshot struct {
	Policy        *domain.Policy
	Users         map[int64]domain.User
	UsersByName   map[string]domain.User // lower-case username
	Teams         map[int64]domain.Team
	TeamIDsByUser map[int64][]int64
	BreakGlass    map[int64]*domain.BreakGlass // break-glass active or scheduled at load time, per user
	Clusters      map[string]domain.Cluster
	Version       uint64
	LoadedAt      time.Time
}

// Subject builds the Decide subject for a user from this snapshot.
func (s *PolicySnapshot) Subject(userID int64) (domain.Subject, bool) {
	u, ok := s.Users[userID]
	if !ok {
		return domain.Subject{}, false
	}
	return domain.Subject{
		ID:         u.ID,
		Role:       u.Role,
		Disabled:   u.Disabled,
		TeamIDs:    s.TeamIDsByUser[u.ID],
		BreakGlass: s.BreakGlass[u.ID],
	}, true
}

// PolicyService keeps the current PolicySnapshot and tells subscribers (the
// stream supervisor) when it changes.
type PolicyService struct {
	store  Store
	argo   *ArgoService
	clock  Clock
	logger *zap.Logger

	// breakGlassEnabled says whether active break-glasses may count; nil means yes.
	breakGlassEnabled func() bool
	// syncClusterKinds returns Settings.ArgoSyncClusterKinds; nil means none.
	syncClusterKinds func() []string

	current atomic.Pointer[PolicySnapshot]
	version atomic.Uint64
	loadMu  sync.Mutex

	subMu sync.Mutex
	subs  map[int]chan struct{}
	next  int
}

func NewPolicyService(store Store, argo *ArgoService, clock Clock, logger *zap.Logger) *PolicyService {
	s := &PolicyService{store: store, argo: argo, clock: clock, logger: logger, subs: map[int]chan struct{}{}}
	s.current.Store(&PolicySnapshot{
		Policy: domain.NewPolicy(domain.PolicyInput{}),
		Users:  map[int64]domain.User{}, UsersByName: map[string]domain.User{},
		Teams: map[int64]domain.Team{}, TeamIDsByUser: map[int64][]int64{},
		BreakGlass: map[int64]*domain.BreakGlass{}, Clusters: map[string]domain.Cluster{},
	})
	return s
}

// SetBreakGlassEnabled sets the function that tells whether the break-glass
// feature is on. While it is off, no break-glass is attached to decisions, so
// time windows and holidays apply to everyone.
func (s *PolicyService) SetBreakGlassEnabled(f func() bool) { s.breakGlassEnabled = f }

// SetSyncClusterKinds sets the function that returns the cluster-scoped kinds
// that do not leave syncing an app to admins (Settings.ArgoSyncClusterKinds).
// Like SetBreakGlassEnabled, it is called before Run.
func (s *PolicyService) SetSyncClusterKinds(f func() []string) { s.syncClusterKinds = f }

// Current returns the latest snapshot.
func (s *PolicyService) Current() *PolicySnapshot { return s.current.Load() }

// Reload rebuilds the snapshot from the database and the Argo cache.
func (s *PolicyService) Reload(ctx context.Context) error {
	s.loadMu.Lock()
	defer s.loadMu.Unlock()
	repos := s.store.Repos()
	now := s.clock.Now()

	users, err := repos.Users.List(ctx)
	if err != nil {
		return err
	}
	teams, err := repos.Teams.List(ctx)
	if err != nil {
		return err
	}
	grants, err := repos.Grants.List(ctx)
	if err != nil {
		return err
	}
	windows, err := repos.Windows.List(ctx)
	if err != nil {
		return err
	}
	holidays, err := repos.Holidays.List(ctx)
	if err != nil {
		return err
	}
	clusters, err := repos.Clusters.List(ctx)
	if err != nil {
		return err
	}
	overrides, err := repos.Clusters.ListEnvironmentOverrides(ctx)
	if err != nil {
		return err
	}
	protected, err := repos.Clusters.ListProtectedNamespaces(ctx)
	if err != nil {
		return err
	}
	activeBG, err := repos.BreakGlass.ListCurrent(ctx, now)
	if err != nil {
		return err
	}

	snap := &PolicySnapshot{
		Users:         make(map[int64]domain.User, len(users)),
		UsersByName:   make(map[string]domain.User, len(users)),
		Teams:         make(map[int64]domain.Team, len(teams)),
		TeamIDsByUser: map[int64][]int64{},
		BreakGlass:    map[int64]*domain.BreakGlass{},
		Clusters:      make(map[string]domain.Cluster, len(clusters)),
		LoadedAt:      now,
		Version:       s.version.Add(1),
	}
	for _, u := range users {
		snap.Users[u.ID] = u
		snap.UsersByName[strings.ToLower(u.Username)] = u
	}
	teamByName := map[string]int64{}
	for _, t := range teams {
		snap.Teams[t.ID] = t
		teamByName[t.Name] = t.ID
		for _, uid := range t.MemberIDs {
			snap.TeamIDsByUser[uid] = append(snap.TeamIDsByUser[uid], t.ID)
		}
	}
	if s.breakGlassEnabled != nil && !s.breakGlassEnabled() {
		activeBG = nil
	}
	// One per user: the active one, else the next scheduled one. A user has
	// more only through the previous release during a rollout.
	for i := range activeBG {
		b := activeBG[i]
		cur, ok := snap.BreakGlass[b.UserID]
		switch {
		case !ok, b.ActiveAt(now) && (!cur.ActiveAt(now) || b.StartedAt.After(cur.StartedAt)),
			!b.ActiveAt(now) && !cur.ActiveAt(now) && b.StartedAt.Before(cur.StartedAt):
			snap.BreakGlass[b.UserID] = &b
		}
	}

	clusterEnv := map[string]string{}
	for _, c := range clusters {
		snap.Clusters[c.Name] = c
		clusterEnv[c.Name] = c.Environment
	}
	nsEnv := map[domain.ClusterNS]string{}
	projEnv := map[string]string{}
	for _, o := range overrides {
		switch o.Kind {
		case domain.OverrideNamespace:
			nsEnv[domain.ClusterNS{Cluster: o.Cluster, Namespace: o.Namespace}] = o.Environment
		case domain.OverrideArgoProject:
			projEnv[o.ArgoProject] = o.Environment
		}
	}
	for i := range windows {
		if windows[i].ScopeKind == domain.WindowTeam && windows[i].TeamID == 0 {
			windows[i].TeamID = teamByName[windows[i].ScopeValue]
		}
	}

	var kinds []string
	if s.syncClusterKinds != nil {
		kinds = s.syncClusterKinds()
	}
	snap.Policy = domain.NewPolicy(domain.PolicyInput{
		Grants: grants, Windows: windows, Holidays: holidays,
		ClusterEnv: clusterEnv, NamespaceEnv: nsEnv, ProjectEnv: projEnv,
		Protected: protected, Argo: s.argo.Snapshot(), SyncClusterKinds: kinds,
	})
	s.current.Store(snap)
	s.notify()
	return nil
}

// Changed reloads the snapshot after a mutation and wakes subscribers. A
// reload failure is logged; the periodic reload will retry.
func (s *PolicyService) Changed(ctx context.Context) {
	if err := s.Reload(ctx); err != nil {
		s.logger.Error("policy reload failed after change", zap.Error(err))
	}
}

// Run reloads periodically until ctx is done.
func (s *PolicyService) Run(ctx context.Context) {
	t := time.NewTicker(PolicyReloadInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := s.Reload(ctx); err != nil {
				s.logger.Error("periodic policy reload failed", zap.Error(err))
			}
		}
	}
}

// Subscribe returns a channel that receives a value after each reload, and a
// function that cancels the subscription.
func (s *PolicyService) Subscribe() (<-chan struct{}, func()) {
	s.subMu.Lock()
	defer s.subMu.Unlock()
	id := s.next
	s.next++
	ch := make(chan struct{}, 1)
	s.subs[id] = ch
	return ch, func() {
		s.subMu.Lock()
		delete(s.subs, id)
		s.subMu.Unlock()
	}
}

func (s *PolicyService) notify() {
	s.subMu.Lock()
	defer s.subMu.Unlock()
	for _, ch := range s.subs {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}
