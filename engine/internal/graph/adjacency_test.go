package graph_test

import (
	"sync"
	"testing"

	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/domain"
	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/graph"
)

// buildTestGraph returns a small dense graph: three nodes (0,1,2) and three
// directed edges — 0:0→1, 1:1→2, 2:0→2 — so node 0 has two out-edges (0 and 2,
// in that id order), node 1 has one (1), and node 2 has none.
func buildTestGraph(t *testing.T) *graph.AdjacencyGraph {
	t.Helper()
	nodes := []graph.Node{
		{ID: 0, Pos: domain.LatLon{Lat: 41.15, Lon: -8.61}},
		{ID: 1, Pos: domain.LatLon{Lat: 41.16, Lon: -8.62}},
		{ID: 2, Pos: domain.LatLon{Lat: 41.17, Lon: -8.63}},
	}
	edges := []graph.Edge{
		{ID: 0, Segment: "100:0:F", From: 0, To: 1, LengthM: 120, FreeFlowS: 12, CapacityVPH: 1800},
		{ID: 1, Segment: "100:1:F", From: 1, To: 2, LengthM: 200, FreeFlowS: 20, CapacityVPH: 1800},
		{ID: 2, Segment: "200:0:F", From: 0, To: 2, LengthM: 300, FreeFlowS: 30, CapacityVPH: 900},
	}
	g, err := graph.New(nodes, edges)
	if err != nil {
		t.Fatalf("graph.New: unexpected error: %v", err)
	}
	return g
}

func TestAdjacencyGraphTopology(t *testing.T) {
	g := buildTestGraph(t)

	if got := g.NodeCount(); got != 3 {
		t.Errorf("NodeCount() = %d, want 3", got)
	}
	if got := g.EdgeCount(); got != 3 {
		t.Errorf("EdgeCount() = %d, want 3", got)
	}

	// Node 0 has two out-edges, returned in increasing EdgeID order (0 then 2).
	n0 := g.Neighbors(0)
	if len(n0) != 2 || n0[0].ID != 0 || n0[1].ID != 2 {
		t.Fatalf("Neighbors(0) = %+v, want edge ids [0 2]", n0)
	}
	if n0[0].To != 1 || n0[1].To != 2 {
		t.Errorf("Neighbors(0) endpoints = [%d %d], want [1 2]", n0[0].To, n0[1].To)
	}
	if got := g.Neighbors(1); len(got) != 1 || got[0].ID != 1 {
		t.Errorf("Neighbors(1) = %+v, want one edge id 1", got)
	}
	if got := g.Neighbors(2); got != nil {
		t.Errorf("Neighbors(2) = %+v, want nil (no out-edges)", got)
	}
	if got := g.Neighbors(99); got != nil {
		t.Errorf("Neighbors(99) = %+v, want nil (out of range)", got)
	}

	if e, ok := g.Edge(1); !ok || e.From != 1 || e.To != 2 || e.Segment != "100:1:F" {
		t.Errorf("Edge(1) = (%+v, ok=%v), want edge 1→2 segment 100:1:F", e, ok)
	}
	if _, ok := g.Edge(3); ok {
		t.Error("Edge(3) ok = true, want false (out of range)")
	}
	if n, ok := g.Node(2); !ok || n.Pos.Lat != 41.17 {
		t.Errorf("Node(2) = (%+v, ok=%v), want node 2", n, ok)
	}
	if _, ok := g.Node(99); ok {
		t.Error("Node(99) ok = true, want false (out of range)")
	}
}

