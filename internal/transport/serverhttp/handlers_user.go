package serverhttp

import (
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"tiphys/internal/application"
	"tiphys/internal/domain"
)

// ---- auth ----

// Login starts the Keycloak code flow.
func (h *Handler) Login(c *gin.Context) {
	authURL, sealed, err := h.svc.Auth.BeginLogin(c.Query("next"))
	if err != nil {
		h.writeError(c, err)
		return
	}
	http.SetCookie(c.Writer, &http.Cookie{
		Name: loginCookie, Value: sealed, Path: "/auth", MaxAge: 600,
		HttpOnly: true, Secure: h.opts.CookieSecure, SameSite: http.SameSiteLaxMode,
	})
	c.Redirect(http.StatusFound, authURL)
}

// DevLoginPage replaces the login page when DEV_LOGIN_USER is set: it logs
// in right away, unless a failed login sent the browser here (no loop).
func (h *Handler) DevLoginPage(c *gin.Context) {
	if c.Query("error") != "" {
		h.staticHandler()(c)
		return
	}
	c.Redirect(http.StatusFound, "/auth/login?next="+url.QueryEscape(application.SafeNext(c.Query("next"))))
}

// Callback finishes the code flow and sets the session cookie.
func (h *Handler) Callback(c *gin.Context) {
	sealed, _ := c.Cookie(loginCookie)
	http.SetCookie(c.Writer, &http.Cookie{Name: loginCookie, Value: "", Path: "/auth", MaxAge: -1, HttpOnly: true, Secure: h.opts.CookieSecure, SameSite: http.SameSiteLaxMode})
	if e := c.Query("error"); e != "" {
		c.Redirect(http.StatusFound, "/login?error="+strings.ReplaceAll(e, " ", "_"))
		return
	}
	token, next, err := h.svc.Auth.CompleteLogin(c.Request.Context(), c.Query("code"), c.Query("state"), sealed, c.ClientIP(), c.Request.UserAgent())
	if err != nil {
		code := "login_failed"
		if err == application.ErrUserDisabled {
			code = "user_disabled"
		}
		c.Redirect(http.StatusFound, "/login?error="+code)
		return
	}
	h.setSessionCookie(c, token, 0)
	c.Redirect(http.StatusFound, next)
}

// AuthOptions tells the login page which sign-in methods exist (no login needed).
func (h *Handler) AuthOptions(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"localLogin": h.svc.Auth.LocalUsersEnabled()})
}

// LocalLogin logs a local user in with a password and sets the session
// cookie. Only the portal's own origin may post it (login CSRF), and attempts
// are limited per IP and per username.
func (h *Handler) LocalLogin(c *gin.Context) {
	if originOf(c.GetHeader("Origin")) != originOf(h.opts.PublicURL) {
		abort(c, http.StatusForbidden, "bad_origin", "Origin not allowed.", nil)
		return
	}
	var in struct {
		Username string `json:"username"`
		Password string `json:"password"`
		Next     string `json:"next"`
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 4<<10)
	if !bindJSON(h, c, &in) {
		return
	}
	in.Username = strings.TrimSpace(in.Username)
	now := time.Now()
	if !h.loginByIP.Allow(c.ClientIP(), now) || !h.loginByUser.Allow(strings.ToLower(in.Username), now) {
		c.Header("Retry-After", "60")
		abort(c, http.StatusTooManyRequests, "rate_limited", "Too many sign-in attempts, wait a minute.", nil)
		return
	}
	token, err := h.svc.Auth.LocalLogin(c.Request.Context(), in.Username, in.Password, c.ClientIP(), c.Request.UserAgent())
	if err != nil {
		h.writeError(c, err)
		return
	}
	h.setSessionCookie(c, token, 0)
	c.JSON(http.StatusOK, gin.H{"redirect": application.SafeNext(in.Next)})
}

// Logout ends the session.
func (h *Handler) Logout(c *gin.Context) {
	redirect := h.svc.Auth.Logout(c.Request.Context(), principal(c))
	h.setSessionCookie(c, "", -1)
	c.JSON(http.StatusOK, gin.H{"redirect": redirect})
}

