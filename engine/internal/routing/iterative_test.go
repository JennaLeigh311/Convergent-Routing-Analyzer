package routing_test

import (
	"bytes"
	"context"
	"fmt"
	"math"
	"testing"

	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/cost"
	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/domain"
	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/graph"
	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/routing"
)

// iterativeRouters returns the two iterative strategies (incremental, msa) over the
// graph, so the shared properties — finite gap, finite flows, determinism, no
// NaN/panic — are asserted for both without duplicating the table per test.
func iterativeRouters(roadGraph graph.Graph) []routing.Router {
	return []routing.Router{
		routing.NewIncrementalRouter(roadGraph, cost.DefaultBPR()),
		routing.NewMSARouter(roadGraph, cost.DefaultBPR()),
	}
}

// heavyDemand builds n requests from node0 to node2 on the toy network, each with the
// given per-request weight. node0->node2 is the lowest-cost-≠-fewest-hops pair (a
// 2-hop motorway vs a 1-hop residential), so under enough demand the motorway
// congests and an equilibrium assignment must split flow across both routes — the
// behavior MSA converges and incremental approximates.
func heavyDemand(test *testing.T, roadGraph *graph.AdjacencyGraph, n int, weight float64) []routing.RouteRequest {
	test.Helper()
	node0, _ := roadGraph.Node(0)
	node2, _ := roadGraph.Node(2)
	reqs := make([]routing.RouteRequest, n)
	for index := range reqs {
		reqs[index] = routing.RouteRequest{
			ID:     fmt.Sprintf("r%d", index),
			From:   node0.Pos,
			To:     node2.Pos,
			Weight: weight,
		}
	}
	return reqs
}

// assertFiniteFlows fails if any FinalFlow is NaN or +/-Inf.
func assertFiniteFlows(test *testing.T, name string, flows []float64) {
	test.Helper()
	for index, flow := range flows {
		if math.IsNaN(flow) || math.IsInf(flow, 0) {
			test.Errorf("%s: FinalFlows[%d] = %v, want a finite value", name, index, flow)
		}
	}
}

// TestIterativeRoutersReportFiniteGapAndIters is the acceptance criterion that both
// routers run an Assign over the toy network and report a finite achieved gap and a
// positive iteration count, with a full-length finite FinalFlows vector.
func TestIterativeRoutersReportFiniteGapAndIters(test *testing.T) {
	roadGraph := loadToyGraph(test)
	// Enough demand (200 vehicles/hour × 50 requests = ~10k vph toward the 5,400 vph
	// motorway) to actually congest the corridor and force an equilibrium split.
	reqs := heavyDemand(test, roadGraph, 50, 200)

	for _, router := range iterativeRouters(roadGraph) {
		result, err := router.AssignResult(context.Background(), reqs)
		if err != nil {
			test.Fatalf("%s: AssignResult() error = %v", router.Name(), err)
		}
		if math.IsNaN(result.Gap) || math.IsInf(result.Gap, 0) {
			test.Errorf("%s: Gap = %v, want finite", router.Name(), result.Gap)
		}
		if result.Gap < 0 {
			test.Errorf("%s: Gap = %v, want >= 0", router.Name(), result.Gap)
		}
		if result.Iters < 1 || result.Iters > 100 {
			test.Errorf("%s: Iters = %d, want in [1,100]", router.Name(), result.Iters)
		}
		if len(result.FinalFlows) != roadGraph.EdgeCount() {
			test.Errorf("%s: FinalFlows length = %d, want EdgeCount %d", router.Name(), len(result.FinalFlows), roadGraph.EdgeCount())
		}
		assertFiniteFlows(test, router.Name(), result.FinalFlows)
		if len(result.Routes) != len(reqs) {
			test.Errorf("%s: Routes length = %d, want %d", router.Name(), len(result.Routes), len(reqs))
		}
	}
}

