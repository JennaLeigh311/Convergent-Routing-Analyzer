package simulator_test

import (
	"math"
	"math/rand"
	"testing"

	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/congestion/simulator"
	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/domain"
)

// tolerance is the float comparison tolerance for vph assertions (house style).
const tolerance = 1e-9

// perturbationFraction mirrors the production evolution knob (simulator.go); the
// order-determinism test recomputes the expected per-tick factors and so needs
// the same bound. If the production constant changes, update this to match.
const perturbationFraction = 0.10

// TestInjectRaisesLoad asserts the acceptance criterion: injecting load on an
// edge makes Load read above its zero baseline, while an un-injected /
// out-of-range / negative EdgeID reads as 0 per the LoadSnapshot.Load contract.
func TestInjectRaisesLoad(test1 *testing.T) {
	simulation := simulator.New(1, 8)
	simulation.Inject(3, 420)

	testCases := []struct {
		name   string
		edgeID domain.EdgeID
		want   float64
	}{
		{name: "injected edge exceeds zero baseline", edgeID: 3, want: 420},
		{name: "un-injected edge reads 0", edgeID: 5, want: 0},
		{name: "out-of-range edge reads 0", edgeID: 999, want: 0},
		{name: "negative edge reads 0", edgeID: -1, want: 0},
	}
	for _, testCase := range testCases {
		test1.Run(testCase.name, func(test2 *testing.T) {
			if got := simulation.Load(testCase.edgeID); math.Abs(got-testCase.want) > tolerance {
				test2.Errorf("Load(%d) = %v, want %v", testCase.edgeID, got, testCase.want)
			}
		})
	}
}

// TestInjectIsAdditive asserts the documented set-vs-add decision: two injects
// on the same edge add, and a negative injection is floored at 0 rather than
// driving the load below the router's non-negativity assumption.
func TestInjectIsAdditive(test1 *testing.T) {
	simulation := simulator.New(1, 4)
	simulation.Inject(2, 300)
	simulation.Inject(2, 120) // additive → 420.
	if got := simulation.Load(2); math.Abs(got-420) > tolerance {
		test1.Errorf("Load(2) after two injects = %v, want 420 (additive)", got)
	}
	simulation.Inject(2, -1000) // would go negative; must floor at 0.
	if got := simulation.Load(2); math.Abs(got-0) > tolerance {
		test1.Errorf("Load(2) after over-subtracting inject = %v, want 0 (floored, never negative)", got)
	}
}

// TestInjectGrowsBackingStore asserts that Inject on an EdgeID beyond the
// initial size grows the simulator rather than dropping the load, while a
// negative EdgeID is ignored.
func TestInjectGrowsBackingStore(test1 *testing.T) {
	simulation := simulator.New(1, 2)
	simulation.Inject(50, 144) // beyond the initial size of 2.
	if got := simulation.Load(50); math.Abs(got-144) > tolerance {
		test1.Errorf("Load(50) after growing Inject = %v, want 144", got)
	}
	simulation.Inject(-1, 999) // ignored; must not panic or appear anywhere.
	if got := simulation.Load(-1); got != 0 {
		test1.Errorf("Load(-1) = %v, want 0", got)
	}
}

// TestInjectAllJamsCorridor asserts the bulk "jam a corridor" path: InjectAll
// raises several edges above baseline in one call, growing the store to fit the
// largest EdgeID, and is additive over any pre-existing load.
func TestInjectAllJamsCorridor(test1 *testing.T) {
	simulation := simulator.New(1, 0)
	simulation.Inject(1, 100) // pre-existing load on a corridor edge.
	simulation.InjectAll(map[domain.EdgeID]float64{
		1:  116, // additive onto the 100 → 216.
		4:  504,
		20: 144,
	})
	want := map[domain.EdgeID]float64{1: 216, 4: 504, 20: 144, 0: 0, 5: 0}
	for edgeID, expected := range want {
		if got := simulation.Load(edgeID); math.Abs(got-expected) > tolerance {
			test1.Errorf("Load(%d) = %v, want %v", edgeID, got, expected)
		}
	}
}

