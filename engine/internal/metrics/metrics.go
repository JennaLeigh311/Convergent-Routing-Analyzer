// Package metrics builds the routing-server's Prometheus scrape handler.
//
// Phase 0 (§R0 observability): a dedicated, non-global registry carrying only
// the standard Go runtime + process collectors. Using an explicit registry
// instead of prometheus.DefaultRegisterer keeps metrics state out of package
// globals, so the handler is unit-testable and the same registry can later be
// injected into the real request counters/histograms that Phase 1+ routing
// registers — rather than every handler reaching for a global default.
package metrics

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Handler returns the /metrics scrape handler backed by a fresh registry with
// the standard Go runtime and process collectors registered. The process
// collector emits nothing on non-Linux hosts (it needs /proc), so the Go
// collector — which is portable — is what guarantees the endpoint always
// exposes at least the go_* series.
func Handler() http.Handler {
	reg := prometheus.NewRegistry()
	reg.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)
	return promhttp.HandlerFor(reg, promhttp.HandlerOpts{Registry: reg})
}
