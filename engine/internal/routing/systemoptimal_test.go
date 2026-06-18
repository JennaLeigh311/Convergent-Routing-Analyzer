package routing_test

import (
	"bytes"
	"context"
	"fmt"
	"math"
	"reflect"
	"testing"

	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/cost"
	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/domain"
	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/graph"
	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/routing"
)

// TestTotalNetworkTimeHandComputed pins the exported total-network-time evaluator
// (project-spec.md §R5) against a HAND-COMPUTED toy case. It builds a tiny two-edge
// graph with chosen free-flow times and capacities and a flow vector where every load
// equals the edge's capacity (so the BPR ratio v/c is exactly 1), which makes
//
//	BPR.Cost(e) = FreeFlowS * (1 + alpha*(v/c)^beta) = FreeFlowS * (1 + 0.15) = 1.15*FreeFlowS
//
// computable without powBeta gymnastics. With FreeFlowS = {10, 20} and loads equal to
// the capacities {100, 200}:
//
//	edge0: 100 * (10  * 1.15) = 100 * 11.5 = 1150
//	edge1: 200 * (20  * 1.15) = 200 * 23.0 = 4600
//	total = 5750
//
// asserted to within 1e-9 of the hand-computed 5750.
func TestTotalNetworkTimeHandComputed(test *testing.T) {
	nodes := []graph.Node{
		{ID: 0, Pos: domain.LatLon{Lat: 0, Lon: 0}},
		{ID: 1, Pos: domain.LatLon{Lat: 0, Lon: 1}},
		{ID: 2, Pos: domain.LatLon{Lat: 0, Lon: 2}},
	}
	edges := []graph.Edge{
		{ID: 0, Segment: "e0", From: 0, To: 1, FreeFlowS: 10, CapacityVPH: 100},
		{ID: 1, Segment: "e1", From: 1, To: 2, FreeFlowS: 20, CapacityVPH: 200},
	}
	roadGraph, err := graph.New(nodes, edges)
	if err != nil {
		test.Fatalf("graph.New() error = %v", err)
	}

	bpr := cost.DefaultBPR() // alpha=0.15, beta=4, capacityScale=1.0
	flows := []float64{100, 200}

	const want = 5750.0 // hand-computed above
	got := routing.TotalNetworkTime(roadGraph, bpr, flows)
	if math.Abs(got-want) > 1e-9 {
		test.Errorf("TotalNetworkTime = %v, want %v (hand-computed Σ flow×BPR.Cost)", got, want)
	}
}

// TestTotalNetworkTimeEmptyFlow confirms a zero flow yields zero total time (no vehicles
// means no travel time), and that a missing/out-of-range flow entry contributes nothing.
// It exercises BOTH the fully-nil case and a flow vector SHORTER than EdgeCount, so the
// out-of-range branch of loadAt (an edge id past the slice reads as zero load) is covered
// directly, not just the nil case.
func TestTotalNetworkTimeEmptyFlow(test *testing.T) {
	roadGraph := loadToyGraph(test)
	if got := routing.TotalNetworkTime(roadGraph, cost.DefaultBPR(), nil); got != 0 {
		test.Errorf("TotalNetworkTime(nil flows) = %v, want 0", got)
	}
	// A short, all-zero slice (length 1 < EdgeCount): every in-range edge has zero load
	// and every edge id past the slice falls through loadAt's out-of-range guard to zero,
	// so the realized total must still be exactly 0.
	if got := routing.TotalNetworkTime(roadGraph, cost.DefaultBPR(), []float64{0}); got != 0 {
		test.Errorf("TotalNetworkTime(short zero flows) = %v, want 0 (out-of-range edges contribute nothing)", got)
	}
}

// soDemand builds n node0->node2 requests on the toy network, the
// lowest-cost-≠-fewest-hops pair whose 2-hop motorway congests under enough demand so a
// system-optimal assignment must internalize the externality and split flow.
func soDemand(test *testing.T, roadGraph *graph.AdjacencyGraph, n int, weight float64) []routing.RouteRequest {
	test.Helper()
	node0, _ := roadGraph.Node(0)
	node2, _ := roadGraph.Node(2)
	reqs := make([]routing.RouteRequest, n)
	for index := range reqs {
		reqs[index] = routing.RouteRequest{
			ID:     fmt.Sprintf("r%d", index),
			From:   node0.Pos,
			To:     node2.Pos,
			Weight: weight,
		}
	}
	return reqs
}

