package serverhttp

import (
	"fmt"
	"time"

	"tiphys/internal/application"
	"tiphys/internal/domain"
)

// JSON shapes of docs/api.md.

func tstr(t time.Time) string { return t.UTC().Format(time.RFC3339) }

func timeStr(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := tstr(*t)
	return &s
}

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

type idName struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

type breakGlassDTO struct {
	ID        int64   `json:"id"`
	UserID    int64   `json:"userId"`
	Username  string  `json:"username"`
	Reason    string  `json:"reason"`
	StartedAt string  `json:"startedAt"`
	ExpiresAt string  `json:"expiresAt"`
	EndedAt   *string `json:"endedAt"`
	EndedBy   *string `json:"endedBy"`
	Active    bool    `json:"active"`
	// Scheduled is true while it has not started yet (and is not cancelled).
	Scheduled bool `json:"scheduled"`
}

func toBreakGlass(b domain.BreakGlass, now time.Time) breakGlassDTO {
	return breakGlassDTO{
		ID: b.ID, UserID: b.UserID, Username: b.Username, Reason: b.Reason, StartedAt: tstr(b.StartedAt),
		ExpiresAt: tstr(b.ExpiresAt), EndedAt: timeStr(b.EndedAt), EndedBy: strPtr(b.EndedBy), Active: b.ActiveAt(now),
		Scheduled: b.Current(now) && now.Before(b.StartedAt),
	}
}

type breakGlassRequestDTO struct {
	ID              int64   `json:"id"`
	UserID          int64   `json:"userId"`
	Username        string  `json:"username"`
	Reason          string  `json:"reason"`
	DurationMinutes int     `json:"durationMinutes"`
	StartsAt        *string `json:"startsAt"` // requested start; null: when approved
	Status          string  `json:"status"`
	DecidedBy       *string `json:"decidedBy"`
	DecidedAt       *string `json:"decidedAt"`
	DecisionNote    *string `json:"decisionNote"`
	ApprovedMinutes *int    `json:"approvedMinutes"`
	BreakGlassID    *int64  `json:"breakGlassId"`
	CreatedAt       string  `json:"createdAt"`
	// ExpiresAt is when a pending request expires undecided.
	ExpiresAt string `json:"expiresAt"`
}

func toBreakGlassRequest(r domain.BreakGlassRequest, maxAgeMin int) breakGlassRequestDTO {
	return breakGlassRequestDTO{ID: r.ID, UserID: r.UserID, Username: r.Username, Reason: r.Reason, DurationMinutes: r.Minutes, StartsAt: timeStr(r.StartsAt),
		Status: string(r.Status), DecidedBy: strPtr(r.DecidedBy), DecidedAt: timeStr(r.DecidedAt), DecisionNote: strPtr(r.DecisionNote),
		ApprovedMinutes: r.ApprovedMinutes, BreakGlassID: r.BreakGlassID, CreatedAt: tstr(r.CreatedAt),
		ExpiresAt: tstr(r.ExpiresAt(time.Duration(maxAgeMin) * time.Minute))}
}

type meDTO struct {
	ID          int64       `json:"id"`
	Username    string      `json:"username"`
	DisplayName string      `json:"displayName"`
	Role        domain.Role `json:"role"`
	Teams       []idName    `json:"teams"`
	// ManagedTeams are the teams whose members the user may change (My teams).
	ManagedTeams []idName       `json:"managedTeams"`
	CSRFToken    string         `json:"csrfToken"`
	BreakGlass   *breakGlassDTO `json:"breakGlass"`
	// BreakGlassRequest is the user's pending break-glass request.
	BreakGlassRequest *breakGlassRequestDTO `json:"breakGlassRequest"`
	Settings          meSettingsDTO         `json:"settings"`
	Argo              argoStatusDTO         `json:"argo"`
}

type meSettingsDTO struct {
	// BreakGlassMode is off (the UI hides break-glass), self or approval
	// (the UI requests it instead of starting it).
	BreakGlassMode           domain.BreakGlassMode `json:"breakGlassMode"`
	BreakGlassDefaultMinutes int                   `json:"breakGlassDefaultMinutes"`
	BreakGlassMaxMinutes     int                   `json:"breakGlassMaxMinutes"`
	ExecIdleTimeoutMinutes   int                   `json:"execIdleTimeoutMinutes"`
	ExecMaxSessionMinutes    int                   `json:"execMaxSessionMinutes"`
	// ClusterRegistration is true when clusters can be added from the UI.
	ClusterRegistration bool `json:"clusterRegistration"`
	// DebugEnabled is true when the portal offers debug containers (DEBUG_IMAGE).
	DebugEnabled bool `json:"debugEnabled"`
	// ArgoURL is Argo CD's address in the browser, for links to applications.
	ArgoURL string `json:"argoUrl"`
	// LocalUsers is true when admins may create users with a portal password
	// (LOCAL_USERS_ENABLED).
	LocalUsers bool `json:"localUsers"`
}

type argoStatusDTO struct {
	FetchedAt    *string `json:"fetchedAt"`
	AgeSeconds   *int64  `json:"ageSeconds"`
	Stale        bool    `json:"stale"`
	AppCount     int     `json:"appCount,omitempty"`
	ProjectCount int     `json:"projectCount,omitempty"`
	LastError    *string `json:"lastError,omitempty"`
}

func toArgoStatus(s application.ArgoStatus) argoStatusDTO {
	return argoStatusDTO{FetchedAt: timeStr(s.FetchedAt), AgeSeconds: s.AgeSeconds, Stale: s.Stale,
		AppCount: s.AppCount, ProjectCount: s.ProjectCount, LastError: strPtr(s.LastError)}
}

