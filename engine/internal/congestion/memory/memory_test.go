package memory_test

import (
	"testing"

	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/congestion/memory"
	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/domain"
)

// TestSetRaisesLoad asserts that Set installs a per-edge load that Load reads
// back, and that an unobserved / out-of-range / negative EdgeID reads as 0 per
// the LoadSnapshot.Load contract.
func TestSetRaisesLoad(test1 *testing.T) {
	provider := memory.New(8)
	provider.Set(3, 420)

	testCases := []struct {
		name   string
		edgeID domain.EdgeID
		want   float64
	}{
		{name: "set edge reads its load", edgeID: 3, want: 420},
		{name: "unobserved edge reads 0", edgeID: 5, want: 0},
		{name: "out-of-range edge reads 0", edgeID: 999, want: 0},
		{name: "negative edge reads 0", edgeID: -1, want: 0},
	}
	for _, testCase := range testCases {
		test1.Run(testCase.name, func(test2 *testing.T) {
			if got := provider.Load(testCase.edgeID); got != testCase.want {
				test2.Errorf("Load(%d) = %v, want %v", testCase.edgeID, got, testCase.want)
			}
		})
	}
}

// TestSetGrowsBackingStore asserts that Set on an EdgeID beyond the initial
// size grows the provider rather than dropping the load, while a negative
// EdgeID is ignored.
func TestSetGrowsBackingStore(test1 *testing.T) {
	provider := memory.New(2)
	provider.Set(50, 144) // beyond the initial size of 2.
	if got := provider.Load(50); got != 144 {
		test1.Errorf("Load(50) after growing Set = %v, want 144", got)
	}
	provider.Set(-1, 999) // ignored; must not panic or appear anywhere.
	if got := provider.Load(-1); got != 0 {
		test1.Errorf("Load(-1) = %v, want 0", got)
	}
}

// TestSetAllBulk asserts that SetAll installs many edges' loads in one call,
// growing the store to fit the largest EdgeID.
func TestSetAllBulk(test1 *testing.T) {
	provider := memory.New(0)
	provider.SetAll(map[domain.EdgeID]float64{
		1:  216,
		4:  504,
		20: 144,
	})
	want := map[domain.EdgeID]float64{1: 216, 4: 504, 20: 144, 0: 0, 5: 0}
	for edgeID, expected := range want {
		if got := provider.Load(edgeID); got != expected {
			test1.Errorf("Load(%d) = %v, want %v", edgeID, got, expected)
		}
	}
}

// TestSnapshotIsIndependentCopy asserts the port's ownership contract from both
// directions: mutating a returned snapshot must not affect the provider, and a
// later Set on the provider must not affect a snapshot already handed out.
func TestSnapshotIsIndependentCopy(test1 *testing.T) {
	provider := memory.New(4)
	provider.Set(2, 420)

	snapshot := provider.Snapshot()
	if got := snapshot.Load(2); got != 420 {
		test1.Fatalf("snapshot.Load(2) = %v, want 420", got)
	}

	// Mutating the snapshot must not affect the provider.
	snapshot[2] = 0
	if got := provider.Load(2); got != 420 {
		test1.Errorf("provider.Load(2) after mutating snapshot = %v, want 420 (snapshot must be a copy)", got)
	}

	// A later Set on the provider must not affect the already-handed-out snapshot.
	provider.Set(2, 999)
	if got := snapshot.Load(2); got != 0 {
		test1.Errorf("snapshot.Load(2) after later provider.Set = %v, want 0 (provider must not alias the snapshot)", got)
	}
}