// Me returns the current user.
func (h *Handler) Me(c *gin.Context) {
	p := principal(c)
	snap := h.svc.Policy.Current()
	now := time.Now()
	teams := []idName{}
	for _, id := range snap.TeamIDsByUser[p.User.ID] {
		teams = append(teams, idName{ID: id, Name: snap.Teams[id].Name})
	}
	sort.Slice(teams, func(i, j int) bool { return teams[i].Name < teams[j].Name })
	var bg *breakGlassDTO
	if b := h.svc.BreakGlass.Active(p.User.ID); b != nil && b.ActiveAt(now) {
		d := toBreakGlass(*b, now)
		bg = &d
	}
	st := h.svc.Settings.Get()
	argo := toArgoStatus(h.svc.Argo.Status())
	argo.AppCount, argo.ProjectCount, argo.LastError = 0, 0, nil
	c.JSON(http.StatusOK, meDTO{
		ID: p.User.ID, Username: p.User.Username, DisplayName: p.User.DisplayName, Role: p.User.Role, Teams: teams,
		CSRFToken: p.CSRFToken, BreakGlass: bg, Argo: argo,
		Settings: meSettingsDTO{BreakGlassEnabled: st.BreakGlassEnabled, BreakGlassDefaultMinutes: st.BreakGlassDefaultMin, BreakGlassMaxMinutes: st.BreakGlassMaxMinutes,
			ExecIdleTimeoutMinutes: st.ExecIdleTimeoutMinutes, ExecMaxSessionMinutes: st.ExecMaxSessionMinutes,
			ClusterRegistration: h.opts.ClusterRegistrationFromUI, DebugEnabled: h.opts.DebugEnabled, ArgoURL: h.opts.ArgoPublicURL,
			LocalUsers: h.svc.Auth.LocalUsersEnabled()},
	})
}

// ---- catalog and workloads ----

func kindFromPath(s string) (domain.WorkloadKind, bool) {
	switch s {
	case "deployments":
		return domain.KindDeployment, true
	case "statefulsets":
		return domain.KindStatefulSet, true
	case "daemonsets":
		return domain.KindDaemonSet, true
	case "rollouts":
		return domain.KindRollout, true
	case "cronjobs":
		return domain.KindCronJob, true
	}
	return "", false
}

// workloadRef reads {kind} and {name} from the path.
func (h *Handler) workloadRef(c *gin.Context) (domain.WorkloadRef, bool) {
	k, ok := kindFromPath(c.Param("kind"))
	if !ok {
		abort(c, http.StatusNotFound, "not_found", "Unknown workload kind.", nil)
		return domain.WorkloadRef{}, false
	}
	return domain.WorkloadRef{Kind: k, Name: c.Param("name")}, true
}

// userOnly rejects auditors on developer endpoints.
func (h *Handler) userOrAdmin() gin.HandlerFunc {
	return h.requireRoles(domain.RoleUser, domain.RoleAdmin)
}

func (h *Handler) Apps(c *gin.Context) {
	apps := h.svc.Workloads.Apps(principal(c))
	out := make([]appDTO, len(apps))
	for i, a := range apps {
		out[i] = toApp(a)
	}
	c.JSON(http.StatusOK, out)
}

// NamespaceApps lists the Argo CD applications deploying to a namespace.
func (h *Handler) NamespaceApps(c *gin.Context) {
	apps, err := h.svc.Workloads.NamespaceApps(c.Request.Context(), principal(c), c.Param("cluster"), c.Param("ns"))
	if err != nil {
		h.writeError(c, err)
		return
	}
	out := make([]namespaceAppDTO, len(apps))
	for i, a := range apps {
		out[i] = toNamespaceApp(a)
	}
	c.JSON(http.StatusOK, out)
}

func (h *Handler) SyncApp(c *gin.Context) {
	op, err := h.svc.Workloads.SyncApp(c.Request.Context(), principal(c), c.Param("cluster"), c.Param("ns"), c.Param("appNs"), c.Param("app"))
	if err != nil {
		h.writeError(c, err)
		return
	}
	c.JSON(http.StatusAccepted, gin.H{"operationId": op.OperationID})
}

func (h *Handler) AppStatus(c *gin.Context) {
	d, err := h.svc.Workloads.AppStatus(c.Request.Context(), principal(c), c.Param("cluster"), c.Param("ns"), c.Param("appNs"), c.Param("app"))
	if err != nil {
		h.writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, toArgoAppStatus(d))
}

