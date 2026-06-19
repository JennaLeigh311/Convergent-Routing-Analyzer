package graph_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/domain"
	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/graph"
)

// fixtureDir is the shared, language-neutral edge_attributes fixture directory,
// the same path the package-domain conformance tests (#15/#22) use.
const fixtureDir = "../../../docs/fixtures/edge_attributes"

func goldenBytes(test *testing.T) []byte {
	test.Helper()
	data, err := os.ReadFile(filepath.Join(fixtureDir, "example_export.geojson"))
	if err != nil {
		test.Fatalf("read golden fixture: %v", err)
	}
	return data
}

// TestGoldenLoads asserts the golden fixture loads cleanly with the expected
// edge/node counts, the adopted in-memory EdgeID == the export edge_id, a
// segment_id↔EdgeID round-trip for every edge, and dense nodes.
func TestGoldenLoads(test *testing.T) {
	roadGraph, geom, err := graph.LoadEdgeAttributesGeoJSON(bytes.NewReader(goldenBytes(test)))
	if err != nil {
		test.Fatalf("golden fixture must load cleanly, got: %v", err)
	}

	const wantEdges = 12
	if roadGraph.EdgeCount() != wantEdges {
		test.Errorf("EdgeCount = %d, want %d", roadGraph.EdgeCount(), wantEdges)
	}
	if len(geom) != wantEdges {
		test.Errorf("geometry map has %d entries, want %d", len(geom), wantEdges)
	}

	// edge_id from the export, in fixture order, with its segment_id.
	wantSeg := map[domain.EdgeID]domain.SegmentID{
		0: "27583001:0:F", 1: "48800123:0:F", 2: "48800123:0:R",
		3: "905512:0:F", 4: "905512:1:F", 5: "9876543:0:F",
		6: "33112200:0:F", 7: "71200:0:F", 8: "60500777:0:F",
		9: "5051000:0:F", 10: "8123456:0:F", 11: "8123456:0:R",
	}
	for edgeID1, seg := range wantSeg {
		edge1, found1 := roadGraph.Edge(edgeID1)
		if !found1 {
			test.Errorf("edge id %d missing", edgeID1)
			continue
		}
		if edge1.ID != edgeID1 {
			test.Errorf("edge at id %d has ID %d (in-memory EdgeID must equal export edge_id)", edgeID1, edge1.ID)
		}
		if edge1.Segment != seg {
			test.Errorf("edge id %d segment = %q, want %q", edgeID1, edge1.Segment, seg)
		}
	}

	// segment_id ↔ EdgeID round-trips for every edge.
	for edgeID2 := domain.EdgeID(0); int(edgeID2) < roadGraph.EdgeCount(); edgeID2++ {
		edge2, _ := roadGraph.Edge(edgeID2)
		// reverse lookup via edges[id].Segment should round-trip
		if _, found2 := geom[edge2.Segment]; !found2 {
			test.Errorf("edge id %d segment %q missing from geometry map", edgeID2, edge2.Segment)
		}
	}

	// Nodes dense & compact: the fixture references 18 distinct export node ids
	// (10..12,20..22,30,31,40,41,50,51,60,61,70,71,80,81); the loader compacts
	// them to a dense 0..17 in-memory NodeID space.
	const wantNodes = 18
	if roadGraph.NodeCount() != wantNodes {
		test.Errorf("NodeCount = %d, want %d", roadGraph.NodeCount(), wantNodes)
	}
	for nodeID := domain.NodeID(0); int(nodeID) < roadGraph.NodeCount(); nodeID++ {
		node, found3 := roadGraph.Node(nodeID)
		if !found3 {
			test.Errorf("node id %d missing — nodes must be dense", nodeID)
		}
		if node.ID != nodeID {
			test.Errorf("node at id %d has ID %d", nodeID, node.ID)
		}
	}
}

