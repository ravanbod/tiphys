package domain

import (
	"slices"
	"sort"
	"sync"
	"time"
)

// Reason is the machine-readable cause of a Decision.
type Reason string

const (
	ReasonAllowed            Reason = "allowed"
	ReasonAdmin              Reason = "admin"
	ReasonProtectedNamespace Reason = "protected_namespace"
	ReasonUserDisabled       Reason = "user_disabled"
	ReasonRole               Reason = "role"
	ReasonNoGrant            Reason = "no_grant"
	ReasonGrantExpired       Reason = "grant_expired"
	ReasonOutsideWindow      Reason = "outside_window"
	ReasonHoliday            Reason = "holiday"
	// ReasonArgoStale means a project-scoped grant could not be evaluated
	// because the Argo CD cache is older than Policy.ArgoMaxAge.
	ReasonArgoStale Reason = "argo_stale"
	// ReasonAppScope means an Argo CD application manages cluster-scoped
	// resources (or deploys nowhere the portal can name), which no namespace
	// grant covers: only admins may act on it as a whole.
	ReasonAppScope Reason = "app_scope"
)

// UnlabeledEnvironment is the environment of a target nothing labels.
const UnlabeledEnvironment = "unlabeled"

// DefaultArgoMaxAge is how old the Argo cache may be before project-scoped
// grants stop matching.
const DefaultArgoMaxAge = 15 * time.Minute

// scheduleHorizon bounds the search for reopens_at / closes_at.
const scheduleHorizon = 15 * 24 * time.Hour

// Decision is the result of Decide.
type Decision struct {
	Allowed      bool
	Reason       Reason
	GrantIDs     []int64
	Environment  string
	BreakGlassID *int64
	// ReopensAt is when a time-based denial ends (nil when unknown or not
	// denied by time).
	ReopensAt *time.Time
	// ClosesAt is the earliest instant the allowance ends: window close,
	// holiday start, grant expiry or break-glass end. Nil if unbounded.
	ClosesAt *time.Time
}

// Subject is the user a decision is made for. BreakGlass is the user's
// active or next scheduled break-glass (Decide checks whether it is active at
// the given time).
type Subject struct {
	ID         int64
	Role       Role
	Disabled   bool
	TeamIDs    []int64
	BreakGlass *BreakGlass
}

// ArgoApp is the part of an Argo CD Application the portal needs.
type ArgoApp struct {
	Name      string
	Project   string
	Cluster   string // cluster name as registered in Argo CD
	Namespace string // destination namespace
	// ResourceNamespaces are the namespaces of all resources in
	// status.resources.
	ResourceNamespaces []string
	Health             string
	Sync               string
	// AppNamespace is the namespace of the Application object itself (for
	// links into Argo CD).
	AppNamespace string
	// LastSyncAt and LastSyncPhase describe the last sync operation
	// (status.operationState); zero/empty if the app was never synced.
	LastSyncAt    time.Time
	LastSyncPhase string
	// ClusterResources are the cluster-scoped resources (no namespace) in
	// status.resources.
	ClusterResources []ClusterResource
	// AutoSync is set when spec.syncPolicy.automated is: Argo CD then
	// refuses a rollback (it would be synced back right away).
	AutoSync bool
	// MultiSource is set for apps with spec.sources.
	MultiSource bool
}

// Including returns the app as it would be with these resources added (a
// rollback deploys the resources of an earlier revision next to the ones it
// manages now, since it never prunes): their namespaces and cluster-scoped
// resources join the app's, so DecideApp covers both.
func (a ArgoApp) Including(namespaces []string, clusterResources []ClusterResource) ArgoApp {
	out := a
	out.ResourceNamespaces = slices.Clone(a.ResourceNamespaces)
	for _, ns := range namespaces {
		if !slices.Contains(out.ResourceNamespaces, ns) {
			out.ResourceNamespaces = append(out.ResourceNamespaces, ns)
		}
	}
	sort.Strings(out.ResourceNamespaces)
	out.ClusterResources = slices.Clone(a.ClusterResources)
	for _, r := range clusterResources {
		if !slices.Contains(out.ClusterResources, r) {
			out.ClusterResources = append(out.ClusterResources, r)
		}
	}
	return out
}

