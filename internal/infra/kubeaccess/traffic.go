package kubeaccess

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"

	"go.uber.org/zap"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"tiphys/internal/application"
	"tiphys/internal/domain"
)

const gatewayGroup = "gateway.networking.k8s.io"

// Gateway API resources, newest served version first. They are read through
// the dynamic client, so no Gateway API module is needed.
var (
	httpRouteGVRs = []schema.GroupVersionResource{
		{Group: gatewayGroup, Version: "v1", Resource: "httproutes"},
		{Group: gatewayGroup, Version: "v1beta1", Resource: "httproutes"},
	}
	grpcRouteGVRs = []schema.GroupVersionResource{
		{Group: gatewayGroup, Version: "v1", Resource: "grpcroutes"},
		{Group: gatewayGroup, Version: "v1alpha2", Resource: "grpcroutes"},
	}
	gatewayGVRs = []schema.GroupVersionResource{
		{Group: gatewayGroup, Version: "v1", Resource: "gateways"},
		{Group: gatewayGroup, Version: "v1beta1", Resource: "gateways"},
	}

	gatewayGK = schema.GroupKind{Group: gatewayGroup, Kind: "Gateway"}
	serviceGK = schema.GroupKind{Kind: "Service"}
)

// NamespaceTraffic builds the graph from the namespace's Ingresses, HTTPRoutes,
// GRPCRoutes and LoadBalancer/NodePort Services to the pods behind them. A
// cluster without the Gateway API CRDs has no routes. Gateways in other
// namespaces are read only for their class and addresses (never in protected
// namespaces); Services in other namespaces are shown but not followed.
// Service selectors are matched in memory, never sent as label selectors.
func (a *Access) NamespaceTraffic(ctx context.Context, actor application.KubeActor, cluster, namespace string) (application.TrafficGraph, error) {
	c, err := a.open(actor, cluster, namespace)
	if err != nil {
		return application.TrafficGraph{}, err
	}
	ctx, cancel := callCtx(ctx)
	defer cancel()

	b := newTrafficBuilder(namespace)
	ings, err := c.kube.NetworkingV1().Ingresses(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return application.TrafficGraph{}, a.fail("list ingresses", cluster, err)
	}
	for i := range ings.Items {
		b.ingress(&ings.Items[i])
	}
	httpRoutes, err := a.listGatewayAPI(ctx, c, cluster, namespace, httpRouteGVRs)
	if err != nil {
		return application.TrafficGraph{}, err
	}
	for i := range httpRoutes {
		if r, ok := decodeRoute[httpMatch](a, &httpRoutes[i]); ok {
			addRoute(b, "HTTPRoute", r)
		}
	}
	grpcRoutes, err := a.listGatewayAPI(ctx, c, cluster, namespace, grpcRouteGVRs)
	if err != nil {
		return application.TrafficGraph{}, err
	}
	for i := range grpcRoutes {
		if r, ok := decodeRoute[grpcMatch](a, &grpcRoutes[i]); ok {
			addRoute(b, "GRPCRoute", r)
		}
	}
	a.describeGateways(ctx, c, cluster, b)

	svcs, err := c.kube.CoreV1().Services(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return application.TrafficGraph{}, a.fail("list services", cluster, err)
	}
	pods, err := c.kube.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return application.TrafficGraph{}, a.fail("list pods", cluster, err)
	}
	rss, err := c.kube.AppsV1().ReplicaSets(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return application.TrafficGraph{}, a.fail("list replicasets", cluster, err)
	}
	b.services(svcs.Items, pods.Items, rss.Items)
	return b.graph(), nil
}

// listGatewayAPI lists a Gateway API resource in the namespace, trying the
// versions in order; a cluster serving none of them has no objects.
func (a *Access) listGatewayAPI(ctx context.Context, c *clients, cluster, namespace string, gvrs []schema.GroupVersionResource) ([]unstructured.Unstructured, error) {
	for _, gvr := range gvrs {
		l, err := c.dyn.Resource(gvr).Namespace(namespace).List(ctx, metav1.ListOptions{})
		switch {
		case apierrors.IsNotFound(err):
			continue
		case err != nil:
			return nil, a.fail("list "+gvr.Resource, cluster, err)
		}
		return l.Items, nil
	}
	return nil, nil
}