type appDTO struct {
	Name          string   `json:"name"`
	Project       string   `json:"project"`
	Cluster       string   `json:"cluster"`
	Namespace     string   `json:"namespace"`
	Environment   string   `json:"environment"`
	Health        string   `json:"health"`
	Sync          string   `json:"sync"`
	WorkloadCount *int     `json:"workloadCount"`
	Access        []string `json:"access"`
}

func actionStrings(a []domain.Action) []string {
	out := make([]string, len(a))
	for i, x := range a {
		out[i] = string(x)
	}
	return out
}

func toApp(v application.AppView) appDTO {
	health, sync := v.App.Health, v.App.Sync
	if health == "" {
		health = "Unknown"
	}
	if sync == "" {
		sync = "Unknown"
	}
	return appDTO{Name: v.App.Name, Project: v.App.Project, Cluster: v.App.Cluster, Namespace: v.App.Namespace,
		Environment: v.Environment, Health: health, Sync: sync, Access: actionStrings(v.Access)}
}

type namespaceAppDTO struct {
	Name          string  `json:"name"`
	Project       string  `json:"project"`
	AppNamespace  string  `json:"appNamespace"`
	Health        string  `json:"health"`
	Sync          string  `json:"sync"`
	LastSyncAt    *string `json:"lastSyncAt"`
	LastSyncPhase *string `json:"lastSyncPhase"`
	// Access holds the decision for syncing the whole app; SyncNamespace is
	// the namespace it is about (the one that denied, or the destination).
	Access        map[string]decisionInfo `json:"access"`
	SyncNamespace string                  `json:"syncNamespace"`
	// AdminOnlyResources are the cluster-scoped resources ("Kind.group/name") that
	// leave the sync to admins; empty unless that is why it is denied.
	AdminOnlyResources []string `json:"adminOnlyResources"`
	// AutoSync and MultiSource keep the app from being rolled back here.
	AutoSync    bool `json:"autoSync"`
	MultiSource bool `json:"multiSource"`
}

// clusterResourceNames are "Kind.group/name" (never null).
func clusterResourceNames(rs []domain.ClusterResource) []string {
	out := make([]string, len(rs))
	for i, r := range rs {
		out[i] = r.String()
	}
	return out
}

func toNamespaceApp(v application.NamespaceAppView) namespaceAppDTO {
	a := v.App
	health, sync := a.Health, a.Sync
	if health == "" {
		health = "Unknown"
	}
	if sync == "" {
		sync = "Unknown"
	}
	var at *string
	if !a.LastSyncAt.IsZero() {
		at = timeStr(&a.LastSyncAt)
	}
	return namespaceAppDTO{Name: a.Name, Project: a.Project, AppNamespace: a.AppNamespace, Health: health, Sync: sync,
		LastSyncAt: at, LastSyncPhase: strPtr(a.LastSyncPhase),
		Access:        map[string]decisionInfo{string(domain.ActionSync): toDecisionInfo(v.Sync), string(domain.ActionRollback): toDecisionInfo(v.Rollback)},
		SyncNamespace: v.SyncAt.Namespace, AdminOnlyResources: clusterResourceNames(v.AdminOnlyResources),
		AutoSync: a.AutoSync, MultiSource: a.MultiSource}
}

type namespaceDTO struct {
	Cluster      string   `json:"cluster"`
	Namespace    string   `json:"namespace"`
	Environment  string   `json:"environment"`
	Apps         []string `json:"apps"`
	ArgoProjects []string `json:"argoProjects"`
	Access       []string `json:"access"`
}

func toNamespace(v application.NamespaceView) namespaceDTO {
	apps, projects := v.Apps, v.ArgoProjects
	if apps == nil {
		apps = []string{}
	}
	if projects == nil {
		projects = []string{}
	}
	return namespaceDTO{Cluster: v.Cluster, Namespace: v.Namespace, Environment: v.Environment,
		Apps: apps, ArgoProjects: projects, Access: actionStrings(v.Access)}
}

type workloadDTO struct {
	Kind        domain.WorkloadKind     `json:"kind"`
	Name        string                  `json:"name"`
	Namespace   string                  `json:"namespace"`
	Cluster     string                  `json:"cluster"`
	Environment string                  `json:"environment"`
	Desired     int32                   `json:"desired"`
	Ready       int32                   `json:"ready"`
	Suspended   *bool                   `json:"suspended,omitempty"`
	Schedule    string                  `json:"schedule,omitempty"`
	Access      map[string]decisionInfo `json:"access"`
}

func toWorkload(v application.WorkloadView) workloadDTO {
	acc := make(map[string]decisionInfo, len(v.Access))
	for a, d := range v.Access {
		acc[string(a)] = toDecisionInfo(d)
	}
	w := workloadDTO{Kind: v.Info.Ref.Kind, Name: v.Info.Ref.Name, Namespace: v.Info.Namespace, Cluster: v.Cluster,
		Environment: v.Environment, Desired: v.Info.Desired, Ready: v.Info.Ready, Access: acc}
	if v.Info.Ref.Kind == domain.KindCronJob {
		s := v.Info.Suspended
		w.Suspended, w.Schedule = &s, v.Info.Schedule
	}
	return w
}

type workloadDetailDTO struct {
	workloadDTO
	ArgoApp       *string  `json:"argoApp"`
	Strategy      string   `json:"strategy"`
	Updated       int32    `json:"updated"`
	Available     int32    `json:"available"`
	Images        []string `json:"images"`
	CreatedAt     string   `json:"createdAt"`
	LastRolloutAt *string  `json:"lastRolloutAt"`
	CronJobCount  int      `json:"cronJobCount"`
}