// TestAdjacencyGraphReadsAreIsolated proves the graph is immutable through its
// accessors: a caller mutating a returned Neighbors slice, or mutating an input
// slice after construction, must not change the graph.
func TestAdjacencyGraphReadsAreIsolated(t *testing.T) {
	nodes := []graph.Node{
		{ID: 0, Pos: domain.LatLon{Lat: 1, Lon: 1}},
		{ID: 1, Pos: domain.LatLon{Lat: 2, Lon: 2}},
	}
	edges := []graph.Edge{
		{ID: 0, Segment: "1:0:F", From: 0, To: 1, LengthM: 100, FreeFlowS: 10, CapacityVPH: 1800},
	}
	g, err := graph.New(nodes, edges)
	if err != nil {
		t.Fatalf("graph.New: %v", err)
	}

	// Mutating an input slice after New must not affect the graph (New copies).
	nodes[0].Pos.Lat = 999
	edges[0].LengthM = 999
	if n, _ := g.Node(0); n.Pos.Lat != 1 {
		t.Errorf("Node(0).Pos.Lat = %v after input mutation, want 1 (graph must copy inputs)", n.Pos.Lat)
	}
	if e, _ := g.Edge(0); e.LengthM != 100 {
		t.Errorf("Edge(0).LengthM = %v after input mutation, want 100", e.LengthM)
	}

	// Mutating a returned Neighbors slice must not affect subsequent reads.
	first := g.Neighbors(0)
	first[0].LengthM = 12345
	again := g.Neighbors(0)
	if again[0].LengthM != 100 {
		t.Errorf("Neighbors(0)[0].LengthM = %v after caller mutation, want 100 (returned slice must be isolated)", again[0].LengthM)
	}
}

func TestNewRejectsMalformedInput(t *testing.T) {
	good := graph.Node{ID: 0, Pos: domain.LatLon{Lat: 1, Lon: 1}}
	cases := []struct {
		name  string
		nodes []graph.Node
		edges []graph.Edge
	}{
		{
			name:  "non-dense node id",
			nodes: []graph.Node{{ID: 5, Pos: domain.LatLon{Lat: 1, Lon: 1}}},
			edges: nil,
		},
		{
			name:  "non-dense edge id",
			nodes: []graph.Node{good, {ID: 1, Pos: domain.LatLon{Lat: 2, Lon: 2}}},
			edges: []graph.Edge{{ID: 7, From: 0, To: 1}},
		},
		{
			name:  "edge endpoint out of range",
			nodes: []graph.Node{good, {ID: 1, Pos: domain.LatLon{Lat: 2, Lon: 2}}},
			edges: []graph.Edge{{ID: 0, From: 0, To: 9}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := graph.New(tc.nodes, tc.edges); err == nil {
				t.Errorf("graph.New(%s) = nil error, want rejection", tc.name)
			}
		})
	}
}

// TestAdjacencyGraphConcurrentReads exercises the immutable-shared-graph
// invariant: many goroutines read concurrently with no synchronization. Run
// under -race (CI Lane A does) this fails if any accessor mutates shared state.
func TestAdjacencyGraphConcurrentReads(t *testing.T) {
	g := buildTestGraph(t)
	const workers = 32
	var wg sync.WaitGroup
	wg.Add(workers)
	for w := 0; w < workers; w++ {
		go func() {
			defer wg.Done()
			for i := 0; i < 1000; i++ {
				if got := g.Neighbors(0); len(got) != 2 {
					t.Errorf("concurrent Neighbors(0) len = %d, want 2", len(got))
					return
				}
				_, _ = g.Edge(1)
				_, _ = g.Node(2)
				_ = g.NodeCount()
				_ = g.EdgeCount()
			}
		}()
	}
	wg.Wait()
}

// TestAdjacencyGraphSpatialStubs documents that the spatial queries are not yet
// implemented (NearestNode → issue #24, NearestEdge → Phase 7): both report
// ok=false rather than masquerading as implemented.
func TestAdjacencyGraphSpatialStubs(t *testing.T) {
	g := buildTestGraph(t)
	if _, ok := g.NearestNode(domain.LatLon{Lat: 41.15, Lon: -8.61}); ok {
		t.Error("NearestNode ok = true, want false (stub until #24)")
	}
	if _, _, _, ok := g.NearestEdge(domain.LatLon{Lat: 41.15, Lon: -8.61}, 90); ok {
		t.Error("NearestEdge ok = true, want false (stub until Phase 7)")
	}
}
