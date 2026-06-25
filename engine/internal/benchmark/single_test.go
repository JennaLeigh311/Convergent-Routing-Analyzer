package benchmark_test

import (
	"context"
	"math"
	"testing"

	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/benchmark"
)

// TestRunSingleShapeAndParamsFlow asserts single-algorithm mode returns the named
// router's cell first (plus the systemoptimal reference), at the synthesized "single"
// level, under the CLIENT'S capacity_scale — the cell echoes the param it ran under so
// the §R6 knob is demonstrably live, not inert.
func TestRunSingleShapeAndParamsFlow(t *testing.T) {
	g := loadToy(t)
	const capScale = 0.7
	cells, err := benchmark.RunSingle(context.Background(), g, 20260618, 300, 0.15, 4, capScale, "reactive")
	if err != nil {
		t.Fatalf("RunSingle: %v", err)
	}
	// reactive (named) + systemoptimal (reference) = two cells, named first.
	if got := len(cells); got != 2 {
		t.Fatalf("cell count = %d, want 2 (named + systemoptimal reference)", got)
	}
	if cells[0].Result.Router != "reactive" {
		t.Errorf("cell[0].Router = %q, want the named router %q first", cells[0].Result.Router, "reactive")
	}
	if cells[1].Result.Router != "systemoptimal" {
		t.Errorf("cell[1].Router = %q, want %q (the PoA reference)", cells[1].Result.Router, "systemoptimal")
	}
	for _, c := range cells {
		if c.Result.DemandLevel != "single" {
			t.Errorf("DemandLevel = %q, want %q", c.Result.DemandLevel, "single")
		}
		if c.CapacityScale != capScale {
			t.Errorf("CapacityScale = %v, want the client's %v (the param must flow through)", c.CapacityScale, capScale)
		}
		for name, v := range map[string]float64{
			"mean": c.Result.MeanRealizedS, "p95": c.Result.P95RealizedS,
			"total": c.Result.TotalNetworkTimeS, "poa": c.PoA,
			"sim_mean": c.SimMeanRealizedS, "sim_p95": c.SimP95RealizedS,
		} {
			if math.IsNaN(v) || math.IsInf(v, 0) {
				t.Errorf("non-finite %s in cell %s: %v", name, c.Result.Router, v)
			}
		}
	}
}

// TestRunSingleSystemOptimalIsOneCell asserts that when the named router IS
// systemoptimal it is routed exactly once (no duplicate reference cell) and its PoA
// is 1 by construction — it is its own reference.
func TestRunSingleSystemOptimalIsOneCell(t *testing.T) {
	g := loadToy(t)
	cells, err := benchmark.RunSingle(context.Background(), g, 20260618, 300, 0.15, 4, 1.0, "systemoptimal")
	if err != nil {
		t.Fatalf("RunSingle: %v", err)
	}
	if got := len(cells); got != 1 {
		t.Fatalf("cell count = %d, want 1 (systemoptimal is its own reference)", got)
	}
	if cells[0].Result.Router != "systemoptimal" {
		t.Errorf("Router = %q, want systemoptimal", cells[0].Result.Router)
	}
	if math.Abs(cells[0].PoA-1) > 1e-9 {
		t.Errorf("PoA = %v, want 1 (systemoptimal is its own reference)", cells[0].PoA)
	}
}

// TestRunSingleParamsChangeResult is the §R6 acceptance criterion at the package
// level: two single-mode runs differing ONLY in capacity_scale (and separately in
// alpha) return DIFFERENT metrics — the params are no longer inert.
func TestRunSingleParamsChangeResult(t *testing.T) {
	g := loadToy(t)
	base, err := benchmark.RunSingle(context.Background(), g, 20260618, 400, 0.15, 4, 1.0, "naive")
	if err != nil {
		t.Fatalf("base: %v", err)
	}
	// Tighter capacity (smaller scale) lifts the BPR cost curve, so realized total
	// rises — a different result driven purely by the capacity_scale param.
	tighter, err := benchmark.RunSingle(context.Background(), g, 20260618, 400, 0.15, 4, 0.5, "naive")
	if err != nil {
		t.Fatalf("tighter: %v", err)
	}
	if base[0].Result.TotalNetworkTimeS == tighter[0].Result.TotalNetworkTimeS {
		t.Errorf("capacity_scale change did not move the realized total (%v) — param is inert",
			base[0].Result.TotalNetworkTimeS)
	}

	// A larger alpha steepens the congestion penalty, again moving the realized total.
	steeper, err := benchmark.RunSingle(context.Background(), g, 20260618, 400, 0.9, 4, 1.0, "naive")
	if err != nil {
		t.Fatalf("steeper: %v", err)
	}
	if base[0].Result.TotalNetworkTimeS == steeper[0].Result.TotalNetworkTimeS {
		t.Errorf("alpha change did not move the realized total (%v) — param is inert",
			base[0].Result.TotalNetworkTimeS)
	}
}

