// Package config loads the portal's configuration from the environment and
// fails fast on anything missing or malformed. Secrets are read from files
// (Kubernetes Secrets mounted as files) via NAME_FILE; NAME itself is
// accepted for local development.
package config

import (
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/joho/godotenv"
	"go.uber.org/zap/zapcore"
)

type Config struct {
	Port        string
	MetricsPort string
	GinMode     string
	LogLevel    zapcore.Level

	// PublicURL is the portal's external URL (scheme and host). The
	// WebSocket Origin check, the OIDC redirect and notification links use it.
	PublicURL string
	// CookieSecure sets the Secure flag on cookies. True unless the portal
	// is reached over plain HTTP for local development.
	CookieSecure bool

	// BootstrapAdmins are Keycloak usernames made admin at every login.
	BootstrapAdmins []string
	// AdminGroups are Keycloak groups (TIPHYS_ADMIN_GROUPS, comma-separated)
	// whose members are made admin at every login; GroupsClaim is the ID
	// token claim that lists a user's groups.
	AdminGroups []string
	GroupsClaim string

	// DatabaseURL is the application role's DSN (INSERT/SELECT only on
	// audit_events). DatabaseRetentionURL is the retention job's role, used
	// for nothing else. DatabaseMigrateURL, when set, runs migrations at
	// startup under the owner role.
	DatabaseURL          string
	DatabaseRetentionURL string
	DatabaseMigrateURL   string

	// DevLoginUser (DEV_LOGIN_USER), for local development only, replaces
	// Keycloak: every visitor is logged in as this user, an admin if it is
	// "admin" and a regular user otherwise.
	// Refused with GIN_MODE=release; the OIDC_* settings are then optional.
	DevLoginUser string

	// LocalUsers (LOCAL_USERS_ENABLED, default false) lets admins create
	// users who log in with a portal password instead of Keycloak.
	LocalUsers bool

	OIDCIssuerURL    string
	OIDCClientID     string
	OIDCClientSecret string
	OIDCRedirectURL  string
	// SessionKeys encrypt stored Keycloak tokens and the login cookie; the
	// first encrypts, all decrypt (rotation).
	SessionKeys []string

	ArgoURL string
	// ArgoPublicURL (ARGO_PUBLIC_URL, default ARGO_URL) is Argo CD's address
	// in the browser, for links to applications.
	ArgoPublicURL          string
	ArgoToken              string
	ArgoCAFile             string
	ArgoInsecureSkipVerify bool

	MinioEndpoint  string
	MinioBucket    string
	MinioAccessKey string
	MinioSecretKey string
	MinioUseSSL    bool
	// MinioEnsureBucket creates the bucket (with Object Lock) if missing;
	// for local development only.
	MinioEnsureBucket bool

	LokiURL      string
	LokiUser     string
	LokiPassword string
	LokiToken    string
	LokiTenantID string

	// CredentialStore is "secrets" (Secrets in the portal's namespace annotated
	// tiphys/cluster: <cluster name>, each with a kubeconfig key; the portal
	// only reads them) or "dir" (a directory of <cluster>.kubeconfig files,
	// for local development). Either way, the credentials define the clusters.
	CredentialStore     string
	CredentialNamespace string
	CredentialDir       string
	// ManagementKubeconfig is used for the Secrets store when the portal does
	// not run in-cluster.
	ManagementKubeconfig string

	// ClusterRegistrationFromUI (CLUSTER_REGISTRATION_FROM_UI, default false)
	// turns on adding a cluster and replacing its credential from the UI, for
	// local testing. It writes the kubeconfig to CREDENTIAL_DIR, so it requires
	// CREDENTIAL_STORE=dir: the portal never writes Secrets in a cluster.
	ClusterRegistrationFromUI bool

	// KubeExtraDomain is the domain of the impersonation extras, e.g.
	// "portal.example.com"; it must match the per-cluster chart.
	KubeExtraDomain string

	// DebugImage (DEBUG_IMAGE) is the image of debug containers, pinned by
	// digest; it must equal the chart's debug.image. Empty turns debug off.
	DebugImage string

	// WebDir, when set, serves the frontend from this directory instead of
	// the embedded build (frontend development).
	WebDir string

	RateLimitPerMinute int
}