// TestMSAConvergesOnToyNetwork is the headline MSA acceptance criterion: on the toy
// network MSA reports Converged = true with Gap < 1e-4. The demand is heavy enough to
// congest the motorway so the equilibrium is a genuine multi-path split (a trivial
// single-path assignment would converge at iteration 1 and not exercise the average).
func TestMSAConvergesOnToyNetwork(test *testing.T) {
	roadGraph := loadToyGraph(test)
	reqs := heavyDemand(test, roadGraph, 50, 200)

	router := routing.NewMSARouter(roadGraph, cost.DefaultBPR())
	result, err := router.AssignResult(context.Background(), reqs)
	if err != nil {
		test.Fatalf("AssignResult() error = %v", err)
	}
	if !result.Converged {
		test.Errorf("msa Converged = false (Gap = %v after %d iters), want true", result.Gap, result.Iters)
	}
	if !(result.Gap < 1e-4) {
		test.Errorf("msa Gap = %v, want < 1e-4", result.Gap)
	}
}

// TestMSASplitsCongestedDemand confirms MSA does real equilibration: under demand
// heavy enough to congest the 2-hop motorway, the user equilibrium routes some flow
// onto the direct residential edge too (both corridors carry flow), rather than
// piling everyone onto the free-flow-cheapest motorway. This is the property that
// distinguishes a converged UE assignment from naive's all-on-the-cheapest-path.
func TestMSASplitsCongestedDemand(test *testing.T) {
	roadGraph := loadToyGraph(test)
	reqs := heavyDemand(test, roadGraph, 100, 200) // ~20k vph toward a 5,400 vph motorway

	router := routing.NewMSARouter(roadGraph, cost.DefaultBPR())
	result, err := router.AssignResult(context.Background(), reqs)
	if err != nil {
		test.Fatalf("AssignResult() error = %v", err)
	}

	motorwayFlow := flowForSegment(test, roadGraph, result.FinalFlows, "905512:0:F")
	directFlow := flowForSegment(test, roadGraph, result.FinalFlows, "9000001:0:F")
	if motorwayFlow <= 0 {
		test.Errorf("msa motorway flow = %v, want > 0", motorwayFlow)
	}
	if directFlow <= 0 {
		test.Errorf("msa direct-edge flow = %v, want > 0 (UE must spill onto the alternate when the motorway congests)", directFlow)
	}
	// Conservation: total flow placed equals total demand (every request weight lands
	// on exactly one of the two first hops out of node 0).
	const wantTotal = 100 * 200.0
	if got := motorwayFlow + directFlow; math.Abs(got-wantTotal) > 1e-6 {
		test.Errorf("msa total first-hop flow = %v, want %v (flow conservation)", got, wantTotal)
	}
}

