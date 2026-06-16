package routing

import (
	"context"
	"fmt"

	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/domain"
	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/graph"
)

// AStarRouter is the A* single-pair query path over free-flow weights. It returns
// the SAME optimal path as the Dijkstra-based naive baseline (it optimizes the
// identical freeFlowWeight), but settles fewer nodes by ordering the frontier with
// an admissible heuristic that biases expansion toward the destination
// (project-spec.md §R5: "add A* with an admissible haversine heuristic for
// single-pair queries").
//
// A* is a single-pair query accelerator, not a demand-aware assignment strategy:
// its heuristic estimates remaining FREE-FLOW time, so its Assign is just a loop of
// independent single-pair searches (like naive), NOT a converged equilibrium. The
// demand-aware strategies (reactive, incremental, MSA, …) live elsewhere; A* exists
// to de-risk single-pair latency for the API/CLI route path.
//
// The router holds only the immutable graph and the precomputed network max
// free-flow speed (a scalar derived once at construction), so it has no mutable
// state and its methods are safe for concurrent use by multiple goroutines.
type AStarRouter struct {
	g graph.Graph
	// maxSpeedMS is the network's maximum edge free-flow speed in meters/second,
	// derived once at construction. It is the divisor in the heuristic: dividing
	// great-circle distance by the FASTEST speed on the network yields a strict
	// LOWER bound on remaining travel time (no edge can be traversed faster), which
	// is exactly the admissibility A* needs. A larger divisor would make h
	// inadmissible and could return a non-optimal path.
	maxSpeedMS float64
}

// NewAStarRouter returns an AStarRouter over the (immutable, already-loaded) graph.
// It scans the edges once to derive the network max free-flow speed that scales the
// heuristic (see maxFreeFlowSpeedMS). The graph is expected to be non-empty with
// positive-length, positive-free-flow-time edges (the loader guarantees this for
// any §2-conformant export); on a degenerate graph with no usable speed the
// heuristic falls back to 0, which is trivially admissible and degrades A* to plain
// Dijkstra rather than risking an inadmissible bound.
func NewAStarRouter(roadGraph graph.Graph) *AStarRouter {
	return &AStarRouter{g: roadGraph, maxSpeedMS: maxFreeFlowSpeedMS(roadGraph)}
}

// Compile-time assertion: *AStarRouter satisfies the Router port.
var _ Router = (*AStarRouter)(nil)

// Name identifies this strategy in benchmark output and the API.
func (router *AStarRouter) Name() string { return "astar" }

// maxFreeFlowSpeedMS returns the maximum per-edge free-flow speed over the graph in
// meters/second, computed as max(LengthM / FreeFlowS) across all edges. Both fields
// are positive on a §2-conformant edge (the export derives freeflow_time_s =
// length_m / (maxspeed_kmh → m/s), so LengthM/FreeFlowS recovers that m/s speed), so
// this recovers the network's single fastest free-flow speed.
//
// Dividing great-circle distance by THIS maximum is what makes the heuristic
// admissible: since every edge's actual free-flow speed is ≤ the maximum, the
// straight-line time h = distance / maxSpeed can never exceed the true remaining
// free-flow travel time along any real path. Returns 0 for an empty graph or one
// with no positive-speed edge, which yields a zero (trivially admissible) heuristic.
func maxFreeFlowSpeedMS(roadGraph graph.Graph) float64 {
	maxSpeed := 0.0
	for edgeID := domain.EdgeID(0); int(edgeID) < roadGraph.EdgeCount(); edgeID++ {
		edge, ok := roadGraph.Edge(edgeID)
		if !ok || edge.FreeFlowS <= 0 || edge.LengthM <= 0 {
			continue
		}
		if speed := edge.LengthM / edge.FreeFlowS; speed > maxSpeed {
			maxSpeed = speed
		}
	}
	return maxSpeed
}

// heuristic builds the admissible time-domain A* heuristic for a search toward dst:
// h(node) = great-circle(node, dst) / maxSpeedMS, a strict LOWER bound on the
// remaining free-flow travel time in seconds. It reuses graph.GreatCircleM (the
// shared haversine) for the straight-line distance and the precomputed network max
// free-flow speed for the time conversion.
//
// When maxSpeedMS is 0 (degenerate graph) or the destination node is unknown, it
// returns the constant-0 heuristic, which is trivially admissible (h ≤ true cost
// for all nodes) and makes the search fall back to plain Dijkstra rather than risk
// an inadmissible — and so potentially non-optimal — bound.
func (router *AStarRouter) heuristic(dst domain.NodeID) heuristicFunc {
	dstNode, ok := router.g.Node(dst)
	if !ok || router.maxSpeedMS <= 0 {
		return func(domain.NodeID) float64 { return 0 }
	}
	return func(node domain.NodeID) float64 {
		n, ok := router.g.Node(node)
		if !ok {
			return 0 // unknown node: admissible underestimate
		}
		return graph.GreatCircleM(n.Pos, dstNode.Pos) / router.maxSpeedMS
	}
}

// Route answers a single request: snap From/To to the nearest graph nodes, then run
// the A* search over free-flow time with the admissible haversine/max-speed
// heuristic. Because the heuristic is admissible (and in fact consistent — see the
// package note), A* settles the same destination cost Dijkstra would, so the
// returned Route.CostS is the summed FreeFlowS of the chosen edges, identical to the
// naive baseline's, just reached with fewer node settlements. It is the cost the
// path was optimized against, not a realized travel time (the benchmark derives
// realized time separately from final edge flows; see docs/benchmarks.md). A
// cancelled context, an endpoint that snaps to no node, or an unreachable
// destination is returned as an error.
//
// When From and To snap to the same node the result is a clean zero-edge, zero-cost
// Route (not an error): a path to where you already are. Downstream flow
// accumulation must therefore tolerate an empty Edges slice.
func (router *AStarRouter) Route(ctx context.Context, req RouteRequest) (Route, error) {
	if err := ctx.Err(); err != nil {
		return Route{}, err
	}

	src, found := router.g.NearestNode(req.From)
	if !found {
		return Route{}, fmt.Errorf("astar: request %q: no graph node near origin %+v", req.ID, req.From)
	}
	dst, found := router.g.NearestNode(req.To)
	if !found {
		return Route{}, fmt.Errorf("astar: request %q: no graph node near destination %+v", req.ID, req.To)
	}

	path, cost, found := shortestPath(router.g, src, dst, freeFlowWeight, router.heuristic(dst))
	if !found {
		return Route{}, fmt.Errorf("astar: request %q: no path from node %d to node %d", req.ID, src, dst)
	}
	return Route{RequestID: req.ID, Edges: path, CostS: cost}, nil
}

// Assign routes a batch of requests as independent single-pair A* searches — A* is
// a query accelerator, not a demand-aware assignment, so each request is routed as
// if alone on the free-flow network (exactly the naive baseline's semantics, just
// faster per query). It returns one Route per request in input order, checking
// ctx.Err() before each so a cancelled or deadline-exceeded context stops the batch
// promptly; on the first routing error it returns that error and no partial slice.
func (router *AStarRouter) Assign(ctx context.Context, reqs []RouteRequest) ([]Route, error) {
	out := make([]Route, len(reqs))
	for index, req := range reqs {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		route, err := router.Route(ctx, req)
		if err != nil {
			return nil, err
		}
		out[index] = route
	}
	return out, nil
}
