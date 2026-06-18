package routing_test

import (
	"context"
	"math"
	"testing"

	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/congestion/memory"
	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/cost"
	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/domain"
	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/graph"
	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/routing"
)

// adversarial_test.go is the regression for issue #73: it pins the documented
// connectedness decision (docs/architecture.md "Graph connectedness:
// unreachability is a routing-layer concern, not a loader rejection"). It loads
// the adversarial toy fixture — which deliberately contains a DISCONNECTED
// component and a ONE-WAY TRAP corridor (no reverse row) — and asserts:
//
//  1. The fixture loads cleanly through the validate-and-reject loader (a
//     disconnected graph is valid §2 data, NOT a load-time rejection).
//  2. An unreachable OD pair returns a defined, non-crashing result: a clean
//     "no route" error, never a panic / NaN / +Inf / divide-by-zero.
//  3. No route any Router returns traverses the one-way corridor against its
//     direction, and the forbidden-direction OD pair yields a clean no-route.
//
// Tests run against the Router port over every concrete strategy so the property
// is enforced for the Phase-3 algorithms as they land, not just today's naive
// baseline.

const adversarialNetworkPath = "../../testdata/toy_network_adversarial.geojson"

// adversarialBounds is a loose box around the adversarial fixture's window
// (main component lon ∈ [-73.99,-73.9807] lat ∈ [40.73,40.7356]; island lon ∈
// [-73.93,-73.9274] lat ∈ [40.78,40.7816]). It still rejects a [lat,lon] axis swap.
func adversarialBounds() graph.LoadOption { return graph.WithExpectedBounds(-74, -73, 40, 41) }

// oneWayTrapSegment is the single directed corridor with NO reverse row
// (7100003:0:F, node 2 -> node 3). No router output may contain this edge id
// "traversed backward" — but since an Edge is directed From->To, traversing it
// at all only ever means From(node2)->To(node3); the real guard is that any
// path INTO node 2 or out of node 3 must never list this edge. We assert the
// stronger, structural property below: a path is a valid directed walk.
const oneWayTrapSegment domain.SegmentID = "7100003:0:F"

// loadAdversarial loads the adversarial fixture, failing the test on any load
// error. Acceptance: a disconnected + one-way-trap graph is VALID §2 data and
// MUST load cleanly (the connectedness decision is enforced at routing time,
// not by rejecting the artifact).
func loadAdversarial(test *testing.T) *graph.AdjacencyGraph {
	test.Helper()
	roadGraph, _, err := graph.LoadEdgeAttributesGeoJSONFile(adversarialNetworkPath, adversarialBounds())
	if err != nil {
		test.Fatalf("toy_network_adversarial.geojson must load cleanly (a disconnected/one-way graph is valid §2 data, not a load-time rejection), got: %v", err)
	}
	return roadGraph
}

// concreteRouters returns every concrete Router strategy under test over the
// given graph. The acceptance criterion is "no route returned by ANY router",
// so the set spans the strategies that exist today — the naive baseline and the
// reactive best-response router — and the remaining Phase-3 strategies
// (incremental, msa, systemoptimal, multipath) append here as they land, so the
// unreachable-OD and one-way invariants are enforced for all of them by
// construction. The reactive router weights edges with the default BPR over a
// zero-load in-memory snapshot sized to the graph's EdgeCount; load values do
// not change reachability or edge direction, so it must reach the same no-route
// / forward-only verdicts as naive on these structural pathologies.
func concreteRouters(roadGraph graph.Graph) []routing.Router {
	reactiveProvider := memory.New(roadGraph.EdgeCount())
	return []routing.Router{
		routing.NewNaiveRouter(roadGraph),
		routing.NewReactiveRouter(roadGraph, cost.DefaultBPR(), reactiveProvider),
		routing.NewIncrementalRouter(roadGraph, cost.DefaultBPR()),
		routing.NewMSARouter(roadGraph, cost.DefaultBPR()),
	}
}

