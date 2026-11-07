package application

import (
	"context"
	"sort"
	"time"

	"tiphys/internal/domain"
	"tiphys/internal/monitoring"
)

// AccessService is the application's single entry to authorization: every
// HTTP handler, WebSocket handler and the stream supervisor decides through
// it, and it calls domain.Decide. It adds auditing of denials.
type AccessService struct {
	policy *PolicyService
	audit  *AuditService
	clock  Clock
}

func NewAccessService(policy *PolicyService, audit *AuditService, clock Clock) *AccessService {
	return &AccessService{policy: policy, audit: audit, clock: clock}
}

// DecideFor runs Decide for a user id against the current snapshot, at now.
// An unknown user is denied as disabled.
func (s *AccessService) DecideFor(userID int64, t domain.Target, a domain.Action, now time.Time) domain.Decision {
	snap := s.policy.Current()
	sub, ok := snap.Subject(userID)
	if !ok {
		return domain.Decision{Reason: domain.ReasonUserDisabled}
	}
	start := time.Now()
	d := domain.Decide(snap.Policy, sub, t, a, now)
	monitoring.DecisionDuration.Observe(time.Since(start).Seconds())
	return d
}

// Decide decides for the principal at the current time, without auditing.
// It is for building UI state (which buttons are enabled).
func (s *AccessService) Decide(p Principal, t domain.Target, a domain.Action) domain.Decision {
	return s.DecideFor(p.User.ID, t, a, s.clock.Now())
}

// AccessMap decides every action for a target.
func (s *AccessService) AccessMap(p Principal, t domain.Target) map[domain.Action]domain.Decision {
	m := make(map[domain.Action]domain.Decision, len(domain.WorkloadActions))
	for _, a := range domain.WorkloadActions {
		m[a] = s.Decide(p, t, a)
	}
	return m
}

// Authorize decides and, when denied, records a decision.denied audit event
// and returns a *DeniedError. This is the check every action goes through
// before it starts.
func (s *AccessService) Authorize(ctx context.Context, p Principal, t domain.Target, a domain.Action) (domain.Decision, error) {
	d := s.Decide(p, t, a)
	if d.Allowed {
		return d, nil
	}
	monitoring.ActionsTotal.WithLabelValues(string(a), "denied").Inc()
	e := s.audit.Event(p, domain.EventDecisionDenied)
	fillTarget(&e, t)
	e.Action = string(a)
	e.Environment = d.Environment
	e.Result = domain.ResultDenied
	e.Reason = string(d.Reason)
	e.SessionID = ""
	s.audit.RecordBestEffort(ctx, e)
	return d, &DeniedError{Decision: d, Target: t, Action: a}
}

// DecideApp decides an action on a whole Argo CD application (every
// namespace it deploys to), without auditing; it is for UI state.
func (s *AccessService) DecideApp(p Principal, app domain.ArgoApp, a domain.Action) (domain.Decision, domain.ClusterNS) {
	snap := s.policy.Current()
	sub, ok := snap.Subject(p.User.ID)
	if !ok {
		return domain.Decision{Reason: domain.ReasonUserDisabled}, domain.ClusterNS{Cluster: app.Cluster, Namespace: app.Namespace}
	}
	start := time.Now()
	d, at := domain.DecideApp(snap.Policy, sub, app, a, s.clock.Now())
	monitoring.DecisionDuration.Observe(time.Since(start).Seconds())
	return d, at
}

// AdminOnlyResources returns the app's cluster-scoped resources that leave
// syncing it to admins (domain.Policy.AdminOnlyResources), to explain an
// app_scope denial.
func (s *AccessService) AdminOnlyResources(app domain.ArgoApp) []domain.ClusterResource {
	return s.policy.Current().Policy.AdminOnlyResources(app)
}

// AuthorizeApp is Authorize for an action on a whole Argo CD application. A
// denial is audited for the namespace that denied it, naming the app.
func (s *AccessService) AuthorizeApp(ctx context.Context, p Principal, app domain.ArgoApp, a domain.Action) (domain.Decision, error) {
	d, at := s.DecideApp(p, app, a)
	if d.Allowed {
		return d, nil
	}
	t := domain.Target{Cluster: at.Cluster, Namespace: at.Namespace}
	monitoring.ActionsTotal.WithLabelValues(string(a), "denied").Inc()
	e := s.audit.Event(p, domain.EventDecisionDenied)
	fillTarget(&e, t)
	e.Action = string(a)
	e.Environment = d.Environment
	e.Result = domain.ResultDenied
	e.Reason = string(d.Reason)
	e.Details = map[string]any{"app": app.Name, "appNamespace": app.AppNamespace}
	s.audit.RecordBestEffort(ctx, e)
	return d, &DeniedError{Decision: d, Target: t, Action: a}
}

// GrantedActions lists the actions the principal holds a usable grant for in
// a namespace (ignoring time windows).
func (s *AccessService) GrantedActions(p Principal, cluster, namespace string) []domain.Action {
	snap := s.policy.Current()
	sub, ok := snap.Subject(p.User.ID)
	if !ok {
		return nil
	}
	return snap.Policy.GrantedActions(sub, cluster, namespace, s.clock.Now())
}

// GrantsReaching lists the unexpired grants that reach a namespace or workload.
func (s *AccessService) GrantsReaching(t domain.Target) []domain.Grant {
	return s.policy.Current().Policy.GrantsReaching(t, s.clock.Now())
}

// Environment returns the effective environment of a namespace.
func (s *AccessService) Environment(cluster, namespace string) string {
	return s.policy.Current().Policy.Environment(cluster, namespace)
}

// UsableClusters lists the registered clusters that are reachable and passed
// the privilege check.
func (s *AccessService) UsableClusters() []string {
	var out []string
	for name, c := range s.policy.Current().Clusters {
		if c.Reachable && c.PrivilegeCheckOK {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// IsProtected reports whether the namespace is protected.
func (s *AccessService) IsProtected(namespace string) bool {
	return s.policy.Current().Policy.IsProtected(namespace)
}

// HiddenCluster reports whether the cluster is protected (tiphys/protected)
// and therefore left out of the principal's pickers and requests: for
// everyone but admins. Grants on it still decide access as usual.
func (s *AccessService) HiddenCluster(p Principal, cluster string) bool {
	return p.User.Role != domain.RoleAdmin && s.policy.Current().Clusters[cluster].Protected
}

// RequireRole fails with ErrForbiddenRole unless the principal has one of the roles.
func RequireRole(p Principal, roles ...domain.Role) error {
	for _, r := range roles {
		if p.User.Role == r && !p.User.Disabled {
			return nil
		}
	}
	return ErrForbiddenRole
}

func fillTarget(e *domain.AuditEvent, t domain.Target) {
	e.Cluster = t.Cluster
	e.Namespace = t.Namespace
	e.WorkloadKind = string(t.Workload.Kind)
	e.WorkloadName = t.Workload.Name
	e.Pod = t.Pod
	e.Container = t.Container
}

// subjectOrZero returns the Decide subject for a user, or an empty subject
// when the user is unknown.
func (s *AccessService) subjectOrZero(userID int64) domain.Subject {
	sub, _ := s.policy.Current().Subject(userID)
	return sub
}
