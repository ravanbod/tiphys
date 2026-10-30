// Package kubeaccess implements application.ClusterAccess. It is the only
// code in the portal that holds Kubernetes clients.
//
// Every call impersonates the fixed identity tiphys:user and carries the real
// portal user as impersonation extras; before every call the namespace, names,
// kinds, shells and pod owner chain are checked again, independently of the
// portal's own authorization.
package kubeaccess

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"
	"unicode"

	"go.uber.org/zap"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/client-go/rest"

	"tiphys/internal/application"
	"tiphys/internal/domain"
)

// ExtraDomain is the domain of the impersonation extras. It is a placeholder:
// change it here once, or per instance with WithExtraDomain; the chart value
// extraDomain must match.
const ExtraDomain = "portal.example.com"

const (
	ImpersonatedUser  = "tiphys:user"
	ImpersonatedGroup = "tiphys:users"

	restartAnnotation   = "kubectl.kubernetes.io/restartedAt"
	instantiateKey      = "cronjob.kubernetes.io/instantiate"
	instantiateManual   = "manual"
	triggeredByKey      = "tiphys/triggered-by"
	defaultCallTimeout  = 30 * time.Second
	maxActorNameBytes   = 256
	defaultProgressSecs = 600
)

// ProtectedNamespaces answers whether a namespace is off limits for every
// verb, reads included.
type ProtectedNamespaces interface {
	IsProtected(ns string) bool
}

// Access is the ClusterAccess adapter. The embedded Registry provides the
// cluster lifecycle methods (Registered, SetUsable, Version, Probe,
// ProbeKubeconfig).
type Access struct {
	*Registry
	protected   ProtectedNamespaces
	logger      *zap.Logger
	extraDomain string
	clock       application.Clock
	pollEvery   time.Duration
	debugImage  string
	debugWait   time.Duration
}

var _ application.ClusterAccess = (*Access)(nil)

type Option func(*Access)

// WithExtraDomain sets the domain of the impersonation extras.
func WithExtraDomain(domain string) Option { return func(a *Access) { a.extraDomain = domain } }

// WithDebugImage turns on debug containers with this image (pinned by digest;
// it must equal the chart's debug.image).
func WithDebugImage(image string) Option { return func(a *Access) { a.debugImage = image } }

// WithClock replaces the system clock.
func WithClock(c application.Clock) Option { return func(a *Access) { a.clock = c } }

func New(reg *Registry, protected ProtectedNamespaces, logger *zap.Logger, opts ...Option) *Access {
	a := &Access{
		Registry:    reg,
		protected:   protected,
		logger:      logger,
		extraDomain: ExtraDomain,
		clock:       application.SystemClock,
		pollEvery:   2 * time.Second,
		debugWait:   defaultDebugWait,
	}
	for _, o := range opts {
		o(a)
	}
	return a
}

// impersonation is the identity every call is made with. The break-glass
// extra is always sent, empty when no break-glass is active.
func (a *Access) impersonation(actor application.KubeActor) rest.ImpersonationConfig {
	bg := ""
	if actor.BreakGlassID != nil {
		bg = strconv.FormatInt(*actor.BreakGlassID, 10)
	}
	return rest.ImpersonationConfig{
		UserName: ImpersonatedUser,
		Groups:   []string{ImpersonatedGroup},
		Extra: map[string][]string{
			a.extraDomain + "/username":    {actor.Username},
			a.extraDomain + "/break-glass": {bg},
		},
	}
}

// open runs the checks every verb shares and returns clients that impersonate
// the actor. The namespace is checked before the cluster is looked up.
func (a *Access) open(actor application.KubeActor, cluster, namespace string) (*clients, error) {
	if !domain.ValidDNS1123Subdomain(cluster) {
		return nil, fmt.Errorf("%w: cluster name", application.ErrInvalidName)
	}
	if !domain.ValidDNS1123Label(namespace) {
		return nil, fmt.Errorf("%w: namespace", application.ErrInvalidName)
	}
	if a.protected.IsProtected(namespace) {
		return nil, application.ErrProtectedNamespace
	}
	return a.openCluster(actor, cluster)
}

// openCluster is the part of open that does not involve a namespace: it
// validates the cluster name and the actor, looks the cluster up (refusing
// unusable ones) and returns clients that impersonate the actor.
func (a *Access) openCluster(actor application.KubeActor, cluster string) (*clients, error) {
	if !domain.ValidDNS1123Subdomain(cluster) {
		return nil, fmt.Errorf("%w: cluster name", application.ErrInvalidName)
	}
	if err := validActor(actor); err != nil {
		return nil, err
	}
	e, err := a.lookup(cluster)
	if err != nil {
		return nil, err
	}
	cfg := rest.CopyConfig(e.base)
	cfg.Impersonate = a.impersonation(actor)
	c, err := a.factory(cfg)
	if err != nil {
		return nil, a.fail("build clients", cluster, err)
	}
	return c, nil
}

func validActor(actor application.KubeActor) error {
	u := actor.Username
	if u == "" || len(u) > maxActorNameBytes {
		return fmt.Errorf("%w: actor username", application.ErrInvalidName)
	}
	for _, r := range u {
		if unicode.IsControl(r) {
			return fmt.Errorf("%w: actor username", application.ErrInvalidName)
		}
	}
	return nil
}

func validWorkload(ref domain.WorkloadRef) error {
	if !ref.Kind.Valid() {
		return fmt.Errorf("%w: workload kind %q", application.ErrInvalidName, ref.Kind)
	}
	if !domain.ValidDNS1123Subdomain(ref.Name) {
		return fmt.Errorf("%w: workload name", application.ErrInvalidName)
	}
	return nil
}

func validObjectName(kind, name string) error {
	if !domain.ValidDNS1123Subdomain(name) {
		return fmt.Errorf("%w: %s", application.ErrInvalidName, kind)
	}
	return nil
}

// openWorkload is open plus validation of the workload reference.
func (a *Access) openWorkload(actor application.KubeActor, w application.KubeWorkloadTarget) (*clients, error) {
	if err := validWorkload(w.Workload); err != nil {
		return nil, err
	}
	return a.open(actor, w.Cluster, w.Namespace)
}

func callCtx(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, defaultCallTimeout)
}

// fail logs unexpected API failures once, where they occur, and returns the
// error wrapped. Outcomes that are business results (not found, forbidden,
// cancellation) are returned without an error log line.
func (a *Access) fail(op, cluster string, err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded),
		apierrors.IsNotFound(err), apierrors.IsForbidden(err), apierrors.IsAlreadyExists(err), apierrors.IsConflict(err):
		a.logger.Debug("kubernetes call refused", zap.String("op", op), zap.String("cluster", cluster), zap.Error(err))
	default:
		a.logger.Error("kubernetes call failed", zap.String("op", op), zap.String("cluster", cluster), zap.Error(err))
	}
	if apierrors.IsNotFound(err) {
		return fmt.Errorf("%s: %w (%v)", op, application.ErrNotFound, err)
	}
	return fmt.Errorf("%s: %w", op, err)
}
