package graph

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"

	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/domain"
)

// loader.go is the consumer half of the frozen edge_attributes export contract
// (docs/contracts.md §2, GeoJSON serialization). It loads an `edge_attributes`
// GeoJSON FeatureCollection into an immutable *AdjacencyGraph plus a retained
// segment_id → geometry map, validating the §2 (and §1, via
// domain.ParseSegmentID) contract invariants and rejecting a malformed export
// LOUDLY, atomically, and fail-closed: ANY violation rejects the WHOLE artifact
// with a descriptive, invariant-specific error and returns a nil graph — never a
// partial subset. The capacity/free-flow derivation is the exporter's job (#15)
// and is NOT re-derived here; this loader checks shape and invariants only.

// schemaVersion is the §2 envelope schema_version this loader understands. The
// GeoJSON envelope carries it as the JSON integer 1 (not the string "1", which
// is the Parquet-footer form).
const schemaVersion = 1

// loadConfig holds the per-call, caller-supplied options for a load. It is
// populated by the functional LoadOptions before the load runs. A zero
// loadConfig (no options) means "region-agnostic": only the WGS84 hard bounds
// are enforced, so any city's WGS84-valid export loads unmodified.
type loadConfig struct {
	// hasBounds is set by WithExpectedBounds. When false, no expected-region box
	// is enforced and the loader is fully region-agnostic.
	hasBounds                      bool
	lonMin, lonMax, latMin, latMax float64
}

// LoadOption configures an individual LoadEdgeAttributesGeoJSON call via the
// functional-options pattern, so the zero-arg call keeps working unchanged.
type LoadOption func(*loadConfig)

// WithExpectedBounds supplies the dataset's expected coordinate window
// [lonMin,lonMax] × [latMin,latMax] (WGS84 degrees, x=lon, y=lat). When set, the
// loader additionally rejects any coordinate outside that box — this is how a
// uniform [lat,lon] axis transposition whose values BOTH land inside global
// WGS84 bounds (e.g. NYC [-73.99,40.73] → [40.73,-73.99]) is caught, since such
// a swap is NOT region-agnostically detectable and requires the dataset's
// region to detect.
//
// The bounds are validated when the load runs: lonMin <= lonMax with both in
// [-180,180], and latMin <= latMax with both in [-90,90]. A nonsense box is a
// programmer error and causes the load to return a descriptive error (the
// loader stays fail-closed: it returns nil graph/map rather than panicking).
//
// Omitting this option keeps the loader region-agnostic (WGS84 hard bounds
// only), preserving the data/engine decoupling — the engine is not baked to any
// one hemisphere or city.
func WithExpectedBounds(lonMin, lonMax, latMin, latMax float64) LoadOption {
	return func(c *loadConfig) {
		c.hasBounds = true
		c.lonMin, c.lonMax, c.latMin, c.latMax = lonMin, lonMax, latMin, latMax
	}
}

// highwayClasses is the exact set of seven legal §2 highway_class enum values.
// A value outside this set has no derivation rule, so the loader rejects it.
var highwayClasses = map[string]bool{
	"motorway": true, "trunk": true, "primary": true, "secondary": true,
	"tertiary": true, "residential": true, "service": true,
}

// LineString is the retained edge geometry: an ordered list of [lon, lat]
// coordinate pairs in WGS84 (EPSG:4326), drawn in the edge's travel direction
// (source_node first, target_node last). Axis order is GeoJSON x,y — index [0]
// is longitude, index [1] is latitude — never swapped. Interior coordinates are
// geometry shape points, not graph nodes (§2): they are retained here verbatim
// but never become routable nodes.
type LineString [][2]float64

// geoFeatureCollection mirrors the top-level §2 GeoJSON envelope. schema_version
// is captured as json.RawMessage so the loader can inspect the raw token and
// distinguish the JSON integer 1 from the string "1" (which would have
// surrounding quotes — the Parquet-footer form, not GeoJSON) and from a missing
// member (RawMessage stays nil when the member is absent).
type geoFeatureCollection struct {
	Type          string          `json:"type"`
	SchemaVersion json.RawMessage `json:"schema_version"`
	Features      []geoFeature    `json:"features"`
}

// geoFeature mirrors one §2 Feature: a LineString geometry plus the 11
// non-geometry contract columns under properties.
type geoFeature struct {
	Type       string      `json:"type"`
	Geometry   geoGeometry `json:"geometry"`
	Properties geoProps    `json:"properties"`
}