// TestSystemOptimalReportsFiniteMarginalGap is the SO acceptance criterion
// (issue #76): on the toy network systemoptimal reports a FINITE, non-negative achieved
// gap and a bounded iteration count — where that gap is the MARGINAL relative gap (SO
// routes and converges on BPR.MarginalCost), the correct convergence criterion for
// System-Optimal-as-UE-on-the-marginal-network.
//
// Note it does NOT require Converged = true / Gap < 1e-4. The shared core's 1/k MSA
// averaging reaches the 1e-4 gap on the gentle BPR.Cost network (TestMSAConvergesOnToyNetwork)
// but NOT on the marginal network within the 100-iteration budget: the BPR marginal term
// carries a (beta+1) = 5x steeper slope than the cost term, so the marginal network is far
// stiffer and 1/k averaging equilibrates it slowly. The iteration still does real work —
// the marginal gap falls from 1.0 at k=1 to ~0.008 here (a >99% reduction) — so SO reports
// the honest "did-not-fully-converge-in-budget" result the core is designed to give
// (Converged = false with a finite, materially-reduced gap), never a crash or a fabricated
// 1e-4. That partially-equilibrated flow is still enough to satisfy the SO <= UE invariant
// (TestSystemOptimalNoWorseThanUE). A fixed budget of 100 iterations is the shared-core
// contract; reaching 1e-4 on the marginal network would need a line-searched step
// (Frank-Wolfe), which is out of scope here.
func TestSystemOptimalReportsFiniteMarginalGap(test *testing.T) {
	roadGraph := loadToyGraph(test)
	reqs := soDemand(test, roadGraph, 50, 200)

	router := routing.NewSystemOptimalRouter(roadGraph, cost.DefaultBPR())
	result, err := router.AssignResult(context.Background(), reqs)
	if err != nil {
		test.Fatalf("AssignResult() error = %v", err)
	}
	if math.IsNaN(result.Gap) || math.IsInf(result.Gap, 0) || result.Gap < 0 {
		test.Errorf("systemoptimal Gap = %v, want a finite non-negative marginal relative gap", result.Gap)
	}
	if result.Iters < 1 || result.Iters > 100 {
		test.Errorf("systemoptimal Iters = %d, want in [1,100]", result.Iters)
	}
	// The averaging must do real work on the marginal network: from the k=1 gap of 1.0 the
	// marginal gap settles to ~0.008 here. The bound is anchored to that observed value with
	// headroom (< 0.02) rather than a loose band, so a regression that materially worsens the
	// equilibration is visible instead of silently absorbed — while still not demanding the
	// 1e-4 the gentler cost network hits in budget.
	if !(result.Gap < 0.02) {
		test.Errorf("systemoptimal marginal Gap = %v after %d iters, want < 0.02 (observed ~0.008; the 1/k average should still materially equilibrate the marginal network)", result.Gap, result.Iters)
	}
	test.Logf("systemoptimal marginal gap = %v after %d iters (Converged = %v)", result.Gap, result.Iters, result.Converged)
}

// TestSystemOptimalName pins the strategy name used in benchmark output / the API.
func TestSystemOptimalName(test *testing.T) {
	roadGraph := loadToyGraph(test)
	if got := routing.NewSystemOptimalRouter(roadGraph, cost.DefaultBPR()).Name(); got != "systemoptimal" {
		test.Errorf("SystemOptimalRouter.Name() = %q, want %q", got, "systemoptimal")
	}
}

// TestSystemOptimalRouteSingleFreeFlow confirms a lone request collapses to its free-flow
// shortest path: with no other vehicle to impose a marginal cost on, SO's path is the
// same 2-hop motorway (905512:0:F + 905512:1:F) naive returns.
func TestSystemOptimalRouteSingleFreeFlow(test *testing.T) {
	roadGraph := loadToyGraph(test)
	node0, _ := roadGraph.Node(0)
	node2, _ := roadGraph.Node(2)
	req := routing.RouteRequest{ID: "single", From: node0.Pos, To: node2.Pos}

	router := routing.NewSystemOptimalRouter(roadGraph, cost.DefaultBPR())
	route, err := router.Route(context.Background(), req)
	if err != nil {
		test.Fatalf("Route() error = %v", err)
	}
	want := []domain.SegmentID{"905512:0:F", "905512:1:F"}
	got := segmentsOf(test, roadGraph, route.Edges)
	if len(got) != len(want) {
		test.Fatalf("Route edges = %v, want %v", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			test.Errorf("Route edge[%d] = %q, want %q", index, got[index], want[index])
		}
	}
}

