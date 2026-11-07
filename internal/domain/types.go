// Package domain holds the concepts intrinsic to Tiphys: actions, targets,
// grants, time rules, and the pure authorization function Decide. It imports
// nothing from the rest of the project.
package domain

import (
	"fmt"
	"regexp"
	"time"
)

// Action is something a user can do to a workload or, for sync and
// rollback, to an Argo CD application.
type Action string

const (
	ActionLogs    Action = "logs" // includes events and pod status
	ActionExec    Action = "exec"
	ActionRestart Action = "restart"
	ActionRunJob  Action = "run-job"
	// ActionSync syncs a whole Argo CD application (see DecideApp).
	ActionSync Action = "sync"
	// ActionRollback rolls a whole Argo CD application back to an earlier
	// deploy of its history (see DecideApp). It is separate from sync, since
	// it deploys what is no longer in Git.
	ActionRollback Action = "rollback"
)

// AllActions lists every action in display order.
var AllActions = []Action{ActionLogs, ActionExec, ActionRestart, ActionRunJob, ActionSync, ActionRollback}

// WorkloadActions are the actions on a workload (all but the app actions).
var WorkloadActions = []Action{ActionLogs, ActionExec, ActionRestart, ActionRunJob}

func (a Action) Valid() bool {
	switch a {
	case ActionLogs, ActionExec, ActionRestart, ActionRunJob, ActionSync, ActionRollback:
		return true
	}
	return false
}

// OnApp reports whether the action applies to a whole Argo CD application
// (sync, rollback) rather than to a workload.
func (a Action) OnApp() bool { return a == ActionSync || a == ActionRollback }

// ValidateScopeActions checks the actions and that they fit the scope: the
// app actions (sync, rollback) need a scope wider than workloads, since a
// workloads scope cannot cover an Argo CD application.
func ValidateScopeActions(s Scope, actions []Action) error {
	if len(actions) == 0 {
		return fmt.Errorf("at least one action is required")
	}
	for _, a := range actions {
		if !a.Valid() {
			return fmt.Errorf("invalid action %q", a)
		}
		if a.OnApp() && s.Kind == ScopeWorkloads {
			return fmt.Errorf("%s needs a namespace, cluster, environment or Argo project scope: it applies to whole Argo CD applications", a)
		}
	}
	return nil
}

// Role is the single portal role of a user.
type Role string

const (
	RoleUser    Role = "user"
	RoleAuditor Role = "auditor"
	RoleAdmin   Role = "admin"
)

func (r Role) Valid() bool {
	return r == RoleUser || r == RoleAuditor || r == RoleAdmin
}

// UsesGrants reports whether the role's access to workloads comes from
// grants. An auditor works like a user and can also read audit data and
// configuration; only admins have access without grants.
func (r Role) UsesGrants() bool {
	return r == RoleUser || r == RoleAuditor
}

// WorkloadKind is a kind of Kubernetes workload the portal can address.
type WorkloadKind string

const (
	KindDeployment  WorkloadKind = "Deployment"
	KindStatefulSet WorkloadKind = "StatefulSet"
	KindDaemonSet   WorkloadKind = "DaemonSet"
	KindRollout     WorkloadKind = "Rollout"
	KindCronJob     WorkloadKind = "CronJob"
)

var AllWorkloadKinds = []WorkloadKind{KindDeployment, KindStatefulSet, KindDaemonSet, KindRollout, KindCronJob}

func (k WorkloadKind) Valid() bool {
	for _, v := range AllWorkloadKinds {
		if v == k {
			return true
		}
	}
	return false
}

// Restartable reports whether Restart applies to this kind.
func (k WorkloadKind) Restartable() bool {
	switch k {
	case KindDeployment, KindStatefulSet, KindDaemonSet, KindRollout:
		return true
	}
	return false
}

// WorkloadRef names a workload within a namespace.
type WorkloadRef struct {
	Kind WorkloadKind `json:"kind"`
	Name string       `json:"name"`
}

func (w WorkloadRef) String() string { return string(w.Kind) + "/" + w.Name }

// Target is what an action is applied to. Workload is zero for
// namespace-level targets (listing workloads in a namespace).
type Target struct {
	Cluster   string
	Namespace string
	Workload  WorkloadRef
	Pod       string
	Container string
}

func (t Target) HasWorkload() bool { return t.Workload.Name != "" }

