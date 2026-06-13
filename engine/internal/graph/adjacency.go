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
// every accessor only reads its slices. Neighbors returns a freshly allocated
// slice (an isolated, mutable copy a caller may keep and modify), while
// OutEdgeIDs deliberately returns the internal CSR sub-slice directly as a
// read-only view for the allocation-free router hot path (issue #35). Exposing
// that aliased slice is safe precisely because the graph is immutable after New:
// the storage never changes, so an aliased read-only view cannot observe a torn
// write. The AdjacencyGraph is therefore safe for unsynchronized concurrent use
// by many goroutines — the engine loads one graph at startup and shares it
// read-only across all request handlers and across an Assign's worker goroutines
// (the R5 concurrency model).
//
// NearestNode and NearestEdge are part of the port but are spatial queries:
// NearestNode is backed by a k-d tree over node positions, built once in New
// (issue #24) and read-only thereafter. NearestEdge's map-matching R-tree
// arrives in Phase 7; until then it panics with an issue reference rather than
// returning ok=false (see its method doc for the convention).
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
	for index1 := range nodes {
		if int(nodes[index1].ID) != index1 {
			return nil, fmt.Errorf("graph.New: nodes not dense: nodes[%d].ID = %d, want %d", index1, nodes[index1].ID, index1)
		}
	}
	for index2 := range edges {
		if int(edges[index2].ID) != index2 {
			return nil, fmt.Errorf("graph.New: edges not dense: edges[%d].ID = %d, want %d", index2, edges[index2].ID, index2)
		}
		if int(edges[index2].From) < 0 || int(edges[index2].From) >= len(nodes) {
			return nil, fmt.Errorf("graph.New: edge %d From = %d out of range [0,%d)", index2, edges[index2].From, len(nodes))
		}
		if int(edges[index2].To) < 0 || int(edges[index2].To) >= len(nodes) {
			return nil, fmt.Errorf("graph.New: edge %d To = %d out of range [0,%d)", index2, edges[index2].To, len(nodes))
		}
	}

	graph := &AdjacencyGraph{
		nodes:      make([]Node, len(nodes)),
		edges:      make([]Edge, len(edges)),
		outOffsets: make([]int, len(nodes)+1),
		outEdgeIDs: make([]domain.EdgeID, len(edges)),
	}
	copy(graph.nodes, nodes)
	copy(graph.edges, edges)

	// CSR build: count out-degree per node into the shifted offset slot, prefix-
	// sum to turn counts into start offsets, then scatter edge ids. Iterating
	// edges in id order keeps each node's neighbor run in increasing EdgeID
	// order, which makes Neighbors deterministic.
	for index3 := range edges {
		graph.outOffsets[int(edges[index3].From)+1]++
	}
	for index4 := 1; index4 < len(graph.outOffsets); index4++ {
		graph.outOffsets[index4] += graph.outOffsets[index4-1]
	}
	cursor := make([]int, len(nodes))
	copy(cursor, graph.outOffsets[:len(nodes)])
	for index5 := range edges {
		from := int(edges[index5].From)
		graph.outEdgeIDs[cursor[from]] = edges[index5].ID
		cursor[from]++
	}

	// Build the spatial index once, over a copy of node positions keyed by
	// NodeID, so NearestNode is a pure read against immutable state. The k-d
	// tree owns its own point slice and never reaches back into g.nodes.
	kdPts := make([]kdPoint, len(graph.nodes))
	for index6 := range graph.nodes {
		// idx round-trips back to a NodeID in NearestNode. Dense ids (enforced
		// above: nodes[i].ID == i) guarantee the int32(NodeID)→...→NodeID(idx)
		// round-trip is exact. A future edge-sample reuse of kdPoint must NOT
		// blindly cast idx back to a NodeID — it would carry an edge index.
		kdPts[index6] = kdPoint{pos: graph.nodes[index6].Pos, idx: int32(graph.nodes[index6].ID)}
	}
	graph.kd = newKDTree(kdPts)

	return graph, nil
}

// Neighbors returns the outgoing directed edges from node n in increasing
// EdgeID order, or nil if n is unknown or has no out-edges. The returned slice
// is freshly allocated and owned by the caller; mutating it does not affect the
// graph.
func (graph *AdjacencyGraph) Neighbors(nodeID domain.NodeID) []Edge {
	index1 := int(nodeID)
	if index1 < 0 || index1 >= len(graph.nodes) {
		return nil
	}
	low, high := graph.outOffsets[index1], graph.outOffsets[index1+1]
	if low == high {
		return nil
	}
	out := make([]Edge, high-low)
	for index2, eid := range graph.outEdgeIDs[low:high] {
		out[index2] = graph.edges[eid]
	}
	return out
}