// ForeignClusterResources returns the app's cluster-scoped resources other
// than the Namespace objects of the namespaces it deploys to. Those not of a
// kind the settings allow leave syncing the app to admins
// (Policy.AdminOnlyResources, ReasonAppScope).
func (a ArgoApp) ForeignClusterResources() []ClusterResource {
	pairs := a.Pairs()
	var out []ClusterResource
	for _, r := range a.ClusterResources {
		own := false
		for _, p := range pairs {
			if r.Group == "" && r.Kind == "Namespace" && r.Name == p.Namespace {
				own = true
				break
			}
		}
		if !own {
			out = append(out, r)
		}
	}
	return out
}

// Pairs returns every (cluster, namespace) the app deploys to.
func (a ArgoApp) Pairs() []ClusterNS {
	seen := map[ClusterNS]struct{}{}
	var out []ClusterNS
	add := func(ns string) {
		if ns == "" || a.Cluster == "" {
			return
		}
		p := ClusterNS{a.Cluster, ns}
		if _, ok := seen[p]; !ok {
			seen[p] = struct{}{}
			out = append(out, p)
		}
	}
	add(a.Namespace)
	for _, ns := range a.ResourceNamespaces {
		add(ns)
	}
	return out
}

// ArgoSnapshot is an immutable view of the Argo CD cache.
type ArgoSnapshot struct {
	FetchedAt    time.Time
	Projects     map[string]map[ClusterNS]struct{}
	PairProjects map[ClusterNS][]string // sorted project names
}

// NewArgoSnapshot indexes apps by project and by (cluster, namespace).
func NewArgoSnapshot(fetchedAt time.Time, projects []string, apps []ArgoApp) ArgoSnapshot {
	s := ArgoSnapshot{
		FetchedAt:    fetchedAt,
		Projects:     map[string]map[ClusterNS]struct{}{},
		PairProjects: map[ClusterNS][]string{},
	}
	for _, p := range projects {
		s.Projects[p] = map[ClusterNS]struct{}{}
	}
	for _, a := range apps {
		set := s.Projects[a.Project]
		if set == nil {
			set = map[ClusterNS]struct{}{}
			s.Projects[a.Project] = set
		}
		for _, pair := range a.Pairs() {
			set[pair] = struct{}{}
		}
	}
	for proj, set := range s.Projects {
		for pair := range set {
			s.PairProjects[pair] = append(s.PairProjects[pair], proj)
		}
	}
	for _, v := range s.PairProjects {
		sort.Strings(v)
	}
	return s
}

// Covers reports whether the project deploys to the namespace.
func (s ArgoSnapshot) Covers(project string, cluster, namespace string) bool {
	_, ok := s.Projects[project][ClusterNS{cluster, namespace}]
	return ok
}

// Policy is an immutable snapshot of everything Decide reads. The
// application rebuilds it when data changes.
type Policy struct {
	grantsByUser map[int64][]Grant
	grantsByTeam map[int64][]Grant
	windows      []TimeWindow
	holidays     []Holiday
	clusterEnv   map[string]string
	namespaceEnv map[ClusterNS]string
	projectEnv   map[string]string
	protected    map[string]struct{}
	argo         ArgoSnapshot
	argoMaxAge   time.Duration
	defaultTZ    string
	// syncClusterKinds are the cluster-scoped kinds ("Kind.group") that do
	// not leave syncing an app to admins (Settings.ArgoSyncClusterKinds).
	syncClusterKinds map[string]struct{}
}

