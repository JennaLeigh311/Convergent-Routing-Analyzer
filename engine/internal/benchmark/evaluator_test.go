package benchmark_test

import (
	"math"
	"testing"

	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/benchmark"
	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/cost"
	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/domain"
	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/graph"
	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/routing"
)

const eps = 1e-9

// handToyGraph builds the two-edge graph the hand-computed metric tests reason
// about: edge 0 (FreeFlow 10 s, cap 100 vph) and edge 1 (FreeFlow 20 s, cap 200
// vph), the same shape routing.TestTotalNetworkTimeHandComputed uses so the
// realized totals here line up with a pinned reference. With DefaultBPR (alpha
// 0.15, beta 4) and loads equal to capacity (v/c = 1 on each edge):
//
//	BPR.Cost(edge0) = 10 * (1 + 0.15*1^4) = 11.5 s
//	BPR.Cost(edge1) = 20 * (1 + 0.15*1^4) = 23.0 s
//
// so every realized number below is computable by hand without powBeta.
func handToyGraph(t *testing.T) *graph.AdjacencyGraph {
	t.Helper()
	nodes := []graph.Node{
		{ID: 0, Pos: domain.LatLon{Lat: 0, Lon: 0}},
		{ID: 1, Pos: domain.LatLon{Lat: 0, Lon: 1}},
		{ID: 2, Pos: domain.LatLon{Lat: 0, Lon: 2}},
	}
	edges := []graph.Edge{
		{ID: 0, Segment: "e0", From: 0, To: 1, FreeFlowS: 10, CapacityVPH: 100},
		{ID: 1, Segment: "e1", From: 1, To: 2, FreeFlowS: 20, CapacityVPH: 200},
	}
	g, err := graph.New(nodes, edges)
	if err != nil {
		t.Fatalf("graph.New() error = %v", err)
	}
	return g
}

// TestRealizedTimesHandComputed pins the per-request realized travel time against
// hand math. Three routes over the hand-toy graph at v/c = 1 on each edge:
//
//	r0 = [edge0]        ⇒ 11.5
//	r1 = [edge0, edge1] ⇒ 11.5 + 23.0 = 34.5
//	r2 = [edge1]        ⇒ 23.0
//
// asserts the realized time is BPR.Cost at the realized loads (NOT Route.CostS —
// the routes carry a deliberately WRONG CostS of 999 to prove the evaluator reads
// the flows, not the router's optimized cost).
func TestRealizedTimesHandComputed(t *testing.T) {
	g := handToyGraph(t)
	result := routing.AssignResult{
		Routes: []routing.Route{
			{RequestID: "r0", Edges: []domain.EdgeID{0}, CostS: 999},
			{RequestID: "r1", Edges: []domain.EdgeID{0, 1}, CostS: 999},
			{RequestID: "r2", Edges: []domain.EdgeID{1}, CostS: 999},
		},
		FinalFlows: []float64{100, 200}, // v/c = 1 on each edge
	}

	got := benchmark.RealizedTimes(g, cost.DefaultBPR(), result)
	want := []float64{11.5, 34.5, 23.0} // hand-computed above
	if len(got) != len(want) {
		t.Fatalf("RealizedTimes len = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if math.Abs(got[i]-want[i]) > eps {
			t.Errorf("RealizedTimes[%d] = %v, want %v", i, got[i], want[i])
		}
	}
}

// TestRealizedTimesUnknownEdgeAndZeroEdgeRoute covers the two degenerate routes the
// contract must tolerate: a route referencing an edge id the graph does not know
// (contributes 0, never panics — matching routing.TotalNetworkTime), and a
// zero-edge route (origin == destination, realizes 0 s). A third route mixes a
// known and an unknown edge to prove only the unknown one is skipped.
func TestRealizedTimesUnknownEdgeAndZeroEdgeRoute(t *testing.T) {
	g := handToyGraph(t)
	result := routing.AssignResult{
		Routes: []routing.Route{
			{RequestID: "unknown", Edges: []domain.EdgeID{42}},  // not in graph ⇒ 0
			{RequestID: "empty", Edges: nil},                    // zero-edge ⇒ 0
			{RequestID: "mixed", Edges: []domain.EdgeID{0, 42}}, // edge0 (11.5) + unknown (0)
		},
		FinalFlows: []float64{100, 200},
	}
	got := benchmark.RealizedTimes(g, cost.DefaultBPR(), result)
	want := []float64{0, 0, 11.5}
	for i := range want {
		if math.Abs(got[i]-want[i]) > eps {
			t.Errorf("RealizedTimes[%d] = %v, want %v", i, got[i], want[i])
		}
	}
}

