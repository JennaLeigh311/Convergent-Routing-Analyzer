package routing

import (
	"context"
	"fmt"
	"math"
	"testing"

	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/cost"
	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/graph"
)

// TestMarginalWeightFromFlowsUsesMarginalCost is the robust, fixture-independent guard that
// systemoptimal routes on the MARGINAL cost, not BPR.Cost. At a positive load the two bases
// diverge (MarginalCost adds the (beta+1) externality term), so the SO weight factory must
// return MarginalCost and must NOT equal Cost. This pins the single swap that distinguishes
// SO from msa directly at the factory — so a regression that reverted marginalWeightFromFlows
// to weightFromFlows is caught regardless of whether the toy fixture's SO and UE flows happen
// to coincide (its price of anarchy is ≈ 1, which makes end-to-end totals unable to tell them
// apart). cf. weightFromFlows, which returns BPR.Cost.
func TestMarginalWeightFromFlowsUsesMarginalCost(test *testing.T) {
	bpr := cost.DefaultBPR()
	edge := graph.Edge{ID: 0, From: 0, To: 1, FreeFlowS: 10, CapacityVPH: 100}
	const load = 200.0 // v/c = 2: a positive load where MarginalCost strictly exceeds Cost
	flows := []float64{load}

	got := marginalWeightFromFlows(bpr, flows)(edge)
	wantMarginal := bpr.MarginalCost(edge, load)
	bprCost := bpr.Cost(edge, load)

	if math.Abs(got-wantMarginal) > 1e-9 {
		test.Errorf("marginalWeightFromFlows weight = %v, want MarginalCost %v", got, wantMarginal)
	}
	if math.Abs(got-bprCost) < 1e-9 {
		test.Errorf("marginalWeightFromFlows returned the BPR.Cost value %v — the SO routing basis must be the steeper MarginalCost (%v), not Cost", got, wantMarginal)
	}
	// And weightFromFlows (the UE family) must still return Cost, so the two factories are
	// genuinely on different bases.
	if ue := weightFromFlows(bpr, flows)(edge); math.Abs(ue-bprCost) > 1e-9 {
		test.Errorf("weightFromFlows weight = %v, want BPR.Cost %v", ue, bprCost)
	}
}

// TestCombineFlowsSumsShardsElementwise pins the sharded-flow REDUCE helper #71
// deferred: combineFlows sums per-worker dense shards element-wise into one vector of
// the requested length, in deterministic edge order.
func TestCombineFlowsSumsShardsElementwise(test *testing.T) {
	shards := [][]float64{
		{1, 0, 2, 0},
		{0, 3, 2, 0},
		{0, 0, 0, 5},
	}
	got := combineFlows(4, shards)
	want := []float64{1, 3, 4, 5}
	if len(got) != len(want) {
		test.Fatalf("combineFlows length = %d, want %d", len(got), len(want))
	}
	for index := range want {
		if got[index] != want[index] {
			test.Errorf("combineFlows[%d] = %v, want %v", index, got[index], want[index])
		}
	}
}

// TestCombineFlowsNoShards returns a full-length all-zero vector for no shards (an
// empty fan-out), never nil.
func TestCombineFlowsNoShards(test *testing.T) {
	got := combineFlows(3, nil)
	if len(got) != 3 {
		test.Fatalf("combineFlows(3, nil) length = %d, want 3", len(got))
	}
	for index, value := range got {
		if value != 0 {
			test.Errorf("combineFlows(3, nil)[%d] = %v, want 0", index, value)
		}
	}
}

// TestCombineFlowsToleratesRaggedShards confirms a shard shorter than n contributes 0
// for its missing tail and a shard longer than n ignores its overflow, so a defensive
// size mismatch never panics or corrupts the reduce.
func TestCombineFlowsToleratesRaggedShards(test *testing.T) {
	shards := [][]float64{
		{1, 1},          // short: contributes only to edges 0,1
		{2, 2, 2, 2, 2}, // long: edges 0..2 used, the rest ignored
	}
	got := combineFlows(3, shards)
	want := []float64{3, 3, 2}
	for index := range want {
		if got[index] != want[index] {
			test.Errorf("combineFlows ragged[%d] = %v, want %v", index, got[index], want[index])
		}
	}
}

// TestCombineFlowsOrderStable confirms the reduce is order-stable across calls (the
// determinism property the concurrent fan-out relies on): the same shards summed in
// the same fixed order give byte-identical output every time.
func TestCombineFlowsOrderStable(test *testing.T) {
	shards := [][]float64{
		{0.1, 0.2, 0.3},
		{0.7, 0.8, 0.9},
		{0.01, 0.02, 0.03},
	}
	first := combineFlows(3, shards)
	second := combineFlows(3, shards)
	for index := range first {
		if first[index] != second[index] {
			test.Errorf("combineFlows not order-stable at [%d]: %v vs %v", index, first[index], second[index])
		}
	}
}

// TestRelativeGapZeroWhenNoDemand pins that a zero SPTT (empty/same-node batch) yields
// a defined gap of 0, never a 0/0 NaN or x/0 +Inf.
func TestRelativeGapZeroWhenNoDemand(test *testing.T) {
	if got := relativeGap(0, 0); got != 0 {
		test.Errorf("relativeGap(0,0) = %v, want 0", got)
	}
	if got := relativeGap(5, 0); got != 0 {
		test.Errorf("relativeGap(5,0) = %v, want 0", got)
	}
}

