// Package static holds the static congestion.CongestionProvider adapter: it
// turns a batch of segment-congestion messages (the §3 schema, defined in
// domain) into a frozen per-edge load snapshot (vehicles/hour) and serves it
// through the CongestionProvider port. It is the offline/replay counterpart to
// the live Kafka consumer — same wire schema, same keep-latest dedup
// (project-spec.md §R4) and same ×12 annualization (project-spec.md §R3), but
// fed from a file or in-memory slice rather than a topic.
//
// The message type, strict decoder, and keep-latest dedup live in domain
// (domain.SegmentCongestion, DecodeSegmentCongestion, DedupKeepLatest) so this
// adapter and the future Kafka consumer share one definition; this package adds
// only the segment→edge join, the §3 value-bounds check at ingest, the ×12
// annualization, and the CongestionProvider serving. It lives in this subpackage
// — not in the congestion core — so the core keeps no dependency on the message
// schema, per the CongestionProvider port's doc comment.
package static

import (
	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/congestion"
	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/domain"
	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/graph"
)

// annualizationFactor scales a 5-minute window's vehicle_count to an hourly rate
// (vehicles/hour). A §3 window is exactly 5 minutes, so there are twelve such
// windows per hour: count × 12 = vph (project-spec.md §R3).
const annualizationFactor = 12

// SegmentEdgeIndex maps a stable §1 segment_id to its dense in-memory EdgeID.
// The graph.Graph port exposes no segment_id lookup, so the static adapter
// builds this index once by iterating the graph's edges; it is the join key
// that attaches a congestion message to a road (docs/contracts.md §3).
type SegmentEdgeIndex map[domain.SegmentID]domain.EdgeID

// BuildSegmentEdgeIndex builds a segment_id → EdgeID index by iterating every
// edge of the graph (Edge over 0..EdgeCount-1, reading .ID and .Segment). It
// adds no method to the frozen graph.Graph interface; it only uses the existing
// EdgeCount and Edge accessors. EdgeIDs are dense and contiguous, so this is a
// single linear pass. It is a setup-time projection — build it once and reuse it
// across assignment rounds rather than rebuilding per round.
func BuildSegmentEdgeIndex(roadGraph graph.Graph) SegmentEdgeIndex {
	index := make(SegmentEdgeIndex, roadGraph.EdgeCount())
	for edgeID := 0; edgeID < roadGraph.EdgeCount(); edgeID++ {
		edge, found := roadGraph.Edge(domain.EdgeID(edgeID))
		if !found {
			continue
		}
		index[edge.Segment] = edge.ID
	}
	return index
}

// Provider is a static, read-only CongestionProvider: it holds one frozen
// per-edge load store (vehicles/hour) produced from a batch of messages and
// serves the same view on every call. Use NewProvider to build one. Its
// SkippedCount reports how many deduped messages named a segment_id that does
// not map to any edge in the graph.
type Provider struct {
	// store is the frozen, owned per-edge load. Snapshot returns a fresh copy of
	// it so a caller can mutate the result without disturbing the provider.
	store *congestion.LoadStore
	// skipped is the number of post-dedup segment winners whose segment_id did
	// not map to an edge — counted, never fatal (§3: unknown segments are
	// dropped, not errors).
	skipped int
}

// NewProvider builds a static Provider from a batch of segment-congestion
// messages and a segment→edge index. It Validates every message against the §3
// value bounds (rejecting, e.g., a negative vehicle_count before it could become
// a negative load), applies the §R4 keep-latest dedup (domain.DedupKeepLatest:
// one message per segment_id, greatest (window_start, emit_time), comparing
// parsed instants, never summing overlapping windows), annualizes each surviving
// count by ×12 to vph (§R3), and writes the result onto the winning segment's
// edge. A message whose segment_id is not in the index is SKIPPED and counted,
// not fatal.
//
// edgeCount sizes the dense load snapshot; pass the graph's EdgeCount so every
// valid EdgeID indexes it directly. It returns an error if a message fails §3
// validation or carries a non-canonical/unparseable window_start/emit_time
// (which would make the dedup ordering ill-defined).
func NewProvider(messages []domain.SegmentCongestion, index SegmentEdgeIndex, edgeCount int) (*Provider, error) {
	if edgeCount < 0 {
		edgeCount = 0
	}
	for _, message := range messages {
		if err := message.Validate(); err != nil {
			return nil, err
		}
	}
	winners, err := domain.DedupKeepLatest(messages)
	if err != nil {
		return nil, err
	}

	store := congestion.NewLoadStore(edgeCount)
	skipped := 0
	for segmentID, winner := range winners {
		edgeID, found := index[domain.SegmentID(segmentID)]
		if !found || int(edgeID) < 0 || int(edgeID) >= edgeCount {
			skipped++
			continue
		}
		store.Set(edgeID, float64(winner.VehicleCount)*annualizationFactor)
	}
	return &Provider{store: store, skipped: skipped}, nil
}

// SkippedCount returns how many deduped segment winners were dropped because
// their segment_id did not map to an edge in the graph (an unknown / unmapped
// segment is skipped and counted per §3, not fatal).
func (provider *Provider) SkippedCount() int { return provider.skipped }

// Load returns the load (vehicles/hour) on the edge, or 0 if the edge is out of
// range or had no observation, per the congestion.CongestionProvider port.
func (provider *Provider) Load(edgeID domain.EdgeID) float64 {
	return provider.store.Load(edgeID)
}

// Snapshot returns a fresh, caller-owned copy of the frozen per-edge loads.
// Mutating the returned snapshot does not affect the provider, as the port
// requires.
func (provider *Provider) Snapshot() congestion.LoadSnapshot {
	return provider.store.Snapshot()
}

// View returns an allocation-free, read-only congestion.LoadView over the frozen
// per-edge loads (it does not copy), per the CongestionProvider.View contract.
// The static provider never mutates its store after construction, so for this
// adapter the borrow is stable for the provider's whole lifetime; it is still
// returned through LoadView so a caller cannot write through it.
func (provider *Provider) View() congestion.LoadView {
	return provider.store.View()
}

// Compile-time assertion: *Provider satisfies the CongestionProvider port.
var _ congestion.CongestionProvider = (*Provider)(nil)