func Load() (*Config, error) {
	_ = godotenv.Load()

	c := &Config{
		Port:        getenv("PORT", "8080"),
		MetricsPort: getenv("METRICS_PORT", "9090"),
		GinMode:     getenv("GIN_MODE", "release"),
	}

	level := getenv("LOG_LEVEL", "info")
	if err := c.LogLevel.UnmarshalText([]byte(level)); err != nil {
		return nil, fmt.Errorf("invalid LOG_LEVEL %q: %w", level, err)
	}

	var err error
	if c.PublicURL, err = requiredURL("PUBLIC_URL"); err != nil {
		return nil, err
	}
	c.PublicURL = strings.TrimRight(c.PublicURL, "/")
	if c.CookieSecure, err = boolEnv("COOKIE_SECURE", strings.HasPrefix(c.PublicURL, "https://")); err != nil {
		return nil, err
	}
	c.BootstrapAdmins = list(os.Getenv("TIPHYS_BOOTSTRAP_ADMINS"))
	c.AdminGroups = list(os.Getenv("TIPHYS_ADMIN_GROUPS"))
	c.GroupsClaim = getenv("OIDC_GROUPS_CLAIM", "groups")

	if c.DatabaseURL, err = required("DATABASE_URL"); err != nil {
		return nil, err
	}
	c.DatabaseRetentionURL = os.Getenv("DATABASE_RETENTION_URL")
	c.DatabaseMigrateURL = os.Getenv("DATABASE_MIGRATE_URL")

	c.DevLoginUser = strings.TrimSpace(os.Getenv("DEV_LOGIN_USER"))
	if c.DevLoginUser != "" && c.GinMode == "release" {
		return nil, fmt.Errorf("DEV_LOGIN_USER bypasses Keycloak and is refused with GIN_MODE=release")
	}
	if c.DevLoginUser == "" {
		if c.OIDCIssuerURL, err = requiredURL("OIDC_ISSUER_URL"); err != nil {
			return nil, err
		}
		if c.OIDCClientID, err = required("OIDC_CLIENT_ID"); err != nil {
			return nil, err
		}
		if c.OIDCClientSecret, err = secret("OIDC_CLIENT_SECRET"); err != nil {
			return nil, err
		}
	}
	c.OIDCRedirectURL = getenv("OIDC_REDIRECT_URL", c.PublicURL+"/auth/callback")
	if c.LocalUsers, err = boolEnv("LOCAL_USERS_ENABLED", false); err != nil {
		return nil, err
	}
	keys, err := secret("SESSION_ENCRYPTION_KEYS")
	if err != nil {
		return nil, err
	}
	c.SessionKeys = list(keys)
	if len(c.SessionKeys) == 0 {
		return nil, fmt.Errorf("SESSION_ENCRYPTION_KEYS must hold at least one key")
	}

	if c.ArgoURL, err = requiredURL("ARGO_URL"); err != nil {
		return nil, err
	}
	c.ArgoPublicURL = c.ArgoURL
	if os.Getenv("ARGO_PUBLIC_URL") != "" {
		if c.ArgoPublicURL, err = requiredURL("ARGO_PUBLIC_URL"); err != nil {
			return nil, err
		}
	}
	c.ArgoPublicURL = strings.TrimRight(c.ArgoPublicURL, "/")
	if c.ArgoToken, err = secret("ARGO_TOKEN"); err != nil {
		return nil, err
	}
	c.ArgoCAFile = os.Getenv("ARGO_CA_FILE")
	if c.ArgoInsecureSkipVerify, err = boolEnv("ARGO_INSECURE_SKIP_VERIFY", false); err != nil {
		return nil, err
	}

	if c.MinioEndpoint, err = required("MINIO_ENDPOINT"); err != nil {
		return nil, err
	}
	c.MinioBucket = getenv("MINIO_BUCKET", "tiphys-recordings")
	if c.MinioAccessKey, err = secret("MINIO_ACCESS_KEY"); err != nil {
		return nil, err
	}
	if c.MinioSecretKey, err = secret("MINIO_SECRET_KEY"); err != nil {
		return nil, err
	}
	if c.MinioUseSSL, err = boolEnv("MINIO_USE_SSL", true); err != nil {
		return nil, err
	}
	if c.MinioEnsureBucket, err = boolEnv("MINIO_ENSURE_BUCKET", false); err != nil {
		return nil, err
	}

	// Loki is optional so the portal can start without it, but audit then
	// has no immutable copy; main logs a warning.
	c.LokiURL = os.Getenv("LOKI_URL")
	c.LokiUser = os.Getenv("LOKI_USER")
	c.LokiPassword, _ = secret("LOKI_PASSWORD")
	c.LokiToken, _ = secret("LOKI_TOKEN")
	c.LokiTenantID = os.Getenv("LOKI_TENANT_ID")

	c.CredentialStore = getenv("CREDENTIAL_STORE", "secrets")
	switch c.CredentialStore {
	case "secrets":
		c.ManagementKubeconfig = os.Getenv("MANAGEMENT_KUBECONFIG")
		c.CredentialNamespace = os.Getenv("CREDENTIAL_NAMESPACE")
		if c.CredentialNamespace == "" {
			// In a cluster, default to the pod's own namespace. Never fall back
			// to "all namespaces": the portal must only see its own Secrets.
			if b, err := os.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/namespace"); err == nil {
				c.CredentialNamespace = strings.TrimSpace(string(b))
			}
		}
		if c.CredentialNamespace == "" {
			return nil, fmt.Errorf("CREDENTIAL_NAMESPACE is required when CREDENTIAL_STORE=secrets and the portal does not run in a cluster")
		}
	case "dir":
		if c.CredentialDir, err = required("CREDENTIAL_DIR"); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("invalid CREDENTIAL_STORE %q, expected \"secrets\" or \"dir\"", c.CredentialStore)
	}

	if c.ClusterRegistrationFromUI, err = boolEnv("CLUSTER_REGISTRATION_FROM_UI", false); err != nil {
		return nil, err
	}
	if c.ClusterRegistrationFromUI && c.CredentialStore != "dir" {
		return nil, fmt.Errorf("CLUSTER_REGISTRATION_FROM_UI needs CREDENTIAL_STORE=dir: the portal never writes Secrets in a cluster")
	}
	c.KubeExtraDomain = getenv("KUBE_EXTRA_DOMAIN", "portal.example.com")
	c.DebugImage = strings.TrimSpace(os.Getenv("DEBUG_IMAGE"))
	c.WebDir = os.Getenv("WEB_DIR")
	n, err := intEnv("RATE_LIMIT_PER_MINUTE", 30)
	if err != nil {
		return nil, err
	}
	c.RateLimitPerMinute = n
	return c, nil
}

