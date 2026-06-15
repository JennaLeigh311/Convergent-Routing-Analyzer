package routing_test

import (
	"context"
	"testing"

	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/congestion"
	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/congestion/memory"
	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/cost"
	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/domain"
	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/graph"
	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/routing"
)

// edgeIDForSegment finds the dense EdgeID whose Segment matches want, scanning the
// dense 0..EdgeCount-1 EdgeID space the loader guarantees. The reactive divert test
// needs the concrete EdgeID of a motorway sub-segment so it can jam exactly that
// edge in the congestion provider.
func edgeIDForSegment(test *testing.T, roadGraph *graph.AdjacencyGraph, want domain.SegmentID) domain.EdgeID {
	test.Helper()
	for edgeID := 0; edgeID < roadGraph.EdgeCount(); edgeID++ {
		edge, found := roadGraph.Edge(domain.EdgeID(edgeID))
		if found && edge.Segment == want {
			return domain.EdgeID(edgeID)
		}
	}
	test.Fatalf("no edge with segment_id %q in the toy graph", want)
	return -1
}

// TestReactiveZeroLoadEqualsNaive anchors the contract that under zero/empty
// congestion the reactive (congested-BPR) path equals the naive (free-flow) path:
// with every edge unloaded, BPR.Cost collapses to FreeFlowS, so the two routers
// must choose the same edges. From node 0 to node 2 that is the 2-hop motorway path
// 905512:0:F + 905512:1:F (32.4 s) over the 108.0 s direct residential edge.
func TestReactiveZeroLoadEqualsNaive(test *testing.T) {
	roadGraph := loadToyGraph(test)
	provider := memory.New(roadGraph.EdgeCount()) // every edge load 0
	reactive := routing.NewReactiveRouter(roadGraph, cost.DefaultBPR(), provider)
	naive := routing.NewNaiveRouter(roadGraph)

	origin, _ := roadGraph.Node(0)
	dest, _ := roadGraph.Node(2)
	req := routing.RouteRequest{ID: "n0->n2", From: origin.Pos, To: dest.Pos}

	reactiveRoute, err := reactive.Route(context.Background(), req)
	if err != nil {
		test.Fatalf("reactive Route() error = %v", err)
	}
	naiveRoute, err := naive.Route(context.Background(), req)
	if err != nil {
		test.Fatalf("naive Route() error = %v", err)
	}

	reactiveSegs := segmentsOf(test, roadGraph, reactiveRoute.Edges)
	naiveSegs := segmentsOf(test, roadGraph, naiveRoute.Edges)
	if len(reactiveSegs) != len(naiveSegs) {
		test.Fatalf("reactive path %v != naive path %v under zero load", reactiveSegs, naiveSegs)
	}
	for index := range naiveSegs {
		if reactiveSegs[index] != naiveSegs[index] {
			test.Errorf("zero-load path[%d] = %q, want naive %q (full %v)", index, reactiveSegs[index], naiveSegs[index], naiveSegs)
		}
	}

	// Under zero load CostS is the summed free-flow time, identical to naive's.
	if reactiveRoute.CostS != naiveRoute.CostS {
		test.Errorf("zero-load CostS = %v, want naive %v", reactiveRoute.CostS, naiveRoute.CostS)
	}
	wantSegs := []domain.SegmentID{"905512:0:F", "905512:1:F"}
	for index := range wantSegs {
		if reactiveSegs[index] != wantSegs[index] {
			test.Errorf("zero-load anchor path[%d] = %q, want %q", index, reactiveSegs[index], wantSegs[index])
		}
	}
}

