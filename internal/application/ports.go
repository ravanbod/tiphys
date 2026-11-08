package application

import (
	"context"
	"errors"
	"io"
	"time"

	"tiphys/internal/domain"
)

// Outbound ports: what the application needs from infra. Inbound ports (what
// the application offers to transport) live in services.go.

// Common errors returned by repositories and adapters.
var (
	ErrNotFound = errors.New("not found")
	ErrConflict = errors.New("conflict") // unique violation, last-admin rule, etc.
)

// ---------------------------------------------------------------------------
// Persistence
// ---------------------------------------------------------------------------

// Store gives access to the repositories, either directly (each call on its
// own connection) or inside one transaction.
type Store interface {
	Repos() Repositories
	// InTx runs fn with repositories bound to one transaction. It commits
	// when fn returns nil and rolls back otherwise.
	InTx(ctx context.Context, fn func(Repositories) error) error
	// Ping checks database connectivity.
	Ping(ctx context.Context) error
}

// Repositories groups the repository ports.
type Repositories struct {
	Users        UserRepository
	Teams        TeamRepository
	Grants       GrantRepository
	Windows      WindowRepository
	Holidays     HolidayRepository
	Clusters     ClusterRepository
	Requests     AccessRequestRepository
	BreakGlass   BreakGlassRepository
	BGRequests   BreakGlassRequestRepository
	ExecSessions ExecSessionRepository
	Audit        AuditRepository
	Settings     SettingsRepository
	WebSessions  WebSessionRepository
}

// AuditRetention deletes audit rows past retention. It is a separate port
// because the application's own database role cannot DELETE audit_events:
// the implementation connects with the retention job's role.
type AuditRetention interface {
	DeleteAuditBefore(ctx context.Context, before time.Time) (int64, error)
}

type UserRepository interface {
	Get(ctx context.Context, id int64) (domain.User, error)
	GetByUsername(ctx context.Context, username string) (domain.User, error)
	GetByKeycloakSub(ctx context.Context, sub string) (domain.User, error)
	List(ctx context.Context) ([]domain.User, error)
	Create(ctx context.Context, u domain.User) (domain.User, error)
	// Update writes role, disabled, display name, keycloak sub, username and last login.
	Update(ctx context.Context, u domain.User) error
	// CountActiveAdmins counts users with role admin and not disabled.
	CountActiveAdmins(ctx context.Context) (int, error)
	// SetPasswordHash makes the user local (only if not bound to Keycloak);
	// PasswordHash returns "" for a Keycloak user.
	SetPasswordHash(ctx context.Context, id int64, hash, updatedBy string) error
	PasswordHash(ctx context.Context, id int64) (string, error)
}

type TeamRepository interface {
	Get(ctx context.Context, id int64) (domain.Team, error)
	List(ctx context.Context) ([]domain.Team, error) // MemberIDs and ManagerIDs filled
	Create(ctx context.Context, t domain.Team) (domain.Team, error)
	Update(ctx context.Context, t domain.Team) error
	Delete(ctx context.Context, id int64) error
	SetMembers(ctx context.Context, teamID int64, userIDs []int64) error
	SetManagers(ctx context.Context, teamID int64, userIDs []int64) error
	// AddMember and RemoveMember change one membership and report whether
	// it changed.
	AddMember(ctx context.Context, teamID, userID int64) (bool, error)
	RemoveMember(ctx context.Context, teamID, userID int64) (bool, error)
	// TeamIDsOfUser returns the ids of the teams a user belongs to.
	TeamIDsOfUser(ctx context.Context, userID int64) ([]int64, error)
}

type GrantRepository interface {
	Get(ctx context.Context, id int64) (domain.Grant, error)
	List(ctx context.Context) ([]domain.Grant, error)
	Create(ctx context.Context, g domain.Grant) (domain.Grant, error)
	Update(ctx context.Context, g domain.Grant) error
	Delete(ctx context.Context, id int64) error
}

type WindowRepository interface {
	Get(ctx context.Context, id int64) (domain.TimeWindow, error)
	List(ctx context.Context) ([]domain.TimeWindow, error)
	Create(ctx context.Context, w domain.TimeWindow) (domain.TimeWindow, error)
	Update(ctx context.Context, w domain.TimeWindow) error
	Delete(ctx context.Context, id int64) error
}

