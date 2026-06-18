package routing

import (
	"math"
	"slices"
	"testing"

	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/domain"
	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/graph"
)

// loadToyInternal loads the shared toy fixture from within the routing package
// (the external _test files use loadToyGraph; this internal test needs its own
// loader since it cannot import the _test helper).
func loadToyInternal(test *testing.T) *graph.AdjacencyGraph {
	test.Helper()
	roadGraph, _, err := graph.LoadEdgeAttributesGeoJSONFile(
		"../../testdata/toy_network.geojson",
		graph.WithExpectedBounds(-74, -73, 40, 41),
	)
	if err != nil {
		test.Fatalf("toy_network.geojson must load with zero error, got: %v", err)
	}
	return roadGraph
}

// TestKShortestReturnsDistinctLooplessInOrder is the Yen acceptance case. From
// node 0 to node 2 the toy network has TWO loopless paths: the fast 2-hop motorway
// (edges 1,2; cost 32.4) and the slow direct 1-hop residential (edge 0; cost
// 108.0). Yen must return both, distinct, in non-decreasing cost order — the fast
// path first.
func TestKShortestReturnsDistinctLooplessInOrder(test *testing.T) {
	roadGraph := loadToyInternal(test)

	paths := kShortestPaths(roadGraph, 0, 2, 5, freeFlowWeight)

	if len(paths) != 2 {
		test.Fatalf("kShortestPaths(0->2, k=5) returned %d paths, want 2 (the only two loopless paths)", len(paths))
	}

	// Non-decreasing cost order.
	for index := 1; index < len(paths); index++ {
		if paths[index].cost < paths[index-1].cost {
			test.Errorf("paths not in non-decreasing cost order: paths[%d].cost=%.4f < paths[%d].cost=%.4f",
				index, paths[index].cost, index-1, paths[index-1].cost)
		}
	}

	// The cheapest path is the 2-hop motorway (edges 1,2; 18.0+14.4=32.4).
	if !slices.Equal(paths[0].edges, []domain.EdgeID{1, 2}) {
		test.Errorf("paths[0].edges = %v, want [1 2] (the fast motorway path)", paths[0].edges)
	}
	if math.Abs(paths[0].cost-32.4) > 1e-9 {
		test.Errorf("paths[0].cost = %.4f, want 32.4", paths[0].cost)
	}
	// The second path is the direct residential edge (edge 0; 108.0).
	if !slices.Equal(paths[1].edges, []domain.EdgeID{0}) {
		test.Errorf("paths[1].edges = %v, want [0] (the direct residential path)", paths[1].edges)
	}
	if math.Abs(paths[1].cost-108.0) > 1e-9 {
		test.Errorf("paths[1].cost = %.4f, want 108.0", paths[1].cost)
	}

	// Every returned path must be loopless (no node visited twice).
	for index, p := range paths {
		if !looplessPath(roadGraph, 0, p.edges) {
			test.Errorf("paths[%d] = %v is not loopless", index, p.edges)
		}
	}
}

// TestKShortestDistinct asserts the returned paths are pairwise distinct edge
// sequences (Yen never returns a duplicate).
func TestKShortestDistinct(test *testing.T) {
	roadGraph := loadToyInternal(test)
	paths := kShortestPaths(roadGraph, 0, 2, 5, freeFlowWeight)
	for index := range paths {
		for innerIndex := index + 1; innerIndex < len(paths); innerIndex++ {
			if slices.Equal(paths[index].edges, paths[innerIndex].edges) {
				test.Errorf("paths[%d] and paths[%d] are identical: %v", index, innerIndex, paths[index].edges)
			}
		}
	}
}

// buildTriplePathGraph builds a small directed graph whose 0->3 OD has THREE
// distinct loopless paths, so Yen's machinery BEYOND the first accepted path is
// actually exercised — the shared toy fixture caps any OD at two loopless paths,
// leaving the spur loop that produces the 3rd accepted path, the root-prefix
// masking across paths that share a prefix, and the candidate carry-over across
// iterations all silent. The three 0->3 paths, by cost:
//
//	A: 0->1->3     (e0,e1)      cost 2    — the shortest
//	B: 0->1->2->3  (e0,e2,e3)   cost 3    — shares root prefix [e0] with A
//	C: 0->2->3     (e4,e3)      cost 11   — a disjoint deviation at node 0
//
// Reaching C requires Yen to (a) generate it as a spur of A in one iteration,
// (b) dedupe it when B's iteration rediscovers it, and (c) carry it over to be
// accepted third — none of which a two-path graph can drive.
func buildTriplePathGraph(test *testing.T) *graph.AdjacencyGraph {
	test.Helper()
	pos := []domain.LatLon{
		{Lat: 40.000, Lon: -74.000}, // 0 origin
		{Lat: 40.010, Lon: -74.000}, // 1
		{Lat: 40.010, Lon: -73.990}, // 2
		{Lat: 40.020, Lon: -73.990}, // 3 destination
	}
	nodes := make([]graph.Node, len(pos))
	for index := range pos {
		nodes[index] = graph.Node{ID: domain.NodeID(index), Pos: pos[index]}
	}
	edges := []graph.Edge{
		{ID: 0, Segment: "e0", From: 0, To: 1, LengthM: 100, FreeFlowS: 1, CapacityVPH: 1800},
		{ID: 1, Segment: "e1", From: 1, To: 3, LengthM: 100, FreeFlowS: 1, CapacityVPH: 1800},
		{ID: 2, Segment: "e2", From: 1, To: 2, LengthM: 100, FreeFlowS: 1, CapacityVPH: 1800},
		{ID: 3, Segment: "e3", From: 2, To: 3, LengthM: 100, FreeFlowS: 1, CapacityVPH: 1800},
		{ID: 4, Segment: "e4", From: 0, To: 2, LengthM: 100, FreeFlowS: 10, CapacityVPH: 1800},
	}
	roadGraph, err := graph.New(nodes, edges)
	if err != nil {
		test.Fatalf("buildTriplePathGraph: graph.New: %v", err)
	}
	return roadGraph
}

