package api

import (
	"net/http"
	"sort"

	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/domain"
	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/graph"
)

// endpointGraph is the metric label for the geometry endpoint.
const endpointGraph = "graph"

// graphResponse is the GET /graph body: the network geometry as a GeoJSON
// FeatureCollection, the frontend's geometry source. Each feature carries its
// segment_id in properties and NOTHING about congestion — coloring is a pure
// client-side segment_id join against /congestion (§R2). Baking the current load
// into this response would couple the (cacheable, static) geometry to the
// (live, polled) congestion and break that separation, so it is deliberately
// kept out.
type graphResponse struct {
	Type     string         `json:"type"`
	Features []graphFeature `json:"features"`
}

// graphFeature is one GeoJSON LineString feature: the segment's geometry plus a
// minimal properties object carrying the segment_id join key. The geometry is the
// loader's retained LineString, axis order [lon,lat] verbatim — the same axis
// order the §2 contract and the loader use, never swapped.
type graphFeature struct {
	Type       string            `json:"type"`
	Geometry   graphGeometry     `json:"geometry"`
	Properties graphFeatureProps `json:"properties"`
}

// graphGeometry mirrors a GeoJSON LineString geometry object.
type graphGeometry struct {
	Type        string       `json:"type"`
	Coordinates [][2]float64 `json:"coordinates"`
}

// graphFeatureProps carries only the join key. It is deliberately minimal: the
// BPR attributes live on the graph for routing, and congestion lives on
// /congestion; /graph carries identity (segment_id) and geometry only.
type graphFeatureProps struct {
	SegmentID string `json:"segment_id"`
}

// handleGraph serves GET /graph: the network as a GeoJSON FeatureCollection
// keyed by segment_id. The body is built and marshaled ONCE at construction (see
// buildGraphResponse / s.graphBody) because the geometry is immutable and carries
// no congestion, so the handler just writes the cached bytes. The client colors
// by joining /congestion to these segment_ids (§R2).
func (s *Server) handleGraph(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.writeError(w, endpointGraph, http.StatusMethodNotAllowed, "method not allowed: use GET")
		return
	}
	s.writeRawJSON(w, endpointGraph, http.StatusOK, s.graphBody)
}

// buildGraphResponse assembles the GeoJSON FeatureCollection from the loader's
// geometry. Features are emitted in segment_id order so the body is deterministic
// (Go map iteration is randomized; the geometry source must be stable for client
// caching/diffing). Each feature copies the loader's LineString into a fresh
// [][2]float64 so the marshaled body never aliases the immutable retained
// geometry. Called once at construction; the result is marshaled and cached.
func buildGraphResponse(geom map[domain.SegmentID]graph.LineString) graphResponse {
	segments := make([]domain.SegmentID, 0, len(geom))
	for segment := range geom {
		segments = append(segments, segment)
	}
	sort.Slice(segments, func(i, j int) bool { return segments[i] < segments[j] })

	features := make([]graphFeature, 0, len(segments))
	for _, segment := range segments {
		line := geom[segment]
		coords := make([][2]float64, len(line))
		copy(coords, line)
		features = append(features, graphFeature{
			Type:       "Feature",
			Geometry:   graphGeometry{Type: "LineString", Coordinates: coords},
			Properties: graphFeatureProps{SegmentID: string(segment)},
		})
	}

	return graphResponse{
		Type:     "FeatureCollection",
		Features: features,
	}
}
