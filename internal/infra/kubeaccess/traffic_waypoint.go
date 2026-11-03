package kubeaccess

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
)

// Istio ambient mode: a waypoint is a Gateway of class istio-waypoint that
// Istio deploys as a Deployment and a Service; Services (or their namespace)
// opt in with the use-waypoint labels, and routes attached to a Service are
// applied by its waypoint.
const (
	waypointClass      = "istio-waypoint"
	labelUseWaypoint   = "istio.io/use-waypoint"
	labelUseWaypointNS = "istio.io/use-waypoint-namespace"
	labelWaypointFor   = "istio.io/waypoint-for"
	labelGatewayName   = "gateway.networking.k8s.io/gateway-name"
	labelGatewayClass  = "gateway.networking.k8s.io/gateway-class-name"
	labelIstioManaged  = "gateway.istio.io/managed"
	istioMeshManaged   = "istio.io-mesh-controller"
)

// waypointOf is the waypoint a Service Istio deployed for one belongs to.
func waypointOf(l map[string]string) (string, bool) {
	name := l[labelGatewayName]
	return name, name != "" && (l[labelIstioManaged] == istioMeshManaged || l[labelGatewayClass] == waypointClass)
}

// waypoints adds the namespace's waypoints, found by the Services Istio
// deployed for them, with the pods those select, and records the waypoint
// each other Service uses: its own istio.io/use-waypoint label, else its
// namespace's ("none" opts out).
func (b *trafficBuilder) waypoints(nsLabels map[string]string, svcs []corev1.Service, pods []corev1.Pod) {
	for i := range svcs {
		s := &svcs[i]
		name, ok := waypointOf(s.Labels)
		if !ok {
			continue
		}
		n := b.node("Waypoint", b.ns, name)
		if len(s.Spec.Selector) == 0 {
			continue
		}
		sel := labels.SelectorFromSet(s.Spec.Selector)
		for j := range pods {
			p := &pods[j]
			if p.DeletionTimestamp != nil || p.Status.Phase == corev1.PodSucceeded || p.Status.Phase == corev1.PodFailed ||
				!sel.Matches(labels.Set(p.Labels)) {
				continue
			}
			n.Total++
			if podReady(p) {
				n.Ready++
			}
		}
	}
	for i := range svcs {
		s := &svcs[i]
		if _, ok := waypointOf(s.Labels); ok {
			continue
		}
		name, ns := s.Labels[labelUseWaypoint], s.Labels[labelUseWaypointNS]
		if name == "" {
			name, ns = nsLabels[labelUseWaypoint], nsLabels[labelUseWaypointNS]
		}
		if name == "" || name == "none" {
			continue
		}
		if ns == "" {
			ns = b.ns
		}
		b.bound = append(b.bound, waypointBinding{svc: b.node("Service", b.ns, s.Name), waypoint: b.node("Waypoint", ns, name)})
	}
}

// connectWaypoints, after describeGateways, connects each waypoint to the
// routes attached to a Service bound to it, or to the Service itself when it
// has none. A waypoint of this namespace without running or ready pods, and
// a binding to a waypoint that takes no Service traffic, are problems.
func (b *trafficBuilder) connectWaypoints() {
	for _, w := range b.nodesOf("Waypoint") {
		switch {
		case w.Namespace != b.ns || w.Problem != "":
		case w.Total == 0:
			w.Problem = "no running pods"
		case w.Ready == 0:
			w.Problem = "no ready pods"
		}
	}
	for _, bd := range b.bound {
		w, s := bd.waypoint, bd.svc
		via := "via waypoint " + w.Name
		if w.Namespace != b.ns {
			via += " in " + w.Namespace
		}
		s.Details = append(s.Details, via)
		if f := b.waypointFor[w.ID]; f == "workload" || f == "none" {
			if s.Problem == "" {
				s.Problem = "waypoint " + w.Name + " takes no Service traffic (waypoint-for: " + f + ")"
			}
			continue
		}
		routed := false
		for _, m := range b.mesh {
			if m.svc == s {
				b.edge(w, m.route, m.label)
				s.Details = append(s.Details, "routed by "+m.route.Kind+" "+m.route.Name)
				routed = true
			}
		}
		if !routed {
			b.edge(w, s)
		}
	}
}