type HolidayRepository interface {
	Get(ctx context.Context, id int64) (domain.Holiday, error)
	List(ctx context.Context) ([]domain.Holiday, error)
	Create(ctx context.Context, h domain.Holiday) (domain.Holiday, error)
	Update(ctx context.Context, h domain.Holiday) error
	Delete(ctx context.Context, id int64) error
}

// ClusterRepository also owns environment overrides and protected namespaces.
type ClusterRepository interface {
	Get(ctx context.Context, name string) (domain.Cluster, error)
	List(ctx context.Context) ([]domain.Cluster, error)
	Create(ctx context.Context, c domain.Cluster) (domain.Cluster, error)
	// Update writes environment and kubeconfig secret name (admin edits) and
	// the health fields.
	Update(ctx context.Context, c domain.Cluster) error
	// UpdateHealth writes only the monitor's fields (kubernetes_version,
	// last_check_at, reachable, privilege_check_ok, admission_mode), so a
	// concurrent admin edit of the environment is never overwritten.
	UpdateHealth(ctx context.Context, name string, h ClusterHealth) error
	Delete(ctx context.Context, name string) error

	ListEnvironmentOverrides(ctx context.Context) ([]domain.EnvironmentOverride, error)
	CreateEnvironmentOverride(ctx context.Context, o domain.EnvironmentOverride) (domain.EnvironmentOverride, error)
	DeleteEnvironmentOverride(ctx context.Context, id int64) error

	ListProtectedNamespaces(ctx context.Context) ([]string, error)
	AddProtectedNamespace(ctx context.Context, ns string) error
	RemoveProtectedNamespace(ctx context.Context, ns string) error
}

// ClusterHealth is what the cluster monitor stores.
type ClusterHealth struct {
	KubernetesVersion string
	LastCheckAt       time.Time
	Reachable         bool
	PrivilegeCheckOK  bool
	AdmissionMode     domain.AdmissionMode
}

type AccessRequestRepository interface {
	Get(ctx context.Context, id int64) (domain.AccessRequest, error)
	// List returns requests newest first. A zero userID means all users; an
	// empty status means all statuses.
	List(ctx context.Context, userID int64, status domain.RequestStatus) ([]domain.AccessRequest, error)
	// ListByTeam returns a team's requests (TeamID set), newest first.
	ListByTeam(ctx context.Context, teamID int64) ([]domain.AccessRequest, error)
	// ListBatch returns the requests of a batch (BatchID), oldest first;
	// ErrNotFound when there are none.
	ListBatch(ctx context.Context, batchID string) ([]domain.AccessRequest, error)
	Create(ctx context.Context, r domain.AccessRequest) (domain.AccessRequest, error)
	// Decide sets the status, decision fields and resulting grant id. It
	// fails with ErrConflict if the request is no longer pending.
	Decide(ctx context.Context, id int64, status domain.RequestStatus, decidedBy, note string, grantID *int64, at time.Time) error
	// ExpirePendingBefore marks pending requests created before t as expired.
	ExpirePendingBefore(ctx context.Context, t time.Time) (int64, error)
}

type BreakGlassRepository interface {
	Get(ctx context.Context, id int64) (domain.BreakGlass, error)
	Create(ctx context.Context, b domain.BreakGlass) (domain.BreakGlass, error)
	End(ctx context.Context, id int64, endedBy string, at time.Time) error
	// CurrentForUser returns the user's break-glass active or scheduled at t
	// (the earliest-starting one), or ErrNotFound.
	CurrentForUser(ctx context.Context, userID int64, t time.Time) (domain.BreakGlass, error)
	// ListCurrent returns break-glasses that have not expired or ended at t,
	// also those that start later (scheduled).
	ListCurrent(ctx context.Context, t time.Time) ([]domain.BreakGlass, error)
	// List returns recent break-glasses, newest first.
	List(ctx context.Context, limit int) ([]domain.BreakGlass, error)
}

type BreakGlassRequestRepository interface {
	Get(ctx context.Context, id int64) (domain.BreakGlassRequest, error)
	// Create fails with ErrConflict if the user already has a pending request.
	Create(ctx context.Context, r domain.BreakGlassRequest) (domain.BreakGlassRequest, error)
	// List returns requests newest first. A zero userID means all users; an
	// empty status means all statuses.
	List(ctx context.Context, userID int64, status domain.RequestStatus, limit int) ([]domain.BreakGlassRequest, error)
	// Decide sets the status and decision fields (approvedMinutes and
	// breakGlassID only when approving). It fails with ErrConflict if the
	// request is no longer pending.
	Decide(ctx context.Context, id int64, status domain.RequestStatus, decidedBy, note string, approvedMinutes *int, breakGlassID *int64, at time.Time) error
	// ExpirePendingBefore marks pending requests created (or, if later,
	// requested to start) before t as expired.
	ExpirePendingBefore(ctx context.Context, t time.Time) (int64, error)
}

