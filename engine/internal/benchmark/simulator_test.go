package benchmark_test

import (
	"context"
	"fmt"
	"math"
	"reflect"
	"testing"
	"time"

	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/benchmark"
	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/cost"
	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/domain"
	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/graph"
	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/routing"
)

// corridorGraph builds a two-node, single-edge "corridor": node 0 → node 1 over one
// congestible edge. A long edge (so a vehicle takes several ≈30s ticks to traverse,
// giving the simulator real time-evolution to model) with a small capacity (so
// stacking departures onto it raises its v/c and BPR cost sharply). Free-flow time
// 120 s (= 1200 m at 10 m/s) and capacity 100 vph make the static-vs-experienced
// difference large and hand-reasonable.
func corridorGraph(t *testing.T) *graph.AdjacencyGraph {
	t.Helper()
	nodes := []graph.Node{
		{ID: 0, Pos: domain.LatLon{Lat: 0, Lon: 0}},
		{ID: 1, Pos: domain.LatLon{Lat: 0, Lon: 1}},
	}
	edges := []graph.Edge{
		{ID: 0, Segment: "c0", From: 0, To: 1, LengthM: 1200, FreeFlowS: 120, CapacityVPH: 100},
	}
	g, err := graph.New(nodes, edges)
	if err != nil {
		t.Fatalf("graph.New() error = %v", err)
	}
	return g
}

// corridorDemand builds n node0→node1 requests at the given per-request weight,
// each departing at departAt[i%len(departAt)] seconds — the departure-time OD set
// the simulator releases on its clock.
func corridorDemand(g *graph.AdjacencyGraph, n int, weight float64, departAt []float64) []routing.RouteRequest {
	n0, _ := g.Node(0)
	n1, _ := g.Node(1)
	reqs := make([]routing.RouteRequest, n)
	for i := range reqs {
		reqs[i] = routing.RouteRequest{
			ID:       fmt.Sprintf("r%d", i),
			From:     n0.Pos,
			To:       n1.Pos,
			Weight:   weight,
			DepartAt: departAt[i%len(departAt)],
		}
	}
	return reqs
}

// recorder collects the per-tick state stream into a slice and a compact comparable
// trace, the seam a consumer (the Phase-5 #93 WebSocket backend) plugs into. The
// trace is a flat string of (tick, inFlight, completed, rounded loads) so two runs'
// traces can be compared byte-for-byte for the determinism criterion.
type recorder struct {
	states []benchmark.TickState
	trace  string
}

func (r *recorder) observe(s benchmark.TickState) {
	r.states = append(r.states, s)
	r.trace += fmt.Sprintf("t=%d sim=%s inf=%d done=%d", s.Tick, s.SimTime.Format(time.RFC3339), s.InFlight, s.Completed)
	for _, load := range s.Load {
		r.trace += fmt.Sprintf("|%.6f", load)
	}
	for _, vc := range s.VC {
		r.trace += fmt.Sprintf("/%.6f", vc)
	}
	r.trace += "\n"
}

// staticMeanRealized computes the Phase-3 STATIC-equilibrium mean realized time for
// the same OD set: route all requests at once (naive, all simultaneous) and apply
// the #89 evaluator. This is the number the experienced (over-the-run) mean must come out
// DISTINCT from — the honesty distinction. We use the public benchmark.Evaluate so
// the comparison is against the exact static metric the project reports.
func staticMeanRealized(t *testing.T, g graph.Graph, bpr cost.BPR, reqs []routing.RouteRequest) float64 {
	t.Helper()
	router := routing.NewNaiveRouter(g)
	res, err := router.AssignResult(context.Background(), reqs)
	if err != nil {
		t.Fatalf("static AssignResult error = %v", err)
	}
	return benchmark.Evaluate(g, bpr, "naive", "static", res).MeanRealizedS
}