// TestGeometryFidelity round-trips a known coordinate (no axis swap) and proves
// the 3-vertex LineString retains its interior shape point WITHOUT promoting it
// to a node.
func TestGeometryFidelity(test *testing.T) {
	roadGraph, geom, err := graph.LoadEdgeAttributesGeoJSON(bytes.NewReader(goldenBytes(test)))
	if err != nil {
		test.Fatalf("load: %v", err)
	}

	// edge_id 0, segment 27583001:0:F: [[-73.99012,40.73456],[-73.98801,40.73502]]
	want := graph.LineString{{-73.99012, 40.73456}, {-73.98801, 40.73502}}
	got := geom["27583001:0:F"]
	if !reflect.DeepEqual(got, want) {
		test.Errorf("geometry for 27583001:0:F = %v, want %v ([lon,lat], no swap)", got, want)
	}

	// 3-vertex LineString, segment 33112200:0:F. Interior coord must be retained.
	threeVtx := geom["33112200:0:F"]
	if len(threeVtx) != 3 {
		test.Fatalf("33112200:0:F geometry has %d coords, want 3 (interior shape point retained)", len(threeVtx))
	}
	wantInterior := [2]float64{-73.93714, 40.75646}
	if threeVtx[1] != wantInterior {
		test.Errorf("33112200:0:F interior coord = %v, want %v", threeVtx[1], wantInterior)
	}

	// The interior shape point must NOT have become a node. Its endpoints are
	// node ids 40 (source) and 41 (target); the interior coord is neither. The
	// total NodeCount already excludes it; assert no node sits at the interior
	// position.
	for nodeID := domain.NodeID(0); int(nodeID) < roadGraph.NodeCount(); nodeID++ {
		node, _ := roadGraph.Node(nodeID)
		if node.Pos.Lon == wantInterior[0] && node.Pos.Lat == wantInterior[1] {
			test.Errorf("interior shape point %v was promoted to node %d", wantInterior, nodeID)
		}
	}
}

// TestDeterminism loads the same bytes twice and asserts identical edge/node id
// assignment and identical sorted neighbor order.
func TestDeterminism(test *testing.T) {
	data := goldenBytes(test)
	roadGraph1, geom1, err := graph.LoadEdgeAttributesGeoJSON(bytes.NewReader(data))
	if err != nil {
		test.Fatalf("load 1: %v", err)
	}
	roadGraph2, geom2, err := graph.LoadEdgeAttributesGeoJSON(bytes.NewReader(data))
	if err != nil {
		test.Fatalf("load 2: %v", err)
	}

	if roadGraph1.EdgeCount() != roadGraph2.EdgeCount() || roadGraph1.NodeCount() != roadGraph2.NodeCount() {
		test.Fatalf("counts differ across loads")
	}
	for edgeID := domain.EdgeID(0); int(edgeID) < roadGraph1.EdgeCount(); edgeID++ {
		edge1, _ := roadGraph1.Edge(edgeID)
		edge2, _ := roadGraph2.Edge(edgeID)
		if edge1 != edge2 {
			test.Errorf("edge id %d differs across loads: %+v vs %+v", edgeID, edge1, edge2)
		}
	}
	for nodeID := domain.NodeID(0); int(nodeID) < roadGraph1.NodeCount(); nodeID++ {
		node1, _ := roadGraph1.Node(nodeID)
		node2, _ := roadGraph2.Node(nodeID)
		if node1 != node2 {
			test.Errorf("node id %d differs across loads: %+v vs %+v", nodeID, node1, node2)
		}
		nb1 := roadGraph1.Neighbors(nodeID)
		nb2 := roadGraph2.Neighbors(nodeID)
		if !reflect.DeepEqual(nb1, nb2) {
			test.Errorf("node id %d neighbors differ across loads: %v vs %v", nodeID, nb1, nb2)
		}
	}
	if !reflect.DeepEqual(geom1, geom2) {
		test.Errorf("geometry maps differ across loads")
	}
}

// malformedEntry mirrors one entry of malformed_exports.json.
type malformedEntry struct {
	Violates          string          `json:"violates"`
	FeatureCollection json.RawMessage `json:"feature_collection"`
}

