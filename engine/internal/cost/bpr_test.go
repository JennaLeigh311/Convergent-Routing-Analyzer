package cost_test

import (
	"math"
	"testing"

	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/cost"
	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/graph"
)

const tolerance = 1e-9

// Compile-time assertion: the production BPR satisfies the CostFunction port.
var _ cost.CostFunction = cost.BPR{}

// DefaultBPR must carry the conventional coefficients (alpha=0.15, beta=4) and
// an unscaled capacity.
func TestDefaultBPRCoefficients(test1 *testing.T) {
	bpr := cost.DefaultBPR()
	if bpr.Alpha != 0.15 || bpr.Beta != 4 || bpr.CapacityScale != 1.0 {
		test1.Errorf("DefaultBPR() = %+v, want alpha=0.15 beta=4 capacityScale=1.0", bpr)
	}
}

func TestBPRCostClosedForm(test1 *testing.T) {
	bpr := cost.DefaultBPR()
	edge := graph.Edge{FreeFlowS: 10, CapacityVPH: 1000}

	tests := []struct {
		name    string
		edge    graph.Edge
		loadVPH float64
		want    float64
	}{
		// v=0 returns exactly the free-flow time.
		{"empty road = free flow", edge, 0, 10},
		// v=c adds exactly alpha: 10*(1 + 0.15) = 11.5.
		{"at capacity adds alpha", edge, 1000, 11.5},
		// Super-linear blow-up at v=2c: 10*(1 + 0.15*2^4) = 34.
		{"over capacity grows fast", edge, 2000, 34},
		// Non-positive capacity falls back to free flow (guards divide-by-zero).
		{"zero capacity = free flow", graph.Edge{FreeFlowS: 10, CapacityVPH: 0}, 500, 10},
		{"negative capacity = free flow", graph.Edge{FreeFlowS: 10, CapacityVPH: -1}, 500, 10},
		// Out-of-contract negative load floors to zero, i.e. free flow.
		{"negative load = free flow", edge, -500, 10},
	}
	for _, testCase := range tests {
		test1.Run(testCase.name, func(test2 *testing.T) {
			if got := bpr.Cost(testCase.edge, testCase.loadVPH); math.Abs(got-testCase.want) > tolerance {
				test2.Errorf("Cost(loadVPH=%v) = %v, want %v", testCase.loadVPH, got, testCase.want)
			}
		})
	}
}

// Cost must be monotonically non-decreasing as load rises.
func TestBPRCostMonotonic(test1 *testing.T) {
	bpr := cost.DefaultBPR()
	edge := graph.Edge{FreeFlowS: 10, CapacityVPH: 1000}

	previous := math.Inf(-1)
	for loadVPH := 0.0; loadVPH <= 3000; loadVPH += 50 {
		current := bpr.Cost(edge, loadVPH)
		if current < previous-tolerance {
			test1.Fatalf("Cost not monotonic: Cost(%v)=%v < Cost(prev)=%v", loadVPH, current, previous)
		}
		previous = current
	}
}

// MarginalCost must match its exact closed form (project-spec.md §R5).
func TestBPRMarginalCostClosedForm(test1 *testing.T) {
	bpr := cost.DefaultBPR()
	edge := graph.Edge{FreeFlowS: 10, CapacityVPH: 1000}

	for _, loadVPH := range []float64{0, 250, 500, 1000, 1500, 2000} {
		ratio := loadVPH / edge.CapacityVPH
		want := edge.FreeFlowS * (1 + bpr.Alpha*(bpr.Beta+1)*math.Pow(ratio, bpr.Beta))
		if got := bpr.MarginalCost(edge, loadVPH); math.Abs(got-want) > tolerance {
			test1.Errorf("MarginalCost(loadVPH=%v) = %v, want %v", loadVPH, got, want)
		}
	}
}

// TestBPRMarginalCostHardcoded anchors the marginal-cost magnitude against
// externally computed constants. The closed-form test above mirrors the
// production expression, so it would re-derive the same number if the formula
// were wrong; these hardcoded values pin the (beta+1) coefficient independently.
func TestBPRMarginalCostHardcoded(test1 *testing.T) {
	bpr := cost.DefaultBPR()
	edge := graph.Edge{FreeFlowS: 10, CapacityVPH: 1000}

	tests := []struct {
		name    string
		loadVPH float64
		want    float64
	}{
		// v=0: marginal equals free flow.
		{"empty road", 0, 10},
		// v=c: 10*(1 + 0.15*5*1^4) = 10*1.75 = 17.5.
		{"at capacity", 1000, 17.5},
		// v=2c: 10*(1 + 0.15*5*2^4) = 10*(1 + 12) = 130.
		{"twice capacity", 2000, 130},
	}
	for _, testCase := range tests {
		test1.Run(testCase.name, func(test2 *testing.T) {
			if got := bpr.MarginalCost(edge, testCase.loadVPH); math.Abs(got-testCase.want) > tolerance {
				test2.Errorf("MarginalCost(loadVPH=%v) = %v, want %v", testCase.loadVPH, got, testCase.want)
			}
		})
	}
}

