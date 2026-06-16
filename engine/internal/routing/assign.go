package routing

import (
	"fmt"

	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/domain"
	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/graph"
)

// odPair is one request's origin/destination resolved to graph node ids, snapped
// ONCE up front by prefetchOD. The iterative routers route over these node ids
// directly instead of re-running NearestNode (a k-d tree query) inside every
// per-request, per-iteration loop body (the warnings at naive.go and reactive.go).
type odPair struct {
	src domain.NodeID
	dst domain.NodeID
}

// prefetchOD resolves every request's From/To lat-lon endpoints to graph node ids
// ONCE, returning them in input order. It is the fix for the per-request snap that
// naive and reactive each warned about: an iterative Assign re-routes the same
// requests over many equilibrium rounds, so snapping the endpoints inside the
// per-request loop would re-run a k-d tree NearestNode query on every request on
// every round. prefetchOD lifts that out — snap once, then feed node ids into the
// inner loop.
//
// strategy is the router name used to prefix an endpoint-resolution error so the
// message reads the same as the per-request errors the routers used to emit (e.g.
// `naive: request "x": no graph node near origin ...`). On the first request whose
// origin or destination snaps to no node it returns (nil, error) — never a partial
// slice — matching the on-first-error contract Assign already documents.
func prefetchOD(roadGraph graph.Graph, reqs []RouteRequest, strategy string) ([]odPair, error) {
	pairs := make([]odPair, len(reqs))
	for index, req := range reqs {
		src, found := roadGraph.NearestNode(req.From)
		if !found {
			return nil, fmt.Errorf("%s: request %q: no graph node near origin %+v", strategy, req.ID, req.From)
		}
		dst, found := roadGraph.NearestNode(req.To)
		if !found {
			return nil, fmt.Errorf("%s: request %q: no graph node near destination %+v", strategy, req.ID, req.To)
		}
		pairs[index] = odPair{src: src, dst: dst}
	}
	return pairs, nil
}

// newFlowVector returns a dense per-edge flow vector sized to roadGraph.EdgeCount(),
// every element 0 — the zero-flow AssignResult.FinalFlows starting point. Sizing to
// EdgeCount() (the loader guarantees dense, contiguous EdgeIDs 0..EdgeCount-1) lets
// addRouteFlow and every reader index it by EdgeID without a bounds check, and an
// empty batch still yields a full-length all-zero vector rather than nil.
func newFlowVector(roadGraph graph.Graph) []float64 {
	return make([]float64, roadGraph.EdgeCount())
}

// addRouteFlow adds weight to flows[e] for every edge e on route, accumulating the
// route's per-edge usage into the dense FinalFlows vector. A route may traverse an
// edge at most once on a simple shortest path, so this is one += per edge; a
// same-node (empty Edges) route contributes nothing, as it should. An edge id
// outside the vector is skipped defensively (the loader's dense-EdgeID guarantee
// makes that unreachable, but flow accumulation never trusts an id blindly). It is
// the single point where naive, reactive, and the iterative routers turn chosen
// paths into FinalFlows, so they all compute the same vector.
func addRouteFlow(flows []float64, route Route, weight float64) {
	for _, edgeID := range route.Edges {
		if edgeID < 0 || int(edgeID) >= len(flows) {
			continue
		}
		flows[edgeID] += weight
	}
}