// ExecSessionFilter selects exec sessions. Zero fields are ignored.
type ExecSessionFilter struct {
	UserID      int64
	Username    string
	Cluster     string
	Namespace   string
	Environment string
	ActiveOnly  bool
	EndedOnly   bool
	BreakGlass  bool
	From, To    time.Time
	Limit       int
	BeforeStart time.Time // pagination: sessions started before this
}

type ExecSessionRepository interface {
	Get(ctx context.Context, id string) (domain.ExecSession, error)
	Create(ctx context.Context, s domain.ExecSession) error
	// End records the end time, reason and final recording size.
	End(ctx context.Context, id string, endedAt time.Time, reason string, recordingBytes int64) error
	List(ctx context.Context, f ExecSessionFilter) ([]domain.ExecSession, error)
	// EndAbandoned marks sessions with no end time as ended with reason
	// "portal_restart"; called at startup.
	EndAbandoned(ctx context.Context, at time.Time) (int64, error)
}

// AuditFilter selects audit events. Zero fields are ignored.
type AuditFilter struct {
	From, To     time.Time
	Username     string
	UserID       int64
	EventType    string
	Action       string
	Result       domain.AuditResult
	Cluster      string
	Namespace    string
	Environment  string
	BreakGlassID int64
	// BreakGlass keeps only events that ran under some break-glass.
	BreakGlass bool
	SessionID  string
	// Entity and EntityID match an admin change's details.entity and
	// details.id (e.g. "team" and "12").
	Entity   string
	EntityID string
	Text     string // free-text match on username, pod, workload, reason
	BeforeID int64  // pagination: events with id < BeforeID
	Limit    int
}

// AuditRepository is append-only: the application's database role has only
// INSERT and SELECT on audit_events.
type AuditRepository interface {
	// Insert stores the event and returns it with ID and At filled.
	Insert(ctx context.Context, e domain.AuditEvent) (domain.AuditEvent, error)
	List(ctx context.Context, f AuditFilter) ([]domain.AuditEvent, error)
}

// SettingsRepository stores the typed settings (key/value JSONB rows).
type SettingsRepository interface {
	// Load returns settings, using defaults for keys never saved.
	Load(ctx context.Context) (domain.Settings, error)
	Save(ctx context.Context, s domain.Settings, updatedBy string) error
}

// WebSession is a server-side login session. Token values are stored
// encrypted by the implementation using a Sealer.
type WebSession struct {
	ID            string // sha256 hex of the cookie value
	UserID        int64
	CSRFToken     string
	AccessToken   string
	RefreshToken  string
	IDToken       string
	CreatedAt     time.Time
	LastRefreshAt time.Time
	ExpiresAt     time.Time // Keycloak session expiry as far as known
	SourceIP      string
	UserAgent     string
}

type WebSessionRepository interface {
	Create(ctx context.Context, s WebSession) error
	Get(ctx context.Context, id string) (WebSession, error)
	// UpdateTokens stores refreshed tokens and the refresh time.
	UpdateTokens(ctx context.Context, id string, accessToken, refreshToken, idToken string, refreshedAt, expiresAt time.Time) error
	Delete(ctx context.Context, id string) error
	DeleteByUser(ctx context.Context, userID int64) (int64, error)
	DeleteExpired(ctx context.Context, before time.Time) (int64, error)
	// ListRefreshDue returns sessions whose last refresh is older than
	// `before` and that have not expired, for the background re-check.
	ListRefreshDue(ctx context.Context, before time.Time) ([]WebSession, error)
}

// ---------------------------------------------------------------------------
// Identity
// ---------------------------------------------------------------------------

// OIDCTokens are the tokens Keycloak issued. They never reach the browser.
type OIDCTokens struct {
	AccessToken  string
	RefreshToken string
	IDToken      string
	Expiry       time.Time // access token expiry
	// SessionExpiry is the refresh token expiry, which follows the Keycloak
	// session; zero if unknown.
	SessionExpiry time.Time
}