// TestRejectCorpus iterates ALL entries in malformed_exports.json and asserts
// each is rejected with a non-nil error and no partial graph (g == nil).
func TestRejectCorpus(test *testing.T) {
	data, err := os.ReadFile(filepath.Join(fixtureDir, "malformed_exports.json"))
	if err != nil {
		test.Fatalf("read corpus: %v", err)
	}
	var corpus []malformedEntry
	if err := json.Unmarshal(data, &corpus); err != nil {
		test.Fatalf("unmarshal corpus: %v", err)
	}
	if len(corpus) != 13 {
		test.Fatalf("corpus has %d entries, want 13", len(corpus))
	}

	// The fixture corpus is an NYC extract. Case 8 (a uniform [lat,lon] axis
	// swap whose swapped values BOTH stay inside WGS84) is only detectable
	// against the dataset's expected region, so we supply the NYC box: lon ∈
	// [-75,-73], lat ∈ [40,41] contains every golden coordinate and excludes the
	// swap (lon 40.73 > -73; lat -73.99 < 40). The other 12 entries reject
	// region-agnostically regardless of whether bounds are supplied.
	nycBounds := graph.WithExpectedBounds(-75, -73, 40, 41)
	for index, entry := range corpus {
		roadGraph, geom, err := graph.LoadEdgeAttributesGeoJSON(bytes.NewReader(entry.FeatureCollection), nycBounds)
		if err == nil {
			test.Errorf("corpus[%d] (%q): expected rejection, got nil error", index, entry.Violates)
			continue
		}
		if roadGraph != nil {
			test.Errorf("corpus[%d] (%q): rejected but returned non-nil graph (must be fail-closed, no partial)", index, entry.Violates)
		}
		if geom != nil {
			test.Errorf("corpus[%d] (%q): rejected but returned non-nil geometry map", index, entry.Violates)
		}
		test.Logf("corpus[%d] rejected: %v", index, err)
	}
}

// TestRejectErrorMessages spot-checks that a few rejections mention the specific
// invariant they violate, so the errors are descriptive and actionable.
func TestRejectErrorMessages(test *testing.T) {
	data, err := os.ReadFile(filepath.Join(fixtureDir, "malformed_exports.json"))
	if err != nil {
		test.Fatalf("read corpus: %v", err)
	}
	var corpus []malformedEntry
	if err := json.Unmarshal(data, &corpus); err != nil {
		test.Fatalf("unmarshal corpus: %v", err)
	}

	wantSubstr := map[int]string{
		0:  "schema_version",
		1:  "schema_version",
		2:  "schema_version",
		3:  "must match",
		6:  "highway_class",
		8:  "outside",
		9:  "length_m",
		10: "maxspeed_kmh",
	}
	// Case 8 (in-range axis swap) needs the dataset's region to be caught; supply
	// the NYC box for it. The others reject region-agnostically, so a uniform
	// bounds arg here is harmless and keeps every golden coord inside the box.
	nycBounds := graph.WithExpectedBounds(-75, -73, 40, 41)
	for idx, sub := range wantSubstr {
		_, _, err := graph.LoadEdgeAttributesGeoJSON(bytes.NewReader(corpus[idx].FeatureCollection), nycBounds)
		if err == nil {
			test.Errorf("corpus[%d]: expected error", idx)
			continue
		}
		if !bytes.Contains([]byte(err.Error()), []byte(sub)) {
			test.Errorf("corpus[%d]: error %q does not mention %q", idx, err.Error(), sub)
		}
	}
}

// tokyoEdge is a minimal, §2-valid single-edge FeatureCollection in the Eastern
// hemisphere (near Tokyo: lon ~139.7, lat ~35.7). It is the regression guard
// against re-hardcoding a hemisphere: dense edge_id 0, a §1-valid segment_id
// whose embedded way matches osm_way_id, a highway_class in the enum, positive
// metrics, and a 2-coordinate [lon,lat] LineString. With the default (no-bounds)
// call it must LOAD, proving the loader is region-agnostic.
const tokyoEdge = `{
  "type": "FeatureCollection",
  "schema_version": 1,
  "features": [
    {
      "type": "Feature",
      "geometry": {
        "type": "LineString",
        "coordinates": [[139.70010, 35.68950], [139.70220, 35.69010]]
      },
      "properties": {
        "segment_id": "4001234:0:F",
        "edge_id": 0,
        "source_node": 10,
        "target_node": 11,
        "osm_way_id": 4001234,
        "highway_class": "primary",
        "lanes_effective": 2,
        "length_m": 210.0,
        "maxspeed_kmh": 50.0,
        "freeflow_time_s": 15.1,
        "capacity_vph": 1800.0
      }
    }
  ]
}`