// ClusterNS identifies a namespace on a cluster.
type ClusterNS struct {
	Cluster   string
	Namespace string
}

// ScopeKind is how a grant selects targets.
type ScopeKind string

const (
	ScopeArgoProject ScopeKind = "argo_project"
	ScopeNamespace   ScopeKind = "namespace"
	ScopeWorkloads   ScopeKind = "workloads"
	// ScopeCluster covers every namespace of a cluster, including later ones.
	ScopeCluster ScopeKind = "cluster"
	// ScopeEnvironment covers every namespace whose Policy.Environment is the
	// scope's, so it follows cluster labels and environment overrides.
	ScopeEnvironment ScopeKind = "environment"
)

func (s ScopeKind) Valid() bool {
	switch s {
	case ScopeArgoProject, ScopeNamespace, ScopeWorkloads, ScopeCluster, ScopeEnvironment:
		return true
	}
	return false
}

// Scope selects targets. Which fields are set depends on Kind:
//   - argo_project: ArgoProject
//   - namespace:    Cluster, Namespace
//   - workloads:    Cluster, Namespace, Workloads
//   - cluster:      Cluster
//   - environment:  Environment
type Scope struct {
	Kind        ScopeKind     `json:"kind"`
	ArgoProject string        `json:"argoProject,omitempty"`
	Environment string        `json:"environment,omitempty"`
	Cluster     string        `json:"cluster,omitempty"`
	Namespace   string        `json:"namespace,omitempty"`
	Workloads   []WorkloadRef `json:"workloads,omitempty"`
}

// Validate checks that exactly the fields for the scope's kind are set.
func (s Scope) Validate() error {
	switch s.Kind {
	case ScopeArgoProject:
		if s.ArgoProject == "" {
			return fmt.Errorf("argo_project scope requires argoProject")
		}
		if s.Cluster != "" || s.Namespace != "" || len(s.Workloads) > 0 || s.Environment != "" {
			return fmt.Errorf("argo_project scope must not set cluster, namespace, workloads or environment")
		}
	case ScopeCluster:
		if s.Cluster == "" {
			return fmt.Errorf("cluster scope requires cluster")
		}
		if s.ArgoProject != "" || s.Namespace != "" || len(s.Workloads) > 0 || s.Environment != "" {
			return fmt.Errorf("cluster scope must not set argoProject, namespace, workloads or environment")
		}
	case ScopeEnvironment:
		if s.Environment == "" {
			return fmt.Errorf("environment scope requires environment")
		}
		if s.Environment == UnlabeledEnvironment {
			return fmt.Errorf("environment %q cannot be granted", UnlabeledEnvironment)
		}
		if s.ArgoProject != "" || s.Cluster != "" || s.Namespace != "" || len(s.Workloads) > 0 {
			return fmt.Errorf("environment scope must not set argoProject, cluster, namespace or workloads")
		}
	case ScopeNamespace:
		if s.Cluster == "" || s.Namespace == "" {
			return fmt.Errorf("namespace scope requires cluster and namespace")
		}
		if s.ArgoProject != "" || len(s.Workloads) > 0 || s.Environment != "" {
			return fmt.Errorf("namespace scope must not set argoProject, workloads or environment")
		}
		if !ValidDNS1123Label(s.Namespace) {
			return fmt.Errorf("invalid namespace %q", s.Namespace)
		}
	case ScopeWorkloads:
		if s.Cluster == "" || s.Namespace == "" || len(s.Workloads) == 0 {
			return fmt.Errorf("workloads scope requires cluster, namespace and at least one workload")
		}
		if s.ArgoProject != "" || s.Environment != "" {
			return fmt.Errorf("workloads scope must not set argoProject or environment")
		}
		if !ValidDNS1123Label(s.Namespace) {
			return fmt.Errorf("invalid namespace %q", s.Namespace)
		}
		for _, w := range s.Workloads {
			if !w.Kind.Valid() {
				return fmt.Errorf("invalid workload kind %q", w.Kind)
			}
			if !ValidDNS1123Subdomain(w.Name) {
				return fmt.Errorf("invalid workload name %q", w.Name)
			}
		}
	default:
		return fmt.Errorf("invalid scope kind %q", s.Kind)
	}
	return nil
}