// describeGateways reads the Gateways the routes attach to for their class,
// addresses and Programmed condition. A Gateway that cannot be read (a
// protected namespace, a chart without the gateways rule) keeps only its name.
func (a *Access) describeGateways(ctx context.Context, c *clients, cluster string, b *trafficBuilder) {
	for _, n := range b.nodesOf("Gateway") {
		if !domain.ValidDNS1123Label(n.Namespace) || !domain.ValidDNS1123Subdomain(n.Name) || a.protected.IsProtected(n.Namespace) {
			continue
		}
		var u *unstructured.Unstructured
		var err error
		for _, gvr := range gatewayGVRs {
			u, err = c.dyn.Resource(gvr).Namespace(n.Namespace).Get(ctx, n.Name, metav1.GetOptions{})
			if !apierrors.IsNotFound(err) {
				break
			}
		}
		if apierrors.IsNotFound(err) {
			n.Problem = "Gateway not found"
			continue
		}
		if err != nil {
			_ = a.fail("get gateway", cluster, err)
			continue
		}
		var gw gateway
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, &gw); err != nil {
			a.logger.Warn("unreadable gateway", zap.String("cluster", cluster), zap.String("gateway", n.ID), zap.Error(err))
			continue
		}
		if gw.Spec.GatewayClassName != "" {
			n.Details = append(n.Details, "class "+gw.Spec.GatewayClassName)
		}
		for _, addr := range gw.Status.Addresses {
			if addr.Value != "" {
				n.Details = append(n.Details, addr.Value)
			}
		}
		if cond := condition(gw.Status.Conditions, "Programmed"); cond != nil && cond.Status == metav1.ConditionFalse {
			n.Problem = "not programmed: " + condText(cond)
		}
	}
}

// --- Gateway API objects, only the fields the graph needs ---

type gwRef struct {
	Group       *string `json:"group,omitempty"`
	Kind        *string `json:"kind,omitempty"`
	Namespace   string  `json:"namespace,omitempty"`
	Name        string  `json:"name"`
	SectionName string  `json:"sectionName,omitempty"`
	Port        *int32  `json:"port,omitempty"`
}

// groupKind is the reference's group and kind, def filling what is unset.
func (r gwRef) groupKind(def schema.GroupKind) schema.GroupKind {
	if r.Group != nil {
		def.Group = *r.Group
	}
	if r.Kind != nil {
		def.Kind = *r.Kind
	}
	return def
}

func (r gwRef) namespaceOr(def string) string {
	if r.Namespace != "" {
		return r.Namespace
	}
	return def
}

// matcher is a route match (HTTPRouteMatch or GRPCRouteMatch); its zero value
// stands for a rule without matches.
type matcher interface{ label() string }

type gwRoute[M matcher] struct {
	Metadata metav1.ObjectMeta `json:"metadata"`
	Spec     struct {
		ParentRefs []gwRef  `json:"parentRefs"`
		Hostnames  []string `json:"hostnames"`
		Rules      []struct {
			Matches     []M     `json:"matches"`
			BackendRefs []gwRef `json:"backendRefs"`
		} `json:"rules"`
	} `json:"spec"`
	Status struct {
		Parents []struct {
			ParentRef  gwRef              `json:"parentRef"`
			Conditions []metav1.Condition `json:"conditions"`
		} `json:"parents"`
	} `json:"status"`
}

type httpMatch struct {
	Path *struct {
		Value string `json:"value"`
	} `json:"path"`
	Method string `json:"method"`
}

func (m httpMatch) label() string {
	p := "/"
	if m.Path != nil && m.Path.Value != "" {
		p = m.Path.Value
	}
	if m.Method != "" {
		return m.Method + " " + p
	}
	return p
}

type grpcMatch struct {
	Method *struct {
		Service string `json:"service"`
		Method  string `json:"method"`
	} `json:"method"`
}

