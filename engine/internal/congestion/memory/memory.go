// Package memory holds the mutable in-memory congestion.CongestionProvider
// adapter. It keeps per-edge load (vehicles/hour) in a flat slice indexed by
// domain.EdgeID and lets a caller set or replace the load on an edge between
// assignment rounds. It is the simplest concrete source behind the
// CongestionProvider boundary (docs/contracts.md §3): the engine core never
// learns the load came from an in-memory store rather than a Kafka topic.
//
// The adapter lives in this subpackage — not in the congestion core — so the
// core package keeps no dependency on any concrete source, per the
// CongestionProvider port's doc comment.
package memory

import (
	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/congestion"
	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/domain"
)

// Provider is a mutable, in-memory CongestionProvider backed by a flat
// per-edge load slice (vehicles/hour) indexed directly by domain.EdgeID, the
// same dense indexing congestion.LoadSnapshot uses. It is safe for a single
// writer between assignment rounds; concurrent Set and Snapshot calls are NOT
// synchronized and must be serialized by the caller.
//
// An EdgeID outside the backing slice (or never set) reads as 0, matching the
// LoadSnapshot.Load contract for an unobserved edge.
type Provider struct {
	// loads is the live backing store of per-edge load in vehicles/hour, indexed
	// by EdgeID. Snapshot returns a fresh copy of it, never this slice itself, so
	// a caller can mutate a snapshot without disturbing the provider.
	loads congestion.LoadSnapshot
}

// New returns an in-memory Provider sized to hold edges 0..edgeCount-1, with
// every edge initially unobserved (load 0). edgeCount is typically the graph's
// EdgeCount so that every valid EdgeID indexes the dense backing slice without
// a grow. A negative edgeCount is treated as zero (an empty provider whose
// every edge reads 0); Set still grows the slice on demand.
func New(edgeCount int) *Provider {
	if edgeCount < 0 {
		edgeCount = 0
	}
	return &Provider{loads: make(congestion.LoadSnapshot, edgeCount)}
}

// Load returns the current load (vehicles/hour) on the edge, or 0 if the edge
// is out of range or has no observation, per the congestion.CongestionProvider
// port.
func (provider *Provider) Load(edgeID domain.EdgeID) float64 {
	return provider.loads.Load(edgeID)
}

// Set replaces the load (vehicles/hour) on a single edge, growing the backing
// slice if edgeID is beyond the current capacity so a provider built smaller
// than the graph still accepts later edges. A negative edgeID is ignored (it
// can never be a valid dense EdgeID and would read back as 0 anyway).
func (provider *Provider) Set(edgeID domain.EdgeID, loadVPH float64) {
	if edgeID < 0 {
		return
	}
	provider.ensure(int(edgeID) + 1)
	provider.loads[edgeID] = loadVPH
}

// SetAll bulk-replaces the load on many edges in one call. It is the
// allocation-conscious path for installing a whole round's loads at once (e.g.
// the static adapter's per-edge vph): it grows the backing slice once to fit
// the largest EdgeID rather than re-growing per Set. Negative EdgeIDs are
// skipped. When two entries name the same edge, the last one wins.
func (provider *Provider) SetAll(loads map[domain.EdgeID]float64) {
	maxIndex := 0
	for edgeID := range loads {
		if edgeID >= 0 && int(edgeID)+1 > maxIndex {
			maxIndex = int(edgeID) + 1
		}
	}
	provider.ensure(maxIndex)
	for edgeID, loadVPH := range loads {
		if edgeID < 0 {
			continue
		}
		provider.loads[edgeID] = loadVPH
	}
}

// Snapshot returns a fresh, caller-owned congestion.LoadSnapshot copy of the
// current per-edge loads. Mutating the returned snapshot does not affect the
// provider, and a later Set on the provider does not affect a snapshot already
// handed out — the two own disjoint backing arrays, as the port requires.
func (provider *Provider) Snapshot() congestion.LoadSnapshot {
	out := make(congestion.LoadSnapshot, len(provider.loads))
	copy(out, provider.loads)
	return out
}

// ensure grows the backing slice so it can index at least length elements,
// preserving existing loads. It is a no-op when the slice is already large
// enough.
func (provider *Provider) ensure(length int) {
	if length <= len(provider.loads) {
		return
	}
	grown := make(congestion.LoadSnapshot, length)
	copy(grown, provider.loads)
	provider.loads = grown
}

// Compile-time assertion: *Provider satisfies the CongestionProvider port.
var _ congestion.CongestionProvider = (*Provider)(nil)
