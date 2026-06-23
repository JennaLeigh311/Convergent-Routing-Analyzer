package api

import (
	"testing"

	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/metrics"
)

// newTestServer builds a Server over the embedded toy graph with a fresh metrics
// registry and a no-op logger. It is the shared fixture for the handler tests; a
// construction failure is fatal because the embedded graph must always load.
func newTestServer(t *testing.T) *Server {
	t.Helper()
	reg := metrics.NewRegistry()
	srv, err := NewDefaultServer(reg, nil)
	if err != nil {
		t.Fatalf("NewDefaultServer: %v", err)
	}
	return srv
}

// Coordinates that snap to known toy-graph nodes (see testdata/toy_network.geojson):
// node 0 is a source with out-edges; node 5 is a sink with NO out-edges, so any
// route FROM node 5 is unreachable — the clean-error fixture.
const (
	coordNode0 = "40.73736,-73.97491" // node 0 (source)
	coordNode2 = "40.73986,-73.96991" // node 2 (reachable from 0)
	coordNode5 = "40.74186,-73.96616" // node 5 (sink: no out-edges)
)

// TestParseLatLon table-drives the coordinate parser: the strict two-float
// contract and the per-field rejections, so a malformed coordinate is a clean
// error rather than a silent mis-snap.
func TestParseLatLon(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		wantErr bool
		wantLat float64
		wantLon float64
	}{
		{name: "valid", in: "40.73,-73.99", wantLat: 40.73, wantLon: -73.99},
		{name: "valid with spaces", in: " 40.73 , -73.99 ", wantLat: 40.73, wantLon: -73.99},
		{name: "empty", in: "", wantErr: true},
		{name: "one field", in: "40.73", wantErr: true},
		{name: "three fields", in: "40.73,-73.99,1", wantErr: true},
		{name: "bad lat", in: "x,-73.99", wantErr: true},
		{name: "bad lon", in: "40.73,y", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseLatLon(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("parseLatLon(%q): want error, got %+v", tc.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseLatLon(%q): unexpected error %v", tc.in, err)
			}
			if got.Lat != tc.wantLat || got.Lon != tc.wantLon {
				t.Errorf("parseLatLon(%q) = %+v, want {%v %v}", tc.in, got, tc.wantLat, tc.wantLon)
			}
		})
	}
}

// TestCacheKey asserts the §R6 tuple keys two requests that differ only in
// omitted-vs-explicit defaults to the SAME cache entry, and distinct tuples to
// distinct keys — the property the job dedupe relies on.
func TestCacheKey(t *testing.T) {
	a := benchmarkParams{Seed: 7}
	b := benchmarkParams{Seed: 7, Alpha: 0.15, Beta: 4, CapacityScale: 1.0, RequestCount: 1000, Algorithm: "all"}
	if a.cacheKey() != b.cacheKey() {
		t.Errorf("cache keys differ for equivalent tuples:\n a=%q\n b=%q", a.cacheKey(), b.cacheKey())
	}
	c := benchmarkParams{Seed: 8}
	if a.cacheKey() == c.cacheKey() {
		t.Errorf("cache keys collide for different seeds: %q", a.cacheKey())
	}
}
