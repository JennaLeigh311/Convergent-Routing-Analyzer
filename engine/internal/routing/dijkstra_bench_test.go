package routing

// This benchmark is the gate for issue #35. Issue #35 forbids implementing the
// zero-copy neighbor accessor speculatively: it must be justified by a
// benchmark-confirmed allocation hotspot. The existing cmd/benchmark harness is
// a 5-node smoke test, far too small to settle enough nodes for Neighbors'
// per-settled-node allocation to dominate. So we build a representative-sized
// synthetic grid here and measure a full Dijkstra traversal over it.
//
// BenchmarkDijkstraNeighbors traverses via a Neighbors-based core (the pre-#35
// hot path, preserved verbatim in dijkstraViaNeighbors below). BenchmarkDijkstra
// runs the real dijkstra, which after #35 uses the zero-copy OutEdgeIDs accessor.
// Both run the same corner-to-corner traversal over the same grid with
// b.ReportAllocs(), so -benchmem shows the allocs/op delta that #35 removes.

import (
	"container/heap"
	"math"
	"testing"

	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/domain"
	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/graph"
)

// buildGridGraph constructs a sideLen x sideLen 4-neighbor lattice as a directed
// road graph with dense ids. Each interior node has up to four out-edges (N/E/S/W
// to its grid neighbors), so a corner-to-corner Dijkstra settles essentially
// every node and calls the neighbor accessor once per settle — the shape that
// makes Neighbors' allocation the measured cost. Node (row, col) gets id
// row*sideLen + col; edges are appended in dense id order. Geometry is a small
// lat/lon step per grid cell so weights (FreeFlowS) are positive and finite.
func buildGridGraph(sideLen int) *graph.AdjacencyGraph {
	nodeCount := sideLen * sideLen
	nodes := make([]graph.Node, nodeCount)
	for row := 0; row < sideLen; row++ {
		for col := 0; col < sideLen; col++ {
			id := domain.NodeID(row*sideLen + col)
			nodes[id] = graph.Node{
				ID:  id,
				Pos: domain.LatLon{Lat: 41.0 + float64(row)*0.001, Lon: -8.0 + float64(col)*0.001},
			}
		}
	}

	// Append edges in increasing-id order so graph.New's dense-id invariant holds.
	// Each directed edge gets a uniform free-flow time; the absolute value is
	// irrelevant to the allocation measurement, only that it is positive.
	var edges []graph.Edge
	addEdge := func(from, to domain.NodeID) {
		edges = append(edges, graph.Edge{
			ID:          domain.EdgeID(len(edges)),
			Segment:     domain.SegmentID("grid"),
			From:        from,
			To:          to,
			LengthM:     100,
			FreeFlowS:   10,
			CapacityVPH: 1800,
		})
	}
	for row := 0; row < sideLen; row++ {
		for col := 0; col < sideLen; col++ {
			from := domain.NodeID(row*sideLen + col)
			if row+1 < sideLen {
				addEdge(from, domain.NodeID((row+1)*sideLen+col))
			}
			if row-1 >= 0 {
				addEdge(from, domain.NodeID((row-1)*sideLen+col))
			}
			if col+1 < sideLen {
				addEdge(from, domain.NodeID(row*sideLen+col+1))
			}
			if col-1 >= 0 {
				addEdge(from, domain.NodeID(row*sideLen+col-1))
			}
		}
	}

	roadGraph, err := graph.New(nodes, edges)
	if err != nil {
		panic("buildGridGraph: graph.New: " + err.Error())
	}
	return roadGraph
}

// dijkstraViaNeighbors is a frozen copy of the pre-#35 hot path: identical to
// dijkstra except the relaxation loop iterates roadGraph.Neighbors(cur.node),
// which allocates and struct-copies a fresh []Edge per settled node. It exists
// only so the benchmark can measure the allocation the zero-copy OutEdgeIDs path
// removes; production code must not call it.
func dijkstraViaNeighbors(roadGraph graph.Graph, src, dst domain.NodeID, weight weightFunc) (path []domain.EdgeID, cost float64, found bool) {
	count := roadGraph.NodeCount()
	if int(src) < 0 || int(src) >= count || int(dst) < 0 || int(dst) >= count {
		return nil, 0, false
	}
	if src == dst {
		return []domain.EdgeID{}, 0, true
	}

	dist := make([]float64, count)
	for index1 := range dist {
		dist[index1] = math.Inf(1)
	}
	prevEdge := make([]domain.EdgeID, count)
	for index2 := range prevEdge {
		prevEdge[index2] = -1
	}

	dist[src] = 0
	queue := &priorityQueue{{node: src, g: 0, priority: 0}}
	for queue.Len() > 0 {
		cur := heap.Pop(queue).(pqItem)
		if cur.g > dist[cur.node] {
			continue
		}
		if cur.node == dst {
			break
		}
		for _, edge1 := range roadGraph.Neighbors(cur.node) {
			relaxed := dist[cur.node] + weight(edge1)
			if relaxed < dist[edge1.To] {
				dist[edge1.To] = relaxed
				prevEdge[edge1.To] = edge1.ID
				heap.Push(queue, pqItem{node: edge1.To, g: relaxed, priority: relaxed})
			}
		}
	}

	if math.IsInf(dist[dst], 1) {
		return nil, 0, false
	}
	for atNodeID := dst; atNodeID != src; {
		eid := prevEdge[atNodeID]
		edge2, ok := roadGraph.Edge(eid)
		if !ok {
			return nil, 0, false
		}
		path = append(path, eid)
		atNodeID = edge2.From
	}
	for index3, innerIndex := 0, len(path)-1; index3 < innerIndex; index3, innerIndex = index3+1, innerIndex-1 {
		path[index3], path[innerIndex] = path[innerIndex], path[index3]
	}
	return path, dist[dst], true
}

// benchGridSide sizes the synthetic grid: 64x64 = 4096 nodes, ~16K directed
// edges, large enough that a corner-to-corner traversal settles thousands of
// nodes and the per-settle neighbor allocation dominates the Neighbors path.
const benchGridSide = 64

// BenchmarkDijkstraNeighbors is the pre-#35 baseline: a full corner-to-corner
// Dijkstra that allocates a fresh []Edge per settled node via Neighbors. Its
// allocs/op is the hotspot #35 targets.
func BenchmarkDijkstraNeighbors(b *testing.B) {
	roadGraph := buildGridGraph(benchGridSide)
	src := domain.NodeID(0)
	dst := domain.NodeID(benchGridSide*benchGridSide - 1)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _, found := dijkstraViaNeighbors(roadGraph, src, dst, freeFlowWeight)
		if !found {
			b.Fatal("dijkstraViaNeighbors: dst unreachable on a fully connected grid")
		}
	}
}

// BenchmarkDijkstra is the post-#35 path: the real dijkstra, which iterates the
// zero-copy OutEdgeIDs CSR sub-slice and fetches each Edge by id, allocating no
// per-settled-node neighbor slice. Compared against BenchmarkDijkstraNeighbors
// under -benchmem it shows the allocs/op #35 removes.
func BenchmarkDijkstra(b *testing.B) {
	roadGraph := buildGridGraph(benchGridSide)
	src := domain.NodeID(0)
	dst := domain.NodeID(benchGridSide*benchGridSide - 1)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _, found := dijkstra(roadGraph, src, dst, freeFlowWeight)
		if !found {
			b.Fatal("dijkstra: dst unreachable on a fully connected grid")
		}
	}
}
