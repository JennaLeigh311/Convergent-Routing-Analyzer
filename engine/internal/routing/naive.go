package routing

import (
	"context"
	"fmt"

	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/graph"
)

// NaiveRouter is the free-flow shortest-path baseline — the honest reference the
// headline improvement number is measured against. It ignores congestion
// entirely: every request is routed on static free-flow time as if it were alone
// on the network. So Assign is correctly a loop of independent shortest paths,
// NOT a faked equilibrium; the demand-aware strategies (reactive, incremental,
// MSA, system-optimal, multipath) that actually couple requests land in Phase 3.
//
// The router holds the immutable graph and no mutable state, so its methods are
// safe for concurrent use by multiple goroutines.
type NaiveRouter struct {
	g graph.Graph
}

// NewNaiveRouter returns a NaiveRouter over the (immutable, already-loaded) graph.
func NewNaiveRouter(roadGraph graph.Graph) *NaiveRouter {
	return &NaiveRouter{g: roadGraph}
}

// Compile-time assertion: *NaiveRouter satisfies the Router port.
var _ Router = (*NaiveRouter)(nil)

// Name identifies this strategy in benchmark output and the API.
func (router *NaiveRouter) Name() string { return "naive" }

// freeFlowWeight is the naive cost of an edge: its free-flow traversal time in
// seconds, with no congestion term. This is the weight that makes "naive" naive.
func freeFlowWeight(edge graph.Edge) float64 { return edge.FreeFlowS }

// Route answers a single request: snap From/To to the nearest graph nodes, then
// run Dijkstra on free-flow time. The returned Route.CostS is the summed
// FreeFlowS of the chosen edges — the cost the path was optimized against, not a
// realized travel time (the benchmark derives realized time separately from final
// edge flows; see docs/benchmarks.md). A cancelled context, an endpoint that
// snaps to no node, or an unreachable destination is returned as an error.
//
// When From and To snap to the same node the result is a clean zero-edge,
// zero-cost Route (not an error): a path to where you already are. Downstream
// flow accumulation must therefore tolerate an empty Edges slice.
func (router *NaiveRouter) Route(ctx context.Context, req RouteRequest) (Route, error) {
	if err := ctx.Err(); err != nil {
		return Route{}, err
	}

	src, found := router.g.NearestNode(req.From)
	if !found {
		return Route{}, fmt.Errorf("naive: request %q: no graph node near origin %+v", req.ID, req.From)
	}
	dst, found := router.g.NearestNode(req.To)
	if !found {
		return Route{}, fmt.Errorf("naive: request %q: no graph node near destination %+v", req.ID, req.To)
	}

	path, cost, found := dijkstra(router.g, src, dst, freeFlowWeight)
	if !found {
		return Route{}, fmt.Errorf("naive: request %q: no path from node %d to node %d", req.ID, src, dst)
	}
	return Route{RequestID: req.ID, Edges: path, CostS: cost}, nil
}

// Assign solves a batch of requests as independent shortest paths and returns
// just the routes, in input order. It is the paths-only face of AssignResult (it
// returns AssignResult's Routes), kept for callers — the benchmark, the route CLI
// — that do not need final flows or convergence metadata.
func (router *NaiveRouter) Assign(ctx context.Context, reqs []RouteRequest) ([]Route, error) {
	return AssignFromResult(ctx, reqs, router.AssignResult)
}

// AssignResult solves a batch of requests as independent shortest paths — naive
// does no demand coupling, so each request is routed as if alone — and reports the
// full outcome: the routes (input order), the dense FinalFlows the batch places on
// the network, and single-pass convergence metadata. Naive is single-pass, so it
// reports Iters = 1, Gap = 0, Converged = true: its one assignment IS its result,
// not an iterated equilibrium (see AssignResult's field docs).
//
// It snaps every request's endpoints to graph node ids ONCE up front (prefetchOD)
// rather than re-snapping inside the per-request loop, the OD-prefetch substrate
// the iterative routers rely on — naive routes each request once so it gains
// nothing here, but it uses the shared path so the substrate has one shape. It
// checks ctx.Err() before each request so a cancelled or deadline-exceeded context
// stops the batch promptly; on the first routing error it returns a zero
// AssignResult and that error (never a partial result).
func (router *NaiveRouter) AssignResult(ctx context.Context, reqs []RouteRequest) (AssignResult, error) {
	pairs, err := prefetchOD(router.g, reqs, router.Name())
	if err != nil {
		return AssignResult{}, err
	}

	routes := make([]Route, len(reqs))
	flows := newFlowVector(router.g)
	for index, req := range reqs {
		if err := ctx.Err(); err != nil {
			return AssignResult{}, err
		}
		path, cost, found := dijkstra(router.g, pairs[index].src, pairs[index].dst, freeFlowWeight)
		if !found {
			return AssignResult{}, fmt.Errorf("naive: request %q: no path from node %d to node %d", req.ID, pairs[index].src, pairs[index].dst)
		}
		route := Route{RequestID: req.ID, Edges: path, CostS: cost}
		routes[index] = route
		addRouteFlow(flows, route, RequestWeight(req))
	}
	return AssignResult{
		Routes:     routes,
		FinalFlows: flows,
		Gap:        0,
		Iters:      1,
		Converged:  true,
	}, nil
}
