package benchmark_test

import (
	"context"
	"math"
	"strings"
	"testing"

	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/benchmark"
)

// TestRunSweepGridShape asserts the sweep returns the full 6×4 grid in (level,
// RouterOrder) order with no router dropped and every metric finite.
func TestRunSweepGridShape(t *testing.T) {
	g := loadToy(t)
	cells, err := benchmark.RunSweep(context.Background(), g, 20260618, 200)
	if err != nil {
		t.Fatalf("RunSweep: %v", err)
	}
	wantLevels := len(benchmark.SweepLevels())
	wantRouters := len(benchmark.RouterOrder)
	if got, want := len(cells), wantLevels*wantRouters; got != want {
		t.Fatalf("cell count = %d, want %d", got, want)
	}
	for _, c := range cells {
		for name, v := range map[string]float64{
			"mean":     c.Result.MeanRealizedS,
			"p95":      c.Result.P95RealizedS,
			"total":    c.Result.TotalNetworkTimeS,
			"poa":      c.PoA,
			"sim_mean": c.SimMeanRealizedS,
			"sim_p95":  c.SimP95RealizedS,
			"max_vc":   c.Result.MaxVC,
			"gini":     c.Result.GiniVC,
		} {
			if math.IsNaN(v) || math.IsInf(v, 0) {
				t.Errorf("non-finite %s in cell %s@%s: %v", name, c.Result.Router, c.Result.DemandLevel, v)
			}
		}
	}
}

// TestRunSweepDeterministic asserts two sweeps over the same (graph, seed, count)
// produce identical cells — every metric, PoA, and the simulator-mode total. This is
// the determinism invariant under the package's own test (Lane A runs it with -race).
func TestRunSweepDeterministic(t *testing.T) {
	g := loadToy(t)
	a, err := benchmark.RunSweep(context.Background(), g, 20260618, 300)
	if err != nil {
		t.Fatalf("sweep a: %v", err)
	}
	b, err := benchmark.RunSweep(context.Background(), g, 20260618, 300)
	if err != nil {
		t.Fatalf("sweep b: %v", err)
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

// TestRunSweepSystemOptimalPoAIsOne asserts the systemoptimal cell's PoA is 1 at
// every level (it is its own reference), and that naive's PoA is >= 1 (the SO ≤ UE
// invariant: selfish total is never below the optimum).
func TestRunSweepSystemOptimalPoAIsOne(t *testing.T) {
	g := loadToy(t)
	cells, err := benchmark.RunSweep(context.Background(), g, 20260618, 500)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	for _, c := range cells {
		switch c.Result.Router {
		case "systemoptimal":
			if math.Abs(c.PoA-1) > 1e-9 {
				t.Errorf("systemoptimal PoA at %s = %v, want 1", c.Result.DemandLevel, c.PoA)
			}
		case "naive":
			if c.PoA < 1-1e-9 {
				t.Errorf("naive PoA at %s = %v, want >= 1 (SO <= UE)", c.Result.DemandLevel, c.PoA)
			}
		}
	}
}

// TestPoAByLevel asserts the per-level PoA list covers all four levels in ascending
// target-v/c order — the issue requires PoA reported across ALL levels.
func TestPoAByLevel(t *testing.T) {
	g := loadToy(t)
	cells, err := benchmark.RunSweep(context.Background(), g, 20260618, 200)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	levels := benchmark.PoAByLevel(cells)
	if got := len(levels); got != len(benchmark.SweepLevels()) {
		t.Fatalf("PoAByLevel entries = %d, want %d", got, len(benchmark.SweepLevels()))
	}
	for i := 1; i < len(levels); i++ {
		if levels[i].TargetVC < levels[i-1].TargetVC {
			t.Errorf("PoAByLevel not in ascending target-v/c order: %v", levels)
		}
	}
}

// TestHeadlineImprovementHasLevel asserts the headline improvement carries a demand
// level and a coordinated best router, and that its percent reduction is finite and
// non-negative (the best coordinated total never exceeds naive's — SO ≤ UE).
func TestHeadlineImprovementHasLevel(t *testing.T) {
	g := loadToy(t)
	cells, err := benchmark.RunSweep(context.Background(), g, 20260618, 400)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	h := benchmark.HeadlineImprovement(cells)
	if h.DemandLevel == "" {
		t.Error("headline improvement missing its demand level")
	}
	if h.BestRouter != "incremental" && h.BestRouter != "systemoptimal" {
		t.Errorf("headline best router = %q, want incremental or systemoptimal", h.BestRouter)
	}
	if math.IsNaN(h.PercentReduction) || math.IsInf(h.PercentReduction, 0) {
		t.Errorf("non-finite percent reduction: %v", h.PercentReduction)
	}
	if h.PercentReduction < -1e-6 {
		t.Errorf("percent reduction is negative (%v); coordinated total exceeded naive", h.PercentReduction)
	}
}

// TestRenderMarkdownTable asserts the rendered table has the header, separator, and
// one row per cell, and that the header carries the sweep-specific columns.
func TestRenderMarkdownTable(t *testing.T) {
	g := loadToy(t)
	cells, err := benchmark.RunSweep(context.Background(), g, 20260618, 100)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	table := benchmark.RenderMarkdownTable(cells)
	lines := strings.Split(strings.TrimRight(table, "\n"), "\n")
	if got, want := len(lines), len(cells)+2; got != want {
		t.Fatalf("table line count = %d, want %d (header + separator + %d cells)", got, want, len(cells))
	}
	for _, col := range []string{"router", "cap_scale", "target_vc", "poa", "sim_mean_s", "sim_p95_s"} {
		if !strings.Contains(lines[0], col) {
			t.Errorf("table header missing column %q:\n%s", col, lines[0])
		}
	}
}

// TestHeadlineImprovementEmpty asserts an empty grid yields a zero Improvement
// without panicking (the degenerate-input contract).
func TestHeadlineImprovementEmpty(t *testing.T) {
	h := benchmark.HeadlineImprovement(nil)
	if h.DemandLevel != "" || h.PercentReduction != 0 {
		t.Errorf("empty grid should yield a zero Improvement, got %+v", h)
	}
}
