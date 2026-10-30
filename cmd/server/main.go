package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
	_ "time/tzdata" // the runtime image has no zoneinfo; time windows need named zones

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"

	"tiphys/internal/application"
	"tiphys/internal/config"
	"tiphys/internal/infra/argocd"
	"tiphys/internal/infra/credentials"
	"tiphys/internal/infra/kubeaccess"
	"tiphys/internal/infra/loki"
	"tiphys/internal/infra/oidc"
	"tiphys/internal/infra/postgres"
	"tiphys/internal/infra/recording"
	"tiphys/internal/infra/rocketchat"
	"tiphys/internal/infra/sealer"
	"tiphys/internal/monitoring"
	"tiphys/internal/transport/serverhttp"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("failed to load config: %v", err)
	}

	// GinMode picks the log format (JSON vs console); LogLevel controls
	// verbosity independently.
	var zapConfig zap.Config
	if cfg.GinMode == gin.ReleaseMode {
		zapConfig = zap.NewProductionConfig()
	} else {
		zapConfig = zap.NewDevelopmentConfig()
	}
	zapConfig.Level = zap.NewAtomicLevelAt(cfg.LogLevel)
	// Sampling would silently drop repeated audit-relevant lines under load.
	zapConfig.Sampling = nil
	logger, err := zapConfig.Build(zap.AddStacktrace(zapcore.PanicLevel))
	if err != nil {
		log.Fatalf("failed to create logger: %v", err)
	}
	defer logger.Sync()

	if err := run(cfg, logger); err != nil {
		logger.Fatal("portal stopped", zap.Error(err))
	}
}