// TestKShortestThreeDistinctPaths drives Yen past the first accepted path on a
// graph that actually has three loopless 0->3 paths. It pins all three edge
// sequences and costs in non-decreasing order — the multi-spur machinery (root
// masking across A and B, dedupe + carry-over of C) is the part a two-path fixture
// never reaches.
func TestKShortestThreeDistinctPaths(test *testing.T) {
	roadGraph := buildTriplePathGraph(test)

	paths := kShortestPaths(roadGraph, 0, 3, 5, freeFlowWeight)
	if len(paths) != 3 {
		test.Fatalf("kShortestPaths(0->3, k=5) returned %d paths, want 3", len(paths))
	}

	want := []struct {
		edges []domain.EdgeID
		cost  float64
	}{
		{[]domain.EdgeID{0, 1}, 2},    // A: 0->1->3
		{[]domain.EdgeID{0, 2, 3}, 3}, // B: 0->1->2->3
		{[]domain.EdgeID{4, 3}, 11},   // C: 0->2->3
	}
	for index, expected := range want {
		if !slices.Equal(paths[index].edges, expected.edges) {
			test.Errorf("paths[%d].edges = %v, want %v", index, paths[index].edges, expected.edges)
		}
		if math.Abs(paths[index].cost-expected.cost) > 1e-9 {
			test.Errorf("paths[%d].cost = %.4f, want %.4f", index, paths[index].cost, expected.cost)
		}
		if !looplessPath(roadGraph, 0, paths[index].edges) {
			test.Errorf("paths[%d] = %v is not loopless", index, paths[index].edges)
		}
	}
	for index := 1; index < len(paths); index++ {
		if paths[index].cost < paths[index-1].cost {
			test.Errorf("paths not in non-decreasing cost order at %d", index)
		}
	}

	// k=2 caps to the two cheapest (A, B), never the expensive disjoint C.
	two := kShortestPaths(roadGraph, 0, 3, 2, freeFlowWeight)
	if len(two) != 2 ||
		!slices.Equal(two[0].edges, []domain.EdgeID{0, 1}) ||
		!slices.Equal(two[1].edges, []domain.EdgeID{0, 2, 3}) {
		test.Errorf("k=2 = %v, want the two cheapest paths [[0 1] [0 2 3]]", two)
	}
}

// TestKShortestCapsAtAvailable asserts Yen returns as many paths as exist when k
// exceeds the number of loopless paths, and never pads. To node 5 the toy network
// has exactly TWO loopless paths (the 0->2 fork: 0->1->2->3->4->5 and
// 0->2->3->4->5, sharing the 2->3->4->5 tail), so k=4 must yield exactly 2, in
// non-decreasing cost order, never padded to 4.
func TestKShortestCapsAtAvailable(test *testing.T) {
	roadGraph := loadToyInternal(test)
	paths := kShortestPaths(roadGraph, 0, 5, 4, freeFlowWeight)
	if len(paths) != 2 {
		test.Fatalf("kShortestPaths(0->5, k=4) returned %d paths, want 2 (only two loopless paths exist)", len(paths))
	}
	for index := 1; index < len(paths); index++ {
		if paths[index].cost < paths[index-1].cost {
			test.Errorf("paths not in non-decreasing cost order at %d", index)
		}
	}
	for index, p := range paths {
		if !looplessPath(roadGraph, 0, p.edges) {
			test.Errorf("paths[%d] = %v is not loopless", index, p.edges)
		}
	}
}

// TestKShortestEdgeCases covers the boundary inputs.
func TestKShortestEdgeCases(test *testing.T) {
	roadGraph := loadToyInternal(test)

	if got := kShortestPaths(roadGraph, 0, 2, 0, freeFlowWeight); got != nil {
		test.Errorf("k=0 returned %v, want nil", got)
	}
	if got := kShortestPaths(roadGraph, 0, 2, -1, freeFlowWeight); got != nil {
		test.Errorf("k<0 returned %v, want nil", got)
	}
	// src == dst is one empty zero-cost path.
	same := kShortestPaths(roadGraph, 3, 3, 3, freeFlowWeight)
	if len(same) != 1 || len(same[0].edges) != 0 || same[0].cost != 0 {
		test.Errorf("src==dst returned %v, want one empty zero-cost path", same)
	}
	// Out-of-range node ids return no paths.
	if got := kShortestPaths(roadGraph, 0, domain.NodeID(roadGraph.NodeCount()), 3, freeFlowWeight); got != nil {
		test.Errorf("out-of-range dst returned %v, want nil", got)
	}
}

// looplessPath reports whether walking edges from src visits no node twice.
func looplessPath(roadGraph graph.Graph, src domain.NodeID, edges []domain.EdgeID) bool {
	seen := map[domain.NodeID]bool{src: true}
	at := src
	for _, edgeID := range edges {
		edge, ok := roadGraph.Edge(edgeID)
		if !ok || edge.From != at {
			return false
		}
		if seen[edge.To] {
			return false
		}
		seen[edge.To] = true
		at = edge.To
	}
	return true
}