// OIDCClaims is the identity the portal keeps from Keycloak.
type OIDCClaims struct {
	Subject           string
	PreferredUsername string
	Name              string
	Email             string
	// Groups are the Keycloak groups from the ID token (group membership
	// mapper); empty when the client does not map them.
	Groups []string
}

// OIDCProvider is the Keycloak authorization code flow with PKCE, as a
// confidential client.
type OIDCProvider interface {
	AuthCodeURL(state, nonce, codeChallenge string) string
	Exchange(ctx context.Context, code, codeVerifier, nonce string) (OIDCTokens, OIDCClaims, error)
	// Refresh uses the refresh token; any error means the Keycloak session
	// is over (or Keycloak unreachable) and the portal ends the session.
	Refresh(ctx context.Context, refreshToken string) (OIDCTokens, error)
	// LogoutURL returns the RP-initiated logout URL, or "" if unsupported.
	LogoutURL(idToken, postLogoutRedirect string) string
}

// Sealer encrypts and authenticates small values (login transaction cookie,
// stored tokens).
type Sealer interface {
	Seal(plain []byte) (string, error)
	Open(sealed string) ([]byte, error)
}

// ---------------------------------------------------------------------------
// Argo CD
// ---------------------------------------------------------------------------

// ArgoClient reads Applications and AppProjects, and syncs and rolls back
// applications.
type ArgoClient interface {
	// ListApplications returns every application with its destination
	// resolved to a cluster name and the namespaces of status.resources.
	ListApplications(ctx context.Context) ([]domain.ArgoApp, error)
	// ListProjects returns the names of all AppProjects.
	ListProjects(ctx context.Context) ([]string, error)
	// SyncApplication asks Argo CD to sync the app to its target revision,
	// always without prune, dry run or a chosen revision, naming the portal
	// user in the operation's info. It returns the operation state from
	// before the sync, so the caller can tell the new operation apart.
	// ErrSyncInProgress when an operation is already running.
	SyncApplication(ctx context.Context, appNamespace, name, triggeredBy string) (ArgoOperation, error)
	// GetOperation returns the app's current operation state.
	GetOperation(ctx context.Context, appNamespace, name string) (ArgoOperation, error)
	// GetAppDetails returns the app's conditions, last operation and deploy
	// history.
	GetAppDetails(ctx context.Context, appNamespace, name string) (ArgoAppDetails, error)
	// ManagedResources returns the app's resources with their live and
	// desired states (Argo CD's normalized live and predicted states when it
	// has them).
	ManagedResources(ctx context.Context, appNamespace, name string) ([]ArgoManagedResource, error)
	// RevisionMetadata returns the commit behind a Git revision of the app.
	RevisionMetadata(ctx context.Context, appNamespace, name, revision string) (ArgoRevision, error)
	// RollbackApplication asks Argo CD to roll the app back to the deploy
	// with this history ID, always without prune or dry run. Like
	// SyncApplication it returns the operation state from before. Argo CD's
	// rollback request carries no info, so the user is not named in Argo CD.
	// ErrSyncInProgress when an operation is already running,
	// ErrAutoSyncEnabled when the app syncs automatically.
	RollbackApplication(ctx context.Context, appNamespace, name string, id int64) (ArgoOperation, error)
	// Manifests returns the objects Argo CD generates for the app's current
	// source at this revision (hooks included), as written in the source: a
	// namespaced object without a namespace goes to the destination.
	Manifests(ctx context.Context, appNamespace, name, revision string) ([]ArgoResourceRef, error)
}

// ArgoResourceRef names a resource of an application. Namespace is empty
// for cluster-scoped resources (and, in Manifests, for objects that name
// none).
type ArgoResourceRef struct {
	Group, Kind, Namespace, Name string
}

// ArgoAppDetails is an application's status beyond health and sync.
type ArgoAppDetails struct {
	Health, HealthMessage string
	SyncStatus            string
	SyncRevision          string
	Conditions            []ArgoCondition
	LastOperation         *ArgoOperationResult // nil if none ran
	History               []ArgoDeploy         // oldest first, as Argo CD keeps it
	AutoSync              bool                 // spec.syncPolicy.automated is enabled
	MultiSource           bool                 // spec.sources
	Resources             []ArgoResourceRef    // status.resources
}

type ArgoCondition struct {
	Type    string // e.g. ComparisonError, SyncError, OrphanedResourceWarning
	Message string
	At      *time.Time
}

