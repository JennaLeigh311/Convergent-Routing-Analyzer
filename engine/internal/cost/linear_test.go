package cost_test

import (
	"math"
	"testing"

	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/cost"
	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/graph"
)

// Compile-time assertion: the production Linear satisfies the CostFunction port.
var _ cost.CostFunction = cost.Linear{}

// DefaultLinear must carry the default slope and an unscaled capacity.
func TestDefaultLinearCoefficients(test1 *testing.T) {
	linear := cost.DefaultLinear()
	if linear.Slope != 0.15 || linear.CapacityScale != 1.0 {
		test1.Errorf("DefaultLinear() = %+v, want slope=0.15 capacityScale=1.0", linear)
	}
}

func TestLinearCostClosedForm(test1 *testing.T) {
	linear := cost.DefaultLinear()
	edge := graph.Edge{FreeFlowS: 10, CapacityVPH: 1000}

	tests := []struct {
		name    string
		edge    graph.Edge
		loadVPH float64
		want    float64
	}{
		// v=0 returns exactly the free-flow time.
		{"empty road = free flow", edge, 0, 10},
		// v=c adds exactly the slope: 10*(1 + 0.15) = 11.5.
		{"at capacity adds slope", edge, 1000, 11.5},
		// Linear in load: at v=2c the term is twice the v=c term.
		{"twice capacity is linear", edge, 2000, 13},
		// Non-positive capacity falls back to free flow.
		{"zero capacity = free flow", graph.Edge{FreeFlowS: 10, CapacityVPH: 0}, 500, 10},
		{"negative capacity = free flow", graph.Edge{FreeFlowS: 10, CapacityVPH: -1}, 500, 10},
		// Out-of-contract negative load floors to zero, i.e. free flow (and so
		// never returns the negative cost a raw linear term would).
		{"negative load = free flow", edge, -500, 10},
	}
	for _, testCase := range tests {
		test1.Run(testCase.name, func(test2 *testing.T) {
			if got := linear.Cost(testCase.edge, testCase.loadVPH); math.Abs(got-testCase.want) > 1e-9 {
				test2.Errorf("Cost(loadVPH=%v) = %v, want %v", testCase.loadVPH, got, testCase.want)
			}
		})
	}
}

// Cost must be monotonically non-decreasing as load rises.
func TestLinearCostMonotonic(test1 *testing.T) {
	linear := cost.DefaultLinear()
	edge := graph.Edge{FreeFlowS: 10, CapacityVPH: 1000}

	previous := math.Inf(-1)
	for loadVPH := 0.0; loadVPH <= 3000; loadVPH += 50 {
		current := linear.Cost(edge, loadVPH)
		if current < previous-1e-9 {
			test1.Fatalf("Cost not monotonic: Cost(%v)=%v < Cost(prev)=%v", loadVPH, current, previous)
		}
		previous = current
	}
}

// A capacity_scale below 1.0 shrinks effective capacity and lifts the curve.
func TestLinearCapacityScale(test1 *testing.T) {
	edge := graph.Edge{FreeFlowS: 10, CapacityVPH: 1000}
	const loadVPH = 800

	unscaled := cost.NewLinear(0.15, 1.0)
	halved := cost.NewLinear(0.15, 0.5)

	wantHalved := edge.FreeFlowS * (1 + 0.15*(loadVPH/(1000*0.5)))
	if got := halved.Cost(edge, loadVPH); math.Abs(got-wantHalved) > 1e-9 {
		test1.Errorf("halved Cost = %v, want %v", got, wantHalved)
	}
	if halved.Cost(edge, loadVPH) <= unscaled.Cost(edge, loadVPH) {
		test1.Errorf("capacity_scale<1 should raise cost: halved=%v not > unscaled=%v",
			halved.Cost(edge, loadVPH), unscaled.Cost(edge, loadVPH))
	}
}

// NewLinear must reject a non-positive capacity scale, mirroring NewBPR.
func TestNewLinearRejectsNonPositiveScale(test1 *testing.T) {
	for _, scale := range []float64{0, -1} {
		func() {
			defer func() {
				if recover() == nil {
					test1.Errorf("NewLinear(_, %v) did not panic", scale)
				}
			}()
			cost.NewLinear(0.15, scale)
		}()
	}
}