// nodePos returns the WGS84 position of a node id (for building RouteRequests
// that snap to a specific node via NearestNode).
func nodePos(test *testing.T, roadGraph *graph.AdjacencyGraph, nodeID domain.NodeID) domain.LatLon {
	test.Helper()
	node, ok := roadGraph.Node(nodeID)
	if !ok {
		test.Fatalf("node %d missing from adversarial graph", nodeID)
	}
	return node.Pos
}

// TestAdversarialFixtureLoadsCleanly is acceptance #1: the disconnected +
// one-way-trap fixture loads through the validate-and-reject loader with zero
// error and the expected shape (5 directed edges, 6 nodes: main {0,1,2,3} +
// island {4,5}).
func TestAdversarialFixtureLoadsCleanly(test *testing.T) {
	roadGraph := loadAdversarial(test)

	if got, want := roadGraph.EdgeCount(), 5; got != want {
		test.Errorf("EdgeCount = %d, want %d", got, want)
	}
	if got, want := roadGraph.NodeCount(), 6; got != want {
		test.Errorf("NodeCount = %d, want %d (main component 0..3 + disconnected island 4,5)", got, want)
	}

	// The one-way trap corridor has NO reverse row: exactly one edge touches
	// node 3, and it points INTO node 3 (node 2 -> node 3), so node 3 has zero
	// out-edges (a directed sink).
	sink := domain.NodeID(3)
	if out := roadGraph.OutEdgeIDs(sink); len(out) != 0 {
		test.Errorf("one-way trap sink node %d has %d out-edge(s) %v, want 0 (no reverse row)", sink, len(out), out)
	}
}

// TestUnreachableODYieldsCleanNoRoute is acceptance #2: routing across the
// disconnected gap (main-component origin -> island destination) returns a
// defined, non-crashing result — an error, never a panic / NaN / +Inf cost.
func TestAdversarialUnreachableODYieldsCleanNoRoute(test *testing.T) {
	roadGraph := loadAdversarial(test)
	ctx := context.Background()

	originPos := nodePos(test, roadGraph, 0) // main component
	islandPos := nodePos(test, roadGraph, 4) // disconnected island

	// Sanity: the two coordinates really snap to different components, so this is
	// a genuine cross-gap request (NearestNode did not collapse them).
	originNode, ok1 := roadGraph.NearestNode(originPos)
	islandNode, ok2 := roadGraph.NearestNode(islandPos)
	if !ok1 || !ok2 {
		test.Fatalf("NearestNode failed: origin ok=%v, island ok=%v", ok1, ok2)
	}
	if originNode == islandNode {
		test.Fatalf("origin and island snapped to the same node %d — gap is too small to test unreachability", originNode)
	}

	for _, router := range concreteRouters(roadGraph) {
		route, err := router.Route(ctx, routing.RouteRequest{ID: "unreachable", From: originPos, To: islandPos})
		if err == nil {
			test.Errorf("%s: routing an unreachable OD pair returned a route (%+v), want a clean no-route error", router.Name(), route)
			continue
		}
		// Defined, non-crashing: no panic reached here, and no poisoned numeric
		// leaked through the (zero-valued) Route.
		if math.IsNaN(route.CostS) || math.IsInf(route.CostS, 0) {
			test.Errorf("%s: unreachable OD produced a non-finite CostS %v (want a clean zero-value Route alongside the error)", router.Name(), route.CostS)
		}
		if len(route.Edges) != 0 {
			test.Errorf("%s: unreachable OD produced %d edge(s) %v alongside the error, want none", router.Name(), len(route.Edges), route.Edges)
		}
	}
}

