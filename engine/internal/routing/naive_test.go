package routing_test

import (
	"context"
	"testing"

	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/domain"
	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/graph"
	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/routing"
)

// toyNetworkPath is the shared routing fixture (#26). From this package
// (engine/internal/routing) the module-level testdata dir resolves at
// ../../testdata. toyBounds is the loose expected-region box the fixture loads
// under (see engine/testdata/README.md).
const toyNetworkPath = "../../testdata/toy_network.geojson"

func loadToyGraph(t *testing.T) *graph.AdjacencyGraph {
	t.Helper()
	g, _, err := graph.LoadEdgeAttributesGeoJSONFile(toyNetworkPath, graph.WithExpectedBounds(-74, -73, 40, 41))
	if err != nil {
		t.Fatalf("toy_network.geojson must load with zero error, got: %v", err)
	}
	return g
}

// segmentsOf maps a path of EdgeIDs to their segment_ids, so assertions read in
// the fixture's stable wire keys rather than load-order-dependent EdgeIDs.
func segmentsOf(t *testing.T, g *graph.AdjacencyGraph, path []domain.EdgeID) []domain.SegmentID {
	t.Helper()
	segs := make([]domain.SegmentID, len(path))
	for i, id := range path {
		e, ok := g.Edge(id)
		if !ok {
			t.Fatalf("path contains unknown edge id %d", id)
		}
		segs[i] = e.Segment
	}
	return segs
}

// TestNaiveRouteLowestCostNotFewestHops is the headline acceptance case: from
// origin node 0 to destination node 2 the toy graph offers a direct 1-hop
// residential edge (108.0 s) and a 2-hop motorway alternative (18.0 + 14.4 =
// 32.4 s). The lowest-cost path has MORE hops than the direct edge, so a correct
// Dijkstra returns the 2-hop motorway path — a BFS / fewest-hops search would
// wrongly return the direct edge. This is what distinguishes Dijkstra from BFS.
func TestNaiveRouteLowestCostNotFewestHops(t *testing.T) {
	g := loadToyGraph(t)
	r := routing.NewNaiveRouter(g)

	origin, _ := g.Node(0)
	dest, _ := g.Node(2)

	got, err := r.Route(context.Background(), routing.RouteRequest{
		ID:   "n0->n2",
		From: origin.Pos,
		To:   dest.Pos,
	})
	if err != nil {
		t.Fatalf("Route() error = %v", err)
	}

	// The chosen path is the 2-hop motorway alternative, by segment_id.
	wantSegs := []domain.SegmentID{"905512:0:F", "905512:1:F"}
	gotSegs := segmentsOf(t, g, got.Edges)
	if len(gotSegs) != len(wantSegs) {
		t.Fatalf("path = %v (%d hops), want %v (%d hops)", gotSegs, len(gotSegs), wantSegs, len(wantSegs))
	}
	for i := range wantSegs {
		if gotSegs[i] != wantSegs[i] {
			t.Errorf("path[%d] = %q, want %q (full path %v)", i, gotSegs[i], wantSegs[i], gotSegs)
		}
	}

	// Lowest-cost beat fewest-hops: the direct 1-hop residential edge was NOT taken.
	if len(got.Edges) <= 1 {
		t.Errorf("router took a %d-hop path; expected the 2-hop lowest-cost path, not the fewest-hops direct edge", len(got.Edges))
	}
	for _, seg := range gotSegs {
		if seg == "9000001:0:F" {
			t.Errorf("router took the slow direct edge 9000001:0:F; a lowest-cost router must avoid it")
		}
	}

	// CostS == summed FreeFlowS of the chosen edges (the cost optimized against).
	var wantCost float64
	for _, id := range got.Edges {
		e, _ := g.Edge(id)
		wantCost += e.FreeFlowS
	}
	if got.CostS != wantCost {
		t.Errorf("CostS = %v, want summed FreeFlowS %v", got.CostS, wantCost)
	}
	if got.RequestID != "n0->n2" {
		t.Errorf("RequestID = %q, want %q", got.RequestID, "n0->n2")
	}
}