// TestReactiveJamDiverts is the headline reactive case: jamming the naive-optimal
// motorway path's first hop hard makes its congested BPR cost exceed the 108.0 s
// direct residential edge, so reactive diverts off the motorway onto 9000001:0:F.
//
// Arithmetic (DefaultBPR: alpha=0.15, beta=4): jam 905512:0:F (FreeFlowS=18.0,
// capacity=5400) with 50000 vph. ratio = 50000/5400 ≈ 9.2593; ratio^4 ≈ 7351.9;
// Cost = 18.0*(1 + 0.15*7351.9) ≈ 19868 s. The second motorway hop 905512:1:F stays
// unloaded at 14.4 s, so the 2-hop motorway path now costs ≈ 19882 s, far above the
// unloaded direct edge's 108.0 s. Reactive therefore picks the direct edge — the
// best response to this single frozen snapshot.
func TestReactiveJamDiverts(test *testing.T) {
	roadGraph := loadToyGraph(test)
	provider := memory.New(roadGraph.EdgeCount())
	jammedEdge := edgeIDForSegment(test, roadGraph, "905512:0:F")
	provider.Set(jammedEdge, 50000) // jam the motorway first hop hard

	reactive := routing.NewReactiveRouter(roadGraph, cost.DefaultBPR(), provider)
	origin, _ := roadGraph.Node(0)
	dest, _ := roadGraph.Node(2)
	got, err := reactive.Route(context.Background(), routing.RouteRequest{ID: "jam", From: origin.Pos, To: dest.Pos})
	if err != nil {
		test.Fatalf("reactive Route() error = %v", err)
	}

	gotSegs := segmentsOf(test, roadGraph, got.Edges)
	wantSegs := []domain.SegmentID{"9000001:0:F"}
	if len(gotSegs) != len(wantSegs) || gotSegs[0] != wantSegs[0] {
		test.Fatalf("jammed path = %v, want diverted direct edge %v", gotSegs, wantSegs)
	}

	// CostS is the unloaded direct edge's BPR cost == its FreeFlowS (108.0 s).
	directEdge, _ := roadGraph.Edge(got.Edges[0])
	if got.CostS != directEdge.FreeFlowS {
		test.Errorf("diverted CostS = %v, want unloaded direct cost %v", got.CostS, directEdge.FreeFlowS)
	}
}

// TestReactiveAssignDeterministic locks the §R5 frozen-snapshot contract: Assign
// takes ONE snapshot and routes the whole batch against it, so repeated Assigns
// over an unchanged provider + OD set are byte-for-byte identical, and every
// request in a batch sees the same view. The batch mixes a node0->node2 request
// (diverted by the jam onto the direct edge) with a node3->node4 request (untouched
// by the jam) to show the single shared view applies uniformly.
func TestReactiveAssignDeterministic(test *testing.T) {
	roadGraph := loadToyGraph(test)
	provider := memory.New(roadGraph.EdgeCount())
	provider.Set(edgeIDForSegment(test, roadGraph, "905512:0:F"), 50000)
	reactive := routing.NewReactiveRouter(roadGraph, cost.DefaultBPR(), provider)

	node0, _ := roadGraph.Node(0)
	node2, _ := roadGraph.Node(2)
	node3, _ := roadGraph.Node(3)
	node4, _ := roadGraph.Node(4)
	reqs := []routing.RouteRequest{
		{ID: "a", From: node0.Pos, To: node2.Pos},
		{ID: "b", From: node3.Pos, To: node4.Pos},
	}

	first, err := reactive.Assign(context.Background(), reqs)
	if err != nil {
		test.Fatalf("first Assign() error = %v", err)
	}
	second, err := reactive.Assign(context.Background(), reqs)
	if err != nil {
		test.Fatalf("second Assign() error = %v", err)
	}

	if len(first) != len(reqs) || len(second) != len(reqs) {
		test.Fatalf("Assign returned %d and %d routes, want %d", len(first), len(second), len(reqs))
	}
	for index := range reqs {
		if first[index].RequestID != reqs[index].ID {
			test.Errorf("routes[%d].RequestID = %q, want %q", index, first[index].RequestID, reqs[index].ID)
		}
		if first[index].CostS != second[index].CostS {
			test.Errorf("route[%d] CostS differs across Assigns: %v vs %v", index, first[index].CostS, second[index].CostS)
		}
		firstSegs := segmentsOf(test, roadGraph, first[index].Edges)
		secondSegs := segmentsOf(test, roadGraph, second[index].Edges)
		if len(firstSegs) != len(secondSegs) {
			test.Fatalf("route[%d] path length differs across Assigns: %v vs %v", index, firstSegs, secondSegs)
		}
		for hop := range firstSegs {
			if firstSegs[hop] != secondSegs[hop] {
				test.Errorf("route[%d] path differs across Assigns at hop %d: %q vs %q", index, hop, firstSegs[hop], secondSegs[hop])
			}
		}
	}

	// The jammed-motorway request diverts to the direct edge; the unaffected
	// request takes its single hop — both seen under the same frozen snapshot.
	if segs := segmentsOf(test, roadGraph, first[0].Edges); len(segs) != 1 || segs[0] != "9000001:0:F" {
		test.Errorf("request a path = %v, want diverted [9000001:0:F]", segs)
	}
}

