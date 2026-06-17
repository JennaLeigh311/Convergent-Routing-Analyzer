package routing

import (
	"math"
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
	if !edgesEqual(paths[0].edges, []domain.EdgeID{1, 2}) {
		test.Errorf("paths[0].edges = %v, want [1 2] (the fast motorway path)", paths[0].edges)
	}
	if math.Abs(paths[0].cost-32.4) > 1e-9 {
		test.Errorf("paths[0].cost = %.4f, want 32.4", paths[0].cost)
	}
	// The second path is the direct residential edge (edge 0; 108.0).
	if !edgesEqual(paths[1].edges, []domain.EdgeID{0}) {
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
			if edgesEqual(paths[index].edges, paths[innerIndex].edges) {
				test.Errorf("paths[%d] and paths[%d] are identical: %v", index, innerIndex, paths[index].edges)
			}
		}
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
