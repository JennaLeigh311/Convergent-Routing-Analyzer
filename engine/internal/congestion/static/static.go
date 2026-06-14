// Package static holds the static congestion.CongestionProvider adapter: it
// turns a batch of segment-congestion records (the §3 message schema) into a
// frozen per-edge load snapshot (vehicles/hour) and serves it through the
// CongestionProvider port. It is the offline/replay counterpart to the live
// Kafka consumer — same wire schema, same keep-latest dedup (project-spec.md
// §R4) and same ×12 annualization (project-spec.md §R3), but fed from a file or
// in-memory slice rather than a topic.
//
// The adapter lives in this subpackage — not in the congestion core — so the
// core keeps no dependency on JSON decoding or the message schema, per the
// CongestionProvider port's doc comment.
package static

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/congestion"
	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/domain"
	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/graph"
)

// annualizationFactor scales a 5-minute window's vehicle_count to an hourly
// rate (vehicles/hour). A §3 window is exactly 5 minutes, so there are twelve
// such windows per hour: count × 12 = vph (project-spec.md §R3).
const annualizationFactor = 12

// canonicalTSLayout is the §3 canonical wire form of window_start / emit_time:
// RFC3339 with a literal Z UTC offset and whole-second precision. time.RFC3339
// carries exactly that shape, so it parses the wire form and — by re-formatting
// and byte-comparing — asserts canonicality. A non-canonical spelling (a
// +00:00 offset, a lowercase z, or fractional seconds) would make the
// keep-latest instant comparison ill-defined (docs/contracts.md §3 "Dedup
// rule"), so it is rejected at load time rather than silently mis-ordered.
const canonicalTSLayout = time.RFC3339

// Record is the production representation of one segment-congestion message —
// exactly the ten §3 contract fields, with json tags matching the wire names.
// It deliberately carries NO field for the fixture's documentation-only `note`,
// which is row commentary, not schema; the strict loader rejects any field
// outside these ten (docs/contracts.md §3 "Fields").
type Record struct {
	SchemaVersion int     `json:"schema_version"`
	SegmentID     string  `json:"segment_id"`
	WindowStart   string  `json:"window_start"`
	WindowEnd     string  `json:"window_end"`
	VehicleCount  int     `json:"vehicle_count"`
	AvgSpeedKmh   float64 `json:"avg_speed_kmh"`
	SamplePings   int     `json:"sample_pings"`
	IsFinal       bool    `json:"is_final"`
	EmitTime      string  `json:"emit_time"`
	Producer      string  `json:"producer"`
}

