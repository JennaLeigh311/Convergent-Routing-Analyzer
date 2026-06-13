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
}
