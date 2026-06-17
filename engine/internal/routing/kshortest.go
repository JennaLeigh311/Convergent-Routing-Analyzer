package routing

import (
	"container/heap"
	"math"

	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/domain"
	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/graph"
)

// kPath is one loopless path produced by Yen's algorithm: its ordered directed
// edges (origin→destination) and the summed weight it was found under. It is the
// k-shortest-core counterpart of a single Route — multipath turns the chosen
// kPath into a Route, carrying the request id, once it has picked which of the K
// paths a request takes.
type kPath struct {
	// edges is the ordered directed edges from src to dst. An empty slice is the
	// degenerate src == dst path (cost 0); a non-empty path is loopless (no node
	// repeats), which Yen guarantees by construction (see kShortestPaths).
	edges []domain.EdgeID

	// cost is the summed weight of edges under the weightFunc the search ran on —
	// the same "cost the path was optimized against" semantics as Route.CostS, not
	// a realized travel time.
	cost float64
}

// kShortestPaths returns up to k loopless paths from src to dst over roadGraph,
// in NON-DECREASING cost order, using Yen's algorithm. It is the multipath
// router's k-shortest core (algorithm catalog row 6): given K alternative paths,
// multipath spreads demand across them.
//
// +Inf-weight MASKING, not a forked Dijkstra (issue #75). Yen repeatedly needs a
// shortest path with some edges and some nodes REMOVED (the spur search must avoid
// the edges already taken by previously-found paths sharing the same root, and the
// nodes already on the root path, so each new path is loopless and distinct). We
// get that removal WITHOUT changing the dijkstra signature or forking the core:
// maskedWeight wraps the base weightFunc to return +Inf for a removed edge (and
// for every out-edge of a removed node), so Dijkstra's relaxation never improves a
// distance through it and the edge/node is skipped naturally — a masked edge can
// never lie on a finite-cost path. This reuses the one settle/relax loop the whole
// package shares.
//
// k <= 0 returns no paths. src == dst returns a single empty (zero-cost) path. If
// fewer than k loopless paths exist (the usual case on a small or sparse graph),
// it returns as many as exist — never pads. The base weight MUST be non-negative
// (Dijkstra's correctness requirement); the masking only ever ADDS +Inf, so the
// wrapped weight stays non-negative.
func kShortestPaths(roadGraph graph.Graph, src, dst domain.NodeID, k int, weight weightFunc) []kPath {
	if k <= 0 {
		return nil
	}
	count := roadGraph.NodeCount()
	if int(src) < 0 || int(src) >= count || int(dst) < 0 || int(dst) >= count {
		return nil
	}
	if src == dst {
		return []kPath{{edges: []domain.EdgeID{}, cost: 0}}
	}

	// A holds the accepted k-shortest paths in increasing cost order. The first is
	// the plain shortest path; each subsequent one is the cheapest spur deviation.
	first, firstCost, found := dijkstra(roadGraph, src, dst, weight)
	if !found {
		return nil // dst unreachable: no paths at all
	}
	accepted := []kPath{{edges: first, cost: firstCost}}

	// candidates is the min-heap of spur deviations not yet accepted (Yen's set B),
	// ordered by total cost. A seq counter breaks cost ties deterministically by
	// insertion order so the K-set is reproducible run to run.
	candidates := &candidateHeap{}
	heap.Init(candidates)
	seenCandidate := make(map[string]bool) // dedupe identical spur paths across iterations
	seq := 0

	for len(accepted) < k {
		prev := accepted[len(accepted)-1].edges

		// Each spur node is the From-node of one edge on the previous accepted path
		// (plus its destination is implicit at the path tail). The root path is the
		// prefix of prev up to (but excluding) the spur edge.
		for spurIndex := 0; spurIndex < len(prev); spurIndex++ {
			spurNode := edgeFrom(roadGraph, prev[spurIndex])
			rootEdges := prev[:spurIndex]

			maskedEdges := make(map[domain.EdgeID]bool)
			// Remove the edges that would retrace an already-found path sharing this
			// exact root prefix — forces the spur search to deviate here.
			for _, accPath := range accepted {
				if sameRoot(accPath.edges, rootEdges) && spurIndex < len(accPath.edges) {
					maskedEdges[accPath.edges[spurIndex]] = true
				}
			}
			// Remove the root path's interior nodes so the spur stays loopless: the
			// spur must not revisit any node the root already used. The spur node
			// itself stays usable (it is the deviation point).
			maskedNodes := make(map[domain.NodeID]bool)
			for _, rootEdge := range rootEdges {
				maskedNodes[edgeFrom(roadGraph, rootEdge)] = true
			}

			spurEdges, spurCost, ok := dijkstra(
				roadGraph, spurNode, dst,
				maskedWeight(roadGraph, weight, maskedEdges, maskedNodes),
			)
			if !ok {
				continue // no spur deviation here under the mask
			}

			// total = rootPath (verbatim) + spur. Recompute the root cost under the
			// base weight so the candidate cost is exact (the spur search only costed
			// the spur half).
			totalEdges := concatEdges(rootEdges, spurEdges)
			totalCost := pathCost(roadGraph, rootEdges, weight) + spurCost

			key := edgesKey(totalEdges)
			if seenCandidate[key] || isAccepted(accepted, totalEdges) {
				continue
			}
			seenCandidate[key] = true
			heap.Push(candidates, candidatePath{edges: totalEdges, cost: totalCost, seq: seq})
			seq++
		}

		if candidates.Len() == 0 {
			break // no more deviations exist: fewer than k paths in the graph
		}
		best := heap.Pop(candidates).(candidatePath)
		accepted = append(accepted, kPath{edges: best.edges, cost: best.cost})
	}

	return accepted
}

