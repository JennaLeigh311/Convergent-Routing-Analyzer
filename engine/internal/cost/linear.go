package cost

import "github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/graph"

// defaultLinearSlope is the conventional congestion slope for Linear. At a
// load equal to effective capacity (v=c) it adds the same proportional penalty
// the conventional BPR does at capacity (alpha=0.15), so the two functions line
// up at v=c and diverge only in curvature.
const defaultLinearSlope = 0.15

// Linear is a linear-in-(v/c) volume-delay cost function, the simpler
// alternative to BPR. It maps an edge and its load to a traversal time in
// seconds via
//
//	t(v) = t_free * (1 + slope*(v/c))
//
// where v is the load in vehicles/hour, c is the edge's effective capacity, and
// t_free is the edge's free-flow time. Unlike BPR (which rises as (v/c)^beta),
// the congestion term here grows only linearly in the load, so Linear neither
// blows up past capacity nor matches the empirical sharpness of real congestion
// — it is offered as a cheap, well-behaved baseline against which BPR's
// super-linear behaviour can be compared.
//
// CapacityScale is the same single global capacity multiplier BPR uses
// (project-spec.md §R3): the effective capacity is CapacityVPH * CapacityScale.
//
// Construct a Linear with NewLinear or DefaultLinear. As with BPR, the zero
// value is NOT usable because a zero CapacityScale collapses every edge to the
// free-flow fallback.
type Linear struct {
	Slope         float64
	CapacityScale float64
}

// NewLinear returns a Linear with the given congestion slope and global
// capacity scale. Callers wanting the default slope and an unscaled capacity
// should use DefaultLinear instead.
func NewLinear(slope, capacityScale float64) Linear {
	return Linear{Slope: slope, CapacityScale: capacityScale}
}

// DefaultLinear returns a Linear with the default slope and an unscaled
// capacity (capacityScale=1.0).
func DefaultLinear() Linear {
	return Linear{Slope: defaultLinearSlope, CapacityScale: defaultCapacityScale}
}

// Cost returns the linear traversal time in seconds for the given edge under
// load loadVPH. Per the vph unit contract (docs/contracts.md §3, project-spec.md
// §R3), loadVPH and edge.CapacityVPH must both be in vehicles/hour.
//
// If the effective capacity is non-positive (a zero/negative CapacityVPH, or a
// non-positive CapacityScale), Cost falls back to the free-flow time to avoid a
// divide-by-zero.
func (linear Linear) Cost(edge graph.Edge, loadVPH float64) float64 {
	effectiveCapacity := edge.CapacityVPH * linear.CapacityScale
	if effectiveCapacity <= 0 {
		return edge.FreeFlowS
	}
	ratio := loadVPH / effectiveCapacity
	return edge.FreeFlowS * (1 + linear.Slope*ratio)
}

// Compile-time assertion: Linear satisfies the CostFunction port.
var _ CostFunction = Linear{}