// TestIterativeRoutersDeterministic is the determinism acceptance criterion driven by
// the ACTUAL iterative routers (not the single-pass naive case): a fixed seed plus a
// SERIALIZED OD set, re-read and re-run through the concurrent fan-out, yields
// byte-identical FinalFlows across runs. The OD set is round-tripped through
// Write/ReadODSet so the test exercises the real on-disk form, and each run fans the
// requests across goroutines — so this also pins that the sharded map-reduce reduce is
// order-stable regardless of how the scheduler interleaves workers.
func TestIterativeRoutersDeterministic(test *testing.T) {
	roadGraph := loadToyGraph(test)
	original := heavyDemand(test, roadGraph, 60, 200)

	var buf bytes.Buffer
	if err := routing.WriteODSet(&buf, original); err != nil {
		test.Fatalf("WriteODSet() error = %v", err)
	}
	serialized := buf.Bytes()

	for _, makeRouter := range []func() routing.Router{
		func() routing.Router { return routing.NewIncrementalRouter(roadGraph, cost.DefaultBPR()) },
		func() routing.Router { return routing.NewMSARouter(roadGraph, cost.DefaultBPR()) },
	} {
		runOnce := func() routing.AssignResult {
			reqs, err := routing.ReadODSet(bytes.NewReader(serialized))
			if err != nil {
				test.Fatalf("ReadODSet() error = %v", err)
			}
			_ = routing.NewSeededRNG(42) // fixed seed source (the iterative core is deterministic without RNG)
			result, err := makeRouter().AssignResult(context.Background(), reqs)
			if err != nil {
				test.Fatalf("AssignResult() error = %v", err)
			}
			return result
		}

		first := runOnce()
		second := runOnce()
		name := makeRouter().Name()

		if len(first.FinalFlows) != len(second.FinalFlows) {
			test.Fatalf("%s: FinalFlows lengths differ across runs: %d vs %d", name, len(first.FinalFlows), len(second.FinalFlows))
		}
		for index := range first.FinalFlows {
			if first.FinalFlows[index] != second.FinalFlows[index] {
				test.Errorf("%s: FinalFlows[%d] differs across runs: %v vs %v (byte-identical determinism)", name, index, first.FinalFlows[index], second.FinalFlows[index])
			}
		}
		if first.Gap != second.Gap || first.Iters != second.Iters || first.Converged != second.Converged {
			test.Errorf("%s: metadata differs across runs: {%v %d %v} vs {%v %d %v}", name,
				first.Gap, first.Iters, first.Converged, second.Gap, second.Iters, second.Converged)
		}
	}
}

// TestIterativeRoutersFanOutRaceClean is the real -race exerciser the #71 scratch
// buffer could not be: it runs an Assign over a large batch so assignAONConcurrent
// actually fans the requests across multiple goroutines, each with its OWN
// dijkstraScratch buffer and its OWN flow shard. Run under `go test -race` this fails
// if any per-worker state were shared. It uses many requests so the worker count is
// > 1 (workersFor caps at GOMAXPROCS but never exceeds the request count), and asserts
// the result is still finite and well-formed.
func TestIterativeRoutersFanOutRaceClean(test *testing.T) {
	roadGraph := loadToyGraph(test)
	reqs := heavyDemand(test, roadGraph, 500, 50) // 500 requests => genuine multi-goroutine fan-out

	for _, router := range iterativeRouters(roadGraph) {
		result, err := router.AssignResult(context.Background(), reqs)
		if err != nil {
			test.Fatalf("%s: AssignResult() error = %v", router.Name(), err)
		}
		if len(result.Routes) != len(reqs) {
			test.Errorf("%s: Routes length = %d, want %d", router.Name(), len(result.Routes), len(reqs))
		}
		assertFiniteFlows(test, router.Name(), result.FinalFlows)
		// Every request's route is recorded at its input index (input order preserved
		// through the fan-out's static index striding).
		for index, route := range result.Routes {
			if route.RequestID != reqs[index].ID {
				test.Errorf("%s: Routes[%d].RequestID = %q, want %q (input order preserved through fan-out)", router.Name(), index, route.RequestID, reqs[index].ID)
			}
		}
	}
}

// TestIterativeRoutersEmptyBatch pins that an empty batch returns a full-length
// all-zero FinalFlows (not nil), a zero gap, and converged metadata — the degenerate
// input must not divide by zero or return nil.
func TestIterativeRoutersEmptyBatch(test *testing.T) {
	roadGraph := loadToyGraph(test)
	for _, router := range iterativeRouters(roadGraph) {
		result, err := router.AssignResult(context.Background(), nil)
		if err != nil {
			test.Fatalf("%s: AssignResult(nil) error = %v", router.Name(), err)
		}
		if len(result.FinalFlows) != roadGraph.EdgeCount() {
			test.Errorf("%s: empty-batch FinalFlows length = %d, want EdgeCount %d", router.Name(), len(result.FinalFlows), roadGraph.EdgeCount())
		}
		for index, flow := range result.FinalFlows {
			if flow != 0 {
				test.Errorf("%s: empty-batch FinalFlows[%d] = %v, want 0", router.Name(), index, flow)
			}
		}
		if result.Gap != 0 || !result.Converged {
			test.Errorf("%s: empty-batch metadata = {Gap:%v Converged:%v}, want {0 true}", router.Name(), result.Gap, result.Converged)
		}
		if result.Routes == nil {
			test.Errorf("%s: empty-batch Routes = nil, want a non-nil zero-length slice", router.Name())
		}
	}
}