func (m grpcMatch) label() string {
	svc, method := "*", "*"
	if m.Method != nil {
		if m.Method.Service != "" {
			svc = m.Method.Service
		}
		if m.Method.Method != "" {
			method = m.Method.Method
		}
	}
	return svc + "/" + method
}

type gateway struct {
	Spec struct {
		GatewayClassName string `json:"gatewayClassName"`
	} `json:"spec"`
	Status struct {
		Addresses []struct {
			Value string `json:"value"`
		} `json:"addresses"`
		Conditions []metav1.Condition `json:"conditions"`
	} `json:"status"`
}

func decodeRoute[M matcher](a *Access, u *unstructured.Unstructured) (*gwRoute[M], bool) {
	r := new(gwRoute[M])
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, r); err != nil {
		a.logger.Warn("unreadable route", zap.String("kind", u.GetKind()), zap.String("namespace", u.GetNamespace()),
			zap.String("name", u.GetName()), zap.Error(err))
		return nil, false
	}
	return r, true
}

func condition(conds []metav1.Condition, typ string) *metav1.Condition {
	for i := range conds {
		if conds[i].Type == typ {
			return &conds[i]
		}
	}
	return nil
}

func condText(c *metav1.Condition) string {
	if c.Message != "" {
		return c.Message
	}
	return c.Reason
}

// --- graph building ---

type trafficBuilder struct {
	ns    string
	nodes map[string]*application.TrafficNode
	edges map[[2]string]*application.TrafficEdge
}

func newTrafficBuilder(ns string) *trafficBuilder {
	return &trafficBuilder{ns: ns, nodes: map[string]*application.TrafficNode{}, edges: map[[2]string]*application.TrafficEdge{}}
}

// node returns the node, creating it on first use.
func (b *trafficBuilder) node(kind, ns, name string) *application.TrafficNode {
	id := kind + "/" + ns + "/" + name
	n := b.nodes[id]
	if n == nil {
		n = &application.TrafficNode{ID: id, Kind: kind, Namespace: ns, Name: name}
		b.nodes[id] = n
	}
	return n
}

