package routing

import (
	"context"
	"fmt"

	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/congestion"
	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/cost"
	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/graph"
)

// ReactiveRouter is the best-response-to-stale-state strategy (algorithm catalog
// row 2, project-spec.md §4): it runs Dijkstra over edge weights computed by
// applying a BPR cost.CostFunction to a single FROZEN congestion snapshot. Every
// request sees the same immutable view of load, so each is routed onto whatever is
// cheapest GIVEN that one snapshot.
//
// This is honest best-response, and best-response to stale state HERDS and
// OSCILLATES rather than converging to a user equilibrium: because every request
// in a round reacts to the same frozen load and none sees the congestion the round
// itself creates, they all pile onto the currently-cheapest path, overload it, and
// the next snapshot then sends the herd to the next-cheapest path — a cycle that
// does NOT settle (contrast incremental / MSA, which DO converge, in Phase 3).
// Reactive is in the catalog precisely to exhibit that non-convergence, not to be
// a good assignment; do not read its output as an equilibrium.
//
// Per project-spec.md §R5 the per-Assign congestion is ONE immutable snapshot
// value: the router takes the snapshot once and weights every edge against it, so
// the result is deterministic for a fixed snapshot + OD set and there is no shared
// mutable state between requests. The router holds only the immutable graph, a
// stateless cost function, and a congestion provider, so its methods are safe for
// concurrent use by multiple goroutines (each call takes its own snapshot).
type ReactiveRouter struct {
	g        graph.Graph
	costFn   cost.CostFunction
	provider congestion.CongestionProvider
}

// NewReactiveRouter returns a ReactiveRouter over the (immutable, already-loaded)
// graph that weights edges with costFn (a BPR cost.CostFunction; see
// cost.DefaultBPR) against the load reported by provider. provider may be any
// congestion.CongestionProvider — the in-memory, static, and simulator adapters
// all plug in here — and is read via a single Snapshot() per Route / per Assign.
func NewReactiveRouter(roadGraph graph.Graph, costFn cost.CostFunction, provider congestion.CongestionProvider) *ReactiveRouter {
	return &ReactiveRouter{g: roadGraph, costFn: costFn, provider: provider}
}

// Compile-time assertion: *ReactiveRouter satisfies the Router port.
var _ Router = (*ReactiveRouter)(nil)

// Name identifies this strategy in benchmark output and the API.
func (router *ReactiveRouter) Name() string { return "reactive" }

// congestedWeight returns the weightFunc the Dijkstra core relaxes against: the
// BPR cost of each edge under its load in the given (frozen) snapshot. BPR.Cost is
// always >= edge.FreeFlowS >= 0, so the weights stay non-negative as Dijkstra's
// correctness requires. Closing over one snapshot value is what makes every request
// in a batch see the identical view (project-spec.md §R5, docs/contracts.md §3).
func (router *ReactiveRouter) congestedWeight(snapshot congestion.LoadSnapshot) weightFunc {
	return func(edge graph.Edge) float64 {
		return router.costFn.Cost(edge, snapshot.Load(edge.ID))
	}
}

// Route answers a single request: take ONE congestion snapshot, snap From/To to
// the nearest graph nodes, then run Dijkstra on the congested BPR weights derived
// from that snapshot. The returned Route.CostS is the summed CONGESTED BPR cost of
// the chosen edges under the snapshot — the cost the path was optimized against,
// not a realized travel time (the benchmark derives realized time separately from
// final edge flows; see docs/benchmarks.md). A cancelled context, an endpoint that
// snaps to no node, or an unreachable destination is returned as an error.
//
// When From and To snap to the same node the result is a clean zero-edge,
// zero-cost Route (not an error): a path to where you already are. Downstream flow
// accumulation must therefore tolerate an empty Edges slice.
func (router *ReactiveRouter) Route(ctx context.Context, req RouteRequest) (Route, error) {
	if err := ctx.Err(); err != nil {
		return Route{}, err
	}

	snapshot := router.provider.Snapshot()
	return router.routeWith(ctx, req, router.congestedWeight(snapshot))
}

// routeWith routes one request against an already-chosen weightFunc. Assign calls
// it with a single shared weightFunc (closed over one snapshot) so every request in
// the batch provably sees the identical congestion view; Route closes over its own
// fresh snapshot. The ctx.Err() check is left to the caller (Route guards before
// the snapshot; Assign guards once per loop iteration), so routeWith does no
// redundant context check of its own.
func (router *ReactiveRouter) routeWith(ctx context.Context, req RouteRequest, weight weightFunc) (Route, error) {
	src, found := router.g.NearestNode(req.From)
	if !found {
		return Route{}, fmt.Errorf("reactive: request %q: no graph node near origin %+v", req.ID, req.From)
	}
	dst, found := router.g.NearestNode(req.To)
	if !found {
		return Route{}, fmt.Errorf("reactive: request %q: no graph node near destination %+v", req.ID, req.To)
	}

	path, cost, found := dijkstra(router.g, src, dst, weight)
	if !found {
		return Route{}, fmt.Errorf("reactive: request %q: no path from node %d to node %d", req.ID, src, dst)
	}
	return Route{RequestID: req.ID, Edges: path, CostS: cost}, nil
}

// Assign routes a whole batch against ONE frozen congestion snapshot: it takes the
// snapshot ONCE up front, builds a single shared weightFunc over it, and routes
// every request against that — so every request in the batch provably sees the
// identical view and the result is deterministic for a fixed snapshot + OD set
// (project-spec.md §R5). Reactive does NOT iterate to equilibrium, so this is one
// best-response pass, not a converged assignment — it herds and oscillates across
// rounds (see the type doc). It returns one Route per request, in input order.
//
// It checks ctx.Err() before each request so a cancelled or deadline-exceeded
// context stops the batch promptly; on the first routing error it returns that
// error and no partial slice. Reactive re-snaps From/To inside each per-request
// route, which is fine here because it routes each request exactly once against the
// frozen snapshot; an iterative demand-aware Assign (incremental / MSA, Phase 3)
// that re-routes the same requests over many rounds should instead snap every
// endpoint to its NodeID once up front rather than copy this per-call snap into its
// hot path.
func (router *ReactiveRouter) Assign(ctx context.Context, reqs []RouteRequest) ([]Route, error) {
	snapshot := router.provider.Snapshot()
	weight := router.congestedWeight(snapshot)

	out := make([]Route, len(reqs))
	for index, req := range reqs {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		route, err := router.routeWith(ctx, req, weight)
		if err != nil {
			return nil, err
		}
		out[index] = route
	}
	return out, nil
}
