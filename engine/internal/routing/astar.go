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
	// maxSpeedMS is the heuristic divisor in meters/second, derived once at
	// construction as the maximum per-edge straight-line (endpoint-chord) speed over
	// the network (see maxFreeFlowSpeedMS). Dividing great-circle distance by it
	// yields a LOWER bound on remaining travel time — h = distance / maxSpeedMS ≤ the
	// true remaining free-flow time — so the heuristic is admissible. The divisor is
	// taken from the same endpoint geometry the heuristic measures (chord/FreeFlowS)
	// rather than LengthM/FreeFlowS, so the bound holds even on an edge whose declared
	// length_m is shorter than its endpoint chord; a smaller divisor would lose
	// pruning (still admissible), a larger one could make h inadmissible.
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

// maxFreeFlowSpeedMS returns the divisor that scales the A* heuristic: the maximum
// over all edges of the edge's STRAIGHT-LINE (great-circle endpoint chord) speed,
// chord(From, To) / FreeFlowS, in meters/second. It is NOT the obvious
// LengthM / FreeFlowS edge speed, deliberately:
//
//   - graph.Edge does not carry maxspeed_kmh (the loader validates it but only
//     LengthM/FreeFlowS/CapacityVPH reach the engine — see graph.Edge), so the
//     declared max-speed field item-3 prefers is not available here. The most
//     robust admissible divisor we CAN derive from the edge fields is this
//     chord/time speed.
//   - the LengthM / FreeFlowS form is corrupted when length_m < the endpoint chord
//     (a real geometry quirk: a hand-written or simplified length shorter than the
//     straight-line distance between the endpoints — see the toy fixture). A single
//     such anomalous edge would NOT change LengthM/FreeFlowS into a safe divisor,
//     and the heuristic chord/maxSpeed could then EXCEED that edge's own true time
//     FreeFlowS, making h inadmissible and A* return a non-optimal path (cost 10.0
//     vs 0.02 in the reviewer's reproduction).
//
// Why chord/FreeFlowS is admissible regardless of any length_m anomaly: for any
// path P from a node to dst, the triangle inequality gives chord(node, dst) ≤
// Σ chord(edge_i endpoints). With the divisor D = max_i chord_i / FreeFlowS_i we
// have chord_i / D ≤ FreeFlowS_i for EVERY edge, so chord(node,dst)/D ≤
// Σ chord_i / D ≤ Σ FreeFlowS_i = time(P). Thus h = chord/D never exceeds the true
// remaining free-flow time along any real path — admissible by construction, even
// on a graph with an edge whose length_m is shorter than its chord, because D is
// derived from the same endpoint geometry the heuristic measures, never from
// length_m. Returns 0 for an empty graph or one with no usable edge, which yields a
// zero (trivially admissible) heuristic and degrades A* to plain Dijkstra.
func maxFreeFlowSpeedMS(roadGraph graph.Graph) float64 {
	maxSpeed := 0.0
	for edgeID := domain.EdgeID(0); int(edgeID) < roadGraph.EdgeCount(); edgeID++ {
		edge, ok := roadGraph.Edge(edgeID)
		if !ok || edge.FreeFlowS <= 0 {
			continue
		}
		fromNode, fromOK := roadGraph.Node(edge.From)
		toNode, toOK := roadGraph.Node(edge.To)
		if !fromOK || !toOK {
			continue
		}
		chord := graph.GreatCircleM(fromNode.Pos, toNode.Pos)
		if chord <= 0 {
			continue
		}
		if speed := chord / edge.FreeFlowS; speed > maxSpeed {
			maxSpeed = speed
		}
	}
	return maxSpeed
}

// heuristic builds the admissible time-domain A* heuristic for a search toward dst:
// h(node) = great-circle(node, dst) / maxSpeedMS, a LOWER bound on the remaining
// free-flow travel time in seconds. It reuses graph.GreatCircleM (the shared
// haversine) for the straight-line distance and the precomputed max-chord-speed
// divisor for the time conversion.
//
// Admissibility (h ≤ true remaining time) follows from the divisor: because
// maxSpeedMS = max_i chord_i / FreeFlowS_i over edges, each edge satisfies
// chord_i / maxSpeedMS ≤ FreeFlowS_i, so by the triangle inequality the
// node→dst chord divided by maxSpeedMS never exceeds the summed FreeFlowS of any
// real path (see maxFreeFlowSpeedMS). This holds without assuming length_m ≥ chord,
// since the divisor is built from endpoint geometry, not length_m.
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
// heuristic. Because the heuristic is admissible (it never overestimates the
// remaining free-flow time — see maxFreeFlowSpeedMS), A* settles the same
// destination cost Dijkstra would, so the returned Route.CostS is the summed
// FreeFlowS of the chosen edges, identical to the naive baseline's, just reached
// with fewer node settlements. It is the cost the
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

	// A* passes its admissible heuristic and a nil scratch buffer: the single-call
	// Route path keeps the allocation-per-call dist/prevEdge shape (the iterative
	// routers, not A*, supply a per-worker scratch).
	path, cost, found := shortestPath(router.g, src, dst, freeFlowWeight, router.heuristic(dst), nil)
	if !found {
		return Route{}, fmt.Errorf("astar: request %q: no path from node %d to node %d", req.ID, src, dst)
	}
	return Route{RequestID: req.ID, Edges: path, CostS: cost}, nil
}

// Assign solves a batch of requests as independent single-pair A* searches and
// returns just the routes, in input order. It is the paths-only face of
// AssignResult (it returns AssignResult's Routes), kept for callers — the
// benchmark, the route CLI — that do not need final flows or convergence metadata.
func (router *AStarRouter) Assign(ctx context.Context, reqs []RouteRequest) ([]Route, error) {
	return AssignFromResult(ctx, reqs, router.AssignResult)
}

// AssignResult routes a batch of requests as independent single-pair A* searches —
// A* is a query accelerator, not a demand-aware assignment, so each request is
// routed as if alone on the free-flow network (exactly the naive baseline's
// semantics, just faster per query) — and reports the full outcome: the routes
// (input order), the dense FinalFlows the batch places on the network, and
// single-pass convergence metadata. A* is single-pass like naive, so it reports
// Iters = 1, Gap = 0, Converged = true: its one assignment IS its result, not an
// iterated equilibrium (see AssignResult's field docs).
//
// It snaps every request's endpoints to graph node ids ONCE up front (prefetchOD)
// rather than re-snapping inside the per-request loop — the shared OD-prefetch
// substrate — then runs one A* per pair, accumulating each chosen path into
// FinalFlows via addRouteFlow weighted by requestWeight(req). It checks ctx.Err()
// before each request so a cancelled or deadline-exceeded context stops the batch
// promptly; on the first routing error it returns a zero AssignResult and that
// error (never a partial result).
func (router *AStarRouter) AssignResult(ctx context.Context, reqs []RouteRequest) (AssignResult, error) {
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
		path, cost, found := shortestPath(router.g, pairs[index].src, pairs[index].dst, freeFlowWeight, router.heuristic(pairs[index].dst), nil)
		if !found {
			return AssignResult{}, fmt.Errorf("astar: request %q: no path from node %d to node %d", req.ID, pairs[index].src, pairs[index].dst)
		}
		route := Route{RequestID: req.ID, Edges: path, CostS: cost}
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
