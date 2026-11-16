package kubeaccess

import (
	"slices"
	"testing"
)

func TestShellCommand(t *testing.T) {
	if got := shellCommand("/bin/sh"); !slices.Equal(got, []string{"/bin/sh"}) {
		t.Fatalf("/bin/sh: %q", got)
	}
	if got := shellCommand("/bin/bash"); !slices.Equal(got, []string{"/bin/sh", "-c", "[ -x /bin/bash ] && exec /bin/bash; exec /bin/sh"}) {
		t.Fatalf("/bin/bash: %q", got)
	}
}