func (h *Handler) AppHistory(c *gin.Context) {
	hs, err := h.svc.Workloads.AppHistory(c.Request.Context(), principal(c), c.Param("cluster"), c.Param("ns"), c.Param("appNs"), c.Param("app"))
	if err != nil {
		h.writeError(c, err)
		return
	}
	out := make([]argoHistoryDTO, len(hs))
	for i, x := range hs {
		out[i] = toArgoHistory(x)
	}
	c.JSON(http.StatusOK, out)
}

func (h *Handler) AppDiff(c *gin.Context) {
	d, err := h.svc.Workloads.AppDiff(c.Request.Context(), principal(c), c.Param("cluster"), c.Param("ns"), c.Param("appNs"), c.Param("app"))
	if err != nil {
		h.writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, toArgoDiff(d))
}

func (h *Handler) Namespaces(c *gin.Context) {
	ns := h.svc.Workloads.Namespaces(c.Request.Context(), principal(c))
	out := make([]namespaceDTO, len(ns))
	for i, n := range ns {
		out[i] = toNamespace(n)
	}
	c.JSON(http.StatusOK, out)
}

func (h *Handler) Workloads(c *gin.Context) {
	ws, err := h.svc.Workloads.Workloads(c.Request.Context(), principal(c), c.Param("cluster"), c.Param("ns"))
	if err != nil {
		h.writeError(c, err)
		return
	}
	out := make([]workloadDTO, len(ws))
	for i, w := range ws {
		out[i] = toWorkload(w)
	}
	c.JSON(http.StatusOK, out)
}

func (h *Handler) Workload(c *gin.Context) {
	ref, ok := h.workloadRef(c)
	if !ok {
		return
	}
	w, err := h.svc.Workloads.Workload(c.Request.Context(), principal(c), c.Param("cluster"), c.Param("ns"), ref)
	if err != nil {
		h.writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, toWorkloadDetail(w))
}

func (h *Handler) Pods(c *gin.Context) {
	ref, ok := h.workloadRef(c)
	if !ok {
		return
	}
	pods, err := h.svc.Workloads.Pods(c.Request.Context(), principal(c), c.Param("cluster"), c.Param("ns"), ref)
	if err != nil {
		h.writeError(c, err)
		return
	}
	out := make([]podDTO, len(pods))
	for i, p := range pods {
		out[i] = toPod(p)
	}
	c.JSON(http.StatusOK, out)
}

func (h *Handler) Events(c *gin.Context) {
	ref, ok := h.workloadRef(c)
	if !ok {
		return
	}
	evs, err := h.svc.Workloads.Events(c.Request.Context(), principal(c), c.Param("cluster"), c.Param("ns"), ref)
	if err != nil {
		h.writeError(c, err)
		return
	}
	out := make([]eventDTO, len(evs))
	for i, e := range evs {
		out[i] = toEvent(e)
	}
	c.JSON(http.StatusOK, out)
}

func (h *Handler) NamespaceEvents(c *gin.Context) {
	evs, err := h.svc.Workloads.NamespaceEvents(c.Request.Context(), principal(c), c.Param("cluster"), c.Param("ns"))
	if err != nil {
		h.writeError(c, err)
		return
	}
	out := make([]eventDTO, len(evs))
	for i, e := range evs {
		out[i] = toEvent(e)
	}
	c.JSON(http.StatusOK, out)
}

func (h *Handler) NamespaceTraffic(c *gin.Context) {
	g, err := h.svc.Workloads.NamespaceTraffic(c.Request.Context(), principal(c), c.Param("cluster"), c.Param("ns"))
	if err != nil {
		h.writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, toTraffic(g))
}

func (h *Handler) CronJobs(c *gin.Context) {
	cs, err := h.svc.Workloads.CronJobs(c.Request.Context(), principal(c), c.Param("cluster"), c.Param("ns"))
	if err != nil {
		h.writeError(c, err)
		return
	}
	out := make([]cronJobDTO, len(cs))
	for i, cj := range cs {
		out[i] = toCronJob(cj)
	}
	c.JSON(http.StatusOK, out)
}

func (h *Handler) Restart(c *gin.Context) {
	ref, ok := h.workloadRef(c)
	if !ok {
		return
	}
	op, err := h.svc.Workloads.Restart(c.Request.Context(), principal(c), c.Param("cluster"), c.Param("ns"), ref)
	if err != nil {
		h.writeError(c, err)
		return
	}
	c.JSON(http.StatusAccepted, gin.H{"operationId": op.OperationID})
}