// TestReactiveName pins the strategy name used in benchmark output and the API.
func TestReactiveName(test *testing.T) {
	router := routing.NewReactiveRouter(loadToyGraph(test), cost.DefaultBPR(), memory.New(0))
	if name := router.Name(); name != "reactive" {
		test.Errorf("Name() = %q, want %q", name, "reactive")
	}
}

// TestReactiveHonorsContextCancellation confirms both Route and Assign return the
// context error promptly when the caller cancels — required because Assign is
// served behind an HTTP handler with caller deadlines.
func TestReactiveHonorsContextCancellation(test *testing.T) {
	roadGraph := loadToyGraph(test)
	router := routing.NewReactiveRouter(roadGraph, cost.DefaultBPR(), memory.New(roadGraph.EdgeCount()))

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel before use

	node0, _ := roadGraph.Node(0)
	node2, _ := roadGraph.Node(2)
	req := routing.RouteRequest{ID: "a", From: node0.Pos, To: node2.Pos}

	if _, err := router.Route(ctx, req); err != context.Canceled {
		test.Errorf("Route() with cancelled ctx err = %v, want context.Canceled", err)
	}
	if _, err := router.Assign(ctx, []routing.RouteRequest{req}); err != context.Canceled {
		test.Errorf("Assign() with cancelled ctx err = %v, want context.Canceled", err)
	}
}

// snapshotCountingProvider wraps a CongestionProvider and counts Snapshot() calls.
// It lets a test assert the §R5 frozen-snapshot contract directly — that Assign
// takes exactly ONE snapshot for the whole batch, not one per request — which a
// provider whose load never changes (the other tests' setup) cannot distinguish,
// since per-request and per-batch snapshots would then return identical loads.
type snapshotCountingProvider struct {
	inner         congestion.CongestionProvider
	snapshotCalls int
}

func (provider *snapshotCountingProvider) Load(edgeID domain.EdgeID) float64 {
	return provider.inner.Load(edgeID)
}

func (provider *snapshotCountingProvider) Snapshot() congestion.LoadSnapshot {
	provider.snapshotCalls++
	return provider.inner.Snapshot()
}

// TestReactiveAssignFreezesSnapshot verifies the mechanism behind §R5: Assign
// takes the congestion snapshot EXACTLY ONCE for the whole batch and routes every
// request against that one frozen view. A counting provider makes the claim
// testable independently of whether the load happens to change — an implementation
// that re-snapshotted per request would record len(reqs) calls and fail here.
func TestReactiveAssignFreezesSnapshot(test *testing.T) {
	roadGraph := loadToyGraph(test)
	inner := memory.New(roadGraph.EdgeCount())
	inner.Set(edgeIDForSegment(test, roadGraph, "905512:0:F"), 50000)
	spy := &snapshotCountingProvider{inner: inner}
	reactive := routing.NewReactiveRouter(roadGraph, cost.DefaultBPR(), spy)

	node0, _ := roadGraph.Node(0)
	node2, _ := roadGraph.Node(2)
	node3, _ := roadGraph.Node(3)
	node4, _ := roadGraph.Node(4)
	reqs := []routing.RouteRequest{
		{ID: "a", From: node0.Pos, To: node2.Pos},
		{ID: "b", From: node3.Pos, To: node4.Pos},
	}
	if _, err := reactive.Assign(context.Background(), reqs); err != nil {
		test.Fatalf("Assign() error = %v", err)
	}
	if spy.snapshotCalls != 1 {
		test.Errorf("Assign took %d snapshots for a %d-request batch, want exactly 1 (frozen-snapshot §R5)", spy.snapshotCalls, len(reqs))
	}
}