// ArgoOperationResult is the last operation with its per-resource results.
type ArgoOperationResult struct {
	Phase       string
	Message     string
	StartedAt   time.Time
	FinishedAt  *time.Time
	InitiatedBy string // username; empty when automated
	Automated   bool
	Revision    string
	Resources   []ArgoResourceResult
}

type ArgoResourceResult struct {
	Group, Kind, Namespace, Name string
	Status                       string // Synced, SyncFailed, Pruned, PruneSkipped
	Message                      string
	HookType, HookPhase          string // set for hooks
	SyncPhase                    string // PreSync, Sync, PostSync, ...
}

// ArgoDeploy is one entry of status.history.
type ArgoDeploy struct {
	ID          int64
	Revision    string // empty for multi-source apps
	Revisions   []string
	DeployedAt  time.Time
	InitiatedBy string
	Automated   bool
	Source      string // repo URL (and chart) of the deployed source
	// StartedAt is when the operation that deployed it started (the
	// operation's startedAt), nil when Argo CD did not record it.
	StartedAt *time.Time
	// SameSource is set when the deployed source equals the app's current
	// source but for targetRevision, so the manifests of the current source
	// at Revision are what a rollback to it deploys.
	SameSource bool
}

// ArgoRevision is the commit behind a revision.
type ArgoRevision struct {
	Author  string
	Date    *time.Time
	Message string
	Tags    []string
}

// ArgoManagedResource is a resource of an app with its states as JSON (nil
// when the resource does not exist on that side).
type ArgoManagedResource struct {
	Group, Kind, Namespace, Name string
	Live, Desired                []byte
	Modified                     bool
	Hook                         bool
}

// ArgoOperation is an application's status.operationState.
type ArgoOperation struct {
	Pending   bool   // an operation is requested but has not started
	Phase     string // Running, Terminating, Succeeded, Failed, Error; "" when none ran
	Message   string
	StartedAt time.Time
	// Resources of the sync result, by outcome.
	Synced, Failed, Total int
}

// ---------------------------------------------------------------------------
// Kubernetes (kubeaccess)
// ---------------------------------------------------------------------------

// KubeGroupKind is a kind of an API group ("" for the core group).
type KubeGroupKind struct{ Group, Kind string }

// KubeActor is who a Kubernetes call is made on behalf of; it is carried to
// the API server as impersonation extras.
type KubeActor struct {
	Username     string
	BreakGlassID *int64
}

// KubeWorkloadTarget names a workload; ClusterAccess verifies every name.
type KubeWorkloadTarget struct {
	Cluster   string
	Namespace string
	Workload  domain.WorkloadRef
}

type WorkloadInfo struct {
	Ref               domain.WorkloadRef
	Namespace         string
	Desired           int32
	Ready             int32
	Updated           int32
	Available         int32
	Strategy          string
	Images            []string
	CreatedAt         time.Time
	LastRolloutAt     *time.Time
	Suspended         bool   // CronJob only
	Schedule          string // CronJob only
	ConcurrencyPolicy string // CronJob only
}

type ContainerInfo struct {
	Name         string
	Image        string
	Ready        bool
	State        string // running, waiting, terminated
	StateReason  string
	RestartCount int32
	LastState    string // terminated, ""
	LastReason   string
	LastExitCode *int32
	LastFinished *time.Time
	LastStarted  *time.Time
	MemoryLimit  string
	Init         bool
}

type PodInfo struct {
	Name             string
	Phase            string
	ReadyCount       int
	TotalCount       int
	Restarts         int32
	Node             string
	CreatedAt        time.Time
	LastTermination  string // e.g. "OOMKilled"
	LastTerminatedAt *time.Time
	Containers       []ContainerInfo
}

type EventInfo struct {
	Type      string
	Reason    string
	Message   string
	Object    string // e.g. "Pod/payments-api-xyz"
	Count     int32
	FirstSeen time.Time
	LastSeen  time.Time
}

// TrafficGraph is how traffic from outside reaches a namespace's pods:
// load balancer addresses and Gateways → Ingresses and routes → Services →
// workloads.
type TrafficGraph struct {
	Nodes []TrafficNode
	Edges []TrafficEdge
}