func toWorkloadDetail(v application.WorkloadDetailView) workloadDetailDTO {
	images := v.Info.Images
	if images == nil {
		images = []string{}
	}
	return workloadDetailDTO{workloadDTO: toWorkload(v.WorkloadView), ArgoApp: strPtr(v.ArgoApp), Strategy: v.Info.Strategy,
		Updated: v.Info.Updated, Available: v.Info.Available, Images: images, CreatedAt: tstr(v.Info.CreatedAt),
		LastRolloutAt: timeStr(v.Info.LastRolloutAt), CronJobCount: v.CronJobCount}
}

type containerDTO struct {
	Name          string  `json:"name"`
	Image         string  `json:"image"`
	Ready         bool    `json:"ready"`
	State         string  `json:"state"`
	StateReason   string  `json:"stateReason"`
	RestartCount  int32   `json:"restartCount"`
	LastState     *string `json:"lastState"`
	LastReason    *string `json:"lastReason"`
	LastExitCode  *int32  `json:"lastExitCode"`
	LastStartedAt *string `json:"lastStartedAt"`
	LastFinished  *string `json:"lastFinishedAt"`
	MemoryLimit   *string `json:"memoryLimit"`
	Init          bool    `json:"init"`
}

type podDTO struct {
	Name             string         `json:"name"`
	Phase            string         `json:"phase"`
	Ready            string         `json:"ready"`
	Restarts         int32          `json:"restarts"`
	Node             string         `json:"node"`
	CreatedAt        string         `json:"createdAt"`
	LastTermination  *string        `json:"lastTermination"`
	LastTerminatedAt *string        `json:"lastTerminatedAt"`
	Containers       []containerDTO `json:"containers"`
}

func toPod(p application.PodInfo) podDTO {
	cs := make([]containerDTO, len(p.Containers))
	for i, c := range p.Containers {
		cs[i] = containerDTO{Name: c.Name, Image: c.Image, Ready: c.Ready, State: c.State, StateReason: c.StateReason,
			RestartCount: c.RestartCount, LastState: strPtr(c.LastState), LastReason: strPtr(c.LastReason), LastExitCode: c.LastExitCode,
			LastStartedAt: timeStr(c.LastStarted), LastFinished: timeStr(c.LastFinished), MemoryLimit: strPtr(c.MemoryLimit), Init: c.Init}
	}
	return podDTO{Name: p.Name, Phase: p.Phase, Ready: fmt.Sprintf("%d/%d", p.ReadyCount, p.TotalCount), Restarts: p.Restarts,
		Node: p.Node, CreatedAt: tstr(p.CreatedAt), LastTermination: strPtr(p.LastTermination),
		LastTerminatedAt: timeStr(p.LastTerminatedAt), Containers: cs}
}

type eventDTO struct {
	Type      string `json:"type"`
	Reason    string `json:"reason"`
	Message   string `json:"message"`
	Object    string `json:"object"`
	Count     int32  `json:"count"`
	FirstSeen string `json:"firstSeen"`
	LastSeen  string `json:"lastSeen"`
}

func toEvent(e application.EventInfo) eventDTO {
	return eventDTO{Type: e.Type, Reason: e.Reason, Message: e.Message, Object: e.Object, Count: e.Count,
		FirstSeen: tstr(e.FirstSeen), LastSeen: tstr(e.LastSeen)}
}

type trafficNodeDTO struct {
	ID        string   `json:"id"`
	Kind      string   `json:"kind"`
	Namespace string   `json:"namespace"`
	Name      string   `json:"name"`
	Details   []string `json:"details"`
	Ready     int      `json:"ready"`
	Total     int      `json:"total"`
	Problem   string   `json:"problem"`
}

type trafficEdgeDTO struct {
	From   string   `json:"from"`
	To     string   `json:"to"`
	Labels []string `json:"labels"`
}

type trafficDTO struct {
	Nodes []trafficNodeDTO `json:"nodes"`
	Edges []trafficEdgeDTO `json:"edges"`
}

// orEmpty keeps a nil list from being encoded as null.
func orEmpty(l []string) []string {
	if l == nil {
		return []string{}
	}
	return l
}

func toTraffic(g application.TrafficGraph) trafficDTO {
	out := trafficDTO{Nodes: make([]trafficNodeDTO, len(g.Nodes)), Edges: make([]trafficEdgeDTO, len(g.Edges))}
	for i, n := range g.Nodes {
		out.Nodes[i] = trafficNodeDTO{ID: n.ID, Kind: n.Kind, Namespace: n.Namespace, Name: n.Name,
			Details: orEmpty(n.Details), Ready: n.Ready, Total: n.Total, Problem: n.Problem}
	}
	for i, e := range g.Edges {
		out.Edges[i] = trafficEdgeDTO{From: e.From, To: e.To, Labels: orEmpty(e.Labels)}
	}
	return out
}

type jobDTO struct {
	Name        string   `json:"name"`
	CronJob     string   `json:"cronJob"`
	TriggeredBy *string  `json:"triggeredBy"`
	Manual      bool     `json:"manual"`
	Status      string   `json:"status"`
	Active      int32    `json:"active"`
	Succeeded   int32    `json:"succeeded"`
	Completions int32    `json:"completions"`
	StartedAt   *string  `json:"startedAt"`
	FinishedAt  *string  `json:"finishedAt"`
	PodNames    []string `json:"podNames"`
}

