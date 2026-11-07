package kubeaccess

import (
	"context"
	"strings"

	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/rest"

	"tiphys/internal/application"
)

// ResourceScopes reads the cluster's discovery and returns whether each kind
// is namespaced. Groups whose discovery fails are left out (the caller treats
// their kinds as unknown); only a failure of the whole discovery is an error.
func (a *Access) ResourceScopes(_ context.Context, actor application.KubeActor, cluster string) (map[application.KubeGroupKind]bool, error) {
	c, err := a.openCluster(actor, cluster)
	if err != nil {
		return nil, err
	}
	// Discovery takes no context, so a timeout on the config bounds it.
	cfg := rest.CopyConfig(c.cfg)
	cfg.Timeout = defaultCallTimeout
	tc, err := a.factory(cfg)
	if err != nil {
		return nil, a.fail("build clients", cluster, err)
	}
	_, lists, err := tc.kube.Discovery().ServerGroupsAndResources()
	if err != nil && !discovery.IsGroupDiscoveryFailedError(err) {
		return nil, a.fail("discovery", cluster, err)
	}
	out := map[application.KubeGroupKind]bool{}
	for _, l := range lists {
		gv, perr := schema.ParseGroupVersion(l.GroupVersion)
		if perr != nil {
			continue
		}
		for _, r := range l.APIResources {
			if strings.Contains(r.Name, "/") { // subresource
				continue
			}
			out[application.KubeGroupKind{Group: gv.Group, Kind: r.Kind}] = r.Namespaced
		}
	}
	return out, nil
}
