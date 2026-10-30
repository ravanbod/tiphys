// Package credentials implements application.CredentialStore: cluster
// kubeconfigs as Secrets in the management cluster, or as files for local
// development.
package credentials

import (
	"fmt"

	"tiphys/internal/application"
	"tiphys/internal/domain"
)

// maxNameLen keeps cluster names short enough for file names and UI labels.
const maxNameLen = 63

func validName(name string) error {
	if len(name) > maxNameLen || !domain.ValidDNS1123Subdomain(name) {
		return fmt.Errorf("%w: cluster name %q", application.ErrInvalidName, name)
	}
	return nil
}

func emptyKubeconfig(b []byte) error {
	if len(b) == 0 {
		return fmt.Errorf("%w: empty kubeconfig", application.ErrInvalidName)
	}
	return nil
}