// TrafficNode is one box of the graph; ID is "Kind/namespace/name".
type TrafficNode struct {
	ID        string
	Kind      string // Address, Gateway, Ingress, HTTPRoute, GRPCRoute, Service, a workload kind, ReplicaSet, Job or Pod
	Namespace string // empty for an Address
	Name      string
	Details   []string // e.g. hosts, class, ports, addresses
	Ready     int      // ready pods behind a Service or workload
	Total     int
	Problem   string // why traffic may not get through, "" when none was found
}

// TrafficEdge connects two nodes; Labels are the rules that route traffic
// along it (e.g. "shop.example.com/api", a listener name).
type TrafficEdge struct {
	From   string
	To     string
	Labels []string
}

type JobInfo struct {
	Name        string
	CronJob     string
	TriggeredBy string // tiphys/triggered-by annotation, if any
	Manual      bool
	Active      int32
	Succeeded   int32
	Failed      int32
	Completions int32
	StartedAt   *time.Time
	FinishedAt  *time.Time
	Status      string // running, succeeded, failed
	PodNames    []string
}

type CronJobInfo struct {
	WorkloadInfo
	LastScheduleAt *time.Time
	LastResult     string // succeeded, failed, running, ""
	ActiveJobs     int
	RecentJobs     []JobInfo
}

// LogOptions are the validated options for Logs.
type LogOptions struct {
	Container    string
	Previous     bool
	Follow       bool
	TailLines    int64 // 100, 1000 or 10000
	SinceSeconds int64 // 300, 3600 or 86400; 0 = none
	Timestamps   bool
	LimitBytes   int64 // 0 = unlimited
}

// ExecStream is an open exec session. Close ends it.
type ExecStream interface {
	// Stdin accepts terminal input.
	io.Writer
	// Stdout yields terminal output (TTY mode: stderr is merged).
	Output() io.Reader
	Resize(cols, rows uint16)
	// Done is closed when the remote process ends; Err is then valid.
	Done() <-chan struct{}
	Err() error
	Close() error
}

// RolloutStatus is a progress snapshot of a restart.
type RolloutStatus struct {
	Desired, Updated, Ready, Available int32
	Complete                           bool
	Failed                             bool
	Message                            string
}

// ChangeType says what a NamespaceChange is. The values are part of the API.
type ChangeType string

const (
	ChangePodAdded        ChangeType = "pod_added"
	ChangePodRemoved      ChangeType = "pod_removed"
	ChangePodUpdated      ChangeType = "pod_updated"   // phase, readiness or terminating
	ChangePodRestarted    ChangeType = "pod_restarted" // a container restarted
	ChangeWorkload        ChangeType = "workload"      // the workload changed; Status is set when it was read
	ChangeRolloutStarted  ChangeType = "rollout_started"
	ChangeRolloutComplete ChangeType = "rollout_complete"
	ChangeRolloutFailed   ChangeType = "rollout_failed"
	ChangeEvent           ChangeType = "event" // a warning, or a CronJob's Job starting or finishing
)

// NamespaceChange is one change in a watched namespace, attributed to the
// workload it belongs to.
type NamespaceChange struct {
	Workload  domain.WorkloadRef
	Type      ChangeType
	Pod       string
	Phase     string         // pod changes: the pod's phase ("Terminating" while deleted)
	Container string         // pod_restarted
	Reason    string         // pod_restarted: how the container last ended (OOMKilled, Error, ...)
	ExitCode  *int32         // pod_restarted
	Status    *RolloutStatus // workload and rollout changes
	Event     *EventInfo     // event
}

// ClusterProbe is the result of validating a kubeconfig or running the
// periodic check.
type ClusterProbe struct {
	Version       string
	PrivilegeOK   bool
	Violations    []string // checks that should have been denied (or the impersonate check that was refused)
	AdmissionMode domain.AdmissionMode
	// DebugReady is true when the chart's tiphys-debug-only policy is
	// installed; debug containers are refused on the cluster otherwise.
	DebugReady bool
}

// Sentinel errors from ClusterAccess.
var (
	ErrProtectedNamespace = errors.New("namespace is protected")
	ErrPodNotInWorkload   = errors.New("pod does not belong to the workload")
	ErrInvalidName        = errors.New("invalid name")
	ErrClusterUnknown     = errors.New("cluster not registered")
	ErrClusterUnusable    = errors.New("cluster is unreachable or over-privileged")
	ErrAlreadyRunning     = errors.New("a job of this cronjob is already running")
	ErrSyncInProgress     = errors.New("an operation on this application is already in progress")
	ErrAutoSyncEnabled    = errors.New("the application syncs automatically, so it cannot be rolled back")
	ErrDebugUnavailable   = errors.New("debug containers are not enabled on this cluster")
)

