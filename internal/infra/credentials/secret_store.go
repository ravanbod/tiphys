package credentials

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clientcmd"

	"tiphys/internal/application"
)

const (
	// ClusterAnnotation marks a Secret as a cluster credential; its value is
	// the cluster name (the same name as in Argo CD).
	ClusterAnnotation = "tiphys/cluster"
	// EnvironmentAnnotation optionally sets the cluster's environment label;
	// when present it wins over the Clusters page.
	EnvironmentAnnotation = "tiphys/environment"
	// KubeconfigKey is the Secret key that holds the kubeconfig.
	KubeconfigKey = "kubeconfig"
)

// ErrReadOnly is returned by Put and Delete: clusters are registered by
// annotated Secrets that the portal's operator manages; the portal never writes
// them.
var ErrReadOnly = errors.New("cluster credentials are read from annotated Secrets and are never written by the portal")

// SecretStore reads cluster kubeconfigs from Secrets in the portal's
// namespace of the management cluster. Any Secret there that carries the
// annotation `tiphys/cluster: <cluster name>` is a cluster credential; the
// kubeconfig is in its `kubeconfig` key. The Secret's own name does not
// matter. The store is strictly read-only: the portal needs only get, list and
// watch on Secrets in that namespace and has no way to change them.
type SecretStore struct {
	client    kubernetes.Interface
	namespace string
	resync    time.Duration
	logger    *zap.Logger
	warned    sync.Map // "secret/value" of invalid annotations already reported
}

var _ application.CredentialStore = (*SecretStore)(nil)

func NewSecretStore(client kubernetes.Interface, namespace string, resync time.Duration, logger *zap.Logger) *SecretStore {
	return &SecretStore{client: client, namespace: namespace, resync: resync, logger: logger}
}

// NewSecretStoreFromConfig connects to the management cluster with the given
// kubeconfig file, or with the in-cluster config when the path is empty.
func NewSecretStoreFromConfig(kubeconfigPath, namespace string, resync time.Duration, logger *zap.Logger) (*SecretStore, error) {
	var cfg *rest.Config
	var err error
	if kubeconfigPath == "" {
		cfg, err = rest.InClusterConfig()
	} else {
		cfg, err = clientcmd.BuildConfigFromFlags("", kubeconfigPath)
	}
	if err != nil {
		return nil, fmt.Errorf("management cluster config: %w", err)
	}
	client, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("management cluster client: %w", err)
	}
	return NewSecretStore(client, namespace, resync, logger), nil
}

// clusterOf returns the cluster name a Secret registers, if it is annotated
// with a valid one.
func (s *SecretStore) clusterOf(sec *corev1.Secret) (string, bool) {
	name, ok := sec.Annotations[ClusterAnnotation]
	if !ok {
		return "", false
	}
	if err := validName(name); err != nil {
		if _, seen := s.warned.LoadOrStore(sec.Name+"/"+name, struct{}{}); !seen {
			s.logger.Warn("ignoring Secret with an invalid cluster name in its annotation",
				zap.String("secret", sec.Name), zap.String("annotation", ClusterAnnotation), zap.String("value", name))
		}
		return "", false
	}
	return name, true
}

// Put is not supported: the portal never writes Secrets. Whoever runs the
// cluster creates, updates and deletes them.
func (s *SecretStore) Put(context.Context, string, []byte) error { return ErrReadOnly }

// Delete is not supported, for the same reason.
func (s *SecretStore) Delete(context.Context, string) error { return ErrReadOnly }