func toJob(j application.JobInfo) jobDTO {
	pods := j.PodNames
	if pods == nil {
		pods = []string{}
	}
	return jobDTO{Name: j.Name, CronJob: j.CronJob, TriggeredBy: strPtr(j.TriggeredBy), Manual: j.Manual, Status: j.Status,
		Active: j.Active, Succeeded: j.Succeeded, Completions: j.Completions, StartedAt: timeStr(j.StartedAt),
		FinishedAt: timeStr(j.FinishedAt), PodNames: pods}
}

type cronJobDTO struct {
	workloadDTO
	LastScheduleAt    *string  `json:"lastScheduleAt"`
	LastResult        *string  `json:"lastResult"`
	ActiveJobs        int      `json:"activeJobs"`
	ConcurrencyPolicy string   `json:"concurrencyPolicy"`
	RecentJobs        []jobDTO `json:"recentJobs"`
}

func toCronJob(v application.CronJobView) cronJobDTO {
	jobs := make([]jobDTO, len(v.CronJobInfo.RecentJobs))
	for i, j := range v.CronJobInfo.RecentJobs {
		jobs[i] = toJob(j)
	}
	return cronJobDTO{workloadDTO: toWorkload(v.WorkloadView), LastScheduleAt: timeStr(v.CronJobInfo.LastScheduleAt),
		LastResult: strPtr(v.CronJobInfo.LastResult), ActiveJobs: v.CronJobInfo.ActiveJobs,
		ConcurrencyPolicy: v.Info.ConcurrencyPolicy, RecentJobs: jobs}
}

// ---- scope / grants / requests ----

type workloadRefDTO struct {
	Kind domain.WorkloadKind `json:"kind"`
	Name string              `json:"name"`
}

type scopeDTO struct {
	Kind        domain.ScopeKind `json:"kind"`
	ArgoProject string           `json:"argoProject,omitempty"`
	Cluster     string           `json:"cluster,omitempty"`
	Namespace   string           `json:"namespace,omitempty"`
	Workloads   []workloadRefDTO `json:"workloads,omitempty"`
	Environment string           `json:"environment,omitempty"`
}

func toScope(s domain.Scope) scopeDTO {
	d := scopeDTO{Kind: s.Kind, ArgoProject: s.ArgoProject, Cluster: s.Cluster, Namespace: s.Namespace, Environment: s.Environment}
	for _, w := range s.Workloads {
		d.Workloads = append(d.Workloads, workloadRefDTO{Kind: w.Kind, Name: w.Name})
	}
	return d
}

func (s scopeDTO) toDomain() domain.Scope {
	d := domain.Scope{Kind: s.Kind, ArgoProject: s.ArgoProject, Cluster: s.Cluster, Namespace: s.Namespace, Environment: s.Environment}
	for _, w := range s.Workloads {
		d.Workloads = append(d.Workloads, domain.WorkloadRef{Kind: w.Kind, Name: w.Name})
	}
	return d
}

func parseActions(in []string) []domain.Action {
	out := make([]domain.Action, len(in))
	for i, a := range in {
		out[i] = domain.Action(a)
	}
	return out
}

type accessRequestDTO struct {
	ID               int64    `json:"id"`
	UserID           int64    `json:"userId"`
	Username         string   `json:"username"`
	Scope            scopeDTO `json:"scope"`
	Actions          []string `json:"actions"`
	DurationHours    *int     `json:"durationHours"`
	Reason           string   `json:"reason"`
	Status           string   `json:"status"`
	DecidedBy        *string  `json:"decidedBy"`
	DecidedAt        *string  `json:"decidedAt"`
	DecisionNote     *string  `json:"decisionNote"`
	ResultingGrantID *int64   `json:"resultingGrantId"`
	CreatedAt        string   `json:"createdAt"`
	// Team is set when a manager asked for their team; approval grants the team.
	Team *idName `json:"team"`
	// BatchID groups the requests filed together; an admin decides a batch as a whole.
	BatchID *string `json:"batchId"`
}

func toRequest(r domain.AccessRequest) accessRequestDTO {
	d := accessRequestDTO{ID: r.ID, UserID: r.UserID, Username: r.Username, Scope: toScope(r.Scope), Actions: actionStrings(r.Actions),
		DurationHours: r.DurationHours, Reason: r.Reason, Status: string(r.Status), DecidedBy: strPtr(r.DecidedBy),
		DecidedAt: timeStr(r.DecidedAt), DecisionNote: strPtr(r.DecisionNote), ResultingGrantID: r.ResultingGrantID, CreatedAt: tstr(r.CreatedAt),
		BatchID: strPtr(r.BatchID)}
	if r.TeamID != 0 {
		d.Team = &idName{ID: r.TeamID, Name: r.TeamName}
	}
	return d
}

func toRequests(rs []domain.AccessRequest) []accessRequestDTO {
	out := make([]accessRequestDTO, len(rs))
	for i, r := range rs {
		out[i] = toRequest(r)
	}
	return out
}

type holderDTO struct {
	Kind string `json:"kind"`
	ID   int64  `json:"id,omitempty"`
	Name string `json:"name"`
}

type grantDTO struct {
	ID          int64     `json:"id"`
	Holder      holderDTO `json:"holder"`
	Scope       scopeDTO  `json:"scope"`
	Actions     []string  `json:"actions"`
	ExpiresAt   *string   `json:"expiresAt"`
	Note        string    `json:"note"`
	Expired     bool      `json:"expired"`
	CreatedBy   string    `json:"createdBy"`
	CreatedAt   string    `json:"createdAt"`
	Environment *string   `json:"environment"`
}