// DebugError is returned when the cluster refused or could not start a debug
// container (admission, Pod Security, image pull, timeout). Reason is the
// cluster's explanation and is shown to the user.
type DebugError struct{ Reason string }

func (e *DebugError) Error() string { return "debug container: " + e.Reason }

// KubeDebugSpec is the part of a debug container the caller chooses; the
// image and everything else are fixed by the ClusterAccess implementation.
type KubeDebugSpec struct {
	Name     string        // tiphys-debug-<suffix>
	Target   string        // regular container whose processes it shares
	Lifetime time.Duration // the container stops by itself after this
}

// ClusterAccess is the kubeaccess module as a port: the only way the rest of
// the portal touches Kubernetes. It exposes typed verbs only; every
// implementation independently checks protected namespaces, DNS-1123 names,
// allow-listed kinds/shells and pod owner chains before each call, and makes
// every call by impersonating tiphys:user with the actor as extras.
type ClusterAccess interface {
	// ListNamespaces returns the names of the cluster's namespaces, protected
	// ones removed.
	ListNamespaces(ctx context.Context, actor KubeActor, cluster string) ([]string, error)
	ListWorkloads(ctx context.Context, actor KubeActor, cluster, namespace string) ([]WorkloadInfo, error)
	GetWorkload(ctx context.Context, actor KubeActor, w KubeWorkloadTarget) (WorkloadInfo, error)
	ListPods(ctx context.Context, actor KubeActor, w KubeWorkloadTarget) ([]PodInfo, error)
	Events(ctx context.Context, actor KubeActor, w KubeWorkloadTarget) ([]EventInfo, error)
	// NamespaceEvents returns the newest events of the whole namespace.
	NamespaceEvents(ctx context.Context, actor KubeActor, cluster, namespace string) ([]EventInfo, error)
	// NamespaceTraffic builds the graph from Ingresses, Gateway API routes and
	// LoadBalancer Services to the pods behind them.
	NamespaceTraffic(ctx context.Context, actor KubeActor, cluster, namespace string) (TrafficGraph, error)
	// ListCronJobs lists the namespace's CronJobs with their recent Jobs.
	ListCronJobs(ctx context.Context, actor KubeActor, cluster, namespace string) ([]CronJobInfo, error)
	// Logs verifies pod ownership and opens the log stream.
	Logs(ctx context.Context, actor KubeActor, w KubeWorkloadTarget, pod string, opts LogOptions) (io.ReadCloser, error)
	Exec(ctx context.Context, actor KubeActor, w KubeWorkloadTarget, pod, container, shell string, cols, rows uint16) (ExecStream, error)
	// Debug adds a debug (ephemeral) container to the pod, waits until it
	// runs and opens a TTY shell in it. ErrDebugUnavailable when debug is not
	// set up for the portal or the cluster; *DebugError when the cluster
	// refused or could not start the container.
	Debug(ctx context.Context, actor KubeActor, w KubeWorkloadTarget, pod string, spec KubeDebugSpec, shell string, cols, rows uint16) (ExecStream, error)
	// Restart patches the workload (restartedAt annotation or spec.restartAt).
	Restart(ctx context.Context, actor KubeActor, w KubeWorkloadTarget) error
	// WatchRollout emits status until complete, failed, ctx done or the
	// deadline passes; the channel is closed at the end.
	WatchRollout(ctx context.Context, actor KubeActor, w KubeWorkloadTarget, deadline time.Duration) (<-chan RolloutStatus, error)
	// RunCronJob builds a Job from the CronJob's jobTemplate and creates it.
	// Returns ErrAlreadyRunning for concurrencyPolicy Forbid with an active job.
	RunCronJob(ctx context.Context, actor KubeActor, cluster, namespace, cronJob string) (jobName string, err error)
	// WatchJob emits job status until it finishes, ctx is done or the
	// timeout passes; the channel is closed at the end.
	WatchJob(ctx context.Context, actor KubeActor, cluster, namespace, job string, timeout time.Duration) (<-chan JobInfo, error)
	// WatchNamespace streams what changes in the namespace's workloads (pods,
	// rollouts, notable events) until ctx is done; the channel is closed at
	// the end. Changes that belong to no workload are dropped.
	WatchNamespace(ctx context.Context, actor KubeActor, cluster, namespace string) (<-chan NamespaceChange, error)
	// ResourceScopes returns, from the cluster's discovery, whether each
	// kind it serves is namespaced (true) or cluster-scoped (false). A kind
	// missing from the map is not served (its group may have failed
	// discovery).
	ResourceScopes(ctx context.Context, actor KubeActor, cluster string) (map[KubeGroupKind]bool, error)

	// --- cluster lifecycle (not tied to a user action) ---

	// ProbeKubeconfig validates a pasted kubeconfig without registering it:
	// connects, reads the version, and runs the privilege check.
	ProbeKubeconfig(ctx context.Context, kubeconfig []byte) (ClusterProbe, error)
	// Probe runs the health/version and privilege check for a registered cluster.
	Probe(ctx context.Context, cluster string) (ClusterProbe, error)
	// Version calls /version on a registered cluster (cheap health check).
	Version(ctx context.Context, cluster string) (string, error)
	// Registered lists cluster names with a credential loaded.
	Registered() []string
	// SetUsable marks a cluster as usable or not (privilege check result);
	// unusable clusters refuse every verb with ErrClusterUnusable.
	SetUsable(cluster string, usable bool)
}