// Same reports whether two scopes cover exactly the same targets: the same
// kind and fields, and for workloads the same set of workloads.
func (s Scope) Same(o Scope) bool {
	if s.Kind != o.Kind || s.ArgoProject != o.ArgoProject || s.Environment != o.Environment ||
		s.Cluster != o.Cluster || s.Namespace != o.Namespace {
		return false
	}
	set := func(ws []WorkloadRef) map[WorkloadRef]bool {
		m := make(map[WorkloadRef]bool, len(ws))
		for _, w := range ws {
			m[w] = true
		}
		return m
	}
	a, b := set(s.Workloads), set(o.Workloads)
	if len(a) != len(b) {
		return false
	}
	for w := range a {
		if !b[w] {
			return false
		}
	}
	return true
}

// HolderKind says whether a grant is held by a team or a user.
type HolderKind string

const (
	HolderTeam HolderKind = "team"
	HolderUser HolderKind = "user"
)

// Grant gives a holder a set of actions within a scope.
type Grant struct {
	ID         int64
	HolderKind HolderKind
	TeamID     int64 // set when HolderKind == HolderTeam
	UserID     int64 // set when HolderKind == HolderUser
	Scope      Scope
	Actions    []Action
	ExpiresAt  *time.Time
	Note       string
	CreatedBy  string
	CreatedAt  time.Time
	UpdatedAt  time.Time
	UpdatedBy  string
}

func (g Grant) Expired(now time.Time) bool {
	return g.ExpiresAt != nil && !now.Before(*g.ExpiresAt)
}

func (g Grant) HasAction(a Action) bool {
	for _, x := range g.Actions {
		if x == a {
			return true
		}
	}
	return false
}

// Validate checks holder, scope and actions.
func (g Grant) Validate() error {
	switch g.HolderKind {
	case HolderTeam:
		if g.TeamID == 0 || g.UserID != 0 {
			return fmt.Errorf("team grant requires teamId and no userId")
		}
	case HolderUser:
		if g.UserID == 0 || g.TeamID != 0 {
			return fmt.Errorf("user grant requires userId and no teamId")
		}
	default:
		return fmt.Errorf("invalid holder kind %q", g.HolderKind)
	}
	if err := g.Scope.Validate(); err != nil {
		return err
	}
	return ValidateScopeActions(g.Scope, g.Actions)
}

// User is a portal user.
type User struct {
	ID          int64
	KeycloakSub string // empty until first login
	// Local users log in with a portal password, never through Keycloak
	// (their hash is read only by the login).
	Local       bool
	Username    string
	DisplayName string
	Role        Role
	Disabled    bool
	LastLoginAt *time.Time
	CreatedAt   time.Time
	UpdatedAt   time.Time
	UpdatedBy   string
}

// Team groups users for grants and rules.
type Team struct {
	ID          int64
	Name        string
	Description string
	MemberIDs   []int64
	CreatedAt   time.Time
	UpdatedAt   time.Time
	UpdatedBy   string
}

// AdmissionMode is how a cluster enforces the restart/job/protected rules.
type AdmissionMode string

const (
	AdmissionVAP     AdmissionMode = "vap"
	AdmissionKyverno AdmissionMode = "kyverno"
	AdmissionNone    AdmissionMode = "none"
)

// Cluster is a registered Kubernetes cluster. Name matches Argo CD.
type Cluster struct {
	ID                int64
	Name              string
	Environment       string // cluster-level label, may be empty
	KubeconfigSecret  string
	KubernetesVersion string
	LastCheckAt       *time.Time
	Reachable         bool
	PrivilegeCheckOK  bool
	AdmissionMode     AdmissionMode
	Protected         bool // tiphys/protected on its Secret: hidden from access requests, grants still work
	CreatedAt         time.Time
	UpdatedAt         time.Time
	UpdatedBy         string
}

// EnvironmentOverrideKind selects what an environment override applies to.
type EnvironmentOverrideKind string

const (
	OverrideArgoProject EnvironmentOverrideKind = "argo_project"
	OverrideNamespace   EnvironmentOverrideKind = "namespace"
)

// EnvironmentOverride sets the environment of a namespace or Argo project.
type EnvironmentOverride struct {
	ID          int64
	Kind        EnvironmentOverrideKind
	Cluster     string // namespace overrides only
	ArgoProject string // argo_project overrides only
	Namespace   string // namespace overrides only
	Environment string
	CreatedAt   time.Time
}