type grantNamer struct {
	snap *application.PolicySnapshot
	now  time.Time
	env  func(cluster, ns string) string
}

func (n grantNamer) toGrant(g domain.Grant) grantDTO {
	h := holderDTO{Kind: string(g.HolderKind)}
	switch g.HolderKind {
	case domain.HolderTeam:
		h.ID, h.Name = g.TeamID, n.snap.Teams[g.TeamID].Name
	case domain.HolderUser:
		h.ID, h.Name = g.UserID, n.snap.Users[g.UserID].Username
	}
	var env *string
	if g.Scope.Namespace != "" {
		e := n.env(g.Scope.Cluster, g.Scope.Namespace)
		env = &e
	} else if g.Scope.Environment != "" {
		env = &g.Scope.Environment
	}
	return grantDTO{ID: g.ID, Holder: h, Scope: toScope(g.Scope), Actions: actionStrings(g.Actions), ExpiresAt: timeStr(g.ExpiresAt),
		Note: g.Note, Expired: g.Expired(n.now), CreatedBy: g.CreatedBy, CreatedAt: tstr(g.CreatedAt), Environment: env}
}

// ---- sessions / audit ----

type execSessionDTO struct {
	ID              string  `json:"id"`
	UserID          int64   `json:"userId"`
	Username        string  `json:"username"`
	Cluster         string  `json:"cluster"`
	Namespace       string  `json:"namespace"`
	WorkloadKind    *string `json:"workloadKind"`
	WorkloadName    *string `json:"workloadName"`
	Pod             string  `json:"pod"`
	Container       string  `json:"container"`
	Shell           string  `json:"shell"`
	DebugContainer  *string `json:"debugContainer"`
	Environment     string  `json:"environment"`
	BreakGlassID    *int64  `json:"breakGlassId"`
	StartedAt       string  `json:"startedAt"`
	EndedAt         *string `json:"endedAt"`
	DurationSeconds *int64  `json:"durationSeconds"`
	EndReason       *string `json:"endReason"`
	RecordingBytes  int64   `json:"recordingBytes"`
	Active          bool    `json:"active"`
}

func toExecSession(s domain.ExecSession, active bool) execSessionDTO {
	d := execSessionDTO{ID: s.ID, UserID: s.UserID, Username: s.Username, Cluster: s.Cluster, Namespace: s.Namespace,
		WorkloadKind: strPtr(string(s.WorkloadKind)), WorkloadName: strPtr(s.WorkloadName), Pod: s.Pod, Container: s.Container,
		Shell: s.Shell, DebugContainer: strPtr(s.DebugContainer), Environment: s.Environment, BreakGlassID: s.BreakGlassID, StartedAt: tstr(s.StartedAt),
		EndedAt: timeStr(s.EndedAt), EndReason: strPtr(s.EndReason), RecordingBytes: s.RecordingBytes, Active: active}
	if s.EndedAt != nil {
		n := int64(s.EndedAt.Sub(s.StartedAt).Seconds())
		d.DurationSeconds = &n
	}
	return d
}

type commandDTO struct {
	At            string  `json:"at"`
	OffsetSeconds float64 `json:"offsetSeconds"`
	Text          string  `json:"text"`
}

type auditEventDTO struct {
	ID           int64          `json:"id"`
	At           string         `json:"at"`
	UserID       *int64         `json:"userId"`
	Username     string         `json:"username"`
	EventType    string         `json:"eventType"`
	Action       *string        `json:"action"`
	Cluster      *string        `json:"cluster"`
	Namespace    *string        `json:"namespace"`
	WorkloadKind *string        `json:"workloadKind"`
	WorkloadName *string        `json:"workloadName"`
	Pod          *string        `json:"pod"`
	Container    *string        `json:"container"`
	Environment  *string        `json:"environment"`
	Result       string         `json:"result"`
	Reason       *string        `json:"reason"`
	BreakGlassID *int64         `json:"breakGlassId"`
	SessionID    *string        `json:"sessionId"`
	Details      map[string]any `json:"details"`
	SourceIP     *string        `json:"sourceIp"`
	UserAgent    *string        `json:"userAgent"`
}

func toAudit(e domain.AuditEvent) auditEventDTO {
	return auditEventDTO{ID: e.ID, At: tstr(e.At), UserID: e.UserID, Username: e.Username, EventType: e.EventType,
		Action: strPtr(e.Action), Cluster: strPtr(e.Cluster), Namespace: strPtr(e.Namespace), WorkloadKind: strPtr(e.WorkloadKind),
		WorkloadName: strPtr(e.WorkloadName), Pod: strPtr(e.Pod), Container: strPtr(e.Container), Environment: strPtr(e.Environment),
		Result: string(e.Result), Reason: strPtr(e.Reason), BreakGlassID: e.BreakGlassID, SessionID: strPtr(e.SessionID),
		Details: e.Details, SourceIP: strPtr(e.SourceIP), UserAgent: strPtr(e.UserAgent)}
}

// ---- admin ----

type userDTO struct {
	ID             int64    `json:"id"`
	Username       string   `json:"username"`
	DisplayName    string   `json:"displayName"`
	Role           string   `json:"role"`
	Disabled       bool     `json:"disabled"`
	KeycloakBound  bool     `json:"keycloakBound"`
	Local          bool     `json:"local"`
	LastLoginAt    *string  `json:"lastLoginAt"`
	Teams          []idName `json:"teams"`
	ActiveSessions int      `json:"activeSessions"`
}

