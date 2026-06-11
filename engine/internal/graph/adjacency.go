package graph

import (
	"fmt"

	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/domain"
)

// AdjacencyGraph is the immutable, adjacency-list implementation of the Graph
// port. It is backed by flat slices indexed by dense ids — node i lives at
// nodes[i], edge i at edges[i] — with out-edges stored in compressed-sparse-row
// (CSR) form so a node's neighbors are a contiguous run with no per-node slice
// header or map.
//
// After New returns, an AdjacencyGraph is read-only: it exposes no mutators and
// every accessor only reads its slices (Neighbors returns a freshly allocated
// slice, so a caller cannot reach the internal CSR storage). It is therefore
// safe for unsynchronized concurrent use by many goroutines — the engine loads
// one graph at startup and shares it read-only across all request handlers and
// across an Assign's worker goroutines (the R5 concurrency model).
//
// NearestNode and NearestEdge are part of the port but are spatial queries:
// NearestNode is backed by a k-d tree over node positions, built once in New
// (issue #24) and read-only thereafter. NearestEdge's map-matching R-tree
// arrives in Phase 7; until then it reports ok=false.
type AdjacencyGraph struct {
	nodes []Node
	edges []Edge

	// CSR out-adjacency. outEdgeIDs[outOffsets[n]:outOffsets[n+1]] are the ids
	// of the edges leaving node n, in increasing EdgeID order. outOffsets has
	// len == NodeCount+1; its trailing entry is len(outEdgeIDs).
	outOffsets []int
	outEdgeIDs []domain.EdgeID

	// kd is the immutable k-d tree over node positions backing NearestNode. It
	// is built once in New and is read-only thereafter, so NearestNode is safe
	// for unsynchronized concurrent queries (the R5 concurrency model). For the
	// degenerate zero-node graph it is an empty tree whose search reports
	// ok=false, so the field is never nil.
	kd *kdTree
}

// New builds an immutable AdjacencyGraph from dense node and edge slices. The
// caller (the export loader, issue #25) owns the export-contract validation;
// New enforces only the structural invariant this representation relies on:
// ids are dense with slice position == id (nodes[i].ID == i, edges[i].ID == i)
// and every edge endpoint references a node in range. It copies the inputs, so
// the returned graph cannot be mutated through the caller's slices.
func New(nodes []Node, edges []Edge) (*AdjacencyGraph, error) {
	for i := range nodes {
		if int(nodes[i].ID) != i {
			return nil, fmt.Errorf("graph.New: nodes not dense: nodes[%d].ID = %d, want %d", i, nodes[i].ID, i)
		}
	}
	for i := range edges {
		if int(edges[i].ID) != i {
			return nil, fmt.Errorf("graph.New: edges not dense: edges[%d].ID = %d, want %d", i, edges[i].ID, i)
		}
		if int(edges[i].From) < 0 || int(edges[i].From) >= len(nodes) {
			return nil, fmt.Errorf("graph.New: edge %d From = %d out of range [0,%d)", i, edges[i].From, len(nodes))
		}
		if int(edges[i].To) < 0 || int(edges[i].To) >= len(nodes) {
			return nil, fmt.Errorf("graph.New: edge %d To = %d out of range [0,%d)", i, edges[i].To, len(nodes))
		}
	}

	g := &AdjacencyGraph{
		nodes:      make([]Node, len(nodes)),
		edges:      make([]Edge, len(edges)),
		outOffsets: make([]int, len(nodes)+1),
		outEdgeIDs: make([]domain.EdgeID, len(edges)),
	}
	copy(g.nodes, nodes)
	copy(g.edges, edges)

	// CSR build: count out-degree per node into the shifted offset slot, prefix-
	// sum to turn counts into start offsets, then scatter edge ids. Iterating
	// edges in id order keeps each node's neighbor run in increasing EdgeID
	// order, which makes Neighbors deterministic.
	for i := range edges {
		g.outOffsets[int(edges[i].From)+1]++
	}
	for i := 1; i < len(g.outOffsets); i++ {
		g.outOffsets[i] += g.outOffsets[i-1]
	}
	cursor := make([]int, len(nodes))
	copy(cursor, g.outOffsets[:len(nodes)])
	for i := range edges {
		from := int(edges[i].From)
		g.outEdgeIDs[cursor[from]] = edges[i].ID
		cursor[from]++
	}

	// Build the spatial index once, over a copy of node positions keyed by
	// NodeID, so NearestNode is a pure read against immutable state. The k-d
	// tree owns its own point slice and never reaches back into g.nodes.
	kdPts := make([]kdPoint, len(g.nodes))
	for i := range g.nodes {
		kdPts[i] = kdPoint{pos: g.nodes[i].Pos, idx: int32(g.nodes[i].ID)}
	}
	g.kd = newKDTree(kdPts)

	return g, nil
}

// Neighbors returns the outgoing directed edges from node n in increasing
// EdgeID order, or nil if n is unknown or has no out-edges. The returned slice
// is freshly allocated and owned by the caller; mutating it does not affect the
// graph.
func (g *AdjacencyGraph) Neighbors(n domain.NodeID) []Edge {
	i := int(n)
	if i < 0 || i >= len(g.nodes) {
		return nil
	}
	lo, hi := g.outOffsets[i], g.outOffsets[i+1]
	if lo == hi {
		return nil
	}
	out := make([]Edge, hi-lo)
	for k, eid := range g.outEdgeIDs[lo:hi] {
		out[k] = g.edges[eid]
	}
	return out
}

// Edge returns the edge with the given id; ok is false if id is out of range.
func (g *AdjacencyGraph) Edge(id domain.EdgeID) (Edge, bool) {
	i := int(id)
	if i < 0 || i >= len(g.edges) {
		return Edge{}, false
	}
	return g.edges[i], true
}

// Node returns the node with the given id; ok is false if id is out of range.
func (g *AdjacencyGraph) Node(id domain.NodeID) (Node, bool) {
	i := int(id)
	if i < 0 || i >= len(g.nodes) {
		return Node{}, false
	}
	return g.nodes[i], true
}

// NodeCount returns the number of nodes.
func (g *AdjacencyGraph) NodeCount() int { return len(g.nodes) }

// EdgeCount returns the number of directed edges.
func (g *AdjacencyGraph) EdgeCount() int { return len(g.edges) }

// NearestNode resolves a coordinate to the node closest to it by great-circle
// (haversine) distance, using the immutable k-d tree built in New. ok is false
// only for a graph with no nodes. The query reads shared immutable state and
// allocates nothing shared, so it is safe to call concurrently from many
// goroutines without synchronization.
func (g *AdjacencyGraph) NearestNode(p domain.LatLon) (domain.NodeID, bool) {
	idx, ok := g.kd.nearest(p)
	if !ok {
		return 0, false
	}
	return domain.NodeID(idx), true
}

// NearestEdge snaps a GPS observation to the closest directed edge for
// map-matching. Its R-tree over edge geometry is built in Phase 7 (map-matching);
// until then it reports ok=false.
func (g *AdjacencyGraph) NearestEdge(_ domain.LatLon, _ float64) (domain.EdgeID, domain.LatLon, float64, bool) {
	return 0, domain.LatLon{}, 0, false
}

// Compile-time assertion: *AdjacencyGraph satisfies the Graph port.
var _ Graph = (*AdjacencyGraph)(nil)
