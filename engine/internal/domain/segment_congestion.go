package domain

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"
)

// expectedSchemaVersion is the frozen §3 segment-congestion envelope version. A
// message carrying any other version is a producer/consumer schema drift and is
// rejected by Validate (docs/contracts.md §3 "Why v2").
const expectedSchemaVersion = 1

// canonicalTimestampLayout is the §3 canonical wire form of a segment-congestion
// window_start / emit_time: RFC3339 with a literal Z UTC offset and whole-second
// precision. time.RFC3339 carries exactly that shape, so it both parses the wire
// form and — by re-formatting and byte-comparing — asserts canonicality. A
// non-canonical spelling (a +00:00 offset, a lowercase z, or fractional seconds)
// would make the keep-latest instant comparison ill-defined (docs/contracts.md
// §3 "Dedup rule"), so it is rejected rather than silently mis-ordered.
const canonicalTimestampLayout = time.RFC3339

// SegmentCongestion is the canonical representation of one segment-congestion
// message — exactly the ten §3 contract fields, with json tags matching the wire
// names. It deliberately carries NO field for the fixture's documentation-only
// `note`, which is row commentary, not schema; the strict decoder rejects any
// field outside these ten (docs/contracts.md §3 "Fields").
//
// This type, the strict decoder, and the keep-latest dedup live in domain — the
// base layer that already owns the §1 segment_id contract (segmentid.go) — so
// every consumer of the §3 feed (the static replay adapter, the future Kafka
// consumer, the conformance test) shares ONE definition of the message shape and
// the dedup ordering, rather than each forking a copy that can silently drift.
type SegmentCongestion struct {
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

// Validate checks the §3 value bounds the contract declares: the frozen
// schema_version, a non-negative vehicle_count and sample_pings, and a strictly
// positive avg_speed_kmh (docs/contracts.md §3 "Fields"). Strict JSON decoding
// guards the message SHAPE; Validate guards the message VALUES, so a malformed
// count (e.g. a negative vehicle_count that would annualize to a negative load
// and break the router's edge-weight non-negativity assumption) is rejected at
// ingest rather than propagated into a snapshot. It does not parse the
// timestamps; DedupKeepLatest does that, since it needs the parsed instants.
func (message SegmentCongestion) Validate() error {
	if message.SchemaVersion != expectedSchemaVersion {
		return fmt.Errorf("segment_congestion: segment %q: schema_version = %d, want %d (§3 frozen envelope)", message.SegmentID, message.SchemaVersion, expectedSchemaVersion)
	}
	if message.VehicleCount < 0 {
		return fmt.Errorf("segment_congestion: segment %q: vehicle_count = %d, want >= 0 (§3)", message.SegmentID, message.VehicleCount)
	}
	if message.SamplePings < 0 {
		return fmt.Errorf("segment_congestion: segment %q: sample_pings = %d, want >= 0 (§3)", message.SegmentID, message.SamplePings)
	}
	if message.AvgSpeedKmh <= 0 {
		return fmt.Errorf("segment_congestion: segment %q: avg_speed_kmh = %g, want > 0 (§3)", message.SegmentID, message.AvgSpeedKmh)
	}
	return nil
}

// DecodeSegmentCongestion STRICT-decodes a JSON array of segment-congestion
// messages from reader into []SegmentCongestion. It uses a json.Decoder with
// DisallowUnknownFields, so any field outside the ten-field §3 schema (the
// fixture's documentation-only `note`, or a producer-only metric like
// `dropped_late`) makes the whole decode fail — the producer and its consumers
// cannot silently drift apart on the message shape. It decodes the wire SHAPE
// only; call Validate to enforce the value bounds.
func DecodeSegmentCongestion(reader io.Reader) ([]SegmentCongestion, error) {
	data, err := io.ReadAll(reader)
	if err != nil {
		return nil, fmt.Errorf("segment_congestion: read input: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var messages []SegmentCongestion
	if err := decoder.Decode(&messages); err != nil {
		return nil, fmt.Errorf("segment_congestion: strict decode (any field outside the 10-field §3 schema is rejected): %w", err)
	}
	return messages, nil
}

// DecodeSegmentCongestionFile is a convenience wrapper around
// DecodeSegmentCongestion that opens and strict-decodes the JSON array at path.
func DecodeSegmentCongestionFile(path string) ([]SegmentCongestion, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("segment_congestion: open %s: %w", path, err)
	}
	defer file.Close()
	return DecodeSegmentCongestion(file)
}

// DedupKeepLatest applies the §R4 / §3 keep-latest dedup: per segment_id it keeps
// exactly ONE message — the one with the greatest (window_start, emit_time),
// ordered by window_start FIRST and emit_time only as the tiebreaker, comparing
// the timestamps as PARSED INSTANTS in the §3 canonical wire form. It replaces,
// never accumulates: a consumer MUST NOT sum overlapping windows, whose counts
// share pings. It returns an error if any message carries a non-canonical or
// unparseable window_start/emit_time, which would make the ordering ill-defined.
//
// Each message's timestamps are parsed exactly once (cached alongside the current
// winner), so a segment with many revisions costs one parse per message rather
// than re-parsing the winner on every comparison.
func DedupKeepLatest(messages []SegmentCongestion) (map[string]SegmentCongestion, error) {
	type timed struct {
		message     SegmentCongestion
		windowStart time.Time
		emitTime    time.Time
	}
	winners := make(map[string]timed, len(messages))
	for _, message := range messages {
		windowStart, emitTime, err := congestionInstants(message)
		if err != nil {
			return nil, err
		}
		current, found := winners[message.SegmentID]
		if !found || congestionLess(current.windowStart, current.emitTime, windowStart, emitTime) {
			winners[message.SegmentID] = timed{message: message, windowStart: windowStart, emitTime: emitTime}
		}
	}
	out := make(map[string]SegmentCongestion, len(winners))
	for segmentID, winner := range winners {
		out[segmentID] = winner.message
	}
	return out, nil
}

// congestionLess reports whether (earlierStart, earlierEmit) precedes
// (laterStart, laterEmit) in the §3 dedup order: window_start primary, emit_time
// tiebreaker.
func congestionLess(earlierStart, earlierEmit, laterStart, laterEmit time.Time) bool {
	if !earlierStart.Equal(laterStart) {
		return earlierStart.Before(laterStart)
	}
	return earlierEmit.Before(laterEmit)
}

// congestionInstants parses and canonicality-checks a message's window_start and
// emit_time, returning them as instants for the keep-latest comparison.
func congestionInstants(message SegmentCongestion) (windowStart, emitTime time.Time, err error) {
	windowStart, err = parseCanonicalTimestamp("window_start", message.SegmentID, message.WindowStart)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	emitTime, err = parseCanonicalTimestamp("emit_time", message.SegmentID, message.EmitTime)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	return windowStart, emitTime, nil
}

// parseCanonicalTimestamp parses an RFC3339 timestamp AND asserts it is in the §3
// canonical wire form (literal Z, whole-second precision, no fractional digits,
// no +00:00, no lowercase z) by re-formatting the parsed instant and requiring a
// byte match: any non-canonical spelling round-trips to a different string. This
// is the precondition that makes the keep-latest comparison well-defined
// (docs/contracts.md §3 "Dedup rule"). segmentID is used only for error context.
func parseCanonicalTimestamp(field, segmentID, text string) (time.Time, error) {
	timestamp, err := time.Parse(canonicalTimestampLayout, text)
	if err != nil {
		return time.Time{}, fmt.Errorf("segment_congestion: segment %q: %s %q is not parseable as RFC3339: %w", segmentID, field, text, err)
	}
	if got := timestamp.UTC().Format(canonicalTimestampLayout); got != text {
		return time.Time{}, fmt.Errorf("segment_congestion: segment %q: %s %q is not canonical RFC3339 UTC (round-trips to %q); §3 requires a literal Z, whole-second precision, no fractional digits", segmentID, field, text, got)
	}
	return timestamp, nil
}
