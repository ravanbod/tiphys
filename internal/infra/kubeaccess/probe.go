package kubeaccess

import (
	"context"
	"fmt"
	"strings"
	"time"

	"go.uber.org/zap"
	authv1 "k8s.io/api/authorization/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/rest"

	"tiphys/internal/application"
	"tiphys/internal/domain"
)

const (
	policyPrefix = "tiphys-"
	debugPolicy  = "tiphys-debug-only"
	probeTimeout = 10 * time.Second
)

var kyvernoClusterPolicies = schema.GroupVersionResource{Group: "kyverno.io", Version: "v1", Resource: "clusterpolicies"}

// privilegeCheck is one SelfSubjectAccessReview made as the connector.
type privilegeCheck struct {
	attrs     authv1.ResourceAttributes
	violation string // reported when the outcome is wrong
}

// mustAllow: the connector has to be able to impersonate the fixed identity.
var mustAllow = privilegeCheck{
	attrs:     authv1.ResourceAttributes{Verb: "impersonate", Resource: "users", Name: ImpersonatedUser},
	violation: "the credential cannot impersonate user " + ImpersonatedUser,
}

// mustDeny are the checks that expose an over-privileged credential (an
// Argo CD admin token pasted by mistake, for instance).
var mustDeny = []privilegeCheck{
	{authv1.ResourceAttributes{Verb: "get", Resource: "secrets", Namespace: "kube-system"}, "can read Secrets in kube-system"},
	{authv1.ResourceAttributes{Verb: "create", Resource: "pods"}, "can create Pods in any namespace"},
	{authv1.ResourceAttributes{Verb: "create", Resource: "pods", Namespace: "kube-system"}, "can create Pods in kube-system"},
	{authv1.ResourceAttributes{Verb: "create", Resource: "pods", Namespace: "default"}, "can create Pods in default"},
	{authv1.ResourceAttributes{Verb: "update", Resource: "pods", Subresource: "ephemeralcontainers"}, "can add ephemeral containers to Pods in any namespace"},
	{authv1.ResourceAttributes{Verb: "impersonate", Resource: "users", Name: "system:admin"}, "can impersonate user system:admin"},
	{authv1.ResourceAttributes{Verb: "impersonate", Resource: "groups", Name: "system:masters"}, "can impersonate group system:masters"},
}

// Probe checks a registered cluster: version, privilege check, admission
// mode. A completed privilege check decides whether the cluster is used.
func (r *Registry) Probe(ctx context.Context, cluster string) (application.ClusterProbe, error) {
	e, err := r.entry(cluster)
	if err != nil {
		return application.ClusterProbe{}, err
	}
	p, err := r.probeEntry(ctx, e)
	if err != nil {
		r.logger.Error("cluster probe failed", zap.String("cluster", cluster), zap.Error(err))
	}
	return p, err
}

// ProbeKubeconfig validates a pasted kubeconfig without registering it.
func (r *Registry) ProbeKubeconfig(ctx context.Context, kubeconfig []byte) (application.ClusterProbe, error) {
	cfg, err := parseKubeconfig(kubeconfig)
	if err != nil {
		return application.ClusterProbe{}, err
	}
	p, err := r.probeConfig(ctx, cfg)
	if err != nil {
		r.logger.Error("kubeconfig probe failed", zap.Error(err))
	}
	return p, err
}

// Version calls /version on a registered cluster. It works for clusters that
// are marked unusable, so health checks keep running.
func (r *Registry) Version(ctx context.Context, cluster string) (string, error) {
	e, err := r.entry(cluster)
	if err != nil {
		return "", err
	}
	c, err := r.shortClients(e.base)
	if err != nil {
		return "", err
	}
	return serverVersion(ctx, c)
}

func (r *Registry) shortClients(base *rest.Config) (*clients, error) {
	cfg := rest.CopyConfig(base)
	cfg.Timeout = probeTimeout
	return r.factory(cfg)
}

