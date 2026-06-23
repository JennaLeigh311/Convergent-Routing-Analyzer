package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// doRequest runs one request against the server's mux and returns the recorder.
func doRequest(t *testing.T, srv *Server, method, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.Routes().ServeHTTP(rec, req)
	return rec
}

// TestHandleRoute table-drives GET /route: a reachable OD pair on each algorithm,
// the method guard, bad coordinates, an unknown algorithm, and the unreachable OD
// pair (which must be a clean 4xx, never a panic or NaN).
func TestHandleRoute(t *testing.T) {
	srv := newTestServer(t)

	cases := []struct {
		name         string
		method       string
		target       string
		wantStatus   int
		wantAlgo     string
		wantNonEmpty bool
	}{
		{
			name:         "naive default reachable",
			method:       http.MethodGet,
			target:       "/route?from=" + coordNode0 + "&to=" + coordNode2,
			wantStatus:   http.StatusOK,
			wantAlgo:     "naive",
			wantNonEmpty: true,
		},
		{
			name:         "reactive reachable",
			method:       http.MethodGet,
			target:       "/route?algo=reactive&from=" + coordNode0 + "&to=" + coordNode2,
			wantStatus:   http.StatusOK,
			wantAlgo:     "reactive",
			wantNonEmpty: true,
		},
		{
			name:       "post not allowed",
			method:     http.MethodPost,
			target:     "/route?from=" + coordNode0 + "&to=" + coordNode2,
			wantStatus: http.StatusMethodNotAllowed,
		},
		{
			name:       "bad from",
			method:     http.MethodGet,
			target:     "/route?from=nope&to=" + coordNode2,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "missing to",
			method:     http.MethodGet,
			target:     "/route?from=" + coordNode0,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "unknown algo",
			method:     http.MethodGet,
			target:     "/route?algo=msa&from=" + coordNode0 + "&to=" + coordNode2,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "unreachable OD pair is a clean error",
			method:     http.MethodGet,
			target:     "/route?from=" + coordNode5 + "&to=" + coordNode0,
			wantStatus: http.StatusUnprocessableEntity,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := doRequest(t, srv, tc.method, tc.target, "")
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d (body: %s)", rec.Code, tc.wantStatus, rec.Body.String())
			}
			if tc.wantStatus != http.StatusOK {
				return
			}
			var resp routeResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if resp.Algorithm != tc.wantAlgo {
				t.Errorf("algorithm = %q, want %q", resp.Algorithm, tc.wantAlgo)
			}
			if tc.wantNonEmpty && len(resp.Segments) == 0 {
				t.Errorf("segments empty, want a non-empty path")
			}
			if isNaN(resp.CostS) {
				t.Errorf("cost_s is NaN")
			}
		})
	}
}

// TestHandleCompare asserts /compare returns BOTH a naive and a reactive route
// over the same OD pair, and that an unroutable side is a clean 4xx.
func TestHandleCompare(t *testing.T) {
	srv := newTestServer(t)

	t.Run("reachable returns both sides", func(t *testing.T) {
		rec := doRequest(t, srv, http.MethodGet, "/compare?from="+coordNode0+"&to="+coordNode2, "")
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
		}
		var resp compareResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if resp.Naive.Algorithm != "naive" || resp.Reactive.Algorithm != "reactive" {
			t.Errorf("algorithms = %q/%q, want naive/reactive", resp.Naive.Algorithm, resp.Reactive.Algorithm)
		}
		if len(resp.Naive.Segments) == 0 || len(resp.Reactive.Segments) == 0 {
			t.Errorf("a side has no segments: naive=%v reactive=%v", resp.Naive.Segments, resp.Reactive.Segments)
		}
	})

	t.Run("unreachable is a clean error", func(t *testing.T) {
		rec := doRequest(t, srv, http.MethodGet, "/compare?from="+coordNode5+"&to="+coordNode0, "")
		if rec.Code != http.StatusUnprocessableEntity {
			t.Fatalf("status = %d, want 422 (body: %s)", rec.Code, rec.Body.String())
		}
	})
}

// TestHandleCongestion asserts /congestion returns a segment-keyed snapshot,
// sorted and finite, and rejects a non-GET.
func TestHandleCongestion(t *testing.T) {
	srv := newTestServer(t)

	rec := doRequest(t, srv, http.MethodGet, "/congestion", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	var resp congestionResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// Sorted, finite, and keyed by segment_id (never by a numeric EdgeID).
	for i, seg := range resp.Segments {
		if seg.SegmentID == "" {
			t.Errorf("segment %d has empty segment_id", i)
		}
		if i > 0 && resp.Segments[i-1].SegmentID > seg.SegmentID {
			t.Errorf("segments not sorted by segment_id at %d", i)
		}
		if isNaN(seg.LoadVPH) || seg.LoadVPH <= 0 {
			t.Errorf("segment %s load = %v, want a positive finite load", seg.SegmentID, seg.LoadVPH)
		}
	}

	if rec := doRequest(t, srv, http.MethodPost, "/congestion", ""); rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST /congestion: status = %d, want 405", rec.Code)
	}
}

