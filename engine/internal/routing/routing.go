package routing

import (
	"context"

	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/domain"
)

// RouteRequest is a single origin→destination navigation request.
type RouteRequest struct {
	ID   string
	From domain.LatLon
	To   domain.LatLon

	// DepartAt is the simulated departure time in seconds from the start of a
	// run. It is consumed by the mesoscopic simulator; a zero value means the
	// request departs at t=0 (the static-assignment case treats all requests as
	// simultaneous).
	DepartAt float64

	// Weight is how many vehicles/hour of demand this request represents on each
	// edge of its chosen path — the per-request contribution AssignResult.FinalFlows
	// accumulates. A zero (unset) Weight is treated as 1.0 by RequestWeight, so a
	// batch built without weights is the conventional "one vehicle per request"
	// flow; a negative Weight is also floored to 1.0 (a request can never subtract
	// flow). Set it explicitly to model a request standing in for many vehicles.
	Weight float64
}

// RequestWeight is the effective flow contribution of a request: its Weight when
// set to a positive value, else the 1.0 default. Centralized here so naive,
// reactive, and every Phase-3 iterative router accumulate FinalFlows the same
// way and the zero-value/negative handling lives in one place. It is EXPORTED so
// the §R5 mesoscopic simulator (benchmark.Simulate) derives a vehicle's per-edge
// load contribution the same way the routers accumulate FinalFlows, rather than
// keeping a byte-copy of this rule.
func RequestWeight(req RouteRequest) float64 {
	if req.Weight > 0 {
		return req.Weight
	}
	return 1.0
}

// Route is a computed path for one request.
type Route struct {
	RequestID string

	// Edges is the ordered list of directed edges from origin to destination.
	Edges []domain.EdgeID

	// CostS is the routing cost in seconds the algorithm optimized against — the
	// weights it chose the path under. This is NOT the realized travel time; the
	// benchmark computes that separately by applying the cost function to the
	// final edge flows (see docs/benchmarks.md).
	CostS float64
}

// AssignResult is the full outcome of a batch assignment: the chosen routes plus
// the convergence metadata and final edge flows the iterative strategies and the
// Phase-4 benchmark need. It is the shared return shape settled once (issue #71)
// so that every Phase-3 iterative router (incremental, msa, systemoptimal,
// multipath) reports its result the same way and no caller has to be re-touched
// when the iterative routers land.
//
// Why a value, not just []Route: project-spec.md §R5 requires the iterative
// strategies to report the achieved convergence gap, and the benchmark needs the
// final per-edge flows to compute realized travel time and the Price of Anarchy.
// Neither has a home on a bare []Route. AssignResult gives the convergence gap,
// the iteration count, and the final flow vector one canonical home that the
// visualization engineer and the benchmark both read.
type AssignResult struct {
	// Routes is the chosen path per request, in input order — exactly what the
	// legacy Assign([]Route) returned, so a caller that only wants paths reads
	// Routes (or keeps calling Assign, which returns this field directly).
	Routes []Route

	// FinalFlows is the dense per-edge flow the assignment places on the network,
	// in vehicles/hour, indexed directly by EdgeID with length graph.EdgeCount().
	// FinalFlows[e] is the sum over every route of (1 if the route traverses edge e
	// else 0) × that request's weight. Single-pass routers (naive, reactive) build
	// it from their one assignment; the iterative routers report the flow at their
	// final iteration. The benchmark applies the BPR cost to this vector to derive
	// realized travel time (docs/benchmarks.md) — the realized ground truth, NOT
	// the routing cost the path was optimized against (Route.CostS).
	//
	// It is sized to EdgeCount() so every valid EdgeID indexes it without a bounds
	// check on the read side; an empty batch still yields a full-length all-zero
	// vector (not nil), so a consumer can always index it.
	FinalFlows []float64

	// Gap is the achieved relative convergence gap at the reported iteration
	// (project-spec.md §R5). Single-pass routers (naive, reactive) do not iterate
	// to equilibrium and report Gap = 0: their one pass is their result, not a
	// converged one — read Converged/Iters to tell a genuine single-pass result
	// from an iterative router that happened to converge in one step.
	Gap float64

	// Iters is the number of assignment iterations performed. Single-pass routers
	// report Iters = 1 (one assignment pass).
	Iters int

	// Converged reports whether the strategy reached its convergence criterion
	// within its iteration budget (project-spec.md §5: relative gap < 1e-4 or 100
	// iterations for the iterative strategies). Single-pass routers report
	// Converged = true: a one-shot assignment is trivially "done" — there is no
	// further iteration that could improve it — even though that is NOT a user
	// equilibrium (reactive in particular herds; see docs/algorithms.md §3.1).
	Converged bool
}

// Router is a routing strategy. Route answers a single request; AssignResult
// solves the convergent (batch) problem for many simultaneous requests and
// returns the full outcome (routes + convergence metadata + final flows). Assign
// is the paths-only convenience over AssignResult. Strategies differ only in the
// weights they feed the shared shortest-path core and how they iterate — see
// docs/algorithms.md for the six implementations.
//
// AssignResult is first-class (not a loop over Route) because demand-aware
// routing is fundamentally a batch problem: the assignment of one request affects
// the cost seen by the others.
//
// All three methods take a context.Context: AssignResult runs an iterative
// equilibrium (MSA / system-optimal) with unbounded wall-clock and is served
// behind an HTTP handler, so callers need cancellation/deadlines. Implementations
// of AssignResult should check ctx.Err() between iterations.
//
// Implementor note: implement AssignResult (the real work) and let Assign delegate
// to it — AssignFromResult does exactly that — so the convergence metadata is
// produced in one place and the two methods can never disagree.
type Router interface {
	Route(ctx context.Context, req RouteRequest) (Route, error)

	// Assign solves the batch problem and returns just the routes, in input
	// order. It is the backward-compatible, paths-only face of AssignResult for
	// callers (the benchmark, the route CLI) that do not need flows or
	// convergence metadata.
	Assign(ctx context.Context, reqs []RouteRequest) ([]Route, error)

	// AssignResult solves the batch problem and returns the full outcome: routes,
	// final per-edge flows, and convergence metadata. This is the shape the
	// iterative strategies (project-spec.md §R5) and the Phase-4 benchmark consume.
	AssignResult(ctx context.Context, reqs []RouteRequest) (AssignResult, error)

	Name() string
}

// AssignFromResult adapts an AssignResult-producing function to the paths-only
// Assign contract: it runs assign and returns its Routes, propagating the error
// unchanged (including the (nil, err) on-first-error contract — a failed
// AssignResult returns a zero AssignResult whose Routes is nil). Routers
// implement AssignResult as their single source of truth and define Assign as a
// one-line call to this helper, so Assign and AssignResult can never report
// different routes.
func AssignFromResult(
	ctx context.Context,
	reqs []RouteRequest,
	assign func(context.Context, []RouteRequest) (AssignResult, error),
) ([]Route, error) {
	result, err := assign(ctx, reqs)
	if err != nil {
		return nil, err
	}
	return result.Routes, nil
}
