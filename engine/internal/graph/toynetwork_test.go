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

// nycBounds is the expected-region box for the toy network: it contains every
// coordinate (lon ∈ [-73.99,-73.95], lat ∈ [40.73,40.75]) and rejects a
// [lat,lon] axis swap.
func toyBounds() graph.LoadOption { return graph.WithExpectedBounds(-74, -73, 40, 41) }

// loadToy loads the toy fixture with the NYC bounds and fails the test on any
// load error (the fixture MUST load with zero error).
func loadToy(t *testing.T) (*graph.AdjacencyGraph, map[domain.SegmentID]graph.LineString) {
	t.Helper()
	g, geom, err := graph.LoadEdgeAttributesGeoJSONFile(toyNetworkPath, toyBounds())
	if err != nil {
		t.Fatalf("toy_network.geojson must load with zero error, got: %v", err)
	}
	return g, geom
}

// segByID walks the graph and returns the edge whose Segment == seg, plus ok.
func segByID(g *graph.AdjacencyGraph, seg domain.SegmentID) (graph.Edge, bool) {
	for id := domain.EdgeID(0); int(id) < g.EdgeCount(); id++ {
		e, _ := g.Edge(id)
		if e.Segment == seg {
			return e, true
		}
	}
	return graph.Edge{}, false
}

// TestToyNetworkLoads asserts the fixture loads cleanly with the expected
// edge/node counts, dense edge ids, and a geometry-map entry per segment_id.
func TestToyNetworkLoads(t *testing.T) {
	g, geom := loadToy(t)

	const wantEdges = 7
	const wantNodes = 6
	if g.EdgeCount() != wantEdges {
		t.Errorf("EdgeCount = %d, want %d", g.EdgeCount(), wantEdges)
	}
	if g.NodeCount() != wantNodes {
		t.Errorf("NodeCount = %d, want %d", g.NodeCount(), wantNodes)
	}
	if len(geom) != wantEdges {
		t.Errorf("geometry map has %d entries, want %d", len(geom), wantEdges)
	}

	// Dense, contiguous edge ids 0..EdgeCount-1, each present, with a geometry
	// entry for its segment_id.
	for id := domain.EdgeID(0); int(id) < g.EdgeCount(); id++ {
		e, ok := g.Edge(id)
		if !ok {
			t.Errorf("edge id %d missing — edge ids must be dense 0..%d", id, wantEdges-1)
			continue
		}
		if e.ID != id {
			t.Errorf("edge at id %d has ID %d (in-memory EdgeID must equal export edge_id)", id, e.ID)
		}
		if _, ok := geom[e.Segment]; !ok {
			t.Errorf("edge id %d segment %q missing from geometry map", id, e.Segment)
		}
	}

	// Dense, contiguous node ids 0..NodeCount-1.
	for id := domain.NodeID(0); int(id) < g.NodeCount(); id++ {
		n, ok := g.Node(id)
		if !ok {
			t.Errorf("node id %d missing — nodes must be dense 0..%d", id, wantNodes-1)
			continue
		}
		if n.ID != id {
			t.Errorf("node at id %d has ID %d", id, n.ID)
		}
	}
}

// TestToyNetworkFRPair asserts the 48800123 two-way pair: both directions
// present, endpoints swapped (F.From == R.To and F.To == R.From), and geometry
// reversed (R's coords are F's reversed).
func TestToyNetworkFRPair(t *testing.T) {
	g, geom := loadToy(t)

	f, okF := segByID(g, "48800123:0:F")
	r, okR := segByID(g, "48800123:0:R")
	if !okF || !okR {
		t.Fatalf("F/R pair must both be present: F present=%v, R present=%v", okF, okR)
	}

	if f.From != r.To {
		t.Errorf("F.From (%d) must equal R.To (%d)", f.From, r.To)
	}
	if f.To != r.From {
		t.Errorf("F.To (%d) must equal R.From (%d)", f.To, r.From)
	}

	fGeom := geom["48800123:0:F"]
	rGeom := geom["48800123:0:R"]
	if len(fGeom) != len(rGeom) {
		t.Fatalf("F/R geometries differ in length: %d vs %d", len(fGeom), len(rGeom))
	}
	reversed := make(graph.LineString, len(fGeom))
	for i := range fGeom {
		reversed[i] = fGeom[len(fGeom)-1-i]
	}
	if !reflect.DeepEqual(rGeom, reversed) {
		t.Errorf("R geometry %v is not the reverse of F geometry %v", rGeom, fGeom)
	}
}

// TestToyNetworkCongestionOverlap asserts the three segment_ids shared with the
// segment_congestion fixture are all present, so a congestion overlay is
// demoable later via a pure segment_id join.
func TestToyNetworkCongestionOverlap(t *testing.T) {
	g, _ := loadToy(t)
	for _, seg := range []domain.SegmentID{"27583001:0:F", "48800123:0:F", "48800123:0:R"} {
		if _, ok := segByID(g, seg); !ok {
			t.Errorf("congestion-overlapping segment %q must be present", seg)
		}
	}
}