// TestSystemOptimalDeterministic is the SO determinism acceptance criterion: SO uses NO
// RNG — its determinism is structural, so two AssignResult runs over the same batch
// produce reflect.DeepEqual FinalFlows. The OD set is round-tripped through Write/ReadODSet
// (the real on-disk form) and each run fans across goroutines, so this also pins that the
// marginal-cost fan-out's sharded reduce is order-stable regardless of scheduler order.
func TestSystemOptimalDeterministic(test *testing.T) {
	roadGraph := loadToyGraph(test)
	original := soDemand(test, roadGraph, 60, 200)

	var buf bytes.Buffer
	if err := routing.WriteODSet(&buf, original); err != nil {
		test.Fatalf("WriteODSet() error = %v", err)
	}
	serialized := buf.Bytes()

	runOnce := func() routing.AssignResult {
		reqs, err := routing.ReadODSet(bytes.NewReader(serialized))
		if err != nil {
			test.Fatalf("ReadODSet() error = %v", err)
		}
		result, err := routing.NewSystemOptimalRouter(roadGraph, cost.DefaultBPR()).AssignResult(context.Background(), reqs)
		if err != nil {
			test.Fatalf("AssignResult() error = %v", err)
		}
		return result
	}

	first := runOnce()
	second := runOnce()
	if !reflect.DeepEqual(first.FinalFlows, second.FinalFlows) {
		test.Errorf("systemoptimal FinalFlows not byte-identical across runs (no RNG ⇒ structural determinism):\n%v\nvs\n%v", first.FinalFlows, second.FinalFlows)
	}
	if first.Gap != second.Gap || first.Iters != second.Iters || first.Converged != second.Converged {
		test.Errorf("systemoptimal metadata differs across runs: {%v %d %v} vs {%v %d %v}",
			first.Gap, first.Iters, first.Converged, second.Gap, second.Iters, second.Converged)
	}
}

// TestSystemOptimalNoWorseThanUE is the project-spec §R5 SO ≤ UE invariant — the whole
// point of System Optimal. On an identical heavy batch it asserts the systemoptimal
// total network time is ≤ the msa (User Equilibrium) total network time, where BOTH are
// the REALIZED total time on BPR.Cost (TotalNetworkTime on each router's FinalFlows),
// NOT the marginal basis SO converged on. Internalizing the congestion externality must
// not raise total time above the selfish equilibrium. A tiny tolerance (1e-6) keeps a
// floating-point tie from flaking the inequality.
//
// SCOPE — this is a NON-REGRESSION check, not a demonstration of strict SO < UE. The toy
// network has no Braess/Pigou structure, so its price of anarchy is ≈ 1: SO and UE land on
// essentially the same split (observed soTotal ≈ ueTotal to ~1e-9), and the invariant holds
// as a near-equality. A fixture that exhibits a strict SO < UE (a genuine PoA > 1) belongs to
// the Phase-4 benchmark's realized-time / price-of-anarchy work and is deferred there; the
// distinguishing-from-UE guard for THIS PR is TestSystemOptimalDiffersFromMSA plus the
// internal TestMarginalWeightFromFlowsUsesMarginalCost, which pin the marginal cost basis
// directly rather than relying on a total-time gap this fixture cannot produce.
func TestSystemOptimalNoWorseThanUE(test *testing.T) {
	roadGraph := loadToyGraph(test)
	reqs := soDemand(test, roadGraph, 100, 200) // ~20k vph toward a 5,400 vph motorway: a genuine split

	bpr := cost.DefaultBPR()
	ueResult, err := routing.NewMSARouter(roadGraph, bpr).AssignResult(context.Background(), reqs)
	if err != nil {
		test.Fatalf("msa AssignResult() error = %v", err)
	}
	soResult, err := routing.NewSystemOptimalRouter(roadGraph, bpr).AssignResult(context.Background(), reqs)
	if err != nil {
		test.Fatalf("systemoptimal AssignResult() error = %v", err)
	}

	ueTotal := routing.TotalNetworkTime(roadGraph, bpr, ueResult.FinalFlows)
	soTotal := routing.TotalNetworkTime(roadGraph, bpr, soResult.FinalFlows)

	test.Logf("realized total network time (BPR.Cost): systemoptimal = %v, msa/UE = %v", soTotal, ueTotal)
	if !(soTotal <= ueTotal+1e-6) {
		test.Errorf("SO ≤ UE invariant violated: systemoptimal total %v > msa/UE total %v (internalizing the externality must not raise total time)", soTotal, ueTotal)
	}
}

