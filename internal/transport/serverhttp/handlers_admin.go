package serverhttp

import (
	"encoding/csv"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"tiphys/internal/application"
	"tiphys/internal/domain"
)

func bindJSON(h *Handler, c *gin.Context, v any) bool {
	if err := c.ShouldBindJSON(v); err != nil {
		h.writeError(c, application.Invalid("body", "invalid JSON body"))
		return false
	}
	return true
}

func parseTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		if d, err2 := time.Parse("2006-01-02", s); err2 == nil {
			return d
		}
	}
	return t
}

// ---- sessions ----

func (h *Handler) ListSessions(c *gin.Context) {
	f := application.ExecSessionFilter{
		Username: c.Query("user"), Cluster: c.Query("cluster"), Namespace: c.Query("namespace"), Environment: c.Query("environment"),
		ActiveOnly: c.Query("status") == "active", EndedOnly: c.Query("status") == "ended", BreakGlass: queryBool(c, "breakGlass"),
		From: parseTime(c.Query("from")), To: parseTime(c.Query("to")), BeforeStart: parseTime(c.Query("before")),
	}
	f.Limit, _ = strconv.Atoi(c.Query("limit"))
	list, err := h.svc.Sessions.List(c.Request.Context(), principal(c), f)
	if err != nil {
		h.writeError(c, err)
		return
	}
	out := make([]execSessionDTO, len(list))
	for i, s := range list {
		out[i] = toExecSession(s, s.EndedAt == nil && h.svc.Exec.IsLive(s.ID))
	}
	c.JSON(http.StatusOK, out)
}

func (h *Handler) GetSession(c *gin.Context) {
	d, err := h.svc.Sessions.Get(c.Request.Context(), principal(c), c.Param("id"))
	if err != nil {
		h.writeError(c, err)
		return
	}
	cmds := make([]commandDTO, len(d.Commands))
	for i, e := range d.Commands {
		cmds[i] = commandDTO{At: tstr(e.At), OffsetSeconds: e.OffsetSeconds, Text: e.Text}
	}
	c.JSON(http.StatusOK, struct {
		execSessionDTO
		Commands []commandDTO `json:"commands"`
	}{toExecSession(d.Session, d.Active), cmds})
}

func (h *Handler) SessionRecording(c *gin.Context) {
	body, _, err := h.svc.Sessions.OpenRecording(c.Request.Context(), principal(c), c.Param("id"))
	if err != nil {
		h.writeError(c, err)
		return
	}
	defer body.Close()
	c.Header("Content-Type", "application/x-asciicast")
	c.Status(http.StatusOK)
	_, _ = io.Copy(c.Writer, body)
}

