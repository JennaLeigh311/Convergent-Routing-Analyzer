package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/api"
	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/metrics"
)

// newTestMux builds the full server mux the binary serves, over the embedded
// graph, so the route-table assertions exercise the real wiring (health +
// metrics + the REST surface) without binding a listener. A construction failure
// is fatal — the embedded graph must always load.
func newTestMux(t *testing.T) *http.ServeMux {
	t.Helper()
	reg := metrics.NewRegistry()
	apiServer, err := api.NewDefaultServer(reg, nil)
	if err != nil {
		t.Fatalf("NewDefaultServer: %v", err)
	}
	return newMux(reg, apiServer)
}

// TestNewMuxRoutes pins the routing-server's route table — the server's public
// contract — so a future edit to newMux that drops or mis-paths an endpoint
// fails CI instead of passing silently. It covers the health/metrics endpoints
// (owned by this binary) and that the REST surface is mounted; the per-endpoint
// behavior is tested in internal/api.
func TestNewMuxRoutes(test1 *testing.T) {
	server := httptest.NewServer(newTestMux(test1))
	defer server.Close()

	test1.Run("healthz and readyz return 200 ok", func(test2 *testing.T) {
		for _, path := range []string{"/healthz", "/readyz"} {
			resp, err := http.Get(server.URL + path)
			if err != nil {
				test2.Fatalf("GET %s: %v", path, err)
			}
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				test2.Errorf("GET %s: status = %d, want 200", path, resp.StatusCode)
			}
			if got := string(body); got != "ok" {
				test2.Errorf("GET %s: body = %q, want %q", path, got, "ok")
			}
		}
	})

	test1.Run("metrics returns prometheus exposition", func(test3 *testing.T) {
		resp, err := http.Get(server.URL + "/metrics")
		if err != nil {
			test3.Fatalf("GET /metrics: %v", err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			test3.Fatalf("GET /metrics: status = %d, want 200", resp.StatusCode)
		}
		if contentType := resp.Header.Get("Content-Type"); !strings.Contains(contentType, "text/plain") {
			test3.Errorf("GET /metrics: Content-Type = %q, want it to contain text/plain", contentType)
		}
		// go_goroutines comes from the Go runtime collector and is present on
		// every platform (unlike process_*, which needs /proc). Asserting on it
		// proves the registry + handler are actually wired, not merely 200-ing.
		if got := string(body); !strings.Contains(got, "go_goroutines") {
			test3.Errorf("GET /metrics: body missing go_goroutines; got:\n%s", got)
		}
	})

	// /metrics is registered without a trailing slash, so ServeMux matches it
	// exactly: a sub-path must 404, not fall through to the scrape handler.
	test1.Run("metrics subpath 404s (exact-match route)", func(test4 *testing.T) {
		resp, err := http.Get(server.URL + "/metrics/extra")
		if err != nil {
			test4.Fatalf("GET /metrics/extra: %v", err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			test4.Errorf("GET /metrics/extra: status = %d, want 404", resp.StatusCode)
		}
	})

	// The REST surface is mounted: each endpoint is reachable (not 404) over the
	// real mux. The detailed request/response behavior lives in internal/api; here
	// we only assert the wiring exists, so a dropped Handle call fails CI.
	test1.Run("rest surface is mounted", func(test5 *testing.T) {
		cases := []struct {
			method, path string
		}{
			{http.MethodGet, "/route?from=40.73,-73.99&to=40.74,-73.97"},
			{http.MethodGet, "/compare?from=40.73,-73.99&to=40.74,-73.97"},
			{http.MethodGet, "/congestion"},
			{http.MethodGet, "/graph"},
			{http.MethodPost, "/benchmark"},
			// A plain GET (no WebSocket upgrade headers) fails the upgrade with a 4xx,
			// not a 404 — which proves /stream is mounted on the real mux.
			{http.MethodGet, "/stream"},
		}
		for _, tc := range cases {
			req, err := http.NewRequest(tc.method, server.URL+tc.path, strings.NewReader(""))
			if err != nil {
				test5.Fatalf("%s %s: build request: %v", tc.method, tc.path, err)
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				test5.Fatalf("%s %s: %v", tc.method, tc.path, err)
			}
			resp.Body.Close()
			if resp.StatusCode == http.StatusNotFound {
				test5.Errorf("%s %s: got 404, want the endpoint mounted", tc.method, tc.path)
			}
		}
	})
}
