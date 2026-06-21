package benchmark

import (
	"math"
	"sort"

	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/cost"
	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/domain"
	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/graph"
	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/routing"
)

// RealizedTimes returns the realized travel time, in seconds, for every route in
// result.Routes, in input order. The realized time of a route is the sum over its
// edges of BPR.Cost(edge, FinalFlows[edge]) — the cost function applied to the
// ACTUAL realized per-edge load the assignment produced (project-spec.md §5).
//
// This is deliberately distinct from Route.CostS: CostS is the weight the router
// optimized against (free-flow for naive, marginal cost for systemoptimal, …),
// whereas the realized time is the ground truth a driver on that path actually
// experiences once every other request's flow is on the network. Comparing
// strategies on CostS would be comparing apples to oranges; the realized time is
// the single basis they are all measured on, exactly as routing.TotalNetworkTime
// is the realized TOTAL the SO ≤ UE invariant is proven on.
//
// An unknown edge id (one not present in the graph) contributes 0 and is skipped,
// matching routing.TotalNetworkTime's "missing/out-of-range edge contributes
// nothing" rule — a route is never rejected for referencing an edge the graph does
// not know, it just gains no time from it. A zero-edge route (origin == destination)
// realizes 0 s. FinalFlows is read by edge id; an id past the FinalFlows slice
// reads as zero load (loadAt-style guard), so a short or nil flow vector never
// panics, it just yields free-flow costs.
//
// The returned slice is freshly allocated and caller-owned; it does not alias
// result.Routes or result.FinalFlows.
func RealizedTimes(g graph.Graph, bpr cost.BPR, result routing.AssignResult) []float64 {
	times := make([]float64, len(result.Routes))
	for i, route := range result.Routes {
		times[i] = routeRealizedTime(g, bpr, route, result.FinalFlows)
	}
	return times
}

// routeRealizedTime sums BPR.Cost over a single route's edges at the realized edge
// loads. It is the per-request kernel RealizedTimes maps over the batch, factored
// out so the "unknown edge contributes nothing" and "load read by edge id, out of
// range ⇒ 0" guards live in exactly one place and stay consistent with
// routing.TotalNetworkTime's edge-sum semantics.
func routeRealizedTime(g graph.Graph, bpr cost.BPR, route routing.Route, flows []float64) float64 {
	var total float64
	for _, edgeID := range route.Edges {
		edge, ok := g.Edge(edgeID)
		if !ok {
			continue // unknown edge: contribute nothing, never panic (matches TotalNetworkTime)
		}
		total += bpr.Cost(edge, loadAt(flows, edgeID))
	}
	return total
}

// loadAt reads the realized flow on an edge by id, returning 0 for a negative id
// or an id past the end of the (possibly short or nil) flow vector. It mirrors the
// unexported routing.loadAt guard so the benchmark reads flows with the same
// out-of-range tolerance the routers and TotalNetworkTime use, rather than indexing
// raw and risking a panic on a degenerate flow vector.
func loadAt(flows []float64, edgeID domain.EdgeID) float64 {
	if edgeID < 0 || int(edgeID) >= len(flows) {
		return 0
	}
	return flows[edgeID]
}

// mean returns the arithmetic mean of values, or 0 for an empty slice. The empty
// case is defined (not NaN) so an empty batch — zero requests — yields a
// well-defined, finite Result (the §R5 "all metrics finite on the empty batch"
// criterion) rather than a 0/0 that would poison every downstream comparison.
func mean(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	var sum float64
	for _, v := range values {
		sum += v
	}
	return sum / float64(len(values))
}

// percentileNearestRank returns the p-th percentile (p in [0,1]) of values by the
// NEAREST-RANK method on a SORTED COPY of the input. It never mutates the caller's
// slice — the copy is the determinism guarantee: a metric that sorted the caller's
// own RealizedTimes slice in place would reorder data the caller still holds and
// silently corrupt a re-run. An empty slice yields 0 (the empty-batch contract).
//
// Nearest-rank (rather than linear interpolation) is chosen for determinism and
// reproducibility: the result is always an actual observed value, so it is exactly
// reproducible across runs and platforms with no interpolation rounding to drift.
// The rank is ceil(p × n) clamped to [1, n], 1-based, so p95 of 20 values is the
// 19th smallest and p100 is the max.
func percentileNearestRank(values []float64, p float64) float64 {
	n := len(values)
	if n == 0 {
		return 0
	}
	sorted := make([]float64, n)
	copy(sorted, values)
	sort.Float64s(sorted)

	if p <= 0 {
		return sorted[0]
	}
	if p >= 1 {
		return sorted[n-1]
	}
	rank := int(math.Ceil(p * float64(n))) // 1-based nearest rank
	if rank < 1 {
		rank = 1
	}
	if rank > n {
		rank = n
	}
	return sorted[rank-1]
}

