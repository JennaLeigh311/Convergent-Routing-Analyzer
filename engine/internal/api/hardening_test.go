package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/metrics"
)

// TestHandleGraphETag asserts the /graph caching contract: a 200 carries a strong
// ETag and a Cache-Control header, and a conditional GET with the matching
// If-None-Match returns a 304 with an empty body (the geometry is not re-sent).
func TestHandleGraphETag(t *testing.T) {
	srv := newTestServer(t)

	// First GET: 200 + ETag + Cache-Control.
	rec := doRequest(t, srv, http.MethodGet, "/graph", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	etag := rec.Header().Get("ETag")
	if etag == "" || !strings.HasPrefix(etag, `"sha256:`) {
		t.Fatalf("ETag = %q, want a strong sha256 validator", etag)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != graphCacheControl {
		t.Errorf("Cache-Control = %q, want %q", cc, graphCacheControl)
	}

	// Conditional GET with the matching validator: 304, empty body, ETag echoed.
	req := httptest.NewRequest(http.MethodGet, "/graph", nil)
	req.Header.Set("If-None-Match", etag)
	condRec := httptest.NewRecorder()
	srv.Routes().ServeHTTP(condRec, req)
	if condRec.Code != http.StatusNotModified {
		t.Fatalf("conditional status = %d, want 304", condRec.Code)
	}
	if condRec.Body.Len() != 0 {
		t.Errorf("304 body should be empty, got %d bytes", condRec.Body.Len())
	}
	if condRec.Header().Get("ETag") != etag {
		t.Errorf("304 ETag = %q, want %q", condRec.Header().Get("ETag"), etag)
	}

	// A non-matching validator still returns the full body.
	req2 := httptest.NewRequest(http.MethodGet, "/graph", nil)
	req2.Header.Set("If-None-Match", `"sha256:stale"`)
	staleRec := httptest.NewRecorder()
	srv.Routes().ServeHTTP(staleRec, req2)
	if staleRec.Code != http.StatusOK {
		t.Errorf("stale validator status = %d, want 200 (full body)", staleRec.Code)
	}
}

// TestWriteJSONMarshalFailure exercises the writeJSON internal-error path: a
// payload that cannot be JSON-marshaled (a channel) must produce a 500 with the
// hardcoded error body and an "error"-outcome metric, never a panic.
func TestWriteJSONMarshalFailure(t *testing.T) {
	reg := metrics.NewRegistry()
	srv, err := NewDefaultServer(reg, nil)
	if err != nil {
		t.Fatalf("NewDefaultServer: %v", err)
	}

	rec := httptest.NewRecorder()
	// A channel is not JSON-serializable, so json.Marshal fails inside writeJSON.
	srv.writeJSON(rec, "route", http.StatusOK, map[string]any{"bad": make(chan int)})

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if got := strings.TrimSpace(rec.Body.String()); got != `{"error":"internal error encoding response"}` {
		t.Errorf("body = %q, want the hardcoded encode-error envelope", got)
	}

	// The failure is counted as an error outcome on the endpoint.
	scrapeRec := httptest.NewRecorder()
	metrics.HandlerFor(reg).ServeHTTP(scrapeRec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	want := `routing_requests_total{endpoint="route",outcome="error"} 1`
	if !strings.Contains(scrapeRec.Body.String(), want) {
		t.Errorf("metrics missing %q; got:\n%s", want, scrapeRec.Body.String())
	}
}

// TestBenchmarkStatusEmbeddedSlash asserts the job-id guard: a GET on a
// /benchmark/{id} where {id} contains an embedded slash (e.g. /benchmark/foo/bar)
// is a clean 400, not a 404 or a panic.
func TestBenchmarkStatusEmbeddedSlash(t *testing.T) {
	srv := newTestServer(t)

	rec := doRequest(t, srv, http.MethodGet, "/benchmark/foo/bar", "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for an embedded-slash job id (body: %s)", rec.Code, rec.Body.String())
	}
}

// TestEtagMatches exercises the If-None-Match parser directly across the branches
// the handler-level test does not reach: the "*" wildcard, a comma-separated list
// with surrounding whitespace, the "W/" weak-validator prefix, and the empty /
// no-match cases.
func TestEtagMatches(t *testing.T) {
	const etag = `"sha256:abc123"`
	cases := []struct {
		name        string
		ifNoneMatch string
		want        bool
	}{
		{"empty", "", false},
		{"wildcard", "*", true},
		{"exact", etag, true},
		{"weak prefix", "W/" + etag, true},
		{"list with whitespace", `"sha256:other", ` + etag, true},
		{"list no match", `"sha256:x", "sha256:y"`, false},
		{"other value", `"sha256:nope"`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := etagMatches(tc.ifNoneMatch, etag); got != tc.want {
				t.Errorf("etagMatches(%q, %q) = %v, want %v", tc.ifNoneMatch, etag, got, tc.want)
			}
		})
	}
}
