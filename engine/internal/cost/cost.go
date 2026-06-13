package cost

import "github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/graph"

// CostFunction maps an edge and its current load to a traversal cost in seconds.
// loadVPH is the edge's flow in vehicles/hour; both load and the edge's
// CapacityVPH must share that unit (see docs/contracts.md §3).
//
// The reference implementation is BPR:
//
//	t(v) = e.FreeFlowS * (1 + alpha*(v/c)^beta)   conventionally alpha=0.15, beta=4
//
// The system-optimal router instead routes on the marginal cost
// t(v) + v*t'(v); implementations that support that expose it separately.
type CostFunction interface {
	Cost(edge graph.Edge, loadVPH float64) float64
}
