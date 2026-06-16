package routing_test

import (
	"bytes"
	"context"
	"testing"

	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/congestion/memory"
	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/cost"
	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/domain"
	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/graph"
	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/routing"
)

// flowForSegment returns FinalFlows[e] for the edge whose Segment is want, so a
// flow assertion reads in stable segment_ids rather than load-order EdgeIDs.
func flowForSegment(test *testing.T, roadGraph *graph.AdjacencyGraph, flows []float64, want domain.SegmentID) float64 {
	test.Helper()
	edgeID := edgeIDForSegment(test, roadGraph, want)
	if int(edgeID) < 0 || int(edgeID) >= len(flows) {
		test.Fatalf("edge %d for segment %q is outside FinalFlows of length %d", edgeID, want, len(flows))
	}
	return flows[edgeID]
}

// TestNaiveAssignResultFinalFlows is the issue #71 acceptance case for naive: the
// AssignResult carries a FinalFlows vector that is the sum of per-edge usage ×
// request weight, sized to EdgeCount(), with single-pass metadata. Two requests
// both route node0->node2 over the 2-hop motorway (905512:0:F + 905512:1:F); with
// weights 1 (default) and 2.5 their combined flow on each motorway hop is 3.5, and
// the untaken direct edge 9000001:0:F carries zero.
func TestNaiveAssignResultFinalFlows(test *testing.T) {
	roadGraph := loadToyGraph(test)
	router := routing.NewNaiveRouter(roadGraph)

	node0, _ := roadGraph.Node(0)
	node2, _ := roadGraph.Node(2)
	reqs := []routing.RouteRequest{
		{ID: "a", From: node0.Pos, To: node2.Pos},              // default weight => 1.0
		{ID: "b", From: node0.Pos, To: node2.Pos, Weight: 2.5}, // explicit weight 2.5
	}

	result, err := router.AssignResult(context.Background(), reqs)
	if err != nil {
		test.Fatalf("AssignResult() error = %v", err)
	}

	// Single-pass metadata.
	if result.Iters != 1 || result.Gap != 0 || !result.Converged {
		test.Errorf("metadata = {Iters:%d Gap:%v Converged:%v}, want {1 0 true}", result.Iters, result.Gap, result.Converged)
	}
	if len(result.FinalFlows) != roadGraph.EdgeCount() {
		test.Fatalf("FinalFlows length = %d, want EdgeCount %d", len(result.FinalFlows), roadGraph.EdgeCount())
	}
	if len(result.Routes) != len(reqs) {
		test.Fatalf("Routes length = %d, want %d", len(result.Routes), len(reqs))
	}

	// Both requests took the 2-hop motorway; the combined flow on each hop is
	// 1.0 + 2.5 = 3.5, and the untaken direct edge carries zero.
	const wantMotorwayFlow = 3.5
	for _, seg := range []domain.SegmentID{"905512:0:F", "905512:1:F"} {
		if got := flowForSegment(test, roadGraph, result.FinalFlows, seg); got != wantMotorwayFlow {
			test.Errorf("FinalFlows[%s] = %v, want %v", seg, got, wantMotorwayFlow)
		}
	}
	if got := flowForSegment(test, roadGraph, result.FinalFlows, "9000001:0:F"); got != 0 {
		test.Errorf("FinalFlows[9000001:0:F] = %v, want 0 (untaken direct edge)", got)
	}

	// Assign returns exactly AssignResult.Routes.
	routes, err := router.Assign(context.Background(), reqs)
	if err != nil {
		test.Fatalf("Assign() error = %v", err)
	}
	if len(routes) != len(result.Routes) {
		test.Fatalf("Assign() returned %d routes, AssignResult %d", len(routes), len(result.Routes))
	}
	for index := range routes {
		if routes[index].RequestID != result.Routes[index].RequestID || routes[index].CostS != result.Routes[index].CostS {
			test.Errorf("Assign()[%d] = %+v, want AssignResult route %+v", index, routes[index], result.Routes[index])
		}
	}
}