// edgeVCRatios returns the volume/capacity ratio v/c for every edge in the graph,
// in ascending edge-id order (deterministic), where v is the realized flow and c
// is the EFFECTIVE capacity (CapacityVPH × bpr.CapacityScale) — defined exactly as
// BPR.Cost defines it, so the utilization metric and the cost curve agree on what
// "capacity" means. An edge with non-positive effective capacity (the same case
// BPR.Cost falls back to free-flow on) is treated as v/c = 0: utilization is
// undefined when there is no capacity, and 0 keeps it out of the spread/Gini rather
// than injecting a +Inf that would make every aggregate non-finite.
//
// The slice is sized to g.EdgeCount() and indexed in sorted edge-id order, never
// from a map, so the utilization aggregates below are deterministic.
func edgeVCRatios(g graph.Graph, bpr cost.BPR, flows []float64) []float64 {
	count := g.EdgeCount()
	ratios := make([]float64, 0, count)
	for id := domain.EdgeID(0); int(id) < count; id++ {
		edge, ok := g.Edge(id)
		if !ok {
			ratios = append(ratios, 0)
			continue
		}
		effectiveCapacity := edge.CapacityVPH * bpr.CapacityScale
		if effectiveCapacity <= 0 {
			ratios = append(ratios, 0)
			continue
		}
		load := loadAt(flows, id)
		if load < 0 {
			load = 0
		}
		ratios = append(ratios, load/effectiveCapacity)
	}
	return ratios
}

// maxOf returns the maximum of values, or 0 for an empty slice. Used for MaxVC (the
// worst-loaded edge's v/c); the empty/zero-edge case is 0, not -Inf, so a network
// with no edges or no flow yields a finite MaxVC.
func maxOf(values []float64) float64 {
	m := 0.0
	for _, v := range values {
		if v > m {
			m = v
		}
	}
	return m
}

// gini returns the Gini coefficient of the (non-negative) values in [0,1]: 0 when
// every edge carries the same v/c (perfectly balanced utilization), approaching 1
// as load concentrates on a single edge (maximally imbalanced). It is the
// edge-utilization BALANCE metric — a single scalar summarizing how evenly an
// assignment spreads load across the network, which mean/max alone cannot capture
// (two networks can share a MaxVC yet spread the rest very differently).
//
// It is computed on a SORTED COPY (ascending) via the mean-absolute-difference
// form Gini = Σ_i Σ_j |x_i − x_j| / (2 n² x̄), evaluated in the O(n log n)
// sorted-prefix form Σ_i (2i − n + 1) x_i / (n² x̄) over the ascending sort — so the
// summation order is fixed and the result is deterministic. The code divides by the
// equivalent n·Σx (since n²·x̄ = n·Σx), which is what the implementation below computes. An empty slice, or one
// whose values sum to 0 (no flow anywhere ⇒ all v/c = 0 ⇒ perfectly "balanced"),
// yields 0. Negative inputs cannot occur here (v/c is floored at 0 above), so the
// classic non-negative Gini definition applies cleanly.
func gini(values []float64) float64 {
	n := len(values)
	if n == 0 {
		return 0
	}
	sorted := make([]float64, n)
	copy(sorted, values)
	sort.Float64s(sorted)

	var sum, weighted float64
	for i, v := range sorted {
		sum += v
		// (2i − n + 1) with i 0-based over the ascending sort is the standard
		// sorted-form Gini coefficient: the lowest value gets the most negative
		// weight, the highest the most positive.
		weighted += float64(2*i-n+1) * v
	}
	if sum == 0 {
		return 0 // no load anywhere ⇒ trivially balanced
	}
	return weighted / (float64(n) * sum)
}
