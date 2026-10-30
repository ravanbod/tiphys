package kubeaccess

import (
	"context"
	"sort"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"tiphys/internal/application"
)

// ListNamespaces lists the cluster's namespace names. Protected namespaces
// are left out, like every other verb refuses them; names that are not valid
// namespace names are skipped.
func (a *Access) ListNamespaces(ctx context.Context, actor application.KubeActor, cluster string) ([]string, error) {
	c, err := a.openCluster(actor, cluster)
	if err != nil {
		return nil, err
	}
	ctx, cancel := callCtx(ctx)
	defer cancel()

	list, err := c.kube.CoreV1().Namespaces().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, a.fail("list namespaces", cluster, err)
	}
	out := make([]string, 0, len(list.Items))
	for i := range list.Items {
		name := list.Items[i].Name
		if a.protected.IsProtected(name) {
			continue
		}
		out = append(out, name)
	}
	sort.Strings(out)
	return out, nil
}