// TestReactiveAssignResultFinalFlows is the #71 acceptance case for reactive: with
// the motorway first hop jammed the node0->node2 request diverts onto the direct
// edge 9000001:0:F, so FinalFlows places its weight there and zero on the jammed
// motorway hop — verifying FinalFlows reflects the actually-chosen (diverted) path.
func TestReactiveAssignResultFinalFlows(test *testing.T) {
	roadGraph := loadToyGraph(test)
	provider := memory.New(roadGraph.EdgeCount())
	provider.Set(edgeIDForSegment(test, roadGraph, "905512:0:F"), 50000) // jam the motorway hop
	reactive := routing.NewReactiveRouter(roadGraph, cost.DefaultBPR(), provider)

	node0, _ := roadGraph.Node(0)
	node2, _ := roadGraph.Node(2)
	reqs := []routing.RouteRequest{{ID: "a", From: node0.Pos, To: node2.Pos, Weight: 3}}

	result, err := reactive.AssignResult(context.Background(), reqs)
	if err != nil {
		test.Fatalf("AssignResult() error = %v", err)
	}
	if result.Iters != 1 || result.Gap != 0 || !result.Converged {
		test.Errorf("metadata = {Iters:%d Gap:%v Converged:%v}, want {1 0 true}", result.Iters, result.Gap, result.Converged)
	}
	if len(result.FinalFlows) != roadGraph.EdgeCount() {
		test.Fatalf("FinalFlows length = %d, want EdgeCount %d", len(result.FinalFlows), roadGraph.EdgeCount())
	}

	// Diverted onto the direct edge: its flow is the request weight; the jammed
	// motorway hop carries none.
	if got := flowForSegment(test, roadGraph, result.FinalFlows, "9000001:0:F"); got != 3 {
		test.Errorf("FinalFlows[9000001:0:F] = %v, want 3 (diverted weight)", got)
	}
	if got := flowForSegment(test, roadGraph, result.FinalFlows, "905512:0:F"); got != 0 {
		test.Errorf("FinalFlows[905512:0:F] = %v, want 0 (jammed, diverted away)", got)
	}
}

// TestAssignResultEmptyBatchYieldsZeroVector pins that an empty batch still returns
// a full-length all-zero FinalFlows (not nil), so a consumer can always index it,
// with single-pass metadata.
func TestAssignResultEmptyBatchYieldsZeroVector(test *testing.T) {
	roadGraph := loadToyGraph(test)
	router := routing.NewNaiveRouter(roadGraph)

	result, err := router.AssignResult(context.Background(), nil)
	if err != nil {
		test.Fatalf("AssignResult(nil) error = %v", err)
	}
	if len(result.FinalFlows) != roadGraph.EdgeCount() {
		test.Errorf("empty-batch FinalFlows length = %d, want EdgeCount %d (full zero vector, not nil)", len(result.FinalFlows), roadGraph.EdgeCount())
	}
	for index, flow := range result.FinalFlows {
		if flow != 0 {
			test.Errorf("empty-batch FinalFlows[%d] = %v, want 0", index, flow)
		}
	}
	if result.Iters != 1 || !result.Converged {
		test.Errorf("empty-batch metadata = {Iters:%d Converged:%v}, want {1 true}", result.Iters, result.Converged)
	}
}

// TestODSetRoundTrip locks the WriteODSet→ReadODSet round-trip: a batch serialized
// and re-read yields the identical requests (ids and exact float64 values), in the
// same order. This is the reproducibility primitive the determinism test stands on.
func TestODSetRoundTrip(test *testing.T) {
	reqs := []routing.RouteRequest{
		{ID: "r0", From: domain.LatLon{Lat: 40.73, Lon: -73.99}, To: domain.LatLon{Lat: 40.74, Lon: -73.97}, DepartAt: 12.5, Weight: 2},
		{ID: "r1", From: domain.LatLon{Lat: 40.742, Lon: -73.965}, To: domain.LatLon{Lat: 40.745, Lon: -73.96}},
	}

	var buf bytes.Buffer
	if err := routing.WriteODSet(&buf, reqs); err != nil {
		test.Fatalf("WriteODSet() error = %v", err)
	}
	got, err := routing.ReadODSet(&buf)
	if err != nil {
		test.Fatalf("ReadODSet() error = %v", err)
	}
	if len(got) != len(reqs) {
		test.Fatalf("ReadODSet() returned %d requests, want %d", len(got), len(reqs))
	}
	for index := range reqs {
		if got[index] != reqs[index] {
			test.Errorf("request[%d] = %+v, want %+v", index, got[index], reqs[index])
		}
	}
}

// TestWriteODSetRejectsDelimiterInID confirms a request id carrying a tab or
// newline (the format's only delimiters) is a hard error, not a silently-corrupted
// serialization.
func TestWriteODSetRejectsDelimiterInID(test *testing.T) {
	var buf bytes.Buffer
	err := routing.WriteODSet(&buf, []routing.RouteRequest{{ID: "bad\tid"}})
	if err == nil {
		test.Error("WriteODSet() with a tab in the id err = nil, want non-nil")
	}
}

// TestReadODSetRejectsMalformed confirms a line without exactly seven fields is a
// hard error rather than a silently-truncated request.
func TestReadODSetRejectsMalformed(test *testing.T) {
	if _, err := routing.ReadODSet(bytes.NewBufferString("only\ttwo\n")); err == nil {
		test.Error("ReadODSet() with a 2-field line err = nil, want non-nil")
	}
}

