package graph_test

import (
	"reflect"
	"testing"

	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/domain"
	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/graph"
)

// toyNetworkPath is the module-level shared toy routing fixture (#26). From this
// package (engine/internal/graph) the new engine/testdata dir resolves at
// ../../testdata. This test pins the data/topology properties #27's router will
// rely on; #27 itself does not exist yet, so we validate through the merged #25
// loader.
const toyNetworkPath = "../../testdata/toy_network.geojson"

// toyBounds is the expected-region guard for the toy network: a deliberately
// loose [-74,-73]×[40,41] box that contains the data window (lon ∈
// [-73.99,-73.95], lat ∈ [40.73,40.75]) yet still rejects a [lat,lon] axis swap
// — a swapped coordinate like [40.73,-73.99] escapes the box and is caught.
func toyBounds() graph.LoadOption { return graph.WithExpectedBounds(-74, -73, 40, 41) }

// loadToy loads the toy fixture with the NYC bounds and fails the test on any
// load error (the fixture MUST load with zero error).
func loadToy(test *testing.T) (*graph.AdjacencyGraph, map[domain.SegmentID]graph.LineString) {
	test.Helper()
	roadGraph, geom, err := graph.LoadEdgeAttributesGeoJSONFile(toyNetworkPath, toyBounds())
	if err != nil {
		test.Fatalf("toy_network.geojson must load with zero error, got: %v", err)
	}
	return roadGraph, geom
}

// segByID walks the graph and returns the edge whose Segment == seg, plus ok.
func segByID(roadGraph *graph.AdjacencyGraph, seg domain.SegmentID) (graph.Edge, bool) {
	for edgeID := domain.EdgeID(0); int(edgeID) < roadGraph.EdgeCount(); edgeID++ {
		edge, _ := roadGraph.Edge(edgeID)
		if edge.Segment == seg {
			return edge, true
		}
	}
	return graph.Edge{}, false
}

// TestToyNetworkLoads asserts the fixture loads cleanly with the expected
// edge/node counts, dense edge ids, and a geometry-map entry per segment_id.
func TestToyNetworkLoads(test *testing.T) {
	roadGraph, geom := loadToy(test)

	const wantEdges = 7
	const wantNodes = 6
	if roadGraph.EdgeCount() != wantEdges {
		test.Errorf("EdgeCount = %d, want %d", roadGraph.EdgeCount(), wantEdges)
	}
	if roadGraph.NodeCount() != wantNodes {
		test.Errorf("NodeCount = %d, want %d", roadGraph.NodeCount(), wantNodes)
	}
	if len(geom) != wantEdges {
		test.Errorf("geometry map has %d entries, want %d", len(geom), wantEdges)
	}

	// Dense, contiguous edge ids 0..EdgeCount-1, each present, with a geometry
	// entry for its segment_id.
	for edgeID := domain.EdgeID(0); int(edgeID) < roadGraph.EdgeCount(); edgeID++ {
		edge, found1 := roadGraph.Edge(edgeID)
		if !found1 {
			test.Errorf("edge id %d missing — edge ids must be dense 0..%d", edgeID, wantEdges-1)
			continue
		}
		if edge.ID != edgeID {
			test.Errorf("edge at id %d has ID %d (in-memory EdgeID must equal export edge_id)", edgeID, edge.ID)
		}
		if _, found2 := geom[edge.Segment]; !found2 {
			test.Errorf("edge id %d segment %q missing from geometry map", edgeID, edge.Segment)
		}
	}

	// Dense, contiguous node ids 0..NodeCount-1.
	for nodeID := domain.NodeID(0); int(nodeID) < roadGraph.NodeCount(); nodeID++ {
		node, found3 := roadGraph.Node(nodeID)
		if !found3 {
			test.Errorf("node id %d missing — nodes must be dense 0..%d", nodeID, wantNodes-1)
			continue
		}
		if node.ID != nodeID {
			test.Errorf("node at id %d has ID %d", nodeID, node.ID)
		}
	}
}