// TestNaiveRouteDirectSingleHop confirms the router returns the single direct
// edge when it IS the cheapest option: node 3 -> node 4 has only the secondary
// F edge (12.96 s), so the optimal path is that one hop.
func TestNaiveRouteDirectSingleHop(t *testing.T) {
	g := loadToyGraph(t)
	r := routing.NewNaiveRouter(g)

	from, _ := g.Node(3)
	to, _ := g.Node(4)
	got, err := r.Route(context.Background(), routing.RouteRequest{ID: "n3->n4", From: from.Pos, To: to.Pos})
	if err != nil {
		t.Fatalf("Route() error = %v", err)
	}
	gotSegs := segmentsOf(t, g, got.Edges)
	if len(gotSegs) != 1 || gotSegs[0] != "48800123:0:F" {
		t.Fatalf("path = %v, want single hop [48800123:0:F]", gotSegs)
	}
	if e, _ := g.Edge(got.Edges[0]); got.CostS != e.FreeFlowS {
		t.Errorf("CostS = %v, want %v", got.CostS, e.FreeFlowS)
	}
}

// TestNaiveRouteSameNode pins the src==dst contract: when From and To snap to the
// same graph node the route is a clean zero-edge, zero-cost success — not an error,
// and not a panic on the empty predecessor walk. Locked here because downstream
// flow accumulation relies on tolerating an empty Edges slice.
func TestNaiveRouteSameNode(t *testing.T) {
	g := loadToyGraph(t)
	r := routing.NewNaiveRouter(g)

	n0, _ := g.Node(0)
	got, err := r.Route(context.Background(), routing.RouteRequest{ID: "n0->n0", From: n0.Pos, To: n0.Pos})
	if err != nil {
		t.Fatalf("Route() same-node error = %v, want nil", err)
	}
	if len(got.Edges) != 0 {
		t.Errorf("Edges = %v, want empty path for a same-node request", got.Edges)
	}
	if got.CostS != 0 {
		t.Errorf("CostS = %v, want 0 for a same-node request", got.CostS)
	}
	if got.RequestID != "n0->n0" {
		t.Errorf("RequestID = %q, want %q", got.RequestID, "n0->n0")
	}
}

// TestNaiveRouteUnreachable confirms an unreachable destination is a clean error,
// not a panic or a zero-cost empty route. Node 5 is a sink (no outgoing edge), so
// nothing is reachable from it.
func TestNaiveRouteUnreachable(t *testing.T) {
	g := loadToyGraph(t)
	r := routing.NewNaiveRouter(g)

	from, _ := g.Node(5) // sink
	to, _ := g.Node(0)
	if _, err := r.Route(context.Background(), routing.RouteRequest{ID: "sink", From: from.Pos, To: to.Pos}); err == nil {
		t.Error("Route() from sink node 5 should error (destination unreachable), got nil")
	}
}

// TestNaiveAssignIndependentRoutes confirms Assign returns one route per request,
// in input order, each routed independently.
func TestNaiveAssignIndependentRoutes(t *testing.T) {
	g := loadToyGraph(t)
	r := routing.NewNaiveRouter(g)

	n0, _ := g.Node(0)
	n2, _ := g.Node(2)
	n3, _ := g.Node(3)
	n5, _ := g.Node(5)
	reqs := []routing.RouteRequest{
		{ID: "a", From: n0.Pos, To: n2.Pos},
		{ID: "b", From: n3.Pos, To: n5.Pos},
	}

	routes, err := r.Assign(context.Background(), reqs)
	if err != nil {
		t.Fatalf("Assign() error = %v", err)
	}
	if len(routes) != len(reqs) {
		t.Fatalf("Assign() returned %d routes, want %d", len(routes), len(reqs))
	}
	for i, want := range []string{"a", "b"} {
		if routes[i].RequestID != want {
			t.Errorf("routes[%d].RequestID = %q, want %q", i, routes[i].RequestID, want)
		}
		if len(routes[i].Edges) == 0 {
			t.Errorf("routes[%d] (%s) has an empty path", i, want)
		}
	}

	// Empty input yields an empty, non-error result.
	if routes, err := r.Assign(context.Background(), nil); err != nil || len(routes) != 0 {
		t.Errorf("Assign(nil) = (%v, %v), want empty, nil", routes, err)
	}
}