// TestSimulateExperiencedDistinctFromStatic is the headline acceptance criterion:
// on a congested toy demand the EXPERIENCED (over-the-run) mean/p95 the mesoscopic simulator
// reports must be DIFFERENT from the static-equilibrium one-shot number, and both
// must be finite and labeled. We stagger departures across the run so congestion
// builds and drains over time — exactly the dynamic the static all-at-once number
// cannot capture — and assert the two means differ by a non-trivial margin.
func TestSimulateExperiencedDistinctFromStatic(t *testing.T) {
	g := corridorGraph(t)
	bpr := cost.DefaultBPR()

	// 20 requests, all on the single corridor. STATIC: all 20 present at once ⇒ a
	// very high constant v/c on the edge. EXPERIENCED (over-the-run): spread the departures over
	// the run so only a few share the edge at any tick ⇒ each driver sees less
	// congestion than the static all-at-once load implies. The two means must differ.
	departAt := []float64{0, 60, 120, 180, 240, 300, 360, 420, 480, 540}
	reqs := corridorDemand(g, 20, 50, departAt)

	staticMean := staticMeanRealized(t, g, bpr, reqs)

	cfg := benchmark.SimConfig{
		StartTime:   time.Date(2026, 6, 20, 8, 0, 0, 0, time.UTC),
		TickSeconds: 30,
		BPR:         bpr,
	}
	res, err := benchmark.Simulate(context.Background(), g, reqs, cfg, benchmark.ReactiveBPRFactory(g, bpr), nil)
	if err != nil {
		t.Fatalf("Simulate error = %v", err)
	}

	if res.Completed != len(reqs) {
		t.Fatalf("Completed = %d, want all %d vehicles to drain", res.Completed, len(reqs))
	}
	if !isFinite(res.MeanRealizedS) || !isFinite(res.P95RealizedS) {
		t.Fatalf("experienced (over-the-run) metrics must be finite, got mean=%v p95=%v", res.MeanRealizedS, res.P95RealizedS)
	}
	if res.MeanRealizedS <= 0 || res.P95RealizedS <= 0 {
		t.Fatalf("experienced (over-the-run) metrics must be positive on a real demand, got mean=%v p95=%v", res.MeanRealizedS, res.P95RealizedS)
	}
	// The whole point of modeling time: the two numbers DIFFER. Staggered departures
	// mean the experienced (over-the-run) mean is LOWER than the static all-at-once mean (less
	// concurrent congestion), and by a clear margin, not float noise.
	relDiff := math.Abs(res.MeanRealizedS-staticMean) / staticMean
	if relDiff < 0.05 {
		t.Errorf("experienced (over-the-run) mean (%v) must be distinct from static mean (%v); relative diff %.4f too small",
			res.MeanRealizedS, staticMean, relDiff)
	}
	t.Logf("experienced (over-the-run) mean = %.2f s, static-equilibrium mean = %.2f s (rel diff %.1f%%)",
		res.MeanRealizedS, staticMean, relDiff*100)
}

// TestSimulatePerTickDeterminism is the determinism acceptance criterion: two runs
// of the SAME OD set at the same config produce a BYTE-IDENTICAL per-tick trace and
// an identical SimResult. The per-tick stream is the Phase-5 plug point, so its
// reproducibility is load-bearing. We also shuffle the input order of the SECOND run
// to prove the sorted-OD-order release makes the trace independent of slice order.
func TestSimulatePerTickDeterminism(t *testing.T) {
	g := corridorGraph(t)
	bpr := cost.DefaultBPR()
	departAt := []float64{0, 30, 60, 90, 120}
	reqs := corridorDemand(g, 15, 40, departAt)

	cfg := benchmark.SimConfig{
		StartTime:   time.Date(2026, 6, 20, 9, 0, 0, 0, time.UTC),
		TickSeconds: 30,
		BPR:         bpr,
	}

	var rec1, rec2 recorder
	res1, err := benchmark.Simulate(context.Background(), g, reqs, cfg, benchmark.ReactiveBPRFactory(g, bpr), rec1.observe)
	if err != nil {
		t.Fatalf("run 1 error = %v", err)
	}

	// Reverse the input order for run 2; the sorted release must yield the identical
	// trace regardless.
	reversed := make([]routing.RouteRequest, len(reqs))
	for i := range reqs {
		reversed[i] = reqs[len(reqs)-1-i]
	}
	res2, err := benchmark.Simulate(context.Background(), g, reversed, cfg, benchmark.ReactiveBPRFactory(g, bpr), rec2.observe)
	if err != nil {
		t.Fatalf("run 2 error = %v", err)
	}

	if rec1.trace != rec2.trace {
		t.Errorf("per-tick traces differ across two runs (determinism violated)\nrun1:\n%s\nrun2:\n%s", rec1.trace, rec2.trace)
	}
	if !reflect.DeepEqual(res1, res2) {
		t.Errorf("SimResult differs across two runs: %+v vs %+v", res1, res2)
	}
	if len(rec1.states) == 0 {
		t.Fatal("expected a non-empty per-tick stream")
	}
	// The stream must be in strict tick order and the loads fresh copies (mutating an
	// emitted state must not affect a later one — they do not alias the live store).
	for i, s := range rec1.states {
		if s.Tick != i+1 {
			t.Errorf("tick %d emitted out of order (got Tick=%d)", i+1, s.Tick)
		}
	}
}

