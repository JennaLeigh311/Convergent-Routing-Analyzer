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

func (fakeImpl *fakeGraph) Neighbors(nodeID domain.NodeID) []graph.Edge {
	var out []graph.Edge
	for _, edge := range fakeImpl.edges {
		if edge.From == nodeID {
			out = append(out, edge)
		}
	}
	return out
}

func (fakeImpl *fakeGraph) OutEdgeIDs(nodeID domain.NodeID) []domain.EdgeID {
	var out []domain.EdgeID
	for _, edge := range fakeImpl.edges {
		if edge.From == nodeID {
			out = append(out, edge.ID)
		}
	}
	return out
}

func (fakeImpl *fakeGraph) Edge(edgeID domain.EdgeID) (graph.Edge, bool) {
	edge, found := fakeImpl.edges[edgeID]
	return edge, found
}
func (fakeImpl *fakeGraph) Node(nodeID domain.NodeID) (graph.Node, bool) {
	node, found := fakeImpl.nodes[nodeID]
	return node, found
}
func (fakeImpl *fakeGraph) NodeCount() int { return len(fakeImpl.nodes) }
func (fakeImpl *fakeGraph) EdgeCount() int { return len(fakeImpl.edges) }

func (fakeImpl *fakeGraph) NearestNode(_ domain.LatLon) (domain.NodeID, bool) {
	for nodeID := range fakeImpl.nodes {
		return nodeID, true
	}
	return 0, false
}

func (fakeImpl *fakeGraph) NearestEdge(_ domain.LatLon, _ float64) (domain.EdgeID, domain.LatLon, float64, bool) {
	for edgeID, edge := range fakeImpl.edges {
		var snapped domain.LatLon
		if node, found := fakeImpl.nodes[edge.From]; found {
			snapped = node.Pos
		}
		return edgeID, snapped, 0, true
	}
	return 0, domain.LatLon{}, 0, false
}

// Compile-time assertion: *fakeGraph satisfies the Graph port.
var _ graph.Graph = (*fakeGraph)(nil)

func TestFakeGraphSatisfiesPort(test *testing.T) {
	fakeImpl := &fakeGraph{
		// Dense, contiguous ids per the port contract: NodeIDs 0..NodeCount-1
		// and EdgeIDs 0..EdgeCount-1, so NodeCount()/EdgeCount() == max id + 1
		// and the double models the invariant real loaders must honor.
		nodes: map[domain.NodeID]graph.Node{
			0: {ID: 0, Pos: domain.LatLon{Lat: 41.15, Lon: -8.61}},
			1: {ID: 1, Pos: domain.LatLon{Lat: 41.16, Lon: -8.62}},
		},
		edges: map[domain.EdgeID]graph.Edge{
			0: {ID: 0, Segment: "123:0:F", From: 0, To: 1, LengthM: 120, FreeFlowS: 12, CapacityVPH: 1800},
		},
	}

	if got := fakeImpl.NodeCount(); got != 2 {
		test.Fatalf("NodeCount() = %d, want 2", got)
	}
	if got := fakeImpl.EdgeCount(); got != 1 {
		test.Fatalf("EdgeCount() = %d, want 1", got)
	}
	if got := fakeImpl.Neighbors(0); len(got) != 1 || got[0].ID != 0 {
		test.Fatalf("Neighbors(0) = %+v, want one edge id 0", got)
	}
	if _, found1 := fakeImpl.Edge(0); !found1 {
		test.Fatal("Edge(0) ok = false, want true")
	}
	if _, found2 := fakeImpl.Edge(999); found2 {
		test.Fatal("Edge(999) ok = true, want false")
	}
	if edgeID, _, _, found3 := fakeImpl.NearestEdge(domain.LatLon{Lat: 41.15, Lon: -8.61}, 90); !found3 || edgeID != 0 {
		test.Fatalf("NearestEdge() = (%d, ok=%v), want (0, true)", edgeID, found3)
	}
}