// TestReactiveRouteResnapsBetweenCalls is the counterpart to the frozen-batch
// property: each separate Route call takes a FRESH snapshot, so a Set between two
// Route calls changes the second result. With the motorway unloaded the first
// Route takes the 2-hop motorway path; after jamming that motorway hop the second
// Route must re-snapshot and divert to the direct edge (it is not caching an old
// snapshot).
func TestReactiveRouteResnapsBetweenCalls(test *testing.T) {
	roadGraph := loadToyGraph(test)
	provider := memory.New(roadGraph.EdgeCount())
	router := routing.NewReactiveRouter(roadGraph, cost.DefaultBPR(), provider)

	origin, _ := roadGraph.Node(0)
	dest, _ := roadGraph.Node(2)
	req := routing.RouteRequest{ID: "n0->n2", From: origin.Pos, To: dest.Pos}

	before, err := router.Route(context.Background(), req)
	if err != nil {
		test.Fatalf("Route() before jam error = %v", err)
	}
	if segs := segmentsOf(test, roadGraph, before.Edges); len(segs) != 2 || segs[0] != "905512:0:F" {
		test.Fatalf("before-jam path = %v, want 2-hop motorway [905512:0:F 905512:1:F]", segs)
	}

	provider.Set(edgeIDForSegment(test, roadGraph, "905512:0:F"), 50000) // jam after the first Route

	after, err := router.Route(context.Background(), req)
	if err != nil {
		test.Fatalf("Route() after jam error = %v", err)
	}
	if segs := segmentsOf(test, roadGraph, after.Edges); len(segs) != 1 || segs[0] != "9000001:0:F" {
		test.Errorf("after-jam path = %v, want diverted [9000001:0:F] (Route must re-snapshot, not cache)", segs)
	}
}

// TestReactiveDivertsNearThreshold anchors the actual crossover, not just the
// extreme jam in TestReactiveJamDiverts. The 2-hop motorway's congested cost first
// exceeds the 108.0 s direct edge at ~12422 vph on 905512:0:F (FreeFlowS=18.0,
// cap=5400): 18.0*(1 + 0.15*(v/5400)^4) + 14.4 > 108  ⇒  v > ~12422. Just below it
// the motorway still wins; just above it reactive diverts — so a regression that
// mis-scaled the BPR ratio or dropped the alpha term would be caught here even
// though it might still divert at the extreme load.
func TestReactiveDivertsNearThreshold(test *testing.T) {
	testCases := []struct {
		name     string
		loadVPH  float64
		wantSegs []domain.SegmentID
	}{
		{name: "just below threshold keeps motorway", loadVPH: 12000, wantSegs: []domain.SegmentID{"905512:0:F", "905512:1:F"}},
		{name: "just above threshold diverts to direct", loadVPH: 13000, wantSegs: []domain.SegmentID{"9000001:0:F"}},
	}
	for _, testCase := range testCases {
		test.Run(testCase.name, func(test1 *testing.T) {
			roadGraph := loadToyGraph(test1)
			provider := memory.New(roadGraph.EdgeCount())
			provider.Set(edgeIDForSegment(test1, roadGraph, "905512:0:F"), testCase.loadVPH)
			reactive := routing.NewReactiveRouter(roadGraph, cost.DefaultBPR(), provider)

			origin, _ := roadGraph.Node(0)
			dest, _ := roadGraph.Node(2)
			got, err := reactive.Route(context.Background(), routing.RouteRequest{ID: testCase.name, From: origin.Pos, To: dest.Pos})
			if err != nil {
				test1.Fatalf("Route() error = %v", err)
			}
			gotSegs := segmentsOf(test1, roadGraph, got.Edges)
			if len(gotSegs) != len(testCase.wantSegs) {
				test1.Fatalf("path = %v (%d hops), want %v", gotSegs, len(gotSegs), testCase.wantSegs)
			}
			for index := range testCase.wantSegs {
				if gotSegs[index] != testCase.wantSegs[index] {
					test1.Errorf("path[%d] = %q, want %q (full %v)", index, gotSegs[index], testCase.wantSegs[index], gotSegs)
				}
			}
		})
	}
}

