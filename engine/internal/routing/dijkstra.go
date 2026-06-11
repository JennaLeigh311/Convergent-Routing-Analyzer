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
// The relaxation loop is deliberately A*-wrappable: a future A* supplies an
// admissible heuristic by pushing priority = dist + h(node) while keeping dist as
// the settled cost, without changing the settle/relax structure here.
func dijkstra(g graph.Graph, src, dst domain.NodeID, weight weightFunc) (path []domain.EdgeID, cost float64, ok bool) {
	n := g.NodeCount()
	if int(src) < 0 || int(src) >= n || int(dst) < 0 || int(dst) >= n {
		return nil, 0, false
	}
	if src == dst {
		return []domain.EdgeID{}, 0, true
	}

	dist := make([]float64, n)
	for i := range dist {
		dist[i] = math.Inf(1)
	}
	// prevEdge[v] is the id of the edge used to settle v on the best path found
	// so far; -1 means "no predecessor yet" and is the path-reconstruction
	// terminator at src.
	prevEdge := make([]domain.EdgeID, n)
	for i := range prevEdge {
		prevEdge[i] = -1
	}

	dist[src] = 0
	pq := &priorityQueue{{node: src, priority: 0}}
	for pq.Len() > 0 {
		cur := heap.Pop(pq).(pqItem)
		if cur.priority > dist[cur.node] {
			continue // stale entry superseded by a shorter settle
		}
		if cur.node == dst {
			break // dst settled; its distance can no longer improve
		}
		for _, e := range g.Neighbors(cur.node) {
			relaxed := dist[cur.node] + weight(e)
			if relaxed < dist[e.To] {
				dist[e.To] = relaxed
				prevEdge[e.To] = e.ID
				heap.Push(pq, pqItem{node: e.To, priority: relaxed})
			}
		}
	}

	if math.IsInf(dist[dst], 1) {
		return nil, 0, false // dst unreachable from src
	}

	// Walk predecessors from dst back to src, then reverse into travel order.
	for at := dst; at != src; {
		eid := prevEdge[at]
		e, found := g.Edge(eid)
		if !found {
			return nil, 0, false // defensive: settled node must have a predecessor edge
		}
		path = append(path, eid)
		at = e.From
	}
	for i, j := 0, len(path)-1; i < j; i, j = i+1, j-1 {
		path[i], path[j] = path[j], path[i]
	}
	return path, dist[dst], true
}

// pqItem is one entry in the Dijkstra frontier: a node keyed by its tentative
// distance (priority). A* will set priority = dist + heuristic while the settle
// check above continues to compare against the pure dist slice.
type pqItem struct {
	node     domain.NodeID
	priority float64
}

// priorityQueue is a min-heap of pqItems ordered by priority, implementing
// container/heap.Interface. Entries are never updated in place; relaxation pushes
// a fresh (lower) entry and the stale one is discarded when popped.
type priorityQueue []pqItem

func (pq priorityQueue) Len() int           { return len(pq) }
func (pq priorityQueue) Less(i, j int) bool { return pq[i].priority < pq[j].priority }
func (pq priorityQueue) Swap(i, j int)      { pq[i], pq[j] = pq[j], pq[i] }
func (pq *priorityQueue) Push(x any)        { *pq = append(*pq, x.(pqItem)) }
func (pq *priorityQueue) Pop() any {
	old := *pq
	n := len(old)
	item := old[n-1]
	*pq = old[:n-1]
	return item
}