// TestHandleGraph asserts /graph emits a GeoJSON FeatureCollection keyed purely
// by segment_id with NO congestion baked in (the §R2 pure join), sorted and
// non-empty, and that each segment_id joins to the /congestion key space.
func TestHandleGraph(t *testing.T) {
	srv := newTestServer(t)

	rec := doRequest(t, srv, http.MethodGet, "/graph", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}

	// Decode into a generic map first to assert no congestion/load field leaked
	// into the geometry source (the §R2 separation).
	var generic map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &generic); err != nil {
		t.Fatalf("decode generic: %v", err)
	}
	if generic["type"] != "FeatureCollection" {
		t.Errorf("type = %v, want FeatureCollection", generic["type"])
	}
	if strings.Contains(strings.ToLower(rec.Body.String()), "load") || strings.Contains(strings.ToLower(rec.Body.String()), "congestion") {
		t.Errorf("/graph body leaks congestion; it must be a pure segment_id geometry source")
	}

	var resp graphResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode typed: %v", err)
	}
	if len(resp.Features) == 0 {
		t.Fatalf("no features")
	}
	for i, f := range resp.Features {
		if f.Type != "Feature" || f.Geometry.Type != "LineString" {
			t.Errorf("feature %d: type=%q geometry=%q", i, f.Type, f.Geometry.Type)
		}
		if f.Properties.SegmentID == "" {
			t.Errorf("feature %d: empty segment_id", i)
		}
		if len(f.Geometry.Coordinates) < 2 {
			t.Errorf("feature %d: degenerate geometry (%d points)", i, len(f.Geometry.Coordinates))
		}
		if i > 0 && resp.Features[i-1].Properties.SegmentID > f.Properties.SegmentID {
			t.Errorf("features not sorted by segment_id at %d", i)
		}
	}

	// The segment_id join is total: every segment in the graph appears in /graph,
	// and the congestion snapshot's keys are a subset of the graph's segments.
	graphSegments := make(map[string]bool, len(resp.Features))
	for _, f := range resp.Features {
		graphSegments[f.Properties.SegmentID] = true
	}
	congRec := doRequest(t, srv, http.MethodGet, "/congestion", "")
	var cong congestionResponse
	if err := json.Unmarshal(congRec.Body.Bytes(), &cong); err != nil {
		t.Fatalf("decode congestion: %v", err)
	}
	for _, seg := range cong.Segments {
		if !graphSegments[seg.SegmentID] {
			t.Errorf("congestion segment %q has no /graph geometry to join to", seg.SegmentID)
		}
	}
}

// TestBenchmarkAsyncFlow asserts POST /benchmark returns a job id immediately
// (without blocking on the sweep), the job completes, GET /benchmark/{id} returns
// the cached metrics, and a repeat POST with the same tuple returns the SAME job
// (the §R6 cache). A small request_count keeps the sweep fast.
func TestBenchmarkAsyncFlow(t *testing.T) {
	srv := newTestServer(t)

	body := `{"request_count": 4, "seed": 1}`
	rec := doRequest(t, srv, http.MethodPost, "/benchmark", body)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("POST /benchmark: status = %d, want 202 (body: %s)", rec.Code, rec.Body.String())
	}
	var start benchmarkStartResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &start); err != nil {
		t.Fatalf("decode start: %v", err)
	}
	if start.JobID == "" {
		t.Fatalf("empty job id")
	}

	// Poll until done (or fail). The job runs async, so it may briefly be running.
	var final benchmarkStatusResponse
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		statusRec := doRequest(t, srv, http.MethodGet, "/benchmark/"+start.JobID, "")
		if statusRec.Code != http.StatusOK {
			t.Fatalf("GET /benchmark/{id}: status = %d, want 200 (body: %s)", statusRec.Code, statusRec.Body.String())
		}
		if err := json.Unmarshal(statusRec.Body.Bytes(), &final); err != nil {
			t.Fatalf("decode status: %v", err)
		}
		if final.Status == statusDone || final.Status == statusFailed {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	if final.Status != statusDone {
		t.Fatalf("job did not complete: status=%q err=%q", final.Status, final.Error)
	}
	if final.Report == nil {
		t.Fatalf("done job has no report")
	}
	if len(final.Report.Cells) == 0 {
		t.Errorf("report has no cells")
	}

	// Repeat POST with the same tuple returns the SAME job id (the §R6 cache).
	rec2 := doRequest(t, srv, http.MethodPost, "/benchmark", body)
	var start2 benchmarkStartResponse
	if err := json.Unmarshal(rec2.Body.Bytes(), &start2); err != nil {
		t.Fatalf("decode start2: %v", err)
	}
	if start2.JobID != start.JobID {
		t.Errorf("repeat POST: job id = %q, want cached %q", start2.JobID, start.JobID)
	}
}

// TestBenchmarkValidation asserts the §R6 tuple is validated (a bad capacity
// scale is a clean 400), an unknown job id is a 404, and a non-POST to
// /benchmark is rejected.
func TestBenchmarkValidation(t *testing.T) {
	srv := newTestServer(t)

	cases := []struct {
		name       string
		method     string
		target     string
		body       string
		wantStatus int
	}{
		{name: "non-positive capacity scale", method: http.MethodPost, target: "/benchmark", body: `{"capacity_scale": -1}`, wantStatus: http.StatusBadRequest},
		{name: "negative request count", method: http.MethodPost, target: "/benchmark", body: `{"request_count": -5}`, wantStatus: http.StatusBadRequest},
		{name: "unknown field", method: http.MethodPost, target: "/benchmark", body: `{"nope": 1}`, wantStatus: http.StatusBadRequest},
		{name: "get on benchmark", method: http.MethodGet, target: "/benchmark", body: "", wantStatus: http.StatusMethodNotAllowed},
		{name: "unknown job id", method: http.MethodGet, target: "/benchmark/deadbeef", body: "", wantStatus: http.StatusNotFound},
		{name: "missing job id", method: http.MethodGet, target: "/benchmark/", body: "", wantStatus: http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := doRequest(t, srv, tc.method, tc.target, tc.body)
			if rec.Code != tc.wantStatus {
				t.Errorf("status = %d, want %d (body: %s)", rec.Code, tc.wantStatus, rec.Body.String())
			}
		})
	}
}

// isNaN reports whether f is NaN without importing math into the table tests'
// hot path more than once.
func isNaN(f float64) bool { return f != f }
