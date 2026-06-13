package graph

import "github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/domain"

// Node is a vertex in the road network (an intersection / OSM node).
type Node struct {
	ID  domain.NodeID
	Pos domain.LatLon
}

// Edge is a directed road segment — one of the two directed edges of a two-way
// street, or the single edge of a one-way. The BPR cost fields (FreeFlowS,
// CapacityVPH) are derived at export time from OSM tags; see docs/contracts.md §2.
type Edge struct {
	ID      domain.EdgeID
	Segment domain.SegmentID
	From    domain.NodeID
	To      domain.NodeID

	LengthM     float64 // segment length, meters
	FreeFlowS   float64 // free-flow traversal time, seconds
	CapacityVPH float64 // BPR capacity, vehicles/hour
}

// Graph is the read-only road-network port. Implementations are adapter-loaded
// from the pgRouting/GeoJSON export; the engine domain depends only on this
// interface, never on a concrete loader. Implementations must be safe for
// concurrent reads (the graph is immutable after load).
type Graph interface {
	// Neighbors returns the outgoing directed edges from node n.
	Neighbors(n domain.NodeID) []Edge

	// Edge returns the edge with the given id; ok is false if it is unknown.
	Edge(id domain.EdgeID) (e Edge, ok bool)

	// Node returns the node with the given id; ok is false if it is unknown.
	Node(id domain.NodeID) (n Node, ok bool)

	// NodeCount returns the number of nodes in the graph.
	NodeCount() int

	// EdgeCount returns the number of directed edges in the graph. EdgeIDs are
	// dense and contiguous (0..EdgeCount-1), so a consumer can size a flat
	// per-edge slice directly — e.g. make([]float64, g.EdgeCount()) for a flow
	// or congestion-load array indexed by EdgeID — without hashing. The
	// congestion adapter and every flow-accumulating router rely on this.
	EdgeCount() int

	// NearestNode returns the closest node to a coordinate. Used to resolve
	// route origin/destination endpoints (backed by a k-d tree over nodes).
	NearestNode(p domain.LatLon) (domain.NodeID, bool)

	// NearestEdge snaps a GPS observation to the closest directed edge, using
	// heading (degrees clockwise from north) to disambiguate the two directed
	// edges of a two-way street. Used for map-matching (backed by an R-tree over
	// edge geometries) — distinct from NearestNode, which snaps to vertices.
	// Returns the matched edge id, the snapped point, and the perpendicular
	// distance in meters; ok is false if nothing is within range.
	//
	// By convention the current *AdjacencyGraph implementation panics with an
	// issue reference until the Phase-7 R-tree lands, rather than overloading
	// ok=false (a genuine "no match") to also mean "not implemented".
	NearestEdge(p domain.LatLon, headingDeg float64) (id domain.EdgeID, snapped domain.LatLon, distM float64, ok bool)
}