// TestSameSeedIsDeterministic asserts the §R5 reproducibility contract: two
// simulators with the SAME seed, given the same Inject + Step sequence, produce
// IDENTICAL load snapshots tick by tick across several ticks.
func TestSameSeedIsDeterministic(test1 *testing.T) {
	const seed = 42
	const edgeCount = 16
	const ticks = 10

	first := simulator.New(seed, edgeCount)
	second := simulator.New(seed, edgeCount)
	for _, simulation := range []*simulator.Simulator{first, second} {
		simulation.InjectAll(map[domain.EdgeID]float64{0: 500, 3: 1200, 7: 800, 12: 300})
	}

	for tick := 0; tick < ticks; tick++ {
		first.Step()
		second.Step()
		firstSnapshot := first.Snapshot()
		secondSnapshot := second.Snapshot()
		if len(firstSnapshot) != len(secondSnapshot) {
			test1.Fatalf("tick %d: snapshot lengths differ: %d vs %d", tick, len(firstSnapshot), len(secondSnapshot))
		}
		for edgeID := range firstSnapshot {
			if math.Abs(firstSnapshot[edgeID]-secondSnapshot[edgeID]) > tolerance {
				test1.Fatalf("tick %d edge %d: same-seed loads diverged: %v vs %v",
					tick, edgeID, firstSnapshot[edgeID], secondSnapshot[edgeID])
			}
		}
	}
}

// TestStepNMatchesRepeatedStep asserts StepN(n) draws from the same seeded RNG
// stream as n successive Step() calls, so the two produce an identical history.
func TestStepNMatchesRepeatedStep(test1 *testing.T) {
	const seed = 7
	stepped := simulator.New(seed, 8)
	batched := simulator.New(seed, 8)
	stepped.InjectAll(map[domain.EdgeID]float64{2: 600, 5: 900})
	batched.InjectAll(map[domain.EdgeID]float64{2: 600, 5: 900})

	for tick := 0; tick < 5; tick++ {
		stepped.Step()
	}
	batched.StepN(5)

	steppedSnapshot := stepped.Snapshot()
	batchedSnapshot := batched.Snapshot()
	for edgeID := range steppedSnapshot {
		if math.Abs(steppedSnapshot[edgeID]-batchedSnapshot[edgeID]) > tolerance {
			test1.Errorf("edge %d: StepN(5) != 5×Step(): %v vs %v",
				edgeID, batchedSnapshot[edgeID], steppedSnapshot[edgeID])
		}
	}
}

// TestDifferentSeedDiverges asserts that a DIFFERENT seed (very likely)
// produces a different evolution: after several ticks at least one loaded edge
// reads a different load. With four loaded edges over eight ticks the chance of
// an identical history under distinct seeds is negligible.
func TestDifferentSeedDiverges(test1 *testing.T) {
	const edgeCount = 16
	const ticks = 8
	one := simulator.New(1, edgeCount)
	two := simulator.New(2, edgeCount)
	for _, simulation := range []*simulator.Simulator{one, two} {
		simulation.InjectAll(map[domain.EdgeID]float64{0: 500, 3: 1200, 7: 800, 12: 300})
	}
	one.StepN(ticks)
	two.StepN(ticks)

	oneSnapshot := one.Snapshot()
	twoSnapshot := two.Snapshot()
	diverged := false
	for edgeID := range oneSnapshot {
		if math.Abs(oneSnapshot[edgeID]-twoSnapshot[edgeID]) > tolerance {
			diverged = true
			break
		}
	}
	if !diverged {
		test1.Error("distinct seeds produced an identical load history; evolution is not seed-sensitive")
	}
}

// TestStepIsLiveAndNonNegative asserts that Step actually changes loaded-edge
// values across ticks (the evolution is live, so routes shift), leaves
// un-injected edges at 0, and never drives any load negative.
func TestStepIsLiveAndNonNegative(test1 *testing.T) {
	simulation := simulator.New(99, 8)
	simulation.Inject(2, 700)
	simulation.Inject(5, 1100)

	before := simulation.Snapshot()
	simulation.StepN(3)
	after := simulation.Snapshot()

	if math.Abs(before[2]-after[2]) <= tolerance && math.Abs(before[5]-after[5]) <= tolerance {
		test1.Errorf("no loaded edge changed across ticks; evolution is not live (edge2 %v→%v, edge5 %v→%v)",
			before[2], after[2], before[5], after[5])
	}
	for edgeID := range after {
		if after[edgeID] < 0 {
			test1.Errorf("edge %d went negative: %v", edgeID, after[edgeID])
		}
	}
	// Un-injected edges stay at exactly 0 (Step only touches loaded edges).
	if math.Abs(after[0]) > tolerance || math.Abs(after[7]) > tolerance {
		test1.Errorf("Step altered an un-injected edge: edge0=%v edge7=%v", after[0], after[7])
	}
}

