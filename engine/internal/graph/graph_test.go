package graph_test

import (
	"testing"

	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/domain"
	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/graph"
)

// fakeGraph is a minimal Graph used to prove the port is satisfiable and to
// exercise the calling convention. It is not a real road network.
type fakeGraph struct {
	nodes map[domain.NodeID]graph.Node
	edges map[domain.EdgeID]graph.Edge
}

func (g *fakeGraph) Neighbors(n domain.NodeID) []graph.Edge {
	var out []graph.Edge
	for _, e := range g.edges {
		if e.From == n {
			out = append(out, e)
		}
	}
	return out
}

func (g *fakeGraph) Edge(id domain.EdgeID) (graph.Edge, bool) { e, ok := g.edges[id]; return e, ok }
func (g *fakeGraph) Node(id domain.NodeID) (graph.Node, bool) { n, ok := g.nodes[id]; return n, ok }
func (g *fakeGraph) NodeCount() int                           { return len(g.nodes) }
func (g *fakeGraph) EdgeCount() int                           { return len(g.edges) }

func (g *fakeGraph) NearestNode(_ domain.LatLon) (domain.NodeID, bool) {
	for id := range g.nodes {
		return id, true
	}
	return 0, false
}

func (g *fakeGraph) NearestEdge(_ domain.LatLon, _ float64) (domain.EdgeID, domain.LatLon, float64, bool) {
	for id, e := range g.edges {
		var snapped domain.LatLon
		if n, ok := g.nodes[e.From]; ok {
			snapped = n.Pos
		}
		return id, snapped, 0, true
	}
	return 0, domain.LatLon{}, 0, false
}

// Compile-time assertion: *fakeGraph satisfies the Graph port.
var _ graph.Graph = (*fakeGraph)(nil)

func TestFakeGraphSatisfiesPort(t *testing.T) {
	g := &fakeGraph{
		nodes: map[domain.NodeID]graph.Node{
			1: {ID: 1, Pos: domain.LatLon{Lat: 41.15, Lon: -8.61}},
			2: {ID: 2, Pos: domain.LatLon{Lat: 41.16, Lon: -8.62}},
		},
		edges: map[domain.EdgeID]graph.Edge{
			10: {ID: 10, Segment: "123:0:F", From: 1, To: 2, LengthM: 120, FreeFlowS: 12, CapacityVPH: 1800},
		},
	}

	if got := g.NodeCount(); got != 2 {
		t.Fatalf("NodeCount() = %d, want 2", got)
	}
	if got := g.EdgeCount(); got != 1 {
		t.Fatalf("EdgeCount() = %d, want 1", got)
	}
	if got := g.Neighbors(1); len(got) != 1 || got[0].ID != 10 {
		t.Fatalf("Neighbors(1) = %+v, want one edge id 10", got)
	}
	if _, ok := g.Edge(10); !ok {
		t.Fatal("Edge(10) ok = false, want true")
	}
	if _, ok := g.Edge(999); ok {
		t.Fatal("Edge(999) ok = true, want false")
	}
	if id, _, _, ok := g.NearestEdge(domain.LatLon{Lat: 41.15, Lon: -8.61}, 90); !ok || id != 10 {
		t.Fatalf("NearestEdge() = (%d, ok=%v), want (10, true)", id, ok)
	}
}
