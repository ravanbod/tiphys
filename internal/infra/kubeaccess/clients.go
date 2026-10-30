package kubeaccess

import (
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// clients are the Kubernetes clients for one call. They are cheap to build:
// client-go shares the TLS transport between configs that differ only in the
// bearer token or impersonation, so a derived config per call reuses the
// cluster's connections.
type clients struct {
	kube kubernetes.Interface
	dyn  dynamic.Interface
	cfg  *rest.Config
}

type clientFactory func(cfg *rest.Config) (*clients, error)

func newClients(cfg *rest.Config) (*clients, error) {
	kube, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, err
	}
	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return nil, err
	}
	return &clients{kube: kube, dyn: dyn, cfg: cfg}, nil
}
