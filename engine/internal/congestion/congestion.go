package congestion

import "github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/domain"

// LoadSnapshot is a dense, caller-owned view of per-edge load in vehicles/hour,
// indexed directly by EdgeID. EdgeIDs are dense load-time indices, so a flat
// slice indexes them O(1) without hashing and copies in one contiguous block —
// far cheaper than a map at city scale (~1–3M edges, copied once per assignment
// round). An EdgeID outside the slice (or an unknown edge) reads as 0.
type LoadSnapshot []float64

// Load returns the load (vehicles/hour) on the edge, or 0 if the edge is out of
// range / has no observation.
func (snapshot LoadSnapshot) Load(edgeID domain.EdgeID) float64 {
	if edgeID < 0 || int(edgeID) >= len(snapshot) {
		return 0
	}
	return snapshot[edgeID]
}

// LoadView is the read-only face of per-edge load: it exposes only Load, never
// the backing slice, so a holder can read load but cannot mutate or copy it out.
// LoadSnapshot satisfies LoadView (it has Load), and so does a provider's live
// backing store — that is the point: a caller that only READS load (the
// single-request Route path's congested-weight closure) can borrow a view over
// the live store instead of paying for a full owning LoadSnapshot copy. See the
// CongestionProvider.View doc for the borrow's precise lifetime/mutation
// contract.
type LoadView interface {
	// Load returns the load (vehicles/hour) on the edge, or 0 if the edge is
	// out of range / has no observation, matching LoadSnapshot.Load.
	Load(edgeID domain.EdgeID) float64
}

// Compile-time assertion: LoadSnapshot satisfies the read-only LoadView face.
var _ LoadView = LoadSnapshot(nil)

// CongestionProvider exposes the current per-edge load in vehicles/hour. The
// engine never learns the source through this boundary — a Spark-fed Kafka
// consumer, a static snapshot file, and a synthetic simulator all satisfy it.
//
// Concrete adapters live in subpackages (congestion/memory, congestion/static,
// congestion/kafka) so that import paths keep the engine core free of transport
// dependencies; the core depends only on this interface.
type CongestionProvider interface {
	// Load returns the current load (vehicles/hour) on the edge, or 0 if the
	// edge has no observation.
	Load(edgeID domain.EdgeID) float64

	// Snapshot returns a fresh LoadSnapshot that the caller owns and may mutate
	// freely without affecting the provider. Batch routing takes one Snapshot
	// per assignment round so every request in that round sees a consistent
	// view. Implementations must return a fresh copy, never their live backing
	// store.
	Snapshot() LoadSnapshot

	// View returns a read-only LoadView over the provider's CURRENT load
	// WITHOUT copying — it may alias the live backing store. It is the
	// allocation-free borrow for the read-only single-request path: at city
	// scale a dense load vector is ~1–3M floats (~8–24MB), and Snapshot copies
	// that whole vector even when the caller (the reactive congested-weight
	// closure, which only ever calls Load) never mutates it. View hands back a
	// borrow instead, so a single Route allocates none of that.
	//
	// The borrow's contract, which the caller MUST honor:
	//   (a) The caller must NOT mutate the view (LoadView exposes no mutator,
	//       and the caller must not type-assert back to the backing slice to
	//       write through it).
	//   (b) The view is valid only for the duration of one synchronous read and
	//       must NOT be retained across a concurrent mutation of the provider.
	//       A mutating provider (memory.Set, simulator.Inject/Step) writes its
	//       backing slice in place, so a borrow held across such a write could
	//       observe a torn / mid-write state. The single-shot Route contract
	//       does not hold a borrow across a mutation: it takes the view, runs
	//       one Dijkstra, and drops it within a single Route call.
	//   (c) Batch routing must NOT use View — Assign keeps taking one owning
	//       Snapshot per round. That copy's immutability is load-bearing: the
	//       per-round concurrency guarantee (project-spec.md §R5) is that every
	//       request in a batch provably sees ONE identical, stable view, which
	//       only an owning copy taken once up front guarantees against a
	//       concurrent provider mutation.
	View() LoadView
}