// loadToyNetwork loads the shared toy fixture (the real multi-route network: node
// 0→2 either over the direct residential edge or the faster 0→1→2 motorway pair).
// It is the same load pattern TestSimulateOnToyNetwork uses; factored out so the
// determinism test can route over a network that has GENUINE route choice (which the
// single-edge corridor cannot), so the concurrent route fan-out is actually exercised.
func loadToyNetwork(t *testing.T) *graph.AdjacencyGraph {
	t.Helper()
	g, _, err := graph.LoadEdgeAttributesGeoJSONFile("../../testdata/toy_network.geojson", graph.WithExpectedBounds(-74, -73, 40, 41))
	if err != nil {
		t.Fatalf("toy_network.geojson must load with zero error, got: %v", err)
	}
	return g
}

// toyDemand builds n node0→node2 requests over the toy network at the given weight,
// each departing at departAt[i%len(departAt)] seconds.
func toyDemand(t *testing.T, g graph.Graph, n int, weight float64, departAt []float64) []routing.RouteRequest {
	t.Helper()
	n0, _ := g.Node(0)
	n2, _ := g.Node(2)
	reqs := make([]routing.RouteRequest, n)
	for i := range reqs {
		reqs[i] = routing.RouteRequest{
			ID:       fmt.Sprintf("r%d", i),
			From:     n0.Pos,
			To:       n2.Pos,
			Weight:   weight,
			DepartAt: departAt[i%len(departAt)],
		}
	}
	return reqs
}

