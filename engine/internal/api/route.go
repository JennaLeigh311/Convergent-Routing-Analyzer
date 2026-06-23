package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/domain"
	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/routing"
)

// endpointRoute and endpointCompare are the metric label values for the two
// routing endpoints, kept as constants so the counter labels never drift from a
// typo.
const (
	endpointRoute   = "route"
	endpointCompare = "compare"
)

// routeResponse is the GET /route body: the chosen path as an ordered segment_id
// list plus the routing cost in seconds and the algorithm that produced it. The
// segment_ids join to /graph's geometry on the client (§R2); cost_s is the
// routing cost the path was OPTIMIZED against, not a realized travel time (see
// routing.Route.CostS), and the field name says so.
type routeResponse struct {
	Algorithm string   `json:"algorithm"`
	Segments  []string `json:"segments"`
	CostS     float64  `json:"cost_s"`
}

// handleRoute serves GET /route?from=lat,lon&to=lat,lon[&algo=naive|reactive].
// It snaps the two endpoints to the graph and returns the single A→B route. algo
// defaults to naive (free-flow shortest path); reactive weights edges by the BPR
// cost over the shared congestion snapshot. An unreachable destination, an
// endpoint that snaps to no node, or a bad coordinate is a clean 4xx with a JSON
// error — never a panic or a NaN cost.
func (s *Server) handleRoute(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.writeError(w, endpointRoute, http.StatusMethodNotAllowed, "method not allowed: use GET")
		return
	}

	query := r.URL.Query()
	from, err := parseLatLon(query.Get("from"))
	if err != nil {
		s.writeError(w, endpointRoute, http.StatusBadRequest, "invalid from: "+err.Error())
		return
	}
	to, err := parseLatLon(query.Get("to"))
	if err != nil {
		s.writeError(w, endpointRoute, http.StatusBadRequest, "invalid to: "+err.Error())
		return
	}

	algo := query.Get("algo")
	if algo == "" {
		algo = algoNaive
	}
	router, ok := s.routerFor(algo)
	if !ok {
		s.writeError(w, endpointRoute, http.StatusBadRequest, "invalid algo: want \"naive\" or \"reactive\"")
		return
	}

	segments, costS, err := s.resolveRoute(r.Context(), router, from, to)
	if err != nil {
		// A routing failure here is an unreachable/unsnappable OD pair — a clean
		// client error. The raw err names node ids/coordinates, so it is logged but
		// NOT returned to the client (no PII/coordinate leak in the body).
		s.logger.Warn("route request failed", "endpoint", endpointRoute, "algo", algo, "err", err)
		s.writeError(w, endpointRoute, http.StatusUnprocessableEntity, "no route between the requested points")
		return
	}

	s.writeJSON(w, endpointRoute, http.StatusOK, routeResponse{
		Algorithm: algo,
		Segments:  segments,
		CostS:     costS,
	})
}

// compareResponse is the GET /compare body: the naive free-flow route and a
// congestion-aware route over the SAME OD pair, each as a routeResponse, so the
// frontend can render the divert side by side. Both are present or the request
// fails: comparing one routed and one unroutable side would be misleading.
type compareResponse struct {
	From     coordinate    `json:"from"`
	To       coordinate    `json:"to"`
	Naive    routeResponse `json:"naive"`
	Reactive routeResponse `json:"reactive"`
}

// coordinate echoes the requested endpoints back so the client can confirm what
// was compared without re-parsing its own query.
type coordinate struct {
	Lat float64 `json:"lat"`
	Lon float64 `json:"lon"`
}