// WindowEffect says whether a window opens or closes access.
type WindowEffect string

const (
	AllowInside WindowEffect = "allow_inside"
	DenyInside  WindowEffect = "deny_inside"
)

// WindowScopeKind selects what a window rule applies to.
type WindowScopeKind string

const (
	WindowGlobal      WindowScopeKind = "global"
	WindowEnvironment WindowScopeKind = "environment"
	WindowCluster     WindowScopeKind = "cluster"
	WindowArgoProject WindowScopeKind = "argo_project"
	WindowTeam        WindowScopeKind = "team"
)

// DefaultTimezone is used by rules and holidays that name none.
const DefaultTimezone = "Asia/Tehran"

// TimeWindow is a window rule. ScopeValue is the environment, cluster name,
// Argo project or team name (global has none).
type TimeWindow struct {
	ID         int64
	Name       string
	ScopeKind  WindowScopeKind
	ScopeValue string
	TeamID     int64 // resolved when ScopeKind == WindowTeam
	Actions    []Action
	Days       []time.Weekday // 0 = Sunday
	Start      TimeOfDay
	End        TimeOfDay
	Timezone   string
	Effect     WindowEffect
	Enabled    bool
	CreatedAt  time.Time
	UpdatedAt  time.Time
	UpdatedBy  string
}

// TimeOfDay is a wall-clock time as minutes after midnight.
type TimeOfDay int

func (t TimeOfDay) Hour() int   { return int(t) / 60 }
func (t TimeOfDay) Minute() int { return int(t) % 60 }
func (t TimeOfDay) String() string {
	return fmt.Sprintf("%02d:%02d", t.Hour(), t.Minute())
}

var timeOfDayRE = regexp.MustCompile(`^([01]?\d|2[0-3]):([0-5]\d)(:[0-5]\d)?$`)

// ParseTimeOfDay parses "HH:MM" (seconds ignored).
func ParseTimeOfDay(s string) (TimeOfDay, error) {
	m := timeOfDayRE.FindStringSubmatch(s)
	if m == nil {
		return 0, fmt.Errorf("invalid time %q, expected HH:MM", s)
	}
	var h, mi int
	fmt.Sscanf(m[1], "%d", &h)
	fmt.Sscanf(m[2], "%d", &mi)
	return TimeOfDay(h*60 + mi), nil
}

// Validate checks the rule's fields.
func (w TimeWindow) Validate() error {
	if w.Name == "" {
		return fmt.Errorf("name is required")
	}
	switch w.ScopeKind {
	case WindowGlobal:
		if w.ScopeValue != "" {
			return fmt.Errorf("global window must not have a scope value")
		}
	case WindowEnvironment, WindowCluster, WindowArgoProject, WindowTeam:
		if w.ScopeValue == "" {
			return fmt.Errorf("%s window requires a scope value", w.ScopeKind)
		}
	default:
		return fmt.Errorf("invalid scope kind %q", w.ScopeKind)
	}
	if len(w.Actions) == 0 {
		return fmt.Errorf("at least one action is required")
	}
	for _, a := range w.Actions {
		if !a.Valid() {
			return fmt.Errorf("invalid action %q", a)
		}
	}
	if len(w.Days) == 0 {
		return fmt.Errorf("at least one day of week is required")
	}
	for _, d := range w.Days {
		if d < time.Sunday || d > time.Saturday {
			return fmt.Errorf("invalid day of week %d", d)
		}
	}
	if w.Effect != AllowInside && w.Effect != DenyInside {
		return fmt.Errorf("invalid effect %q", w.Effect)
	}
	if w.Timezone != "" {
		if _, err := time.LoadLocation(w.Timezone); err != nil {
			return fmt.Errorf("invalid timezone %q", w.Timezone)
		}
	}
	return nil
}

// Holiday closes the listed environments (all when empty) for a calendar day.
type Holiday struct {
	ID           int64
	Date         time.Time // only the Y-M-D is meaningful
	Title        string
	Environments []string
	CreatedAt    time.Time
}

// BreakGlass is a user-initiated override of time windows and holidays.
type BreakGlass struct {
	ID        int64
	UserID    int64
	Username  string
	Reason    string
	StartedAt time.Time
	ExpiresAt time.Time
	EndedAt   *time.Time
	EndedBy   string
}

