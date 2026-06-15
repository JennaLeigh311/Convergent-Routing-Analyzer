package routing_test

import (
	"context"
	"testing"

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
