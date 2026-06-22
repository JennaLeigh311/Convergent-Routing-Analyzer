package benchmark

import (
	"math"
	"testing"

	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/congestion"
	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/cost"
	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/domain"
	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/graph"
)

// TestAdvanceStepsOverDegenerateEdges is the direct unit test of advance's two
// degenerate-edge step-over guards — the bounded-run property that keeps a single
// vehicle from stalling or spinning on an edge it cannot traverse. advance is
// unexported, so this test lives in the internal package. It builds a path whose
// edges deliberately hit, in order:
//
//  1. the !ok branch     — an edge id not present in the graph (out of range);
//  2. the LengthM<=0 branch — a zero-length edge (graph.New does not reject one);
//  3. the speed<=0 branch — a positive-length edge whose load is so large the BPR
//     cost overflows to +Inf, so LengthM/Cost == 0;
//  4. a normal edge      — proving advance keeps moving past the stepped-over edges
//     and accrues real travel time only on the traversable one.
//
// It asserts the vehicle completes (never stalls/loops), and that the experienced
// time equals ONLY the normal edge's free-flow traversal — every degenerate edge
// is stepped over at zero cost.
func TestAdvanceStepsOverDegenerateEdges(t *testing.T) {
	bpr := cost.DefaultBPR()

	// A 3-node graph with three real edges. Edge 0 is zero-length (LengthM<=0 guard);
	// edge 1 is a congestible positive-length edge we'll force to infinite cost
	// (speed<=0 guard); edge 2 is a normal edge that takes real time to traverse.
	nodes := []graph.Node{
		{ID: 0, Pos: domain.LatLon{Lat: 0, Lon: 0}},
		{ID: 1, Pos: domain.LatLon{Lat: 0, Lon: 1}},
		{ID: 2, Pos: domain.LatLon{Lat: 0, Lon: 2}},
	}
	edges := []graph.Edge{
		{ID: 0, Segment: "zero", From: 0, To: 1, LengthM: 0, FreeFlowS: 0, CapacityVPH: 100},
		{ID: 1, Segment: "inf", From: 1, To: 2, LengthM: 1000, FreeFlowS: 100, CapacityVPH: 100},
		{ID: 2, Segment: "normal", From: 1, To: 2, LengthM: 100, FreeFlowS: 10, CapacityVPH: 100},
	}
	g, err := graph.New(nodes, edges)
	if err != nil {
		t.Fatalf("graph.New() error = %v", err)
	}

	const tickSeconds = 30.0

	// Load store sized to the graph, with edge 1 driven to an enormous load so its
	// BPR cost overflows to +Inf and its derived speed is 0 (the speed<=0 guard).
	load := congestion.NewLoadStore(g.EdgeCount())
	load.Set(1, math.MaxFloat64)
	if c := bpr.Cost(edges[1], load.Load(1)); !math.IsInf(c, 1) {
		t.Fatalf("precondition: edge 1 cost must be +Inf to reach speed<=0 guard, got %v", c)
	}

	// Path: unknown edge id 99 (!ok), then edge 0 (LengthM<=0), then edge 1
	// (speed<=0), then edge 2 (a real, traversable edge).
	const unknownEdgeID = domain.EdgeID(99)
	veh := &vehicle{
		requestIndex: 0,
		weight:       1,
		edges:        []domain.EdgeID{unknownEdgeID, 0, 1, 2},
	}

	// One tick of 30s is far more than the normal edge's 10s free-flow time, so the
	// whole path resolves within this single advance call — and it must terminate.
	advance(veh, g, bpr, load, tickSeconds)

	if !veh.done {
		t.Fatalf("vehicle must complete after stepping over the degenerate edges, done=%v edgeIndex=%d", veh.done, veh.edgeIndex)
	}
	if veh.edgeIndex != len(veh.edges) {
		t.Errorf("edgeIndex = %d, want %d (past the last edge)", veh.edgeIndex, len(veh.edges))
	}
	// Only edge 2 contributes travel time: 100 m at free-flow 10 s (load 0 ⇒ BPR
	// cost == FreeFlowS), so experiencedS must be exactly 10 s. The three degenerate
	// edges are stepped over at zero cost.
	const wantExperienced = 10.0
	if math.Abs(veh.experiencedS-wantExperienced) > 1e-9 {
		t.Errorf("experiencedS = %v, want %v (only the normal edge accrues time; degenerate edges are 0)", veh.experiencedS, wantExperienced)
	}

	// Calling advance again on a done vehicle is a no-op (no stall, no extra time).
	before := veh.experiencedS
	advance(veh, g, bpr, load, tickSeconds)
	if !veh.done || veh.experiencedS != before {
		t.Errorf("advance on a done vehicle must be a no-op: done=%v experiencedS=%v (was %v)", veh.done, veh.experiencedS, before)
	}
}
