package benchmark

import "math"

// PriceOfAnarchy returns the realized-time Price of Anarchy: the ratio of the
// SELFISH baseline's realized total network time to a REFERENCE assignment's, both
// measured on the SAME OD set —
//
//	PoA = selfishTotal / referenceTotal
//
// It is the headline number of the whole project (project-spec.md §5): how much
// worse the network performs when every driver routes selfishly (the naive,
// free-flow baseline) than under a coordinated optimum (the systemoptimal
// reference). PoA ≥ 1 whenever the reference is a true optimum; PoA > 1 quantifies
// the gap the coordinated routing closes. A PoA of 2.0 means selfish routing
// realizes twice the total travel time the optimum does.
//
// This function is deliberately ROUTER-AGNOSTIC: it takes the two precomputed
// totals (each from routing.TotalNetworkTime on that assignment's FinalFlows), not
// routers or AssignResults, so the evaluator never hardcodes which strategy is the
// baseline or the reference. The caller decides — conventionally selfishTotal is
// naive and referenceTotal is systemoptimal — and the metric just divides. Keeping
// router construction out of the metric is what lets the same function serve the
// Phase-4 CLI table and the Phase-5 live API without either re-deriving the ratio.
//
// Degenerate inputs are defined rather than allowed to produce a NaN/Inf that would
// poison a results table: a non-positive referenceTotal (an empty batch, or a
// network where no flow incurs any cost) yields PoA = 1 — with no realized time to
// improve on, selfish and optimal are trivially equal — and so does a non-positive
// selfishTotal. The result is always finite and ≥ 0.
func PriceOfAnarchy(selfishTotal, referenceTotal float64) float64 {
	if referenceTotal <= 0 || selfishTotal <= 0 {
		return 1
	}
	poa := selfishTotal / referenceTotal
	if math.IsNaN(poa) || math.IsInf(poa, 0) {
		return 1
	}
	return poa
}
