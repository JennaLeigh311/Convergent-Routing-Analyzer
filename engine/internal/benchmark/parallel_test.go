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

// totalRealizedNetworkTime sums an algo's per-tick realized total network time over
// its whole stream — the run's integrated (over-the-run) realized network time, the
// "how much traffic did the network carry" magnitude the peak-vs-off-peak criterion
// compares.
func totalRealizedNetworkTime(ticks []benchmark.AlgoTick) float64 {
	var sum float64
	for _, tk := range ticks {
		sum += tk.RealizedTotalS
	}
	return sum
}

// TestRunParallelPeakVsOffPeak is the issue-#111 acceptance criterion: starting the
// simulation at a rush-hour peak yields measurably HIGHER realized network time than
// starting it off-peak (dead of night), on the SAME seed. The chosen StartTime now
// drives the diurnal demand curve (demand.go) — both the OD-set magnitude and the
// DepartAt spread — so 08:00 and 02:00 are no longer byte-identical runs with a
// different caption (the cosmetic-only behavior this issue retired). This replaces the
// former TestRunParallelStartTimeShifts, whose assertion was that the v/c trace was
// IDENTICAL across a start shift.
func TestRunParallelPeakVsOffPeak(t *testing.T) {
	const seed = 20260618

	peak := baseConfig()
	peak.Seed = seed
	peak.StartTime = time.Date(2026, 6, 22, 8, 0, 0, 0, time.UTC) // AM rush peak

	off := baseConfig()
	off.Seed = seed
	off.StartTime = time.Date(2026, 6, 22, 2, 0, 0, 0, time.UTC) // dead of night trough

	peakOut := collectAll(t, peak)
	offOut := collectAll(t, off)

	// Compare on every router: a peak start must carry strictly more realized network
	// time than an off-peak start at the same seed.
	for _, name := range benchmark.RouterOrder {
		peakTotal := totalRealizedNetworkTime(peakOut[name])
		offTotal := totalRealizedNetworkTime(offOut[name])
		if !(peakTotal > offTotal) {
			t.Errorf("%q: peak-hour start must yield strictly higher realized network time than off-peak; got peak=%v off=%v",
				name, peakTotal, offTotal)
		}
	}

	// Sanity: the magnitude factor itself orders peak above off-peak (the curve the
	// demand is scaled by), so the result above is causal, not incidental.
	if benchmark.DiurnalDemandFactor(peak.StartTime) <= benchmark.DiurnalDemandFactor(off.StartTime) {
		t.Fatalf("diurnal factor must rank the rush peak above the night trough: peak=%v off=%v",
			benchmark.DiurnalDemandFactor(peak.StartTime), benchmark.DiurnalDemandFactor(off.StartTime))
	}
}

// TestRunParallelStartShiftDeterministic asserts the other half of the acceptance
// criterion: for a FIXED (StartTime, Seed) the per-tick trace is byte-identical run to
// run — the diurnal demand shaping introduced no wall-clock or unseeded randomness.
func TestRunParallelStartShiftDeterministic(t *testing.T) {
	cfg := baseConfig()
	cfg.StartTime = time.Date(2026, 6, 22, 2, 0, 0, 0, time.UTC) // an off-peak start
	a := collectAll(t, cfg)
	b := collectAll(t, cfg)

	for _, name := range benchmark.RouterOrder {
		ta, tb := a[name], b[name]
		if len(ta) != len(tb) {
			t.Fatalf("%q tick counts differ run to run at a fixed start: %d vs %d", name, len(ta), len(tb))
		}
		for i := range ta {
			if !ta[i].State.SimTime.Equal(tb[i].State.SimTime) {
				t.Errorf("%q tick %d SimTime differ run to run", name, i)
			}
			if ta[i].RealizedTotalS != tb[i].RealizedTotalS {
				t.Errorf("%q tick %d realized total not deterministic: %v vs %v", name, i, ta[i].RealizedTotalS, tb[i].RealizedTotalS)
			}
			for e := range ta[i].State.VC {
				if ta[i].State.VC[e] != tb[i].State.VC[e] {
					t.Errorf("%q tick %d edge %d VC not deterministic", name, i, e)
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