func run(cfg *config.Config, logger *zap.Logger) error {
	// sigCtx ends on SIGTERM; rootCtx outlives it so streams can be closed
	// and their recordings finalized before background loops stop.
	sigCtx, stopSignals := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stopSignals()
	rootCtx, cancelRoot := context.WithCancel(context.Background())
	defer cancelRoot()

	startCtx, cancelStart := context.WithTimeout(rootCtx, 60*time.Second)
	defer cancelStart()

	clock := application.SystemClock

	// ---- persistence ----
	if cfg.DatabaseMigrateURL != "" {
		if err := postgres.Migrate(startCtx, cfg.DatabaseMigrateURL); err != nil {
			return err
		}
		logger.Info("database migrations applied")
	}
	store, err := postgres.Open(startCtx, postgres.Config{DSN: cfg.DatabaseURL})
	if err != nil {
		return err
	}
	defer store.Close()

	var retention application.AuditRetention
	if cfg.DatabaseRetentionURL != "" {
		r, err := postgres.NewAuditRetention(startCtx, cfg.DatabaseRetentionURL)
		if err != nil {
			return err
		}
		defer r.Close()
		retention = r
	} else {
		logger.Warn("DATABASE_RETENTION_URL is not set: audit retention deletes are disabled")
	}

	// ---- identity ----
	var oidcClient application.OIDCProvider
	if cfg.DevLoginUser != "" {
		logger.Warn("DEV_LOGIN_USER is set: Keycloak is bypassed and every visitor is logged in as this user (local development only)",
			zap.String("user", cfg.DevLoginUser), zap.Bool("admin", cfg.DevLoginUser == oidc.DevAdminUser))
		oidcClient = oidc.NewDev(cfg.DevLoginUser, cfg.OIDCRedirectURL)
		cfg.AdminGroups = append(cfg.AdminGroups, oidc.DevAdminGroup)
	} else {
		kc, err := oidc.NewKeycloak(startCtx, oidc.Config{
			Issuer: cfg.OIDCIssuerURL, ClientID: cfg.OIDCClientID, ClientSecret: cfg.OIDCClientSecret, RedirectURL: cfg.OIDCRedirectURL,
			// No offline_access: the session must end with the Keycloak session.
			Scopes:      []string{"openid", "profile", "email"},
			GroupsClaim: cfg.GroupsClaim,
		}, &http.Client{Timeout: 15 * time.Second}, logger)
		if err != nil {
			return err
		}
		oidcClient = kc
	}
	sealerImpl, err := sealer.New(cfg.SessionKeys...)
	if err != nil {
		return err
	}

	// ---- upstream systems ----
	argoHTTP, err := argoHTTPClient(cfg)
	if err != nil {
		return err
	}
	argoClient := argocd.NewHTTPClient(cfg.ArgoURL, cfg.ArgoToken, argoHTTP, logger)

	recStore, err := recording.NewMinIOStore(recording.Config{
		Endpoint: cfg.MinioEndpoint, AccessKey: cfg.MinioAccessKey, SecretKey: cfg.MinioSecretKey,
		Secure: cfg.MinioUseSSL, Bucket: cfg.MinioBucket,
	}, logger)
	if err != nil {
		return err
	}
	if cfg.MinioEnsureBucket {
		if err := recStore.EnsureBucket(startCtx, 180); err != nil {
			return err
		}
	}

	var sink application.AuditSink
	if cfg.LokiURL != "" {
		sink = loki.NewHTTPSink(cfg.LokiURL, loki.Options{Username: cfg.LokiUser, Password: cfg.LokiPassword,
			BearerToken: cfg.LokiToken, TenantID: cfg.LokiTenantID}, &http.Client{Timeout: 10 * time.Second}, logger)
	} else {
		logger.Warn("LOKI_URL is not set: audit events are stored in PostgreSQL only, without the immutable copy")
	}
	notifier := rocketchat.NewHTTPNotifier(&http.Client{Timeout: 10 * time.Second}, logger)

	// ---- application ----
	argo := application.NewArgoService(argoClient, clock, logger)
	audit := application.NewAuditService(store, sink, clock, logger)
	policy := application.NewPolicyService(store, argo, clock, logger)
	argo.SetOnChange(policy.Changed)
	settings := application.NewSettingsService(store, audit, logger)
	if err := settings.Reload(startCtx); err != nil {
		return err
	}
	policy.SetBreakGlassEnabled(func() bool { return settings.Get().BreakGlassEnabled })
	if err := policy.Reload(startCtx); err != nil {
		return err
	}
	access := application.NewAccessService(policy, audit, clock)

	credStore, err := newCredentialStore(cfg, logger)
	if err != nil {
		return err
	}
	if cfg.ClusterRegistrationFromUI {
		logger.Info("adding clusters from the UI is enabled: credentials are written to CREDENTIAL_DIR (local testing)")
	}
	registry := kubeaccess.NewRegistry(credStore, logger)
	kube := kubeaccess.New(registry, access, logger, kubeaccess.WithExtraDomain(cfg.KubeExtraDomain), kubeaccess.WithDebugImage(cfg.DebugImage))
	if cfg.DebugImage != "" {
		logger.Info("debug containers are enabled", zap.String("image", cfg.DebugImage))
	}

	notify := application.NewNotificationService(notifier, settings, audit, cfg.PublicURL, logger)
	auth := application.NewAuthService(oidcClient, sealerImpl, store, policy, audit, clock,
		application.AuthConfig{BootstrapAdmins: cfg.BootstrapAdmins, AdminGroups: cfg.AdminGroups, PostLogoutRedirect: cfg.PublicURL + "/login",
			LocalUsers: cfg.LocalUsers}, logger)
	if cfg.LocalUsers {
		logger.Info("local users are enabled: admins can create users who log in with a portal password")
	}
	logger.Info("admin bootstrap", zap.Strings("bootstrap_admins", cfg.BootstrapAdmins), zap.Strings("admin_groups", cfg.AdminGroups), zap.String("groups_claim", cfg.GroupsClaim))
	sessionAlive := func(ctx context.Context, id string) bool {
		_, err := store.Repos().WebSessions.Get(ctx, id)
		return err == nil
	}
	streams := application.NewStreamManager(access, policy, sessionAlive, clock, logger)
	auth.SetOnSessionEnded(func(id string) { streams.TerminateWebSession(id, application.EndUserDisabled) })

	ops := application.NewOperations()
	workloads := application.NewWorkloadService(rootCtx, access, kube, argo, audit, streams, ops, settings, notify, clock, logger)
	execSvc := application.NewExecService(rootCtx, access, kube, recStore, audit, streams, store, settings, notify, clock, logger)
	sessions := application.NewSessionService(store, recStore, audit, execSvc)
	breakGlass := application.NewBreakGlassService(store, audit, policy, settings, notify, clock)
	requests := application.NewAccessRequestService(store, audit, policy, argo, notify, clock)
	admin := application.NewAdminService(store, audit, policy, settings, notify, auth, streams, access, clock, logger)
	clusters := application.NewClusterService(store, kube, credStore, audit, policy, clock, logger)
	clusters.SetCredentialsWritable(cfg.ClusterRegistrationFromUI)
	maintenance := application.NewMaintenanceService(store, retention, requests, settings, audit, clock, logger)

	maintenance.AbandonedSessions(startCtx)

	// ---- background loops ----
	go audit.Run(rootCtx)
	go notify.Run(rootCtx)
	go argo.Run(rootCtx)
	go policy.Run(rootCtx)
	go settings.Run(rootCtx)
	go streams.Run(rootCtx)
	go ops.Run(rootCtx)
	go auth.RefreshLoop(rootCtx)
	go maintenance.Run(rootCtx)
	go func() {
		if err := registry.Run(rootCtx); err != nil && !errors.Is(err, context.Canceled) {
			logger.Error("cluster registry stopped", zap.Error(err))
		}
	}()
	go clusters.Run(rootCtx)

	// ---- HTTP ----
	handler := serverhttp.NewHandler(serverhttp.Services{
		Auth: auth, Access: access, Workloads: workloads, Exec: execSvc, Sessions: sessions, Audit: audit,
		BreakGlass: breakGlass, Requests: requests, Admin: admin, Clusters: clusters, Argo: argo, Settings: settings,
		Streams: streams, Policy: policy, Store: store, Kube: kube, Notify: notify,
	}, serverhttp.Options{PublicURL: cfg.PublicURL, CookieSecure: cfg.CookieSecure, RateLimitPerMinute: cfg.RateLimitPerMinute, WebDir: cfg.WebDir,
		ClusterRegistrationFromUI: cfg.ClusterRegistrationFromUI, DevLogin: cfg.DevLoginUser != "", DebugEnabled: cfg.DebugImage != "",
		ArgoPublicURL: cfg.ArgoPublicURL}, logger)
	router := serverhttp.NewRouter(handler, logger, cfg.GinMode)

	srv := &http.Server{Addr: cfg.Addr(), Handler: router, ReadHeaderTimeout: 10 * time.Second}
	metricsMux := http.NewServeMux()
	metricsMux.Handle("/metrics", monitoring.Handler())
	metricsSrv := &http.Server{Addr: cfg.MetricsAddr(), Handler: metricsMux, ReadHeaderTimeout: 10 * time.Second}

	errCh := make(chan error, 2)
	go func() {
		logger.Info("starting metrics server", zap.String("addr", cfg.MetricsAddr()))
		errCh <- metricsSrv.ListenAndServe()
	}()
	go func() {
		logger.Info("starting server", zap.String("addr", cfg.Addr()))
		errCh <- srv.ListenAndServe()
	}()

	select {
	case err := <-errCh:
		return err
	case <-sigCtx.Done():
	}

	// Graceful stop: end streams so recordings are finalized, wait for them,
	// then stop the servers and the background loops.
	logger.Info("shutting down")
	streams.CloseAll(application.EndPortalRestart)
	deadline := time.Now().Add(20 * time.Second)
	for execSvc.ActiveCount() > 0 && time.Now().Before(deadline) {
		time.Sleep(200 * time.Millisecond)
	}
	shutCtx, cancelShut := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelShut()
	_ = srv.Shutdown(shutCtx)
	_ = metricsSrv.Shutdown(shutCtx)
	cancelRoot()
	time.Sleep(500 * time.Millisecond) // let the audit queue drain
	return nil
}

func argoHTTPClient(cfg *config.Config) (*http.Client, error) {
	tlsCfg := &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: cfg.ArgoInsecureSkipVerify} //nolint:gosec // explicit opt-in for dev
	if cfg.ArgoCAFile != "" {
		pem, err := os.ReadFile(cfg.ArgoCAFile)
		if err != nil {
			return nil, err
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, errors.New("ARGO_CA_FILE holds no valid certificate")
		}
		tlsCfg.RootCAs = pool
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.TLSClientConfig = tlsCfg
	return &http.Client{Transport: tr, Timeout: 30 * time.Second}, nil
}

func newCredentialStore(cfg *config.Config, logger *zap.Logger) (application.CredentialStore, error) {
	if cfg.CredentialStore == "dir" {
		return credentials.NewDirStore(cfg.CredentialDir, 2*time.Second, logger)
	}
	// Read-only: the Secrets belong to whoever runs the cluster.
	return credentials.NewSecretStoreFromConfig(cfg.ManagementKubeconfig, cfg.CredentialNamespace, 10*time.Minute, logger)
}
