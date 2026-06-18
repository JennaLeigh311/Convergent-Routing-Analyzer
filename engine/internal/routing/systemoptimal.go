package routing

import (
	"context"

	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/cost"
	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/domain"
	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/graph"
)

// SystemOptimalRouter is the system-optimal strategy (algorithm catalog row 5,
// project-spec.md §4; docs/algorithms.md §2, §5): it runs the SAME Method-of-Successive-
// Averages machine as msa, but routes every iteration's all-or-nothing assignment on
// BPR.MarginalCost instead of BPR.Cost, and CONVERGES to System Optimal — the
// assignment that minimizes TOTAL network travel time, not the selfish Nash
// equilibrium MSA finds.
//
// The single idea: System Optimal ≡ User Equilibrium on the MARGINAL-cost network. A
// driver who is charged the marginal cost — the delay it imposes on every other vehicle
// on the edge, not just its own travel time — internalizes the congestion externality
// and picks the path that is best for the SYSTEM rather than only for itself. So
// MSA-on-marginal converges to SO exactly as MSA-on-cost converges to UE.
//
// Each iteration k (1-based):
//
//	weight every edge with BPR.MarginalCost over the CURRENT flow x  (marginalWeightFromFlows)
//	AON-assign every request on its current-marginal-cost shortest path  -> auxiliary flow y
//	x <- x + (1/k)*(y - x)                                                (the MSA average)
//	gap <- (TSTT_marginal(x) - SPTT_marginal) / SPTT_marginal
//
// COST-BASIS distinction — the single most subtle thing to get right here:
//
//   - ROUTING + GAP are on MarginalCost. assignAONConcurrent computes its SPTT under
//     whatever weightFunc it is handed; here that is marginalWeightFromFlows, so
//     outcome.totalSP is the MARGINAL-weighted shortest-path total. For the relative gap
//     (TSTT-SPTT)/SPTT to be coherent its TSTT must be in the same basis, so SO's gap
//     TSTT is totalMarginalCost (Σ flow × MarginalCost), NOT totalSystemCost. Converging
//     on the bpr.Cost gap would stop the loop on the wrong condition (the forward-looking
//     note in iterative.go that claimed the gap "stays on bpr.Cost for both" was wrong
//     for SO and has been corrected). MSA-on-marginal's correct convergence criterion is
//     the relative gap on the marginal network.
//   - The REPORTED / COMPARED total network time stays on BPR.Cost. The realized travel
//     time the SO ≤ UE invariant (project-spec.md §R5) compares both strategies on is
//     TotalNetworkTime (Σ flow × BPR.Cost) applied to FinalFlows — the real time drivers
//     experience, regardless of the marginal basis SO routed/converged on. SO minimizes
//     that objective; UE does not. Do not conflate the gap basis (marginal) with the
//     objective (BPR.Cost).
//
// The router holds only the immutable graph and a stateless BPR cost function (no
// congestion provider — it derives load from the flow it accumulates), so its methods
// are safe for concurrent use; each Assign owns its per-iteration flow vectors and fans
// requests across goroutines with per-worker scratch (see iterative.go). SO uses NO RNG:
// its determinism is structural — a fixed OD set yields byte-identical FinalFlows.
type SystemOptimalRouter struct {
	g   graph.Graph
	bpr cost.BPR
}

// NewSystemOptimalRouter returns a SystemOptimalRouter over the (immutable,
// already-loaded) graph that weights edges with bpr (see cost.DefaultBPR). As with MSA
// it takes the concrete cost.BPR (not the CostFunction port) because the iterative core
// is BPR-specific: SO routes and measures its gap on bpr.MarginalCost, which only the
// concrete BPR type exposes.
func NewSystemOptimalRouter(roadGraph graph.Graph, bpr cost.BPR) *SystemOptimalRouter {
	return &SystemOptimalRouter{g: roadGraph, bpr: bpr}
}

// Compile-time assertion: *SystemOptimalRouter satisfies the Router port.
var _ Router = (*SystemOptimalRouter)(nil)

// Name identifies this strategy in benchmark output and the API.
func (router *SystemOptimalRouter) Name() string { return "systemoptimal" }