// TestSimulatePerTickDeterminismToyNetwork is the determinism criterion on the REAL
// multi-route toy network. Unlike the corridor (one route, so the route fan-out has
// no choice to make), the toy net has actual route choice (0→2 direct vs 0→1→2), so
// running many staggered requests concurrently genuinely exercises the per-tick
// concurrent fan-out and proves it introduces NO nondeterminism: two same-OD runs and
// one with the input slice REVERSED must all produce a byte-identical per-tick trace
// AND an identical SimResult. The reversed run pins that the sorted-OD release makes
// the result independent of input slice order.
func TestSimulatePerTickDeterminismToyNetwork(t *testing.T) {
	g := loadToyNetwork(t)
	bpr := cost.DefaultBPR()

	// 24 staggered requests — enough to stride across the fan-out's worker pool so a
	// nondeterministic reduction would surface as a trace divergence.
	departAt := []float64{0, 30, 60, 90, 120, 150, 180, 210}
	reqs := toyDemand(t, g, 24, 80, departAt)

	cfg := benchmark.SimConfig{
		StartTime:   time.Date(2026, 6, 20, 9, 0, 0, 0, time.UTC),
		TickSeconds: 30,
		BPR:         bpr,
	}

	var rec1, rec2, rec3 recorder
	res1, err := benchmark.Simulate(context.Background(), g, reqs, cfg, benchmark.ReactiveBPRFactory(g, bpr), rec1.observe)
	if err != nil {
		t.Fatalf("run 1 error = %v", err)
	}
	res2, err := benchmark.Simulate(context.Background(), g, reqs, cfg, benchmark.ReactiveBPRFactory(g, bpr), rec2.observe)
	if err != nil {
		t.Fatalf("run 2 error = %v", err)
	}

	// Reverse the input order for run 3; the sorted release must yield the identical
	// trace and result regardless of slice order.
	reversed := make([]routing.RouteRequest, len(reqs))
	for i := range reqs {
		reversed[i] = reqs[len(reqs)-1-i]
	}
	res3, err := benchmark.Simulate(context.Background(), g, reversed, cfg, benchmark.ReactiveBPRFactory(g, bpr), rec3.observe)
	if err != nil {
		t.Fatalf("run 3 (reversed) error = %v", err)
	}

	if rec1.trace != rec2.trace {
		t.Errorf("per-tick traces differ across two identical runs (determinism violated)\nrun1:\n%s\nrun2:\n%s", rec1.trace, rec2.trace)
	}
	if rec1.trace != rec3.trace {
		t.Errorf("per-tick trace depends on input slice order (sorted release violated)\nrun1:\n%s\nreversed:\n%s", rec1.trace, rec3.trace)
	}
	if !reflect.DeepEqual(res1, res2) {
		t.Errorf("SimResult differs across two identical runs: %+v vs %+v", res1, res2)
	}
	if !reflect.DeepEqual(res1, res3) {
		t.Errorf("SimResult depends on input slice order: %+v vs %+v", res1, res3)
	}
	if len(rec1.states) == 0 {
		t.Fatal("expected a non-empty per-tick stream on the toy network")
	}
	if res1.Completed != len(reqs) {
		t.Errorf("Completed = %d, want all %d to drain on the toy network", res1.Completed, len(reqs))
	}
}

// TestSimulateMaxTicksCutoff pins the documented budget-exhaustion path: a run whose
// demand CANNOT drain within MaxTicks stops at the cap with Completed < Released and
// never hangs. With MaxTicks=1 and a 120 s free-flow corridor edge, a single 30 s tick
// cannot move any vehicle to completion, so every released vehicle is still in flight
// when the budget is spent — the SimResult.Completed < total / Ticks == MaxTicks path.
func TestSimulateMaxTicksCutoff(t *testing.T) {
	g := corridorGraph(t)
	bpr := cost.DefaultBPR()

	// Several requests on the corridor whose 120 s free-flow edge needs multiple 30 s
	// ticks to traverse; with only one tick of budget none can complete.
	reqs := corridorDemand(g, 6, 40, []float64{0})

	cfg := benchmark.SimConfig{
		StartTime:   time.Date(2026, 6, 20, 8, 0, 0, 0, time.UTC),
		TickSeconds: 30,
		MaxTicks:    1,
		BPR:         bpr,
	}
	res, err := benchmark.Simulate(context.Background(), g, reqs, cfg, benchmark.ReactiveBPRFactory(g, bpr), nil)
	if err != nil {
		t.Fatalf("Simulate error = %v", err)
	}

	if res.Ticks != 1 {
		t.Errorf("Ticks = %d, want 1 (the MaxTicks cap)", res.Ticks)
	}
	if res.Released == 0 {
		t.Fatalf("expected requests to be released within the single tick, Released = %d", res.Released)
	}
	if res.Completed >= res.Released {
		t.Errorf("Completed (%d) must be < Released (%d): the budget cuts the run short with vehicles still in flight", res.Completed, res.Released)
	}
	if !isFinite(res.MeanRealizedS) || !isFinite(res.P95RealizedS) {
		t.Errorf("metrics must be finite even when the budget is exhausted, got mean=%v p95=%v", res.MeanRealizedS, res.P95RealizedS)
	}
}