// PolicyInput is the raw data a Policy is built from.
type PolicyInput struct {
	Grants          []Grant
	Windows         []TimeWindow
	Holidays        []Holiday
	ClusterEnv      map[string]string
	NamespaceEnv    map[ClusterNS]string
	ProjectEnv      map[string]string
	Protected       []string
	Argo            ArgoSnapshot
	ArgoMaxAge      time.Duration // zero = DefaultArgoMaxAge
	DefaultTimezone string        // empty = DefaultTimezone
	// SyncClusterKinds is Settings.ArgoSyncClusterKinds.
	SyncClusterKinds []string
}

// NewPolicy indexes the input.
func NewPolicy(in PolicyInput) *Policy {
	p := &Policy{
		grantsByUser:     map[int64][]Grant{},
		grantsByTeam:     map[int64][]Grant{},
		clusterEnv:       in.ClusterEnv,
		namespaceEnv:     in.NamespaceEnv,
		projectEnv:       in.ProjectEnv,
		protected:        map[string]struct{}{},
		argo:             in.Argo,
		argoMaxAge:       in.ArgoMaxAge,
		defaultTZ:        in.DefaultTimezone,
		syncClusterKinds: map[string]struct{}{},
	}
	for _, k := range in.SyncClusterKinds {
		p.syncClusterKinds[k] = struct{}{}
	}
	if p.argoMaxAge == 0 {
		p.argoMaxAge = DefaultArgoMaxAge
	}
	if p.defaultTZ == "" {
		p.defaultTZ = DefaultTimezone
	}
	for _, g := range in.Grants {
		switch g.HolderKind {
		case HolderUser:
			p.grantsByUser[g.UserID] = append(p.grantsByUser[g.UserID], g)
		case HolderTeam:
			p.grantsByTeam[g.TeamID] = append(p.grantsByTeam[g.TeamID], g)
		}
	}
	for _, w := range in.Windows {
		if w.Enabled {
			p.windows = append(p.windows, w)
		}
	}
	p.holidays = in.Holidays
	for _, ns := range in.Protected {
		p.protected[ns] = struct{}{}
	}
	return p
}

// Argo returns the Argo snapshot the policy was built with.
func (p *Policy) Argo() ArgoSnapshot { return p.argo }

// IsProtected reports whether the namespace is protected on every cluster.
func (p *Policy) IsProtected(namespace string) bool {
	_, ok := p.protected[namespace]
	return ok
}

// ArgoFresh reports whether the Argo cache is recent enough at now.
func (p *Policy) ArgoFresh(now time.Time) bool {
	return !p.argo.FetchedAt.IsZero() && now.Sub(p.argo.FetchedAt) <= p.argoMaxAge
}

// Environment returns the effective environment of a target: namespace
// override, then Argo project override, then cluster label, then unlabeled.
func (p *Policy) Environment(cluster, namespace string) string {
	if env, ok := p.namespaceEnv[ClusterNS{cluster, namespace}]; ok && env != "" {
		return env
	}
	for _, proj := range p.argo.PairProjects[ClusterNS{cluster, namespace}] {
		if env, ok := p.projectEnv[proj]; ok && env != "" {
			return env
		}
	}
	if env := p.clusterEnv[cluster]; env != "" {
		return env
	}
	return UnlabeledEnvironment
}

// UserGrants returns the grants held by the subject directly or through a team.
func (p *Policy) UserGrants(sub Subject) []Grant {
	out := append([]Grant(nil), p.grantsByUser[sub.ID]...)
	for _, tid := range sub.TeamIDs {
		out = append(out, p.grantsByTeam[tid]...)
	}
	return out
}

// TeamGrants returns the grants held by a team.
func (p *Policy) TeamGrants(teamID int64) []Grant {
	return append([]Grant(nil), p.grantsByTeam[teamID]...)
}

