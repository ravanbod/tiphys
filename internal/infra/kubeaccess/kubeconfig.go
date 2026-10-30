package kubeaccess

import (
	"errors"
	"fmt"
	"net/url"

	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

// ErrKubeconfigRejected is returned for a kubeconfig that is unparsable or
// uses something the portal does not accept; the message says what.
var ErrKubeconfigRejected = errors.New("kubeconfig rejected")

const (
	connectorQPS   = 50
	connectorBurst = 100
	userAgent      = "tiphys-kubeaccess"
)

// parseKubeconfig turns a stored or pasted kubeconfig into the connector's
// base rest.Config: API server, CA and bearer token only. Anything that could
// run a program, read a local file, skip TLS verification or use another
// credential type is rejected; the token is never included in an error.
func parseKubeconfig(data []byte) (*rest.Config, error) {
	raw, err := clientcmd.Load(data)
	if err != nil {
		return nil, fmt.Errorf("%w: not a valid kubeconfig", ErrKubeconfigRejected)
	}
	ctxName := raw.CurrentContext
	if ctxName == "" && len(raw.Contexts) == 1 {
		for name := range raw.Contexts {
			ctxName = name
		}
	}
	kctx := raw.Contexts[ctxName]
	if kctx == nil {
		return nil, reject("no usable current-context")
	}
	cluster := raw.Clusters[kctx.Cluster]
	user := raw.AuthInfos[kctx.AuthInfo]
	if cluster == nil || user == nil {
		return nil, reject("context refers to a missing cluster or user")
	}
	if err := checkCluster(cluster); err != nil {
		return nil, err
	}
	if err := checkUser(user); err != nil {
		return nil, err
	}
	return &rest.Config{
		Host:            cluster.Server,
		BearerToken:     user.Token,
		TLSClientConfig: rest.TLSClientConfig{CAData: cluster.CertificateAuthorityData, ServerName: cluster.TLSServerName},
		UserAgent:       userAgent,
		WarningHandler:  rest.NoWarnings{},
		QPS:             connectorQPS,
		Burst:           connectorBurst,
	}, nil
}

func reject(why string) error { return fmt.Errorf("%w: %s", ErrKubeconfigRejected, why) }

func checkCluster(c *clientcmdapi.Cluster) error {
	u, err := url.Parse(c.Server)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return reject("server must be an https URL")
	}
	if c.InsecureSkipTLSVerify {
		return reject("insecure-skip-tls-verify is not allowed")
	}
	if c.CertificateAuthority != "" {
		return reject("certificate-authority must be inline (certificate-authority-data)")
	}
	if c.ProxyURL != "" {
		return reject("proxy-url is not allowed")
	}
	return nil
}

func checkUser(u *clientcmdapi.AuthInfo) error {
	switch {
	case u.Exec != nil:
		return reject("exec credential plugins are not allowed")
	case u.AuthProvider != nil:
		return reject("auth-provider plugins are not allowed")
	case u.TokenFile != "":
		return reject("tokenFile is not allowed, use an inline token")
	case u.ClientCertificate != "" || len(u.ClientCertificateData) > 0 || u.ClientKey != "" || len(u.ClientKeyData) > 0:
		return reject("client certificates are not allowed, use a bearer token")
	case u.Username != "" || u.Password != "":
		return reject("basic auth is not allowed, use a bearer token")
	case u.Impersonate != "" || len(u.ImpersonateGroups) > 0 || len(u.ImpersonateUserExtra) > 0 || u.ImpersonateUID != "":
		return reject("the kubeconfig must not set impersonation")
	case u.Token == "":
		return reject("a bearer token is required")
	}
	return nil
}
