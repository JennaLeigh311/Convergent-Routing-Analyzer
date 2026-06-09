package congestion

import "github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/domain"

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
	Load(id domain.EdgeID) float64

	// Snapshot returns an immutable copy of all known edge loads. Batch routing
	// takes one Snapshot per assignment round so every request in that round
	// sees a consistent view.
	Snapshot() map[domain.EdgeID]float64
}
