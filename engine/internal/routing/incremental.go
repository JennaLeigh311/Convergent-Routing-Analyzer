package routing

import (
	"context"

	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/cost"
	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/graph"
)

// incrementalBatches is the number of equal batches incremental loads the demand in.
// Loading the demand in N slices and re-weighting after each is what places
// incremental BETWEEN user-equilibrium and system-optimal (docs/algorithms.md §2): a
// single batch would collapse to one best-response pass (reactive over zero load,
// i.e. naive), while many tiny batches approach an equilibrium. Four is the
// conventional middling choice that keeps the toy network's behavior legible while
// still re-weighting enough times to divert later batches off the corridors the
// earlier ones loaded. A batch count above the request count is harmless — empty
// trailing batches just contribute no flow.
const incrementalBatches = 4

// IncrementalRouter is the incremental-assignment strategy (algorithm catalog row 3,
// project-spec.md §4; docs/algorithms.md §2): it splits the demand into equal batches
// and assigns one batch per iteration, RE-WEIGHTING every edge with BPR over the flow
// loaded so far before each batch. Because each batch best-responds to the congestion
// the EARLIER batches created, later batches divert off the corridors the first ones
// filled — so the assignment lands BETWEEN user equilibrium and system optimum (it
// neither fully equilibrates like MSA nor internalizes the marginal externality like
// systemoptimal).
//
// Each iteration k (1-based) loads batch k-1:
//
//	weight every edge with BPR over the CUMULATIVE flow loaded so far
//	AON-assign ONLY this batch's requests on their current-cost shortest paths
//	add this batch's flow to the cumulative flow (it is never re-removed)
//	gap <- (TSTT(cumulative) - SPTT_batch) / SPTT_batch
//
// Unlike MSA, incremental does NOT average or iterate to a fixed point — once every
// batch is loaded the assignment is complete (the loop's `done` signal), so it
// converges in exactly incrementalBatches iterations (or fewer if the gap criterion is
// met first). The reported routes are each request's path from the batch in which it
// was loaded.
//
// The router holds only the immutable graph and a stateless BPR cost function (no
// congestion provider — it derives load from the flow it accumulates), so its methods
// are safe for concurrent use; each Assign owns its cumulative flow vector and fans
// each batch across goroutines with per-worker scratch (see iterative.go).
type IncrementalRouter struct {
	g   graph.Graph
	bpr cost.BPR
}

// NewIncrementalRouter returns an IncrementalRouter over the (immutable,
// already-loaded) graph that weights edges with bpr (see cost.DefaultBPR). As with
// MSA it takes the concrete cost.BPR because the gap metric and the shared iterative
// core are BPR-specific.
func NewIncrementalRouter(roadGraph graph.Graph, bpr cost.BPR) *IncrementalRouter {
	return &IncrementalRouter{g: roadGraph, bpr: bpr}
}

// Compile-time assertion: *IncrementalRouter satisfies the Router port.
var _ Router = (*IncrementalRouter)(nil)

// Name identifies this strategy in benchmark output and the API.
func (router *IncrementalRouter) Name() string { return "incremental" }

// Route answers a single request on free-flow weights: a lone request shares no
// congestion with anyone, so its incremental path is just its free-flow shortest path
// (the batching only matters for a batch where requests interact). A cancelled
// context, an endpoint that snaps to no node, or an unreachable destination is an
// error; same-node From/To is a clean zero-edge, zero-cost Route.
func (router *IncrementalRouter) Route(ctx context.Context, req RouteRequest) (Route, error) {
	if err := ctx.Err(); err != nil {
		return Route{}, err
	}
	return routeSingleFreeFlow(router.g, req, router.Name())
}

// Assign solves the batch problem and returns just the routes, in input order — the
// paths-only face of AssignResult.
func (router *IncrementalRouter) Assign(ctx context.Context, reqs []RouteRequest) ([]Route, error) {
	return AssignFromResult(ctx, reqs, router.AssignResult)
}

