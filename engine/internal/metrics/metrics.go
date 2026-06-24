// Package metrics builds the routing-server's Prometheus scrape handler and the
// routing request counters registered against it.
//
// Phase 0 (§R0 observability): a dedicated, non-global registry carrying only
// the standard Go runtime + process collectors. Using an explicit registry
// instead of prometheus.DefaultRegisterer keeps metrics state out of package
// globals, so the handler is unit-testable and the same registry can be injected
// into the real request counters that the routing API registers — rather than
// every handler reaching for a global default.
//
// Phase 5 (issue #92): the routing API needs request counters on the SAME
// registry the /metrics endpoint scrapes. NewRegistry builds a registry with the
// runtime/process collectors; HandlerFor serves any registry; RoutingMetrics
// registers the per-endpoint request counters against a registry. The Phase-0
// Handler() is retained as a convenience that wires a fresh registry with no
// routing counters (used where only the runtime series are wanted, e.g. the
// route-table smoke test), so the existing call sites are unchanged.
package metrics

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// NewRegistry returns a fresh Prometheus registry with the standard Go runtime
// and process collectors registered. It is the one place the baseline collectors
// are wired, so both Handler (Phase 0) and the routing server (which also
// registers RoutingMetrics against it) start from the same baseline. The process
// collector emits nothing on non-Linux hosts (it needs /proc), so the Go
// collector — which is portable — is what guarantees the endpoint always exposes
// at least the go_* series.
func NewRegistry() *prometheus.Registry {
	reg := prometheus.NewRegistry()
	reg.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)
	return reg
}

// HandlerFor returns the /metrics scrape handler backed by reg. It is the seam
// the routing server uses to expose the registry it also registered its routing
// counters against, so one registry serves both the runtime series and the
// request counters.
func HandlerFor(reg *prometheus.Registry) http.Handler {
	return promhttp.HandlerFor(reg, promhttp.HandlerOpts{Registry: reg})
}

// Handler returns the /metrics scrape handler backed by a fresh registry with
// the standard Go runtime and process collectors registered. It is the Phase-0
// convenience (a self-contained registry, no routing counters); the routing
// server builds its own registry via NewRegistry so it can register
// RoutingMetrics on it.
func Handler() http.Handler {
	return HandlerFor(NewRegistry())
}

// RoutingMetrics holds the routing-server's request counters. They are
// registered once against the same registry the /metrics endpoint scrapes, so a
// scrape reports both the runtime series and the per-endpoint request totals.
//
// Requests counts every handled routing request, partitioned by the logical
// endpoint and a coarse outcome ("ok" | "error") so a dashboard can chart the
// request rate and error ratio per endpoint without one series per status code.
type RoutingMetrics struct {
	Requests *prometheus.CounterVec
}

// NewRoutingMetrics registers the routing request counters against reg and
// returns the handle the API increments. It uses MustRegister, so a duplicate
// registration (two NewRoutingMetrics on one registry) panics at startup rather
// than silently double-counting — registering the counters is a once-at-startup
// wiring step, not a per-request one.
func NewRoutingMetrics(reg *prometheus.Registry) *RoutingMetrics {
	requests := prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "routing_requests_total",
			Help: "Total routing API requests, partitioned by endpoint and outcome (ok|error).",
		},
		[]string{"endpoint", "outcome"},
	)
	reg.MustRegister(requests)
	return &RoutingMetrics{Requests: requests}
}

// Observe records one handled request for endpoint with the given outcome
// ("ok" or "error"). It is the single increment point so every handler reports
// the same two label values.
func (m *RoutingMetrics) Observe(endpoint, outcome string) {
	if m == nil {
		return
	}
	m.Requests.WithLabelValues(endpoint, outcome).Inc()
}
