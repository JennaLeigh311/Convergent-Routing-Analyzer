package routing

import (
	"context"
	"fmt"

	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/congestion"
	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/cost"
	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/domain"
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
// concurrent use by multiple goroutines: Assign takes its own owning Snapshot, and
// Route borrows a read-only View (CongestionProvider.View) that it reads within one
// synchronous call. That borrow is safe only against a provider not being mutated
// concurrently — the providers wired here are built once and then frozen — so the
// read-only borrow, not a per-call copy, is what Route relies on (see Route).
type ReactiveRouter struct {
	g        graph.Graph
	costFn   cost.CostFunction
	provider congestion.CongestionProvider
}

// NewReactiveRouter returns a ReactiveRouter over the (immutable, already-loaded)
// graph that weights edges with costFn (a BPR cost.CostFunction; see
// cost.DefaultBPR) against the load reported by provider. provider may be any
// congestion.CongestionProvider — the in-memory, static, and simulator adapters
// all plug in here — and is read via a single View() borrow per Route and a single
// owning Snapshot() per Assign.
//
// This provider seam is reactive-specific: reactive reads externally-supplied load
// once and best-responds to it. The Phase-3 demand-aware strategies (incremental,
// MSA) instead own their per-round snapshot lifecycle, deriving load from the flows
// they themselves accumulate, so they should reuse the congestedWeight seam (which
// already takes a LoadSnapshot) rather than copy this (graph, costFn, provider)
// constructor shape wholesale.
func NewReactiveRouter(roadGraph graph.Graph, costFn cost.CostFunction, provider congestion.CongestionProvider) *ReactiveRouter {
	return &ReactiveRouter{g: roadGraph, costFn: costFn, provider: provider}
}

// Compile-time assertion: *ReactiveRouter satisfies the Router port.
var _ Router = (*ReactiveRouter)(nil)

// Name identifies this strategy in benchmark output and the API.
func (router *ReactiveRouter) Name() string { return "reactive" }

// congestedWeight returns the weightFunc the Dijkstra core relaxes against: the
// BPR cost of each edge under its load in the given load view. BPR.Cost is always
// >= edge.FreeFlowS >= 0, so the weights stay non-negative as Dijkstra's
// correctness requires. It takes the read-only congestion.LoadView (which only
// exposes Load) rather than an owning LoadSnapshot, since it only ever reads load
// — that lets Route pass an allocation-free borrow while Assign passes its owning
// per-batch Snapshot (a LoadSnapshot satisfies LoadView). Closing over one view is
// what makes every request in a batch see the identical load (project-spec.md §R5,
// docs/contracts.md §3); for a batch that one view is the frozen owning Snapshot.
func (router *ReactiveRouter) congestedWeight(view congestion.LoadView) weightFunc {
	return func(edge graph.Edge) float64 {
		return router.costFn.Cost(edge, view.Load(edge.ID))
	}
}

// Route answers a single request: BORROW a read-only view of congestion, snap
// From/To to the nearest graph nodes, then run Dijkstra on the congested BPR
// weights derived from that view. The returned Route.CostS is the summed
// CONGESTED BPR cost of the chosen edges under the view — the cost the path was
// optimized against, not a realized travel time (the benchmark derives realized
// time separately from final edge flows; see docs/benchmarks.md). A cancelled
// context, an endpoint that snaps to no node, or an unreachable destination is
// returned as an error.
//
// Route uses provider.View() (an allocation-free borrow over the live load), NOT
// Snapshot(): the congested-weight closure only READS load, so copying the whole
// dense load vector per single request — ~8–24MB at city scale, the dominant
// transient allocation under heavy single-request concurrency — is pure waste.
// The borrow is valid because Route is single-shot best-response-to-stale-state:
// it holds the view only for this one synchronous call and never across a provider
// mutation (per the CongestionProvider.View contract). Assign, by contrast, MUST
// keep its owning per-batch Snapshot so every request in a round provably sees one
// stable view — see Assign.
//
// When From and To snap to the same node the result is a clean zero-edge,
// zero-cost Route (not an error): a path to where you already are. Downstream flow
// accumulation must therefore tolerate an empty Edges slice.
func (router *ReactiveRouter) Route(ctx context.Context, req RouteRequest) (Route, error) {
	if err := ctx.Err(); err != nil {
		return Route{}, err
	}

	view := router.provider.View()
	return router.routeWith(ctx, req, router.congestedWeight(view))
}

// routeWith routes one request against an already-chosen weightFunc. Route calls
// it after snapping its single request and closing over its own borrowed View;
// AssignResult instead pre-snaps every endpoint once (prefetchOD) and routes node
// ids through routeNodes, so this per-request snap is on the single-shot Route path
// only. The ctx.Err() check is left to the caller (Route guards before taking the
// view), so routeWith does no redundant context check of its own.
func (router *ReactiveRouter) routeWith(ctx context.Context, req RouteRequest, weight weightFunc) (Route, error) {
	src, found := router.g.NearestNode(req.From)
	if !found {
		return Route{}, fmt.Errorf("reactive: request %q: no graph node near origin %+v", req.ID, req.From)
	}
	dst, found := router.g.NearestNode(req.To)
	if !found {
		return Route{}, fmt.Errorf("reactive: request %q: no graph node near destination %+v", req.ID, req.To)
	}
	return router.routeNodes(req, src, dst, weight)
}

// routeNodes routes one request whose endpoints are ALREADY resolved to graph node
// ids, against an already-chosen weightFunc. It is the per-request core both Route
// (via routeWith, after snapping) and AssignResult (after a single up-front
// prefetchOD) funnel through, so the snap-to-node step happens exactly once per
// request in either path — never re-snapped inside an iteration loop.
func (router *ReactiveRouter) routeNodes(req RouteRequest, src, dst domain.NodeID, weight weightFunc) (Route, error) {
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
// error and no partial slice. It returns just the routes — the paths-only face of
// AssignResult — for callers that do not need final flows or convergence metadata.
func (router *ReactiveRouter) Assign(ctx context.Context, reqs []RouteRequest) ([]Route, error) {
	return AssignFromResult(ctx, reqs, router.AssignResult)
}

// AssignResult routes a whole batch against ONE frozen congestion snapshot and
// reports the full outcome: the routes (input order), the dense FinalFlows the
// batch places on the network, and single-pass convergence metadata. As with
// Assign it takes the snapshot ONCE up front and routes every request against one
// shared weightFunc, so every request provably sees the identical view and the
// result is deterministic for a fixed snapshot + OD set (project-spec.md §R5).
//
// Reactive is single-pass best-response, NOT an iterated equilibrium, so it reports
// Iters = 1, Gap = 0, Converged = true — but it HERDS and oscillates across rounds
// (see the type doc); FinalFlows is the flow of this one best-response pass, not a
// user equilibrium. It snaps every endpoint to its node id ONCE up front
// (prefetchOD) instead of re-snapping per request — the shared substrate the
// iterative routers use. It checks ctx.Err() before each request; on the first
// routing error it returns a zero AssignResult and that error (never a partial
// result).
func (router *ReactiveRouter) AssignResult(ctx context.Context, reqs []RouteRequest) (AssignResult, error) {
	pairs, err := prefetchOD(router.g, reqs, router.Name())
	if err != nil {
		return AssignResult{}, err
	}

	snapshot := router.provider.Snapshot()
	weight := router.congestedWeight(snapshot)

	routes := make([]Route, len(reqs))
	flows := newFlowVector(router.g)
	for index, req := range reqs {
		if err := ctx.Err(); err != nil {
			return AssignResult{}, err
		}
		route, err := router.routeNodes(req, pairs[index].src, pairs[index].dst, weight)
		if err != nil {
			return AssignResult{}, err
		}
		routes[index] = route
		addRouteFlow(flows, route, requestWeight(req))
	}
	return AssignResult{
		Routes:     routes,
		FinalFlows: flows,
		Gap:        0,
		Iters:      1,
		Converged:  true,
	}, nil
}
