package routing

import "github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/domain"

// dijkstraScratchBuffer holds the two O(NodeCount) per-Dijkstra working slices —
// the settled-cost dist and the predecessor prevEdge — so an iterative router can
// reuse them across the thousands of shortest-path calls one Assign makes, rather
// than allocating both afresh on every call (dijkstra.go's original
// allocate-per-call note). It owns no graph state and is purely a reusable
// arena; correctness does not depend on its previous contents because
// dijkstraScratch fully re-initializes both slices (dist→+Inf, prevEdge→-1) at
// the top of every call.
//
// Concurrency: a buffer is single-goroutine state and is NOT safe for concurrent
// use. An Assign that fans requests across N goroutines gives each its OWN buffer
// (see newDijkstraScratch); the immutable graph stays shared, only this mutable
// scratch is per-worker. The single-call Route path passes no buffer at all
// (dijkstra → dijkstraScratch with scratch == nil) and is unchanged.
type dijkstraScratchBuffer struct {
	dist     []float64
	prevEdge []domain.EdgeID
}

// newDijkstraScratch returns a scratch buffer sized for a graph of nodeCount
// nodes. A negative nodeCount is treated as zero. Callers that fan an Assign
// across goroutines allocate one buffer PER goroutine (never one shared), since
// a buffer is not safe for concurrent use.
func newDijkstraScratch(nodeCount int) *dijkstraScratchBuffer {
	if nodeCount < 0 {
		nodeCount = 0
	}
	return &dijkstraScratchBuffer{
		dist:     make([]float64, nodeCount),
		prevEdge: make([]domain.EdgeID, nodeCount),
	}
}

// reset returns the buffer's dist/prevEdge slices sized to exactly count
// elements, growing (reallocating) only if the buffer was built smaller than the
// graph now needs. It does NOT clear the contents — dijkstraScratch overwrites
// every element it reads (dist→+Inf, prevEdge→-1) before use, so a clear here
// would be redundant work. The returned slices alias the buffer's storage and
// stay valid until the next reset.
func (buffer *dijkstraScratchBuffer) reset(count int) (dist []float64, prevEdge []domain.EdgeID) {
	if count > len(buffer.dist) {
		buffer.dist = make([]float64, count)
		buffer.prevEdge = make([]domain.EdgeID, count)
	}
	return buffer.dist[:count], buffer.prevEdge[:count]
}