func (r *Registry) probeEntry(ctx context.Context, e *clusterEntry) (application.ClusterProbe, error) {
	p, err := r.probeConfig(ctx, e.base)
	if err == nil {
		e.privilegeOK.Store(p.PrivilegeOK)
		e.debugReady.Store(p.DebugReady)
	}
	return p, err
}

func (r *Registry) probeConfig(ctx context.Context, base *rest.Config) (application.ClusterProbe, error) {
	c, err := r.shortClients(base)
	if err != nil {
		return application.ClusterProbe{}, err
	}
	version, err := serverVersion(ctx, c)
	if err != nil {
		return application.ClusterProbe{}, err
	}
	p := application.ClusterProbe{Version: version}
	if p.Violations, err = privilegeViolations(ctx, c); err != nil {
		return application.ClusterProbe{}, err
	}
	p.PrivilegeOK = len(p.Violations) == 0
	p.AdmissionMode, p.DebugReady = detectAdmission(ctx, c)
	return p, nil
}

func serverVersion(ctx context.Context, c *clients) (string, error) {
	type result struct {
		v   string
		err error
	}
	ch := make(chan result, 1)
	go func() {
		v, err := c.kube.Discovery().ServerVersion()
		if err != nil {
			ch <- result{err: err}
			return
		}
		ch <- result{v: v.GitVersion}
	}()
	select {
	case res := <-ch:
		if res.err != nil {
			return "", fmt.Errorf("get /version: %w", res.err)
		}
		return res.v, nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

// privilegeViolations runs the access reviews as the connector (no
// impersonation) and lists everything that is wrong.
func privilegeViolations(ctx context.Context, c *clients) ([]string, error) {
	var out []string
	allowed, err := review(ctx, c, mustAllow.attrs)
	if err != nil {
		return nil, err
	}
	if !allowed {
		out = append(out, mustAllow.violation)
	}
	for _, chk := range mustDeny {
		allowed, err := review(ctx, c, chk.attrs)
		if err != nil {
			return nil, err
		}
		if allowed {
			out = append(out, chk.violation)
		}
	}
	return out, nil
}

func review(ctx context.Context, c *clients, attrs authv1.ResourceAttributes) (bool, error) {
	attrs.Version = "*"
	ssar := &authv1.SelfSubjectAccessReview{Spec: authv1.SelfSubjectAccessReviewSpec{ResourceAttributes: &attrs}}
	got, err := c.kube.AuthorizationV1().SelfSubjectAccessReviews().Create(ctx, ssar, metav1.CreateOptions{})
	if err != nil {
		return false, fmt.Errorf("access review %s %s: %w", attrs.Verb, attrs.Resource, err)
	}
	return got.Status.Allowed, nil
}

// detectAdmission looks for the chart's policies by name. Any error (the API
// is not served, RBAC forbids the read) means none. debugReady reports whether
// the debug policy is installed; it exists only as a VAP.
func detectAdmission(ctx context.Context, c *clients) (mode domain.AdmissionMode, debugReady bool) {
	if l, err := c.kube.AdmissionregistrationV1().ValidatingAdmissionPolicies().List(ctx, metav1.ListOptions{}); err == nil {
		names := make([]string, len(l.Items))
		for i, p := range l.Items {
			names[i] = p.Name
		}
		if found, debug := chartPolicies(names); found {
			return domain.AdmissionVAP, debug
		}
	}
	if l, err := c.dyn.Resource(kyvernoClusterPolicies).List(ctx, metav1.ListOptions{}); err == nil {
		for _, p := range l.Items {
			if strings.HasPrefix(p.GetName(), policyPrefix) {
				return domain.AdmissionKyverno, false
			}
		}
	}
	return domain.AdmissionNone, false
}

func chartPolicies(names []string) (found, debug bool) {
	for _, n := range names {
		found = found || strings.HasPrefix(n, policyPrefix)
		debug = debug || n == debugPolicy
	}
	return found, debug
}