// nodesOf returns the nodes of a kind, sorted by ID.
func (b *trafficBuilder) nodesOf(kind string) []*application.TrafficNode {
	var out []*application.TrafficNode
	for _, n := range b.nodes {
		if n.Kind == kind {
			out = append(out, n)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// edge connects from to to, adding the labels it does not have yet.
func (b *trafficBuilder) edge(from, to *application.TrafficNode, lbls ...string) {
	k := [2]string{from.ID, to.ID}
	e := b.edges[k]
	if e == nil {
		e = &application.TrafficEdge{From: from.ID, To: to.ID}
		b.edges[k] = e
	}
	for _, l := range lbls {
		if !slices.Contains(e.Labels, l) {
			e.Labels = append(e.Labels, l)
		}
	}
}

// addresses connects a load balancer's addresses to the node they lead to.
func (b *trafficBuilder) addresses(to *application.TrafficNode, addrs []string) {
	for _, addr := range addrs {
		if addr != "" {
			b.edge(b.node("Address", "", addr), to)
		}
	}
}

func (b *trafficBuilder) ingress(ing *networkingv1.Ingress) {
	in := b.node("Ingress", b.ns, ing.Name)
	class := ing.Annotations["kubernetes.io/ingress.class"]
	if ing.Spec.IngressClassName != nil {
		class = *ing.Spec.IngressClassName
	}
	if class != "" {
		in.Details = append(in.Details, "class "+class)
	}
	if len(ing.Spec.TLS) > 0 {
		in.Details = append(in.Details, "TLS")
	}
	var addrs []string
	for _, lb := range ing.Status.LoadBalancer.Ingress {
		addrs = append(addrs, lb.IP, lb.Hostname)
	}
	b.addresses(in, addrs)
	if d := ing.Spec.DefaultBackend; d != nil && d.Service != nil {
		b.edge(in, b.node("Service", b.ns, d.Service.Name), "default backend")
	}
	for _, r := range ing.Spec.Rules {
		if r.HTTP == nil {
			continue
		}
		host := r.Host
		if host == "" {
			host = "*"
		}
		for _, p := range r.HTTP.Paths {
			if p.Backend.Service == nil {
				continue // a resource backend carries no traffic to pods
			}
			path := p.Path
			if path == "" {
				path = "/"
			}
			b.edge(in, b.node("Service", b.ns, p.Backend.Service.Name), host+path)
		}
	}
}

func addRoute[M matcher](b *trafficBuilder, kind string, r *gwRoute[M]) {
	rn := b.node(kind, b.ns, r.Metadata.Name)
	rn.Details = append(rn.Details, r.Spec.Hostnames...)
	for _, p := range r.Spec.ParentRefs {
		if p.Name == "" || p.groupKind(gatewayGK) != gatewayGK {
			continue // mesh routes attach to Services, not Gateways
		}
		var lbls []string
		if p.SectionName != "" {
			lbls = append(lbls, "listener "+p.SectionName)
		}
		if p.Port != nil {
			lbls = append(lbls, fmt.Sprintf("port %d", *p.Port))
		}
		b.edge(b.node("Gateway", p.namespaceOr(b.ns), p.Name), rn, lbls...)
	}
	for _, rule := range r.Spec.Rules {
		var lbls []string
		for _, m := range rule.Matches {
			lbls = append(lbls, m.label())
		}
		if len(lbls) == 0 {
			var m M
			lbls = append(lbls, m.label())
		}
		for _, be := range rule.BackendRefs {
			if be.Name == "" {
				continue
			}
			b.edge(rn, b.node(be.groupKind(serviceGK).Kind, be.namespaceOr(b.ns), be.Name), lbls...)
		}
	}
	for _, p := range r.Status.Parents {
		if c := condition(p.Conditions, "Accepted"); c != nil && c.Status == metav1.ConditionFalse {
			rn.Problem = "not accepted by " + p.ParentRef.Name + ": " + condText(c)
			return
		}
		if c := condition(p.Conditions, "ResolvedRefs"); c != nil && c.Status == metav1.ConditionFalse {
			rn.Problem = "backends not resolved: " + condText(c)
			return
		}
	}
}

// services adds LoadBalancer and NodePort Services as entry points, then
// fills every Service node of the namespace and connects it to the
// workloads of the running pods its selector matches.
func (b *trafficBuilder) services(svcs []corev1.Service, pods []corev1.Pod, rss []appsv1.ReplicaSet) {
	byName := map[string]*corev1.Service{}
	for i := range svcs {
		s := &svcs[i]
		byName[s.Name] = s
		if s.Spec.Type != corev1.ServiceTypeLoadBalancer && s.Spec.Type != corev1.ServiceTypeNodePort {
			continue
		}
		n := b.node("Service", b.ns, s.Name)
		var addrs []string
		for _, lb := range s.Status.LoadBalancer.Ingress {
			addrs = append(addrs, lb.IP, lb.Hostname)
		}
		b.addresses(n, addrs)
	}
	rsOwner := map[string]*metav1.OwnerReference{}
	for i := range rss {
		if o := metav1.GetControllerOf(&rss[i]); o != nil {
			rsOwner[rss[i].Name] = o
		}
	}
	counted := map[string]bool{} // pods already counted for their workload
	for _, n := range b.nodesOf("Service") {
		if n.Namespace != b.ns {
			n.Details = append(n.Details, "in another namespace")
			continue
		}
		s := byName[n.Name]
		if s == nil {
			n.Problem = "Service not found"
			continue
		}
		n.Details = append(n.Details, serviceDetails(s)...)
		if s.Spec.Type == corev1.ServiceTypeExternalName {
			continue
		}
		if len(s.Spec.Selector) == 0 {
			n.Details = append(n.Details, "no selector (endpoints managed outside Kubernetes)")
			continue
		}
		sel := labels.SelectorFromSet(s.Spec.Selector)
		for i := range pods {
			p := &pods[i]
			if p.DeletionTimestamp != nil || p.Status.Phase == corev1.PodSucceeded || p.Status.Phase == corev1.PodFailed ||
				!sel.Matches(labels.Set(p.Labels)) {
				continue
			}
			kind, name := podOwner(p, rsOwner)
			w := b.node(kind, b.ns, name)
			ready := podReady(p)
			n.Total++
			if ready {
				n.Ready++
			}
			if !counted[p.Name] {
				counted[p.Name] = true
				w.Total++
				if ready {
					w.Ready++
				}
			}
			b.edge(n, w)
		}
		switch {
		case s.Spec.Type == corev1.ServiceTypeLoadBalancer && len(s.Status.LoadBalancer.Ingress) == 0:
			n.Problem = "load balancer address pending"
		case n.Total == 0:
			n.Problem = "no running pods match the selector"
		case n.Ready == 0:
			n.Problem = "no ready pods"
		}
	}
}

func serviceDetails(s *corev1.Service) []string {
	var out []string
	switch {
	case s.Spec.Type == corev1.ServiceTypeExternalName:
		return []string{"ExternalName " + s.Spec.ExternalName}
	case s.Spec.ClusterIP == corev1.ClusterIPNone:
		out = append(out, "headless")
	case s.Spec.Type != corev1.ServiceTypeClusterIP && s.Spec.Type != "":
		out = append(out, string(s.Spec.Type))
	}
	var ports []string
	for _, p := range s.Spec.Ports {
		v := fmt.Sprint(p.Port)
		if t := p.TargetPort.String(); t != "0" && t != "" && t != v {
			v += "→" + t
		}
		if p.NodePort != 0 {
			v += fmt.Sprintf(" (node %d)", p.NodePort)
		}
		if p.Protocol != "" && p.Protocol != corev1.ProtocolTCP {
			v += "/" + string(p.Protocol)
		}
		ports = append(ports, v)
	}
	if len(ports) > 0 {
		out = append(out, "port "+strings.Join(ports, ", "))
	}
	return out
}

// podOwner is the workload a pod belongs to: the Deployment or Rollout of its
// ReplicaSet, else its controller, else the pod itself.
func podOwner(p *corev1.Pod, rsOwner map[string]*metav1.OwnerReference) (kind, name string) {
	o := metav1.GetControllerOf(p)
	if o == nil {
		return "Pod", p.Name
	}
	if refGK(o) == replicaSetGK {
		if d := rsOwner[o.Name]; d != nil {
			if gk := refGK(d); gk == workloadGK(domain.KindDeployment) || gk == workloadGK(domain.KindRollout) {
				return d.Kind, d.Name
			}
		}
	}
	return o.Kind, o.Name
}

func refGK(o *metav1.OwnerReference) schema.GroupKind {
	gv, _ := schema.ParseGroupVersion(o.APIVersion)
	return schema.GroupKind{Group: gv.Group, Kind: o.Kind}
}

func podReady(p *corev1.Pod) bool {
	for _, c := range p.Status.Conditions {
		if c.Type == corev1.PodReady {
			return c.Status == corev1.ConditionTrue
		}
	}
	return false
}

// graph returns the nodes sorted by ID and the edges by their ends.
func (b *trafficBuilder) graph() application.TrafficGraph {
	g := application.TrafficGraph{Nodes: make([]application.TrafficNode, 0, len(b.nodes)), Edges: make([]application.TrafficEdge, 0, len(b.edges))}
	for _, n := range b.nodes {
		g.Nodes = append(g.Nodes, *n)
	}
	for _, e := range b.edges {
		g.Edges = append(g.Edges, *e)
	}
	sort.Slice(g.Nodes, func(i, j int) bool { return g.Nodes[i].ID < g.Nodes[j].ID })
	sort.Slice(g.Edges, func(i, j int) bool {
		if g.Edges[i].From != g.Edges[j].From {
			return g.Edges[i].From < g.Edges[j].From
		}
		return g.Edges[i].To < g.Edges[j].To
	})
	return g
}
