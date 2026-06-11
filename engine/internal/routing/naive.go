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
func NewNaiveRouter(g graph.Graph) *NaiveRouter {
	return &NaiveRouter{g: g}
}

// Compile-time assertion: *NaiveRouter satisfies the Router port.
var _ Router = (*NaiveRouter)(nil)

// Name identifies this strategy in benchmark output and the API.
func (r *NaiveRouter) Name() string { return "naive" }

// freeFlowWeight is the naive cost of an edge: its free-flow traversal time in
// seconds, with no congestion term. This is the weight that makes "naive" naive.
func freeFlowWeight(e graph.Edge) float64 { return e.FreeFlowS }

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
func (r *NaiveRouter) Route(ctx context.Context, req RouteRequest) (Route, error) {
	if err := ctx.Err(); err != nil {
		return Route{}, err
	}

	src, ok := r.g.NearestNode(req.From)
	if !ok {
		return Route{}, fmt.Errorf("naive: request %q: no graph node near origin %+v", req.ID, req.From)
	}
	dst, ok := r.g.NearestNode(req.To)
	if !ok {
		return Route{}, fmt.Errorf("naive: request %q: no graph node near destination %+v", req.ID, req.To)
	}

	path, cost, ok := dijkstra(r.g, src, dst, freeFlowWeight)
	if !ok {
		return Route{}, fmt.Errorf("naive: request %q: no path from node %d to node %d", req.ID, src, dst)
	}
	return Route{RequestID: req.ID, Edges: path, CostS: cost}, nil
}

// Assign solves a batch of requests as independent shortest paths — naive does no
// demand coupling, so each request is routed as if alone. It returns one Route
// per request, in input order. It checks ctx.Err() before each request so a
// cancelled or deadline-exceeded context stops the batch promptly; on the first
// routing error it returns that error and no partial slice.
//
// Naive re-snaps From/To inside each per-request Route, which is fine here because
// it routes each request exactly once. A demand-aware Assign that re-routes the
// same requests over many equilibrium iterations should instead snap every
// endpoint to its NodeID once up front and feed node ids into the inner loop,
// rather than copying this per-call snap into its hot path.
func (r *NaiveRouter) Assign(ctx context.Context, reqs []RouteRequest) ([]Route, error) {
	out := make([]Route, len(reqs))
	for i, req := range reqs {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		rt, err := r.Route(ctx, req)
		if err != nil {
			return nil, err
		}
		out[i] = rt
	}
	return out, nil
}