// TestEasternHemisphereLoadsDefault proves the decoupling goal: an
// Eastern-hemisphere (Tokyo) export that is otherwise §2-valid LOADS with the
// default no-bounds call. The longitude +139.7 would have been silently rejected
// by the old hardcoded Western-hemisphere window; region-agnostic by default, it
// is accepted. This is the guard against ever re-baking a hemisphere into the
// engine.
func TestEasternHemisphereLoadsDefault(test *testing.T) {
	roadGraph, geom, err := graph.LoadEdgeAttributesGeoJSON(bytes.NewReader([]byte(tokyoEdge)))
	if err != nil {
		test.Fatalf("eastern-hemisphere export must load with the default (no-bounds) call, got: %v", err)
	}
	if roadGraph.EdgeCount() != 1 {
		test.Errorf("EdgeCount = %d, want 1", roadGraph.EdgeCount())
	}
	// Coordinates retained verbatim as [lon,lat], no swap.
	want := graph.LineString{{139.70010, 35.68950}, {139.70220, 35.69010}}
	if got := geom["4001234:0:F"]; !reflect.DeepEqual(got, want) {
		test.Errorf("geometry = %v, want %v", got, want)
	}
}

// TestExpectedBoundsGatesGolden proves WithExpectedBounds actually gates: a box
// that EXCLUDES the golden NYC coordinates (a Tokyo box) causes the golden to be
// rejected as an apparent axis swap, even though it loads fine by default.
func TestExpectedBoundsGatesGolden(test *testing.T) {
	tokyoBox := graph.WithExpectedBounds(139, 140, 35, 36)
	roadGraph, geom, err := graph.LoadEdgeAttributesGeoJSON(bytes.NewReader(goldenBytes(test)), tokyoBox)
	if err == nil {
		test.Fatalf("golden must be rejected when expected bounds exclude its coords")
	}
	if roadGraph != nil || geom != nil {
		test.Errorf("rejected load must return nil graph/map, got g=%v geom=%v", roadGraph, geom)
	}
	if !bytes.Contains([]byte(err.Error()), []byte("axis swap")) {
		test.Errorf("error %q should name the [lat,lon] axis swap", err.Error())
	}
}

// TestExpectedBoundsRejectsNonsenseBox proves a programmer-error bounds box
// (lonMin > lonMax) is reported as a load error rather than silently accepted.
func TestExpectedBoundsRejectsNonsenseBox(test *testing.T) {
	bad := graph.WithExpectedBounds(10, -10, 40, 41)
	roadGraph, _, err := graph.LoadEdgeAttributesGeoJSON(bytes.NewReader(goldenBytes(test)), bad)
	if err == nil {
		test.Fatalf("nonsense bounds box must be rejected")
	}
	if roadGraph != nil {
		test.Errorf("rejected load must return nil graph")
	}
}

// infLengthEdge is a §2-valid single-edge FeatureCollection EXCEPT that length_m
// is 1e400 — a huge-exponent JSON number encoding/json decodes to +Inf. +Inf
// passes a bare > 0 check (Inf > 0 is true) and would silently poison downstream
// BPR cost, so the loader must reject it. One metric (length_m) is exercised
// here; the same isFinitePositive guard covers maxspeed_kmh, freeflow_time_s,
// and capacity_vph identically.
const infLengthEdge = `{
  "type": "FeatureCollection",
  "schema_version": 1,
  "features": [
    {
      "type": "Feature",
      "geometry": {
        "type": "LineString",
        "coordinates": [[139.70010, 35.68950], [139.70220, 35.69010]]
      },
      "properties": {
        "segment_id": "4001234:0:F",
        "edge_id": 0,
        "source_node": 10,
        "target_node": 11,
        "osm_way_id": 4001234,
        "highway_class": "primary",
        "lanes_effective": 2,
        "length_m": 1e400,
        "maxspeed_kmh": 50.0,
        "freeflow_time_s": 15.1,
        "capacity_vph": 1800.0
      }
    }
  ]
}`

