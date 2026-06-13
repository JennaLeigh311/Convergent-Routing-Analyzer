package graph

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
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

// maxArtifactBytes caps how much of the input reader the loader will read. A
// well-formed edge_attributes export is an operator-supplied startup artifact;
// 512 MiB is a generous cap for a city-scale export while still bounding a
// hostile or runaway artifact so a single load cannot exhaust memory. An input
// that hits this cap is rejected (not silently truncated-and-parsed).
const maxArtifactBytes = 512 << 20 // 512 MiB

// nodePosEpsilonDeg is the tolerance, in WGS84 degrees, within which two
// geometry endpoints that reference the SAME export node id are treated as the
// same physical position. §2 makes the geometry endpoints the durable
// node-position identity, so a well-formed export repeats EXACTLY-equal coords
// for a re-used node id; this small epsilon (1e-7 deg ≈ ~1 cm at the equator)
// only absorbs trivial float noise. Endpoints that disagree beyond it are a
// contradictory export and rejected loudly, not silently accept-first'd.
const nodePosEpsilonDeg = 1e-7

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
	return func(config *loadConfig) {
		config.hasBounds = true
		config.lonMin, config.lonMax, config.latMin, config.latMax = lonMin, lonMax, latMin, latMax
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
// The loader assumes a trusted, operator-supplied startup artifact: it bounds
// the read at maxArtifactBytes but does no JSON-depth bounding, so it is not
// safe to point directly at untrusted uploads without an additional size/depth
// bound around it.
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
func LoadEdgeAttributesGeoJSON(reader io.Reader, opts ...LoadOption) (graph *AdjacencyGraph, geom map[domain.SegmentID]LineString, err error) {
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

	// Bound the read so a hostile or runaway artifact cannot exhaust memory.
	// Read up to maxArtifactBytes+1: if we actually got that extra byte the input
	// exceeds the cap, so reject it rather than silently truncate-and-parse.
	data, err := io.ReadAll(io.LimitReader(reader, maxArtifactBytes+1))
	if err != nil {
		return nil, nil, fmt.Errorf("edge_attributes: read input: %w", err)
	}
	if int64(len(data)) > maxArtifactBytes {
		return nil, nil, fmt.Errorf("edge_attributes: artifact exceeds %d MiB limit (this loader bounds input size; raise maxArtifactBytes only for a trusted larger export)", maxArtifactBytes>>20)
	}

	var featureCollection geoFeatureCollection
	if err := json.Unmarshal(data, &featureCollection); err != nil {
		return nil, nil, fmt.Errorf("edge_attributes: invalid GeoJSON: %w", err)
	}

	// (1) Envelope: type and integer schema_version.
	if featureCollection.Type != "FeatureCollection" {
		return nil, nil, fmt.Errorf("edge_attributes: top-level type = %q, want %q", featureCollection.Type, "FeatureCollection")
	}
	if len(featureCollection.SchemaVersion) == 0 {
		return nil, nil, fmt.Errorf("edge_attributes: missing top-level schema_version (§2 requires the integer %d)", schemaVersion)
	}
	rawSV := bytes.TrimSpace(featureCollection.SchemaVersion)
	// Reject the stringified form ("1"): GeoJSON requires the integer 1, and the
	// quoted "1" is the Parquet-footer form. A quote in the raw token gives it away.
	if len(rawSV) > 0 && rawSV[0] == '"' {
		return nil, nil, fmt.Errorf("edge_attributes: schema_version is the JSON string %s, not the integer %d (the quoted form is the Parquet-footer form, not GeoJSON)", string(rawSV), schemaVersion)
	}
	var stringValue json.Number
	if err := json.Unmarshal(rawSV, &stringValue); err != nil {
		return nil, nil, fmt.Errorf("edge_attributes: schema_version %s is not a JSON number: %w", string(rawSV), err)
	}
	value, convErr := stringValue.Int64()
	if convErr != nil {
		return nil, nil, fmt.Errorf("edge_attributes: schema_version %s is not the JSON integer %d", string(rawSV), schemaVersion)
	}
	if value != schemaVersion {
		return nil, nil, fmt.Errorf("edge_attributes: schema_version = %d, this loader understands only %d", value, schemaVersion)
	}

	count := len(featureCollection.Features)
	if count == 0 {
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
	edges := make([]Edge, count)
	edgeIDSeen := make([]bool, count)
	geom = make(map[domain.SegmentID]LineString, count)
	segSeen := make(map[domain.SegmentID]bool, count)
	nodePos := make(map[int64]domain.LatLon)
	// pending records, per edge_id, the export source/target node ids so we can
	// remap them to dense NodeIDs after the full pass.
	type pending struct {
		src, dst int64
	}
	endpoints := make([]pending, count)

	for index1 := range featureCollection.Features {
		edge, lineString, srcNode, dstNode, verr := validateFeature(index1, &featureCollection.Features[index1], cfg, count)
		if verr != nil {
			return nil, nil, verr
		}
		props := featureCollection.Features[index1].Properties

		// (4) Adopt the export's edge_id: dense 0..n-1, no gaps or duplicates.
		// (The range check 0 <= edge_id < n lives in validateFeature.)
		if edgeIDSeen[props.EdgeID] {
			return nil, nil, fmt.Errorf("edge_attributes: edge_id %d is duplicated — edge_id set must be dense 0..%d with no duplicates", props.EdgeID, count-1)
		}
		edgeIDSeen[props.EdgeID] = true

		// (6) Duplicate segment_id rejected. (Also catches the interior-shape-
		// point-promoted-to-node case, which manifests as a duplicate segment_id.)
		if segSeen[props.SegmentID] {
			return nil, nil, fmt.Errorf("edge_attributes: duplicate segment_id %q", props.SegmentID)
		}
		segSeen[props.SegmentID] = true

		// Node positions come from geometry endpoints: source_node ← first
		// coordinate, target_node ← last coordinate (§4). When a node id recurs,
		// its endpoint must match the position the first edge established — §2
		// makes the geometry endpoints the durable node-position identity, so a
		// well-formed export repeats identical coords for the same id. A genuine
		// disagreement beyond nodePosEpsilonDeg is a contradictory export and is
		// rejected loudly rather than silently accept-first'd.
		first, last := lineString[0], lineString[len(lineString)-1]
		srcPos := domain.LatLon{Lat: first[1], Lon: first[0]}
		dstPos := domain.LatLon{Lat: last[1], Lon: last[0]}
		if err := recordNodePos(nodePos, srcNode, srcPos); err != nil {
			return nil, nil, err
		}
		if err := recordNodePos(nodePos, dstNode, dstPos); err != nil {
			return nil, nil, err
		}
		endpoints[props.EdgeID] = pending{src: srcNode, dst: dstNode}

		edges[props.EdgeID] = edge
		geom[props.SegmentID] = lineString
	}

	// (4) Build the dense node space. Collect the distinct export node ids, sort
	// them (deterministic, never Go-map order), and map each to a contiguous
	// in-memory NodeID 0..NodeCount-1. Then rewrite each edge's From/To through
	// the remap. Build []Node with NodeID i at index i so graph.New's dense
	// contract holds.
	exportNodeIDs := make([]int64, 0, len(nodePos))
	for identifier := range nodePos {
		exportNodeIDs = append(exportNodeIDs, identifier)
	}
	sort.Slice(exportNodeIDs, func(index2, innerIndex int) bool { return exportNodeIDs[index2] < exportNodeIDs[innerIndex] })
	remap := make(map[int64]domain.NodeID, len(exportNodeIDs))
	nodes := make([]Node, len(exportNodeIDs))
	for index3, exportID := range exportNodeIDs {
		nid := domain.NodeID(index3)
		remap[exportID] = nid
		nodes[index3] = Node{ID: nid, Pos: nodePos[exportID]}
	}
	for index4 := range edges {
		edges[index4].From = remap[endpoints[index4].src]
		edges[index4].To = remap[endpoints[index4].dst]
	}

	// No gap re-scan needed: n features whose edge_ids each pass the per-row
	// range check (0 <= edge_id < n) AND the duplicate check are forced by
	// pigeonhole to be a permutation of 0..n-1, so every slot is filled.

	graph, err = New(nodes, edges)
	if err != nil {
		return nil, nil, fmt.Errorf("edge_attributes: build graph: %w", err)
	}
	return graph, geom, nil
}

// LoadEdgeAttributesGeoJSONFile is a convenience wrapper around
// LoadEdgeAttributesGeoJSON that opens and loads the file at path. It forwards
// any LoadOptions (e.g. WithExpectedBounds) to the underlying load.
func LoadEdgeAttributesGeoJSONFile(path string, opts ...LoadOption) (*AdjacencyGraph, map[domain.SegmentID]LineString, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, nil, fmt.Errorf("edge_attributes: open %s: %w", path, err)
	}
	defer file.Close()
	return LoadEdgeAttributesGeoJSON(file, opts...)
}

// validateFeature checks every per-row §2 (and §1, via domain.ParseSegmentID)
// invariant for a single Feature and, on success, returns the in-memory Edge,
// its retained [lon,lat] LineString, and the export source/target node ids the
// caller will remap. It is pure per-row validation: it does NOT touch the
// cross-row state (edge_id/segment_id duplicate tracking, node-position
// reconciliation, dense remap) — those stay in the main loop. i and n are
// passed for the dense-edge_id range check and for messages. On ANY violation
// it returns a zero Edge/LineString, zero node ids, and a descriptive,
// invariant-specific error.
func validateFeature(index int, feature *geoFeature, cfg loadConfig, count int) (edge Edge, lineString LineString, srcNode, dstNode int64, err error) {
	props := feature.Properties

	// (2) Geometry type.
	if feature.Type != "Feature" {
		return Edge{}, nil, 0, 0, fmt.Errorf("edge_attributes: feature %d (segment_id %q): type = %q, want %q", index, props.SegmentID, feature.Type, "Feature")
	}
	if feature.Geometry.Type != "LineString" {
		return Edge{}, nil, 0, 0, fmt.Errorf("edge_attributes: segment_id %q: geometry.type = %q, want %q", props.SegmentID, feature.Geometry.Type, "LineString")
	}

	// (3a) segment_id strict §1 parse + self-consistency with osm_way_id.
	wayID, _, _, perr := domain.ParseSegmentID(props.SegmentID)
	if perr != nil {
		return Edge{}, nil, 0, 0, fmt.Errorf("edge_attributes: segment_id %q invalid under §1: %w", props.SegmentID, perr)
	}
	if wayID != props.OSMWayID {
		return Edge{}, nil, 0, 0, fmt.Errorf("edge_attributes: segment_id %q embeds osm_way_id %d but the osm_way_id property is %d — they must match (§2 self-consistency)", props.SegmentID, wayID, props.OSMWayID)
	}

	// (3b) osm_way_id positive. (ParseSegmentID already rejects a way < 1,
	// but check the property explicitly for a clear message.)
	if props.OSMWayID < 1 {
		return Edge{}, nil, 0, 0, fmt.Errorf("edge_attributes: segment_id %q: osm_way_id %d is non-positive — §2/§1 require >= 1", props.SegmentID, props.OSMWayID)
	}

	// (3c) highway_class enum.
	if !highwayClasses[props.HighwayClass] {
		return Edge{}, nil, 0, 0, fmt.Errorf("edge_attributes: segment_id %q: highway_class %q is outside the seven legal §2 enum values", props.SegmentID, props.HighwayClass)
	}

	// (3d) positive-field invariants. The four metric fields must be finite and
	// strictly positive: a non-finite +Inf (e.g. from a huge-exponent JSON number
	// like 1e400) would pass a bare > 0 check (Inf > 0 is true) and silently poison
	// the downstream BPR cost, so require finiteness explicitly. (NaN cannot reach
	// here through encoding/json, but the !IsNaN guard documents the intent.)
	if props.LanesEffective < 1 {
		return Edge{}, nil, 0, 0, fmt.Errorf("edge_attributes: segment_id %q: lanes_effective = %d, §2 requires >= 1", props.SegmentID, props.LanesEffective)
	}
	if !isFinitePositive(props.LengthM) {
		return Edge{}, nil, 0, 0, fmt.Errorf("edge_attributes: segment_id %q: length_m = %v is not a finite positive number, §2 requires a finite value > 0", props.SegmentID, props.LengthM)
	}
	if !isFinitePositive(props.MaxspeedKMH) {
		return Edge{}, nil, 0, 0, fmt.Errorf("edge_attributes: segment_id %q: maxspeed_kmh = %v is not a finite positive number, §2 requires a finite value > 0", props.SegmentID, props.MaxspeedKMH)
	}
	if !isFinitePositive(props.FreeFlowS) {
		return Edge{}, nil, 0, 0, fmt.Errorf("edge_attributes: segment_id %q: freeflow_time_s = %v is not a finite positive number, §2 requires a finite value > 0", props.SegmentID, props.FreeFlowS)
	}
	if !isFinitePositive(props.CapacityVPH) {
		return Edge{}, nil, 0, 0, fmt.Errorf("edge_attributes: segment_id %q: capacity_vph = %v is not a finite positive number, §2 requires a finite value > 0", props.SegmentID, props.CapacityVPH)
	}

	// (5) Geometry: >= 2 coords, [lon,lat] axis fidelity, preserve verbatim.
	coords := feature.Geometry.Coordinates
	if len(coords) < 2 {
		return Edge{}, nil, 0, 0, fmt.Errorf("edge_attributes: segment_id %q: LineString has %d coordinate(s), need >= 2", props.SegmentID, len(coords))
	}
	lineString = make(LineString, len(coords))
	for innerIndex, coord := range coords {
		// Exactly two ordinates: §2 pins 2D [lon,lat]. A 3D GeoJSON position
		// [lon,lat,elevation] is intentionally rejected here — the §2 contract is
		// 2D, so dropping/keeping an elevation is deliberately out of scope, not an
		// oversight.
		if len(coord) != 2 {
			return Edge{}, nil, 0, 0, fmt.Errorf("edge_attributes: segment_id %q: coordinate %d is not a [lon,lat] pair (got %d values)", props.SegmentID, innerIndex, len(coord))
		}
		lon, lat := coord[0], coord[1]
		// WGS84 hard bounds (§2 / GeoJSON x,y). A [lat,lon] swap whose values
		// exceed these (e.g. a latitude > 90 landing in the lat slot, or a
		// longitude > 180) is rejected here outright.
		if lon < -180 || lon > 180 {
			return Edge{}, nil, 0, 0, fmt.Errorf("edge_attributes: segment_id %q: coordinate %d lon %v outside [-180,180] (a [lat,lon] axis swap puts a latitude here)", props.SegmentID, innerIndex, lon)
		}
		if lat < -90 || lat > 90 {
			return Edge{}, nil, 0, 0, fmt.Errorf("edge_attributes: segment_id %q: coordinate %d lat %v outside [-90,90] (a [lat,lon] axis swap puts a longitude here)", props.SegmentID, innerIndex, lat)
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
			return Edge{}, nil, 0, 0, fmt.Errorf("edge_attributes: segment_id %q: coordinate %d [lon=%v,lat=%v] is outside the supplied expected bounds [lon %v..%v, lat %v..%v] — this indicates a [lat,lon] axis swap (§2 requires [lon,lat])", props.SegmentID, innerIndex, lon, lat, cfg.lonMin, cfg.lonMax, cfg.latMin, cfg.latMax)
		}
		lineString[innerIndex] = [2]float64{lon, lat}
	}

	// (4) Dense edge_id range. (The duplicate check stays in the main loop, which
	// owns the cross-row edgeIDSeen tracking.)
	if props.EdgeID < 0 || props.EdgeID >= int64(count) {
		return Edge{}, nil, 0, 0, fmt.Errorf("edge_attributes: segment_id %q: edge_id %d out of dense range [0,%d)", props.SegmentID, props.EdgeID, count)
	}

	// Node ids must be non-negative; positions come from geometry endpoints.
	if props.SourceNode < 0 {
		return Edge{}, nil, 0, 0, fmt.Errorf("edge_attributes: segment_id %q: source_node %d is negative", props.SegmentID, props.SourceNode)
	}
	if props.TargetNode < 0 {
		return Edge{}, nil, 0, 0, fmt.Errorf("edge_attributes: segment_id %q: target_node %d is negative", props.SegmentID, props.TargetNode)
	}

	edge = Edge{
		ID:          domain.EdgeID(props.EdgeID),
		Segment:     props.SegmentID,
		LengthM:     props.LengthM,
		FreeFlowS:   props.FreeFlowS,
		CapacityVPH: props.CapacityVPH,
	}
	return edge, lineString, props.SourceNode, props.TargetNode, nil
}

// isFinitePositive reports whether v is a finite (non-Inf, non-NaN) number
// strictly greater than zero — the guard for the four §2 metric fields that
// feed downstream BPR cost. A bare v > 0 would admit +Inf (Inf > 0 is true);
// this does not.
func isFinitePositive(value float64) bool {
	return value > 0 && !math.IsInf(value, 1) && !math.IsNaN(value)
}

// recordNodePos reconciles a node id's geometry-endpoint position against any
// position a prior edge already established for that id. §2 makes the geometry
// endpoints the durable node-position identity, so a well-formed export repeats
// EXACTLY-equal coords for a re-used node id; nodePosEpsilonDeg absorbs only
// trivial float noise. A genuine disagreement beyond it is a contradictory
// export and is rejected loudly (fail-closed) rather than silently accept-first.
func recordNodePos(nodePos map[int64]domain.LatLon, identifier int64, pos domain.LatLon) error {
	prev, found := nodePos[identifier]
	if !found {
		nodePos[identifier] = pos
		return nil
	}
	if math.Abs(prev.Lon-pos.Lon) > nodePosEpsilonDeg || math.Abs(prev.Lat-pos.Lat) > nodePosEpsilonDeg {
		return fmt.Errorf("edge_attributes: node id %d has contradictory endpoint coordinates [lon=%v,lat=%v] vs [lon=%v,lat=%v] — a re-used node id must resolve to the same geometry endpoint (§2)", identifier, prev.Lon, prev.Lat, pos.Lon, pos.Lat)
	}
	return nil
}
