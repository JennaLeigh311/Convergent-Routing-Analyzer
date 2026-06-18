package routing

import (
	"context"

	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/cost"
	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/domain"
	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/graph"
)

// MSARouter is the Method of Successive Averages strategy (algorithm catalog row 4,
// project-spec.md §4; docs/algorithms.md §2, §5): it iterates an all-or-nothing (AON)
// assignment and averages it into the running flow with a diminishing 1/k step, and
// CONVERGES to User Equilibrium — the Nash state where no single driver can lower
// their own travel time by switching paths.
//
// Each iteration k (1-based):
//
//	weight every edge with BPR over the CURRENT flow x  (weightFromFlows)
//	AON-assign every request on its current-cost shortest path  -> auxiliary flow y
//	x <- x + (1/k)*(y - x)                                       (the MSA average)
//	gap <- (TSTT(x) - SPTT) / SPTT
//
// At k=1 the running flow is all-zero, so the first weights are free-flow and x
// becomes exactly y (step 1/1 = 1). As k grows the 1/k step shrinks, damping the
// herding/oscillation that makes reactive (best-response to stale state) never settle
// — which is precisely why MSA converges to UE where reactive does not
// (docs/algorithms.md §3.1). The loop stops at relative gap < 1e-4 or 100 iterations.
//
// The router holds only the immutable graph and a stateless BPR cost function (no
// congestion provider — MSA derives load from the flows it accumulates, not from an
// external feed), so its methods are safe for concurrent use; each Assign owns its
// per-iteration flow vectors and fans requests across goroutines with per-worker
// scratch (see iterative.go).
type MSARouter struct {
	g   graph.Graph
	bpr cost.BPR
}

// NewMSARouter returns an MSARouter over the (immutable, already-loaded) graph that
// weights edges with bpr (a BPR cost function; see cost.DefaultBPR). It takes the
// concrete cost.BPR (not the CostFunction port) because the iterative core is BPR-
// specific: the relative-gap metric needs BPR.Cost on the realized flow, and the
// sibling systemoptimal router (#76) reuses this same core with BPR.MarginalCost.
func NewMSARouter(roadGraph graph.Graph, bpr cost.BPR) *MSARouter {
	return &MSARouter{g: roadGraph, bpr: bpr}
}

// Compile-time assertion: *MSARouter satisfies the Router port.
var _ Router = (*MSARouter)(nil)

// Name identifies this strategy in benchmark output and the API.
func (router *MSARouter) Name() string { return "msa" }

// Route answers a single request on free-flow weights. A lone request has no demand
// to equilibrate against — there is no congestion it shares with anyone — so its
// user-equilibrium path is just its free-flow shortest path. MSA's iteration only
// matters for a BATCH (AssignResult), where requests interact; for one request it
// collapses to the same shortest path naive would return. A cancelled context, an
// endpoint that snaps to no node, or an unreachable destination is an error; same-node
// From/To is a clean zero-edge, zero-cost Route.
func (router *MSARouter) Route(ctx context.Context, req RouteRequest) (Route, error) {
	if err := ctx.Err(); err != nil {
		return Route{}, err
	}
	return routeSingleFreeFlow(router.g, req, router.Name())
}

// Assign solves the batch problem to User Equilibrium and returns just the routes, in
// input order — the paths-only face of AssignResult for callers that do not need
// final flows or convergence metadata.
func (router *MSARouter) Assign(ctx context.Context, reqs []RouteRequest) ([]Route, error) {
	return AssignFromResult(ctx, reqs, router.AssignResult)
}

// AssignResult runs MSA to convergence and reports the full outcome: the routes
// (input order) from the final iteration, the dense FinalFlows at User Equilibrium,
// and the convergence metadata (achieved gap, iteration count, whether it converged).
//
// Snapshot discipline (project-spec.md §R5): MSA owns its load entirely — it derives
// every iteration's weights from the flow it itself accumulates, takes no external
// congestion provider, and never mutates shared state mid-loop. Each iteration builds
// a fresh IMMUTABLE flow vector and a fresh weightFunc over it, so every request in an
// iteration provably sees one identical, stable view. Request weights are taken as the
// final demand (no re-scaling), and a missing-segment load is free-flow (0), not an
// error (loadAt). On the first unreachable OD pair it returns a zero AssignResult and
// that error (never a partial result).
func (router *MSARouter) AssignResult(ctx context.Context, reqs []RouteRequest) (AssignResult, error) {
	pairs, err := prefetchOD(router.g, reqs, router.Name())
	if err != nil {
		return AssignResult{}, err
	}

	step := func(ctx context.Context, k int, flow []float64) ([]float64, []Route, float64, bool, error) {
		// Weight edges with BPR over the current (immutable) flow, then AON-assign
		// every request against those weights. The fan-out gives each worker its own
		// scratch buffer and flow shard and reduces them once (assignAONConcurrent).
		weight := weightFromFlows(router.bpr, flow)
		outcome, err := assignAONConcurrent(ctx, router.g, pairs, reqs, weight, router.Name())
		if err != nil {
			return nil, nil, 0, false, err
		}

		// MSA average: x <- x + (1/k)*(y - x), equivalently a convex blend
		// (1-1/k)*x + (1/k)*y. At k=1 this is exactly y. Built into a fresh vector so
		// the previous iteration's flow (closed over by `weight`) is never mutated.
		stepSize := 1.0 / float64(k)
		next := newFlowVector(router.g)
		for edgeID := domain.EdgeID(0); int(edgeID) < router.g.EdgeCount(); edgeID++ { // 0..EdgeCount-1, deterministic order
			x := loadAt(flow, edgeID)
			y := loadAt(outcome.flows, edgeID)
			next[edgeID] = x + stepSize*(y-x)
		}

		// Gap is the standard relative gap of the CURRENT iterate `flow` (the point this
		// AON was computed for), measured consistently under one weight vector t(flow):
		// TSTT(flow) against outcome.totalSP, which is the all-or-nothing total of `flow`
		// under those same weights (its SPTT). Measuring TSTT on the AVERAGED `next` while
		// taking SPTT from `flow` would mix two flow/weight vectors and report a non-
		// standard gap; this reports the textbook UE convergence gap of `flow`. At k=1
		// `flow` is all-zero so the gap is 1 (the zero flow is not at equilibrium), and as
		// the averaging settles flow→UE the gap → 0.
		gap := relativeGap(totalSystemCost(router.g, router.bpr, flow), outcome.totalSP)
		// MSA never sets done: it iterates until the gap criterion or the budget.
		return next, outcome.routes, gap, false, nil
	}

	return runConvergenceLoop(ctx, router.g, step)
}
