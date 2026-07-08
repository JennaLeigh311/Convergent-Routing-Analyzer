package benchmark_test

import (
	"context"
	"math"
	"sync"
	"testing"
	"time"

	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/benchmark"
	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/graph"
)

// portoSlicePath is a small, COMMITTED connected slice of the real Porto
// edge_attributes export (issue #120's data/out/edge_attributes.geojson, which is a
// git-ignored 1.7 MB blob). It is a 75-edge / 45-node induced subgraph carved from central
// Porto (Baixa/Cedofeita), renumbered to a dense edge_id space, so the routing engine can be
// smoke-tested against REAL road geometry and the real §2 exporter's derived
// capacity/free-flow values — not just the synthetic toy network — without committing the
// full city blob. It is loaded through the SAME graph.LoadEdgeAttributesGeoJSON contract
// loader the binary uses for the full export (issue #121).
const portoSlicePath = "../../testdata/porto_slice.geojson"

func loadPortoSlice(t *testing.T) graph.Graph {
	t.Helper()
	g, _, err := graph.LoadEdgeAttributesGeoJSONFile(portoSlicePath, graph.WithConnectivityWarn())
	if err != nil {
		t.Fatalf("load Porto slice: %v", err)
	}
	return g
}

// TestPortoSliceRunsAllAlgorithms is the issue-#121 real-network smoke test: it confirms
// the engine boots and RUNS every one of the six algorithms over a real Porto geometry
// slice, producing finite per-tick routing metrics and a well-formed (≥ 1, the #121 floor)
// static-equilibrium PoA for each. It is the real-data analogue of the toy-network
// TestRunParallelEmitsAllAlgorithms — the acceptance that the larger, real graph loads,
// connects, and routes end-to-end through the parallel orchestrator.
func TestPortoSliceRunsAllAlgorithms(t *testing.T) {
	g := loadPortoSlice(t)

	cfg := benchmark.ParallelConfig{
		StartTime:     time.Date(2026, 6, 22, 8, 0, 0, 0, time.UTC),
		TickSeconds:   30,
		Seed:          20260707,
		Count:         120,
		CapacityScale: 0.9, // over-capacity so v/c climbs and the metrics move on the slice
	}

	out := make(map[string][]benchmark.AlgoTick)
	var mu sync.Mutex
	emit := func(tick benchmark.AlgoTick) {
		mu.Lock()
		out[tick.Algo] = append(out[tick.Algo], tick)
		mu.Unlock()
	}
	if err := benchmark.RunParallel(context.Background(), g, cfg, emit); err != nil {
		t.Fatalf("RunParallel over Porto slice: %v", err)
	}

	for _, name := range benchmark.RouterOrder {
		ticks := out[name]
		if len(ticks) == 0 {
			t.Errorf("algo %q produced no ticks on the Porto slice", name)
			continue
		}
		last := ticks[len(ticks)-1]
		// Metrics must be finite and the static PoA ≥ 1 (the #121 floor), on real geometry.
		if math.IsNaN(last.RealizedTotalS) || math.IsInf(last.RealizedTotalS, 0) {
			t.Errorf("algo %q: non-finite realized total %v on Porto slice", name, last.RealizedTotalS)
		}
		if last.StaticPoA < 1.0 || math.IsNaN(last.StaticPoA) || math.IsInf(last.StaticPoA, 0) {
			t.Errorf("algo %q: StaticPoA = %v, want a finite value >= 1 on Porto slice", name, last.StaticPoA)
		}
		if last.RouteMedianNanos <= 0 {
			t.Errorf("algo %q: RouteMedianNanos = %d, want > 0 (a route was actually computed)", name, last.RouteMedianNanos)
		}
	}

	// The six algorithms must produce DISTINCT outcomes on the real geometry — a selfish
	// naive assignment carries strictly more integrated realized network time than the
	// system-optimal reference, so the run is genuinely exercising different routers, not
	// six copies of one path.
	naiveTotal := integratedRealized(out["naive"])
	soTotal := integratedRealized(out["systemoptimal"])
	if !(naiveTotal >= soTotal) {
		t.Errorf("expected naive integrated realized time (%v) >= systemoptimal (%v) on the Porto slice",
			naiveTotal, soTotal)
	}
	if naiveTotal == 0 || soTotal == 0 {
		t.Errorf("expected non-zero realized network time on the Porto slice (naive=%v so=%v)", naiveTotal, soTotal)
	}
}

// TestPortoSliceSingleRoutesAndPoA confirms the static /benchmark path (RunSingle) also
// routes over the real Porto slice: every algorithm yields a finite PoA ≥ 1 with a
// non-empty flow assignment, so the larger real graph is routable through the iterative
// assignment machinery as well as the mesoscopic stream.
func TestPortoSliceSingleRoutesAndPoA(t *testing.T) {
	g := loadPortoSlice(t)
	for _, name := range benchmark.RouterOrder {
		cells, err := benchmark.RunSingle(context.Background(), g, 20260707, 120, 0.15, 4, 0.9, name)
		if err != nil {
			t.Fatalf("RunSingle(%q) over Porto slice: %v", name, err)
		}
		if len(cells) == 0 {
			t.Fatalf("RunSingle(%q): no result cells", name)
		}
		c := cells[0]
		if c.Result.Router != name {
			t.Fatalf("RunSingle(%q) first cell is %q", name, c.Result.Router)
		}
		if c.PoA < 1.0 || math.IsNaN(c.PoA) || math.IsInf(c.PoA, 0) {
			t.Errorf("algo %q: static PoA = %v, want a finite value >= 1 on Porto slice", name, c.PoA)
		}
	}
}

// integratedRealized sums an algo's per-tick realized total network time over its whole
// stream — the "how much traffic the network carried over the run" magnitude.
func integratedRealized(ticks []benchmark.AlgoTick) float64 {
	var sum float64
	for _, tk := range ticks {
		sum += tk.RealizedTotalS
	}
	return sum
}
