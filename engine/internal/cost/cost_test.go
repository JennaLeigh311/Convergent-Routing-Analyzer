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
		load float64
		want float64
	}{
		{"empty road = free flow", 0, 10},
		{"at capacity adds 15%", 1000, 11.5},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := f.Cost(e, tt.load); math.Abs(got-tt.want) > 1e-9 {
				t.Errorf("Cost(load=%v) = %v, want %v", tt.load, got, tt.want)
			}
		})
	}
}
