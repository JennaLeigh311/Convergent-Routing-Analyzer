package cost_test

import (
	"math"
	"testing"

	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/cost"
	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/graph"
)

// fakeBPR is a minimal CostFunction implementing the BPR formula, used to prove
// the port is satisfiable. The production implementation lands in a later phase.
type fakeBPR struct {
	alpha, beta float64
}

func (bpr fakeBPR) Cost(edge graph.Edge, loadVPH float64) float64 {
	if edge.CapacityVPH <= 0 {
		return edge.FreeFlowS
	}
	return edge.FreeFlowS * (1 + bpr.alpha*math.Pow(loadVPH/edge.CapacityVPH, bpr.beta))
}

// Compile-time assertion: fakeBPR satisfies the CostFunction port.
var _ cost.CostFunction = fakeBPR{}

func TestFakeCostSatisfiesPort(test1 *testing.T) {
	bpr := fakeBPR{alpha: 0.15, beta: 4}
	edge := graph.Edge{FreeFlowS: 10, CapacityVPH: 1000}

	tests := []struct {
		name string
		edge graph.Edge
		load float64
		want float64
	}{
		{"empty road = free flow", edge, 0, 10},
		{"at capacity adds 15%", edge, 1000, 11.5},
		// Super-linear blow-up past capacity: 10*(1 + 0.15*2^4) = 34.
		{"over capacity grows fast", edge, 2000, 34},
		// Zero-capacity edge falls back to free-flow (guards divide-by-zero).
		{"zero capacity = free flow", graph.Edge{FreeFlowS: 10, CapacityVPH: 0}, 500, 10},
	}
	for _, testCase := range tests {
		test1.Run(testCase.name, func(test2 *testing.T) {
			if got := bpr.Cost(testCase.edge, testCase.load); math.Abs(got-testCase.want) > 1e-9 {
				test2.Errorf("Cost(load=%v) = %v, want %v", testCase.load, got, testCase.want)
			}
		})
	}
}