func (c *Config) Addr() string        { return ":" + c.Port }
func (c *Config) MetricsAddr() string { return ":" + c.MetricsPort }

func getenv(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}

func required(name string) (string, error) {
	v := os.Getenv(name)
	if v == "" {
		return "", fmt.Errorf("%s is required", name)
	}
	return v, nil
}

func requiredURL(name string) (string, error) {
	v, err := required(name)
	if err != nil {
		return "", err
	}
	u, err := url.Parse(v)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("invalid %s %q: expected an absolute URL", name, v)
	}
	return v, nil
}

// secret reads NAME_FILE (trimmed) or falls back to NAME.
func secret(name string) (string, error) {
	if path := os.Getenv(name + "_FILE"); path != "" {
		b, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("read %s_FILE: %w", name, err)
		}
		return strings.TrimSpace(string(b)), nil
	}
	if v := os.Getenv(name); v != "" {
		return v, nil
	}
	return "", fmt.Errorf("%s_FILE (or %s) is required", name, name)
}

func boolEnv(name string, def bool) (bool, error) {
	raw := os.Getenv(name)
	if raw == "" {
		return def, nil
	}
	v, err := strconv.ParseBool(raw)
	if err != nil {
		return false, fmt.Errorf("invalid %s %q: %w", name, raw, err)
	}
	return v, nil
}

func intEnv(name string, def int) (int, error) {
	raw := os.Getenv(name)
	if raw == "" {
		return def, nil
	}
	v, err := strconv.Atoi(raw)
	if err != nil || v < 1 {
		return 0, fmt.Errorf("invalid %s %q: expected a positive integer", name, raw)
	}
	return v, nil
}

func list(raw string) []string {
	var out []string
	for _, item := range strings.FieldsFunc(raw, func(r rune) bool { return r == ',' || r == '\n' }) {
		if item = strings.TrimSpace(item); item != "" {
			out = append(out, item)
		}
	}
	return out
}