// DecideApp decides an action on a whole Argo CD application. It is allowed
// only when Decide allows it on every (cluster, namespace) the app deploys
// to; otherwise the first denial is returned with the namespace it is for.
// A stale Argo cache denies (the app's namespaces may have changed), and an
// app with cluster-scoped resources (other than the Namespace objects of its
// own namespaces) or no known namespace is left to admins (ReasonAppScope). An allowed decision is the destination's, with the
// earliest ClosesAt and the break-glass of any namespace that needed one.
func DecideApp(p *Policy, sub Subject, app ArgoApp, action Action, now time.Time) (Decision, ClusterNS) {
	dest := ClusterNS{app.Cluster, app.Namespace}
	if !p.ArgoFresh(now) {
		return Decision{Reason: ReasonArgoStale, Environment: p.Environment(dest.Cluster, dest.Namespace)}, dest
	}
	pairs := app.Pairs()
	var out Decision
	for i, pair := range pairs {
		d := Decide(p, sub, Target{Cluster: pair.Cluster, Namespace: pair.Namespace}, action, now)
		if !d.Allowed {
			return d, pair
		}
		if i == 0 {
			out = d
			continue
		}
		if out.BreakGlassID == nil {
			out.BreakGlassID = d.BreakGlassID
		}
		if d.ClosesAt != nil && (out.ClosesAt == nil || d.ClosesAt.Before(*out.ClosesAt)) {
			out.ClosesAt = d.ClosesAt
		}
		out.GrantIDs = append(out.GrantIDs, d.GrantIDs...)
	}
	if len(pairs) == 0 || (out.Reason != ReasonAdmin && len(p.AdminOnlyResources(app)) > 0) {
		return Decision{Reason: ReasonAppScope, Environment: p.Environment(dest.Cluster, dest.Namespace)}, dest
	}
	return out, pairs[0]
}

// Decide is the only place access logic lives. It is pure: the same inputs
// always give the same Decision.
func Decide(p *Policy, sub Subject, t Target, action Action, now time.Time) Decision {
	ev := p.evaluate(sub, t, action, now)
	d := ev.Decision

	switch {
	case d.Allowed && d.Reason != ReasonAdmin:
		if closes, ok := p.findClosesAt(sub, t, action, now, ev); ok {
			d.ClosesAt = &closes
		}
	case !d.Allowed && (d.Reason == ReasonOutsideWindow || d.Reason == ReasonHoliday):
		if reopens, ok := p.findReopensAt(sub, t, action, now, d.Environment); ok {
			d.ReopensAt = &reopens
		}
	}
	return d
}

type evaluation struct {
	Decision
	matched []Grant
}

func (p *Policy) evaluate(sub Subject, t Target, action Action, at time.Time) evaluation {
	if t.Namespace != "" && p.IsProtected(t.Namespace) {
		return evaluation{Decision: Decision{Reason: ReasonProtectedNamespace, Environment: p.Environment(t.Cluster, t.Namespace)}}
	}
	if sub.Disabled {
		return evaluation{Decision: Decision{Reason: ReasonUserDisabled}}
	}
	env := p.Environment(t.Cluster, t.Namespace)
	switch {
	case sub.Role == RoleAdmin:
		return evaluation{Decision: Decision{Allowed: true, Reason: ReasonAdmin, Environment: env}}
	case !sub.Role.UsesGrants():
		return evaluation{Decision: Decision{Reason: ReasonRole, Environment: env}}
	}

	argoFresh := p.ArgoFresh(at)
	var active, expired []Grant
	staleSkipped := false
	for _, g := range p.UserGrants(sub) {
		if !g.HasAction(action) {
			continue
		}
		if g.Scope.Kind == ScopeArgoProject && !argoFresh {
			staleSkipped = true
			continue
		}
		if !p.grantCovers(g, t) {
			continue
		}
		if g.Expired(at) {
			expired = append(expired, g)
		} else {
			active = append(active, g)
		}
	}
	if len(active) == 0 {
		reason := ReasonNoGrant
		switch {
		case len(expired) > 0:
			reason = ReasonGrantExpired
		case staleSkipped:
			reason = ReasonArgoStale
		}
		return evaluation{Decision: Decision{Reason: reason, Environment: env}}
	}
	ids := make([]int64, len(active))
	for i, g := range active {
		ids[i] = g.ID
	}

	closed, why := p.timeClosed(sub, t, env, action, at)
	d := Decision{Allowed: true, Reason: ReasonAllowed, GrantIDs: ids, Environment: env}
	if closed {
		if sub.BreakGlass.ActiveAt(at) {
			id := sub.BreakGlass.ID
			d.BreakGlassID = &id
		} else {
			d.Allowed = false
			d.Reason = why
		}
	}
	return evaluation{Decision: d, matched: active}
}

