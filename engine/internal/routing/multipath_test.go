package routing_test

import (
	"context"
	"errors"
	"fmt"
	"math"
	"reflect"
	"testing"

	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/cost"
	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/domain"
	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/graph"
	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/routing"
)

// multipathReqs builds n identical OD requests from node 0 to node 2 (the toy
// fork: a fast 2-hop motorway and a slow direct residential), with stable ids so
// the per-request seeding is reproducible.
func multipathReqs(test *testing.T, roadGraph *graph.AdjacencyGraph, n int) []routing.RouteRequest {
	test.Helper()
	origin, _ := roadGraph.Node(0)
	dest, _ := roadGraph.Node(2)
	reqs := make([]routing.RouteRequest, n)
	for index := range reqs {
		reqs[index] = routing.RouteRequest{
			ID:   fmt.Sprintf("req-%d", index),
			From: origin.Pos,
			To:   dest.Pos,
		}
	}
	return reqs
}

// TestMultipathSatisfiesRouterPort asserts MultipathRouter is a drop-in Router and
// its AssignResult matches the shared shape.
func TestMultipathSatisfiesRouterPort(test *testing.T) {
	roadGraph := loadToyGraph(test)
	var router routing.Router = routing.NewMultipathRouter(roadGraph, cost.DefaultBPR(), 42, 3)
	if router.Name() != "multipath" {
		test.Errorf("Name() = %q, want multipath", router.Name())
	}
	reqs := multipathReqs(test, roadGraph, 4)
	res, err := router.AssignResult(context.Background(), reqs)
	if err != nil {
		test.Fatalf("AssignResult error = %v", err)
	}
	if len(res.Routes) != len(reqs) {
		test.Fatalf("got %d routes, want %d", len(res.Routes), len(reqs))
	}
	if res.Iters != 1 || !res.Converged {
		test.Errorf("single-pass metadata wrong: Iters=%d Converged=%v", res.Iters, res.Converged)
	}
	if len(res.FinalFlows) != roadGraph.EdgeCount() {
		test.Errorf("FinalFlows len = %d, want EdgeCount %d", len(res.FinalFlows), roadGraph.EdgeCount())
	}
}

// TestMultipathDeterminismByteIdentical is the headline acceptance: a fixed seed
// produces a BYTE-IDENTICAL split across runs. We run AssignMultipath twice and
// require the chosen-path provenance, the routes, and the final flows to match
// exactly — the determinism guarantee that depends on PER-REQUEST seeding.
func TestMultipathDeterminismByteIdentical(test *testing.T) {
	roadGraph := loadToyGraph(test)
	router := routing.NewMultipathRouter(roadGraph, cost.DefaultBPR(), 12345, 3)
	reqs := multipathReqs(test, roadGraph, 50)

	first, err := router.AssignMultipath(context.Background(), reqs)
	if err != nil {
		test.Fatalf("first AssignMultipath error = %v", err)
	}
	second, err := router.AssignMultipath(context.Background(), reqs)
	if err != nil {
		test.Fatalf("second AssignMultipath error = %v", err)
	}

	if !reflect.DeepEqual(first.Provenance.ChosenPathIndex, second.Provenance.ChosenPathIndex) {
		test.Errorf("ChosenPathIndex differs across runs:\n%v\n%v",
			first.Provenance.ChosenPathIndex, second.Provenance.ChosenPathIndex)
	}
	if !reflect.DeepEqual(first.Routes, second.Routes) {
		test.Errorf("Routes differ across runs")
	}
	if !reflect.DeepEqual(first.FinalFlows, second.FinalFlows) {
		test.Errorf("FinalFlows differ across runs")
	}
	if !reflect.DeepEqual(first.Provenance.ODPaths, second.Provenance.ODPaths) {
		test.Errorf("ODPaths provenance differs across runs")
	}
}

