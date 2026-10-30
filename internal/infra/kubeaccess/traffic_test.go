package kubeaccess

import (
	"context"
	"slices"
	"testing"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/intstr"
	clienttesting "k8s.io/client-go/testing"
	"k8s.io/utils/ptr"

	"tiphys/internal/application"
)

func gatewayObj(kind, ns, name string, spec, status map[string]any) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": gatewayGroup + "/v1", "kind": kind,
		"metadata": map[string]any{"name": name, "namespace": ns},
		"spec":     spec,
		"status":   status,
	}}
}

func service(name string, typ corev1.ServiceType, selector map[string]string, lbIP string) *corev1.Service {
	s := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNS},
		Spec: corev1.ServiceSpec{Type: typ, Selector: selector,
			Ports: []corev1.ServicePort{{Port: 80, TargetPort: intstr.FromInt32(8080), Protocol: corev1.ProtocolTCP}}},
	}
	if lbIP != "" {
		s.Status.LoadBalancer.Ingress = []corev1.LoadBalancerIngress{{IP: lbIP}}
	}
	return s
}

func labelledPod(name string, owners []metav1.OwnerReference, app string, ready bool) *corev1.Pod {
	p := pod(name, owners)
	p.Labels = map[string]string{"app": app}
	p.Status.Phase = corev1.PodRunning
	st := corev1.ConditionFalse
	if ready {
		st = corev1.ConditionTrue
	}
	p.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: st}}
	return p
}