// TestRejectNonFiniteMetric asserts a non-finite (+Inf) metric field is rejected
// with a non-nil error and a nil graph, proving Inf cannot slip past the > 0
// invariant and poison BPR cost.
func TestRejectNonFiniteMetric(test *testing.T) {
	roadGraph, geom, err := graph.LoadEdgeAttributesGeoJSON(bytes.NewReader([]byte(infLengthEdge)))
	if err == nil {
		test.Fatalf("non-finite length_m (+Inf) must be rejected")
	}
	if roadGraph != nil || geom != nil {
		test.Errorf("rejected load must return nil graph/map, got g=%v geom=%v", roadGraph, geom)
	}
	if !bytes.Contains([]byte(err.Error()), []byte("length_m")) {
		test.Errorf("error %q should name length_m", err.Error())
	}
}

// contradictoryNodeEdges is a FeatureCollection with two edges that share
// source_node id 10 but give it DIFFERENT geometry endpoints (lon 139.70010 vs
// 139.80010 — ~8 km apart, far beyond nodePosEpsilonDeg). §2 makes the geometry
// endpoint the durable node-position identity, so a re-used node id resolving to
// two positions is a contradictory export the loader must reject loudly rather
// than silently accept-first.
const contradictoryNodeEdges = `{
  "type": "FeatureCollection",
  "schema_version": 1,
  "features": [
    {
      "type": "Feature",
      "geometry": {
        "type": "LineString",
        "coordinates": [[139.70010, 35.68950], [139.70220, 35.69010]]
      },
      "properties": {
        "segment_id": "4001234:0:F",
        "edge_id": 0,
        "source_node": 10,
        "target_node": 11,
        "osm_way_id": 4001234,
        "highway_class": "primary",
        "lanes_effective": 2,
        "length_m": 210.0,
        "maxspeed_kmh": 50.0,
        "freeflow_time_s": 15.1,
        "capacity_vph": 1800.0
      }
    },
    {
      "type": "Feature",
      "geometry": {
        "type": "LineString",
        "coordinates": [[139.80010, 35.68950], [139.80220, 35.69010]]
      },
      "properties": {
        "segment_id": "4005678:0:F",
        "edge_id": 1,
        "source_node": 10,
        "target_node": 12,
        "osm_way_id": 4005678,
        "highway_class": "primary",
        "lanes_effective": 2,
        "length_m": 210.0,
        "maxspeed_kmh": 50.0,
        "freeflow_time_s": 15.1,
        "capacity_vph": 1800.0
      }
    }
  ]
}`

// TestRejectContradictoryNodeCoords asserts that two edges re-using a node id
// with conflicting geometry endpoints are rejected (fail-closed), not silently
// reconciled by accept-first.
func TestRejectContradictoryNodeCoords(test *testing.T) {
	roadGraph, geom, err := graph.LoadEdgeAttributesGeoJSON(bytes.NewReader([]byte(contradictoryNodeEdges)))
	if err == nil {
		test.Fatalf("contradictory node-endpoint coords must be rejected")
	}
	if roadGraph != nil || geom != nil {
		test.Errorf("rejected load must return nil graph/map, got g=%v geom=%v", roadGraph, geom)
	}
	if !bytes.Contains([]byte(err.Error()), []byte("contradictory")) {
		test.Errorf("error %q should name the contradictory endpoint coordinates", err.Error())
	}
}

// shortLengthEdge is a §2-valid single-edge FeatureCollection EXCEPT that
// length_m (50 m) is far shorter than the great-circle chord between its
// endpoints (~210 m for these Tokyo coords). A road must be at least as long as
// the straight line between its ends, so the issue #81 loader guard must reject
// it (a length_m < endpoint chord is a contradictory export — and it would make
// the A* admissible heuristic inadmissible). The same coords with length_m=210
// load cleanly, see TestStraightRoadLengthEqualsChordLoads.
const shortLengthEdge = `{
  "type": "FeatureCollection",
  "schema_version": 1,
  "features": [
    {
      "type": "Feature",
      "geometry": {
        "type": "LineString",
        "coordinates": [[139.70010, 35.68950], [139.70220, 35.69010]]
      },
      "properties": {
        "segment_id": "4001234:0:F",
        "edge_id": 0,
        "source_node": 10,
        "target_node": 11,
        "osm_way_id": 4001234,
        "highway_class": "primary",
        "lanes_effective": 2,
        "length_m": 50.0,
        "maxspeed_kmh": 50.0,
        "freeflow_time_s": 15.1,
        "capacity_vph": 1800.0
      }
    }
  ]
}`

