package congestion_test

import (
	"testing"

	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/congestion"
	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/domain"
)

// TestLoadStoreSetAndLoad asserts Set installs a per-edge load that Load reads
// back, and that an unset / out-of-range / negative EdgeID reads as 0.
func TestLoadStoreSetAndLoad(test1 *testing.T) {
	store := congestion.NewLoadStore(8)
	store.Set(3, 420)

	testCases := []struct {
		name   string
		edgeID domain.EdgeID
		want   float64
	}{
		{name: "set edge reads its load", edgeID: 3, want: 420},
		{name: "unset edge reads 0", edgeID: 5, want: 0},
		{name: "out-of-range edge reads 0", edgeID: 999, want: 0},
		{name: "negative edge reads 0", edgeID: -1, want: 0},
	}
	for _, testCase := range testCases {
		test1.Run(testCase.name, func(test2 *testing.T) {
			if got := store.Load(testCase.edgeID); got != testCase.want {
				test2.Errorf("Load(%d) = %v, want %v", testCase.edgeID, got, testCase.want)
			}
		})
	}
}

// TestLoadStoreNegativeEdgeCountIsEmpty asserts a negative edgeCount yields an
// empty store whose every edge reads 0, and that Set still grows it on demand.
func TestLoadStoreNegativeEdgeCountIsEmpty(test1 *testing.T) {
	store := congestion.NewLoadStore(-5)
	if got := store.Len(); got != 0 {
		test1.Fatalf("Len() = %d, want 0 for a negative edgeCount", got)
	}
	store.Set(2, 144)
	if got := store.Load(2); got != 144 {
		test1.Errorf("Load(2) after growing Set = %v, want 144", got)
	}
}

// TestLoadStoreSetGrows asserts Set on an EdgeID beyond the current size grows
// the store (preserving existing loads) while a negative EdgeID is ignored.
func TestLoadStoreSetGrows(test1 *testing.T) {
	store := congestion.NewLoadStore(2)
	store.Set(0, 100)
	store.Set(50, 144) // beyond the initial size of 2.
	if got := store.Load(0); got != 100 {
		test1.Errorf("Load(0) after growing = %v, want 100 (existing load preserved)", got)
	}
	if got := store.Load(50); got != 144 {
		test1.Errorf("Load(50) after growing Set = %v, want 144", got)
	}
	if store.Len() < 51 {
		test1.Errorf("Len() = %d, want >= 51 after Set(50)", store.Len())
	}
	store.Set(-1, 999) // ignored.
	if got := store.Load(-1); got != 0 {
		test1.Errorf("Load(-1) = %v, want 0", got)
	}
}

// TestLoadStoreAtIteratesAscending asserts At reads the dense slots in EdgeID
// order, the access pattern adapters rely on for deterministic per-tick evolution.
func TestLoadStoreAtIteratesAscending(test1 *testing.T) {
	store := congestion.NewLoadStore(4)
	store.Set(1, 10)
	store.Set(3, 30)
	want := []float64{0, 10, 0, 30}
	if store.Len() != len(want) {
		test1.Fatalf("Len() = %d, want %d", store.Len(), len(want))
	}
	for index := 0; index < store.Len(); index++ {
		if got := store.At(index); got != want[index] {
			test1.Errorf("At(%d) = %v, want %v", index, got, want[index])
		}
	}
}

// TestLoadStoreSnapshotIsIndependentCopy asserts the ownership contract from both
// directions: mutating a returned snapshot must not affect the store, and a later
// Set must not affect a snapshot already handed out.
func TestLoadStoreSnapshotIsIndependentCopy(test1 *testing.T) {
	store := congestion.NewLoadStore(4)
	store.Set(2, 420)

	snapshot := store.Snapshot()
	if got := snapshot.Load(2); got != 420 {
		test1.Fatalf("snapshot.Load(2) = %v, want 420", got)
	}

	snapshot[2] = 0
	if got := store.Load(2); got != 420 {
		test1.Errorf("store.Load(2) after mutating snapshot = %v, want 420 (snapshot must be a copy)", got)
	}

	store.Set(2, 999)
	if got := snapshot.Load(2); got != 0 {
		test1.Errorf("snapshot.Load(2) after later Set = %v, want 0 (store must not alias the snapshot)", got)
	}
}

// TestLoadStoreViewAliasesLiveStore asserts View() returns a read-only borrow over
// the LIVE backing slice — the allocation-free path issue #67 adds and the inverse
// of Snapshot's owning copy. It reads the current load, and because it aliases
// rather than copies, a later in-place Set is visible through the SAME borrow. (A
// Set that grows the slice reallocates and detaches the borrow, which is why the
// View contract forbids retaining it across a mutation; this test stays within the
// initial capacity to exercise the aliasing the borrow is built on.)
func TestLoadStoreViewAliasesLiveStore(test1 *testing.T) {
	store := congestion.NewLoadStore(8)
	store.Set(3, 420)

	view := store.View()
	if got := view.Load(3); got != 420 {
		test1.Errorf("View().Load(3) = %v, want 420", got)
	}
	// An in-place Set within capacity is visible through the existing borrow: it
	// aliases the live slice, it did not copy it.
	store.Set(3, 900)
	if got := view.Load(3); got != 900 {
		test1.Errorf("View().Load(3) after in-place Set = %v, want 900 (the borrow aliases the live store)", got)
	}
	// View agrees with Snapshot at the same edge — both faces report one load.
	if got, want := view.Load(3), store.Snapshot().Load(3); got != want {
		test1.Errorf("View().Load(3) = %v, want it to match Snapshot().Load(3) = %v", got, want)
	}
}