// ActiveAt reports whether the break-glass covers t.
func (b *BreakGlass) ActiveAt(t time.Time) bool {
	if b == nil {
		return false
	}
	if t.Before(b.StartedAt) || !t.Before(b.ExpiresAt) {
		return false
	}
	return b.EndedAt == nil || t.Before(*b.EndedAt)
}

// EffectiveEnd is when the break-glass stops applying.
func (b *BreakGlass) EffectiveEnd() time.Time {
	if b.EndedAt != nil && b.EndedAt.Before(b.ExpiresAt) {
		return *b.EndedAt
	}
	return b.ExpiresAt
}

// RequestStatus is the state of an access or break-glass request.
type RequestStatus string

const (
	RequestPending   RequestStatus = "pending"
	RequestApproved  RequestStatus = "approved"
	RequestRejected  RequestStatus = "rejected"
	RequestExpired   RequestStatus = "expired"
	RequestCancelled RequestStatus = "cancelled" // break-glass requests only: withdrawn by the requester
)

// BreakGlassRequest asks an admin for a break-glass (BreakGlassApproval mode).
// The break-glass starts when it is approved.
type BreakGlassRequest struct {
	ID              int64
	UserID          int64
	Username        string
	Reason          string
	Minutes         int // requested
	Status          RequestStatus
	DecidedBy       string
	DecidedAt       *time.Time
	DecisionNote    string
	ApprovedMinutes *int
	BreakGlassID    *int64
	CreatedAt       time.Time
}

// AccessRequest asks for a grant.
type AccessRequest struct {
	ID               int64
	UserID           int64
	Username         string
	Scope            Scope
	Actions          []Action
	DurationHours    *int // nil = permanent
	Reason           string
	Status           RequestStatus
	DecidedBy        string
	DecidedAt        *time.Time
	DecisionNote     string
	ResultingGrantID *int64
	CreatedAt        time.Time
}

// ExecSession is a recorded exec session.
type ExecSession struct {
	ID             string // UUID
	UserID         int64
	Username       string
	Cluster        string
	Namespace      string
	Pod            string
	Container      string
	Shell          string
	DebugContainer string // set for a debug session: the ephemeral container it added
	WorkloadKind   WorkloadKind
	WorkloadName   string
	Environment    string
	BreakGlassID   *int64
	StartedAt      time.Time
	EndedAt        *time.Time
	EndReason      string
	RecordingKey   string
	RecordingBytes int64
}

// AuditResult is the outcome recorded on an audit event.
type AuditResult string

const (
	ResultAllowed   AuditResult = "allowed"
	ResultDenied    AuditResult = "denied"
	ResultSucceeded AuditResult = "succeeded"
	ResultFailed    AuditResult = "failed"
)

// AuditEvent is one entry of the audit trail.
type AuditEvent struct {
	ID           int64
	At           time.Time
	UserID       *int64
	Username     string
	EventType    string
	Action       string
	Cluster      string
	Namespace    string
	WorkloadKind string
	WorkloadName string
	Pod          string
	Container    string
	Environment  string
	Result       AuditResult
	Reason       string
	BreakGlassID *int64
	SessionID    string
	Details      map[string]any
	SourceIP     string
	UserAgent    string
}

// Audit event types.
const (
	EventLogin            = "login"
	EventLogout           = "logout"
	EventDecisionDenied   = "decision.denied"
	EventLogsView         = "logs.view"
	EventLogsDownload     = "logs.download"
	EventExecStart        = "exec.start"
	EventExecEnd          = "exec.end"
	EventExecWatch        = "exec.watch"
	EventExecTerminate    = "exec.terminate"
	EventExecReplay       = "exec.replay"
	EventRestart          = "restart"
	EventRunJob           = "run-job"
	EventArgoSync         = "argo.sync"
	EventArgoDiff         = "argo.diff"
	EventArgoRollback     = "argo.rollback"
	EventBreakGlassStart  = "break_glass.start"
	EventBreakGlassEnd    = "break_glass.end"
	EventBreakGlassReq    = "break_glass.request"
	EventBreakGlassReject = "break_glass.reject"
	EventBreakGlassCancel = "break_glass.cancel"
	EventRequestCreate    = "access_request.create"
	EventRequestDecide    = "access_request.decide"
	EventAdminChange      = "admin.change"
	EventNotificationFail = "notification.failed"
	EventClusterRefused   = "kubeaccess.refused"
	EventSessionRevoked   = "session.revoked"
)