// TestRejectLengthShorterThanChord asserts the issue #81 guard: an edge whose
// length_m is shorter than its endpoint great-circle chord is rejected with a
// non-nil error and a nil graph (fail-closed), and the message names length_m so
// the offending invariant is identifiable.
func TestRejectLengthShorterThanChord(test *testing.T) {
	roadGraph, geom, err := graph.LoadEdgeAttributesGeoJSON(bytes.NewReader([]byte(shortLengthEdge)))
	if err == nil {
		test.Fatalf("length_m shorter than the endpoint chord must be rejected (§2 length_m >= chord)")
	}
	if roadGraph != nil || geom != nil {
		test.Errorf("rejected load must return nil graph/map, got g=%v geom=%v", roadGraph, geom)
	}
	if !bytes.Contains([]byte(err.Error()), []byte("length_m")) || !bytes.Contains([]byte(err.Error()), []byte("chord")) {
		test.Errorf("error %q should name length_m and the chord", err.Error())
	}
}

// straightRoadEdge is a §2-valid single-edge FeatureCollection whose length_m
// (210 m) is set to ~the endpoint chord for these coords: a legitimately straight
// road where length_m ≈ chord. It MUST load cleanly — the issue #81 guard's small
// relative tolerance (lengthChordRelTol) keeps float rounding between the authored
// length and the loader's haversine from spuriously rejecting it.
const straightRoadEdge = `{
  "type": "FeatureCollection",
  "schema_version": 1,
  "features": [
    {
      "type": "Feature",
      "geometry": {
        "type": "LineString",
        "coordinates": [[139.70010, 35.68950], [139.70220, 35.69010]]
      },
      "properties": {
        "segment_id": "4001234:0:F",
        "edge_id": 0,
        "source_node": 10,
        "target_node": 11,
        "osm_way_id": 4001234,
        "highway_class": "primary",
        "lanes_effective": 2,
        "length_m": 210.0,
        "maxspeed_kmh": 50.0,
        "freeflow_time_s": 15.1,
        "capacity_vph": 1800.0
      }
    }
  ]
}`

// TestStraightRoadLengthEqualsChordLoads asserts a legitimately straight road
// (length_m ≈ endpoint chord) is NOT rejected by the issue #81 guard — the
// tolerance absorbs float noise so length_m == chord is accepted, not a
// false-positive rejection.
func TestStraightRoadLengthEqualsChordLoads(test *testing.T) {
	roadGraph, geom, err := graph.LoadEdgeAttributesGeoJSON(bytes.NewReader([]byte(straightRoadEdge)))
	if err != nil {
		test.Fatalf("a straight road with length_m ≈ chord must load cleanly, got: %v", err)
	}
	if roadGraph == nil || geom == nil {
		test.Errorf("clean load must return a non-nil graph and geometry map")
	}
}

// TestRejectSyntacticallyBrokenJSON feeds syntactically invalid JSON and asserts
// the loader returns a non-nil error and a nil graph — i.e. it errors cleanly
// rather than panicking on malformed input.
func TestRejectSyntacticallyBrokenJSON(test *testing.T) {
	roadGraph, geom, err := graph.LoadEdgeAttributesGeoJSON(bytes.NewReader([]byte("{not json")))
	if err == nil {
		test.Fatalf("syntactically broken JSON must be rejected")
	}
	if roadGraph != nil || geom != nil {
		test.Errorf("rejected load must return nil graph/map, got g=%v geom=%v", roadGraph, geom)
	}
}

// TestLoadFromFile exercises the file-path convenience wrapper.
func TestLoadFromFile(test *testing.T) {
	roadGraph, _, err := graph.LoadEdgeAttributesGeoJSONFile(filepath.Join(fixtureDir, "example_export.geojson"))
	if err != nil {
		test.Fatalf("LoadEdgeAttributesGeoJSONFile: %v", err)
	}
	if roadGraph.EdgeCount() != 12 {
		test.Errorf("EdgeCount = %d, want 12", roadGraph.EdgeCount())
	}
}