// maskedWeight wraps a base weightFunc so removed edges and the out-edges of
// removed nodes cost +Inf — the masking trick that lets Yen reuse the existing
// Dijkstra core without changing its signature. A masked edge can never lie on a
// finite-cost shortest path, so Dijkstra skips it naturally (its relaxation never
// improves a distance through a +Inf edge). The base weight is returned unchanged
// for every non-masked edge, so the search is otherwise identical to the
// unmasked one. maskedNodes masks an edge whose From-node is removed (the root
// path's interior nodes Yen excludes to keep the spur loopless).
func maskedWeight(
	roadGraph graph.Graph,
	base weightFunc,
	maskedEdges map[domain.EdgeID]bool,
	maskedNodes map[domain.NodeID]bool,
) weightFunc {
	return func(edge graph.Edge) float64 {
		if maskedEdges[edge.ID] || maskedNodes[edge.From] {
			return math.Inf(1)
		}
		return base(edge)
	}
}

// edgeFrom returns the From-node of an edge id (the node a path leaves when it
// traverses the edge). A missing edge is defensively reported as -1, an invalid
// node id no real edge ever carries.
func edgeFrom(roadGraph graph.Graph, edgeID domain.EdgeID) domain.NodeID {
	edge, ok := roadGraph.Edge(edgeID)
	if !ok {
		return -1
	}
	return edge.From
}

// sameRoot reports whether path begins with exactly the edges in root (root is a
// prefix of path). Yen masks an accepted path's spur-index edge only when that
// path shares the current root prefix, so the new spur is forced to deviate.
func sameRoot(path, root []domain.EdgeID) bool {
	if len(path) < len(root) {
		return false
	}
	for index := range root {
		if path[index] != root[index] {
			return false
		}
	}
	return true
}

// concatEdges returns a fresh slice root++spur. A fresh allocation is required —
// root aliases the previous accepted path's backing array, so appending in place
// would corrupt it.
func concatEdges(root, spur []domain.EdgeID) []domain.EdgeID {
	out := make([]domain.EdgeID, 0, len(root)+len(spur))
	out = append(out, root...)
	out = append(out, spur...)
	return out
}

// pathCost sums the base weight over a slice of edges. Used to cost the root half
// of a Yen candidate (the spur search costs only the spur half).
func pathCost(roadGraph graph.Graph, edges []domain.EdgeID, weight weightFunc) float64 {
	total := 0.0
	for _, edgeID := range edges {
		edge, ok := roadGraph.Edge(edgeID)
		if !ok {
			continue
		}
		total += weight(edge)
	}
	return total
}

// isAccepted reports whether edges equals one of the already-accepted paths — a
// guard so a candidate identical to an accepted path is never re-accepted.
func isAccepted(accepted []kPath, edges []domain.EdgeID) bool {
	for _, accPath := range accepted {
		if edgesEqual(accPath.edges, edges) {
			return true
		}
	}
	return false
}

// edgesEqual reports element-wise edge-id equality of two paths.
func edgesEqual(left, right []domain.EdgeID) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

// edgesKey renders a path's edge ids as a stable string key for the candidate
// dedupe set, so the same spur deviation discovered from two different spur nodes
// is enqueued only once.
func edgesKey(edges []domain.EdgeID) string {
	if len(edges) == 0 {
		return ""
	}
	buf := make([]byte, 0, len(edges)*4)
	for _, edgeID := range edges {
		value := int64(edgeID)
		buf = append(buf, byte(value), byte(value>>8), byte(value>>16), byte(value>>24))
	}
	return string(buf)
}

// candidatePath is one entry in Yen's candidate set B: a full src→dst deviation
// path with its total cost and an insertion sequence number that breaks cost ties
// deterministically (so the K-set is reproducible run to run).
type candidatePath struct {
	edges []domain.EdgeID
	cost  float64
	seq   int
}

// candidateHeap is a min-heap of candidatePaths ordered by cost, then by
// insertion sequence on ties — a total, deterministic order so Yen pops the same
// next-best path on every run regardless of enqueue interleaving.
type candidateHeap []candidatePath

func (h candidateHeap) Len() int { return len(h) }
func (h candidateHeap) Less(index, innerIndex int) bool {
	if h[index].cost != h[innerIndex].cost {
		return h[index].cost < h[innerIndex].cost
	}
	return h[index].seq < h[innerIndex].seq
}
func (h candidateHeap) Swap(index, innerIndex int) {
	h[index], h[innerIndex] = h[innerIndex], h[index]
}
func (h *candidateHeap) Push(value any) { *h = append(*h, value.(candidatePath)) }
func (h *candidateHeap) Pop() any {
	old := *h
	count := len(old)
	item := old[count-1]
	*h = old[:count-1]
	return item
}