// geoGeometry mirrors a GeoJSON LineString geometry object.
type geoGeometry struct {
	Type        string      `json:"type"`
	Coordinates [][]float64 `json:"coordinates"`
}

// geoProps mirrors the 11 §2 contract columns in a Feature's properties. Only
// edge_id/segment_id/source_node/target_node/length_m/freeflow_time_s/
// capacity_vph are retained on the in-memory Edge; osm_way_id/highway_class/
// lanes_effective/maxspeed_kmh are validation inputs only (§2).
type geoProps struct {
	SegmentID      domain.SegmentID `json:"segment_id"`
	EdgeID         int64            `json:"edge_id"`
	SourceNode     int64            `json:"source_node"`
	TargetNode     int64            `json:"target_node"`
	OSMWayID       int64            `json:"osm_way_id"`
	HighwayClass   string           `json:"highway_class"`
	LanesEffective int64            `json:"lanes_effective"`
	LengthM        float64          `json:"length_m"`
	MaxspeedKMH    float64          `json:"maxspeed_kmh"`
	FreeFlowS      float64          `json:"freeflow_time_s"`
	CapacityVPH    float64          `json:"capacity_vph"`
}

// LoadEdgeAttributesGeoJSON loads a §2 `edge_attributes` GeoJSON
// FeatureCollection from r into an immutable *AdjacencyGraph plus a retained
// segment_id → geometry map. It adopts the export's materialized
// edge_id/source_node/target_node as the in-memory ids (it does not invent new
// numbering) and validates every §2 (and §1, via domain.ParseSegmentID)
// invariant. On ANY violation it returns a nil graph, a nil map, and a
// descriptive, invariant-specific error — it never loads a partial subset.
//
// Region policy: the loader is region-agnostic by default. Every coordinate is
// always checked against the WGS84 hard bounds (lon ∈ [-180,180], lat ∈
// [-90,90]), so any city's WGS84-valid export — Tokyo (lon +139), Sydney (lat
// -33), anywhere — loads unmodified. A uniform [lat,lon] axis transposition
// whose values BOTH stay inside WGS84 (e.g. NYC [-73.99,40.73] → [40.73,-73.99])
// is NOT region-agnostically detectable: catching it needs the dataset's
// expected region, supplied per-call via WithExpectedBounds. Baking a hemisphere
// into the engine would break the data/engine decoupling, so the region is a
// caller option, never a constant.
func LoadEdgeAttributesGeoJSON(r io.Reader, opts ...LoadOption) (g *AdjacencyGraph, geom map[domain.SegmentID]LineString, err error) {
	var cfg loadConfig
	for _, opt := range opts {
		opt(&cfg)
	}
	if cfg.hasBounds {
		if cfg.lonMin < -180 || cfg.lonMax > 180 || cfg.lonMin > cfg.lonMax {
			return nil, nil, fmt.Errorf("edge_attributes: WithExpectedBounds longitude window [%v,%v] is invalid — require -180 <= lonMin <= lonMax <= 180", cfg.lonMin, cfg.lonMax)
		}
		if cfg.latMin < -90 || cfg.latMax > 90 || cfg.latMin > cfg.latMax {
			return nil, nil, fmt.Errorf("edge_attributes: WithExpectedBounds latitude window [%v,%v] is invalid — require -90 <= latMin <= latMax <= 90", cfg.latMin, cfg.latMax)
		}
	}

	data, err := io.ReadAll(r)
	if err != nil {
		return nil, nil, fmt.Errorf("edge_attributes: read input: %w", err)
	}

	var fc geoFeatureCollection
	if err := json.Unmarshal(data, &fc); err != nil {
		return nil, nil, fmt.Errorf("edge_attributes: invalid GeoJSON: %w", err)
	}

	// (1) Envelope: type and integer schema_version.
	if fc.Type != "FeatureCollection" {
		return nil, nil, fmt.Errorf("edge_attributes: top-level type = %q, want %q", fc.Type, "FeatureCollection")
	}
	if len(fc.SchemaVersion) == 0 {
		return nil, nil, fmt.Errorf("edge_attributes: missing top-level schema_version (§2 requires the integer %d)", schemaVersion)
	}
	rawSV := bytes.TrimSpace(fc.SchemaVersion)
	// Reject the stringified form ("1"): GeoJSON requires the integer 1, and the
	// quoted "1" is the Parquet-footer form. A quote in the raw token gives it away.
	if len(rawSV) > 0 && rawSV[0] == '"' {
		return nil, nil, fmt.Errorf("edge_attributes: schema_version is the JSON string %s, not the integer %d (the quoted form is the Parquet-footer form, not GeoJSON)", string(rawSV), schemaVersion)
	}
	var sv json.Number
	if err := json.Unmarshal(rawSV, &sv); err != nil {
		return nil, nil, fmt.Errorf("edge_attributes: schema_version %s is not a JSON number: %w", string(rawSV), err)
	}
	v, convErr := sv.Int64()
	if convErr != nil {
		return nil, nil, fmt.Errorf("edge_attributes: schema_version %s is not the JSON integer %d", string(rawSV), schemaVersion)
	}
	if v != schemaVersion {
		return nil, nil, fmt.Errorf("edge_attributes: schema_version = %d, this loader understands only %d", v, schemaVersion)
	}

	n := len(fc.Features)
	if n == 0 {
		return nil, nil, fmt.Errorf("edge_attributes: FeatureCollection has no features")
	}

	// Per-edge slices placed by edge_id; node positions captured by the export's
	// node id. The export's node ids are an in-artifact identity, not a contiguous
	// 0-based index in this fixture corpus (they may be sparse, e.g. 10,11,20,…),
	// so we group rows that share a node id onto one vertex by that id and then
	// assign dense, contiguous in-memory NodeIDs (sorted by the export id, so the
	// assignment is deterministic) — graph.New requires nodes[i].ID == i. Adopting
	// the export id directly would either leave gaps graph.New rejects or force the
	// golden fixture (sparse ids) to fail to load, so we compact instead. The
	// retained identity that survives a re-export is segment_id (edges) and the
	// geometry endpoints (node position), per §2.
	edges := make([]Edge, n)
	edgeIDSeen := make([]bool, n)
	geom = make(map[domain.SegmentID]LineString, n)
	segSeen := make(map[domain.SegmentID]bool, n)
	nodePos := make(map[int64]domain.LatLon)
	// pending records, per edge_id, the export source/target node ids so we can
	// remap them to dense NodeIDs after the full pass.
	type pending struct {
		src, dst int64
	}
	endpoints := make([]pending, n)

	for i := range fc.Features {
		f := &fc.Features[i]
		p := f.Properties

		// (2) Geometry type.
		if f.Type != "Feature" {
			return nil, nil, fmt.Errorf("edge_attributes: feature %d (segment_id %q): type = %q, want %q", i, p.SegmentID, f.Type, "Feature")
		}
		if f.Geometry.Type != "LineString" {
			return nil, nil, fmt.Errorf("edge_attributes: segment_id %q: geometry.type = %q, want %q", p.SegmentID, f.Geometry.Type, "LineString")
		}

		// (3a) segment_id strict §1 parse + self-consistency with osm_way_id.
		wayID, _, _, perr := domain.ParseSegmentID(p.SegmentID)
		if perr != nil {
			return nil, nil, fmt.Errorf("edge_attributes: segment_id %q invalid under §1: %w", p.SegmentID, perr)
		}
		if wayID != p.OSMWayID {
			return nil, nil, fmt.Errorf("edge_attributes: segment_id %q embeds osm_way_id %d but the osm_way_id property is %d — they must match (§2 self-consistency)", p.SegmentID, wayID, p.OSMWayID)
		}

		// (3b) osm_way_id positive. (ParseSegmentID already rejects a way < 1,
		// but check the property explicitly for a clear message.)
		if p.OSMWayID < 1 {
			return nil, nil, fmt.Errorf("edge_attributes: segment_id %q: osm_way_id %d is non-positive — §2/§1 require >= 1", p.SegmentID, p.OSMWayID)
		}

		// (3c) highway_class enum.
		if !highwayClasses[p.HighwayClass] {
			return nil, nil, fmt.Errorf("edge_attributes: segment_id %q: highway_class %q is outside the seven legal §2 enum values", p.SegmentID, p.HighwayClass)
		}

		// (3d) positive-field invariants.
		if p.LanesEffective < 1 {
			return nil, nil, fmt.Errorf("edge_attributes: segment_id %q: lanes_effective = %d, §2 requires >= 1", p.SegmentID, p.LanesEffective)
		}
		if !(p.LengthM > 0) {
			return nil, nil, fmt.Errorf("edge_attributes: segment_id %q: length_m = %v is non-positive, §2 requires > 0", p.SegmentID, p.LengthM)
		}
		if !(p.MaxspeedKMH > 0) {
			return nil, nil, fmt.Errorf("edge_attributes: segment_id %q: maxspeed_kmh = %v is non-positive, §2 requires > 0", p.SegmentID, p.MaxspeedKMH)
		}
		if !(p.FreeFlowS > 0) {
			return nil, nil, fmt.Errorf("edge_attributes: segment_id %q: freeflow_time_s = %v is non-positive, §2 requires > 0", p.SegmentID, p.FreeFlowS)
		}
		if !(p.CapacityVPH > 0) {
			return nil, nil, fmt.Errorf("edge_attributes: segment_id %q: capacity_vph = %v is non-positive, §2 requires > 0", p.SegmentID, p.CapacityVPH)
		}

		// (5) Geometry: >= 2 coords, [lon,lat] axis fidelity, preserve verbatim.
		coords := f.Geometry.Coordinates
		if len(coords) < 2 {
			return nil, nil, fmt.Errorf("edge_attributes: segment_id %q: LineString has %d coordinate(s), need >= 2", p.SegmentID, len(coords))
		}
		ls := make(LineString, len(coords))
		for j, c := range coords {
			if len(c) != 2 {
				return nil, nil, fmt.Errorf("edge_attributes: segment_id %q: coordinate %d is not a [lon,lat] pair (got %d values)", p.SegmentID, j, len(c))
			}
			lon, lat := c[0], c[1]
			// WGS84 hard bounds (§2 / GeoJSON x,y). A [lat,lon] swap whose values
			// exceed these (e.g. a latitude > 90 landing in the lat slot, or a
			// longitude > 180) is rejected here outright.
			if lon < -180 || lon > 180 {
				return nil, nil, fmt.Errorf("edge_attributes: segment_id %q: coordinate %d lon %v outside [-180,180] (a [lat,lon] axis swap puts a latitude here)", p.SegmentID, j, lon)
			}
			if lat < -90 || lat > 90 {
				return nil, nil, fmt.Errorf("edge_attributes: segment_id %q: coordinate %d lat %v outside [-90,90] (a [lat,lon] axis swap puts a longitude here)", p.SegmentID, j, lat)
			}
			// Optional axis-swap guard. The WGS84 bounds above do NOT catch every
			// swap: a mid-latitude Western-hemisphere coordinate like [-73.99, 40.73]
			// (lon,lat) swaps to [40.73, -73.99], and BOTH a longitude of 40.73 and a
			// latitude of -73.99 are individually in-range, so the swap slips past a
			// pure range check. Such an in-range transposition is only detectable
			// against the dataset's expected region, so it is enforced ONLY when the
			// caller supplied WithExpectedBounds. Without bounds the loader stays
			// region-agnostic (any WGS84-valid city loads); with bounds, a coordinate
			// that escapes the box is flagged as a likely [lat,lon] axis swap.
			if cfg.hasBounds && (lon < cfg.lonMin || lon > cfg.lonMax || lat < cfg.latMin || lat > cfg.latMax) {
				return nil, nil, fmt.Errorf("edge_attributes: segment_id %q: coordinate %d [lon=%v,lat=%v] is outside the supplied expected bounds [lon %v..%v, lat %v..%v] — this indicates a [lat,lon] axis swap (§2 requires [lon,lat])", p.SegmentID, j, lon, lat, cfg.lonMin, cfg.lonMax, cfg.latMin, cfg.latMax)
			}
			ls[j] = [2]float64{lon, lat}
		}

		// (4) Adopt the export's edge_id: dense 0..n-1, no gaps or duplicates.
		if p.EdgeID < 0 || p.EdgeID >= int64(n) {
			return nil, nil, fmt.Errorf("edge_attributes: segment_id %q: edge_id %d out of dense range [0,%d)", p.SegmentID, p.EdgeID, n)
		}
		if edgeIDSeen[p.EdgeID] {
			return nil, nil, fmt.Errorf("edge_attributes: edge_id %d is duplicated — edge_id set must be dense 0..%d with no duplicates", p.EdgeID, n-1)
		}
		edgeIDSeen[p.EdgeID] = true

		// (6) Duplicate segment_id rejected. (Also catches the interior-shape-
		// point-promoted-to-node case, which manifests as a duplicate segment_id.)
		if segSeen[p.SegmentID] {
			return nil, nil, fmt.Errorf("edge_attributes: duplicate segment_id %q", p.SegmentID)
		}
		segSeen[p.SegmentID] = true

		// Node ids must be non-negative; positions come from geometry endpoints,
		// source_node ← first coordinate, target_node ← last coordinate (§4).
		if p.SourceNode < 0 {
			return nil, nil, fmt.Errorf("edge_attributes: segment_id %q: source_node %d is negative", p.SegmentID, p.SourceNode)
		}
		if p.TargetNode < 0 {
			return nil, nil, fmt.Errorf("edge_attributes: segment_id %q: target_node %d is negative", p.SegmentID, p.TargetNode)
		}
		first, last := ls[0], ls[len(ls)-1]
		// accept-first node position (a later edge re-using the node id keeps the
		// position established by the first edge that referenced it).
		if _, ok := nodePos[p.SourceNode]; !ok {
			nodePos[p.SourceNode] = domain.LatLon{Lat: first[1], Lon: first[0]}
		}
		if _, ok := nodePos[p.TargetNode]; !ok {
			nodePos[p.TargetNode] = domain.LatLon{Lat: last[1], Lon: last[0]}
		}
		endpoints[p.EdgeID] = pending{src: p.SourceNode, dst: p.TargetNode}

		edges[p.EdgeID] = Edge{
			ID:          domain.EdgeID(p.EdgeID),
			Segment:     p.SegmentID,
			LengthM:     p.LengthM,
			FreeFlowS:   p.FreeFlowS,
			CapacityVPH: p.CapacityVPH,
		}
		geom[p.SegmentID] = ls
	}

	// (4) Build the dense node space. Collect the distinct export node ids, sort
	// them (deterministic, never Go-map order), and map each to a contiguous
	// in-memory NodeID 0..NodeCount-1. Then rewrite each edge's From/To through
	// the remap. Build []Node with NodeID i at index i so graph.New's dense
	// contract holds.
	exportNodeIDs := make([]int64, 0, len(nodePos))
	for id := range nodePos {
		exportNodeIDs = append(exportNodeIDs, id)
	}
	sort.Slice(exportNodeIDs, func(i, j int) bool { return exportNodeIDs[i] < exportNodeIDs[j] })
	remap := make(map[int64]domain.NodeID, len(exportNodeIDs))
	nodes := make([]Node, len(exportNodeIDs))
	for i, exportID := range exportNodeIDs {
		nid := domain.NodeID(i)
		remap[exportID] = nid
		nodes[i] = Node{ID: nid, Pos: nodePos[exportID]}
	}
	for i := range edges {
		edges[i].From = remap[endpoints[i].src]
		edges[i].To = remap[endpoints[i].dst]
	}

	// edge_id density: every slot 0..n-1 must have been filled. (The per-row
	// range + duplicate checks above already guarantee a bijection when all n
	// rows land in distinct slots; this re-asserts no gap remains.)
	for id := 0; id < n; id++ {
		if !edgeIDSeen[id] {
			return nil, nil, fmt.Errorf("edge_attributes: edge_id %d is missing — edge_id set must be dense 0..%d with no gaps", id, n-1)
		}
	}

	g, err = New(nodes, edges)
	if err != nil {
		return nil, nil, fmt.Errorf("edge_attributes: build graph: %w", err)
	}
	return g, geom, nil
}

// LoadEdgeAttributesGeoJSONFile is a convenience wrapper around
// LoadEdgeAttributesGeoJSON that opens and loads the file at path. It forwards
// any LoadOptions (e.g. WithExpectedBounds) to the underlying load.
func LoadEdgeAttributesGeoJSONFile(path string, opts ...LoadOption) (*AdjacencyGraph, map[domain.SegmentID]LineString, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, fmt.Errorf("edge_attributes: open %s: %w", path, err)
	}
	defer f.Close()
	return LoadEdgeAttributesGeoJSON(f, opts...)
}

// SegmentIDs returns the segment_ids of a geometry map in deterministic
// (sorted) order, so a caller iterating geometry never depends on Go map order.
func SegmentIDs(geom map[domain.SegmentID]LineString) []domain.SegmentID {
	out := make([]domain.SegmentID, 0, len(geom))
	for id := range geom {
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
