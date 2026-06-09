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

func (f fakeBPR) Cost(e graph.Edge, loadVPH float64) float64 {
	if e.CapacityVPH <= 0 {
		return e.FreeFlowS
	}
	return e.FreeFlowS * (1 + f.alpha*math.Pow(loadVPH/e.CapacityVPH, f.beta))
}

// Compile-time assertion: fakeBPR satisfies the CostFunction port.
var _ cost.CostFunction = fakeBPR{}

func TestFakeCostSatisfiesPort(t *testing.T) {
	f := fakeBPR{alpha: 0.15, beta: 4}
	e := graph.Edge{FreeFlowS: 10, CapacityVPH: 1000}

	tests := []struct {
		name string
		edge graph.Edge
		load float64
		want float64
	}{
		{"empty road = free flow", e, 0, 10},
		{"at capacity adds 15%", e, 1000, 11.5},
		// Super-linear blow-up past capacity: 10*(1 + 0.15*2^4) = 34.
		{"over capacity grows fast", e, 2000, 34},
		// Zero-capacity edge falls back to free-flow (guards divide-by-zero).
		{"zero capacity = free flow", graph.Edge{FreeFlowS: 10, CapacityVPH: 0}, 500, 10},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := f.Cost(tt.edge, tt.load); math.Abs(got-tt.want) > 1e-9 {
				t.Errorf("Cost(load=%v) = %v, want %v", tt.load, got, tt.want)
			}
		})
	}
}