// TestRunSingleDeterministic asserts two identical single-mode runs return
// byte-identical cells — the §R5/§R6 determinism criterion for the new mode.
func TestRunSingleDeterministic(t *testing.T) {
	g := loadToy(t)
	a, err := benchmark.RunSingle(context.Background(), g, 20260618, 300, 0.15, 4, 1.0, "incremental")
	if err != nil {
		t.Fatalf("run a: %v", err)
	}
	b, err := benchmark.RunSingle(context.Background(), g, 20260618, 300, 0.15, 4, 1.0, "incremental")
	if err != nil {
		t.Fatalf("run b: %v", err)
	}
	if len(a) != len(b) {
		t.Fatalf("cell counts differ: %d vs %d", len(a), len(b))
	}
	for i := range a {
		if a[i] != b[i] {
			t.Errorf("cell %d differs across runs:\n a=%+v\n b=%+v", i, a[i], b[i])
		}
	}
}

// TestRunSingleUnknownRouter asserts RunSingle guards an unknown router name with a
// clean error rather than panicking deep in buildRouter (the API rejects it as a 400
// first, but the package guards it too).
func TestRunSingleUnknownRouter(t *testing.T) {
	g := loadToy(t)
	if _, err := benchmark.RunSingle(context.Background(), g, 1, 10, 0.15, 4, 1.0, "nope"); err == nil {
		t.Fatalf("RunSingle with unknown router: err = nil, want a clean error")
	}
}

// TestRunSingleContextCancelled asserts a routing error mid-run propagates out of
// RunSingle (it returns the error and no partial grid) rather than being swallowed.
// A pre-cancelled context makes the first router's AssignResult return ctx.Err() on
// the non-empty OD set — exercising RunSingle's per-router error-return path, which
// the unknown-router guard alone does not reach.
func TestRunSingleContextCancelled(t *testing.T) {
	g := loadToy(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel before the run so the very first AssignResult bails.
	cells, err := benchmark.RunSingle(ctx, g, 20260618, 300, 0.15, 4, 1.0, "naive")
	if err == nil {
		t.Fatalf("RunSingle with cancelled context: err = nil, want a propagated error")
	}
	if cells != nil {
		t.Errorf("RunSingle returned %d cells alongside an error, want no partial grid", len(cells))
	}
}

// TestIsRouter asserts the membership helper the API validation uses agrees with
// RouterOrder and excludes "all".
func TestIsRouter(t *testing.T) {
	for _, name := range benchmark.RouterOrder {
		if !benchmark.IsRouter(name) {
			t.Errorf("IsRouter(%q) = false, want true", name)
		}
	}
	for _, name := range []string{"all", "", "nope", "Naive"} {
		if benchmark.IsRouter(name) {
			t.Errorf("IsRouter(%q) = true, want false", name)
		}
	}
}

// TestBuildReportSingleModeDegrades asserts BuildReport (and its PoAByLevel /
// HeadlineImprovement helpers) carry a one-or-two-cell single-mode grid through to a
// finite report rather than NaN/panicking — the helpers are documented to return
// 1/zero on degenerate input.
func TestBuildReportSingleModeDegrades(t *testing.T) {
	g := loadToy(t)
	// A non-systemoptimal router yields a naive-free, two-cell grid (named + so).
	cells, err := benchmark.RunSingle(context.Background(), g, 20260618, 200, 0.15, 4, 1.0, "msa")
	if err != nil {
		t.Fatalf("RunSingle: %v", err)
	}
	report := benchmark.BuildReport(0, 200, cells)
	if len(report.Cells) != len(cells) {
		t.Errorf("report cells = %d, want %d", len(report.Cells), len(cells))
	}
	// No "naive" cell ⇒ PoAByLevel is empty and HeadlineImprovement is the zero value,
	// both finite/defined, never NaN or a panic.
	if got := report.Headline.PercentReduction; math.IsNaN(got) || math.IsInf(got, 0) {
		t.Errorf("headline PercentReduction = %v, want a finite value", got)
	}
	for _, lp := range report.PoAByLevel {
		if math.IsNaN(lp.PoA) || math.IsInf(lp.PoA, 0) {
			t.Errorf("PoA at %s = %v, want finite", lp.DemandLevel, lp.PoA)
		}
	}
}
