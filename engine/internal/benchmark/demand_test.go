package benchmark

import (
	"sort"
	"testing"
	"time"
)

func at(hour, min int) time.Time {
	return time.Date(2026, 6, 22, hour, min, 0, 0, time.UTC)
}

// TestDiurnalDemandFactorShape asserts the curve is bimodal (AM/PM peaks) over a night
// trough, bounded in [nightTrough, 1], and ranks the rush peaks strictly above the
// off-peak hours — the property RunParallel relies on to make a peak start heavier.
func TestDiurnalDemandFactorShape(t *testing.T) {
	amPeak := DiurnalDemandFactor(at(8, 0))
	pmPeak := DiurnalDemandFactor(at(17, 30))
	night := DiurnalDemandFactor(at(3, 0))
	midday := DiurnalDemandFactor(at(12, 0))

	// Peaks are at (or essentially at) 1.
	for _, p := range []struct {
		name string
		v    float64
	}{{"am", amPeak}, {"pm", pmPeak}} {
		if p.v < 0.99 || p.v > 1.0+1e-12 {
			t.Errorf("%s peak factor = %v, want ≈1", p.name, p.v)
		}
	}
	// Night trough is the floor.
	if night > nightTrough+0.02 {
		t.Errorf("night factor = %v, want ≈ trough %v", night, nightTrough)
	}
	// Strict ordering: a rush peak carries more than midday, which carries more than
	// the dead of night.
	if !(amPeak > midday && midday > night) {
		t.Errorf("expected amPeak > midday > night, got %v, %v, %v", amPeak, midday, night)
	}
	// Bounded everywhere across the day.
	for h := 0; h < 24; h++ {
		v := DiurnalDemandFactor(at(h, 0))
		if v < nightTrough-1e-9 || v > 1.0+1e-9 {
			t.Errorf("factor at %02d:00 = %v out of [%v,1]", h, v, nightTrough)
		}
	}
}

// TestDiurnalDemandFactorDateIndependent asserts the curve depends only on time-of-day,
// not the date — the same wall-clock hour on any date sees identical demand.
func TestDiurnalDemandFactorDateIndependent(t *testing.T) {
	a := DiurnalDemandFactor(time.Date(2026, 1, 5, 8, 0, 0, 0, time.UTC))
	b := DiurnalDemandFactor(time.Date(2030, 11, 19, 8, 0, 0, 0, time.UTC))
	if a != b {
		t.Errorf("factor must be date-independent: %v vs %v", a, b)
	}
}

// TestDepartureSpreadDeterministicAndSorted asserts the DepartAt schedule is a pure,
// reproducible function of (start, window, n): identical inputs give an identical
// slice, the offsets are sorted ascending and lie inside the window — the determinism
// guarantee the §R5 byte-identical-trace criterion rests on.
func TestDepartureSpreadDeterministicAndSorted(t *testing.T) {
	start := at(8, 0)
	const window = DefaultDemandWindowSeconds
	const n = 200

	a := departureSpread(start, window, n)
	b := departureSpread(start, window, n)
	if len(a) != n || len(b) != n {
		t.Fatalf("lengths = %d, %d, want %d", len(a), len(b), n)
	}
	for i := range a {
		if a[i] != b[i] {
			t.Fatalf("non-deterministic at %d: %v vs %v", i, a[i], b[i])
		}
	}
	if !sort.Float64sAreSorted(a) {
		t.Errorf("departures must be sorted ascending: %v", a)
	}
	for i, d := range a {
		if d < 0 || d > window {
			t.Errorf("departure %d = %v outside [0,%v]", i, d, window)
		}
	}
}

// TestDepartureSpreadDegenerate asserts the defined degenerate behaviors: a
// non-positive window collapses to the all-at-t=0 static batch, and n<=0 is empty.
func TestDepartureSpreadDegenerate(t *testing.T) {
	if got := departureSpread(at(8, 0), 0, 5); len(got) != 5 {
		t.Fatalf("zero window len = %d, want 5", len(got))
	} else {
		for i, d := range got {
			if d != 0 {
				t.Errorf("zero window departure %d = %v, want 0", i, d)
			}
		}
	}
	if got := departureSpread(at(8, 0), 3600, 0); len(got) != 0 {
		t.Errorf("n=0 len = %d, want 0", len(got))
	}
}
