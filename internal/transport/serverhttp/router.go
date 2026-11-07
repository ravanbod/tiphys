package serverhttp

import (
	"net/http"

	ginzap "github.com/gin-contrib/zap"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"tiphys/internal/domain"
)

// NewRouter wires every route. Authorization lives in the application
// services; the router only enforces login, CSRF, rate limits and roles.
func NewRouter(h *Handler, logger *zap.Logger, ginMode string) *gin.Engine {
	gin.SetMode(ginMode)
	r := gin.New()
	r.Use(withRequestID(), accessLog(logger), ginzap.RecoveryWithZap(logger, true), securityHeaders(h.opts.PublicURL, h.opts.CookieSecure))

	r.GET("/healthz", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"status": "ok"}) })
	r.GET("/readyz", func(c *gin.Context) {
		if err := h.svc.Store.Ping(c.Request.Context()); err != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"status": "database unavailable"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "ready"})
	})

	if h.opts.DevLogin {
		r.GET("/login", h.DevLoginPage)
	}
	r.GET("/auth/login", h.Login)
	r.GET("/auth/callback", h.Callback)
	r.GET("/auth/options", h.AuthOptions)
	if h.svc.Auth.LocalUsersEnabled() {
		r.POST("/auth/local-login", h.LocalLogin)
	}
	r.POST("/auth/logout", h.authRequired(), csrfRequired(), h.Logout)

	api := r.Group("/api/v1", h.authRequired(), csrfRequired(), h.rateLimited())
	api.GET("/me", h.Me)

	user := h.worker()
	staff := h.requireRoles(domain.RoleAdmin, domain.RoleAuditor)
	admin := h.requireRoles(domain.RoleAdmin)

	// Workloads and catalog (users and auditors through grants; admins see
	// everything non-protected).
	api.GET("/apps", user, h.Apps)
	api.GET("/namespaces", user, h.Namespaces)
	api.GET("/my/access", user, h.MyAccess)
	ns := "/clusters/:cluster/namespaces/:ns"
	api.GET(ns+"/workloads", user, h.Workloads)
	api.GET(ns+"/workloads/:kind/:name", user, h.Workload)
	api.GET(ns+"/workloads/:kind/:name/pods", user, h.Pods)
	api.GET(ns+"/workloads/:kind/:name/events", user, h.Events)
	api.POST(ns+"/workloads/:kind/:name/restart", user, h.Restart)
	api.GET(ns+"/workloads/:kind/:name/pods/:pod/logs/download", user, h.DownloadLogs)
	api.GET(ns+"/access", user, h.WhoHasAccess)
	api.GET(ns+"/events", user, h.NamespaceEvents)
	api.GET(ns+"/traffic", user, h.NamespaceTraffic)
	api.GET(ns+"/apps", user, h.NamespaceApps)
	api.POST(ns+"/apps/:appNs/:app/sync", user, h.SyncApp)
	api.GET(ns+"/apps/:appNs/:app/status", user, h.AppStatus)
	api.GET(ns+"/apps/:appNs/:app/history", user, h.AppHistory)
	api.GET(ns+"/apps/:appNs/:app/diff", user, h.AppDiff)
	api.GET(ns+"/apps/:appNs/:app/history/:id/rollback", user, h.AppRollbackPlan)
	api.POST(ns+"/apps/:appNs/:app/history/:id/rollback", user, h.RollbackApp)
	api.GET(ns+"/cronjobs", user, h.CronJobs)
	api.POST(ns+"/cronjobs/:name/run", user, h.RunCronJob)
	api.GET("/operations/:id/stream", user, h.OperationStream)
	api.GET("/live", user, h.Live)
	api.GET("/catalog/argo-projects", user, h.CatalogProjects)
	api.GET("/catalog/clusters", user, h.CatalogClusters)
	api.GET("/catalog/clusters/:cluster/namespaces", user, h.CatalogNamespaces)
	api.GET("/catalog/clusters/:cluster/namespaces/:ns/workloads", user, h.CatalogWorkloads)

	// Streams: same session cookie, Origin and ?csrf= checked.
	api.GET(ns+"/workloads/:kind/:name/pods/:pod/logs/stream", user, h.wsGuard(), h.LogsStream)
	api.GET(ns+"/workloads/:kind/:name/pods/:pod/exec", user, h.wsGuard(), h.ExecStream)
	if h.opts.DebugEnabled {
		api.GET(ns+"/workloads/:kind/:name/pods/:pod/debug", user, h.wsGuard(), h.DebugStream)
	}
	api.GET("/sessions/:id/watch", staff, h.wsGuard(), h.WatchSession)

	// Access requests and break-glass.
	api.GET("/access-requests", h.ListRequests)
	api.POST("/access-requests", user, h.CreateRequest)
	api.POST("/access-requests/:id/approve", admin, h.ApproveRequest)
	api.POST("/access-requests/:id/reject", admin, h.RejectRequest)
	api.POST("/break-glass", user, h.StartBreakGlass)
	api.POST("/break-glass/:id/end", user, h.EndBreakGlass)
	api.GET("/break-glass", staff, h.ListBreakGlass)
	api.POST("/break-glass/requests", user, h.RequestBreakGlass)
	api.GET("/break-glass/requests", staff, h.ListBreakGlassRequests)
	api.POST("/break-glass/requests/:id/approve", admin, h.ApproveBreakGlassRequest)
	api.POST("/break-glass/requests/:id/reject", admin, h.RejectBreakGlassRequest)
	api.POST("/break-glass/requests/:id/cancel", user, h.CancelBreakGlassRequest)

	// Sessions and audit.
	api.GET("/sessions", staff, h.ListSessions)
	api.GET("/sessions/:id", staff, h.GetSession)
	api.GET("/sessions/:id/recording", staff, h.SessionRecording)
	api.POST("/sessions/:id/terminate", admin, h.TerminateSession)
	api.GET("/audit", staff, h.AuditList)
	api.GET("/audit/export.csv", staff, h.AuditExport)

	// Admin configuration; auditors may read.
	a := api.Group("/admin", h.adminOrReadOnlyAuditor())
	a.GET("/overview", h.AdminOverview)
	a.GET("/users", h.AdminUsers)
	a.POST("/users", h.AdminCreateUser)
	a.PATCH("/users/:id", h.AdminUpdateUser)
	a.PUT("/users/:id/password", h.AdminSetPassword)
	a.GET("/teams", h.AdminTeams)
	a.POST("/teams", h.AdminCreateTeam)
	a.PATCH("/teams/:id", h.AdminUpdateTeam)
	a.DELETE("/teams/:id", h.AdminDeleteTeam)
	a.PUT("/teams/:id/members", h.AdminSetMembers)
	a.GET("/grants", h.AdminGrants)
	a.GET("/grants/covering", h.AdminGrantsCovering)
	a.POST("/grants", h.AdminCreateGrant)
	a.PUT("/grants/:id", h.AdminUpdateGrant)
	a.DELETE("/grants/:id", h.AdminDeleteGrant)
	a.POST("/access-check", h.AdminAccessCheck)
	a.GET("/time-windows", h.AdminWindows)
	a.POST("/time-windows", h.AdminSaveWindow)
	a.PUT("/time-windows/:id", h.AdminSaveWindow)
	a.DELETE("/time-windows/:id", h.AdminDeleteWindow)
	a.GET("/holidays", h.AdminHolidays)
	a.POST("/holidays", h.AdminSaveHoliday)
	a.PUT("/holidays/:id", h.AdminSaveHoliday)
	a.DELETE("/holidays/:id", h.AdminDeleteHoliday)
	a.GET("/clusters", h.AdminClusters)
	// Clusters are registered by creating a Secret annotated `tiphys/cluster`
	// (the portal watches them). Pasting a kubeconfig in the UI is the same thing
	// done by the portal, and is on only with CLUSTER_REGISTRATION_FROM_UI.
	if h.opts.ClusterRegistrationFromUI {
		a.POST("/clusters", h.AdminAddCluster)
		a.POST("/clusters/:name/credential", h.AdminReplaceCredential)
	}
	a.PUT("/clusters/:name", h.AdminSetClusterEnv)
	a.POST("/clusters/:name/check", h.AdminCheckCluster)
	a.DELETE("/clusters/:name", h.AdminDeleteCluster)
	a.GET("/environment-overrides", h.AdminOverrides)
	a.POST("/environment-overrides", h.AdminAddOverride)
	a.DELETE("/environment-overrides/:id", h.AdminDeleteOverride)
	a.GET("/protected-namespaces", h.AdminProtected)
	a.POST("/protected-namespaces", h.AdminAddProtected)
	a.DELETE("/protected-namespaces/:namespace", h.AdminRemoveProtected)
	a.GET("/argo", h.AdminArgo)
	a.POST("/argo/refresh", h.AdminArgoRefresh)
	a.GET("/settings", h.AdminSettings)
	a.PUT("/settings", h.AdminUpdateSettings)
	a.POST("/settings/rocket/test", h.AdminRocketTest)

	r.NoRoute(h.staticHandler())
	return r
}