// Route answers a single request on free-flow weights. A lone request internalizes no
// externality — it shares no edge with another vehicle, so the marginal cost it imposes
// is on nobody but itself and its system-optimal path collapses to its free-flow
// shortest path. SO's marginal-cost iteration only matters for a BATCH (AssignResult),
// where requests interact; for one request it is the same shortest path naive would
// return. A cancelled context, an endpoint that snaps to no node, or an unreachable
// destination is an error; same-node From/To is a clean zero-edge, zero-cost Route.
func (router *SystemOptimalRouter) Route(ctx context.Context, req RouteRequest) (Route, error) {
	if err := ctx.Err(); err != nil {
		return Route{}, err
	}
	return routeSingleFreeFlow(router.g, req, router.Name())
}

// Assign solves the batch problem to System Optimal and returns just the routes, in
// input order — the paths-only face of AssignResult for callers that do not need final
// flows or convergence metadata.
func (router *SystemOptimalRouter) Assign(ctx context.Context, reqs []RouteRequest) ([]Route, error) {
	return AssignFromResult(ctx, reqs, router.AssignResult)
}

// AssignResult runs system-optimal assignment to convergence and reports the full
// outcome: the routes (input order) from the final iteration, the dense FinalFlows at
// System Optimal, and the convergence metadata (achieved MARGINAL gap, iteration count,
// whether it converged).
//
// Snapshot discipline (project-spec.md §R5): SO owns its load entirely — it derives
// every iteration's marginal weights from the flow it itself accumulates, takes no
// external congestion provider, and never mutates shared state mid-loop. Each iteration
// builds a fresh IMMUTABLE flow vector and a fresh marginal weightFunc over it, so every
// request in an iteration provably sees one identical, stable view. Request weights are
// taken as the final demand (no re-scaling), and a missing-segment load is free-flow
// (0), not an error (loadAt). On the first unreachable OD pair it returns a zero
// AssignResult and that error (never a partial result).
func (router *SystemOptimalRouter) AssignResult(ctx context.Context, reqs []RouteRequest) (AssignResult, error) {
	pairs, err := prefetchOD(router.g, reqs, router.Name())
	if err != nil {
		return AssignResult{}, err
	}

	step := func(ctx context.Context, k int, flow []float64) ([]float64, []Route, float64, bool, error) {
		// Weight edges with BPR.MARGINAL cost over the current (immutable) flow, then
		// AON-assign every request against those marginal weights. Routing on the marginal
		// cost is the ONLY thing distinguishing SO from MSA — each driver is charged the
		// externality it imposes, so the assignment converges to the system optimum rather
		// than the selfish equilibrium. The fan-out gives each worker its own scratch
		// buffer and flow shard and reduces them once (assignAONConcurrent).
		weight := marginalWeightFromFlows(router.bpr, flow)
		outcome, err := assignAONConcurrent(ctx, router.g, pairs, reqs, weight, router.Name())
		if err != nil {
			return nil, nil, 0, false, err
		}

		// MSA average: x <- x + (1/k)*(y - x), equivalently a convex blend
		// (1-1/k)*x + (1/k)*y. At k=1 this is exactly y. Built into a fresh vector so the
		// previous iteration's flow (closed over by `weight`) is never mutated.
		stepSize := 1.0 / float64(k)
		next := newFlowVector(router.g)
		for edgeID := domain.EdgeID(0); int(edgeID) < router.g.EdgeCount(); edgeID++ { // 0..EdgeCount-1, deterministic order
			x := loadAt(flow, edgeID)
			y := loadAt(outcome.flows, edgeID)
			next[edgeID] = x + stepSize*(y-x)
		}

		// Gap is the standard relative gap of the CURRENT iterate `flow` measured on the
		// MARGINAL network, the basis SO routes on — NOT bpr.Cost. outcome.totalSP is the
		// all-or-nothing total of `flow` under MARGINAL weights (its marginal SPTT), so the
		// TSTT it is compared against must also be marginal-weighted: totalMarginalCost,
		// Σ flow × MarginalCost. (Using totalSystemCost here would mix a bpr.Cost TSTT
		// against a marginal SPTT and stop the loop on an incoherent condition; the
		// realized bpr.Cost total time is the OBJECTIVE the SO ≤ UE invariant compares on,
		// not the gap.) At k=1 `flow` is all-zero so the gap is 1, and as the averaging
		// settles flow→SO the marginal gap → 0.
		gap := relativeGap(totalMarginalCost(router.g, router.bpr, flow), outcome.totalSP)
		// SO never sets done: like MSA it iterates until the gap criterion or the budget.
		return next, outcome.routes, gap, false, nil
	}

	return runConvergenceLoop(ctx, router.g, step)
}