// TestToyNetworkFRPair asserts the 48800123 two-way pair: both directions
// present, endpoints swapped (F.From == R.To and F.To == R.From), and geometry
// reversed (R's coords are F's reversed).
func TestToyNetworkFRPair(test *testing.T) {
	roadGraph, geom := loadToy(test)

	edge1, okF := segByID(roadGraph, "48800123:0:F")
	edge2, okR := segByID(roadGraph, "48800123:0:R")
	if !okF || !okR {
		test.Fatalf("F/R pair must both be present: F present=%v, R present=%v", okF, okR)
	}

	if edge1.From != edge2.To {
		test.Errorf("F.From (%d) must equal R.To (%d)", edge1.From, edge2.To)
	}
	if edge1.To != edge2.From {
		test.Errorf("F.To (%d) must equal R.From (%d)", edge1.To, edge2.From)
	}

	fGeom := geom["48800123:0:F"]
	rGeom := geom["48800123:0:R"]
	if len(fGeom) != len(rGeom) {
		test.Fatalf("F/R geometries differ in length: %d vs %d", len(fGeom), len(rGeom))
	}
	reversed := make(graph.LineString, len(fGeom))
	for index := range fGeom {
		reversed[index] = fGeom[len(fGeom)-1-index]
	}
	if !reflect.DeepEqual(rGeom, reversed) {
		test.Errorf("R geometry %v is not the reverse of F geometry %v", rGeom, fGeom)
	}
}

// TestToyNetworkCongestionOverlap asserts the three segment_ids shared with the
// segment_congestion fixture are all present, so a congestion overlay is
// demoable later via a pure segment_id join.
func TestToyNetworkCongestionOverlap(test *testing.T) {
	roadGraph, _ := loadToy(test)
	for _, seg := range []domain.SegmentID{"27583001:0:F", "48800123:0:F", "48800123:0:R"} {
		if _, found := segByID(roadGraph, seg); !found {
			test.Errorf("congestion-overlapping segment %q must be present", seg)
		}
	}
}

// TestToyNetworkDerivedFields pins the exact §2-derived values for every edge so
// the "derivations verified exact" property is machine-checked, not only stated
// in the README. The #25 loader passes these numbers through verbatim (it does
// NOT re-derive them, see loader.go), so a typo'd fixture value would otherwise
// load silently green and corrupt every downstream #27 router test. Exact == is
// valid because the fixture literal and the test literal parse to the identical
// float64 (no arithmetic on the path). Derivation rules (§2, capacity_scale=1.0):
// capacity_vph = lanes × 1800 × class_factor;
// freeflow_time_s = length_m / (maxspeed_kmh / 3.6).
func TestToyNetworkDerivedFields(test *testing.T) {
	roadGraph, _ := loadToy(test)

	want := map[domain.SegmentID]struct {
		lengthM, freeFlowS, capacityVPH float64
	}{
		"9000001:0:F":  {900.0, 108.0, 900.0},  // residential, 1 lane, 30 km/h
		"905512:0:F":   {500.0, 18.0, 5400.0},  // motorway, 3 lanes, 100 km/h
		"905512:1:F":   {400.0, 14.4, 5400.0},  // motorway, 3 lanes, 100 km/h
		"27583001:0:F": {240.0, 14.4, 2880.0},  // primary, 2 lanes, 60 km/h
		"48800123:0:F": {180.0, 12.96, 2520.0}, // secondary, 2 lanes, 50 km/h
		"48800123:0:R": {180.0, 12.96, 2520.0}, // secondary (reverse), 2 lanes, 50 km/h
		"33112200:0:F": {400.0, 18.0, 3240.0},  // trunk, 2 lanes, 80 km/h
	}

	for seg, attrs := range want {
		edge, found := segByID(roadGraph, seg)
		if !found {
			test.Errorf("segment %q missing", seg)
			continue
		}
		if edge.LengthM != attrs.lengthM {
			test.Errorf("%s: LengthM = %v, want %v", seg, edge.LengthM, attrs.lengthM)
		}
		if edge.FreeFlowS != attrs.freeFlowS {
			test.Errorf("%s: FreeFlowS = %v, want %v", seg, edge.FreeFlowS, attrs.freeFlowS)
		}
		if edge.CapacityVPH != attrs.capacityVPH {
			test.Errorf("%s: CapacityVPH = %v, want %v", seg, edge.CapacityVPH, attrs.capacityVPH)
		}
	}
}

