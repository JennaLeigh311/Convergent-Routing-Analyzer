package routing

import (
	"container/heap"
	"math"

	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/domain"
	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/graph"
)

// weightFunc returns the non-negative traversal weight (cost) of a directed
// edge. It is the single point of variation across the six strategies: they feed
// the same shortest-path core different weights (free-flow time, congested time,
// marginal cost, …) and otherwise share this relaxation loop. The free-flow
// baseline passes freeFlowWeight; later strategies close over a congestion
// snapshot.
//
// Weights MUST be non-negative — Dijkstra's correctness depends on it. Every §2
// edge field that feeds a weight (FreeFlowS, LengthM, CapacityVPH) is a finite
// positive number guaranteed by the loader, so a derived weight stays >= 0.
type weightFunc func(graph.Edge) float64

// dijkstra computes a minimum-weight directed path from src to dst over g, using
// weight for each edge. It returns the ordered edge ids of the chosen path and
// the summed weight. ok is false if dst is unreachable from src (or either node
// id is out of range); a src == dst request returns an empty path with zero cost.
//
// It is a lazy-deletion binary-heap Dijkstra: stale heap entries (a node popped
// with a distance worse than its settled distance) are skipped rather than
// decrease-key'd. Node ids are dense 0..NodeCount-1 (the loader guarantees this),
// so the per-node bookkeeping is flat slices indexed by NodeID — no hashing.
//
// The relaxation loop is deliberately A*-wrappable: each frontier entry carries
// both its settled cost g and its heap priority. Dijkstra sets priority == g; a
// future A* sets priority = g + h(node) for an admissible heuristic h. The
// staleness check compares the entry's g against the settled-cost slice (never the
// priority), so adding the heuristic reorders the heap without touching the
// settle/relax structure here.
func dijkstra(roadGraph graph.Graph, src, dst domain.NodeID, weight weightFunc) (path []domain.EdgeID, cost float64, found bool) {
	return dijkstraScratch(roadGraph, src, dst, weight, nil)
}