func (h *Handler) TerminateSession(c *gin.Context) {
	if err := h.svc.Exec.Terminate(c.Request.Context(), principal(c), c.Param("id")); err != nil {
		h.writeError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// ---- audit ----

func (h *Handler) auditFilter(c *gin.Context) application.AuditFilter {
	return application.AuditFilter{
		From: parseTime(c.Query("from")), To: parseTime(c.Query("to")), Username: c.Query("user"),
		EventType: c.Query("eventType"), Action: c.Query("action"), Result: domain.AuditResult(c.Query("result")),
		Cluster: c.Query("cluster"), Namespace: c.Query("namespace"), Environment: c.Query("environment"),
		BreakGlassID: queryInt64(c, "breakGlassId"), SessionID: c.Query("sessionId"), Text: c.Query("q"),
	}
}

func (h *Handler) AuditList(c *gin.Context) {
	f := h.auditFilter(c)
	f.Limit, _ = strconv.Atoi(c.Query("limit"))
	if f.Limit <= 0 || f.Limit > 200 {
		f.Limit = 50
	}
	f.BeforeID, _ = strconv.ParseInt(c.Query("cursor"), 10, 64)
	events, err := h.svc.Audit.Query(c.Request.Context(), f)
	if err != nil {
		h.writeError(c, err)
		return
	}
	out := make([]auditEventDTO, len(events))
	for i, e := range events {
		out[i] = toAudit(e)
	}
	var next *string
	if len(events) == f.Limit {
		s := strconv.FormatInt(events[len(events)-1].ID, 10)
		next = &s
	}
	c.JSON(http.StatusOK, gin.H{"items": out, "nextCursor": next})
}

func (h *Handler) AuditExport(c *gin.Context) {
	f := h.auditFilter(c)
	const maxRows = 10000
	c.Header("Content-Type", "text/csv; charset=utf-8")
	c.Header("Content-Disposition", `attachment; filename="tiphys-audit.csv"`)
	w := csv.NewWriter(c.Writer)
	_ = w.Write([]string{"id", "at", "username", "event_type", "action", "result", "reason", "cluster", "namespace", "workload", "pod",
		"container", "environment", "break_glass_id", "session_id", "source_ip"})
	written := 0
	for written < maxRows {
		f.Limit = 1000
		events, err := h.svc.Audit.Query(c.Request.Context(), f)
		if err != nil {
			h.logger.Error("audit export failed")
			break
		}
		for _, e := range events {
			bg := ""
			if e.BreakGlassID != nil {
				bg = strconv.FormatInt(*e.BreakGlassID, 10)
			}
			_ = w.Write([]string{strconv.FormatInt(e.ID, 10), tstr(e.At), e.Username, e.EventType, e.Action, string(e.Result), e.Reason,
				e.Cluster, e.Namespace, strings.Trim(e.WorkloadKind+"/"+e.WorkloadName, "/"), e.Pod, e.Container, e.Environment, bg, e.SessionID, e.SourceIP})
			written++
			if written >= maxRows {
				break
			}
		}
		if len(events) < f.Limit {
			break
		}
		f.BeforeID = events[len(events)-1].ID
	}
	w.Flush()
}

// ---- admin: users and teams ----

func (h *Handler) AdminUsers(c *gin.Context) {
	users, err := h.svc.Admin.Users(principal(c))
	if err != nil {
		h.writeError(c, err)
		return
	}
	snap := h.svc.Policy.Current()
	active := h.svc.Exec.ActiveByUser()
	out := make([]userDTO, len(users))
	for i, u := range users {
		teams := []idName{}
		for _, id := range u.TeamIDs {
			teams = append(teams, idName{ID: id, Name: snap.Teams[id].Name})
		}
		out[i] = userDTO{ID: u.User.ID, Username: u.User.Username, DisplayName: u.User.DisplayName, Role: string(u.User.Role),
			Disabled: u.User.Disabled, KeycloakBound: u.User.KeycloakSub != "", LastLoginAt: timeStr(u.User.LastLoginAt), Teams: teams, ActiveSessions: active[u.User.ID]}
	}
	c.JSON(http.StatusOK, out)
}

func (h *Handler) userView(u domain.User) userDTO {
	snap := h.svc.Policy.Current()
	teams := []idName{}
	for _, id := range snap.TeamIDsByUser[u.ID] {
		teams = append(teams, idName{ID: id, Name: snap.Teams[id].Name})
	}
	return userDTO{ID: u.ID, Username: u.Username, DisplayName: u.DisplayName, Role: string(u.Role), Disabled: u.Disabled,
		KeycloakBound: u.KeycloakSub != "", Local: u.Local, LastLoginAt: timeStr(u.LastLoginAt), Teams: teams}
}

func (h *Handler) AdminCreateUser(c *gin.Context) {
	var in struct {
		Username    string `json:"username"`
		DisplayName string `json:"displayName"`
		Role        string `json:"role"`
		// Password, when set, creates a local user.
		Password string `json:"password"`
	}
	if !bindJSON(h, c, &in) {
		return
	}
	u, err := h.svc.Admin.CreateUser(c.Request.Context(), principal(c), in.Username, in.DisplayName, domain.Role(in.Role), in.Password)
	if err != nil {
		h.writeError(c, err)
		return
	}
	c.JSON(http.StatusCreated, h.userView(u))
}

func (h *Handler) AdminUpdateUser(c *gin.Context) {
	id, ok := pathID(c, "id")
	if !ok {
		return
	}
	var in struct {
		Role        *string `json:"role"`
		Disabled    *bool   `json:"disabled"`
		DisplayName *string `json:"displayName"`
	}
	if !bindJSON(h, c, &in) {
		return
	}
	patch := application.UserPatch{Disabled: in.Disabled, DisplayName: in.DisplayName}
	if in.Role != nil {
		r := domain.Role(*in.Role)
		patch.Role = &r
	}
	u, err := h.svc.Admin.UpdateUser(c.Request.Context(), principal(c), id, patch)
	if err != nil {
		h.writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, h.userView(u))
}

func (h *Handler) AdminSetPassword(c *gin.Context) {
	id, ok := pathID(c, "id")
	if !ok {
		return
	}
	var in struct {
		Password string `json:"password"`
	}
	if !bindJSON(h, c, &in) {
		return
	}
	u, err := h.svc.Admin.SetPassword(c.Request.Context(), principal(c), id, in.Password)
	if err != nil {
		h.writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, h.userView(u))
}

func (h *Handler) teamView(t domain.Team) teamDTO {
	snap := h.svc.Policy.Current()
	members := []memberDTO{}
	for _, id := range t.MemberIDs {
		if u, ok := snap.Users[id]; ok {
			members = append(members, memberDTO{ID: u.ID, Username: u.Username, DisplayName: u.DisplayName})
		}
	}
	sort.Slice(members, func(i, j int) bool { return members[i].Username < members[j].Username })
	return teamDTO{ID: t.ID, Name: t.Name, Description: t.Description, Members: members}
}

func (h *Handler) AdminTeams(c *gin.Context) {
	teams, err := h.svc.Admin.Teams(principal(c))
	if err != nil {
		h.writeError(c, err)
		return
	}
	out := make([]teamDTO, len(teams))
	for i, t := range teams {
		out[i] = h.teamView(t)
	}
	c.JSON(http.StatusOK, out)
}

func (h *Handler) AdminCreateTeam(c *gin.Context) {
	var in struct{ Name, Description string }
	if !bindJSON(h, c, &in) {
		return
	}
	t, err := h.svc.Admin.CreateTeam(c.Request.Context(), principal(c), in.Name, in.Description)
	if err != nil {
		h.writeError(c, err)
		return
	}
	c.JSON(http.StatusCreated, h.teamView(t))
}

func (h *Handler) AdminUpdateTeam(c *gin.Context) {
	id, ok := pathID(c, "id")
	if !ok {
		return
	}
	var in struct {
		Name        *string `json:"name"`
		Description *string `json:"description"`
	}
	if !bindJSON(h, c, &in) {
		return
	}
	t, err := h.svc.Admin.UpdateTeam(c.Request.Context(), principal(c), id, in.Name, in.Description)
	if err != nil {
		h.writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, h.teamView(h.svc.Policy.Current().Teams[t.ID]))
}

func (h *Handler) AdminDeleteTeam(c *gin.Context) {
	id, ok := pathID(c, "id")
	if !ok {
		return
	}
	if err := h.svc.Admin.DeleteTeam(c.Request.Context(), principal(c), id); err != nil {
		h.writeError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *Handler) AdminSetMembers(c *gin.Context) {
	id, ok := pathID(c, "id")
	if !ok {
		return
	}
	var in struct {
		UserIDs []int64 `json:"userIds"`
	}
	if !bindJSON(h, c, &in) {
		return
	}
	if err := h.svc.Admin.SetTeamMembers(c.Request.Context(), principal(c), id, in.UserIDs); err != nil {
		h.writeError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// ---- admin: grants ----

type grantInput struct {
	Holder struct {
		Kind string `json:"kind"`
		ID   int64  `json:"id"`
	} `json:"holder"`
	Scope     scopeDTO `json:"scope"`
	Actions   []string `json:"actions"`
	ExpiresAt *string  `json:"expiresAt"`
	Note      string   `json:"note"`
}

func (in grantInput) toDomain() (domain.Grant, error) {
	g := domain.Grant{HolderKind: domain.HolderKind(in.Holder.Kind), Scope: in.Scope.toDomain(), Actions: parseActions(in.Actions), Note: in.Note}
	switch g.HolderKind {
	case domain.HolderTeam:
		g.TeamID = in.Holder.ID
	case domain.HolderUser:
		g.UserID = in.Holder.ID
	}
	if in.ExpiresAt != nil && *in.ExpiresAt != "" {
		t, err := time.Parse(time.RFC3339, *in.ExpiresAt)
		if err != nil {
			return g, application.Invalid("expiresAt", "expiresAt must be an RFC 3339 timestamp")
		}
		g.ExpiresAt = &t
	}
	return g, nil
}

func (h *Handler) grantNamer() grantNamer {
	return grantNamer{snap: h.svc.Policy.Current(), now: time.Now(), env: h.svc.Access.Environment}
}

func (h *Handler) AdminGrants(c *gin.Context) {
	grants, err := h.svc.Admin.Grants(c.Request.Context(), principal(c))
	if err != nil {
		h.writeError(c, err)
		return
	}
	n := h.grantNamer()
	q := c.Request.URL.Query()
	out := []grantDTO{}
	for _, g := range grants {
		if v := q.Get("holderKind"); v != "" && string(g.HolderKind) != v {
			continue
		}
		if v := q.Get("holderId"); v != "" {
			id, _ := strconv.ParseInt(v, 10, 64)
			if g.TeamID != id && g.UserID != id {
				continue
			}
		}
		if v := q.Get("scopeKind"); v != "" && string(g.Scope.Kind) != v {
			continue
		}
		if v := q.Get("cluster"); v != "" && g.Scope.Cluster != v {
			continue
		}
		if v := q.Get("argoProject"); v != "" && g.Scope.ArgoProject != v {
			continue
		}
		if v := q.Get("action"); v != "" && !g.HasAction(domain.Action(v)) {
			continue
		}
		expired := g.Expired(n.now)
		if queryBool(c, "expired") && !expired {
			continue
		}
		if queryBool(c, "expiring") && (g.ExpiresAt == nil || expired || g.ExpiresAt.After(n.now.Add(14*24*time.Hour))) {
			continue
		}
		out = append(out, n.toGrant(g))
	}
	c.JSON(http.StatusOK, out)
}

func (h *Handler) AdminGrantsCovering(c *gin.Context) {
	q := c.Request.URL.Query()
	t := domain.Target{Cluster: q.Get("cluster"), Namespace: q.Get("namespace"),
		Workload: domain.WorkloadRef{Kind: domain.WorkloadKind(q.Get("kind")), Name: q.Get("name")}}
	grants, err := h.svc.Admin.GrantsCovering(c.Request.Context(), principal(c), t)
	if err != nil {
		h.writeError(c, err)
		return
	}
	n := h.grantNamer()
	out := []grantDTO{}
	for _, g := range grants {
		out = append(out, n.toGrant(g))
	}
	c.JSON(http.StatusOK, out)
}

func (h *Handler) AdminCreateGrant(c *gin.Context) {
	var in grantInput
	if !bindJSON(h, c, &in) {
		return
	}
	g, err := in.toDomain()
	if err == nil {
		g, err = h.svc.Admin.CreateGrant(c.Request.Context(), principal(c), g)
	}
	if err != nil {
		h.writeError(c, err)
		return
	}
	c.JSON(http.StatusCreated, h.grantNamer().toGrant(g))
}

func (h *Handler) AdminUpdateGrant(c *gin.Context) {
	id, ok := pathID(c, "id")
	if !ok {
		return
	}
	var in grantInput
	if !bindJSON(h, c, &in) {
		return
	}
	g, err := in.toDomain()
	if err == nil {
		g.ID = id
		g, err = h.svc.Admin.UpdateGrant(c.Request.Context(), principal(c), g)
	}
	if err != nil {
		h.writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, h.grantNamer().toGrant(g))
}

func (h *Handler) AdminDeleteGrant(c *gin.Context) {
	id, ok := pathID(c, "id")
	if !ok {
		return
	}
	if err := h.svc.Admin.DeleteGrant(c.Request.Context(), principal(c), id); err != nil {
		h.writeError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *Handler) AdminAccessCheck(c *gin.Context) {
	var in struct {
		UserID    int64  `json:"userId"`
		Cluster   string `json:"cluster"`
		Namespace string `json:"namespace"`
		Workload  *struct {
			Kind domain.WorkloadKind `json:"kind"`
			Name string              `json:"name"`
		} `json:"workload"`
		Action string  `json:"action"`
		At     *string `json:"at"`
	}
	if !bindJSON(h, c, &in) {
		return
	}
	t := domain.Target{Cluster: in.Cluster, Namespace: in.Namespace}
	wl := ""
	if in.Workload != nil && in.Workload.Name != "" {
		t.Workload = domain.WorkloadRef{Kind: in.Workload.Kind, Name: in.Workload.Name}
		wl = t.Workload.String()
	}
	var at time.Time
	if in.At != nil {
		at = parseTime(*in.At)
	}
	res, err := h.svc.Admin.AccessCheck(principal(c), application.AccessCheckInput{UserID: in.UserID, Target: t, Action: domain.Action(in.Action), At: at})
	if err != nil {
		h.writeError(c, err)
		return
	}
	n := h.grantNamer()
	matched := []grantDTO{}
	for _, g := range res.MatchedGrants {
		matched = append(matched, n.toGrant(g))
	}
	teams := res.TeamNames
	if teams == nil {
		teams = []string{}
	}
	var wlp *string
	if wl != "" {
		wlp = &wl
	}
	c.JSON(http.StatusOK, struct {
		decisionInfo
		Cluster       string     `json:"cluster"`
		Namespace     string     `json:"namespace"`
		Workload      *string    `json:"workload"`
		Action        string     `json:"action"`
		EvaluatedAt   string     `json:"evaluatedAt"`
		UserRole      string     `json:"userRole"`
		Teams         []string   `json:"teams"`
		MatchedGrants []grantDTO `json:"matchedGrants"`
	}{toDecisionInfo(res.Decision), in.Cluster, in.Namespace, wlp, in.Action, tstr(res.EvaluatedAt), string(res.User.Role), teams, matched})
}

// ---- admin: time windows and holidays ----

func (h *Handler) AdminWindows(c *gin.Context) {
	ws, err := h.svc.Admin.Windows(c.Request.Context(), principal(c))
	if err != nil {
		h.writeError(c, err)
		return
	}
	out := make([]windowDTO, len(ws))
	for i, w := range ws {
		out[i] = toWindow(w)
	}
	c.JSON(http.StatusOK, out)
}

func (h *Handler) AdminSaveWindow(c *gin.Context) {
	var in windowDTO
	if !bindJSON(h, c, &in) {
		return
	}
	w, err := in.toDomain()
	if err != nil {
		h.writeError(c, err)
		return
	}
	status := http.StatusCreated
	if c.Request.Method == http.MethodPut {
		id, ok := pathID(c, "id")
		if !ok {
			return
		}
		w.ID, status = id, http.StatusOK
		w, err = h.svc.Admin.UpdateWindow(c.Request.Context(), principal(c), w)
	} else {
		w, err = h.svc.Admin.CreateWindow(c.Request.Context(), principal(c), w)
	}
	if err != nil {
		h.writeError(c, err)
		return
	}
	c.JSON(status, toWindow(w))
}

func (h *Handler) AdminDeleteWindow(c *gin.Context) {
	id, ok := pathID(c, "id")
	if !ok {
		return
	}
	if err := h.svc.Admin.DeleteWindow(c.Request.Context(), principal(c), id); err != nil {
		h.writeError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *Handler) AdminHolidays(c *gin.Context) {
	hs, err := h.svc.Admin.Holidays(c.Request.Context(), principal(c))
	if err != nil {
		h.writeError(c, err)
		return
	}
	out := make([]holidayDTO, len(hs))
	for i, x := range hs {
		out[i] = toHoliday(x)
	}
	c.JSON(http.StatusOK, out)
}

func (h *Handler) AdminSaveHoliday(c *gin.Context) {
	var in holidayDTO
	if !bindJSON(h, c, &in) {
		return
	}
	d, perr := time.Parse("2006-01-02", in.Date)
	if perr != nil {
		h.writeError(c, application.Invalid("date", "date must be YYYY-MM-DD"))
		return
	}
	hol := domain.Holiday{Date: d, Title: in.Title, Environments: in.Environments}
	var err error
	status := http.StatusCreated
	if c.Request.Method == http.MethodPut {
		id, ok := pathID(c, "id")
		if !ok {
			return
		}
		hol.ID, status = id, http.StatusOK
		hol, err = h.svc.Admin.UpdateHoliday(c.Request.Context(), principal(c), hol)
	} else {
		hol, err = h.svc.Admin.CreateHoliday(c.Request.Context(), principal(c), hol)
	}
	if err != nil {
		h.writeError(c, err)
		return
	}
	c.JSON(status, toHoliday(hol))
}

func (h *Handler) AdminDeleteHoliday(c *gin.Context) {
	id, ok := pathID(c, "id")
	if !ok {
		return
	}
	if err := h.svc.Admin.DeleteHoliday(c.Request.Context(), principal(c), id); err != nil {
		h.writeError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// ---- admin: clusters ----

func (h *Handler) clusterView(c domain.Cluster) clusterDTO {
	return toCluster(c, h.svc.Clusters.Runtime(c.Name))
}

func (h *Handler) AdminClusters(c *gin.Context) {
	cs, err := h.svc.Clusters.List(c.Request.Context(), principal(c))
	if err != nil {
		h.writeError(c, err)
		return
	}
	out := make([]clusterDTO, len(cs))
	for i, cl := range cs {
		out[i] = h.clusterView(cl)
	}
	c.JSON(http.StatusOK, out)
}

func (h *Handler) AdminAddCluster(c *gin.Context) {
	var in struct {
		Name        string `json:"name"`
		Environment string `json:"environment"`
		Kubeconfig  string `json:"kubeconfig"`
	}
	if !bindJSON(h, c, &in) {
		return
	}
	cl, err := h.svc.Clusters.Add(c.Request.Context(), principal(c), in.Name, in.Environment, []byte(in.Kubeconfig))
	if err != nil {
		h.writeError(c, err)
		return
	}
	c.JSON(http.StatusCreated, h.clusterView(cl))
}

func (h *Handler) AdminSetClusterEnv(c *gin.Context) {
	var in struct {
		Environment string `json:"environment"`
	}
	if !bindJSON(h, c, &in) {
		return
	}
	cl, err := h.svc.Clusters.SetEnvironment(c.Request.Context(), principal(c), c.Param("name"), in.Environment)
	if err != nil {
		h.writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, h.clusterView(cl))
}

func (h *Handler) AdminReplaceCredential(c *gin.Context) {
	var in struct {
		Kubeconfig string `json:"kubeconfig"`
	}
	if !bindJSON(h, c, &in) {
		return
	}
	cl, err := h.svc.Clusters.ReplaceCredential(c.Request.Context(), principal(c), c.Param("name"), []byte(in.Kubeconfig))
	if err != nil {
		h.writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, h.clusterView(cl))
}

func (h *Handler) AdminCheckCluster(c *gin.Context) {
	cl, err := h.svc.Clusters.CheckNow(c.Request.Context(), principal(c), c.Param("name"))
	if err != nil {
		h.writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, h.clusterView(cl))
}

func (h *Handler) AdminDeleteCluster(c *gin.Context) {
	if err := h.svc.Clusters.Delete(c.Request.Context(), principal(c), c.Param("name")); err != nil {
		h.writeError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *Handler) AdminOverrides(c *gin.Context) {
	os, err := h.svc.Clusters.Overrides(c.Request.Context(), principal(c))
	if err != nil {
		h.writeError(c, err)
		return
	}
	out := make([]overrideDTO, len(os))
	for i, o := range os {
		out[i] = toOverride(o)
	}
	c.JSON(http.StatusOK, out)
}

func (h *Handler) AdminAddOverride(c *gin.Context) {
	var in struct {
		Kind        string `json:"kind"`
		Cluster     string `json:"cluster"`
		ArgoProject string `json:"argoProject"`
		Namespace   string `json:"namespace"`
		Environment string `json:"environment"`
	}
	if !bindJSON(h, c, &in) {
		return
	}
	o, err := h.svc.Clusters.AddOverride(c.Request.Context(), principal(c), domain.EnvironmentOverride{
		Kind: domain.EnvironmentOverrideKind(in.Kind), Cluster: in.Cluster, ArgoProject: in.ArgoProject, Namespace: in.Namespace, Environment: in.Environment})
	if err != nil {
		h.writeError(c, err)
		return
	}
	c.JSON(http.StatusCreated, toOverride(o))
}

func (h *Handler) AdminDeleteOverride(c *gin.Context) {
	id, ok := pathID(c, "id")
	if !ok {
		return
	}
	if err := h.svc.Clusters.DeleteOverride(c.Request.Context(), principal(c), id); err != nil {
		h.writeError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *Handler) AdminProtected(c *gin.Context) {
	l, err := h.svc.Clusters.ProtectedNamespaces(c.Request.Context(), principal(c))
	if err != nil {
		h.writeError(c, err)
		return
	}
	if l == nil {
		l = []string{}
	}
	c.JSON(http.StatusOK, l)
}

func (h *Handler) AdminAddProtected(c *gin.Context) {
	var in struct {
		Namespace string `json:"namespace"`
	}
	if !bindJSON(h, c, &in) {
		return
	}
	if err := h.svc.Clusters.AddProtected(c.Request.Context(), principal(c), in.Namespace); err != nil {
		h.writeError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *Handler) AdminRemoveProtected(c *gin.Context) {
	if err := h.svc.Clusters.RemoveProtected(c.Request.Context(), principal(c), c.Param("namespace")); err != nil {
		h.writeError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *Handler) AdminArgo(c *gin.Context) {
	c.JSON(http.StatusOK, toArgoStatus(h.svc.Argo.Status()))
}

func (h *Handler) AdminArgoRefresh(c *gin.Context) {
	if err := h.svc.Argo.Refresh(c.Request.Context()); err != nil {
		h.writeError(c, &application.ClusterError{Cluster: "argocd", Err: err})
		return
	}
	c.JSON(http.StatusOK, toArgoStatus(h.svc.Argo.Status()))
}

// ---- admin: settings, overview ----

func (h *Handler) AdminSettings(c *gin.Context) {
	s, err := h.svc.Admin.Settings(principal(c))
	if err != nil {
		h.writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, toSettings(s, true))
}

func (h *Handler) AdminUpdateSettings(c *gin.Context) {
	var in settingsDTO
	if !bindJSON(h, c, &in) {
		return
	}
	cur := h.svc.Settings.Get()
	s, err := h.svc.Admin.UpdateSettings(c.Request.Context(), principal(c), in.toDomain(cur))
	if err != nil {
		h.writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, toSettings(s, true))
}

func (h *Handler) AdminRocketTest(c *gin.Context) {
	if err := h.svc.Notify.SendTest(c.Request.Context(), principal(c).User.Username); err != nil {
		var inv *application.InvalidError
		if asInvalid(err, &inv) {
			h.writeError(c, err)
			return
		}
		abort(c, http.StatusBadGateway, "notification_failed", "The test message could not be delivered: "+err.Error(), nil)
		return
	}
	c.Status(http.StatusNoContent)
}

func asInvalid(err error, target **application.InvalidError) bool {
	if e, ok := err.(*application.InvalidError); ok {
		*target = e
		return true
	}
	return false
}

func (h *Handler) AdminOverview(c *gin.Context) {
	ctx := c.Request.Context()
	counts := h.svc.Streams.Counts()
	pending, _ := h.svc.Requests.Pending(ctx)
	pendingBG, _ := h.svc.BreakGlass.PendingCount(ctx)
	bgs, _ := h.svc.BreakGlass.List(ctx, principal(c), 200)
	now := time.Now()
	activeBG := 0
	for _, b := range bgs {
		if b.ActiveAt(now) {
			activeBG++
		}
	}
	c.JSON(http.StatusOK, gin.H{
		"activeExecSessions": h.svc.Exec.ActiveCount(), "openLogStreams": counts[application.StreamLogs],
		"pendingRequests": pending, "pendingBreakGlassRequests": pendingBG, "activeBreakGlasses": activeBG, "clustersUnreachable": h.svc.Clusters.UnreachableCount(ctx),
	})
}
