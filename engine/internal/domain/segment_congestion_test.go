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
// it here — against the SAME production decoder and dedup reducer the static and
// future Kafka adapters use (DecodeSegmentCongestion / DedupKeepLatest) — is what
// stops the producer and the consumers from silently drifting apart: if they
// disagree on this shape or on the dedup ordering, congestion attaches to the
// wrong window or the wrong road and every downstream number (the headline "26%
// improvement") is computed against garbage WITHOUT anything crashing. See
// docs/contracts.md §3.
const segmentCongestionFixtureDir = "../../../docs/fixtures/segment_congestion"

// loadCongestionStrict reads example_messages.json, strips each row's
// documentation-only `note`, asserts each stripped row has exactly the ten §3
// fields, and STRICT-decodes the corpus through the production
// DecodeSegmentCongestion. The exactly-ten-keys check pins the MISSING-field side
// (a row dropping, say, is_final or producer would otherwise decode to a zero
// value and pass silently); DisallowUnknownFields inside DecodeSegmentCongestion
// pins the EXTRA-field side. The fixture's documentation-only `note` is the one
// known extra, consciously stripped before the strict decode; any OTHER extra
// field still trips the decode.
func loadCongestionStrict(test *testing.T) []SegmentCongestion {
	test.Helper()
	path := filepath.Join(segmentCongestionFixtureDir, "example_messages.json")
	data, err := os.ReadFile(path)
	if err != nil {
		test.Fatalf("read fixture %s: %v", path, err)
	}

	var raws []map[string]json.RawMessage
	if err := json.Unmarshal(data, &raws); err != nil {
		test.Fatalf("unmarshal fixture %s into raw rows: %v", path, err)
	}
	if len(raws) == 0 {
		test.Fatalf("fixture %s is empty", path)
	}
	for index, raw := range raws {
		delete(raw, "note") // documentation, not schema — consciously ignored.
		if len(raw) != 10 {
			test.Fatalf("row %d: has %d fields after stripping `note`, want exactly 10 (§3 message shape); keys = %v",
				index, len(raw), rawKeys(raw))
		}
	}

	stripped, err := json.Marshal(raws)
	if err != nil {
		test.Fatalf("re-marshal note-stripped rows: %v", err)
	}
	messages, err := DecodeSegmentCongestion(bytes.NewReader(stripped))
	if err != nil {
		test.Fatalf("DecodeSegmentCongestion(note-stripped fixture): %v", err)
	}
	return messages
}

// parseCanonicalTS adapts the production parseCanonicalTimestamp to a test
// helper that fails the test on a non-canonical/unparseable value. It delegates
// to the production parser so the test asserts the SAME canonicality rule the
// adapters enforce, rather than shadowing a second copy of it.
func parseCanonicalTS(test *testing.T, field, text string) time.Time {
	test.Helper()
	timestamp, err := parseCanonicalTimestamp(field, "", text)
	if err != nil {
		test.Errorf("%v", err)
		return time.Time{}
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
		raw := []map[string]json.RawMessage{{
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
		}}
		encoded, err := json.Marshal(raw)
		if err != nil {
			test2.Fatalf("marshal: %v", err)
		}
		if _, err := DecodeSegmentCongestion(bytes.NewReader(encoded)); err == nil {
			test2.Fatal("DecodeSegmentCongestion ACCEPTED a row with an 11th field (dropped_late); §3 forbids any field outside the 10-field schema")
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
			// The §3 value bounds (schema_version, non-negative counts, positive
			// speed) are exactly what SegmentCongestion.Validate enforces at ingest;
			// assert the fixture passes the production validator.
			if err := message.Validate(); err != nil {
				test2.Errorf("Validate: %v", err)
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

			// sample_pings >= vehicle_count is asserted as a FIXTURE-QUALITY gate,
			// NOT a §3 MUST: the §3 field table says sample_pings is "Typically >=
			// vehicle_count" (deliberately soft language, unlike the hard >=0 / >0
			// bounds Validate enforces), so a row with fewer pings than vehicles
			// would still be contract-conformant. We hold the curated, frozen
			// fixture to the stronger relationship on purpose; because this is
			// stricter than the contract, the failure is not labeled a §3 violation.
			if message.SamplePings < message.VehicleCount {
				test2.Errorf("sample_pings (%d) < vehicle_count (%d): fixture-quality gate wants sample_pings >= vehicle_count (stricter than §3's \"Typically\")",
					message.SamplePings, message.VehicleCount)
			}
		})
	}
}