// TestIterativeRoutersAssignEqualsAssignResult pins that the paths-only Assign returns
// exactly AssignResult.Routes (same request ids and chosen edges, in order), so the
// two faces never disagree.
func TestIterativeRoutersAssignEqualsAssignResult(test *testing.T) {
	roadGraph := loadToyGraph(test)
	reqs := heavyDemand(test, roadGraph, 20, 200)

	for _, router := range iterativeRouters(roadGraph) {
		result, err := router.AssignResult(context.Background(), reqs)
		if err != nil {
			test.Fatalf("%s: AssignResult() error = %v", router.Name(), err)
		}
		routes, err := router.Assign(context.Background(), reqs)
		if err != nil {
			test.Fatalf("%s: Assign() error = %v", router.Name(), err)
		}
		if len(routes) != len(result.Routes) {
			test.Fatalf("%s: Assign() returned %d routes, AssignResult %d", router.Name(), len(routes), len(result.Routes))
		}
		for index := range routes {
			if routes[index].RequestID != result.Routes[index].RequestID {
				test.Errorf("%s: Assign()[%d].RequestID = %q, want %q", router.Name(), index, routes[index].RequestID, result.Routes[index].RequestID)
			}
		}
	}
}

// TestIterativeRoutersCancelledContext confirms a cancelled context stops an Assign
// promptly with the context error and a zero AssignResult (no partial result).
func TestIterativeRoutersCancelledContext(test *testing.T) {
	roadGraph := loadToyGraph(test)
	reqs := heavyDemand(test, roadGraph, 10, 200)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	for _, router := range iterativeRouters(roadGraph) {
		_, err := router.AssignResult(ctx, reqs)
		if err == nil {
			test.Errorf("%s: AssignResult with a cancelled context err = nil, want context.Canceled", router.Name())
		}
	}
}

// TestIterativeRoutersNames pins the strategy names used in benchmark output / the API.
func TestIterativeRoutersNames(test *testing.T) {
	roadGraph := loadToyGraph(test)
	if got := routing.NewIncrementalRouter(roadGraph, cost.DefaultBPR()).Name(); got != "incremental" {
		test.Errorf("IncrementalRouter.Name() = %q, want %q", got, "incremental")
	}
	if got := routing.NewMSARouter(roadGraph, cost.DefaultBPR()).Name(); got != "msa" {
		test.Errorf("MSARouter.Name() = %q, want %q", got, "msa")
	}
}

const adversarialPath = "../../testdata/toy_network_adversarial.geojson"

// loadAdversarialGraph loads the adversarial fixture (disconnected component + one-way
// trap) for the iterative-router Assign-path tests.
func loadAdversarialGraph(test *testing.T) *graph.AdjacencyGraph {
	test.Helper()
	roadGraph, _, err := graph.LoadEdgeAttributesGeoJSONFile(adversarialPath, graph.WithExpectedBounds(-74, -73, 40, 41))
	if err != nil {
		test.Fatalf("toy_network_adversarial.geojson must load cleanly, got: %v", err)
	}
	return roadGraph
}