func (p *Policy) grantCovers(g Grant, t Target) bool {
	s := g.Scope
	switch s.Kind {
	case ScopeArgoProject:
		return t.Namespace != "" && p.argo.Covers(s.ArgoProject, t.Cluster, t.Namespace)
	case ScopeNamespace:
		return s.Cluster == t.Cluster && s.Namespace == t.Namespace
	case ScopeCluster:
		return t.Namespace != "" && s.Cluster == t.Cluster
	case ScopeEnvironment:
		return t.Namespace != "" && p.Environment(t.Cluster, t.Namespace) == s.Environment
	case ScopeWorkloads:
		if s.Cluster != t.Cluster || s.Namespace != t.Namespace || !t.HasWorkload() {
			return false
		}
		for _, w := range s.Workloads {
			if w == t.Workload {
				return true
			}
		}
	}
	return false
}

// GrantReaches reports whether the grant's scope reaches the target, for
// listing who has access to a namespace or workload. A namespace target is
// also reached by a workloads grant in it, which covers part of it. Expiry,
// time rules and Argo staleness are not considered.
func (p *Policy) GrantReaches(g Grant, t Target) bool {
	if !t.HasWorkload() && g.Scope.Kind == ScopeWorkloads {
		return g.Scope.Cluster == t.Cluster && g.Scope.Namespace == t.Namespace
	}
	return p.grantCovers(g, t)
}