// handleCompare serves GET /compare?from=lat,lon&to=lat,lon. It routes the SAME
// OD pair through naive (free-flow) and reactive (congestion-aware BPR over the
// shared snapshot) and returns both, the §6 divert demo as a single response. An
// OD pair unroutable by EITHER router is a clean 4xx (both sides must route for a
// meaningful comparison) — never a panic or a half-populated body.
func (s *Server) handleCompare(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.writeError(w, endpointCompare, http.StatusMethodNotAllowed, "method not allowed: use GET")
		return
	}

	query := r.URL.Query()
	from, err := parseLatLon(query.Get("from"))
	if err != nil {
		s.writeError(w, endpointCompare, http.StatusBadRequest, "invalid from: "+err.Error())
		return
	}
	to, err := parseLatLon(query.Get("to"))
	if err != nil {
		s.writeError(w, endpointCompare, http.StatusBadRequest, "invalid to: "+err.Error())
		return
	}

	naiveSegments, naiveCost, err := s.resolveRoute(r.Context(), s.naive, from, to)
	if err != nil {
		s.logger.Warn("compare naive route failed", "endpoint", endpointCompare, "err", err)
		s.writeError(w, endpointCompare, http.StatusUnprocessableEntity, "no route between the requested points")
		return
	}
	reactiveSegments, reactiveCost, err := s.resolveRoute(r.Context(), s.reactive, from, to)
	if err != nil {
		s.logger.Warn("compare reactive route failed", "endpoint", endpointCompare, "err", err)
		s.writeError(w, endpointCompare, http.StatusUnprocessableEntity, "no route between the requested points")
		return
	}

	s.writeJSON(w, endpointCompare, http.StatusOK, compareResponse{
		From:     coordinate{Lat: from.Lat, Lon: from.Lon},
		To:       coordinate{Lat: to.Lat, Lon: to.Lon},
		Naive:    routeResponse{Algorithm: algoNaive, Segments: naiveSegments, CostS: naiveCost},
		Reactive: routeResponse{Algorithm: algoReactive, Segments: reactiveSegments, CostS: reactiveCost},
	})
}

// algoNaive and algoReactive are the two routing.Router names /route exposes.
// They mirror the cmd/route flag vocabulary so the CLI and the API name the same
// strategies the same way.
const (
	algoNaive    = "naive"
	algoReactive = "reactive"
)

// routerFor maps an algo query value to the matching pre-built router. Only the
// two single-request strategies are exposed on /route — the four iterative
// routers are batch (Assign) strategies the /benchmark sweep exercises, not
// single-A→B endpoints. An unknown algo returns ok=false for a clean 4xx.
func (s *Server) routerFor(algo string) (router routing.Router, ok bool) {
	switch algo {
	case algoNaive:
		return s.naive, true
	case algoReactive:
		return s.reactive, true
	default:
		return nil, false
	}
}

// Coordinate-parse error sentinels: distinct so a test can assert which field was
// rejected, generic enough that the message returned to a client carries no
// coordinate values (the handler prefixes "invalid from:"/"invalid to:").
var (
	errEmptyCoordinate = errors.New("missing coordinate (want \"lat,lon\")")
	errCoordinateShape = errors.New("want \"lat,lon\" (two comma-separated decimal degrees)")
	errCoordinateLat   = errors.New("latitude is not a number")
	errCoordinateLon   = errors.New("longitude is not a number")
)

// parseLatLon parses a "lat,lon" pair of decimal degrees into a domain.LatLon.
// It is strict — exactly two comma-separated parseable floats — so a single value
// or a non-numeric token is a clean error rather than a silent snap to the wrong
// place. It mirrors cmd/route.parseLatLon so the CLI and the API reject the same
// malformed inputs identically.
func parseLatLon(text string) (domain.LatLon, error) {
	if strings.TrimSpace(text) == "" {
		return domain.LatLon{}, errEmptyCoordinate
	}
	parts := strings.Split(text, ",")
	if len(parts) != 2 {
		return domain.LatLon{}, errCoordinateShape
	}
	lat, err := strconv.ParseFloat(strings.TrimSpace(parts[0]), 64)
	if err != nil {
		return domain.LatLon{}, errCoordinateLat
	}
	lon, err := strconv.ParseFloat(strings.TrimSpace(parts[1]), 64)
	if err != nil {
		return domain.LatLon{}, errCoordinateLon
	}
	return domain.LatLon{Lat: lat, Lon: lon}, nil
}