// TestSegmentCongestionDedupKeepLatest runs the production DedupKeepLatest over
// all 7 fixture rows and asserts the per-segment winners §3 mandates. This is the
// heart of the conformance test: it pins (a) the window_start-PRIMARY ordering
// via the row3-vs-row4 case, (b) the provisional→revision→final collapse, and
// (c) per-segment :F/:R independence — each a silent-failure mode if a naive
// implementation gets it wrong.
func TestSegmentCongestionDedupKeepLatest(test1 *testing.T) {
	msgs := loadCongestionStrict(test1)
	winners, err := DedupKeepLatest(msgs)
	if err != nil {
		test1.Fatalf("DedupKeepLatest: %v", err)
	}

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
		sub, err := DedupKeepLatest(win0805)
		if err != nil {
			test3.Fatalf("DedupKeepLatest(subset): %v", err)
		}
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

// TestSegmentCongestionValidate asserts the §3 value bounds enforced by
// SegmentCongestion.Validate independently of the fixture: a conformant record
// passes, and each violated bound (schema_version, vehicle_count, sample_pings,
// avg_speed_kmh) is rejected.
func TestSegmentCongestionValidate(test1 *testing.T) {
	valid := SegmentCongestion{
		SchemaVersion: 1,
		SegmentID:     "27583001:0:F",
		WindowStart:   "2026-06-08T08:00:00Z",
		WindowEnd:     "2026-06-08T08:05:00Z",
		VehicleCount:  31,
		AvgSpeedKmh:   22.4,
		SamplePings:   98,
		IsFinal:       false,
		EmitTime:      "2026-06-08T08:05:03Z",
		Producer:      "spark-structured-stream",
	}
	if err := valid.Validate(); err != nil {
		test1.Errorf("Validate(conformant record) = %v, want nil", err)
	}

	testCases := []struct {
		name   string
		mutate func(*SegmentCongestion)
	}{
		{name: "wrong schema_version", mutate: func(record *SegmentCongestion) { record.SchemaVersion = 2 }},
		{name: "negative vehicle_count", mutate: func(record *SegmentCongestion) { record.VehicleCount = -1 }},
		{name: "negative sample_pings", mutate: func(record *SegmentCongestion) { record.SamplePings = -1 }},
		{name: "zero avg_speed_kmh", mutate: func(record *SegmentCongestion) { record.AvgSpeedKmh = 0 }},
	}
	for _, testCase := range testCases {
		test1.Run(testCase.name, func(test2 *testing.T) {
			invalid := valid
			testCase.mutate(&invalid)
			if err := invalid.Validate(); err == nil {
				test2.Errorf("Validate(%s) = nil, want an error (§3 bound)", testCase.name)
			}
		})
	}
}

// TestDecodeSegmentCongestionFileMissing asserts the file wrapper surfaces an
// open error for a path that does not exist, rather than panicking or returning
// nil records.
func TestDecodeSegmentCongestionFileMissing(test1 *testing.T) {
	if _, err := DecodeSegmentCongestionFile(filepath.Join(test1.TempDir(), "does-not-exist.json")); err == nil {
		test1.Fatal("DecodeSegmentCongestionFile(missing path) = nil error, want an open error")
	}
}

// filterWindow returns the fixture rows matching a given segment_id AND
// window_start — used to isolate the rows-1..3 collapse subset.
func filterWindow(msgs []SegmentCongestion, segmentID, windowStart string) []SegmentCongestion {
	var out []SegmentCongestion
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