// TestReactiveRouteSameNode pins the src==dst contract reactive documents: when
// From and To snap to the same node the route is a clean zero-edge, zero-cost
// success — not an error and not a panic on the empty predecessor walk. Downstream
// flow accumulation relies on tolerating an empty Edges slice.
func TestReactiveRouteSameNode(test *testing.T) {
	roadGraph := loadToyGraph(test)
	router := routing.NewReactiveRouter(roadGraph, cost.DefaultBPR(), memory.New(roadGraph.EdgeCount()))

	node0, _ := roadGraph.Node(0)
	got, err := router.Route(context.Background(), routing.RouteRequest{ID: "n0->n0", From: node0.Pos, To: node0.Pos})
	if err != nil {
		test.Fatalf("Route() same-node error = %v, want nil", err)
	}
	if len(got.Edges) != 0 {
		test.Errorf("Edges = %v, want empty path for a same-node request", got.Edges)
	}
	if got.CostS != 0 {
		test.Errorf("CostS = %v, want 0 for a same-node request", got.CostS)
	}
}

// TestReactiveRouteUnreachable confirms an unreachable destination is a clean
// error, not a panic or a zero-cost empty route. Node 5 is a sink (no outgoing
// edge), so nothing is reachable from it.
func TestReactiveRouteUnreachable(test *testing.T) {
	roadGraph := loadToyGraph(test)
	router := routing.NewReactiveRouter(roadGraph, cost.DefaultBPR(), memory.New(roadGraph.EdgeCount()))

	from, _ := roadGraph.Node(5) // sink
	toNode, _ := roadGraph.Node(0)
	if _, err := router.Route(context.Background(), routing.RouteRequest{ID: "sink", From: from.Pos, To: toNode.Pos}); err == nil {
		test.Error("Route() from sink node 5 should error (destination unreachable), got nil")
	}
}

// TestReactiveAssignFirstErrorReturnsNil locks reactive's Assign contract: on the
// first unroutable request it returns (nil, error) — never a partially-filled slice
// a caller might read stale zero-value Routes from. The second request originates at
// the sink node 5, from which nothing is reachable.
func TestReactiveAssignFirstErrorReturnsNil(test *testing.T) {
	roadGraph := loadToyGraph(test)
	router := routing.NewReactiveRouter(roadGraph, cost.DefaultBPR(), memory.New(roadGraph.EdgeCount()))

	node0, _ := roadGraph.Node(0)
	node2, _ := roadGraph.Node(2)
	node5, _ := roadGraph.Node(5) // sink: nothing is reachable from it
	reqs := []routing.RouteRequest{
		{ID: "ok", From: node0.Pos, To: node2.Pos},
		{ID: "unroutable", From: node5.Pos, To: node0.Pos},
	}

	routes, err := router.Assign(context.Background(), reqs)
	if err == nil {
		test.Fatal("Assign() with an unroutable request err = nil, want non-nil")
	}
	if routes != nil {
		test.Errorf("Assign() returned a partial slice %v, want nil on error", routes)
	}
}

// TestReactiveAssignHonorsMidBatchCancellation confirms the per-request ctx.Err()
// check inside Assign's loop aborts a batch already in progress, with no partial
// slice. Reactive's loop calls the unguarded routeWith (not Route), so the loop
// guard is the ONLY ctx.Err() check per iteration — a reactive Assign iteration
// consumes one Err() call, not the two a naive iteration does (Route adds its own).
// liveCalls=1 therefore lets request[0] route, then trips the guard at request[1].
// countingCtx is defined in naive_test.go (same test package).
func TestReactiveAssignHonorsMidBatchCancellation(test *testing.T) {
	roadGraph := loadToyGraph(test)
	router := routing.NewReactiveRouter(roadGraph, cost.DefaultBPR(), memory.New(roadGraph.EdgeCount()))

	node0, _ := roadGraph.Node(0)
	node2, _ := roadGraph.Node(2)
	ctx := &countingCtx{Context: context.Background(), liveCalls: 1}
	reqs := []routing.RouteRequest{
		{ID: "first", From: node0.Pos, To: node2.Pos},
		{ID: "second", From: node0.Pos, To: node2.Pos},
	}

	routes, err := router.Assign(ctx, reqs)
	if err != context.Canceled {
		test.Errorf("Assign() mid-batch cancel err = %v, want context.Canceled", err)
	}
	if routes != nil {
		test.Errorf("Assign() returned %v, want nil when cancelled mid-batch", routes)
	}
}
