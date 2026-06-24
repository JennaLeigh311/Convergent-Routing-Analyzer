package api

import (
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/congestion"
	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/congestion/source"
	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/congestion/static"
	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/cost"
	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/domain"
	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/graph"
	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/metrics"
	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/routing"
)

// toyNetworkGeoJSON is the toy edge_attributes graph compiled into the binary so
// the distroless container (which copies only the binaries, not the testdata
// tree) can load a graph with no filesystem dependency. It is a verbatim copy of
// testdata/toy_network.geojson — TestEmbeddedGraphMatchesTestdata asserts the two
// stay byte-identical so the copy cannot silently drift. The engine treats it as
// the immutable Phase-5 network until the real road network lands.
// NewDefaultServer loads from it.
//
//go:embed toy_network.geojson
var toyNetworkFS embed.FS

// toyNetworkName is the embedded graph's filename, kept in one place so the load
// and any diagnostic name it the same.
const toyNetworkName = "toy_network.geojson"

// Server is the REST surface over an immutable, already-loaded road graph. It is
// built once (NewServer / NewDefaultServer) and is safe for concurrent use by
// many goroutines: the graph and geometry are immutable after load, the
// congestion provider is only READ on the request path, and the benchmark job
// store guards its own state. The thin cmd/routing-server binary owns the
// listener and lifecycle and mounts Routes onto its mux.
type Server struct {
	graph graph.Graph
	// geom is the segment_id → geometry map the loader returned; it is the source
	// of /graph's GeoJSON and is never mutated after load.
	geom map[domain.SegmentID]graph.LineString
	// graphBody is the /graph GeoJSON marshaled ONCE at construction. The geometry
	// is immutable after load and the body carries no congestion, so it never
	// changes — the handler writes these cached bytes instead of rebuilding and
	// re-marshaling the FeatureCollection on every request.
	graphBody []byte
	// congestion is the shared, read-only congestion snapshot the reactive router
	// best-responds to and /congestion reports. Built via the shared source seam
	// (deterministic simulator, fixed seed) so the snapshot is stable across the
	// process lifetime — the server never mutates it.
	congestion congestion.CongestionProvider
	// segmentByEdge maps a dense EdgeID back to its segment_id so a per-edge load
	// vector renders as the segment-keyed /congestion view. Built once from the
	// graph at construction.
	segmentByEdge map[domain.EdgeID]domain.SegmentID

	// naive and reactive are the two routers /route and /compare select between.
	// They hold only the immutable graph (+ the shared snapshot, for reactive), so
	// a single instance of each is safe for concurrent Route calls.
	naive    routing.Router
	reactive routing.Router

	jobs *jobStore
	// sweepSlots is a counting semaphore bounding in-flight async benchmark sweeps
	// to its capacity (maxConcurrentSweeps): a POST acquires a slot before
	// launching a sweep and the goroutine releases it on completion, so distinct
	// tuples cannot spawn unbounded concurrent CPU-heavy sweeps.
	sweepSlots chan struct{}
	// sweepFn is the benchmark seam (defaultSweep = benchmark.RunSweep) tests
	// override to exercise the failure / panic / capacity paths.
	sweepFn sweepFunc
	// metrics may be nil when NewServer is built with a nil registry (test/embed
	// paths). That is intentional and safe: RoutingMetrics.Observe has a
	// nil-receiver guard, so writeJSON's s.metrics.Observe(...) is a no-op rather
	// than a panic. Keep that guard if Observe is ever refactored.
	metrics *metrics.RoutingMetrics
	logger  *slog.Logger
}

// NewDefaultServer builds a Server over the embedded toy edge_attributes graph,
// registering its routing counters against reg and logging via logger. It is the
// composition the binary uses: it loads the immutable graph ONCE from the
// embedded GeoJSON and wires the cost function, the routers, and the simulator
// congestion adapter behind the handlers. A load failure is returned (never a
// half-built server) so the binary can exit non-zero rather than serve a broken
// surface.
func NewDefaultServer(reg *prometheus.Registry, logger *slog.Logger) (*Server, error) {
	data, err := toyNetworkFS.ReadFile(toyNetworkName)
	if err != nil {
		return nil, fmt.Errorf("api: read embedded graph %q: %w", toyNetworkName, err)
	}
	roadGraph, geom, err := graph.LoadEdgeAttributesGeoJSON(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("api: load embedded graph %q: %w", toyNetworkName, err)
	}
	return NewServer(roadGraph, geom, reg, logger)
}