// TestNaiveAssignFirstErrorReturnsNil locks the documented Assign contract: on the
// first unroutable request it returns (nil, error) — never a partially-filled slice
// a caller might read stale zero-value Routes from. The second request originates at
// the sink node 5, from which nothing is reachable, so its Route fails.
func TestNaiveAssignFirstErrorReturnsNil(t *testing.T) {
	g := loadToyGraph(t)
	r := routing.NewNaiveRouter(g)

	n0, _ := g.Node(0)
	n2, _ := g.Node(2)
	n5, _ := g.Node(5) // sink: nothing is reachable from it
	reqs := []routing.RouteRequest{
		{ID: "ok", From: n0.Pos, To: n2.Pos},
		{ID: "unroutable", From: n5.Pos, To: n0.Pos},
	}

	routes, err := r.Assign(context.Background(), reqs)
	if err == nil {
		t.Fatal("Assign() with an unroutable request err = nil, want non-nil")
	}
	if routes != nil {
		t.Errorf("Assign() returned a partial slice %v, want nil on error", routes)
	}
}

// countingCtx reports cancellation only after liveCalls observations of Err(),
// letting a test drive Assign's per-request ctx.Err() check to a chosen point in
// the batch deterministically (no wall-clock races). Each fully-routed Assign
// iteration consumes two Err() calls — the loop guard plus Route's own
// top-of-function check — so liveCalls = 2 lets request[0] route, then trips the
// loop guard at request[1].
type countingCtx struct {
	context.Context
	calls     int
	liveCalls int
}

func (c *countingCtx) Err() error {
	c.calls++
	if c.calls > c.liveCalls {
		return context.Canceled
	}
	return c.Context.Err()
}

// TestNaiveAssignHonorsMidBatchCancellation confirms the per-request ctx.Err()
// check inside Assign's loop (not just the pre-call guard) aborts a batch already
// in progress, with no partial slice — the property that lets a cancelled HTTP
// request stop a long assignment promptly instead of running it to completion.
func TestNaiveAssignHonorsMidBatchCancellation(t *testing.T) {
	g := loadToyGraph(t)
	r := routing.NewNaiveRouter(g)

	n0, _ := g.Node(0)
	n2, _ := g.Node(2)
	ctx := &countingCtx{Context: context.Background(), liveCalls: 2}
	reqs := []routing.RouteRequest{
		{ID: "first", From: n0.Pos, To: n2.Pos},
		{ID: "second", From: n0.Pos, To: n2.Pos},
	}

	routes, err := r.Assign(ctx, reqs)
	if err != context.Canceled {
		t.Errorf("Assign() mid-batch cancel err = %v, want context.Canceled", err)
	}
	if routes != nil {
		t.Errorf("Assign() returned %v, want nil when cancelled mid-batch", routes)
	}
}

// TestNaiveHonorsContextCancellation confirms both Route and Assign return the
// context error promptly when the caller cancels — required because Assign is
// served behind an HTTP handler with caller deadlines.
func TestNaiveHonorsContextCancellation(t *testing.T) {
	g := loadToyGraph(t)
	r := routing.NewNaiveRouter(g)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel before use

	n0, _ := g.Node(0)
	n2, _ := g.Node(2)
	req := routing.RouteRequest{ID: "a", From: n0.Pos, To: n2.Pos}

	if _, err := r.Route(ctx, req); err != context.Canceled {
		t.Errorf("Route() with cancelled ctx err = %v, want context.Canceled", err)
	}
	if _, err := r.Assign(ctx, []routing.RouteRequest{req}); err != context.Canceled {
		t.Errorf("Assign() with cancelled ctx err = %v, want context.Canceled", err)
	}
}

// TestNaiveName pins the strategy name used in benchmark output and the API.
func TestNaiveName(t *testing.T) {
	if name := routing.NewNaiveRouter(loadToyGraph(t)).Name(); name != "naive" {
		t.Errorf("Name() = %q, want %q", name, "naive")
	}
}