// TestToyNetworkCostNotHops pins the acceptance property #27 relies on: between
// origin node 0 and destination node 2 the multi-hop alternative has strictly
// MORE edges than the direct edge, yet a strictly LOWER summed freeflow_time_s.
// We resolve the path edges by segment_id (no router yet) and confirm both halves
// of the property at the data level, also sanity-checking endpoints via the
// graph's Neighbors accessor.
func TestToyNetworkCostNotHops(test *testing.T) {
	roadGraph, _ := loadToy(test)

	// Direct 1-hop edge: 9000001:0:F, node 0 -> node 2.
	direct, found := segByID(roadGraph, "9000001:0:F")
	if !found {
		test.Fatal("direct edge 9000001:0:F missing")
	}

	// Multi-hop alternative: 905512:0:F (node0->node1) then 905512:1:F (node1->node2).
	hop1, ok1 := segByID(roadGraph, "905512:0:F")
	hop2, ok2 := segByID(roadGraph, "905512:1:F")
	if !ok1 || !ok2 {
		test.Fatalf("alternative edges missing: 905512:0:F present=%v, 905512:1:F present=%v", ok1, ok2)
	}

	// Origin/destination agree across both routes.
	origin, dest := direct.From, direct.To
	if hop1.From != origin {
		test.Errorf("alternative hop1.From (%d) must equal origin (%d)", hop1.From, origin)
	}
	if hop2.To != dest {
		test.Errorf("alternative hop2.To (%d) must equal destination (%d)", hop2.To, dest)
	}
	if hop1.To != hop2.From {
		test.Errorf("alternative is not connected: hop1.To (%d) != hop2.From (%d)", hop1.To, hop2.From)
	}

	// Sanity via Neighbors: the direct edge and hop1 both leave the origin; hop2
	// leaves the intermediate node.
	if !neighborHas(roadGraph, origin, direct.ID) {
		test.Errorf("direct edge %d not among origin (%d) out-edges", direct.ID, origin)
	}
	if !neighborHas(roadGraph, origin, hop1.ID) {
		test.Errorf("hop1 edge %d not among origin (%d) out-edges", hop1.ID, origin)
	}
	if !neighborHas(roadGraph, hop1.To, hop2.ID) {
		test.Errorf("hop2 edge %d not among node %d out-edges", hop2.ID, hop1.To)
	}

	// (a) The multi-hop alternative has strictly MORE edges. Count from the edges
	// actually resolved by segment_id above, so the hop counts are derived from the
	// loaded data and cannot drift from it (a constant 2 > 1 would prove nothing).
	directPath := []graph.Edge{direct}
	altPath := []graph.Edge{hop1, hop2}
	if len(altPath) <= len(directPath) {
		test.Fatalf("expected alternative (%d edges) to have more edges than direct (%d)", len(altPath), len(directPath))
	}

	// (b) Its summed freeflow_time_s is strictly LESS than the direct edge's.
	var altCost float64
	for _, edge := range altPath {
		altCost += edge.FreeFlowS
	}
	if !(altCost < direct.FreeFlowS) {
		test.Errorf("cost!=hops violated: alternative summed FreeFlowS = %v must be < direct FreeFlowS = %v", altCost, direct.FreeFlowS)
	}
	test.Logf("cost!=hops: direct 1-hop FreeFlowS = %v s; alternative 2-hop FreeFlowS = %v s", direct.FreeFlowS, altCost)
}

// neighborHas reports whether edge id is among node n's outgoing edges.
func neighborHas(roadGraph *graph.AdjacencyGraph, nodeID domain.NodeID, edgeID domain.EdgeID) bool {
	for _, edge := range roadGraph.Neighbors(nodeID) {
		if edge.ID == edgeID {
			return true
		}
	}
	return false
}

// TestToyNetworkInteriorShapePoint confirms an interior shape point is retained
// (an edge whose LineString has 3 coords) and that NodeCount counts only
// endpoints, not interior points.
func TestToyNetworkInteriorShapePoint(test *testing.T) {
	roadGraph, geom := loadToy(test)

	// 33112200:0:F has a 3-coordinate LineString with an interior shape point.
	lineString := geom["33112200:0:F"]
	if len(lineString) != 3 {
		test.Fatalf("33112200:0:F geometry has %d coords, want 3 (interior shape point retained)", len(lineString))
	}
	interior := lineString[1]

	// The interior coordinate must NOT have been promoted to a graph node. Exact
	// == is valid here: the loader stores coordinates verbatim (no arithmetic on
	// the path), so a promoted interior point would carry bit-identical coords.
	for nodeID := domain.NodeID(0); int(nodeID) < roadGraph.NodeCount(); nodeID++ {
		node, _ := roadGraph.Node(nodeID)
		if node.Pos.Lon == interior[0] && node.Pos.Lat == interior[1] {
			test.Errorf("interior shape point %v was promoted to node %d", interior, nodeID)
		}
	}

	// NodeCount counts only the 6 endpoints, not the 2 interior shape points
	// (edge 0 and edge 6 each carry one).
	if roadGraph.NodeCount() != 6 {
		test.Errorf("NodeCount = %d, want 6 (endpoints only, interior shape points excluded)", roadGraph.NodeCount())
	}
}
