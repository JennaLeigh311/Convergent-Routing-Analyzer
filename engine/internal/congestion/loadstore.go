package congestion

import "github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/domain"

// LoadStore is a mutable, dense per-edge load backing store (vehicles/hour),
// indexed directly by domain.EdgeID like LoadSnapshot. It factors out the slice
// mechanics — sizing, growing on demand, reading, and copying out an owned
// snapshot — that every mutable CongestionProvider needs, so the concrete
// adapters (congestion/memory, congestion/simulator) hold one of these and add
// only their own write policy (replace vs additive-with-floor vs per-tick
// evolution) rather than each re-implementing the grow/copy/index plumbing.
//
// It is safe for a single writer; concurrent mutation and Snapshot calls are NOT
// synchronized and must be serialized by the caller. An EdgeID outside the
// backing slice reads as 0, matching the LoadSnapshot.Load contract.
type LoadStore struct {
	loads LoadSnapshot
}

// NewLoadStore returns a LoadStore sized to hold edges 0..edgeCount-1, every one
// initially 0. edgeCount is typically the graph's EdgeCount so every valid EdgeID
// indexes the dense slice without a grow; a negative edgeCount is treated as zero
// (Set still grows on demand).
func NewLoadStore(edgeCount int) *LoadStore {
	if edgeCount < 0 {
		edgeCount = 0
	}
	return &LoadStore{loads: make(LoadSnapshot, edgeCount)}
}

// Len returns the number of dense edge slots currently backed. Callers iterate
// indices 0..Len()-1 (ascending EdgeID order) to evolve every backed edge.
func (store *LoadStore) Len() int { return len(store.loads) }

// At returns the load at a dense index in [0, Len()). It exists so a writer can
// iterate in ascending EdgeID order (e.g. a per-tick evolution) without copying
// out a snapshot; index must be in range.
func (store *LoadStore) At(index int) float64 { return store.loads[index] }

// Load returns the load (vehicles/hour) on the edge, or 0 if the edge is out of
// range or has no observation, per the LoadSnapshot.Load contract.
func (store *LoadStore) Load(edgeID domain.EdgeID) float64 { return store.loads.Load(edgeID) }

// Set replaces the load on a single edge, growing the backing slice if edgeID is
// beyond the current capacity so a store built smaller than the graph still
// accepts later edges. A negative edgeID is ignored (it can never be a valid
// dense EdgeID and would read back as 0 anyway). It is the single primitive the
// adapters build their write policies on (replace directly, or read-Load + add +
// Set for additive semantics).
func (store *LoadStore) Set(edgeID domain.EdgeID, loadVPH float64) {
	if edgeID < 0 {
		return
	}
	store.Grow(int(edgeID) + 1)
	store.loads[edgeID] = loadVPH
}

// Grow ensures the store can index at least length elements, preserving existing
// loads. It is a no-op when the slice is already large enough. Bulk writers call
// Grow once for the largest EdgeID before a loop of Sets so the slice grows a
// single time rather than re-growing per element.
func (store *LoadStore) Grow(length int) {
	if length <= len(store.loads) {
		return
	}
	grown := make(LoadSnapshot, length)
	copy(grown, store.loads)
	store.loads = grown
}

// Snapshot returns a fresh, caller-owned LoadSnapshot copy of the current
// per-edge loads. Mutating the returned snapshot does not affect the store, and a
// later Set does not affect a snapshot already handed out — the two own disjoint
// backing arrays, as the CongestionProvider port requires.
func (store *LoadStore) Snapshot() LoadSnapshot {
	out := make(LoadSnapshot, len(store.loads))
	copy(out, store.loads)
	return out
}