// DecodeRecords STRICT-decodes a JSON array of segment-congestion messages from
// r into []Record. It uses a json.Decoder with DisallowUnknownFields, so any
// field outside the ten-field §3 schema (including the fixture's `note`, or a
// producer-only metric like `dropped_late`) makes the whole decode fail — the
// producer and this consumer cannot silently drift apart on the message shape.
//
// Unlike the test-only loader, this is production decoding of the live wire
// form: a real topic carries no `note`, so DecodeRecords does NOT strip one.
func DecodeRecords(reader io.Reader) ([]Record, error) {
	data, err := io.ReadAll(reader)
	if err != nil {
		return nil, fmt.Errorf("segment_congestion: read input: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var records []Record
	if err := decoder.Decode(&records); err != nil {
		return nil, fmt.Errorf("segment_congestion: strict decode (any field outside the 10-field §3 schema is rejected): %w", err)
	}
	return records, nil
}

// DecodeRecordsFile is a convenience wrapper around DecodeRecords that opens
// and strict-decodes the JSON record array at path.
func DecodeRecordsFile(path string) ([]Record, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("segment_congestion: open %s: %w", path, err)
	}
	defer file.Close()
	return DecodeRecords(file)
}

// SegmentEdgeIndex maps a stable §1 segment_id to its dense in-memory EdgeID.
// The graph.Graph port exposes no segment_id lookup, so the static adapter
// builds this index once by iterating the graph's edges; it is the join key
// that attaches a congestion record to a road (docs/contracts.md §3).
type SegmentEdgeIndex map[domain.SegmentID]domain.EdgeID

// BuildSegmentEdgeIndex builds a segment_id → EdgeID index by iterating every
// edge of the graph (Edge over 0..EdgeCount-1, reading .ID and .Segment). It
// adds no method to the frozen graph.Graph interface; it only uses the existing
// EdgeCount and Edge accessors. EdgeIDs are dense and contiguous, so this is a
// single linear pass.
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
// per-edge load snapshot (vehicles/hour) produced from a batch of records and
// serves the same view on every call. Use NewProvider to build one. Its
// SkippedCount reports how many deduped records named a segment_id that does
// not map to any edge in the graph.
type Provider struct {
	// snapshot is the frozen, owned per-edge load. Snapshot returns a fresh copy
	// of it so a caller can mutate the result without disturbing the provider.
	snapshot congestion.LoadSnapshot
	// skipped is the number of post-dedup segment winners whose segment_id did
	// not map to an edge — counted, never fatal (§3: unknown segments are
	// dropped, not errors).
	skipped int
}

// NewProvider builds a static Provider from a batch of segment-congestion
// records and a segment→edge index. It applies the §R4 keep-latest dedup (one
// record per segment_id — the greatest (window_start, emit_time), window_start
// primary, emit_time tiebreaker, comparing PARSED INSTANTS, never summing
// overlapping windows), annualizes each surviving count by ×12 to vph (§R3),
// and writes the result onto the winning segment's edge. A record whose
// segment_id is not in the index is SKIPPED and counted, not fatal.
//
// edgeCount sizes the dense load snapshot; pass the graph's EdgeCount so every
// valid EdgeID indexes it directly. It returns an error only if a record
// carries a non-canonical or unparseable window_start/emit_time, which would
// make the dedup ordering ill-defined.
func NewProvider(records []Record, index SegmentEdgeIndex, edgeCount int) (*Provider, error) {
	if edgeCount < 0 {
		edgeCount = 0
	}
	winners, err := keepLatest(records)
	if err != nil {
		return nil, err
	}

	snapshot := make(congestion.LoadSnapshot, edgeCount)
	skipped := 0
	for segmentID, winner := range winners {
		edgeID, found := index[domain.SegmentID(segmentID)]
		if !found || int(edgeID) < 0 || int(edgeID) >= edgeCount {
			skipped++
			continue
		}
		snapshot[edgeID] = float64(winner.VehicleCount) * annualizationFactor
	}
	return &Provider{snapshot: snapshot, skipped: skipped}, nil
}

// SkippedCount returns how many deduped segment winners were dropped because
// their segment_id did not map to an edge in the graph (an unknown / unmapped
// segment is skipped and counted per §3, not fatal).
func (provider *Provider) SkippedCount() int { return provider.skipped }

// Load returns the load (vehicles/hour) on the edge, or 0 if the edge is out of
// range or had no observation, per the congestion.CongestionProvider port.
func (provider *Provider) Load(edgeID domain.EdgeID) float64 {
	return provider.snapshot.Load(edgeID)
}

// Snapshot returns a fresh, caller-owned copy of the frozen per-edge loads.
// Mutating the returned snapshot does not affect the provider, as the port
// requires.
func (provider *Provider) Snapshot() congestion.LoadSnapshot {
	out := make(congestion.LoadSnapshot, len(provider.snapshot))
	copy(out, provider.snapshot)
	return out
}

// keepLatest applies the §R4 / §3 keep-latest dedup: per segment_id it keeps
// exactly ONE record — the one with the greatest (window_start, emit_time),
// ordered by window_start FIRST and emit_time only as the tiebreaker, comparing
// the timestamps as PARSED INSTANTS in the §3 canonical wire form. It replaces,
// never accumulates: a consumer MUST NOT sum overlapping windows, whose counts
// share pings. This is the production promotion of the test reference reducer.
func keepLatest(records []Record) (map[string]Record, error) {
	winners := make(map[string]Record, len(records))
	for _, record := range records {
		current, found := winners[record.SegmentID]
		if !found {
			if _, _, err := recordInstants(record); err != nil {
				return nil, err
			}
			winners[record.SegmentID] = record
			continue
		}
		later, err := recordLess(current, record)
		if err != nil {
			return nil, err
		}
		if later {
			winners[record.SegmentID] = record
		}
	}
	return winners, nil
}

// recordLess reports whether earlier precedes later in the §3 dedup order
// (window_start primary, emit_time tiebreaker), comparing parsed instants. It
// returns an error if either record carries a non-canonical or unparseable
// timestamp.
func recordLess(earlier, later Record) (bool, error) {
	earlierStart, earlierEmit, err := recordInstants(earlier)
	if err != nil {
		return false, err
	}
	laterStart, laterEmit, err := recordInstants(later)
	if err != nil {
		return false, err
	}
	if !earlierStart.Equal(laterStart) {
		return earlierStart.Before(laterStart), nil
	}
	return earlierEmit.Before(laterEmit), nil
}

// recordInstants parses and canonicality-checks a record's window_start and
// emit_time, returning them as instants for the dedup comparison.
func recordInstants(record Record) (windowStart, emitTime time.Time, err error) {
	windowStart, err = parseCanonicalTS("window_start", record.SegmentID, record.WindowStart)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	emitTime, err = parseCanonicalTS("emit_time", record.SegmentID, record.EmitTime)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	return windowStart, emitTime, nil
}

// parseCanonicalTS parses an RFC3339 timestamp AND asserts it is in the §3
// canonical wire form (literal Z, whole-second precision, no fractional digits,
// no +00:00, no lowercase z) by re-formatting the parsed instant and requiring
// a byte match: any non-canonical spelling round-trips to a different string.
// This is the precondition that makes the keep-latest comparison well-defined
// (docs/contracts.md §3 "Dedup rule").
func parseCanonicalTS(field, segmentID, text string) (time.Time, error) {
	timestamp, err := time.Parse(canonicalTSLayout, text)
	if err != nil {
		return time.Time{}, fmt.Errorf("segment_congestion: segment %q: %s %q is not parseable as RFC3339: %w", segmentID, field, text, err)
	}
	if got := timestamp.UTC().Format(canonicalTSLayout); got != text {
		return time.Time{}, fmt.Errorf("segment_congestion: segment %q: %s %q is not canonical RFC3339 UTC (round-trips to %q); §3 requires a literal Z, whole-second precision, no fractional digits", segmentID, field, text, got)
	}
	return timestamp, nil
}

// Compile-time assertion: *Provider satisfies the CongestionProvider port.
var _ congestion.CongestionProvider = (*Provider)(nil)