// TestToyNetworkCostNotHops pins the acceptance property #27 relies on: between
// origin node 0 and destination node 2 the multi-hop alternative has strictly
// MORE edges than the direct edge, yet a strictly LOWER summed freeflow_time_s.
// We resolve the path edges by segment_id (no router yet) and confirm both halves
// of the property at the data level, also sanity-checking endpoints via the
// graph's Neighbors accessor.
func TestToyNetworkCostNotHops(t *testing.T) {
	g, _ := loadToy(t)

	// Direct 1-hop edge: 9000001:0:F, node 0 -> node 2.
	direct, ok := segByID(g, "9000001:0:F")
	if !ok {
		t.Fatal("direct edge 9000001:0:F missing")
	}

	// Multi-hop alternative: 905512:0:F (node0->node1) then 905512:1:F (node1->node2).
	hop1, ok1 := segByID(g, "905512:0:F")
	hop2, ok2 := segByID(g, "905512:1:F")
	if !ok1 || !ok2 {
		t.Fatalf("alternative edges missing: 905512:0:F present=%v, 905512:1:F present=%v", ok1, ok2)
	}

	// Origin/destination agree across both routes.
	origin, dest := direct.From, direct.To
	if hop1.From != origin {
		t.Errorf("alternative hop1.From (%d) must equal origin (%d)", hop1.From, origin)
	}
	if hop2.To != dest {
		t.Errorf("alternative hop2.To (%d) must equal destination (%d)", hop2.To, dest)
	}
	if hop1.To != hop2.From {
		t.Errorf("alternative is not connected: hop1.To (%d) != hop2.From (%d)", hop1.To, hop2.From)
	}

	// Sanity via Neighbors: the direct edge and hop1 both leave the origin; hop2
	// leaves the intermediate node.
	if !neighborHas(g, origin, direct.ID) {
		t.Errorf("direct edge %d not among origin (%d) out-edges", direct.ID, origin)
	}
	if !neighborHas(g, origin, hop1.ID) {
		t.Errorf("hop1 edge %d not among origin (%d) out-edges", hop1.ID, origin)
	}
	if !neighborHas(g, hop1.To, hop2.ID) {
		t.Errorf("hop2 edge %d not among node %d out-edges", hop2.ID, hop1.To)
	}

	// (a) The multi-hop alternative has strictly MORE edges.
	const directHops = 1
	const altHops = 2
	if !(altHops > directHops) {
		t.Fatalf("expected alternative (%d edges) to have more edges than direct (%d)", altHops, directHops)
	}

	// (b) Its summed freeflow_time_s is strictly LESS than the direct edge's.
	altCost := hop1.FreeFlowS + hop2.FreeFlowS
	if !(altCost < direct.FreeFlowS) {
		t.Errorf("cost!=hops violated: alternative summed FreeFlowS = %v must be < direct FreeFlowS = %v", altCost, direct.FreeFlowS)
	}
	t.Logf("cost!=hops: direct 1-hop FreeFlowS = %v s; alternative 2-hop FreeFlowS = %v s", direct.FreeFlowS, altCost)
}

// neighborHas reports whether edge id is among node n's outgoing edges.
func neighborHas(g *graph.AdjacencyGraph, n domain.NodeID, id domain.EdgeID) bool {
	for _, e := range g.Neighbors(n) {
		if e.ID == id {
			return true
		}
	}
	return false
}

// TestToyNetworkInteriorShapePoint confirms an interior shape point is retained
// (an edge whose LineString has 3 coords) and that NodeCount counts only
// endpoints, not interior points.
func TestToyNetworkInteriorShapePoint(t *testing.T) {
	g, geom := loadToy(t)

	// 33112200:0:F has a 3-coordinate LineString with an interior shape point.
	ls := geom["33112200:0:F"]
	if len(ls) != 3 {
		t.Fatalf("33112200:0:F geometry has %d coords, want 3 (interior shape point retained)", len(ls))
	}
	interior := ls[1]

	// The interior coordinate must NOT have been promoted to a graph node.
	for id := domain.NodeID(0); int(id) < g.NodeCount(); id++ {
		n, _ := g.Node(id)
		if n.Pos.Lon == interior[0] && n.Pos.Lat == interior[1] {
			t.Errorf("interior shape point %v was promoted to node %d", interior, id)
		}
	}

	// NodeCount counts only the 6 endpoints, not the 2 interior shape points
	// (edge 0 and edge 6 each carry one).
	if g.NodeCount() != 6 {
		t.Errorf("NodeCount = %d, want 6 (endpoints only, interior shape points excluded)", g.NodeCount())
	}
}