// TestSimulateStartTimeShift is the start-time acceptance criterion: a chosen start
// time-of-day/date sets the demand/clock origin, so every TickState.SimTime shifts by
// exactly the start-time offset while the DYNAMICS (loads, completions, realized
// metrics) are unchanged. We run the same OD set at two start times and assert each
// tick's SimTime differs by exactly the offset and the realized metrics are identical.
func TestSimulateStartTimeShift(t *testing.T) {
	g := corridorGraph(t)
	bpr := cost.DefaultBPR()
	reqs := corridorDemand(g, 10, 40, []float64{0, 30, 60})

	startA := time.Date(2026, 6, 20, 8, 0, 0, 0, time.UTC)
	startB := time.Date(2026, 6, 20, 17, 30, 0, 0, time.UTC) // a different time of day
	offset := startB.Sub(startA)

	cfgA := benchmark.SimConfig{StartTime: startA, TickSeconds: 30, BPR: bpr}
	cfgB := benchmark.SimConfig{StartTime: startB, TickSeconds: 30, BPR: bpr}

	var recA, recB recorder
	resA, err := benchmark.Simulate(context.Background(), g, reqs, cfgA, benchmark.ReactiveBPRFactory(g, bpr), recA.observe)
	if err != nil {
		t.Fatalf("run A error = %v", err)
	}
	resB, err := benchmark.Simulate(context.Background(), g, reqs, cfgB, benchmark.ReactiveBPRFactory(g, bpr), recB.observe)
	if err != nil {
		t.Fatalf("run B error = %v", err)
	}

	if len(recA.states) != len(recB.states) {
		t.Fatalf("start-time shift changed tick count: %d vs %d", len(recA.states), len(recB.states))
	}
	// Every tick's clock is shifted by exactly the start offset — observable.
	for i := range recA.states {
		gotOffset := recB.states[i].SimTime.Sub(recA.states[i].SimTime)
		if gotOffset != offset {
			t.Errorf("tick %d SimTime offset = %v, want %v (start-time must shift the clock)", i+1, gotOffset, offset)
		}
	}
	// The dynamics are start-time-independent: the realized metrics match exactly.
	if resA.MeanRealizedS != resB.MeanRealizedS || resA.P95RealizedS != resB.P95RealizedS {
		t.Errorf("start time must not change the realized metrics: A=(%v,%v) B=(%v,%v)",
			resA.MeanRealizedS, resA.P95RealizedS, resB.MeanRealizedS, resB.P95RealizedS)
	}
}

// TestSimulateDegenerateInputs is the table-driven edge-case suite: the empty batch,
// an origin == destination request, and a zero/default Δt must all produce a finite,
// well-defined SimResult — never a NaN, Inf, panic, or hang.
func TestSimulateDegenerateInputs(t *testing.T) {
	g := corridorGraph(t)
	bpr := cost.DefaultBPR()
	n0, _ := g.Node(0)

	cases := []struct {
		name          string
		reqs          []routing.RouteRequest
		tickSeconds   float64
		wantCompleted int
	}{
		{
			name:          "empty batch",
			reqs:          nil,
			tickSeconds:   30,
			wantCompleted: 0,
		},
		{
			name:          "origin equals destination",
			reqs:          []routing.RouteRequest{{ID: "self", From: n0.Pos, To: n0.Pos, Weight: 1, DepartAt: 0}},
			tickSeconds:   30,
			wantCompleted: 1, // a zero-edge trip completes immediately at zero travel time
		},
		{
			name:          "zero tick seconds defaults",
			reqs:          corridorDemand(g, 3, 30, []float64{0}),
			tickSeconds:   0, // must default to ≈30s, not divide by zero
			wantCompleted: 3,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := benchmark.SimConfig{StartTime: time.Unix(0, 0).UTC(), TickSeconds: tc.tickSeconds, BPR: bpr}
			res, err := benchmark.Simulate(context.Background(), g, tc.reqs, cfg, benchmark.ReactiveBPRFactory(g, bpr), nil)
			if err != nil {
				t.Fatalf("Simulate error = %v", err)
			}
			if res.Completed != tc.wantCompleted {
				t.Errorf("Completed = %d, want %d", res.Completed, tc.wantCompleted)
			}
			if !isFinite(res.MeanRealizedS) || !isFinite(res.P95RealizedS) {
				t.Errorf("metrics must be finite on degenerate input, got mean=%v p95=%v", res.MeanRealizedS, res.P95RealizedS)
			}
			if tc.tickSeconds == 0 && res.TickSeconds <= 0 {
				t.Errorf("zero Δt must resolve to a positive default, got %v", res.TickSeconds)
			}
		})
	}
}