// TestSystemOptimalDiffersFromMSA is a coarse end-to-end guard that SO routes on a
// DIFFERENT cost basis than msa: on the heavy batch their FinalFlows must not be
// byte-identical. A regression that reverted SO to msa wholesale (routing AND gap back on
// BPR.Cost) would make SO a literal msa clone — identical flows — and this catches that.
//
// It is deliberately coarse: on this PoA ≈ 1 fixture SO and msa land very close (they differ
// only because SO equilibrates the stiffer marginal network to a shallower gap in budget), so
// this proves "not the same router", not "correctly system-optimal". The precise, fixture-
// independent guard that SO uses MarginalCost is the internal
// TestMarginalWeightFromFlowsUsesMarginalCost; a strict SO < UE demonstration is the deferred
// Phase-4 Pigou/Braess fixture (see TestSystemOptimalNoWorseThanUE).
func TestSystemOptimalDiffersFromMSA(test *testing.T) {
	roadGraph := loadToyGraph(test)
	reqs := soDemand(test, roadGraph, 100, 200)
	bpr := cost.DefaultBPR()

	so, err := routing.NewSystemOptimalRouter(roadGraph, bpr).AssignResult(context.Background(), reqs)
	if err != nil {
		test.Fatalf("systemoptimal AssignResult() error = %v", err)
	}
	msa, err := routing.NewMSARouter(roadGraph, bpr).AssignResult(context.Background(), reqs)
	if err != nil {
		test.Fatalf("msa AssignResult() error = %v", err)
	}
	if reflect.DeepEqual(so.FinalFlows, msa.FinalFlows) {
		test.Errorf("systemoptimal FinalFlows are byte-identical to msa's — SO is not routing on a different (marginal) cost basis")
	}
}

// TestSystemOptimalCancelledContext confirms a pre-cancelled context aborts the batch
// Assign with an error and no routing — exercising the per-iteration ctx.Err() check the
// shared loop runs (the path iterativeRouters covers for msa/incremental, asserted here for
// SO too).
func TestSystemOptimalCancelledContext(test *testing.T) {
	roadGraph := loadToyGraph(test)
	reqs := soDemand(test, roadGraph, 10, 200)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := routing.NewSystemOptimalRouter(roadGraph, cost.DefaultBPR()).AssignResult(ctx, reqs); err == nil {
		test.Errorf("AssignResult with a cancelled context err = nil, want a context error")
	}
}

// TestSystemOptimalEmptyBatch pins SO's degenerate-input contract: an empty batch returns a
// full-length all-zero FinalFlows (not nil), a zero gap, and Converged = true — the same
// shape the other iterative routers return, never a divide-by-zero or nil.
func TestSystemOptimalEmptyBatch(test *testing.T) {
	roadGraph := loadToyGraph(test)
	result, err := routing.NewSystemOptimalRouter(roadGraph, cost.DefaultBPR()).AssignResult(context.Background(), nil)
	if err != nil {
		test.Fatalf("AssignResult(nil) error = %v", err)
	}
	if len(result.FinalFlows) != roadGraph.EdgeCount() {
		test.Errorf("empty-batch FinalFlows length = %d, want EdgeCount %d", len(result.FinalFlows), roadGraph.EdgeCount())
	}
	if result.Gap != 0 || !result.Converged {
		test.Errorf("empty-batch metadata = {Gap:%v Converged:%v}, want {0 true}", result.Gap, result.Converged)
	}
}
