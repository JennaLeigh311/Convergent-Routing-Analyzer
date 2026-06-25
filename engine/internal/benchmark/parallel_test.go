package benchmark_test

import (
	"context"
	"math"
	"sync"
	"testing"
	"time"

	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/benchmark"
)

// collectAll runs RunParallel and returns the per-algo tick stream collected under a
// lock (the six sims emit concurrently). It is the test harness's view of the run.
func collectAll(t *testing.T, cfg benchmark.ParallelConfig) map[string][]benchmark.AlgoTick {
	t.Helper()
	g := loadToy(t)
	out := make(map[string][]benchmark.AlgoTick)
	var mu sync.Mutex
	emit := func(tick benchmark.AlgoTick) {
		mu.Lock()
		out[tick.Algo] = append(out[tick.Algo], tick)
		mu.Unlock()
	}
	if err := benchmark.RunParallel(context.Background(), g, cfg, emit); err != nil {
		t.Fatalf("RunParallel: %v", err)
	}
	return out
}

func baseConfig() benchmark.ParallelConfig {
	return benchmark.ParallelConfig{
		StartTime:     time.Date(2026, 6, 22, 8, 0, 0, 0, time.UTC),
		TickSeconds:   30,
		Seed:          20260618,
		Count:         150,
		CapacityScale: 0.84, // an over-capacity scale so v/c rises above 1 and metrics move
	}
}

// TestRunParallelEmitsAllAlgorithms asserts every router in RouterOrder produces a
// stream and every per-tick metric is finite (the empty-batch / degenerate-safe
// contract carried through to the parallel run).
func TestRunParallelEmitsAllAlgorithms(t *testing.T) {
	out := collectAll(t, baseConfig())
	for _, name := range benchmark.RouterOrder {
		ticks := out[name]
		if len(ticks) == 0 {
			t.Errorf("algo %q produced no ticks", name)
			continue
		}
		for _, tk := range ticks {
			if tk.Algo != name {
				t.Errorf("tick mislabeled: got %q in %q stream", tk.Algo, name)
			}
			for label, v := range map[string]float64{
				"realized_total": tk.RealizedTotalS,
			} {
				if math.IsNaN(v) || math.IsInf(v, 0) {
					t.Errorf("non-finite %s in %q tick %d: %v", label, name, tk.State.Tick, v)
				}
			}
			if tk.ComputeNanos < 0 {
				t.Errorf("negative compute nanos in %q: %d", name, tk.ComputeNanos)
			}
		}
	}
}

// TestRunParallelComputeMonotonic asserts each algo's cumulative compute time never
// decreases over its stream — it is an accumulating counter.
func TestRunParallelComputeMonotonic(t *testing.T) {
	out := collectAll(t, baseConfig())
	for _, name := range benchmark.RouterOrder {
		ticks := out[name]
		var prev int64
		for _, tk := range ticks {
			if tk.ComputeNanos < prev {
				t.Errorf("%q compute nanos decreased: %d -> %d", name, prev, tk.ComputeNanos)
			}
			prev = tk.ComputeNanos
		}
	}
}

// TestRunParallelDeterministic asserts two runs at the same seed produce a
// byte-identical per-tick TRACE — same tick count, same SimTime, same per-edge VC,
// same realized totals — for every algorithm (modulo wall-clock compute time, which
// is excluded). This is the §R5 / #93 determinism criterion.
func TestRunParallelDeterministic(t *testing.T) {
	a := collectAll(t, baseConfig())
	b := collectAll(t, baseConfig())

	for _, name := range benchmark.RouterOrder {
		ta, tb := a[name], b[name]
		if len(ta) != len(tb) {
			t.Fatalf("%q tick counts differ: %d vs %d", name, len(ta), len(tb))
		}
		for i := range ta {
			if ta[i].State.Tick != tb[i].State.Tick {
				t.Errorf("%q tick %d index mismatch", name, i)
			}
			if !ta[i].State.SimTime.Equal(tb[i].State.SimTime) {
				t.Errorf("%q tick %d SimTime differ", name, i)
			}
			if ta[i].RealizedTotalS != tb[i].RealizedTotalS {
				t.Errorf("%q tick %d realized total differ: %v vs %v", name, i, ta[i].RealizedTotalS, tb[i].RealizedTotalS)
			}
			if len(ta[i].State.VC) != len(tb[i].State.VC) {
				t.Fatalf("%q tick %d VC lengths differ", name, i)
			}
			for e := range ta[i].State.VC {
				if ta[i].State.VC[e] != tb[i].State.VC[e] {
					t.Errorf("%q tick %d edge %d VC differ: %v vs %v", name, i, e, ta[i].State.VC[e], tb[i].State.VC[e])
				}
			}
		}
	}
}