// OutEdgeIDs returns the ids of the outgoing directed edges from node n in
// increasing EdgeID order, or nil if n is unknown or has no out-edges. It is the
// zero-copy counterpart to Neighbors for the router hot path (issue #35): rather
// than allocating and struct-copying a fresh []Edge per call, it returns the
// internal CSR sub-slice outEdgeIDs[low:high] DIRECTLY — no allocation, no copy.
//
// The returned slice ALIASES the graph's internal storage and MUST be treated as
// read-only: the caller must not mutate its elements nor append in place to it
// (an append that overran low:high would scribble into a neighboring node's run).
// This aliasing is safe to expose because the graph is immutable after New: the
// CSR storage is never written again, so the view is safe for unsynchronized
// concurrent reads by many goroutines (the R5 concurrency model). Neighbors stays
// for callers that want an owned, isolated, mutable copy.
func (graph *AdjacencyGraph) OutEdgeIDs(nodeID domain.NodeID) []domain.EdgeID {
	index := int(nodeID)
	if index < 0 || index >= len(graph.nodes) {
		return nil
	}
	low, high := graph.outOffsets[index], graph.outOffsets[index+1]
	if low == high {
		return nil
	}
	return graph.outEdgeIDs[low:high]
}

// Edge returns the edge with the given id; ok is false if id is out of range.
func (graph *AdjacencyGraph) Edge(edgeID domain.EdgeID) (Edge, bool) {
	index := int(edgeID)
	if index < 0 || index >= len(graph.edges) {
		return Edge{}, false
	}
	return graph.edges[index], true
}

// Node returns the node with the given id; ok is false if id is out of range.
func (graph *AdjacencyGraph) Node(nodeID domain.NodeID) (Node, bool) {
	index := int(nodeID)
	if index < 0 || index >= len(graph.nodes) {
		return Node{}, false
	}
	return graph.nodes[index], true
}

// NodeCount returns the number of nodes.
func (graph *AdjacencyGraph) NodeCount() int { return len(graph.nodes) }

// EdgeCount returns the number of directed edges.
func (graph *AdjacencyGraph) EdgeCount() int { return len(graph.edges) }

// NearestNode resolves a coordinate to the node closest to it by great-circle
// (haversine) distance, using the immutable k-d tree built in New. ok is false
// only for a graph with no nodes (or a query carrying a NaN coordinate). The
// query reads shared immutable state and allocates nothing shared, so it is safe
// to call concurrently from many goroutines without synchronization.
//
// The result is exact provided the node set does not span the ±180° antimeridian
// (it lies within a <180°-wide longitude band), which holds for any single-region
// road network; see the kdTree pruning-admissibility doc for the seam caveat.
func (graph *AdjacencyGraph) NearestNode(point domain.LatLon) (domain.NodeID, bool) {
	idx, found := graph.kd.nearest(point)
	if !found {
		return 0, false
	}
	// idx is the dense NodeID packed in New; the round-trip is exact (see New).
	return domain.NodeID(idx), true
}

// NearestEdge snaps a GPS observation to the closest directed edge for
// map-matching. Its R-tree over edge geometry is built in Phase 7 (map-matching).
//
// By convention an unimplemented spatial query panics with an issue reference
// rather than returning ok=false: ok=false is a legitimate runtime answer ("the
// observation is outside every edge's range"), so overloading it to also mean
// "not implemented yet" makes a stub indistinguishable from a real no-match —
// the silent-degradation trap. Panicking instead makes a premature Phase-7
// integration fail loudly rather than silently resolve every observation to "no
// match". There are no production callers today, so this breaks nothing.
func (graph *AdjacencyGraph) NearestEdge(_ domain.LatLon, _ float64) (domain.EdgeID, domain.LatLon, float64, bool) {
	panic("graph.NearestEdge: not implemented (Phase 7 map-matching R-tree); see issue #36")
}

// Compile-time assertion: *AdjacencyGraph satisfies the Graph port.
var _ Graph = (*AdjacencyGraph)(nil)
