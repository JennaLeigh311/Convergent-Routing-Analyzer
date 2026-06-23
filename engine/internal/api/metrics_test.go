package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/metrics"
)

// TestRoutingRequestCounter asserts a handled request increments the
// routing_requests_total counter on the SAME registry the /metrics endpoint
// scrapes — the observability deliverable. It builds a server against a fresh
// registry, issues an OK and an error request, then scrapes the registry and
// asserts both outcomes are counted.
func TestRoutingRequestCounter(t *testing.T) {
	reg := metrics.NewRegistry()
	srv, err := NewDefaultServer(reg, nil)
	if err != nil {
		t.Fatalf("NewDefaultServer: %v", err)
	}

	// One OK request and one error request on /route.
	okReq := httptest.NewRequest(http.MethodGet, "/route?from="+coordNode0+"&to="+coordNode2, nil)
	srv.Routes().ServeHTTP(httptest.NewRecorder(), okReq)
	errReq := httptest.NewRequest(http.MethodGet, "/route?from=bad&to="+coordNode2, nil)
	srv.Routes().ServeHTTP(httptest.NewRecorder(), errReq)

	// Scrape the registry the counters were registered against.
	scrapeRec := httptest.NewRecorder()
	metrics.HandlerFor(reg).ServeHTTP(scrapeRec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body := scrapeRec.Body.String()

	wants := []string{
		`routing_requests_total{endpoint="route",outcome="ok"} 1`,
		`routing_requests_total{endpoint="route",outcome="error"} 1`,
	}
	for _, want := range wants {
		if !strings.Contains(body, want) {
			t.Errorf("metrics scrape missing %q; got:\n%s", want, body)
		}
	}
}