// TestStepVisitsEdgesInAscendingOrder pins the §R5 contract that Step draws from
// the RNG in ascending EdgeID order, independent of map iteration order. Two
// same-seed simulators running identical ops would match even if Step used a
// randomized order, so that test alone can't catch a regression to map-order
// iteration; this one recomputes the expected post-tick loads from an
// independent rand.Rand consumed in ascending edge order, so it fails the moment
// the visit order changes.
func TestStepVisitsEdgesInAscendingOrder(test1 *testing.T) {
	const seed = 123
	simulation := simulator.New(seed, 8)
	const (
		loadLow  = 500.0 // edge 3 — visited first (lower EdgeID).
		loadHigh = 900.0 // edge 7 — visited second.
	)
	simulation.Inject(3, loadLow)
	simulation.Inject(7, loadHigh)

	// Independent RNG with the same seed: the first draw must apply to edge 3, the
	// second to edge 7, because Step visits ascending EdgeID order.
	expectedRNG := rand.New(rand.NewSource(seed))
	factorLow := 1 - perturbationFraction + expectedRNG.Float64()*(2*perturbationFraction)
	factorHigh := 1 - perturbationFraction + expectedRNG.Float64()*(2*perturbationFraction)

	simulation.Step()

	if got, want := simulation.Load(3), loadLow*factorLow; math.Abs(got-want) > tolerance {
		test1.Errorf("Load(3) after Step = %v, want %v (first RNG draw applies to the lowest loaded EdgeID)", got, want)
	}
	if got, want := simulation.Load(7), loadHigh*factorHigh; math.Abs(got-want) > tolerance {
		test1.Errorf("Load(7) after Step = %v, want %v (second RNG draw applies to the next loaded EdgeID)", got, want)
	}
}

// TestStepNNonPositiveIsNoOp asserts StepN(0) and StepN(negative) leave the
// loads (and the RNG stream) untouched.
func TestStepNNonPositiveIsNoOp(test1 *testing.T) {
	simulation := simulator.New(5, 8)
	simulation.Inject(2, 600)
	before := simulation.Snapshot()

	for _, tickCount := range []int{0, -3} {
		simulation.StepN(tickCount)
		after := simulation.Snapshot()
		for edgeID := range after {
			if math.Abs(before[edgeID]-after[edgeID]) > tolerance {
				test1.Errorf("StepN(%d) changed edge %d: %v → %v, want no-op", tickCount, edgeID, before[edgeID], after[edgeID])
			}
		}
	}
}

// TestStepOnUnloadedSimulator asserts Step on a simulator with no loaded edges
// is a harmless no-op: every edge stays 0 and nothing panics.
func TestStepOnUnloadedSimulator(test1 *testing.T) {
	simulation := simulator.New(1, 8)
	simulation.StepN(5)
	snapshot := simulation.Snapshot()
	for edgeID := range snapshot {
		if math.Abs(snapshot[edgeID]) > tolerance {
			test1.Errorf("edge %d = %v after stepping an unloaded simulator, want 0", edgeID, snapshot[edgeID])
		}
	}
}

// TestSnapshotIsIndependentCopy asserts the port's ownership contract from both
// directions: mutating a returned snapshot must not affect the simulator, and a
// later Inject/Step on the simulator must not affect a snapshot already handed
// out.
func TestSnapshotIsIndependentCopy(test1 *testing.T) {
	simulation := simulator.New(1, 4)
	simulation.Inject(2, 420)

	snapshot := simulation.Snapshot()
	if got := snapshot.Load(2); math.Abs(got-420) > tolerance {
		test1.Fatalf("snapshot.Load(2) = %v, want 420", got)
	}

	// Mutating the snapshot must not affect the simulator.
	snapshot[2] = 0
	if got := simulation.Load(2); math.Abs(got-420) > tolerance {
		test1.Errorf("simulator.Load(2) after mutating snapshot = %v, want 420 (snapshot must be a copy)", got)
	}

	// A later Inject on the simulator must not affect the already-handed-out snapshot.
	simulation.Inject(2, 999)
	if got := snapshot.Load(2); math.Abs(got-0) > tolerance {
		test1.Errorf("snapshot.Load(2) after later Inject = %v, want 0 (simulator must not alias the snapshot)", got)
	}
}