// CredentialStore keeps cluster kubeconfigs as Secrets in the management
// cluster, labelled tiphys/cluster=<name>. The token is never readable by
// the UI.
type CredentialStore interface {
	Put(ctx context.Context, cluster string, kubeconfig []byte) error
	Get(ctx context.Context, cluster string) ([]byte, error)
	Delete(ctx context.Context, cluster string) error
	List(ctx context.Context) ([]string, error)
	// Environment returns the environment label the credential carries (the
	// Secret's tiphys/environment annotation); ok is false when it carries
	// none. ErrNotFound when there is no credential.
	Environment(ctx context.Context, cluster string) (environment string, ok bool, err error)
	// Protected reports whether the credential marks the cluster protected
	// (the Secret's tiphys/protected annotation is "true"): hidden from access
	// requests. ErrNotFound when there is no credential.
	Protected(ctx context.Context, cluster string) (bool, error)
	// Watch calls onChange(cluster) whenever a credential is added, changed
	// or removed, until ctx is done.
	Watch(ctx context.Context, onChange func(cluster string)) error
}

// ---------------------------------------------------------------------------
// Recording, audit sink, notifications
// ---------------------------------------------------------------------------

// RecordingMeta is stored in manifest.json at the end of a session.
type RecordingMeta struct {
	SessionID string    `json:"sessionId"`
	Segments  int       `json:"segments"`
	Bytes     int64     `json:"bytes"`
	StartedAt time.Time `json:"startedAt"`
	EndedAt   time.Time `json:"endedAt"`
	EndReason string    `json:"endReason"`
}

// RecordingStore keeps exec recordings (S3 API with Object Lock). Its
// credentials can put and read but not delete.
type RecordingStore interface {
	// Healthy checks that a recording can be started; exec is refused otherwise.
	Healthy(ctx context.Context) error
	// PutSegment writes sessions/<yyyy>/<mm>/<id>/<seq>.cast, locked until retainUntil.
	PutSegment(ctx context.Context, prefix string, seq int, data []byte, retainUntil time.Time) error
	PutManifest(ctx context.Context, prefix string, m RecordingMeta, retainUntil time.Time) error
	// Open returns the concatenation of all segments in order.
	Open(ctx context.Context, prefix string) (io.ReadCloser, error)
}

// AuditSink pushes audit events to the copy the portal cannot alter (Loki,
// label app=tiphys, one JSON line per event).
type AuditSink interface {
	Push(ctx context.Context, e domain.AuditEvent) error
}

// NotificationMessage is a Rocket.Chat message.
type NotificationMessage struct {
	Event  domain.NotificationEvent
	Title  string
	Fields []NotificationField
	Link   string
	// DirectTo is a username to send a direct message to as well, if possible.
	DirectTo string
}

type NotificationField struct {
	Name  string
	Value string
}

// Notifier posts to the Rocket.Chat incoming webhook.
type Notifier interface {
	Send(ctx context.Context, webhookURL, channel string, m NotificationMessage) error
}

// Clock lets tests control time.
type Clock interface{ Now() time.Time }

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now() }

// SystemClock is the real clock.
var SystemClock Clock = systemClock{}