// AssignResult runs incremental assignment and reports the full outcome: the routes
// (input order — each request's path from the batch it was loaded in), the dense
// FinalFlows after every batch is loaded, and the convergence metadata.
//
// Snapshot discipline (project-spec.md §R5): incremental owns its load — it derives
// each batch's weights from the cumulative flow it itself accumulates, takes no
// external congestion provider, and never mutates shared state mid-loop. Each batch
// reads the cumulative flow as an IMMUTABLE vector and writes a fresh one, so every
// request in a batch provably sees one stable view. Request weights are the final
// demand (no re-scaling); a missing-segment load is free-flow (0), not an error. On
// the first unreachable OD pair it returns a zero AssignResult and that error.
func (router *IncrementalRouter) AssignResult(ctx context.Context, reqs []RouteRequest) (AssignResult, error) {
	pairs, err := prefetchOD(router.g, reqs, router.Name())
	if err != nil {
		return AssignResult{}, err
	}

	bounds := batchBounds(len(reqs), incrementalBatches)
	// routes accumulates across batches: each request is routed exactly once, in its
	// batch, and written at its global input index, so the final slice is in input
	// order. Closed over the step so it persists across iterations.
	routes := make([]Route, len(reqs))

	step := func(ctx context.Context, k int, flow []float64) ([]float64, []Route, float64, bool, error) {
		batchIndex := k - 1
		lo, hi := bounds[batchIndex].lo, bounds[batchIndex].hi
		done := batchIndex == len(bounds)-1 // last batch: assignment complete

		// Weight edges with BPR over the cumulative flow loaded so far, then AON-assign
		// ONLY this batch. The fan-out gives each worker its own scratch and shard.
		weight := weightFromFlows(router.bpr, flow)
		outcome, err := assignAONConcurrent(ctx, router.g, pairs[lo:hi], reqs[lo:hi], weight, router.Name())
		if err != nil {
			return nil, nil, 0, false, err
		}
		copy(routes[lo:hi], outcome.routes) // place this batch's routes at their global indices

		// Add this batch's flow to the cumulative flow (cumulative, never re-removed),
		// written into a fresh vector so the previous flow (closed over by `weight`) is
		// never mutated.
		next := newFlowVector(router.g)
		for _, edgeID := range SortedEdgeIDs(router.g) { // sorted, never map order
			next[edgeID] = loadAt(flow, edgeID) + loadAt(outcome.flows, edgeID)
		}

		// The reported gap is the standard relative gap of the WHOLE cumulative flow,
		// not of this one batch: re-route ALL requests under the new cumulative weights
		// (an all-or-nothing pass over every OD) to get the equilibrium-reference SPTT,
		// then compare it to TSTT(cumulative). Comparing the batch's own SPTT would mix
		// scales (a few requests' shortest-path cost against the whole network's load)
		// and report a meaningless gap. Incremental still stops on `done`, not the gap;
		// this just makes the reported number an honest distance-from-equilibrium.
		gapWeight := weightFromFlows(router.bpr, next)
		gapOutcome, err := assignAONConcurrent(ctx, router.g, pairs, reqs, gapWeight, router.Name())
		if err != nil {
			return nil, nil, 0, false, err
		}
		gap := relativeGap(totalSystemCost(router.g, router.bpr, next), gapOutcome.totalSP)
		return next, routes, gap, done, nil
	}

	return runConvergenceLoop(ctx, router.g, step)
}

// batchRange is one half-open slice [lo, hi) of the input request order assigned to a
// single incremental batch.
type batchRange struct{ lo, hi int }

// batchBounds splits n requests into batches half-open ranges, distributing the
// remainder so the first (n mod batches) batches get one extra request — an even,
// deterministic split. With fewer requests than batches the trailing batches are
// empty ([hi,hi)), which assignAONConcurrent handles as a no-op (zero flow). It always
// returns exactly `batches` ranges (>= 1) so the convergence loop runs a fixed number
// of iterations regardless of request count.
func batchBounds(n, batches int) []batchRange {
	if batches < 1 {
		batches = 1
	}
	bounds := make([]batchRange, batches)
	base := n / batches
	remainder := n % batches
	at := 0
	for batchIndex := 0; batchIndex < batches; batchIndex++ {
		size := base
		if batchIndex < remainder {
			size++
		}
		bounds[batchIndex] = batchRange{lo: at, hi: at + size}
		at += size
	}
	return bounds
}