// dijkstraScratch is dijkstra with an optional caller-supplied scratch buffer.
// When scratch is nil it allocates fresh dist/prevEdge slices per call (the
// single-call Route path keeps that allocation-per-call shape, unchanged). When
// scratch is non-nil it reuses that buffer's dist/prevEdge slices across calls,
// so an iterative router running thousands of Dijkstras per Assign does not
// re-allocate two O(NodeCount) slices each time — the optimization deferred at
// the original dijkstra (see dijkstraScratch's reset below).
//
// A scratch buffer is single-goroutine state: it MUST NOT be shared across
// concurrent dijkstraScratch calls (one per worker — see newDijkstraScratch).
// Correctness is identical to a fresh allocation because the buffer is fully
// re-initialized (dist→+Inf, prevEdge→-1) at the top of every call.
func dijkstraScratch(roadGraph graph.Graph, src, dst domain.NodeID, weight weightFunc, scratch *dijkstraScratchBuffer) (path []domain.EdgeID, cost float64, found bool) {
	count := roadGraph.NodeCount()
	if int(src) < 0 || int(src) >= count || int(dst) < 0 || int(dst) >= count {
		return nil, 0, false
	}
	if src == dst {
		return []domain.EdgeID{}, 0, true
	}

	// dist[v] is the best settled cost to v so far; prevEdge[v] is the edge that
	// settled it (-1 = no predecessor yet, the path-reconstruction terminator at
	// src). With no scratch buffer these are allocated and initialized per call
	// (O(n)) — the original allocation-per-call shape that keeps an unsynchronized
	// single Route safe. With a scratch buffer they are reused across calls and
	// only re-initialized, saving the two allocations per Dijkstra on the iterative
	// hot path.
	var dist []float64
	var prevEdge []domain.EdgeID
	if scratch == nil {
		dist = make([]float64, count)
		prevEdge = make([]domain.EdgeID, count)
	} else {
		dist, prevEdge = scratch.reset(count)
	}
	for index1 := range dist {
		dist[index1] = math.Inf(1)
	}
	for index2 := range prevEdge {
		prevEdge[index2] = -1
	}

	dist[src] = 0
	queue := &priorityQueue{{node: src, g: 0, priority: 0}}
	for queue.Len() > 0 {
		cur := heap.Pop(queue).(pqItem)
		if cur.g > dist[cur.node] {
			continue // stale entry superseded by a shorter settle
		}
		if cur.node == dst {
			break // dst settled; its distance can no longer improve
		}
		// Zero-copy neighbor iteration (issue #35, resolved): OutEdgeIDs returns
		// the graph's internal CSR sub-slice of out-edge ids directly — no fresh
		// []Edge allocation per settled node — and we resolve each id to its Edge by
		// flat index. A benchmark over a representative grid confirmed Neighbors'
		// per-settle allocation was the hot-path bottleneck this removes. The CSR
		// view is read-only; this loop only reads it. Neighbors stays on the port
		// for callers wanting an owned, mutable copy.
		for _, edgeID := range roadGraph.OutEdgeIDs(cur.node) {
			edge1, ok := roadGraph.Edge(edgeID)
			if !ok {
				continue // defensive: CSR-stored ids are always in range, but never trust an id blindly
			}
			relaxed := dist[cur.node] + weight(edge1)
			if relaxed < dist[edge1.To] {
				dist[edge1.To] = relaxed
				prevEdge[edge1.To] = edge1.ID
				// priority == g for Dijkstra; an A* wrapper would push
				// g: relaxed, priority: relaxed + h(e.To).
				heap.Push(queue, pqItem{node: edge1.To, g: relaxed, priority: relaxed})
			}
		}
	}

	if math.IsInf(dist[dst], 1) {
		return nil, 0, false // dst unreachable from src
	}

	// Walk predecessors from dst back to src, then reverse into travel order.
	for atNodeID := dst; atNodeID != src; {
		eid := prevEdge[atNodeID]
		edge2, found := roadGraph.Edge(eid)
		if !found {
			return nil, 0, false // defensive: a settled node always has a predecessor edge; a missing one is a logic bug above, not a valid graph
		}
		path = append(path, eid)
		atNodeID = edge2.From
	}
	for index3, innerIndex := 0, len(path)-1; index3 < innerIndex; index3, innerIndex = index3+1, innerIndex-1 {
		path[index3], path[innerIndex] = path[innerIndex], path[index3]
	}
	return path, dist[dst], true
}

// pqItem is one entry in the Dijkstra frontier: a node with its settled cost g and
// its heap priority. For Dijkstra priority == g; an A* wrapper sets priority =
// g + h(node) while g stays the pure cost the staleness check compares against, so
// the heap orders by f = g + h without corrupting the settle decision.
type pqItem struct {
	node     domain.NodeID
	g        float64 // settled cost-so-far at push time; the staleness key
	priority float64 // heap key: g for Dijkstra, g + h(node) for A*
}

// priorityQueue is a min-heap of pqItems ordered by priority, implementing
// container/heap.Interface. Entries are never updated in place; relaxation pushes
// a fresh (lower) entry and the stale one is discarded when popped.
type priorityQueue []pqItem

func (queue priorityQueue) Len() int { return len(queue) }
func (queue priorityQueue) Less(index, innerIndex int) bool {
	return queue[index].priority < queue[innerIndex].priority
}
func (queue priorityQueue) Swap(index, innerIndex int) {
	queue[index], queue[innerIndex] = queue[innerIndex], queue[index]
}
func (queue *priorityQueue) Push(value any) { *queue = append(*queue, value.(pqItem)) }
func (queue *priorityQueue) Pop() any {
	old := *queue
	count := len(old)
	item := old[count-1]
	*queue = old[:count-1]
	return item
}