// GrantsReaching returns the grants in the policy that reach the target and
// have not expired at now, ordered by ID.
func (p *Policy) GrantsReaching(t Target, now time.Time) []Grant {
	var out []Grant
	add := func(gs []Grant) {
		for _, g := range gs {
			if !g.Expired(now) && p.GrantReaches(g, t) {
				out = append(out, g)
			}
		}
	}
	for _, gs := range p.grantsByUser {
		add(gs)
	}
	for _, gs := range p.grantsByTeam {
		add(gs)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// timeClosed reports whether time windows or holidays close the target at
// the given instant, and why. Break-glass is not considered here.
func (p *Policy) timeClosed(sub Subject, t Target, env string, action Action, at time.Time) (bool, Reason) {
	// Reading logs is never limited by time windows or holidays (so it never
	// needs break-glass); rules that list `logs` are ignored.
	if action == ActionLogs {
		return false, ""
	}
	var allows, denies []TimeWindow
	zones := map[string]struct{}{p.defaultTZ: {}}
	for _, w := range p.windows {
		if !windowHasAction(w, action) || !p.windowApplies(w, sub, t, env) {
			continue
		}
		zones[windowTZ(w, p.defaultTZ)] = struct{}{}
		if w.Effect == AllowInside {
			allows = append(allows, w)
		} else {
			denies = append(denies, w)
		}
	}

	for tz := range zones {
		loc := loadLocation(tz)
		y, m, d := at.In(loc).Date()
		for _, h := range p.holidays {
			hy, hm, hd := h.Date.Date()
			if hy != y || hm != m || hd != d {
				continue
			}
			if len(h.Environments) == 0 || contains(h.Environments, env) {
				return true, ReasonHoliday
			}
		}
	}

	if len(allows) > 0 {
		inside := false
		for _, w := range allows {
			if insideWindow(w, p.defaultTZ, at) {
				inside = true
				break
			}
		}
		if !inside {
			return true, ReasonOutsideWindow
		}
	}
	for _, w := range denies {
		if insideWindow(w, p.defaultTZ, at) {
			return true, ReasonOutsideWindow
		}
	}
	return false, ""
}

func (p *Policy) windowApplies(w TimeWindow, sub Subject, t Target, env string) bool {
	switch w.ScopeKind {
	case WindowGlobal:
		return true
	case WindowEnvironment:
		return w.ScopeValue == env
	case WindowCluster:
		return w.ScopeValue == t.Cluster
	case WindowArgoProject:
		for _, proj := range p.argo.PairProjects[ClusterNS{t.Cluster, t.Namespace}] {
			if proj == w.ScopeValue {
				return true
			}
		}
		return false
	case WindowTeam:
		for _, id := range sub.TeamIDs {
			if id == w.TeamID {
				return true
			}
		}
	}
	return false
}

func windowHasAction(w TimeWindow, a Action) bool {
	for _, x := range w.Actions {
		if x == a {
			return true
		}
	}
	return false
}

func windowTZ(w TimeWindow, def string) string {
	if w.Timezone != "" {
		return w.Timezone
	}
	return def
}

// insideWindow reports whether at falls inside the window, including windows
// that cross midnight (end <= start).
func insideWindow(w TimeWindow, defaultTZ string, at time.Time) bool {
	loc := loadLocation(windowTZ(w, defaultTZ))
	lt := at.In(loc)
	mins := TimeOfDay(lt.Hour()*60 + lt.Minute())
	today := hasDay(w.Days, lt.Weekday())
	if w.Start < w.End {
		return today && mins >= w.Start && mins < w.End
	}
	// Crossing midnight (or a full 24h window when start == end).
	yesterday := hasDay(w.Days, (lt.Weekday()+6)%7)
	return (today && mins >= w.Start) || (yesterday && mins < w.End)
}

func hasDay(days []time.Weekday, d time.Weekday) bool {
	for _, x := range days {
		if x == d {
			return true
		}
	}
	return false
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

var locCache sync.Map

func loadLocation(name string) *time.Location {
	if v, ok := locCache.Load(name); ok {
		return v.(*time.Location)
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		loc = time.UTC
	}
	locCache.Store(name, loc)
	return loc
}

// boundaries returns the instants after `after` (up to the horizon) where a
// window or holiday boundary may change the outcome of timeClosed, for the
// windows that apply to this request.
func (p *Policy) boundaries(sub Subject, t Target, env string, action Action, after time.Time) []time.Time {
	if action == ActionLogs {
		return nil // time rules never apply to logs
	}
	end := after.Add(scheduleHorizon)
	var out []time.Time
	add := func(x time.Time) {
		if x.After(after) && !x.After(end) {
			out = append(out, x)
		}
	}
	zones := map[string]struct{}{p.defaultTZ: {}}
	for _, w := range p.windows {
		if !windowHasAction(w, action) || !p.windowApplies(w, sub, t, env) {
			continue
		}
		tz := windowTZ(w, p.defaultTZ)
		zones[tz] = struct{}{}
		loc := loadLocation(tz)
		base := after.In(loc)
		for off := -1; off <= int(scheduleHorizon/(24*time.Hour))+1; off++ {
			day := base.AddDate(0, 0, off)
			y, m, d := day.Date()
			start := time.Date(y, m, d, w.Start.Hour(), w.Start.Minute(), 0, 0, loc)
			endDay := day
			if w.End <= w.Start {
				endDay = day.AddDate(0, 0, 1)
			}
			ey, em, ed := endDay.Date()
			stop := time.Date(ey, em, ed, w.End.Hour(), w.End.Minute(), 0, 0, loc)
			add(start)
			add(stop)
		}
	}
	if len(p.holidays) > 0 {
		for tz := range zones {
			loc := loadLocation(tz)
			base := after.In(loc)
			for off := 0; off <= int(scheduleHorizon/(24*time.Hour))+1; off++ {
				y, m, d := base.AddDate(0, 0, off).Date()
				add(time.Date(y, m, d, 0, 0, 0, 0, loc))
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Before(out[j]) })
	return out
}

// findReopensAt finds the first boundary, or the start of a scheduled
// break-glass, after now at which time no longer closes the request.
func (p *Policy) findReopensAt(sub Subject, t Target, action Action, now time.Time, env string) (time.Time, bool) {
	cands := p.boundaries(sub, t, env, action, now)
	if bg := sub.BreakGlass; bg != nil && bg.StartedAt.After(now) && !bg.StartedAt.After(now.Add(scheduleHorizon)) {
		cands = append(cands, bg.StartedAt)
		sort.Slice(cands, func(i, j int) bool { return cands[i].Before(cands[j]) })
	}
	for _, c := range cands {
		if closed, _ := p.timeClosed(sub, t, env, action, c); !closed || sub.BreakGlass.ActiveAt(c) {
			return c, true
		}
	}
	return time.Time{}, false
}

// findClosesAt finds the first instant after now at which the same request
// would no longer be allowed.
func (p *Policy) findClosesAt(sub Subject, t Target, action Action, now time.Time, ev evaluation) (time.Time, bool) {
	end := now.Add(scheduleHorizon)
	cands := p.boundaries(sub, t, ev.Environment, action, now)
	for _, g := range ev.matched {
		if g.ExpiresAt != nil && g.ExpiresAt.After(now) && !g.ExpiresAt.After(end) {
			cands = append(cands, *g.ExpiresAt)
		}
	}
	if bg := sub.BreakGlass; bg != nil {
		if e := bg.EffectiveEnd(); e.After(now) && !e.After(end) {
			cands = append(cands, e)
		}
	}
	sort.Slice(cands, func(i, j int) bool { return cands[i].Before(cands[j]) })
	for _, c := range cands {
		if !p.evaluate(sub, t, action, c).Allowed {
			return c, true
		}
	}
	return time.Time{}, false
}

// HoldsGrant reports whether the decision shows the subject has a usable
// grant (access may still be closed by time). The UI uses it to decide what
// to list; the denial reason explains the rest.
func (d Decision) HoldsGrant() bool {
	switch d.Reason {
	case ReasonAllowed, ReasonAdmin, ReasonOutsideWindow, ReasonHoliday:
		return true
	}
	return false
}

// GrantedActions returns the actions the subject holds a usable grant for in
// the namespace, ignoring time windows. For workload-scoped grants it counts
// when any listed workload of the namespace is granted the action.
func (p *Policy) GrantedActions(sub Subject, cluster, namespace string, now time.Time) []Action {
	var out []Action
	for _, a := range AllActions {
		if p.holdsInNamespace(sub, cluster, namespace, a, now) {
			out = append(out, a)
		}
	}
	return out
}

func (p *Policy) holdsInNamespace(sub Subject, cluster, namespace string, a Action, now time.Time) bool {
	if p.evaluate(sub, Target{Cluster: cluster, Namespace: namespace}, a, now).HoldsGrant() {
		return true
	}
	if !sub.Role.UsesGrants() {
		return false
	}
	for _, g := range p.UserGrants(sub) {
		s := g.Scope
		if s.Kind != ScopeWorkloads || s.Cluster != cluster || s.Namespace != namespace || !g.HasAction(a) {
			continue
		}
		for _, w := range s.Workloads {
			t := Target{Cluster: cluster, Namespace: namespace, Workload: w}
			if p.evaluate(sub, t, a, now).HoldsGrant() {
				return true
			}
		}
	}
	return false
}
