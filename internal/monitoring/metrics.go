// Package monitoring exposes Prometheus metrics for the portal.
package monitoring

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// ActionsTotal counts verbs by result: "allowed", "denied", "succeeded",
// "failed". Verbs are logs, exec, restart, run-job, list, and so on.
var ActionsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
	Name: "tiphys_actions_total",
	Help: "Total number of portal verbs, by verb and result.",
}, []string{"verb", "result"})

// DecisionDuration is the latency of domain.Decide.
var DecisionDuration = prometheus.NewHistogram(prometheus.HistogramOpts{
	Name:    "tiphys_decision_duration_seconds",
	Help:    "Latency of authorization decisions.",
	Buckets: []float64{.00005, .0001, .00025, .0005, .001, .0025, .005, .01, .05},
})

// OpenStreams is the number of open streams by kind (logs, exec, watch).
var OpenStreams = prometheus.NewGaugeVec(prometheus.GaugeOpts{
	Name: "tiphys_open_streams",
	Help: "Number of open log, exec and watch streams.",
}, []string{"kind"})

// ClusterReachable is 1 when the cluster answered /version recently.
var ClusterReachable = prometheus.NewGaugeVec(prometheus.GaugeOpts{
	Name: "tiphys_cluster_reachable",
	Help: "1 if the cluster is reachable, 0 otherwise.",
}, []string{"cluster"})

// ClusterPrivilegeOK is 1 when the connector credential passed the privilege check.
var ClusterPrivilegeOK = prometheus.NewGaugeVec(prometheus.GaugeOpts{
	Name: "tiphys_cluster_privilege_check_ok",
	Help: "1 if the connector credential passed the privilege check, 0 otherwise.",
}, []string{"cluster"})

// AuditSinkErrors counts failed pushes to the immutable audit copy (Loki).
var AuditSinkErrors = prometheus.NewCounter(prometheus.CounterOpts{
	Name: "tiphys_audit_sink_errors_total",
	Help: "Failed pushes of audit events to the external sink.",
})

// AuditQueueDropped counts audit events dropped from the sink queue because
// it was full.
var AuditQueueDropped = prometheus.NewCounter(prometheus.CounterOpts{
	Name: "tiphys_audit_queue_dropped_total",
	Help: "Audit events dropped from the sink queue.",
})

// HTTPRequests counts API requests by method, route and status class.
var HTTPRequests = prometheus.NewCounterVec(prometheus.CounterOpts{
	Name: "tiphys_http_requests_total",
	Help: "HTTP requests by method, route and status.",
}, []string{"method", "route", "status"})

// ArgoCacheAge is the age of the Argo CD cache in seconds.
var ArgoCacheAge = prometheus.NewGauge(prometheus.GaugeOpts{
	Name: "tiphys_argo_cache_age_seconds",
	Help: "Seconds since the last successful Argo CD refresh.",
})

func init() {
	prometheus.MustRegister(ActionsTotal, DecisionDuration, OpenStreams, ClusterReachable,
		ClusterPrivilegeOK, AuditSinkErrors, AuditQueueDropped, HTTPRequests, ArgoCacheAge)
}

// Handler serves all registered metrics in Prometheus text exposition format.
func Handler() http.Handler { return promhttp.Handler() }