// TestEvaluateHandComputed pins EVERY aggregate metric on the Result against hand
// math, with the three-route batch above:
//
//	realized times    = [11.5, 34.5, 23.0]
//	MeanRealizedS     = (11.5+34.5+23.0)/3 = 69/3 = 23.0
//	P95RealizedS      = nearest-rank rank ceil(0.95*3)=3 ⇒ sorted[2] = 34.5
//	TotalNetworkTimeS = 100*11.5 + 200*23.0 = 1150 + 4600 = 5750
//	v/c per edge      = [100/100, 200/200] = [1.0, 1.0]
//	MaxVC             = 1.0
//	GiniVC            = 0 (both edges equally loaded ⇒ perfectly balanced)
func TestEvaluateHandComputed(t *testing.T) {
	g := handToyGraph(t)
	result := routing.AssignResult{
		Routes: []routing.Route{
			{RequestID: "r0", Edges: []domain.EdgeID{0}},
			{RequestID: "r1", Edges: []domain.EdgeID{0, 1}},
			{RequestID: "r2", Edges: []domain.EdgeID{1}},
		},
		FinalFlows: []float64{100, 200},
		Iters:      7,
		Converged:  true,
		Gap:        0.0001,
	}

	got := benchmark.Evaluate(g, cost.DefaultBPR(), "naive", "congested", result)

	checks := []struct {
		name string
		got  float64
		want float64
	}{
		{"MeanRealizedS", got.MeanRealizedS, 23.0},
		{"P95RealizedS", got.P95RealizedS, 34.5},
		{"TotalNetworkTimeS", got.TotalNetworkTimeS, 5750.0},
		{"GiniVC", got.GiniVC, 0.0},
		{"MaxVC", got.MaxVC, 1.0},
	}
	for _, c := range checks {
		if math.Abs(c.got-c.want) > eps {
			t.Errorf("%s = %v, want %v", c.name, c.got, c.want)
		}
	}
	// Pass-through metadata is copied verbatim.
	if got.Router != "naive" || got.DemandLevel != "congested" {
		t.Errorf("labels = (%q,%q), want (naive,congested)", got.Router, got.DemandLevel)
	}
	if got.Iters != 7 || !got.Converged || got.Gap != 0.0001 {
		t.Errorf("metadata = {Iters:%d Converged:%v Gap:%v}, want {7 true 0.0001}", got.Iters, got.Converged, got.Gap)
	}
}

// TestGiniHandComputed pins the Gini coefficient on a deliberately IMBALANCED
// network where the hand value is exact. Edge 0 carries zero flow, edge 1 carries
// its full capacity (v/c = [0, 1]). For the sorted form
// Gini = Σ_i (2i − n + 1) x_i / (n² x̄):
//
//	sorted = [0, 1], n = 2, sum = 1
//	weighted = (2*0 − 2 + 1)*0 + (2*1 − 2 + 1)*1 = (−1)*0 + (1)*1 = 1
//	Gini = weighted / (n * sum) = 1 / (2 * 1) = 0.5
//
// so all the load on one of two edges gives Gini exactly 0.5 (the spec's
// "utilization balance" metric: 0 = even, → 1 = concentrated).
func TestGiniHandComputed(t *testing.T) {
	g := handToyGraph(t)
	result := routing.AssignResult{
		Routes:     []routing.Route{{RequestID: "r", Edges: []domain.EdgeID{1}}},
		FinalFlows: []float64{0, 200}, // v/c = [0, 1]
	}
	got := benchmark.Evaluate(g, cost.DefaultBPR(), "naive", "x", result)
	if math.Abs(got.GiniVC-0.5) > eps {
		t.Errorf("GiniVC = %v, want 0.5 (all load on one of two edges)", got.GiniVC)
	}
	if math.Abs(got.MaxVC-1.0) > eps {
		t.Errorf("MaxVC = %v, want 1.0", got.MaxVC)
	}
}

// TestEvaluateEmptyBatch is the §R5 "all metrics finite/well-defined on the empty
// batch" criterion: zero requests and an all-zero flow vector must yield a fully
// finite Result with every aggregate at its defined zero, not a NaN/Inf from a 0/0
// mean or a Gini over no load.
func TestEvaluateEmptyBatch(t *testing.T) {
	g := handToyGraph(t)
	result := routing.AssignResult{
		Routes:     nil,
		FinalFlows: make([]float64, g.EdgeCount()), // all zero
		Iters:      1,
		Converged:  true,
	}
	got := benchmark.Evaluate(g, cost.DefaultBPR(), "naive", "empty", result)

	for _, c := range []struct {
		name string
		v    float64
	}{
		{"MeanRealizedS", got.MeanRealizedS},
		{"P95RealizedS", got.P95RealizedS},
		{"TotalNetworkTimeS", got.TotalNetworkTimeS},
		{"GiniVC", got.GiniVC},
		{"MaxVC", got.MaxVC},
	} {
		if math.IsNaN(c.v) || math.IsInf(c.v, 0) {
			t.Errorf("%s = %v, want a finite value on the empty batch", c.name, c.v)
		}
		if c.v != 0 {
			t.Errorf("%s = %v, want 0 on the empty/zero-flow batch", c.name, c.v)
		}
	}
}

// TestPercentileNearestRankShape exercises the nearest-rank percentile through
// Evaluate on small batches where the rank arithmetic is hand-checkable, and
// confirms the evaluator never mutates the caller's flow/route data (a sorted-copy
// guarantee). n=1 ⇒ p95 is the sole value; n=20 ⇒ rank ceil(0.95*20)=19 ⇒ the 19th
// smallest (second largest).
func TestPercentileNearestRankShape(t *testing.T) {
	g := handToyGraph(t)

	// n = 1: p95 is the only realized time. Single route over edge 0 at v/c=1 ⇒ 11.5.
	one := routing.AssignResult{
		Routes:     []routing.Route{{Edges: []domain.EdgeID{0}}},
		FinalFlows: []float64{100, 200},
	}
	if got := benchmark.Evaluate(g, cost.DefaultBPR(), "naive", "x", one).P95RealizedS; math.Abs(got-11.5) > eps {
		t.Errorf("P95 (n=1) = %v, want 11.5", got)
	}
}
