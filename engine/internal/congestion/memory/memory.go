// Package memory holds the mutable in-memory congestion.CongestionProvider
// adapter. It keeps per-edge load (vehicles/hour) in a congestion.LoadStore
// indexed by domain.EdgeID and lets a caller set or replace the load on an edge
// between assignment rounds. It is the simplest concrete source behind the
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

// Provider is a mutable, in-memory CongestionProvider. It holds a
// congestion.LoadStore (a dense per-edge load slice, vehicles/hour) and applies
// the simplest write policy: Set REPLACES an edge's load. It is safe for a single
// writer between assignment rounds; concurrent Set and Snapshot calls are NOT
// synchronized and must be serialized by the caller.
//
// An EdgeID outside the backing store (or never set) reads as 0, matching the
// LoadSnapshot.Load contract for an unobserved edge.
type Provider struct {
	store *congestion.LoadStore
}

// New returns an in-memory Provider sized to hold edges 0..edgeCount-1, with
// every edge initially unobserved (load 0). edgeCount is typically the graph's
// EdgeCount so that every valid EdgeID indexes the dense backing store without a
// grow; a negative edgeCount is treated as zero (Set still grows on demand).
func New(edgeCount int) *Provider {
	return &Provider{store: congestion.NewLoadStore(edgeCount)}
}

// Load returns the current load (vehicles/hour) on the edge, or 0 if the edge
// is out of range or has no observation, per the congestion.CongestionProvider
// port.
func (provider *Provider) Load(edgeID domain.EdgeID) float64 {
	return provider.store.Load(edgeID)
}

// Set replaces the load (vehicles/hour) on a single edge, growing the backing
// store if edgeID is beyond the current capacity so a provider built smaller
// than the graph still accepts later edges. A negative edgeID is ignored (it
// can never be a valid dense EdgeID and would read back as 0 anyway).
func (provider *Provider) Set(edgeID domain.EdgeID, loadVPH float64) {
	provider.store.Set(edgeID, loadVPH)
}

// SetAll bulk-replaces the load on many edges in one call. It is the
// allocation-conscious path for installing a whole round's loads at once (e.g.
// the static adapter's per-edge vph): it grows the backing store once to fit the
// largest EdgeID rather than re-growing per Set. Negative EdgeIDs are skipped.
func (provider *Provider) SetAll(loads map[domain.EdgeID]float64) {
	maxIndex := 0
	for edgeID := range loads {
		if edgeID >= 0 && int(edgeID)+1 > maxIndex {
			maxIndex = int(edgeID) + 1
		}
	}
	provider.store.Grow(maxIndex)
	for edgeID, loadVPH := range loads {
		provider.store.Set(edgeID, loadVPH)
	}
}

// Snapshot returns a fresh, caller-owned congestion.LoadSnapshot copy of the
// current per-edge loads. Mutating the returned snapshot does not affect the
// provider, and a later Set on the provider does not affect a snapshot already
// handed out — the two own disjoint backing arrays, as the port requires.
func (provider *Provider) Snapshot() congestion.LoadSnapshot {
	return provider.store.Snapshot()
}

// View returns an allocation-free, read-only congestion.LoadView over the
// provider's live load (it does not copy), per the CongestionProvider.View
// contract. Because Set writes the backing slice in place, the borrow must not be
// retained across a concurrent Set; the single-request Route path drops it within
// one synchronous call, so it never does.
func (provider *Provider) View() congestion.LoadView {
	return provider.store.View()
}

// Compile-time assertion: *Provider satisfies the CongestionProvider port.
var _ congestion.CongestionProvider = (*Provider)(nil)