type memberDTO struct {
	ID          int64  `json:"id"`
	Username    string `json:"username"`
	DisplayName string `json:"displayName"`
}

type teamDTO struct {
	ID          int64       `json:"id"`
	Name        string      `json:"name"`
	Description string      `json:"description"`
	Members     []memberDTO `json:"members"`
	// Managers may add and remove members themselves (they need not be members).
	Managers []memberDTO `json:"managers"`
}

type windowDTO struct {
	ID         int64    `json:"id"`
	Name       string   `json:"name"`
	ScopeKind  string   `json:"scopeKind"`
	ScopeValue *string  `json:"scopeValue"`
	Actions    []string `json:"actions"`
	Days       []int    `json:"daysOfWeek"`
	StartTime  string   `json:"startTime"`
	EndTime    string   `json:"endTime"`
	Timezone   string   `json:"timezone"`
	Effect     string   `json:"effect"`
	Enabled    bool     `json:"enabled"`
}

func toWindow(w domain.TimeWindow) windowDTO {
	days := make([]int, len(w.Days))
	for i, d := range w.Days {
		days[i] = int(d)
	}
	return windowDTO{ID: w.ID, Name: w.Name, ScopeKind: string(w.ScopeKind), ScopeValue: strPtr(w.ScopeValue), Actions: actionStrings(w.Actions),
		Days: days, StartTime: w.Start.String(), EndTime: w.End.String(), Timezone: w.Timezone, Effect: string(w.Effect), Enabled: w.Enabled}
}

func (w windowDTO) toDomain() (domain.TimeWindow, error) {
	start, err := domain.ParseTimeOfDay(w.StartTime)
	if err != nil {
		return domain.TimeWindow{}, application.Invalid("startTime", err.Error())
	}
	end, err := domain.ParseTimeOfDay(w.EndTime)
	if err != nil {
		return domain.TimeWindow{}, application.Invalid("endTime", err.Error())
	}
	days := make([]time.Weekday, len(w.Days))
	for i, d := range w.Days {
		days[i] = time.Weekday(d)
	}
	val := ""
	if w.ScopeValue != nil {
		val = *w.ScopeValue
	}
	return domain.TimeWindow{ID: w.ID, Name: w.Name, ScopeKind: domain.WindowScopeKind(w.ScopeKind), ScopeValue: val, Actions: parseActions(w.Actions),
		Days: days, Start: start, End: end, Timezone: w.Timezone, Effect: domain.WindowEffect(w.Effect), Enabled: w.Enabled}, nil
}

type holidayDTO struct {
	ID           int64    `json:"id"`
	Date         string   `json:"date"`
	Title        string   `json:"title"`
	Environments []string `json:"environments"`
}

func toHoliday(h domain.Holiday) holidayDTO {
	envs := h.Environments
	if envs == nil {
		envs = []string{}
	}
	return holidayDTO{ID: h.ID, Date: h.Date.Format("2006-01-02"), Title: h.Title, Environments: envs}
}

type clusterDTO struct {
	Name                string   `json:"name"`
	Environment         *string  `json:"environment"`
	Reachable           bool     `json:"reachable"`
	KubernetesVersion   *string  `json:"kubernetesVersion"`
	PrivilegeCheckOK    bool     `json:"privilegeCheckOk"`
	PrivilegeViolations []string `json:"privilegeViolations"`
	AdmissionMode       string   `json:"admissionMode"`
	LastCheckAt         *string  `json:"lastCheckAt"`
	Usable              bool     `json:"usable"`
	// CredentialLoaded is false when the cluster's annotated Secret is missing
	// or was rejected.
	CredentialLoaded bool `json:"credentialLoaded"`
	// EnvironmentFromSecret is true when the Secret's tiphys/environment
	// annotation sets the environment; it cannot be changed in the UI then.
	EnvironmentFromSecret bool `json:"environmentFromSecret"`
	// Protected comes from the Secret's tiphys/protected annotation: the
	// cluster is hidden from access requests.
	Protected bool `json:"protected"`
}

func toCluster(c domain.Cluster, rt application.ClusterRuntime) clusterDTO {
	v := rt.PrivilegeViolations
	if v == nil {
		v = []string{}
	}
	mode := string(c.AdmissionMode)
	if mode == "" {
		mode = string(domain.AdmissionNone)
	}
	return clusterDTO{Name: c.Name, Environment: strPtr(c.Environment), Reachable: c.Reachable, KubernetesVersion: strPtr(c.KubernetesVersion),
		PrivilegeCheckOK: c.PrivilegeCheckOK, PrivilegeViolations: v, AdmissionMode: mode, LastCheckAt: timeStr(c.LastCheckAt),
		Usable: c.Reachable && c.PrivilegeCheckOK && rt.CredentialLoaded, CredentialLoaded: rt.CredentialLoaded,
		EnvironmentFromSecret: rt.EnvironmentFromCredential, Protected: c.Protected}
}

type overrideDTO struct {
	ID          int64   `json:"id"`
	Kind        string  `json:"kind"`
	Cluster     *string `json:"cluster,omitempty"`
	ArgoProject *string `json:"argoProject,omitempty"`
	Namespace   *string `json:"namespace,omitempty"`
	Environment string  `json:"environment"`
}

func toOverride(o domain.EnvironmentOverride) overrideDTO {
	return overrideDTO{ID: o.ID, Kind: string(o.Kind), Cluster: strPtr(o.Cluster), ArgoProject: strPtr(o.ArgoProject),
		Namespace: strPtr(o.Namespace), Environment: o.Environment}
}