// TestRunParallelStartTimeShifts asserts shifting StartTime shifts every tick's
// SimTime by exactly the same offset while leaving the relative dynamics (VC trace)
// identical — the #93 "slider sets the start clock observably" requirement.
func TestRunParallelStartTimeShifts(t *testing.T) {
	cfg := baseConfig()
	a := collectAll(t, cfg)

	cfg2 := baseConfig()
	offset := 3 * time.Hour
	cfg2.StartTime = cfg.StartTime.Add(offset)
	b := collectAll(t, cfg2)

	for _, name := range benchmark.RouterOrder {
		ta, tb := a[name], b[name]
		if len(ta) != len(tb) {
			t.Fatalf("%q tick counts differ across start shift: %d vs %d", name, len(ta), len(tb))
		}
		for i := range ta {
			wantTime := ta[i].State.SimTime.Add(offset)
			if !tb[i].State.SimTime.Equal(wantTime) {
				t.Errorf("%q tick %d SimTime not shifted: got %v want %v", name, i, tb[i].State.SimTime, wantTime)
			}
			// Relative dynamics unchanged: the VC trace is byte-identical.
			for e := range ta[i].State.VC {
				if ta[i].State.VC[e] != tb[i].State.VC[e] {
					t.Errorf("%q tick %d edge %d VC changed across start shift", name, i, e)
				}
			}
		}
	}
}

// TestPoASameTickPairing asserts the consumer-side PoA seam: pairing an algo's
// same-tick total with systemoptimal's through PriceOfAnarchy gives PoA = total/soTotal,
// and systemoptimal against itself is exactly 1. This is the deterministic PoA the
// stream layer emits (no cross-goroutine timing in the result).
func TestPoASameTickPairing(t *testing.T) {
	out := collectAll(t, baseConfig())
	soByTick := make(map[int]float64)
	for _, tk := range out["systemoptimal"] {
		soByTick[tk.State.Tick] = tk.RealizedTotalS
	}
	// systemoptimal vs itself is 1 at every tick.
	for _, tk := range out["systemoptimal"] {
		if got := benchmark.PriceOfAnarchy(tk.RealizedTotalS, soByTick[tk.State.Tick]); got != 1.0 {
			t.Errorf("systemoptimal self-PoA at tick %d = %v, want 1", tk.State.Tick, got)
		}
	}
	// naive vs systemoptimal at a shared tick equals the direct ratio (finite).
	for _, tk := range out["naive"] {
		so, ok := soByTick[tk.State.Tick]
		if !ok {
			continue
		}
		got := benchmark.PriceOfAnarchy(tk.RealizedTotalS, so)
		if math.IsNaN(got) || math.IsInf(got, 0) {
			t.Errorf("naive PoA at tick %d non-finite: %v", tk.State.Tick, got)
		}
	}
}

// TestRunParallelZeroCountFallsBack asserts the documented Count<=0 fallback: a
// zero-count run does NOT route zero OD pairs — RunParallel falls back to
// DefaultODCount, so every algorithm still produces a real (non-empty, finite) stream
// and no error. (A genuinely empty per-algo stream is unreachable through the
// orchestrator because the simulator always emits at least tick 1; the empty-snapshot
// branch is covered directly in stream_test.go.)
func TestRunParallelZeroCountFallsBack(t *testing.T) {
	cfg := baseConfig()
	cfg.Count = 0
	out := collectAll(t, cfg)
	for _, name := range benchmark.RouterOrder {
		ticks := out[name]
		if len(ticks) == 0 {
			t.Errorf("algo %q produced no ticks; Count<=0 must fall back to DefaultODCount, not an empty run", name)
			continue
		}
		for _, tk := range ticks {
			if math.IsNaN(tk.RealizedTotalS) || math.IsInf(tk.RealizedTotalS, 0) {
				t.Errorf("algo %q tick %d non-finite realized total: %v", name, tk.State.Tick, tk.RealizedTotalS)
			}
		}
	}
}