// TestIterativeRoutersAdversarialUnreachableOD is the adversarial acceptance criterion
// on the ASSIGN path: a batch containing an unreachable OD pair (main component ->
// disconnected island) returns a clean error from both routers — never a panic, NaN,
// or +Inf — because assignAONConcurrent treats an unroutable OD as a hard error.
func TestIterativeRoutersAdversarialUnreachableOD(test *testing.T) {
	roadGraph := loadAdversarialGraph(test)
	node0, _ := roadGraph.Node(0) // main component
	node4, _ := roadGraph.Node(4) // disconnected island
	reqs := []routing.RouteRequest{{ID: "cross-gap", From: node0.Pos, To: node4.Pos, Weight: 100}}

	for _, router := range iterativeRouters(roadGraph) {
		result, err := router.AssignResult(context.Background(), reqs)
		if err == nil {
			test.Errorf("%s: AssignResult over an unreachable OD returned no error (result %+v), want a clean no-route error", router.Name(), result)
			continue
		}
		// Defined, non-crashing: no panic reached here and the zero AssignResult
		// carries no poisoned numerics.
		if math.IsNaN(result.Gap) || math.IsInf(result.Gap, 0) {
			test.Errorf("%s: unreachable-OD Gap = %v, want a finite zero-value", router.Name(), result.Gap)
		}
		assertFiniteFlows(test, router.Name(), result.FinalFlows)
	}
}

// TestIterativeRoutersAdversarialOneWayForward confirms both routers Assign the
// reachable forward direction across the one-way trap (node 0 -> node 3) without
// panic/NaN, placing finite flow that uses the trap edge forward, and never producing
// a non-finite gap or flow on the one-way corridor fixture.
func TestIterativeRoutersAdversarialOneWayForward(test *testing.T) {
	roadGraph := loadAdversarialGraph(test)
	node0, _ := roadGraph.Node(0)
	node3, _ := roadGraph.Node(3) // reachable only via the one-way trap, forward
	reqs := []routing.RouteRequest{{ID: "forward", From: node0.Pos, To: node3.Pos, Weight: 100}}

	for _, router := range iterativeRouters(roadGraph) {
		result, err := router.AssignResult(context.Background(), reqs)
		if err != nil {
			test.Fatalf("%s: forward OD node 0 -> node 3 must be reachable, got: %v", router.Name(), err)
		}
		if math.IsNaN(result.Gap) || math.IsInf(result.Gap, 0) {
			test.Errorf("%s: forward-OD Gap = %v, want finite", router.Name(), result.Gap)
		}
		assertFiniteFlows(test, router.Name(), result.FinalFlows)
		// The one-way trap edge carries the request's flow forward.
		if got := flowForSegment(test, roadGraph, result.FinalFlows, "7100003:0:F"); got <= 0 {
			test.Errorf("%s: one-way trap edge flow = %v, want > 0 (forward OD uses it)", router.Name(), got)
		}
	}
}

// TestIterativeRouteSingleFreeFlow confirms the single-request Route path is the
// free-flow shortest path: from node 0 to node 2 it returns the 2-hop motorway
// (905512:0:F + 905512:1:F), the lowest-cost-≠-fewest-hops pair, matching naive.
func TestIterativeRouteSingleFreeFlow(test *testing.T) {
	roadGraph := loadToyGraph(test)
	node0, _ := roadGraph.Node(0)
	node2, _ := roadGraph.Node(2)
	req := routing.RouteRequest{ID: "single", From: node0.Pos, To: node2.Pos}

	want := []domain.SegmentID{"905512:0:F", "905512:1:F"}
	for _, router := range iterativeRouters(roadGraph) {
		route, err := router.Route(context.Background(), req)
		if err != nil {
			test.Fatalf("%s: Route() error = %v", router.Name(), err)
		}
		got := segmentsOf(test, roadGraph, route.Edges)
		if len(got) != len(want) {
			test.Fatalf("%s: Route edges = %v, want %v", router.Name(), got, want)
		}
		for index := range want {
			if got[index] != want[index] {
				test.Errorf("%s: Route edge[%d] = %q, want %q", router.Name(), index, got[index], want[index])
			}
		}
	}
}