type settingsDTO struct {
	BreakGlassMode           domain.BreakGlassMode `json:"breakGlassMode"`
	ExecMaxSessionMinutes    int                   `json:"execMaxSessionMinutes"`
	ExecIdleTimeoutMinutes   int                   `json:"execIdleTimeoutMinutes"`
	BreakGlassDefaultMinutes int                   `json:"breakGlassDefaultMinutes"`
	BreakGlassMaxMinutes     int                   `json:"breakGlassMaxMinutes"`
	// BreakGlassRequestMaxAgeMinutes is how long a break-glass request may stay pending.
	BreakGlassRequestMaxAgeMinutes int       `json:"breakGlassRequestMaxAgeMinutes"`
	RetentionAuditDays             int       `json:"retentionAuditDays"`
	RetentionRecordingsDays        int       `json:"retentionRecordingsDays"`
	Rocket                         rocketDTO `json:"rocket"`
	LokiRetentionInfo              *string   `json:"lokiRetentionInfo"`
	// ArgoSyncClusterKinds is optional on PUT: left out (a client from before
	// the setting existed), the stored list is kept.
	ArgoSyncClusterKinds *[]string `json:"argoSyncClusterKinds"`
}

type rocketDTO struct {
	WebhookURL string          `json:"webhookUrl"`
	Channel    string          `json:"channel"`
	Events     map[string]bool `json:"events"`
}

func toSettings(s domain.Settings, maskWebhook bool) settingsDTO {
	ev := map[string]bool{}
	for _, e := range domain.AllNotificationEvents {
		ev[string(e)] = s.Rocket.Events[e]
	}
	kinds := orEmpty(s.ArgoSyncClusterKinds)
	url := s.Rocket.WebhookURL
	if maskWebhook && url != "" {
		url = maskURL(url)
	}
	return settingsDTO{ExecMaxSessionMinutes: s.ExecMaxSessionMinutes, ExecIdleTimeoutMinutes: s.ExecIdleTimeoutMinutes,
		BreakGlassMode: s.BreakGlassMode, BreakGlassDefaultMinutes: s.BreakGlassDefaultMin, BreakGlassMaxMinutes: s.BreakGlassMaxMinutes,
		BreakGlassRequestMaxAgeMinutes: s.BreakGlassRequestMaxAgeMin,
		RetentionAuditDays:             s.RetentionAuditDays, RetentionRecordingsDays: s.RetentionRecordingsDays,
		Rocket:               rocketDTO{WebhookURL: url, Channel: s.Rocket.Channel, Events: ev},
		ArgoSyncClusterKinds: &kinds}
}

// maskURL hides the secret token at the end of a webhook URL.
func maskURL(u string) string {
	if len(u) <= 24 {
		return "…"
	}
	return u[:len(u)-12] + "…" + u[len(u)-4:]
}

func (d settingsDTO) toDomain(current domain.Settings) domain.Settings {
	s := domain.Settings{
		ExecMaxSessionMinutes: d.ExecMaxSessionMinutes, ExecIdleTimeoutMinutes: d.ExecIdleTimeoutMinutes,
		BreakGlassMode: d.BreakGlassMode, BreakGlassDefaultMin: d.BreakGlassDefaultMinutes, BreakGlassMaxMinutes: d.BreakGlassMaxMinutes,
		BreakGlassRequestMaxAgeMin: d.BreakGlassRequestMaxAgeMinutes,
		RetentionAuditDays:         d.RetentionAuditDays, RetentionRecordingsDays: d.RetentionRecordingsDays,
		Rocket: domain.RocketSettings{WebhookURL: d.Rocket.WebhookURL, Channel: d.Rocket.Channel, Events: map[domain.NotificationEvent]bool{}},
	}
	// The masked URL echoed back by the UI means "unchanged".
	if d.Rocket.WebhookURL == maskURL(current.Rocket.WebhookURL) && current.Rocket.WebhookURL != "" {
		s.Rocket.WebhookURL = current.Rocket.WebhookURL
	}
	for _, e := range domain.AllNotificationEvents {
		s.Rocket.Events[e] = d.Rocket.Events[string(e)]
	}
	s.ArgoSyncClusterKinds = current.ArgoSyncClusterKinds
	if d.ArgoSyncClusterKinds != nil {
		s.ArgoSyncClusterKinds = *d.ArgoSyncClusterKinds
	}
	return s
}

type argoConditionDTO struct {
	Type    string  `json:"type"`
	Message string  `json:"message"`
	At      *string `json:"at"`
}

type argoResourceResultDTO struct {
	Group     string `json:"group"`
	Kind      string `json:"kind"`
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	Status    string `json:"status"`
	Message   string `json:"message"`
	HookType  string `json:"hookType"`
	HookPhase string `json:"hookPhase"`
	SyncPhase string `json:"syncPhase"`
}

type argoOperationDTO struct {
	Phase       string                  `json:"phase"`
	Message     string                  `json:"message"`
	StartedAt   string                  `json:"startedAt"`
	FinishedAt  *string                 `json:"finishedAt"`
	InitiatedBy string                  `json:"initiatedBy"`
	Automated   bool                    `json:"automated"`
	Revision    string                  `json:"revision"`
	Resources   []argoResourceResultDTO `json:"resources"`
}

type argoAppStatusDTO struct {
	Health        string             `json:"health"`
	HealthMessage string             `json:"healthMessage"`
	SyncStatus    string             `json:"syncStatus"`
	SyncRevision  string             `json:"syncRevision"`
	Conditions    []argoConditionDTO `json:"conditions"`
	LastOperation *argoOperationDTO  `json:"lastOperation"`
}

