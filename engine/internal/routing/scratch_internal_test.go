package routing

import (
	"testing"

	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/domain"
	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/graph"
)

// loadToyGraphInternal loads the shared toy fixture from inside package routing
// (the external _test files use loadToyGraph). The module testdata dir resolves at
// ../../testdata from this package.
func loadToyGraphInternal(test *testing.T) *graph.AdjacencyGraph {
	test.Helper()
	roadGraph, _, err := graph.LoadEdgeAttributesGeoJSONFile("../../testdata/toy_network.geojson", graph.WithExpectedBounds(-74, -73, 40, 41))
	if err != nil {
		test.Fatalf("toy_network.geojson must load, got: %v", err)
	}
	return roadGraph
}

// TestDijkstraScratchMatchesFreshAllocation is the correctness anchor for the
// per-goroutine scratch buffer: routing with a reused scratch buffer must produce
// the EXACT same path and cost as the allocate-per-call path, even when the buffer
// is reused across calls (its contents from a previous call must not leak, since
// dijkstraScratch re-initializes dist/prevEdge every call). It routes several
// node pairs through one shared buffer and compares each against a fresh-alloc
// dijkstra over the same pair.
func TestDijkstraScratchMatchesFreshAllocation(test *testing.T) {
	roadGraph := loadToyGraphInternal(test)
	scratch := newDijkstraScratch(roadGraph.NodeCount())

	pairs := []struct{ src, dst domain.NodeID }{
		{0, 2}, // 2-hop motorway
		{3, 4}, // single hop
		{0, 2}, // repeat, to prove buffer reuse does not leak prior state
		{0, 0}, // same node
	}
	for _, pair := range pairs {
		wantPath, wantCost, wantFound := dijkstra(roadGraph, pair.src, pair.dst, freeFlowWeight)
		gotPath, gotCost, gotFound := dijkstraScratch(roadGraph, pair.src, pair.dst, freeFlowWeight, scratch)

		if gotFound != wantFound || gotCost != wantCost {
			test.Errorf("scratch route %d->%d = (cost %v, found %v), want (cost %v, found %v)", pair.src, pair.dst, gotCost, gotFound, wantCost, wantFound)
		}
		if len(gotPath) != len(wantPath) {
			test.Fatalf("scratch route %d->%d path = %v, want %v", pair.src, pair.dst, gotPath, wantPath)
		}
		for index := range wantPath {
			if gotPath[index] != wantPath[index] {
				test.Errorf("scratch route %d->%d path[%d] = %d, want %d", pair.src, pair.dst, index, gotPath[index], wantPath[index])
			}
		}
	}
}

// TestDijkstraScratchGrows confirms a buffer built smaller than the graph grows on
// first use rather than panicking on an out-of-range index, so an undersized
// scratch is self-healing.
func TestDijkstraScratchGrows(test *testing.T) {
	roadGraph := loadToyGraphInternal(test)
	scratch := newDijkstraScratch(0) // deliberately too small

	gotPath, gotCost, gotFound := dijkstraScratch(roadGraph, 0, 2, freeFlowWeight, scratch)
	wantPath, wantCost, wantFound := dijkstra(roadGraph, 0, 2, freeFlowWeight)
	if gotFound != wantFound || gotCost != wantCost || len(gotPath) != len(wantPath) {
		test.Errorf("grown-scratch route = (%v, %v, %v), want (%v, %v, %v)", gotPath, gotCost, gotFound, wantPath, wantCost, wantFound)
	}
}

// TestPrefetchOD confirms the OD-prefetch helper resolves every endpoint to a node
// id once and reports a clear error (never a partial slice) when an endpoint snaps
// to no node — the resolve-once substrate the iterative routers route over.
func TestPrefetchOD(test *testing.T) {
	roadGraph := loadToyGraphInternal(test)
	node0, _ := roadGraph.Node(0)
	node2, _ := roadGraph.Node(2)

	pairs, err := prefetchOD(roadGraph, []RouteRequest{{ID: "a", From: node0.Pos, To: node2.Pos}}, "test")
	if err != nil {
		test.Fatalf("prefetchOD() error = %v", err)
	}
	if len(pairs) != 1 {
		test.Fatalf("prefetchOD() returned %d pairs, want 1", len(pairs))
	}
	if pairs[0].src != 0 || pairs[0].dst != 2 {
		test.Errorf("prefetchOD() pair = %+v, want {src:0 dst:2}", pairs[0])
	}
}

// TestRequestWeight pins the weight defaulting: a positive Weight is used as-is; a
// zero or negative Weight floors to 1.0, so a batch built without weights is the
// conventional one-vehicle-per-request flow.
func TestRequestWeight(test *testing.T) {
	cases := []struct {
		weight float64
		want   float64
	}{
		{weight: 0, want: 1},
		{weight: -3, want: 1},
		{weight: 2.5, want: 2.5},
	}
	for _, testCase := range cases {
		if got := RequestWeight(RouteRequest{Weight: testCase.weight}); got != testCase.want {
			test.Errorf("RequestWeight(Weight=%v) = %v, want %v", testCase.weight, got, testCase.want)
		}
	}
}
