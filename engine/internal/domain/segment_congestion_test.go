package domain

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"
)

// segmentCongestionFixtureDir is the path (relative to this package directory)
// of the frozen segment-congestion golden fixture. segment-congestion is the
// live, event-time-windowed congestion feed flowing from the Spark pipeline
// (producer) to this engine (consumer); the fixture is the single source of
// truth for the §3 message schema AND the keep-latest dedup ordering. Executing
// it here — rather than leaving the fixture to be eyeballed via its `note`
// strings — is what stops the future Spark producer and the engine's congestion
// adapter from silently drifting apart: if they disagree on this shape or on the
// dedup ordering, congestion attaches to the wrong window or the wrong road and
// every downstream number (the headline "26% improvement") is computed against
// garbage WITHOUT anything crashing. See docs/contracts.md §3.
const segmentCongestionFixtureDir = "../../../docs/fixtures/segment_congestion"

// The §3 "Dedup rule" requires comparing window_start and emit_time as parsed
// instants in the canonical wire form: RFC3339 with a literal `Z` UTC offset and
// whole-second precision (no fractional digits). time.RFC3339 carries exactly
// that shape (a `+00:00` offset, lowercase `z`, or fractional seconds round-trip
// to a DIFFERENT string), so we use it both to parse and — by re-formatting and
// byte-comparing against the original — to assert canonicality. A non-canonical
// timestamp would make the dedup comparison ill-defined (§3: a consumer MUST NOT
// byte-compare a timestamp it has not validated to that form).
const canonicalTSLayout = time.RFC3339

