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

// heuristicFunc estimates the remaining cost from a node to the search
// destination. It is the A* seam over the shared shortest-path core: the
// relaxation loop adds h(node) to a frontier entry's heap priority (never to its
// settled cost g), so an admissible h reorders the heap toward the destination
// without changing which path is optimal. It MUST be admissible — h(node) ≤ the
// true shortest remaining cost to dst — or A* can return a non-optimal path; see
// astar.go for the admissible time-domain heuristic the A* router supplies.
//
// A nil heuristicFunc means "no heuristic": the core then sets priority == g and
// behaves as plain Dijkstra (see dijkstra). Threading nil rather than a separate
// code path is what lets Dijkstra and A* share one settle/relax loop — and the
// nil check is hoisted out of the per-relaxation hot path (see shortestPath) so
// plain Dijkstra pays no per-edge indirect call for the heuristic it does not use.
type heuristicFunc func(domain.NodeID) float64

// dijkstra computes a minimum-weight directed path from src to dst over g, using
// weight for each edge. It returns the ordered edge ids of the chosen path and
// the summed weight. ok is false if dst is unreachable from src (or either node
// id is out of range); a src == dst request returns an empty path with zero cost.
//
// It is the plain (no-heuristic, no-scratch) specialization of shortestPath: it
// passes a nil heuristicFunc (so priority == g, exactly Dijkstra) and a nil
// scratch buffer (so dist/prevEdge are allocated per call, the unsynchronized
// single-call Route shape). The single-call Route paths of every router call this
// — or shortestPath with nil scratch — so they share the one lazy-deletion
// binary-heap settle/relax loop with the A* path rather than duplicating it.
func dijkstra(roadGraph graph.Graph, src, dst domain.NodeID, weight weightFunc) (path []domain.EdgeID, cost float64, found bool) {
	return shortestPath(roadGraph, src, dst, weight, nil, nil)
}

// dijkstraScratch is dijkstra with an optional caller-supplied scratch buffer and
// no heuristic. When scratch is nil it allocates fresh dist/prevEdge slices per
// call (the single-call Route path's allocation-per-call shape, unchanged); when
// scratch is non-nil it reuses that buffer's slices across calls, so an iterative
// router running thousands of Dijkstras per Assign does not re-allocate two
// O(NodeCount) slices each time.
//
// A scratch buffer is single-goroutine state: it MUST NOT be shared across
// concurrent dijkstraScratch calls (one per worker — see newDijkstraScratch).
// Correctness is identical to a fresh allocation because the buffer is fully
// re-initialized (dist→+Inf, prevEdge→-1) at the top of every call. It is the
// no-heuristic specialization of shortestPath, threading the scratch through.
func dijkstraScratch(roadGraph graph.Graph, src, dst domain.NodeID, weight weightFunc, scratch *dijkstraScratchBuffer) (path []domain.EdgeID, cost float64, found bool) {
	return shortestPath(roadGraph, src, dst, weight, nil, scratch)
}

// shortestPath is the shared shortest-path core for both Dijkstra and A*. It
// folds the two orthogonal seams the strategies vary:
//
//   - heuristic — the A* seam. With a nil heuristic it is exactly Dijkstra; with
//     an admissible heuristicFunc h it is A*, identical except that each frontier
//     entry's heap priority is g + h(node) instead of g. The heuristic enters ONLY
//     the heap priority: each entry carries both its settled cost g and its heap
//     priority, and the staleness check compares the entry's g against the
//     settled-cost slice (never the priority). So adding an admissible h reorders
//     the heap to expand nodes closer to dst first — pruning the frontier —
//     without changing which path is optimal or the settle/relax structure.
//   - scratch — the allocation seam. With a nil scratch buffer dist/prevEdge are
//     allocated fresh per call (the unsynchronized single-call Route shape); with a
//     non-nil buffer they are reused across calls (the iterative-router hot path —
//     see dijkstraScratch). It is single-goroutine state and never shared across
//     concurrent calls.
//
// The two are independent: heuristic chooses the heap priority key, scratch
// chooses the dist/prevEdge backing storage. dijkstra passes both nil; the A*
// router passes its heuristic and nil scratch; the iterative routers pass nil
// heuristic and a per-worker scratch.
//
// It returns the ordered edge ids of the chosen path and the summed weight. ok is
// false if dst is unreachable from src (or either node id is out of range); a
// src == dst request returns an empty path with zero cost.
//
// It is a lazy-deletion binary-heap search: stale heap entries (a node popped with
// a distance worse than its settled distance) are skipped rather than
// decrease-key'd. Node ids are dense 0..NodeCount-1 (the loader guarantees this),
// so the per-node bookkeeping is flat slices indexed by NodeID — no hashing.
func shortestPath(roadGraph graph.Graph, src, dst domain.NodeID, weight weightFunc, heuristic heuristicFunc, scratch *dijkstraScratchBuffer) (path []domain.EdgeID, cost float64, found bool) {
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
	// The nil-heuristic check is hoisted out of the per-relaxation loop into the
	// two loop variants below: plain Dijkstra (heuristic == nil) pushes priority ==
	// g with no per-edge indirect call, while A* (heuristic != nil) pushes
	// priority = g + h(node). Branching once here keeps the plain-Dijkstra hot path
	// (naive/reactive/iterative routers) free of the per-edge heuristic dispatch the
	// folded-closure form taxed it with, with identical behavior.
	if heuristic == nil {
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
			// []Edge allocation per settled node. The CSR view is read-only.
			for _, edgeID := range roadGraph.OutEdgeIDs(cur.node) {
				edge1, ok := roadGraph.Edge(edgeID)
				if !ok {
					continue // defensive: CSR-stored ids are always in range, but never trust an id blindly
				}
				relaxed := dist[cur.node] + weight(edge1)
				if relaxed < dist[edge1.To] {
					dist[edge1.To] = relaxed
					prevEdge[edge1.To] = edge1.ID
					// priority == g for Dijkstra: no heuristic term.
					heap.Push(queue, pqItem{node: edge1.To, g: relaxed, priority: relaxed})
				}
			}
		}
	} else {
		// The src entry's priority is g + h(src) = h(src): the admissible
		// remaining-cost estimate that biases the heap toward dst.
		queue := &priorityQueue{{node: src, g: 0, priority: heuristic(src)}}
		for queue.Len() > 0 {
			cur := heap.Pop(queue).(pqItem)
			if cur.g > dist[cur.node] {
				continue // stale entry superseded by a shorter settle
			}
			if cur.node == dst {
				break // dst settled; its distance can no longer improve
			}
			for _, edgeID := range roadGraph.OutEdgeIDs(cur.node) {
				edge1, ok := roadGraph.Edge(edgeID)
				if !ok {
					continue // defensive: CSR-stored ids are always in range, but never trust an id blindly
				}
				relaxed := dist[cur.node] + weight(edge1)
				if relaxed < dist[edge1.To] {
					dist[edge1.To] = relaxed
					prevEdge[edge1.To] = edge1.ID
					// priority = g + h(To): the f = g + h that biases the heap toward
					// dst. g stays the pure cost the staleness check above compares.
					heap.Push(queue, pqItem{node: edge1.To, g: relaxed, priority: relaxed + heuristic(edge1.To)})
				}
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
// its heap priority. For Dijkstra priority == g; A* sets priority = g + h(node)
// while g stays the pure cost the staleness check compares against, so the heap
// orders by f = g + h without corrupting the settle decision.
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