// TestOneWayTrapNeverTraversedBackward is acceptance #3. Two halves:
//
//	(a) Routing the FORBIDDEN direction across the one-way trap (node 3 -> node 0,
//	    where the only edge touching node 3 points the other way) yields a clean
//	    no-route — the router never "uses" the one-way edge in reverse.
//	(b) Every route any router DOES return is a valid forward-directed walk: each
//	    listed edge's From equals the previous edge's To, so no edge is ever
//	    traversed against its From->To direction. We additionally route the
//	    reachable forward OD (node 0 -> node 3) and confirm it legitimately uses
//	    the one-way edge FORWARD, so the test would fail if direction were ignored.
func TestAdversarialOneWayTrapNeverTraversedBackward(test *testing.T) {
	roadGraph := loadAdversarial(test)
	ctx := context.Background()

	sinkPos := nodePos(test, roadGraph, 3)   // only reachable via the one-way trap, forward
	originPos := nodePos(test, roadGraph, 0) // main-component origin

	for _, router := range concreteRouters(roadGraph) {
		// (a) Forbidden direction: out of the sink there is no edge at all, so the
		// only way to reach node 0 would be to walk the one-way trap backward.
		// Must be a clean no-route.
		if route, err := router.Route(ctx, routing.RouteRequest{ID: "forbidden", From: sinkPos, To: originPos}); err == nil {
			test.Errorf("%s: routing the forbidden one-way direction (node 3 -> node 0) returned a route %+v; the one-way corridor must not be traversed backward", router.Name(), route)
		}

		// (b) Reachable forward OD: node 0 -> node 3 must succeed AND use the
		// one-way trap edge forward.
		route, err := router.Route(ctx, routing.RouteRequest{ID: "forward", From: originPos, To: sinkPos})
		if err != nil {
			test.Fatalf("%s: forward OD node 0 -> node 3 must be reachable, got error: %v", router.Name(), err)
		}
		assertForwardWalk(test, router.Name(), roadGraph, originPos, sinkPos, route.Edges)

		// The forward route legitimately traverses the one-way trap edge (forward),
		// proving the no-route above is due to direction, not a disconnected node.
		usedTrap := false
		for _, edgeID := range route.Edges {
			edge, ok := roadGraph.Edge(edgeID)
			if !ok {
				test.Fatalf("%s: route lists edge id %d not in graph", router.Name(), edgeID)
			}
			if edge.Segment == oneWayTrapSegment {
				usedTrap = true
			}
		}
		if !usedTrap {
			test.Errorf("%s: forward route node 0 -> node 3 did not use the one-way trap edge %q; the OD pair no longer exercises the trap", router.Name(), oneWayTrapSegment)
		}
	}
}

// assertForwardWalk verifies the returned edge list is a valid directed walk
// from the node nearest src to the node nearest dst: it starts at src's snapped
// node, each edge's From equals the previous edge's To (so no edge is traversed
// backward), and it ends at dst's snapped node. This is the structural guard
// that "no router output traverses a one-way edge against its direction" — a
// backward traversal would break the From==prevTo chain.
func assertForwardWalk(test *testing.T, name string, roadGraph *graph.AdjacencyGraph, srcPos, dstPos domain.LatLon, edges []domain.EdgeID) {
	test.Helper()
	srcNode, ok1 := roadGraph.NearestNode(srcPos)
	dstNode, ok2 := roadGraph.NearestNode(dstPos)
	if !ok1 || !ok2 {
		test.Fatalf("%s: NearestNode failed snapping walk endpoints", name)
	}
	if len(edges) == 0 {
		if srcNode != dstNode {
			test.Errorf("%s: empty path but src node %d != dst node %d", name, srcNode, dstNode)
		}
		return
	}
	at := srcNode
	for index, edgeID := range edges {
		edge, ok := roadGraph.Edge(edgeID)
		if !ok {
			test.Fatalf("%s: path edge %d (id %d) not in graph", name, index, edgeID)
		}
		if edge.From != at {
			test.Errorf("%s: path edge %d (id %d) starts at node %d but the walk is at node %d — an edge traversed against its From->To direction breaks this chain", name, index, edgeID, edge.From, at)
		}
		at = edge.To
	}
	if at != dstNode {
		test.Errorf("%s: path ends at node %d, want dst node %d", name, at, dstNode)
	}
}
