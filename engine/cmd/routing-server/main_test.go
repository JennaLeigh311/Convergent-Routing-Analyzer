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
func TestNewMuxRoutes(t *testing.T) {
	ts := httptest.NewServer(newMux())
	defer ts.Close()

	t.Run("healthz and readyz return 200 ok", func(t *testing.T) {
		for _, path := range []string{"/healthz", "/readyz"} {
			resp, err := http.Get(ts.URL + path)
			if err != nil {
				t.Fatalf("GET %s: %v", path, err)
			}
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Errorf("GET %s: status = %d, want 200", path, resp.StatusCode)
			}
			if got := string(body); got != "ok" {
				t.Errorf("GET %s: body = %q, want %q", path, got, "ok")
			}
		}
	})

	t.Run("metrics returns prometheus exposition", func(t *testing.T) {
		resp, err := http.Get(ts.URL + "/metrics")
		if err != nil {
			t.Fatalf("GET /metrics: %v", err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET /metrics: status = %d, want 200", resp.StatusCode)
		}
		if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "text/plain") {
			t.Errorf("GET /metrics: Content-Type = %q, want it to contain text/plain", ct)
		}
		// go_goroutines comes from the Go runtime collector and is present on
		// every platform (unlike process_*, which needs /proc). Asserting on it
		// proves the registry + handler are actually wired, not merely 200-ing.
		if got := string(body); !strings.Contains(got, "go_goroutines") {
			t.Errorf("GET /metrics: body missing go_goroutines; got:\n%s", got)
		}
	})

	// /metrics is registered without a trailing slash, so ServeMux matches it
	// exactly: a sub-path must 404, not fall through to the scrape handler.
	t.Run("metrics subpath 404s (exact-match route)", func(t *testing.T) {
		resp, err := http.Get(ts.URL + "/metrics/extra")
		if err != nil {
			t.Fatalf("GET /metrics/extra: %v", err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("GET /metrics/extra: status = %d, want 404", resp.StatusCode)
		}
	})
}