// secretsFor lists the annotated Secrets for a cluster, ordered by name.
func (s *SecretStore) secretsFor(ctx context.Context, cluster string) ([]corev1.Secret, error) {
	list, err := s.client.CoreV1().Secrets(s.namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		s.logger.Error("list cluster credentials failed", zap.Error(err))
		return nil, fmt.Errorf("list credentials: %w", err)
	}
	var out []corev1.Secret
	for i := range list.Items {
		if name, ok := s.clusterOf(&list.Items[i]); ok && name == cluster {
			out = append(out, list.Items[i])
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (s *SecretStore) Get(ctx context.Context, cluster string) ([]byte, error) {
	if err := validName(cluster); err != nil {
		return nil, err
	}
	secs, err := s.secretsFor(ctx, cluster)
	if err != nil {
		return nil, err
	}
	if len(secs) == 0 {
		return nil, application.ErrNotFound
	}
	if len(secs) > 1 {
		s.logger.Warn("several Secrets are annotated for the same cluster, using the first by name",
			zap.String("cluster", cluster), zap.String("used", secs[0].Name), zap.Int("count", len(secs)))
	}
	kc := secs[0].Data[KubeconfigKey]
	if len(kc) == 0 {
		s.logger.Warn("cluster Secret has no kubeconfig key", zap.String("cluster", cluster), zap.String("secret", secs[0].Name), zap.String("key", KubeconfigKey))
		return nil, application.ErrNotFound
	}
	return kc, nil
}

// Environment returns the trimmed tiphys/environment annotation of the
// Secret Get uses; an empty value counts as none.
func (s *SecretStore) Environment(ctx context.Context, cluster string) (string, bool, error) {
	if err := validName(cluster); err != nil {
		return "", false, err
	}
	secs, err := s.secretsFor(ctx, cluster)
	if err != nil {
		return "", false, err
	}
	if len(secs) == 0 {
		return "", false, application.ErrNotFound
	}
	env := strings.TrimSpace(secs[0].Annotations[EnvironmentAnnotation])
	return env, env != "", nil
}

// List returns the names of all annotated clusters.
func (s *SecretStore) List(ctx context.Context) ([]string, error) {
	list, err := s.client.CoreV1().Secrets(s.namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		s.logger.Error("list cluster credentials failed", zap.Error(err))
		return nil, fmt.Errorf("list credentials: %w", err)
	}
	seen := map[string]struct{}{}
	for i := range list.Items {
		if name, ok := s.clusterOf(&list.Items[i]); ok {
			seen[name] = struct{}{}
		}
	}
	out := make([]string, 0, len(seen))
	for n := range seen {
		out = append(out, n)
	}
	sort.Strings(out)
	return out, nil
}

// Watch reports every add, change (including resyncs) and removal of an
// annotated Secret until ctx is done. Credentials that exist at start are
// reported as adds. Kubernetes cannot filter by annotation on the server, so
// the informer sees every Secret of the namespace and filters here. When an
// update changes or removes the annotation, the old cluster name is reported
// too.
func (s *SecretStore) Watch(ctx context.Context, onChange func(cluster string)) error {
	factory := informers.NewSharedInformerFactoryWithOptions(s.client, s.resync, informers.WithNamespace(s.namespace))
	informer := factory.Core().V1().Secrets().Informer()
	notify := func(obj any) {
		if t, ok := obj.(cache.DeletedFinalStateUnknown); ok {
			obj = t.Obj
		}
		if sec, ok := obj.(*corev1.Secret); ok {
			if name, ok := s.clusterOf(sec); ok {
				onChange(name)
			}
		}
	}
	_, err := informer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: notify,
		UpdateFunc: func(oldObj, newObj any) {
			notify(newObj)
			o, _ := oldObj.(*corev1.Secret)
			n, _ := newObj.(*corev1.Secret)
			if o == nil || n == nil {
				return
			}
			if oldName, ok := s.clusterOf(o); ok {
				if newName, ok := s.clusterOf(n); !ok || newName != oldName {
					onChange(oldName)
				}
			}
		},
		DeleteFunc: notify,
	})
	if err != nil {
		return fmt.Errorf("watch credentials: %w", err)
	}
	factory.Start(ctx.Done())
	if !cache.WaitForCacheSync(ctx.Done(), informer.HasSynced) && ctx.Err() == nil {
		s.logger.Error("credential informer did not sync")
		return fmt.Errorf("watch credentials: cache did not sync")
	}
	<-ctx.Done()
	factory.Shutdown()
	return nil
}
