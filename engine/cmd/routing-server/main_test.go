package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestNewMuxRoutes pins the routing-server's Phase-0 route table — the server's
// entire public contract this phase — so a future edit to newMux that drops or
// mis-paths an endpoint fails CI instead of passing silently. /metrics is a
// one-line wiring of a library handler whose only real risk IS the wiring, and
// the health endpoints are exercised nowhere else.
func TestNewMuxRoutes(test1 *testing.T) {
	server := httptest.NewServer(newMux())
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
}