// Out-of-contract negative load floors to zero for MarginalCost too.
func TestBPRMarginalCostNegativeLoad(test1 *testing.T) {
	bpr := cost.DefaultBPR()
	edge := graph.Edge{FreeFlowS: 10, CapacityVPH: 1000}
	if got := bpr.MarginalCost(edge, -500); math.Abs(got-edge.FreeFlowS) > tolerance {
		test1.Errorf("MarginalCost(negative load) = %v, want %v", got, edge.FreeFlowS)
	}
}

// NewBPR must reject a non-positive capacity scale rather than return a cost
// function that silently collapses every edge to free flow.
func TestNewBPRRejectsNonPositiveScale(test1 *testing.T) {
	for _, scale := range []float64{0, -1} {
		func() {
			defer func() {
				if recover() == nil {
					test1.Errorf("NewBPR(_, _, %v) did not panic", scale)
				}
			}()
			cost.NewBPR(0.15, 4, scale)
		}()
	}
}

// Non-positive effective capacity makes MarginalCost fall back to free flow.
func TestBPRMarginalCostZeroCapacity(test1 *testing.T) {
	bpr := cost.DefaultBPR()
	edge := graph.Edge{FreeFlowS: 10, CapacityVPH: 0}
	if got := bpr.MarginalCost(edge, 500); math.Abs(got-edge.FreeFlowS) > tolerance {
		test1.Errorf("MarginalCost(zero capacity) = %v, want %v", got, edge.FreeFlowS)
	}
}

// MarginalCost equals Cost at v=0 and exceeds it for all v>0.
func TestBPRMarginalCostExceedsAverage(test1 *testing.T) {
	bpr := cost.DefaultBPR()
	edge := graph.Edge{FreeFlowS: 10, CapacityVPH: 1000}

	if average, marginal := bpr.Cost(edge, 0), bpr.MarginalCost(edge, 0); math.Abs(marginal-average) > tolerance {
		test1.Errorf("at v=0: MarginalCost=%v, Cost=%v, want equal", marginal, average)
	}
	// The invariant marginal >= average holds for all v>0. Where the
	// congestion term is large enough to clear float tolerance, marginal is
	// strictly greater (it carries the extra alpha*beta*(v/c)^beta term).
	for _, loadVPH := range []float64{1, 250, 500, 1000, 1500, 2000} {
		average := bpr.Cost(edge, loadVPH)
		marginal := bpr.MarginalCost(edge, loadVPH)
		if marginal < average-tolerance {
			test1.Errorf("at v=%v: MarginalCost=%v < Cost=%v", loadVPH, marginal, average)
		}
		if loadVPH >= 250 && marginal <= average {
			test1.Errorf("at v=%v: MarginalCost=%v not strictly > Cost=%v", loadVPH, marginal, average)
		}
	}
}

// A capacity_scale below 1.0 shrinks effective capacity and so lifts the cost
// curve at a fixed load. Verify the effective-capacity arithmetic explicitly.
func TestBPRCapacityScale(test1 *testing.T) {
	edge := graph.Edge{FreeFlowS: 10, CapacityVPH: 1000}
	const loadVPH = 800

	unscaled := cost.NewBPR(0.15, 4, 1.0)
	halved := cost.NewBPR(0.15, 4, 0.5)

	// Effective capacity is CapacityVPH * CapacityScale: 1000 vs 500.
	wantUnscaled := edge.FreeFlowS * (1 + 0.15*math.Pow(loadVPH/(1000*1.0), 4))
	wantHalved := edge.FreeFlowS * (1 + 0.15*math.Pow(loadVPH/(1000*0.5), 4))

	if got := unscaled.Cost(edge, loadVPH); math.Abs(got-wantUnscaled) > tolerance {
		test1.Errorf("unscaled Cost = %v, want %v", got, wantUnscaled)
	}
	if got := halved.Cost(edge, loadVPH); math.Abs(got-wantHalved) > tolerance {
		test1.Errorf("halved Cost = %v, want %v", got, wantHalved)
	}
	if halved.Cost(edge, loadVPH) <= unscaled.Cost(edge, loadVPH) {
		test1.Errorf("capacity_scale<1 should raise cost: halved=%v not > unscaled=%v",
			halved.Cost(edge, loadVPH), unscaled.Cost(edge, loadVPH))
	}
}