// congestionMsg mirrors the exactly-10 §3 contract fields of one
// segment-congestion message, with json tags matching the wire names. This is a
// test-only struct (no production type exists yet — the adapter is pending);
// it deliberately has NO field for the fixture's non-contract `note`, which is
// row documentation, not schema. The strict loader below proves that omission is
// enforced, not merely conventional. Field→column mapping follows
// docs/contracts.md §3 "Fields".
type congestionMsg struct {
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

// loadCongestionStrict reads example_messages.json and STRICT-decodes each row
// into a congestionMsg. The shared loadFixture helper (segmentid_test.go) uses
// non-strict json.Unmarshal, which would silently tolerate unknown fields — so
// this conformance test needs its own loader with json.Decoder +
// DisallowUnknownFields to prove the producer MUST NOT emit any field outside
// the 10-field §3 schema. The fixture's documentation-only `note` is the one
// known extra, so it is consciously stripped from each raw row BEFORE the strict
// decode; everything else (e.g. a stray `dropped_late`, which §3 pins as a
// producer-only metric and NOT a message field) must then fail to decode.
func loadCongestionStrict(test *testing.T) []congestionMsg {
	test.Helper()
	path := filepath.Join(segmentCongestionFixtureDir, "example_messages.json")
	data, err := os.ReadFile(path)
	if err != nil {
		test.Fatalf("read fixture %s: %v", path, err)
	}

	// First pass: decode into raw JSON objects so we can strip the non-contract
	// `note` documentation field before the strict schema decode. We do NOT relax
	// the decoder to tolerate `note`; we remove it, so any OTHER extra field still
	// trips DisallowUnknownFields below.
	var raws []map[string]json.RawMessage
	if err := json.Unmarshal(data, &raws); err != nil {
		test.Fatalf("unmarshal fixture %s into raw rows: %v", path, err)
	}
	if len(raws) == 0 {
		test.Fatalf("fixture %s is empty", path)
	}

	out := make([]congestionMsg, len(raws))
	for index, raw := range raws {
		delete(raw, "note") // documentation, not schema — consciously ignored.
		out[index] = decodeStrictRow(test, index, raw)
	}
	return out
}

// decodeStrictRow re-marshals one note-stripped raw row and strict-decodes it
// into a congestionMsg, failing if any field outside the 10-field §3 schema
// remains. DisallowUnknownFields only catches EXTRA fields, so we also assert
// the note-stripped row has exactly 10 keys — that pins the MISSING-field side
// (a row dropping, say, is_final or producer would otherwise decode to a zero
// value and pass silently), making "exactly the 10 §3 fields" enforced both ways.
func decodeStrictRow(test *testing.T, idx int, raw map[string]json.RawMessage) congestionMsg {
	test.Helper()
	if len(raw) != 10 {
		test.Fatalf("row %d: has %d fields after stripping `note`, want exactly 10 (§3 message shape); keys = %v",
			idx, len(raw), rawKeys(raw))
	}
	reMarshaled, err := json.Marshal(raw)
	if err != nil {
		test.Fatalf("row %d: re-marshal: %v", idx, err)
	}
	dec := json.NewDecoder(bytes.NewReader(reMarshaled))
	dec.DisallowUnknownFields()
	var msg congestionMsg
	if err := dec.Decode(&msg); err != nil {
		test.Fatalf("row %d: strict decode failed: %v", idx, err)
	}
	return msg
}

// parseCanonicalTS parses an RFC3339 timestamp AND asserts it is in the §3
// canonical wire form (literal `Z`, whole-second precision, no fractional
// digits, no `+00:00`, no lowercase `z`). It does so by re-formatting the parsed
// instant with time.RFC3339 and requiring the result to byte-match the input:
// any non-canonical spelling round-trips to a different string. This is the
// precondition that makes the dedup comparison well-defined (§3 "Dedup rule").
func parseCanonicalTS(test *testing.T, field, text string) time.Time {
	test.Helper()
	timestamp, err := time.Parse(canonicalTSLayout, text)
	if err != nil {
		test.Fatalf("%s = %q: not parseable as RFC3339: %v", field, text, err)
	}
	if got := timestamp.UTC().Format(canonicalTSLayout); got != text {
		test.Errorf("%s = %q is not canonical RFC3339 UTC (round-trips to %q); §3 requires a literal Z, whole-second precision, no fractional digits", field, text, got)
	}
	return timestamp
}

// TestSegmentCongestionStrictSchema asserts that the frozen fixture decodes
// under STRICT JSON (10 contract fields only) once the documentation-only `note`
// is stripped, and — the load-bearing half — that an 11th field would be
// REJECTED. The producer MUST NOT emit `note`, nor `dropped_late` (§3 pins it as
// a producer-only metric, never a message field); a silently-tolerated extra
// field is exactly how the producer and consumer schemas would drift.
func TestSegmentCongestionStrictSchema(test1 *testing.T) {
	msgs := loadCongestionStrict(test1)
	if len(msgs) != 7 {
		test1.Fatalf("fixture has %d rows, want 7 (the frozen §3 dedup story)", len(msgs))
	}

	// Negative case: a row carrying an 11th field MUST fail the strict decode.
	// We use `dropped_late` precisely because §3 calls it out as a producer-only
	// metric that "is NOT a message field and never appears on the topic" — so
	// proving the schema rejects it is proving the contract, not an arbitrary key.
	test1.Run("extra_field_rejected", func(test2 *testing.T) {
		// Build a fully-valid 10-field row, then add the forbidden 11th field.
		raw := map[string]json.RawMessage{
			"schema_version": json.RawMessage(`1`),
			"segment_id":     json.RawMessage(`"27583001:0:F"`),
			"window_start":   json.RawMessage(`"2026-06-08T08:00:00Z"`),
			"window_end":     json.RawMessage(`"2026-06-08T08:05:00Z"`),
			"vehicle_count":  json.RawMessage(`31`),
			"avg_speed_kmh":  json.RawMessage(`22.4`),
			"sample_pings":   json.RawMessage(`98`),
			"is_final":       json.RawMessage(`false`),
			"emit_time":      json.RawMessage(`"2026-06-08T08:05:03Z"`),
			"producer":       json.RawMessage(`"spark-structured-stream"`),
			"dropped_late":   json.RawMessage(`5`), // the forbidden 11th field.
		}
		reMarshaled, err := json.Marshal(raw)
		if err != nil {
			test2.Fatalf("re-marshal: %v", err)
		}
		dec := json.NewDecoder(bytes.NewReader(reMarshaled))
		dec.DisallowUnknownFields()
		var msg congestionMsg
		if err := dec.Decode(&msg); err == nil {
			test2.Fatal("strict decode ACCEPTED a row with an 11th field (dropped_late); §3 forbids any field outside the 10-field schema")
		}
	})
}

// TestSegmentCongestionFieldInvariants asserts the §3 per-row invariants on the
// real fixture rows: the canonical-RFC3339 timestamp form, the exactly-5-minute
// half-open window, the schema_version pin, the non-negativity / positivity
// bounds, and that segment_id parses under §1 — plus one fixture-quality gate
// (sample_pings >= vehicle_count) that is stricter than §3's "Typically".
// Each violation is silent in production (a 6-minute window or a fractional
// timestamp doesn't crash — it just corrupts the dedup ordering or the BPR
// flow), which is why every one is gated here.
func TestSegmentCongestionFieldInvariants(test1 *testing.T) {
	msgs := loadCongestionStrict(test1)
	for _, message := range msgs {
		// emit_time is appended so the rows that share a segment_id+window_start
		// (the rows-1..3 revisions of one window) get distinct subtest names
		// instead of Go's auto #01/#02 suffixes.
		test1.Run(message.SegmentID+"@"+message.WindowStart+"#"+message.EmitTime, func(test2 *testing.T) {
			// schema_version is the frozen v2 envelope version (§3 "Why v2").
			if message.SchemaVersion != 1 {
				test2.Errorf("schema_version = %d, want 1 (§3 frozen v2 envelope)", message.SchemaVersion)
			}

			// segment_id must parse under §1 — it is the §1 wire key and Kafka
			// message key; if it doesn't parse, congestion can't join to an edge.
			if _, _, _, err := ParseSegmentID(SegmentID(message.SegmentID)); err != nil {
				test2.Errorf("ParseSegmentID(%q): %v (§3 requires §1-valid segment_id)", message.SegmentID, err)
			}

			// Canonical timestamps (also returns parsed instants for the window
			// check). emit_time is exercised here too so a non-canonical emit_time
			// — which would break the dedup byte/instant comparison — is caught.
			windowStart := parseCanonicalTS(test2, "window_start", message.WindowStart)
			windowEnd := parseCanonicalTS(test2, "window_end", message.WindowEnd)
			parseCanonicalTS(test2, "emit_time", message.EmitTime)

			// Window is half-open [window_start, window_end) and EXACTLY 5 minutes
			// (§3). A drifted window length would make overlapping-window dedup and
			// the ×12 vph scaling wrong without any crash.
			if duration := windowEnd.Sub(windowStart); duration != 5*time.Minute {
				test2.Errorf("window_end - window_start = %v, want exactly 5m (§3 half-open window)", duration)
			}

			// Hard §3 field-table bounds: avg_speed_kmh is strictly > 0 and the
			// counts are >= 0.
			if message.VehicleCount < 0 {
				test2.Errorf("vehicle_count = %d, want >= 0 (§3)", message.VehicleCount)
			}
			if message.SamplePings < 0 {
				test2.Errorf("sample_pings = %d, want >= 0 (§3)", message.SamplePings)
			}
			if message.AvgSpeedKmh <= 0 {
				test2.Errorf("avg_speed_kmh = %g, want > 0 (§3)", message.AvgSpeedKmh)
			}
			// sample_pings >= vehicle_count is asserted as a FIXTURE-QUALITY gate,
			// NOT a §3 MUST: the §3 field table says sample_pings is "Typically >=
			// vehicle_count" (deliberately soft language, unlike the hard >=0 / >0
			// bounds above), so a row with fewer pings than vehicles would still be
			// contract-conformant. We hold the curated, frozen fixture to the
			// stronger relationship on purpose; because this is stricter than the
			// contract, the failure is not labeled a §3 violation.
			if message.SamplePings < message.VehicleCount {
				test2.Errorf("sample_pings (%d) < vehicle_count (%d): fixture-quality gate wants sample_pings >= vehicle_count (stricter than §3's \"Typically\")",
					message.SamplePings, message.VehicleCount)
			}
		})
	}
}

// keepLatest is the reference implementation of the §3 keep-latest dedup rule:
// per segment_id, keep exactly ONE record — the one with the greatest
// (window_start, emit_time), ordered by window_start FIRST and emit_time only as
// the tiebreaker. It compares the timestamps as PARSED INSTANTS (the form §3
// makes authoritative), not as raw strings. Returning one record per segment is
// the whole point: a consumer MUST NOT sum overlapping windows (their counts
// share pings), so the reducer replaces, never accumulates.
func keepLatest(test *testing.T, msgs []congestionMsg) map[string]congestionMsg {
	test.Helper()
	winners := make(map[string]congestionMsg, len(msgs))
	for _, message := range msgs {
		cur, found := winners[message.SegmentID]
		if !found || congestionLess(test, cur, message) {
			winners[message.SegmentID] = message
		}
	}
	return winners
}

// congestionLess reports whether a precedes b in the §3 dedup order
// (window_start primary, emit_time tiebreaker), comparing parsed instants.
func congestionLess(test *testing.T, message1, message2 congestionMsg) bool {
	test.Helper()
	aws := parseCanonicalTS(test, "window_start", message1.WindowStart)
	bws := parseCanonicalTS(test, "window_start", message2.WindowStart)
	if !aws.Equal(bws) {
		return aws.Before(bws)
	}
	aet := parseCanonicalTS(test, "emit_time", message1.EmitTime)
	bet := parseCanonicalTS(test, "emit_time", message2.EmitTime)
	return aet.Before(bet)
}

// TestSegmentCongestionDedupKeepLatest runs the reference keep-latest reducer
// over all 7 fixture rows and asserts the per-segment winners §3 mandates. This
// is the heart of the conformance test: it pins (a) the window_start-PRIMARY
// ordering via the row3-vs-row4 case, (b) the provisional→revision→final
// collapse, and (c) per-segment :F/:R independence — each a silent-failure mode
// if a naive implementation gets it wrong.
func TestSegmentCongestionDedupKeepLatest(test1 *testing.T) {
	msgs := loadCongestionStrict(test1)
	winners := keepLatest(test1, msgs)

	// The §3-mandated final per-segment winners, keyed by segment_id. Values are
	// the distinguishing vehicle_count of the row that MUST survive dedup. (The
	// fixture's `note` strings spell out each row; these assertions make them
	// executable.)
	wantCount := map[string]int{
		"27583001:0:F":  35, // row 4 — window_start 08:01 beats row 3's 08:00 final.
		"48800123:0:R":  12, // row 5 — reverse direction, kept independently.
		"48800123:0:F":  18, // row 6 — forward direction, kept independently.
		"123456789:2:F": 42, // row 7 — lone multi-seq record (the §3 example id).
	}

	// Exactly these 4 segments must survive; the 7 input rows collapse to 4
	// records. An extra surviving segment would mean a segment_id mismatch; a
	// missing one would mean over-aggressive collapse.
	if len(winners) != len(wantCount) {
		gotIDs := make([]string, 0, len(winners))
		for identifier1 := range winners {
			gotIDs = append(gotIDs, identifier1)
		}
		sort.Strings(gotIDs)
		test1.Fatalf("dedup produced %d segments %v, want %d %v",
			len(winners), gotIDs, len(wantCount), keysOf(wantCount))
	}

	for identifier2, want := range wantCount {
		message1, found := winners[identifier2]
		if !found {
			test1.Errorf("segment %q missing from dedup winners", identifier2)
			continue
		}
		if message1.VehicleCount != want {
			test1.Errorf("segment %q dedup winner vehicle_count = %d, want %d", identifier2, message1.VehicleCount, want)
		}
	}

	// (a) window_start-PRIMARY — the explicit row3-vs-row4 case, named so the
	// intent is legible. Row 3 (window_start 08:00, emit_time 08:07:05, is_final)
	// has a LATER emit_time than row 4 (window_start 08:01, emit_time 08:06:03,
	// provisional), yet row 4 MUST win because window_start sorts first. This is
	// the guard against a naive "latest emit_time wins" reducer: such an impl
	// would wrongly keep row 3 (count 39) and pin a stale, value-older window as
	// the segment's live load.
	test1.Run("window_start_primary_row3_vs_row4", func(test2 *testing.T) {
		message2 := winners["27583001:0:F"]
		if message2.WindowStart != "2026-06-08T08:01:00Z" {
			test2.Errorf("27583001:0:F winner window_start = %q, want 2026-06-08T08:01:00Z (row 4; window_start is the PRIMARY dedup key, beating row 3's later emit_time 08:07:05)", message2.WindowStart)
		}
		if message2.VehicleCount != 35 {
			test2.Errorf("27583001:0:F winner vehicle_count = %d, want 35 (row 4); a 'latest emit_time wins' impl would wrongly keep row 3 (39)", message2.VehicleCount)
		}
		if message2.IsFinal {
			test2.Error("27583001:0:F winner is_final = true, want false: the freshest window (row 4) is provisional and STILL wins (§3 'freshest window wins')")
		}
	})

	// (b) provisional→revision→final collapse — rows 1→2→3 all share segment
	// 27583001:0:F and window [08:00,08:05); they MUST collapse so that, among
	// just those three, row 3 (the is_final, latest emit_time) is the within-
	// window winner. We assert this on the rows-1..3 subset directly so the
	// collapse is pinned independently of row 4's newer window superseding it.
	test1.Run("provisional_revision_final_collapse", func(test3 *testing.T) {
		win0805 := filterWindow(msgs, "27583001:0:F", "2026-06-08T08:00:00Z")
		if len(win0805) != 3 {
			test3.Fatalf("expected 3 rows for 27583001:0:F window 08:00, got %d", len(win0805))
		}
		sub := keepLatest(test3, win0805)
		message3 := sub["27583001:0:F"]
		if message3.VehicleCount != 39 || !message3.IsFinal {
			test3.Errorf("rows 1→2→3 collapse to vehicle_count=%d is_final=%v, want 39/true (row 3, the final, latest-emit_time record)", message3.VehicleCount, message3.IsFinal)
		}
		// A consumer MUST NOT sum the three provisional/revision/final emissions
		// (31+37+39): that double-counts the revisions of one window.
		if message3.VehicleCount == 31+37+39 {
			test3.Error("rows 1→2→3 were SUMMED (107); §3 forbids summing successive emissions for one window")
		}
	})

	// (c) :F/:R independence — the two directions of two-way way 48800123 are
	// DISTINCT segment_ids and carry independent counts; dedup MUST keep both.
	// Collapsing them (e.g. keying on osm_way_id instead of the full segment_id)
	// would silently merge a road's two directions' congestion.
	test1.Run("forward_reverse_independence", func(test4 *testing.T) {
		message4, okF := winners["48800123:0:F"]
		message5, okR := winners["48800123:0:R"]
		if !okF || !okR {
			test4.Fatalf("expected both 48800123:0:F and :0:R to survive (F=%v R=%v)", okF, okR)
		}
		if message4.VehicleCount != 18 || message5.VehicleCount != 12 {
			test4.Errorf(":F count=%d (want 18), :R count=%d (want 12); the two directions must stay distinct", message4.VehicleCount, message5.VehicleCount)
		}
		if message4.VehicleCount == message5.VehicleCount {
			test4.Error(":F and :R have equal counts — directions appear to have been merged")
		}
	})
}

// filterWindow returns the fixture rows matching a given segment_id AND
// window_start — used to isolate the rows-1..3 collapse subset.
func filterWindow(msgs []congestionMsg, segmentID, windowStart string) []congestionMsg {
	var out []congestionMsg
	for _, message := range msgs {
		if message.SegmentID == segmentID && message.WindowStart == windowStart {
			out = append(out, message)
		}
	}
	return out
}

// keysOf returns the sorted keys of a map, for legible failure messages.
func keysOf(fields map[string]int) []string {
	out := make([]string, 0, len(fields))
	for key := range fields {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}

// rawKeys returns the sorted keys of a raw row, for legible failure messages
// when the field count is off.
func rawKeys(fields map[string]json.RawMessage) []string {
	out := make([]string, 0, len(fields))
	for key := range fields {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}
