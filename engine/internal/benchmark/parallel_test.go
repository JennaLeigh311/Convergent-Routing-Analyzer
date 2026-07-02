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

// TestPriceOfAnarchyRatioOnTickTotals asserts the PriceOfAnarchy ratio function on
// same-OD-set totals: pairing an algo's total with systemoptimal's gives PoA =
// total/soTotal, and systemoptimal against itself is exactly 1. The stream NO LONGER
// pairs per-tick totals — since #112 its live PoA comes from AlgoTick.StaticPoA (the
// static-equilibrium reference) — so this test only pins the underlying ratio math
// that reference relies on, not any same-tick pairing seam (which no longer exists).
func TestPriceOfAnarchyRatioOnTickTotals(t *testing.T) {
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

// TestRunParallelStaticPoARefErrorPropagates asserts RunParallel's new (#112)
// pre-sim early return: if the static PoA reference (staticPoARef → assignStatic)
// fails, RunParallel returns that error BEFORE launching any sim goroutine and emits
// nothing — never a partial stream. A pre-cancelled context makes the first
// AssignResult bail, exercising exactly that path.
func TestRunParallelStaticPoARefErrorPropagates(t *testing.T) {
	g := loadToy(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel before the run so staticPoARef's first AssignResult bails.

	emitted := 0
	emit := func(benchmark.AlgoTick) { emitted++ }
	err := benchmark.RunParallel(ctx, g, baseConfig(), emit)
	if err == nil {
		t.Fatalf("RunParallel with cancelled context: err = nil, want a propagated error")
	}
	if emitted != 0 {
		t.Errorf("RunParallel emitted %d ticks alongside an error, want no partial stream", emitted)
	}
}

// perAlgoStaticPoA collects each algo's StaticPoA from a parallel run, asserting it is
// carried on every frame and CONSTANT across that algo's ticks (an equilibrium is a
// property of the OD set, not a mesoscopic instant).
func perAlgoStaticPoA(t *testing.T, out map[string][]benchmark.AlgoTick) map[string]float64 {
	t.Helper()
	poa := make(map[string]float64)
	for _, name := range benchmark.RouterOrder {
		ticks := out[name]
		if len(ticks) == 0 {
			t.Fatalf("algo %q produced no ticks", name)
		}
		poa[name] = ticks[0].StaticPoA
		for _, tk := range ticks {
			if tk.StaticPoA != poa[name] {
				t.Errorf("%q StaticPoA not constant across ticks: %v vs %v", name, tk.StaticPoA, poa[name])
			}
		}
	}
	return poa
}

// TestLivePoAMatchesStaticBenchmark is the #112 acceptance criterion: the live stream's
// PoA on a fixed (start, seed) matches the static /benchmark PoA for the SAME OD set.
// The live PoA (AlgoTick.StaticPoA) and RunSingle's per-cell PoA are computed by the
// identical machinery (buildRouter → real iterative AssignResult → TotalNetworkTime →
// PriceOfAnarchy) over a byte-identical OD set + BPR, so they agree to a tiny numerical
// tolerance (they are the same operations — bit-for-bit in practice).
func TestLivePoAMatchesStaticBenchmark(t *testing.T) {
	g := loadToy(t)
	cfg := baseConfig() // seed 20260618, count 150, cap_scale 0.84

	out := collectAll(t, cfg)
	livePoA := perAlgoStaticPoA(t, out)

	// The documented tolerance. The live reference and the /benchmark reference run the
	// SAME deterministic code over the SAME OD set + BPR, so the difference is only
	// float reassociation (there is none here) — 1e-9 relative is effectively exact.
	const relTol = 1e-9
	for _, name := range benchmark.RouterOrder {
		cells, err := benchmark.RunSingle(context.Background(), g, cfg.Seed, cfg.Count, 0.15, 4, cfg.CapacityScale, name)
		if err != nil {
			t.Fatalf("RunSingle(%q): %v", name, err)
		}
		staticPoA := cells[0].PoA // the named router is always cells[0]
		if cells[0].Result.Router != name {
			t.Fatalf("RunSingle(%q) first cell is %q", name, cells[0].Result.Router)
		}
		diff := math.Abs(livePoA[name] - staticPoA)
		if diff > relTol*(1+math.Abs(staticPoA)) {
			t.Errorf("%q live PoA %.12f != static /benchmark PoA %.12f (diff %g > tol)",
				name, livePoA[name], staticPoA, diff)
		}
	}
}

// TestLivePoASOLessEqualUE is the #112 SO ≤ UE invariant for the live reference: every
// algo's static-equilibrium PoA is ≥ 1 (systemoptimal minimizes the total network time,
// so SO ≤ every other assignment's total), and systemoptimal against itself is exactly 1.
func TestLivePoASOLessEqualUE(t *testing.T) {
	out := collectAll(t, baseConfig())
	poa := perAlgoStaticPoA(t, out)

	if poa["systemoptimal"] != 1.0 {
		t.Errorf("systemoptimal StaticPoA = %v, want exactly 1 (its own reference)", poa["systemoptimal"])
	}
	for _, name := range benchmark.RouterOrder {
		if poa[name] < 1-1e-9 {
			t.Errorf("%q StaticPoA = %v, want >= 1 (SO minimizes total ⇒ SO <= UE)", name, poa[name])
		}
		if math.IsNaN(poa[name]) || math.IsInf(poa[name], 0) {
			t.Errorf("%q StaticPoA non-finite: %v", name, poa[name])
		}
	}
}

// TestRouteMedianIsPerCallNotCumulative is the #112 compute-metric criterion that the
// per-route figure is a PER-CALL statistic independent of how many routes/ticks/algos
// ran — NOT the fan-out-summed cumulative timer. Every request departs at t=0 so each
// is routed exactly once (Count total Route calls per algo); the cumulative
// ComputeNanos is therefore ≈ Count × per-call, so ComputeNanos must dwarf the median.
// A regression that reported the cumulative sum as the "per-route" figure would push
// the ratio to ≈ 1 and fail here.
func TestRouteMedianIsPerCallNotCumulative(t *testing.T) {
	cfg := baseConfig() // count 150 ⇒ ~150 Route calls per algo
	out := collectAll(t, cfg)
	for _, name := range benchmark.RouterOrder {
		last := out[name][len(out[name])-1]
		if last.RouteMedianNanos <= 0 {
			t.Errorf("%q RouteMedianNanos = %d, want > 0", name, last.RouteMedianNanos)
			continue
		}
		if last.ComputeNanos < 20*last.RouteMedianNanos {
			t.Errorf("%q cumulative ComputeNanos %d not >> per-route median %d (×%.1f); "+
				"the per-route metric looks cumulative, not per-call",
				name, last.ComputeNanos, last.RouteMedianNanos,
				float64(last.ComputeNanos)/float64(last.RouteMedianNanos))
		}
	}
}

// TestRouteMedianStableRunToRun is the #112 stability criterion: the per-route median is
// stable run-to-run (low variance) and does not blow up with the six-sim concurrency.
// It runs the same fixed config several times and asserts each run's median for the
// simplest, most consistent algo ("naive") stays within a generous band of the central
// value — the cumulative sum, summing overlapping concurrent Route intervals under CPU
// contention, is what the old metric could not keep stable. The band is deliberately
// generous (timing is inherently noisy) yet tight enough to catch an order-of-magnitude
// regression to cumulative-style behavior.
func TestRouteMedianStableRunToRun(t *testing.T) {
	const runs = 5
	medians := make([]int64, 0, runs)
	for i := 0; i < runs; i++ {
		out := collectAll(t, baseConfig())
		ticks := out["naive"]
		m := ticks[len(ticks)-1].RouteMedianNanos
		if m <= 0 {
			t.Fatalf("run %d: naive RouteMedianNanos = %d, want > 0", i, m)
		}
		medians = append(medians, m)
	}
	lo, hi := medians[0], medians[0]
	for _, m := range medians {
		if m < lo {
			lo = m
		}
		if m > hi {
			hi = m
		}
	}
	// Generous 10x band: robust to CI timing noise, but a cumulative-style metric (which
	// scales with route count AND contention) would not stay this tight run-to-run.
	if hi > 10*lo {
		t.Errorf("naive per-route median unstable run-to-run: min=%d max=%d (×%.1f > 10)",
			lo, hi, float64(hi)/float64(lo))
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