// NewServer builds a Server over an already-loaded graph + geometry, registering
// its routing counters against reg and logging via logger. It is the testable
// seam (the handler tests build a Server from a graph loaded out of testdata),
// while NewDefaultServer is the binary's embedded-graph composition over it.
//
// The congestion provider is the shared deterministic simulator snapshot
// (source.Build with source.SimSource, fixed seed) — the same construction the
// route CLI uses — so /congestion and the reactive router see one stable,
// reproducible per-edge load for the process lifetime. A nil logger defaults to
// a discarding logger so a caller need not supply one.
func NewServer(roadGraph graph.Graph, geom map[domain.SegmentID]graph.LineString, reg *prometheus.Registry, logger *slog.Logger) (*Server, error) {
	if roadGraph == nil {
		return nil, fmt.Errorf("api: NewServer requires a non-nil graph")
	}
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(discardWriter{}, nil))
	}

	provider, err := source.Build(roadGraph, source.Spec{Source: source.SimSource})
	if err != nil {
		return nil, fmt.Errorf("api: build congestion source: %w", err)
	}

	bpr := cost.DefaultBPR()

	// Invert the segment→edge index so a per-edge load vector renders by
	// segment_id. The index is 1:1 over the graph's edges, so the inverse is total.
	index := static.BuildSegmentEdgeIndex(roadGraph)
	segmentByEdge := make(map[domain.EdgeID]domain.SegmentID, len(index))
	for segment, edgeID := range index {
		segmentByEdge[edgeID] = segment
	}

	var routingMetrics *metrics.RoutingMetrics
	if reg != nil {
		routingMetrics = metrics.NewRoutingMetrics(reg)
	}

	// Marshal the static /graph body once: the geometry is immutable and carries
	// no congestion, so the response never changes and need not be rebuilt per
	// request. A marshal failure here is a construction-time bug, not a runtime one.
	graphBody, err := json.Marshal(buildGraphResponse(geom))
	if err != nil {
		return nil, fmt.Errorf("api: marshal graph body: %w", err)
	}

	return &Server{
		graph:         roadGraph,
		geom:          geom,
		graphBody:     graphBody,
		congestion:    provider,
		segmentByEdge: segmentByEdge,
		naive:         routing.NewNaiveRouter(roadGraph),
		reactive:      routing.NewReactiveRouter(roadGraph, bpr, provider),
		jobs:          newJobStore(maxBenchmarkJobs),
		sweepSlots:    make(chan struct{}, maxConcurrentSweeps),
		sweepFn:       defaultSweep,
		metrics:       routingMetrics,
		logger:        logger,
	}, nil
}

// Routes returns the REST handlers as a mux the binary mounts. It is the
// package's single wiring point: the binary composes this with its own
// /healthz, /readyz and /metrics rather than this package reaching for those.
// /benchmark and /benchmark/ are registered separately so the trailing-slash
// path captures the {id} sub-path while the bare path stays the POST target.
func (s *Server) Routes() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/route", s.handleRoute)
	mux.HandleFunc("/compare", s.handleCompare)
	mux.HandleFunc("/congestion", s.handleCongestion)
	mux.HandleFunc("/graph", s.handleGraph)
	mux.HandleFunc("/benchmark", s.handleBenchmark)
	mux.HandleFunc("/benchmark/", s.handleBenchmarkStatus)
	return mux
}

// errorResponse is the uniform JSON error envelope every handler emits on a
// failure, so a client parses one shape regardless of which endpoint failed.
type errorResponse struct {
	Error string `json:"error"`
}

// writeJSON marshals payload as JSON with the given status and records the
// request outcome against the endpoint counter. A marshal failure is logged and
// downgraded to a 500 — it is an internal bug (a non-serializable payload), not a
// client error. It centralizes the Content-Type, status, and metric increment so
// every handler is consistent.
func (s *Server) writeJSON(w http.ResponseWriter, endpoint string, status int, payload any) {
	body, err := json.Marshal(payload)
	if err != nil {
		s.logger.Error("encode response failed", "endpoint", endpoint, "err", err)
		s.metrics.Observe(endpoint, outcomeError)
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"internal error encoding response"}`))
		return
	}
	outcome := outcomeOK
	if status >= http.StatusBadRequest {
		outcome = outcomeError
	}
	s.metrics.Observe(endpoint, outcome)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

// writeRawJSON writes an already-marshaled JSON body at the given status and
// records the outcome against the endpoint counter. It is the cached-body path
// (the static /graph response is marshaled once at construction), so it skips the
// per-request json.Marshal that writeJSON performs while keeping the same
// Content-Type, status, and metric bookkeeping.
func (s *Server) writeRawJSON(w http.ResponseWriter, endpoint string, status int, body []byte) {
	outcome := outcomeOK
	if status >= http.StatusBadRequest {
		outcome = outcomeError
	}
	s.metrics.Observe(endpoint, outcome)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

// writeError emits the uniform JSON error envelope at status and counts the
// request as an error. message is a client-facing string; it must not leak
// internal detail or PII (handlers pass a sanitized message, not a raw err that
// might carry coordinates).
func (s *Server) writeError(w http.ResponseWriter, endpoint string, status int, message string) {
	s.writeJSON(w, endpoint, status, errorResponse{Error: message})
}

const (
	outcomeOK    = "ok"
	outcomeError = "error"
)

// resolveRoute runs router over the request and resolves the chosen edge ids to
// segment_ids, returning the ordered segment list and the routing cost. A
// zero-edge route (origin snapped to destination) is a clean empty list, not an
// error. An edge the router returned but the graph does not know is an internal
// inconsistency surfaced as an error, never a half-rendered path. The caller maps
// a routing error to a clean 4xx/5xx; resolveRoute never panics or returns NaN.
func (s *Server) resolveRoute(ctx context.Context, router routing.Router, from, to domain.LatLon) ([]string, float64, error) {
	route, err := router.Route(ctx, routing.RouteRequest{ID: "api", From: from, To: to})
	if err != nil {
		return nil, 0, err
	}
	segments := make([]string, 0, len(route.Edges))
	for _, edgeID := range route.Edges {
		edge, found := s.graph.Edge(edgeID)
		if !found {
			return nil, 0, fmt.Errorf("internal: router returned unknown edge %d", edgeID)
		}
		segments = append(segments, string(edge.Segment))
	}
	return segments, route.CostS, nil
}

// discardWriter is an io.Writer that drops everything, backing the default
// no-op logger so a nil logger is harmless.
type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }
