package cost

import (
	"math"

	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/graph"
)

// Default BPR parameters. These are the conventional Bureau of Public Roads
// coefficients (project-spec.md §1): alpha scales the congestion penalty and
// beta sets how sharply cost blows up past capacity.
const (
	defaultAlpha = 0.15
	defaultBeta  = 4

	// defaultCapacityScale leaves the per-edge capacity untouched. It is the
	// frontend's single global capacity knob (project-spec.md §R3).
	defaultCapacityScale = 1.0
)

// BPR is the Bureau of Public Roads volume-delay cost function, the reference
// CostFunction implementation. It maps an edge and its load to a traversal time
// in seconds via
//
//	t(v) = t_free * (1 + alpha*(v/c)^beta)
//
// where v is the load in vehicles/hour, c is the edge's effective capacity, and
// t_free is the edge's free-flow time. The conventional coefficients are
// alpha=0.15 and beta=4.
//
// CapacityScale is a single global multiplier applied to every edge's
// CapacityVPH (project-spec.md §R3): the effective capacity is
// CapacityVPH * CapacityScale. It is the frontend's one tunable capacity knob;
// a value below 1.0 shrinks effective capacity and so lifts the cost curve at a
// fixed load.
//
// Construct a BPR with NewBPR or DefaultBPR. The zero value is NOT usable: a
// zero CapacityScale would zero out every edge's capacity and collapse to the
// free-flow fallback, which is why NewBPR rejects a non-positive scale. Use
// DefaultBPR for the conventional coefficients.
type BPR struct {
	Alpha         float64
	Beta          float64
	CapacityScale float64
}

// NewBPR returns a BPR with the given coefficients and global capacity scale.
// Callers wanting the conventional alpha=0.15, beta=4, capacityScale=1.0
// should use DefaultBPR instead.
//
// capacityScale must be > 0. A non-positive scale is a construction-time
// misconfiguration that would silently switch off congestion modeling for every
// edge (it collapses to the free-flow fallback), so NewBPR panics rather than
// returning a cost function that quietly does nothing.
func NewBPR(alpha, beta, capacityScale float64) BPR {
	if capacityScale <= 0 {
		panic("cost: NewBPR requires capacityScale > 0")
	}
	return BPR{Alpha: alpha, Beta: beta, CapacityScale: capacityScale}
}

// DefaultBPR returns a BPR with the conventional coefficients (alpha=0.15,
// beta=4) and an unscaled capacity (capacityScale=1.0).
func DefaultBPR() BPR {
	return BPR{Alpha: defaultAlpha, Beta: defaultBeta, CapacityScale: defaultCapacityScale}
}

// Cost returns the BPR traversal time in seconds for the given edge under load
// loadVPH. Per the vph unit contract (docs/contracts.md §3, project-spec.md
// §R3), loadVPH and edge.CapacityVPH must both be in vehicles/hour.
//
// If the effective capacity is non-positive (a zero/negative CapacityVPH, or a
// non-positive CapacityScale), Cost falls back to the free-flow time to avoid a
// divide-by-zero. A negative loadVPH is out of contract; it is floored to zero
// so the result never drops below the free-flow time (a negative edge cost would
// break the router's non-negativity assumption).
func (bpr BPR) Cost(edge graph.Edge, loadVPH float64) float64 {
	effectiveCapacity := edge.CapacityVPH * bpr.CapacityScale
	if effectiveCapacity <= 0 {
		return edge.FreeFlowS
	}
	if loadVPH < 0 {
		loadVPH = 0
	}
	ratio := loadVPH / effectiveCapacity
	return edge.FreeFlowS * (1 + bpr.Alpha*powBeta(ratio, bpr.Beta))
}

// MarginalCost returns the marginal traversal time the future system-optimal
// router routes on (project-spec.md §R5), stated exactly as
//
//	t_free * (1 + alpha*(beta+1)*(v/c)^beta)
//
// This is t(v) + v*t'(v): the cost a marginal vehicle imposes on every vehicle
// already on the edge, not just on itself. It is deliberately NOT part of the
// CostFunction port — only the BPR concrete type exposes it. MarginalCost is
// always >= Cost for loadVPH > 0 and equals it at loadVPH = 0.
//
// As with Cost, loadVPH and edge.CapacityVPH are both vehicles/hour, a
// non-positive effective capacity falls back to the free-flow time, and a
// negative loadVPH is floored to zero.
func (bpr BPR) MarginalCost(edge graph.Edge, loadVPH float64) float64 {
	effectiveCapacity := edge.CapacityVPH * bpr.CapacityScale
	if effectiveCapacity <= 0 {
		return edge.FreeFlowS
	}
	if loadVPH < 0 {
		loadVPH = 0
	}
	ratio := loadVPH / effectiveCapacity
	return edge.FreeFlowS * (1 + bpr.Alpha*(bpr.Beta+1)*powBeta(ratio, bpr.Beta))
}

// powBeta raises a non-negative base to the BPR exponent. Beta is conventionally
// the integer 4, so for non-negative integer exponents it uses exponentiation by
// squaring — a handful of multiplications — instead of the general, log/exp-based
// math.Pow, which is the dominant cost of Cost on the router's hot path. It falls
// back to math.Pow for fractional exponents. The base (a load/capacity ratio) is
// always non-negative here, so there is no negative-base branch to consider.
func powBeta(base, beta float64) float64 {
	exponent := int(beta)
	if float64(exponent) != beta || exponent < 0 {
		return math.Pow(base, beta)
	}
	result := 1.0
	for exponent > 0 {
		if exponent&1 == 1 {
			result *= base
		}
		base *= base
		exponent >>= 1
	}
	return result
}

// Compile-time assertion: BPR satisfies the CostFunction port.
var _ CostFunction = BPR{}