func (h *Handler) RunCronJob(c *gin.Context) {
	op, err := h.svc.Workloads.RunCronJob(c.Request.Context(), principal(c), c.Param("cluster"), c.Param("ns"), c.Param("name"))
	if err != nil {
		h.writeError(c, err)
		return
	}
	c.JSON(http.StatusAccepted, gin.H{"operationId": op.OperationID, "jobName": op.JobName})
}

func queryBool(c *gin.Context, name string) bool {
	v, _ := strconv.ParseBool(c.Query(name))
	return v
}

func queryInt64(c *gin.Context, name string) int64 {
	v, _ := strconv.ParseInt(c.Query(name), 10, 64)
	return v
}

// logRequest builds a LogRequest from the path and query.
func (h *Handler) logRequest(c *gin.Context, follow bool) (application.LogRequest, bool) {
	ref, ok := h.workloadRef(c)
	if !ok {
		return application.LogRequest{}, false
	}
	return application.LogRequest{
		Cluster: c.Param("cluster"), Namespace: c.Param("ns"), Workload: ref, Pod: c.Param("pod"),
		Container: c.Query("container"), Previous: queryBool(c, "previous"), Follow: follow,
		TailLines: queryInt64(c, "tailLines"), SinceSeconds: queryInt64(c, "sinceSeconds"), Timestamps: queryBool(c, "timestamps"),
	}, true
}

func (h *Handler) DownloadLogs(c *gin.Context) {
	req, ok := h.logRequest(c, false)
	if !ok {
		return
	}
	d, err := h.svc.Workloads.DownloadLogs(c.Request.Context(), principal(c), req)
	if err != nil {
		h.writeError(c, err)
		return
	}
	name := req.Pod
	if req.Container != "" {
		name += "-" + req.Container
	}
	c.Header("Content-Disposition", `attachment; filename="`+name+`.log"`)
	c.Header("X-Tiphys-Truncated", strconv.FormatBool(d.Truncated))
	c.Data(http.StatusOK, "text/plain; charset=utf-8", d.Data)
}

// OperationStream streams restart / job progress as Server-Sent Events.
func (h *Handler) OperationStream(c *gin.Context) {
	op, err := h.svc.Workloads.Operation(principal(c), c.Param("id"))
	if err != nil {
		h.writeError(c, err)
		return
	}
	replay, ch, cancel := op.Subscribe()
	defer cancel()
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("X-Accel-Buffering", "no")
	c.Status(http.StatusOK)
	flusher, _ := c.Writer.(http.Flusher)

	send := func(ev application.OpEvent) bool {
		var data any = ev.Data
		if j, ok := ev.Data.(application.JobInfo); ok {
			data = toJob(j)
		}
		c.SSEvent(ev.Name, data)
		if flusher != nil {
			flusher.Flush()
		}
		return ev.Name == "done"
	}
	for _, ev := range replay {
		if send(ev) {
			return
		}
	}
	for {
		select {
		case <-c.Request.Context().Done():
			return
		case ev, ok := <-ch:
			if !ok {
				return
			}
			if send(ev) {
				return
			}
		}
	}
}

// ---- catalog (pickers) ----

func (h *Handler) CatalogProjects(c *gin.Context) {
	out := h.svc.Argo.Projects()
	if out == nil {
		out = []string{}
	}
	c.JSON(http.StatusOK, out)
}