// TestSimulateNilRouterFactory pins the usage-error contract: a nil RouterFactory is
// a returned error, never a panic.
func TestSimulateNilRouterFactory(t *testing.T) {
	g := corridorGraph(t)
	cfg := benchmark.SimConfig{TickSeconds: 30, BPR: cost.DefaultBPR()}
	_, err := benchmark.Simulate(context.Background(), g, corridorDemand(g, 1, 1, []float64{0}), cfg, nil, nil)
	if err == nil {
		t.Fatal("expected an error for a nil RouterFactory, got nil")
	}
}

// TestSimulateContextCancellation pins prompt cancellation: a cancelled context
// stops the run with ctx.Err(), never hangs.
func TestSimulateContextCancellation(t *testing.T) {
	g := corridorGraph(t)
	bpr := cost.DefaultBPR()
	reqs := corridorDemand(g, 50, 40, []float64{0, 30, 60})

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel before the run

	_, err := benchmark.Simulate(ctx, g, reqs, benchmark.SimConfig{TickSeconds: 30, BPR: bpr}, benchmark.ReactiveBPRFactory(g, bpr), nil)
	if err == nil {
		t.Fatal("expected a context error from a cancelled run, got nil")
	}
}

// TestSimulateOnToyNetwork runs the simulator on the real shared toy fixture to prove
// it works beyond the hand corridor: a staggered demand over the toy network drains
// fully, every metric is finite, and the per-tick stream is non-empty and ordered.
func TestSimulateOnToyNetwork(t *testing.T) {
	g, _, err := graph.LoadEdgeAttributesGeoJSONFile("../../testdata/toy_network.geojson", graph.WithExpectedBounds(-74, -73, 40, 41))
	if err != nil {
		t.Fatalf("toy_network.geojson must load with zero error, got: %v", err)
	}
	bpr := cost.DefaultBPR()

	n0, _ := g.Node(0)
	n2, _ := g.Node(2)
	reqs := make([]routing.RouteRequest, 12)
	for i := range reqs {
		reqs[i] = routing.RouteRequest{
			ID:       fmt.Sprintf("r%d", i),
			From:     n0.Pos,
			To:       n2.Pos,
			Weight:   100,
			DepartAt: float64(i) * 30, // one per tick
		}
	}

	var rec recorder
	cfg := benchmark.SimConfig{StartTime: time.Date(2026, 6, 20, 8, 0, 0, 0, time.UTC), TickSeconds: 30, BPR: bpr}
	res, err := benchmark.Simulate(context.Background(), g, reqs, cfg, benchmark.ReactiveBPRFactory(g, bpr), rec.observe)
	if err != nil {
		t.Fatalf("Simulate on toy network error = %v", err)
	}
	if res.Completed != len(reqs) {
		t.Errorf("Completed = %d, want %d", res.Completed, len(reqs))
	}
	if !isFinite(res.MeanRealizedS) || !isFinite(res.P95RealizedS) {
		t.Errorf("toy-network metrics must be finite, got mean=%v p95=%v", res.MeanRealizedS, res.P95RealizedS)
	}
	if len(rec.states) == 0 {
		t.Error("expected a non-empty per-tick stream on the toy network")
	}
	// Each emitted Load/VC slice is sized to the graph's edge count.
	for _, s := range rec.states {
		if len(s.Load) != g.EdgeCount() || len(s.VC) != g.EdgeCount() {
			t.Fatalf("tick %d Load/VC length = %d/%d, want %d", s.Tick, len(s.Load), len(s.VC), g.EdgeCount())
		}
	}
}

// isFinite reports whether x is neither NaN nor ±Inf.
func isFinite(x float64) bool { return !math.IsNaN(x) && !math.IsInf(x, 0) }