// TestMultipathSplitSpreadsDemand asserts the split actually uses MORE than one of
// the K paths — the whole point of multipath. With 50 requests over an OD with two
// paths and a fixed seed, both paths must draw at least one request (a degenerate
// all-on-one split would defeat demand spreading).
func TestMultipathSplitSpreadsDemand(test *testing.T) {
	roadGraph := loadToyGraph(test)
	router := routing.NewMultipathRouter(roadGraph, cost.DefaultBPR(), 7, 3)
	reqs := multipathReqs(test, roadGraph, 50)

	res, err := router.AssignMultipath(context.Background(), reqs)
	if err != nil {
		test.Fatalf("AssignMultipath error = %v", err)
	}
	if len(res.Provenance.ODPaths) != 1 {
		test.Fatalf("want 1 distinct OD, got %d", len(res.Provenance.ODPaths))
	}
	od := res.Provenance.ODPaths[0]
	if len(od.Paths) != 2 {
		test.Fatalf("want 2 K-paths for OD 0->2, got %d", len(od.Paths))
	}
	used := 0
	for _, realized := range od.Realized {
		if realized > 0 {
			used++
		}
	}
	if used < 2 {
		test.Errorf("split used only %d of %d paths; expected demand spread across both (realized=%v)",
			used, len(od.Paths), od.Realized)
	}
}

// TestMultipathProvenanceRetrievable asserts the provenance is the documented
// adjunct shape and is internally consistent — request→path index, per-OD K paths,
// and intended/realized proportions are directly retrievable, NOT reconstructed by
// deduping edge sequences.
func TestMultipathProvenanceRetrievable(test *testing.T) {
	roadGraph := loadToyGraph(test)
	router := routing.NewMultipathRouter(roadGraph, cost.DefaultBPR(), 99, 3)
	reqs := multipathReqs(test, roadGraph, 20)

	res, err := router.AssignMultipath(context.Background(), reqs)
	if err != nil {
		test.Fatalf("AssignMultipath error = %v", err)
	}
	prov := res.Provenance

	if len(prov.ChosenPathIndex) != len(reqs) || len(prov.ChosenODIndex) != len(reqs) {
		test.Fatalf("per-request provenance length mismatch: chosen=%d odIndex=%d reqs=%d",
			len(prov.ChosenPathIndex), len(prov.ChosenODIndex), len(reqs))
	}

	// Intended sums to ~1 per OD; realized sums to ~1; each chosen path matches the
	// route's edges (provenance and routes agree without deduping).
	for slot, od := range prov.ODPaths {
		if sum := sumFloats(od.Intended); math.Abs(sum-1.0) > 1e-9 {
			test.Errorf("OD %d Intended sums to %.6f, want 1.0", slot, sum)
		}
		if sum := sumFloats(od.Realized); math.Abs(sum-1.0) > 1e-9 {
			test.Errorf("OD %d Realized sums to %.6f, want 1.0", slot, sum)
		}
		// Cheaper path gets more intended mass (path 0 is cheapest).
		if len(od.Intended) >= 2 && od.Intended[0] <= od.Intended[1] {
			test.Errorf("OD %d: cheaper path 0 intended %.4f not > path 1 intended %.4f",
				slot, od.Intended[0], od.Intended[1])
		}
	}

	// Every request's route edges equal its chosen K-path's edges — provenance is
	// load-bearing, not reconstructed.
	for index, route := range res.Routes {
		od := prov.ODPaths[prov.ChosenODIndex[index]]
		chosen := od.Paths[prov.ChosenPathIndex[index]]
		if !reflect.DeepEqual([]domain.EdgeID(route.Edges), []domain.EdgeID(chosen.Edges)) {
			test.Errorf("request %d route edges %v != provenance chosen edges %v",
				index, route.Edges, chosen.Edges)
		}
		if route.RequestID != reqs[index].ID {
			test.Errorf("request %d RequestID = %q, want %q", index, route.RequestID, reqs[index].ID)
		}
	}
}