// Settings are the admin-editable settings.
type Settings struct {
	ExecMaxSessionMinutes  int
	ExecIdleTimeoutMinutes int
	BreakGlassMode         BreakGlassMode
	BreakGlassDefaultMin   int
	BreakGlassMaxMinutes   int
	// BreakGlassRequestMaxAgeMin is how long a break-glass request may stay
	// pending before it expires.
	BreakGlassRequestMaxAgeMin int
	RetentionAuditDays         int
	RetentionRecordingsDays    int
	Rocket                     RocketSettings
	// ArgoSyncClusterKinds are cluster-scoped kinds ("Kind.group") whose
	// resources do not leave syncing an Argo CD app to admins
	// (CheckSyncClusterKind says which may be listed).
	ArgoSyncClusterKinds []string
}

// BreakGlassMode says how break-glass is started.
type BreakGlassMode string

const (
	// BreakGlassOff: nobody can start one, and active ones stop overriding
	// time windows.
	BreakGlassOff BreakGlassMode = "off"
	// BreakGlassSelf: users start it themselves, without approval.
	BreakGlassSelf BreakGlassMode = "self"
	// BreakGlassApproval: users request it and an admin approves it.
	BreakGlassApproval BreakGlassMode = "approval"
)

// Valid reports whether m is a known mode.
func (m BreakGlassMode) Valid() bool {
	return m == BreakGlassOff || m == BreakGlassSelf || m == BreakGlassApproval
}

// Enabled reports whether break-glasses can exist and count in this mode.
func (m BreakGlassMode) Enabled() bool { return m == BreakGlassSelf || m == BreakGlassApproval }

// RocketSettings configures Rocket.Chat notifications.
type RocketSettings struct {
	WebhookURL string
	Channel    string
	Events     map[NotificationEvent]bool
}

// NotificationEvent names a notification that can be toggled.
type NotificationEvent string

const (
	NotifyBreakGlassStarted NotificationEvent = "break_glass_started"
	NotifyBreakGlassRequest NotificationEvent = "break_glass_requested"
	NotifyRequestCreated    NotificationEvent = "request_created"
	NotifyRequestDecided    NotificationEvent = "request_decided"
	NotifyExecInProd        NotificationEvent = "exec_in_prod"
	NotifyAdminChanges      NotificationEvent = "admin_changes"
)

var AllNotificationEvents = []NotificationEvent{
	NotifyBreakGlassStarted, NotifyBreakGlassRequest, NotifyRequestCreated, NotifyRequestDecided, NotifyExecInProd, NotifyAdminChanges,
}

// DefaultSettings are used until an admin changes them.
func DefaultSettings() Settings {
	return Settings{
		ExecMaxSessionMinutes:      120,
		ExecIdleTimeoutMinutes:     30,
		BreakGlassMode:             BreakGlassSelf,
		BreakGlassDefaultMin:       60,
		BreakGlassMaxMinutes:       240,
		BreakGlassRequestMaxAgeMin: 60,
		RetentionAuditDays:         365,
		RetentionRecordingsDays:    180,
		Rocket: RocketSettings{Events: map[NotificationEvent]bool{
			NotifyBreakGlassStarted: true,
			NotifyBreakGlassRequest: true,
			NotifyRequestCreated:    true,
			NotifyRequestDecided:    true,
			NotifyExecInProd:        true,
			NotifyAdminChanges:      true,
		}},
	}
}

var (
	dns1123Label     = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$`)
	dns1123Subdomain = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`)
)

// ValidDNS1123Label reports whether s is a valid namespace-style name.
func ValidDNS1123Label(s string) bool { return dns1123Label.MatchString(s) }

// ValidDNS1123Subdomain reports whether s is a valid object name.
func ValidDNS1123Subdomain(s string) bool { return len(s) <= 253 && dns1123Subdomain.MatchString(s) }

// DebugContainerPrefix starts the name of every debug container the portal
// adds; the chart's admission policy accepts no other name.
const DebugContainerPrefix = "tiphys-debug-"

// ExecShells is the allow-list of shells for exec.
var ExecShells = []string{"/bin/sh", "/bin/bash"}

func ValidShell(s string) bool {
	for _, v := range ExecShells {
		if v == s {
			return true
		}
	}
	return false
}