func toArgoAppStatus(d application.ArgoAppDetails) argoAppStatusDTO {
	out := argoAppStatusDTO{Health: d.Health, HealthMessage: d.HealthMessage, SyncStatus: d.SyncStatus, SyncRevision: d.SyncRevision,
		Conditions: make([]argoConditionDTO, len(d.Conditions))}
	for i, c := range d.Conditions {
		out.Conditions[i] = argoConditionDTO{Type: c.Type, Message: c.Message, At: timeStr(c.At)}
	}
	if op := d.LastOperation; op != nil {
		o := &argoOperationDTO{Phase: op.Phase, Message: op.Message, StartedAt: tstr(op.StartedAt), FinishedAt: timeStr(op.FinishedAt),
			InitiatedBy: op.InitiatedBy, Automated: op.Automated, Revision: op.Revision, Resources: make([]argoResourceResultDTO, len(op.Resources))}
		for i, r := range op.Resources {
			o.Resources[i] = argoResourceResultDTO{Group: r.Group, Kind: r.Kind, Namespace: r.Namespace, Name: r.Name, Status: r.Status,
				Message: r.Message, HookType: r.HookType, HookPhase: r.HookPhase, SyncPhase: r.SyncPhase}
		}
		out.LastOperation = o
	}
	return out
}

type argoCommitDTO struct {
	Author  string   `json:"author"`
	Date    *string  `json:"date"`
	Message string   `json:"message"`
	Tags    []string `json:"tags"`
}

type argoHistoryDTO struct {
	ID          int64          `json:"id"`
	Revision    string         `json:"revision"`
	Revisions   []string       `json:"revisions"`
	DeployedAt  string         `json:"deployedAt"`
	InitiatedBy string         `json:"initiatedBy"`
	Automated   bool           `json:"automated"`
	Source      string         `json:"source"`
	Current     bool           `json:"current"`
	Commit      *argoCommitDTO `json:"commit"`
	// PortalUser started the deploy through the portal, with PortalAction
	// (argo.sync or argo.rollback); null otherwise.
	PortalUser   *string `json:"portalUser"`
	PortalAction *string `json:"portalAction"`
	// RollbackBlocked: current, auto_sync, multi_source, source_changed or null.
	RollbackBlocked *string `json:"rollbackBlocked"`
}

func toArgoHistory(h application.ArgoHistoryEntry) argoHistoryDTO {
	out := argoHistoryDTO{ID: h.ID, Revision: h.Revision, Revisions: orEmpty(h.Revisions), DeployedAt: tstr(h.DeployedAt),
		InitiatedBy: h.InitiatedBy, Automated: h.Automated, Source: h.Source, Current: h.Current,
		PortalUser: strPtr(h.PortalUser), PortalAction: strPtr(h.PortalAction), RollbackBlocked: strPtr(h.RollbackBlocked)}
	if c := h.Commit; c != nil {
		out.Commit = &argoCommitDTO{Author: c.Author, Date: timeStr(c.Date), Message: c.Message, Tags: orEmpty(c.Tags)}
	}
	return out
}

type argoResourceRefDTO struct {
	Group     string `json:"group"`
	Kind      string `json:"kind"`
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
}

func toArgoResourceRefs(rs []application.ArgoResourceRef) []argoResourceRefDTO {
	out := make([]argoResourceRefDTO, len(rs))
	for i, r := range rs {
		out[i] = argoResourceRefDTO{Group: r.Group, Kind: r.Kind, Namespace: r.Namespace, Name: r.Name}
	}
	return out
}

type argoRollbackPlanDTO struct {
	ID       int64        `json:"id"`
	Revision string       `json:"revision"`
	Decision decisionInfo `json:"decision"`
	// DecisionNamespace is the namespace the decision is about.
	DecisionNamespace  string               `json:"decisionNamespace"`
	AdminOnlyResources []string             `json:"adminOnlyResources"`
	Added              []argoResourceRefDTO `json:"added"`
	Kept               []argoResourceRefDTO `json:"kept"`
	Truncated          bool                 `json:"truncated"`
}

func toArgoRollbackPlan(p application.ArgoRollbackPlan) argoRollbackPlanDTO {
	return argoRollbackPlanDTO{ID: p.Deploy.ID, Revision: p.Deploy.Revision, Decision: toDecisionInfo(p.Decision),
		DecisionNamespace: p.DecisionAt.Namespace, AdminOnlyResources: clusterResourceNames(p.AdminOnlyResources),
		Added: toArgoResourceRefs(p.Added), Kept: toArgoResourceRefs(p.Kept), Truncated: p.Truncated}
}

type argoResourceDiffDTO struct {
	Group     string `json:"group"`
	Kind      string `json:"kind"`
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	State     string `json:"state"`
	Diff      string `json:"diff"`
	Hidden    bool   `json:"hidden"`
	Truncated bool   `json:"truncated"`
}

type argoDiffDTO struct {
	Resources []argoResourceDiffDTO `json:"resources"`
	Truncated bool                  `json:"truncated"`
}

func toArgoDiff(d application.ArgoDiff) argoDiffDTO {
	out := argoDiffDTO{Resources: make([]argoResourceDiffDTO, len(d.Resources)), Truncated: d.Truncated}
	for i, r := range d.Resources {
		out.Resources[i] = argoResourceDiffDTO{Group: r.Group, Kind: r.Kind, Namespace: r.Namespace, Name: r.Name, State: r.State,
			Diff: r.Diff, Hidden: r.Hidden, Truncated: r.Truncated}
	}
	return out
}