// TestSortedIterationHelpers confirms SortedNodeIDs/SortedEdgeIDs return the dense
// id ranges in ascending order — the deterministic iteration the assignment path
// uses instead of ranging a map.
func TestSortedIterationHelpers(test *testing.T) {
	roadGraph := loadToyGraph(test)

	nodes := routing.SortedNodeIDs(roadGraph)
	if len(nodes) != roadGraph.NodeCount() {
		test.Fatalf("SortedNodeIDs length = %d, want NodeCount %d", len(nodes), roadGraph.NodeCount())
	}
	for index, id := range nodes {
		if int(id) != index {
			test.Errorf("SortedNodeIDs[%d] = %d, want %d (dense ascending)", index, id, index)
		}
	}

	edges := routing.SortedEdgeIDs(roadGraph)
	if len(edges) != roadGraph.EdgeCount() {
		test.Fatalf("SortedEdgeIDs length = %d, want EdgeCount %d", len(edges), roadGraph.EdgeCount())
	}
	for index, id := range edges {
		if int(id) != index {
			test.Errorf("SortedEdgeIDs[%d] = %d, want %d (dense ascending)", index, id, index)
		}
	}

	// SortedKeysFloat returns sparse map keys in ascending order.
	keys := routing.SortedKeysFloat(map[domain.EdgeID]float64{5: 1, 1: 1, 3: 1})
	want := []domain.EdgeID{1, 3, 5}
	if len(keys) != len(want) {
		test.Fatalf("SortedKeysFloat = %v, want %v", keys, want)
	}
	for index := range want {
		if keys[index] != want[index] {
			test.Errorf("SortedKeysFloat[%d] = %d, want %d", index, keys[index], want[index])
		}
	}
}

// TestNaiveAssignResultDeterministic is the issue #71 determinism acceptance
// criterion: a fixed seed plus a SERIALIZED OD set reproduces an Assign
// byte-identically — identical FinalFlows and identical route ordering across
// runs. The OD set is round-tripped through Write/ReadODSet (so the test exercises
// the actual on-disk form, not an in-memory slice), and the RNG is built from one
// fixed seed (NewSeededRNG) to stand in for the reproducible-seed plumbing the
// iterative routers will use. Naive's assignment is itself deterministic, so the
// two runs must match bit-for-bit.
func TestNaiveAssignResultDeterministic(test *testing.T) {
	roadGraph := loadToyGraph(test)

	node0, _ := roadGraph.Node(0)
	node2, _ := roadGraph.Node(2)
	node3, _ := roadGraph.Node(3)
	node4, _ := roadGraph.Node(4)
	original := []routing.RouteRequest{
		{ID: "a", From: node0.Pos, To: node2.Pos, Weight: 2},
		{ID: "b", From: node3.Pos, To: node4.Pos, Weight: 1.5},
	}

	// Serialize the OD set once, then read it back into two independent batches —
	// the two "runs" both start from the identical serialized form.
	var buf bytes.Buffer
	if err := routing.WriteODSet(&buf, original); err != nil {
		test.Fatalf("WriteODSet() error = %v", err)
	}
	serialized := buf.Bytes()

	runOnce := func() routing.AssignResult {
		reqs, err := routing.ReadODSet(bytes.NewReader(serialized))
		if err != nil {
			test.Fatalf("ReadODSet() error = %v", err)
		}
		_ = routing.NewSeededRNG(42) // fixed single seed source (no-op for naive, present for the iterative routers)
		router := routing.NewNaiveRouter(roadGraph)
		result, err := router.AssignResult(context.Background(), reqs)
		if err != nil {
			test.Fatalf("AssignResult() error = %v", err)
		}
		return result
	}

	first := runOnce()
	second := runOnce()

	// FinalFlows byte-identical.
	if len(first.FinalFlows) != len(second.FinalFlows) {
		test.Fatalf("FinalFlows lengths differ across runs: %d vs %d", len(first.FinalFlows), len(second.FinalFlows))
	}
	for index := range first.FinalFlows {
		if first.FinalFlows[index] != second.FinalFlows[index] {
			test.Errorf("FinalFlows[%d] differs across runs: %v vs %v", index, first.FinalFlows[index], second.FinalFlows[index])
		}
	}

	// Route ordering byte-identical (request ids and the chosen edge sequences).
	if len(first.Routes) != len(second.Routes) {
		test.Fatalf("Routes lengths differ across runs: %d vs %d", len(first.Routes), len(second.Routes))
	}
	for index := range first.Routes {
		if first.Routes[index].RequestID != second.Routes[index].RequestID {
			test.Errorf("Routes[%d].RequestID differs across runs: %q vs %q", index, first.Routes[index].RequestID, second.Routes[index].RequestID)
		}
		firstEdges, secondEdges := first.Routes[index].Edges, second.Routes[index].Edges
		if len(firstEdges) != len(secondEdges) {
			test.Fatalf("Routes[%d].Edges lengths differ across runs: %v vs %v", index, firstEdges, secondEdges)
		}
		for hop := range firstEdges {
			if firstEdges[hop] != secondEdges[hop] {
				test.Errorf("Routes[%d].Edges[%d] differs across runs: %d vs %d", index, hop, firstEdges[hop], secondEdges[hop])
			}
		}
	}
}
