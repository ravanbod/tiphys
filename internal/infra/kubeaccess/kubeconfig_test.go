package kubeaccess

import (
	"errors"
	"strings"
	"testing"
)

func kubeconfigYAML(cluster, user string) []byte {
	return []byte(`apiVersion: v1
kind: Config
clusters:
- name: c
  cluster:
` + cluster + `
users:
- name: u
  user:
` + user + `
contexts:
- name: x
  context: {cluster: c, user: u}
current-context: x
`)
}

const goodCluster = "    server: https://k8s.example:6443\n    certificate-authority-data: Zm9v"
const goodUser = "    token: s3cr3t-token"

func TestParseKubeconfigAcceptsTokenAndCAOnly(t *testing.T) {
	cfg, err := parseKubeconfig(kubeconfigYAML(goodCluster, goodUser))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Host != "https://k8s.example:6443" || cfg.BearerToken != "s3cr3t-token" || string(cfg.CAData) != "foo" {
		t.Fatalf("cfg = %+v", cfg)
	}
}

func TestParseKubeconfigRejections(t *testing.T) {
	cases := map[string]struct{ cluster, user string }{
		"exec plugin":   {goodCluster, "    exec:\n      apiVersion: client.authentication.k8s.io/v1\n      command: /bin/evil"},
		"auth provider": {goodCluster, "    auth-provider:\n      name: gcp"},
		"insecure skip": {goodCluster + "\n    insecure-skip-tls-verify: true", goodUser},
		"no token":      {goodCluster, "    username: a\n    password: b"},
		"token file":    {goodCluster, "    tokenFile: /etc/passwd"},
		"client cert":   {goodCluster, goodUser + "\n    client-certificate-data: Zm9v\n    client-key-data: Zm9v"},
		"ca file":       {"    server: https://k8s.example\n    certificate-authority: /etc/ssl/ca.pem", goodUser},
		"http server":   {"    server: http://k8s.example", goodUser},
		"proxy":         {goodCluster + "\n    proxy-url: http://evil:3128", goodUser},
		"impersonation": {goodCluster, goodUser + "\n    as: system:admin"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := parseKubeconfig(kubeconfigYAML(c.cluster, c.user))
			if !errors.Is(err, ErrKubeconfigRejected) {
				t.Fatalf("err = %v", err)
			}
		})
	}
	if _, err := parseKubeconfig([]byte("not: [a kubeconfig")); !errors.Is(err, ErrKubeconfigRejected) {
		t.Fatalf("garbage: %v", err)
	}
}

func TestKubeconfigErrorsNeverContainTheToken(t *testing.T) {
	for _, c := range []string{goodCluster + "\n    insecure-skip-tls-verify: true", "    server: http://x"} {
		_, err := parseKubeconfig(kubeconfigYAML(c, goodUser))
		if err == nil || strings.Contains(err.Error(), "s3cr3t") {
			t.Fatalf("err = %v", err)
		}
	}
}