// TestMultipathRealizedConvergesToIntended asserts realized proportions approach
// intended as demand grows — the law-of-large-numbers sanity check on the
// probabilistic split.
func TestMultipathRealizedConvergesToIntended(test *testing.T) {
	roadGraph := loadToyGraph(test)
	router := routing.NewMultipathRouter(roadGraph, cost.DefaultBPR(), 2024, 3)
	reqs := multipathReqs(test, roadGraph, 2000)

	res, err := router.AssignMultipath(context.Background(), reqs)
	if err != nil {
		test.Fatalf("AssignMultipath error = %v", err)
	}
	od := res.Provenance.ODPaths[0]
	for index := range od.Intended {
		if diff := math.Abs(od.Intended[index] - od.Realized[index]); diff > 0.05 {
			test.Errorf("path %d: realized %.4f far from intended %.4f (diff %.4f) at n=2000",
				index, od.Realized[index], od.Intended[index], diff)
		}
	}
}

// TestMultipathRequestIDPreserved asserts RequestID survives Assign for every
// route, in input order, so the frontend can pair routes back to OD pairs.
func TestMultipathRequestIDPreserved(test *testing.T) {
	roadGraph := loadToyGraph(test)
	router := routing.NewMultipathRouter(roadGraph, cost.DefaultBPR(), 1, 3)
	reqs := multipathReqs(test, roadGraph, 10)

	routes, err := router.Assign(context.Background(), reqs)
	if err != nil {
		test.Fatalf("Assign error = %v", err)
	}
	for index, route := range routes {
		if route.RequestID != reqs[index].ID {
			test.Errorf("routes[%d].RequestID = %q, want %q", index, route.RequestID, reqs[index].ID)
		}
	}
}

// TestMultipathChosenVectorGolden pins the EXACT chosen-path vector for a fixed
// (seed, batch) against a hard-coded golden. Running AssignMultipath twice in one
// process only proves intra-process repeatability; a change to the seed SOURCE
// (e.g. seeding requestRNG from the wall clock) would still self-agree across two
// same-process calls yet diverge from this baked vector — so the golden is the
// trap that actually catches such a regression.
func TestMultipathChosenVectorGolden(test *testing.T) {
	roadGraph := loadToyGraph(test)
	router := routing.NewMultipathRouter(roadGraph, cost.DefaultBPR(), 12345, 3)
	reqs := multipathReqs(test, roadGraph, 10)

	res, err := router.AssignMultipath(context.Background(), reqs)
	if err != nil {
		test.Fatalf("AssignMultipath error = %v", err)
	}

	// Golden vector for seed=12345, k=3, 10 requests over OD 0->2 (two K-paths) on
	// the toy fixture. Regenerate ONLY if the split rule or seed source changes
	// intentionally — an unexplained change here is a determinism regression.
	want := []int{1, 0, 1, 0, 0, 0, 0, 0, 0, 1}
	if !reflect.DeepEqual(res.Provenance.ChosenPathIndex, want) {
		test.Errorf("ChosenPathIndex = %v, want golden %v (a diff here means the seed source or split rule changed)",
			res.Provenance.ChosenPathIndex, want)
	}
}

// TestMultipathHonorsContextCancellation asserts a cancelled context aborts the
// batch with the context error rather than routing — exercising the per-request
// ctx.Err() check in AssignMultipath (and the AssignResult face over it). Mirrors
// the reactive router's cancellation test.
func TestMultipathHonorsContextCancellation(test *testing.T) {
	roadGraph := loadToyGraph(test)
	router := routing.NewMultipathRouter(roadGraph, cost.DefaultBPR(), 42, 3)
	reqs := multipathReqs(test, roadGraph, 8)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancel: the first per-request check must trip

	if _, err := router.AssignMultipath(ctx, reqs); !errors.Is(err, context.Canceled) {
		test.Errorf("AssignMultipath with a cancelled context: err = %v, want context.Canceled", err)
	}
	if _, err := router.AssignResult(ctx, reqs); !errors.Is(err, context.Canceled) {
		test.Errorf("AssignResult with a cancelled context: err = %v, want context.Canceled", err)
	}
}

func sumFloats(values []float64) float64 {
	total := 0.0
	for _, value := range values {
		total += value
	}
	return total
}
