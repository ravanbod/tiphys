package application

import (
	"fmt"
	"strings"
	"testing"

	"tiphys/internal/domain"
)

func TestValidateSettingsSyncClusterKinds(t *testing.T) {
	check := func(kinds []string) string {
		s := domain.DefaultSettings()
		s.ArgoSyncClusterKinds = kinds
		if err := ValidateSettings(s); err != nil {
			return err.Fields["argoSyncClusterKinds"]
		}
		return ""
	}
	if msg := check([]string{"ClusterInput.fluentbit.fluent.io", "ClusterOutput.fluentbit.fluent.io"}); msg != "" {
		t.Fatalf("valid list: %s", msg)
	}
	if msg := check([]string{"ClusterInput.fluentbit.fluent.io", "ClusterInput.fluentbit.fluent.io"}); !strings.Contains(msg, "twice") {
		t.Fatalf("duplicate: %q", msg)
	}
	if msg := check([]string{"ClusterRole.rbac.authorization.k8s.io"}); !strings.Contains(msg, "cannot be allowed") {
		t.Fatalf("never allowed: %q", msg)
	}
	many := make([]string, domain.MaxSyncClusterKinds+1)
	for i := range many {
		many[i] = fmt.Sprintf("Kind%d.example.com", i)
	}
	if msg := check(many); !strings.Contains(msg, "at most") {
		t.Fatalf("too many: %q", msg)
	}
}