func (h *Handler) CatalogClusters(c *gin.Context) {
	snap := h.svc.Policy.Current()
	type row struct {
		Name        string `json:"name"`
		Environment string `json:"environment"`
	}
	out := []row{}
	for _, cl := range snap.Clusters {
		out = append(out, row{Name: cl.Name, Environment: cl.Environment})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	c.JSON(http.StatusOK, out)
}

// CatalogNamespaces lists a cluster's namespaces for pickers: read from the
// cluster itself plus the ones Argo CD knows, protected ones removed.
func (h *Handler) CatalogNamespaces(c *gin.Context) {
	c.JSON(http.StatusOK, h.svc.Workloads.CatalogNamespaces(c.Request.Context(), principal(c), c.Param("cluster")))
}

// CatalogWorkloads lists the workloads of a namespace for the grant editor.
func (h *Handler) CatalogWorkloads(c *gin.Context) {
	p := principal(c)
	cluster, ns := c.Param("cluster"), c.Param("ns")
	if !domain.ValidDNS1123Label(ns) {
		h.writeError(c, application.Invalid("namespace", "invalid namespace"))
		return
	}
	if h.svc.Access.IsProtected(ns) {
		h.writeError(c, &application.DeniedError{Decision: domain.Decision{Reason: domain.ReasonProtectedNamespace}})
		return
	}
	infos, err := h.svc.Kube.ListWorkloads(c.Request.Context(), p.KubeActor(nil), cluster, ns)
	if err != nil {
		h.writeError(c, &application.ClusterError{Cluster: cluster, Err: err})
		return
	}
	out := make([]workloadRefDTO, 0, len(infos))
	for _, w := range infos {
		out = append(out, workloadRefDTO{Kind: w.Ref.Kind, Name: w.Ref.Name})
	}
	c.JSON(http.StatusOK, out)
}

// ---- my access, requests, break-glass ----

type myGrantDTO struct {
	ID        int64     `json:"id"`
	Holder    holderDTO `json:"holder"`
	Scope     scopeDTO  `json:"scope"`
	Actions   []string  `json:"actions"`
	ExpiresAt *string   `json:"expiresAt"`
	Note      string    `json:"note"`
	Expired   bool      `json:"expired"`
}

func (h *Handler) MyAccess(c *gin.Context) {
	p := principal(c)
	snap := h.svc.Policy.Current()
	now := time.Now()
	sub, _ := snap.Subject(p.User.ID)
	grants := []myGrantDTO{}
	for _, g := range snap.Policy.UserGrants(sub) {
		holder := holderDTO{Kind: string(g.HolderKind), Name: p.User.Username}
		if g.HolderKind == domain.HolderTeam {
			holder.Name = snap.Teams[g.TeamID].Name
		}
		grants = append(grants, myGrantDTO{ID: g.ID, Holder: holder, Scope: toScope(g.Scope), Actions: actionStrings(g.Actions),
			ExpiresAt: timeStr(g.ExpiresAt), Note: g.Note, Expired: g.Expired(now)})
	}
	teams := []idName{}
	for _, id := range snap.TeamIDsByUser[p.User.ID] {
		teams = append(teams, idName{ID: id, Name: snap.Teams[id].Name})
	}
	reqs, err := h.svc.Requests.List(c.Request.Context(), p, "")
	if err != nil {
		h.writeError(c, err)
		return
	}
	rd := make([]accessRequestDTO, len(reqs))
	for i, r := range reqs {
		rd[i] = toRequest(r)
	}
	var bg *breakGlassDTO
	if b := h.svc.BreakGlass.Active(p.User.ID); b != nil && b.ActiveAt(now) {
		d := toBreakGlass(*b, now)
		bg = &d
	}
	c.JSON(http.StatusOK, gin.H{"grants": grants, "teams": teams, "breakGlass": bg, "requests": rd})
}

// accessGrantDTO is a grant as "Who has access" shows it to developers: no
// note or author, and a team's members are listed.
type accessGrantDTO struct {
	ID        int64           `json:"id"`
	Holder    accessHolderDTO `json:"holder"`
	Scope     scopeDTO        `json:"scope"`
	Actions   []string        `json:"actions"`
	ExpiresAt *string         `json:"expiresAt"`
}

type accessHolderDTO struct {
	holderDTO
	Members []string `json:"members"`
}

func (h *Handler) WhoHasAccess(c *gin.Context) {
	q := c.Request.URL.Query()
	t := domain.Target{Cluster: c.Param("cluster"), Namespace: c.Param("ns"),
		Workload: domain.WorkloadRef{Kind: domain.WorkloadKind(q.Get("kind")), Name: q.Get("name")}}
	grants, err := h.svc.Workloads.WhoHasAccess(c.Request.Context(), principal(c), t)
	if err != nil {
		h.writeError(c, err)
		return
	}
	snap := h.svc.Policy.Current()
	out := []accessGrantDTO{}
	for _, g := range grants {
		holder := accessHolderDTO{holderDTO: holderDTO{Kind: string(g.HolderKind)}, Members: []string{}}
		switch g.HolderKind {
		case domain.HolderTeam:
			team := snap.Teams[g.TeamID]
			holder.ID, holder.Name = team.ID, team.Name
			for _, id := range team.MemberIDs {
				if u, ok := snap.Users[id]; ok && !u.Disabled {
					name := u.DisplayName
					if name == "" {
						name = u.Username
					}
					holder.Members = append(holder.Members, name)
				}
			}
		case domain.HolderUser:
			holder.ID, holder.Name = g.UserID, snap.Users[g.UserID].Username
		}
		out = append(out, accessGrantDTO{ID: g.ID, Holder: holder, Scope: toScope(g.Scope), Actions: actionStrings(g.Actions),
			ExpiresAt: timeStr(g.ExpiresAt)})
	}
	c.JSON(http.StatusOK, out)
}

func (h *Handler) ListRequests(c *gin.Context) {
	reqs, err := h.svc.Requests.List(c.Request.Context(), principal(c), domain.RequestStatus(c.Query("status")))
	if err != nil {
		h.writeError(c, err)
		return
	}
	out := make([]accessRequestDTO, len(reqs))
	for i, r := range reqs {
		out[i] = toRequest(r)
	}
	c.JSON(http.StatusOK, out)
}

func (h *Handler) CreateRequest(c *gin.Context) {
	var in struct {
		Scope         scopeDTO `json:"scope"`
		Actions       []string `json:"actions"`
		DurationHours *int     `json:"durationHours"`
		Reason        string   `json:"reason"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		h.writeError(c, application.Invalid("body", "invalid JSON body"))
		return
	}
	r, err := h.svc.Requests.Create(c.Request.Context(), principal(c), application.CreateRequest{
		Scope: in.Scope.toDomain(), Actions: parseActions(in.Actions), DurationHours: in.DurationHours, Reason: in.Reason})
	if err != nil {
		h.writeError(c, err)
		return
	}
	c.JSON(http.StatusCreated, toRequest(r))
}

func pathID(c *gin.Context, name string) (int64, bool) {
	id, err := strconv.ParseInt(c.Param(name), 10, 64)
	if err != nil {
		abort(c, http.StatusNotFound, "not_found", "Not found.", nil)
		return 0, false
	}
	return id, true
}

func (h *Handler) ApproveRequest(c *gin.Context) {
	id, ok := pathID(c, "id")
	if !ok {
		return
	}
	var in struct {
		Note          string `json:"note"`
		DurationHours *int   `json:"durationHours"`
	}
	raw := map[string]any{}
	_ = c.ShouldBindJSON(&raw)
	var narrow []domain.Action
	if l, ok := raw["actions"].([]any); ok {
		for _, v := range l {
			if str, ok := v.(string); ok {
				narrow = append(narrow, domain.Action(str))
			}
		}
	}
	if v, ok := raw["note"].(string); ok {
		in.Note = v
	}
	_, override := raw["durationHours"]
	if f, ok := raw["durationHours"].(float64); ok {
		n := int(f)
		in.DurationHours = &n
	}
	r, err := h.svc.Requests.Approve(c.Request.Context(), principal(c), id, in.Note, override, in.DurationHours, narrow)
	if err != nil {
		h.writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, toRequest(r))
}

func (h *Handler) RejectRequest(c *gin.Context) {
	id, ok := pathID(c, "id")
	if !ok {
		return
	}
	var in struct {
		Note string `json:"note"`
	}
	_ = c.ShouldBindJSON(&in)
	r, err := h.svc.Requests.Reject(c.Request.Context(), principal(c), id, in.Note)
	if err != nil {
		h.writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, toRequest(r))
}

func (h *Handler) StartBreakGlass(c *gin.Context) {
	var in struct {
		Reason          string `json:"reason"`
		DurationMinutes int    `json:"durationMinutes"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		h.writeError(c, application.Invalid("body", "invalid JSON body"))
		return
	}
	bg, err := h.svc.BreakGlass.Start(c.Request.Context(), principal(c), in.Reason, in.DurationMinutes)
	if err != nil {
		h.writeError(c, err)
		return
	}
	c.JSON(http.StatusCreated, toBreakGlass(bg, time.Now()))
}

func (h *Handler) EndBreakGlass(c *gin.Context) {
	id, ok := pathID(c, "id")
	if !ok {
		return
	}
	if err := h.svc.BreakGlass.End(c.Request.Context(), principal(c), id); err != nil {
		h.writeError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *Handler) ListBreakGlass(c *gin.Context) {
	list, err := h.svc.BreakGlass.List(c.Request.Context(), principal(c), 100)
	if err != nil {
		h.writeError(c, err)
		return
	}
	now := time.Now()
	out := make([]breakGlassDTO, len(list))
	for i, b := range list {
		out[i] = toBreakGlass(b, now)
	}
	c.JSON(http.StatusOK, out)
}