func TestNamespaceTrafficFollowsIngressesRoutesAndServices(t *testing.T) {
	pathType := networkingv1.PathTypePrefix
	backend := func(svc string) networkingv1.IngressBackend {
		return networkingv1.IngressBackend{Service: &networkingv1.IngressServiceBackend{Name: svc, Port: networkingv1.ServiceBackendPort{Number: 80}}}
	}
	ing := &networkingv1.Ingress{
		ObjectMeta: metav1.ObjectMeta{Name: "shop", Namespace: testNS},
		Spec: networkingv1.IngressSpec{IngressClassName: ptr.To("nginx"), Rules: []networkingv1.IngressRule{{
			Host: "shop.example.com",
			IngressRuleValue: networkingv1.IngressRuleValue{HTTP: &networkingv1.HTTPIngressRuleValue{Paths: []networkingv1.HTTPIngressPath{
				{Path: "/api", PathType: &pathType, Backend: backend("api")},
				{Path: "/old", PathType: &pathType, Backend: backend("gone")},
			}}},
		}}},
		Status: networkingv1.IngressStatus{LoadBalancer: networkingv1.IngressLoadBalancerStatus{Ingress: []networkingv1.IngressLoadBalancerIngress{{IP: "1.2.3.4"}}}},
	}
	done := labelledPod("api-done", ctrl("apps/v1", "ReplicaSet", "api-abc"), "api", false)
	done.Status.Phase = corev1.PodSucceeded
	r := newRig(t, ing,
		service("api", corev1.ServiceTypeClusterIP, map[string]string{"app": "api"}, ""),
		service("web", corev1.ServiceTypeClusterIP, map[string]string{"app": "web"}, ""),
		service("lb", corev1.ServiceTypeLoadBalancer, map[string]string{"app": "api"}, "9.9.9.9"),
		service("internal", corev1.ServiceTypeClusterIP, map[string]string{"app": "api"}, ""),
		replicaSet("api-abc", ctrl("apps/v1", "Deployment", "api")),
		labelledPod("api-1", ctrl("apps/v1", "ReplicaSet", "api-abc"), "api", true),
		labelledPod("api-2", ctrl("apps/v1", "ReplicaSet", "api-abc"), "api", false),
		done,
		labelledPod("web-0", ctrl("apps/v1", "StatefulSet", "web"), "web", true),
	)
	for _, o := range []*unstructured.Unstructured{
		gatewayObj("HTTPRoute", testNS, "web", map[string]any{
			"parentRefs": []any{map[string]any{"name": "public", "namespace": "gw", "sectionName": "https"}},
			"hostnames":  []any{"web.example.com"},
			"rules": []any{map[string]any{
				"matches":     []any{map[string]any{"path": map[string]any{"type": "PathPrefix", "value": "/"}}},
				"backendRefs": []any{map[string]any{"name": "web", "port": int64(80)}, map[string]any{"name": "remote", "namespace": "other"}},
			}},
		}, map[string]any{"parents": []any{map[string]any{
			"parentRef":  map[string]any{"name": "public"},
			"conditions": []any{map[string]any{"type": "Accepted", "status": "True", "reason": "Accepted", "message": "", "lastTransitionTime": "2026-10-20T12:00:00Z"}},
		}}}),
		gatewayObj("GRPCRoute", testNS, "rpc", map[string]any{
			"parentRefs": []any{map[string]any{"name": "internal", "namespace": "kube-system"}},
			"rules": []any{map[string]any{
				"matches":     []any{map[string]any{"method": map[string]any{"service": "shop.Api"}}},
				"backendRefs": []any{map[string]any{"name": "api"}},
			}},
		}, map[string]any{"parents": []any{map[string]any{
			"parentRef":  map[string]any{"name": "internal"},
			"conditions": []any{map[string]any{"type": "ResolvedRefs", "status": "False", "reason": "BackendNotFound", "message": "", "lastTransitionTime": "2026-10-20T12:00:00Z"}},
		}}}),
		gatewayObj("Gateway", "gw", "public", map[string]any{"gatewayClassName": "eg"},
			map[string]any{"addresses": []any{map[string]any{"value": "5.6.7.8"}}}),
	} {
		// Created through the resource: the tracker would guess "gatewaies".
		gvr := map[string]schema.GroupVersionResource{"HTTPRoute": httpRouteGVRs[0], "GRPCRoute": grpcRouteGVRs[0], "Gateway": gatewayGVRs[0]}[o.GetKind()]
		if _, err := r.dyn.Resource(gvr).Namespace(o.GetNamespace()).Create(context.Background(), o, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
	}

	g, err := r.NamespaceTraffic(context.Background(), actor, "c1", testNS)
	if err != nil {
		t.Fatal(err)
	}
	nodes := map[string]application.TrafficNode{}
	for _, n := range g.Nodes {
		nodes[n.ID] = n
	}
	edges := map[[2]string][]string{}
	for _, e := range g.Edges {
		edges[[2]string{e.From, e.To}] = e.Labels
	}
	node := func(id string) application.TrafficNode {
		t.Helper()
		n, ok := nodes[id]
		if !ok {
			t.Fatalf("no node %s in %+v", id, g.Nodes)
		}
		return n
	}
	edge := func(from, to string, labels ...string) {
		t.Helper()
		got, ok := edges[[2]string{from, to}]
		if !ok || !slices.Equal(got, labels) {
			t.Fatalf("edge %s -> %s: %v (present %v), want %v", from, to, got, ok, labels)
		}
	}

	if n := node("Ingress/shop/shop"); !slices.Equal(n.Details, []string{"class nginx"}) {
		t.Fatalf("ingress details: %v", n.Details)
	}
	edge("Address//1.2.3.4", "Ingress/shop/shop")
	edge("Ingress/shop/shop", "Service/shop/api", "shop.example.com/api")
	edge("Ingress/shop/shop", "Service/shop/gone", "shop.example.com/old")
	if n := node("Service/shop/gone"); n.Problem != "Service not found" {
		t.Fatalf("missing service: %+v", n)
	}
	if n := node("Service/shop/api"); n.Ready != 1 || n.Total != 2 || n.Problem != "" || !slices.Equal(n.Details, []string{"port 80→8080"}) {
		t.Fatalf("api service: %+v", n)
	}
	// Selected by both api and lb, the Deployment's pods are counted once.
	if n := node("Deployment/shop/api"); n.Ready != 1 || n.Total != 2 {
		t.Fatalf("deployment: %+v", n)
	}
	edge("Service/shop/api", "Deployment/shop/api")
	edge("Address//9.9.9.9", "Service/shop/lb")
	edge("Service/shop/lb", "Deployment/shop/api")
	if _, ok := nodes["Service/shop/internal"]; ok {
		t.Fatal("a ClusterIP Service no route reaches must not be shown")
	}

	if n := node("Gateway/gw/public"); !slices.Equal(n.Details, []string{"class eg", "5.6.7.8"}) {
		t.Fatalf("gateway details: %+v", n)
	}
	edge("Gateway/gw/public", "HTTPRoute/shop/web", "listener https")
	edge("HTTPRoute/shop/web", "Service/shop/web", "/")
	edge("HTTPRoute/shop/web", "Service/other/remote", "/")
	if n := node("HTTPRoute/shop/web"); n.Problem != "" || !slices.Equal(n.Details, []string{"web.example.com"}) {
		t.Fatalf("http route: %+v", n)
	}
	if n := node("Service/other/remote"); n.Total != 0 || n.Problem != "" || !slices.Equal(n.Details, []string{"in another namespace"}) {
		t.Fatalf("remote service: %+v", n)
	}
	if n := node("StatefulSet/shop/web"); n.Ready != 1 || n.Total != 1 {
		t.Fatalf("statefulset: %+v", n)
	}

	edge("Gateway/kube-system/internal", "GRPCRoute/shop/rpc")
	edge("GRPCRoute/shop/rpc", "Service/shop/api", "shop.Api/*")
	if n := node("GRPCRoute/shop/rpc"); n.Problem != "backends not resolved: BackendNotFound" {
		t.Fatalf("grpc route: %+v", n)
	}
	// The Gateway in a protected namespace is never read.
	if n := node("Gateway/kube-system/internal"); len(n.Details) != 0 {
		t.Fatalf("protected gateway: %+v", n)
	}
	for _, a := range r.dyn.Actions() {
		if a.GetNamespace() == "kube-system" {
			t.Fatalf("read in a protected namespace: %v", a)
		}
	}
}

func TestNamespaceTrafficWithoutGatewayAPI(t *testing.T) {
	r := newRig(t, service("lb", corev1.ServiceTypeLoadBalancer, map[string]string{"app": "api"}, ""))
	for _, res := range []string{"httproutes", "grpcroutes"} {
		r.dyn.PrependReactor("list", res, func(a clienttesting.Action) (bool, runtime.Object, error) {
			return true, nil, apierrors.NewNotFound(schema.GroupResource{Group: gatewayGroup, Resource: res}, "")
		})
	}
	g, err := r.NamespaceTraffic(context.Background(), actor, "c1", testNS)
	if err != nil {
		t.Fatal(err)
	}
	if len(g.Nodes) != 1 || g.Nodes[0].Problem != "load balancer address pending" {
		t.Fatalf("graph: %+v", g)
	}
}