// TestRelativeGapAtEquilibrium confirms the gap is 0 when the cost on the current
// flows equals the all-or-nothing total (the UE condition TSTT == SPTT).
func TestRelativeGapAtEquilibrium(test *testing.T) {
	if got := relativeGap(100, 100); got != 0 {
		test.Errorf("relativeGap(100,100) = %v, want 0", got)
	}
}

// TestRelativeGapPositiveAwayFromEquilibrium confirms a positive, finite gap when the
// realized cost exceeds the all-or-nothing total, and that a tiny floating-point
// negative (TSTT just below SPTT near convergence) reads as a small magnitude, never a
// negative gap.
func TestRelativeGapPositiveAwayFromEquilibrium(test *testing.T) {
	got := relativeGap(150, 100)
	if math.Abs(got-0.5) > 1e-12 {
		test.Errorf("relativeGap(150,100) = %v, want 0.5", got)
	}
	if neg := relativeGap(100-1e-9, 100); neg < 0 {
		test.Errorf("relativeGap near convergence = %v, want a non-negative magnitude", neg)
	}
}

// TestWorkersForBounds pins the fan-out worker-count policy: at least 1 (even for an
// empty batch), never more than the request count.
func TestWorkersForBounds(test *testing.T) {
	if got := WorkersFor(0); got != 1 {
		test.Errorf("WorkersFor(0) = %d, want 1", got)
	}
	if got := WorkersFor(-5); got != 1 {
		test.Errorf("WorkersFor(-5) = %d, want 1", got)
	}
	if got := WorkersFor(1); got != 1 {
		test.Errorf("WorkersFor(1) = %d, want 1 (never more workers than requests)", got)
	}
}

// TestBatchBoundsEvenSplit confirms incremental's demand split is even and exhaustive:
// the ranges tile [0,n) with no gap or overlap, and the remainder lands on the first
// batches.
func TestBatchBoundsEvenSplit(test *testing.T) {
	bounds := batchBounds(10, 4) // 10 into 4 => sizes 3,3,2,2
	wantSizes := []int{3, 3, 2, 2}
	at := 0
	for index, b := range bounds {
		if b.lo != at {
			test.Errorf("batch %d lo = %d, want %d (contiguous tiling)", index, b.lo, at)
		}
		if got := b.hi - b.lo; got != wantSizes[index] {
			test.Errorf("batch %d size = %d, want %d", index, got, wantSizes[index])
		}
		at = b.hi
	}
	if at != 10 {
		test.Errorf("batches cover [0,%d), want [0,10)", at)
	}
}

// TestBatchBoundsFewerRequestsThanBatches confirms trailing batches are empty (not
// negative-sized) when there are fewer requests than batches, and the count is always
// exactly `batches`.
func TestBatchBoundsFewerRequestsThanBatches(test *testing.T) {
	bounds := batchBounds(2, 4) // sizes 1,1,0,0
	if len(bounds) != 4 {
		test.Fatalf("batchBounds(2,4) returned %d ranges, want 4", len(bounds))
	}
	total := 0
	for index, b := range bounds {
		size := b.hi - b.lo
		if size < 0 {
			test.Errorf("batch %d has negative size %d", index, size)
		}
		total += size
	}
	if total != 2 {
		test.Errorf("batches cover %d requests, want 2", total)
	}
}

// TestMSAReportedConvergenceMatchesIndependentGap guards the MSA gap-metric fix: the
// reported convergence must be backed by FinalFlows genuinely sitting near user
// equilibrium under a CONSISTENT, single-weight-vector gap measurement. It recomputes
// the relative gap of FinalFlows independently — TSTT under t(FinalFlows) against an
// all-or-nothing SPTT under those SAME weights — and requires it below the tolerance.
// A regression to the old metric (TSTT taken at the averaged flow while SPTT is taken
// at the pre-average flow — two different flow/weight vectors) could report Converged
// off a gap that this consistent re-measurement would not corroborate.
func TestMSAReportedConvergenceMatchesIndependentGap(test *testing.T) {
	roadGraph := loadToyGraphInternal(test)
	node0, _ := roadGraph.Node(0)
	node2, _ := roadGraph.Node(2)
	reqs := make([]RouteRequest, 50)
	for index := range reqs {
		reqs[index] = RouteRequest{ID: fmt.Sprintf("r%d", index), From: node0.Pos, To: node2.Pos, Weight: 200}
	}

	router := NewMSARouter(roadGraph, cost.DefaultBPR())
	result, err := router.AssignResult(context.Background(), reqs)
	if err != nil {
		test.Fatalf("AssignResult() error = %v", err)
	}
	if !result.Converged {
		test.Fatalf("MSA did not converge (gap %v after %d iters)", result.Gap, result.Iters)
	}

	// Independent, consistent gap of FinalFlows: both terms under t(FinalFlows).
	pairs, err := prefetchOD(roadGraph, reqs, "test")
	if err != nil {
		test.Fatalf("prefetchOD() error = %v", err)
	}
	weight := weightFromFlows(cost.DefaultBPR(), result.FinalFlows)
	aon, err := assignAONConcurrent(context.Background(), roadGraph, pairs, reqs, weight, "test")
	if err != nil {
		test.Fatalf("assignAONConcurrent() error = %v", err)
	}
	independentGap := relativeGap(totalSystemCost(roadGraph, cost.DefaultBPR(), result.FinalFlows), aon.totalSP)
	if !(independentGap < gapTolerance) {
		test.Errorf("independent gap of FinalFlows = %v, want < gapTolerance %v (reported Converged with Gap %v) — FinalFlows is not actually near equilibrium",
			independentGap, gapTolerance, result.Gap)
	}
}
