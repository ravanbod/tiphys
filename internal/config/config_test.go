package config

import (
	"strings"
	"testing"
)

// setRequired sets every variable Load insists on.
func setRequired(t *testing.T) {
	t.Helper()
	for k, v := range map[string]string{
		"PUBLIC_URL": "http://localhost:8080", "DATABASE_URL": "postgres://x", "OIDC_ISSUER_URL": "http://kc/realms/r",
		"OIDC_CLIENT_ID": "tiphys", "OIDC_CLIENT_SECRET": "s", "SESSION_ENCRYPTION_KEYS": "k",
		"ARGO_URL": "http://argo", "ARGO_TOKEN": "t", "MINIO_ENDPOINT": "minio:9000",
		"MINIO_ACCESS_KEY": "a", "MINIO_SECRET_KEY": "b",
	} {
		t.Setenv(k, v)
	}
}

func TestDevLoginIsRefusedInRelease(t *testing.T) {
	setRequired(t)
	t.Setenv("DEV_LOGIN_USER", "dev")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "GIN_MODE=release") {
		t.Fatalf("an unset GIN_MODE is release and must refuse the Keycloak bypass: %v", err)
	}

	t.Setenv("GIN_MODE", "debug")
	t.Setenv("CREDENTIAL_STORE", "dir")
	t.Setenv("CREDENTIAL_DIR", t.TempDir())
	t.Setenv("OIDC_ISSUER_URL", "")
	t.Setenv("OIDC_CLIENT_ID", "")
	t.Setenv("OIDC_CLIENT_SECRET", "")
	c, err := Load()
	if err != nil || c.DevLoginUser != "dev" {
		t.Fatalf("debug mode without OIDC settings: %v %+v", err, c)
	}
}

func TestUIRegistrationNeedsTheDirStore(t *testing.T) {
	setRequired(t)
	t.Setenv("CREDENTIAL_STORE", "secrets")
	t.Setenv("CREDENTIAL_NAMESPACE", "tiphys-portal")
	t.Setenv("CLUSTER_REGISTRATION_FROM_UI", "true")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "CREDENTIAL_STORE=dir") {
		t.Fatalf("the portal must never be able to write Secrets: %v", err)
	}

	t.Setenv("CREDENTIAL_STORE", "dir")
	t.Setenv("CREDENTIAL_DIR", t.TempDir())
	c, err := Load()
	if err != nil || !c.ClusterRegistrationFromUI {
		t.Fatalf("dir store with UI registration: %+v, %v", c, err)
	}
}

func TestSecretsStoreIsReadOnlyByDefaultAndNeedsANamespace(t *testing.T) {
	setRequired(t)
	t.Setenv("CREDENTIAL_STORE", "secrets")
	t.Setenv("CREDENTIAL_NAMESPACE", "")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "CREDENTIAL_NAMESPACE") {
		t.Fatalf("an empty namespace must not mean all namespaces: %v", err)
	}
	t.Setenv("CREDENTIAL_NAMESPACE", "tiphys-portal")
	c, err := Load()
	if err != nil || c.ClusterRegistrationFromUI || c.CredentialNamespace != "tiphys-portal" {
		t.Fatalf("secrets store: %+v, %v", c, err)
	}
}

func TestArgoPublicURLDefaultsToArgoURL(t *testing.T) {
	setRequired(t)
	t.Setenv("CREDENTIAL_STORE", "dir")
	t.Setenv("CREDENTIAL_DIR", t.TempDir())
	t.Setenv("ARGO_URL", "http://argocd-server.argocd.svc/")
	if c, err := Load(); err != nil || c.ArgoPublicURL != "http://argocd-server.argocd.svc" {
		t.Fatalf("default: %v %q", err, c.ArgoPublicURL)
	}
	t.Setenv("ARGO_PUBLIC_URL", "https://argo.example.com/")
	if c, err := Load(); err != nil || c.ArgoPublicURL != "https://argo.example.com" {
		t.Fatalf("set: %v %q", err, c.ArgoPublicURL)
	}
	t.Setenv("ARGO_PUBLIC_URL", "argo.example.com")
	if _, err := Load(); err == nil {
		t.Fatal("a relative ARGO_PUBLIC_URL must be refused")
	}
}
