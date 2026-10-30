package serverhttp

import (
	"go.uber.org/zap"

	"tiphys/internal/application"
)

// Services are the application services the HTTP layer calls.
type Services struct {
	Auth       *application.AuthService
	Access     *application.AccessService
	Workloads  *application.WorkloadService
	Exec       *application.ExecService
	Sessions   *application.SessionService
	Audit      *application.AuditService
	BreakGlass *application.BreakGlassService
	Requests   *application.AccessRequestService
	Admin      *application.AdminService
	Clusters   *application.ClusterService
	Argo       *application.ArgoService
	Settings   *application.SettingsService
	Streams    *application.StreamManager
	Policy     *application.PolicyService
	Store      application.Store
	Kube       application.ClusterAccess
	Notify     *application.NotificationService
}

// Options configure the HTTP layer.
type Options struct {
	// PublicURL is the portal's external URL; its origin is the only one
	// accepted on WebSocket upgrades.
	PublicURL    string
	CookieSecure bool
	// RateLimitPerMinute is the per-user limit on state-changing requests.
	RateLimitPerMinute int
	// WebDir serves the frontend from disk instead of the embedded build.
	WebDir string
	// ClusterRegistrationFromUI enables the endpoints that add a cluster or
	// replace its credential from a pasted kubeconfig (and tells the UI).
	ClusterRegistrationFromUI bool
	// DevLogin (DEV_LOGIN_USER) sends the login page straight to /auth/login,
	// which logs in without Keycloak.
	DevLogin bool
	// DebugEnabled (DEBUG_IMAGE set) registers the debug WebSocket and tells
	// the UI.
	DebugEnabled bool
	// ArgoPublicURL is Argo CD's address in the browser, sent to the UI for
	// links to applications.
	ArgoPublicURL string
}

// Local login attempts allowed per minute, per client IP and per username.
const (
	loginLimitPerIP   = 20
	loginLimitPerUser = 5
)

// Handler holds the dependencies of all HTTP handlers.
type Handler struct {
	svc    Services
	opts   Options
	limits *rateLimiter[int64]
	// loginByIP and loginByUser throttle local logins.
	loginByIP   *rateLimiter[string]
	loginByUser *rateLimiter[string]
	logger      *zap.Logger
}

func NewHandler(svc Services, opts Options, logger *zap.Logger) *Handler {
	return &Handler{svc: svc, opts: opts, limits: newRateLimiter[int64](opts.RateLimitPerMinute),
		loginByIP: newRateLimiter[string](loginLimitPerIP), loginByUser: newRateLimiter[string](loginLimitPerUser), logger: logger}
}
