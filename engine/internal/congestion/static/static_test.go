package static_test

import (
	"bytes"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/congestion/static"
	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/domain"
	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/graph"
)

// tolerance is the float comparison tolerance for vph assertions (house style).
const tolerance = 1e-9

// fixturePath is the frozen segment-congestion golden corpus (7 rows), relative
// to this package directory. It is the same artifact the domain conformance
// test exercises; here we assert the post-dedup, post-annualize vph the static
// adapter produces on the mapped edges (docs/contracts.md §3).
var fixturePath = filepath.Join("..", "..", "..", "..", "docs", "fixtures", "segment_congestion", "example_messages.json")

// testGraph builds a tiny AdjacencyGraph whose edges carry the given
// segment_ids in EdgeID order. It constructs edges directly via graph.New (no
// GeoJSON parsing needed) — two nodes and one self-describing edge per
// segment_id is the least-effort way to get a segment→EdgeID mapping to test
// against. EdgeID i carries segmentIDs[i].
func testGraph(test *testing.T, segmentIDs ...domain.SegmentID) *graph.AdjacencyGraph {
	test.Helper()
	nodes := []graph.Node{
		{ID: 0, Pos: domain.LatLon{Lat: 0, Lon: 0}},
		{ID: 1, Pos: domain.LatLon{Lat: 0, Lon: 1}},
	}
	edges := make([]graph.Edge, len(segmentIDs))
	for index, segmentID := range segmentIDs {
		edges[index] = graph.Edge{
			ID:          domain.EdgeID(index),
			Segment:     segmentID,
			From:        0,
			To:          1,
			LengthM:     100,
			FreeFlowS:   10,
			CapacityVPH: 1000,
		}
	}
	built, err := graph.New(nodes, edges)
	if err != nil {
		test.Fatalf("graph.New: %v", err)
	}
	return built
}

// TestConformanceGoldenCorpus runs the full fixture through
// domain.DecodeSegmentCongestion + NewProvider against a graph mapping three of
// the four dedup winners, and asserts the §R4-deduped, §R3-annualized vph on each
// mapped edge. The fourth winner (123456789:2:F) is deliberately left unmapped
// to exercise the skipped count.
func TestConformanceGoldenCorpus(test1 *testing.T) {
	records := loadFixtureRecords(test1)
	if len(records) != 7 {
		test1.Fatalf("fixture has %d rows, want 7", len(records))
	}

	// EdgeID 0..2 map to three of the four winners; 123456789:2:F is intentionally
	// absent so it is counted as skipped.
	roadGraph := testGraph(test1,
		"27583001:0:F", // edge 0, winner count 35 → 420 vph (row 4, window_start 08:01 wins)
		"48800123:0:R", // edge 1, winner count 12 → 144 vph
		"48800123:0:F", // edge 2, winner count 18 → 216 vph
	)
	index := static.BuildSegmentEdgeIndex(roadGraph)

	provider, err := static.NewProvider(records, index, roadGraph.EdgeCount())
	if err != nil {
		test1.Fatalf("NewProvider: %v", err)
	}

	wantVPH := map[domain.EdgeID]float64{
		0: 420, // 35 × 12 — row 4's 08:01 window beats row 3's 08:00 final.
		1: 144, // 12 × 12 — reverse direction, kept independently of forward.
		2: 216, // 18 × 12 — forward direction, kept independently of reverse.
	}
	for edgeID, expected := range wantVPH {
		if got := provider.Load(edgeID); math.Abs(got-expected) > tolerance {
			test1.Errorf("Load(edge %d) = %v vph, want %v", edgeID, got, expected)
		}
	}

	// 123456789:2:F dedups to a winner but maps to no edge → skipped, not fatal.
	if got := provider.SkippedCount(); got != 1 {
		test1.Errorf("SkippedCount() = %d, want 1 (123456789:2:F is unmapped)", got)
	}
}

// TestAnnualizationFactor asserts the §R3 acceptance criterion directly: a
// 5-minute vehicle_count of N annualizes to N×12 vph on its edge.
func TestAnnualizationFactor(test1 *testing.T) {
	testCases := []struct {
		name         string
		vehicleCount int
		wantVPH      float64
	}{
		{name: "zero", vehicleCount: 0, wantVPH: 0},
		{name: "one", vehicleCount: 1, wantVPH: 12},
		{name: "thirty-five", vehicleCount: 35, wantVPH: 420},
		{name: "forty-two", vehicleCount: 42, wantVPH: 504},
	}
	for _, testCase := range testCases {
		test1.Run(testCase.name, func(test2 *testing.T) {
			records := []domain.SegmentCongestion{makeRecord(
				"27583001:0:F", "2026-06-08T08:00:00Z", "2026-06-08T08:07:00Z", testCase.vehicleCount, true,
			)}
			roadGraph := testGraph(test2, "27583001:0:F")
			index := static.BuildSegmentEdgeIndex(roadGraph)
			provider, err := static.NewProvider(records, index, roadGraph.EdgeCount())
			if err != nil {
				test2.Fatalf("NewProvider: %v", err)
			}
			if got := provider.Load(0); math.Abs(got-testCase.wantVPH) > tolerance {
				test2.Errorf("Load(0) = %v vph, want %v", got, testCase.wantVPH)
			}
		})
	}
}

// TestKeepLatestOrdering asserts the §R4 dedup ordering on hand-built records:
// a later (window_start, emit_time) overwrites an earlier one, window_start is
// the primary key (a newer window with an EARLIER emit_time still wins), and a
// provisional (is_final=false) record is still overwritable AND can still win.
func TestKeepLatestOrdering(test1 *testing.T) {
	roadGraph := testGraph(test1, "27583001:0:F")
	index := static.BuildSegmentEdgeIndex(roadGraph)

	testCases := []struct {
		name    string
		records []domain.SegmentCongestion
		wantVPH float64
	}{
		{
			name: "later emit_time within same window wins",
			records: []domain.SegmentCongestion{
				makeRecord("27583001:0:F", "2026-06-08T08:00:00Z", "2026-06-08T08:05:03Z", 31, false),
				makeRecord("27583001:0:F", "2026-06-08T08:00:00Z", "2026-06-08T08:06:04Z", 37, false),
			},
			wantVPH: 37 * 12,
		},
		{
			name: "newer window wins even with earlier emit_time",
			records: []domain.SegmentCongestion{
				// Row 3: window 08:00, final, LATER emit_time 08:07:05.
				makeRecord("27583001:0:F", "2026-06-08T08:00:00Z", "2026-06-08T08:07:05Z", 39, true),
				// Row 4: window 08:01, provisional, EARLIER emit_time 08:06:03 — still wins.
				makeRecord("27583001:0:F", "2026-06-08T08:01:00Z", "2026-06-08T08:06:03Z", 35, false),
			},
			wantVPH: 35 * 12,
		},
		{
			name: "provisional is still overwritable by a later final",
			records: []domain.SegmentCongestion{
				makeRecord("27583001:0:F", "2026-06-08T08:00:00Z", "2026-06-08T08:05:03Z", 31, false),
				makeRecord("27583001:0:F", "2026-06-08T08:00:00Z", "2026-06-08T08:07:05Z", 39, true),
			},
			wantVPH: 39 * 12,
		},
		{
			name: "records must not be summed",
			records: []domain.SegmentCongestion{
				makeRecord("27583001:0:F", "2026-06-08T08:00:00Z", "2026-06-08T08:05:03Z", 31, false),
				makeRecord("27583001:0:F", "2026-06-08T08:00:00Z", "2026-06-08T08:06:04Z", 37, false),
				makeRecord("27583001:0:F", "2026-06-08T08:00:00Z", "2026-06-08T08:07:05Z", 39, true),
			},
			wantVPH: 39 * 12, // NOT (31+37+39)×12.
		},
	}
	for _, testCase := range testCases {
		test1.Run(testCase.name, func(test2 *testing.T) {
			provider, err := static.NewProvider(testCase.records, index, roadGraph.EdgeCount())
			if err != nil {
				test2.Fatalf("NewProvider: %v", err)
			}
			if got := provider.Load(0); math.Abs(got-testCase.wantVPH) > tolerance {
				test2.Errorf("Load(0) = %v vph, want %v", got, testCase.wantVPH)
			}
		})
	}
}

// TestUnknownSegmentSkipped asserts that a record whose segment_id maps to no
// edge is skipped and counted, never fatal, and never contaminates a mapped
// edge.
func TestUnknownSegmentSkipped(test1 *testing.T) {
	roadGraph := testGraph(test1, "27583001:0:F") // only this one is mapped.
	index := static.BuildSegmentEdgeIndex(roadGraph)
	records := []domain.SegmentCongestion{
		makeRecord("27583001:0:F", "2026-06-08T08:00:00Z", "2026-06-08T08:07:00Z", 10, true),
		makeRecord("99999999:0:F", "2026-06-08T08:00:00Z", "2026-06-08T08:07:00Z", 99, true), // unknown.
	}
	provider, err := static.NewProvider(records, index, roadGraph.EdgeCount())
	if err != nil {
		test1.Fatalf("NewProvider: %v", err)
	}
	if got := provider.Load(0); math.Abs(got-120) > tolerance {
		test1.Errorf("Load(0) = %v vph, want 120 (10×12)", got)
	}
	if got := provider.SkippedCount(); got != 1 {
		test1.Errorf("SkippedCount() = %d, want 1 (99999999:0:F unmapped)", got)
	}
}

// TestNewProviderRejectsBadTimestamp asserts that NewProvider rejects a record
// whose timestamp is not in the §3 canonical wire form — both a non-canonical
// spelling (fractional seconds) and an outright unparseable value — since either
// makes the keep-latest instant comparison ill-defined (§3 "Dedup rule").
func TestNewProviderRejectsBadTimestamp(test1 *testing.T) {
	roadGraph := testGraph(test1, "27583001:0:F")
	index := static.BuildSegmentEdgeIndex(roadGraph)

	testCases := []struct {
		name        string
		windowStart string
	}{
		{name: "fractional seconds is non-canonical", windowStart: "2026-06-08T08:00:00.5Z"},
		{name: "garbage is unparseable", windowStart: "not-a-timestamp"},
	}
	for _, testCase := range testCases {
		test1.Run(testCase.name, func(test2 *testing.T) {
			records := []domain.SegmentCongestion{
				makeRecord("27583001:0:F", testCase.windowStart, "2026-06-08T08:07:00Z", 10, true),
			}
			if _, err := static.NewProvider(records, index, roadGraph.EdgeCount()); err == nil {
				test2.Fatalf("NewProvider accepted window_start %q; §3 requires canonical, parseable RFC3339", testCase.windowStart)
			}
		})
	}
}

// TestNewProviderRejectsInvalidValues asserts that NewProvider rejects a record
// violating the §3 value bounds before it can become a snapshot load — a
// negative vehicle_count (which would annualize to a negative load and break the
// router's edge-weight non-negativity assumption) and a wrong schema_version.
func TestNewProviderRejectsInvalidValues(test1 *testing.T) {
	roadGraph := testGraph(test1, "27583001:0:F")
	index := static.BuildSegmentEdgeIndex(roadGraph)

	negativeCount := makeRecord("27583001:0:F", "2026-06-08T08:00:00Z", "2026-06-08T08:07:00Z", -5, true)
	wrongVersion := makeRecord("27583001:0:F", "2026-06-08T08:00:00Z", "2026-06-08T08:07:00Z", 10, true)
	wrongVersion.SchemaVersion = 2

	testCases := []struct {
		name   string
		record domain.SegmentCongestion
	}{
		{name: "negative vehicle_count rejected", record: negativeCount},
		{name: "wrong schema_version rejected", record: wrongVersion},
	}
	for _, testCase := range testCases {
		test1.Run(testCase.name, func(test2 *testing.T) {
			if _, err := static.NewProvider([]domain.SegmentCongestion{testCase.record}, index, roadGraph.EdgeCount()); err == nil {
				test2.Fatalf("NewProvider accepted an invalid record (%s); §3 bounds must be enforced at ingest", testCase.name)
			}
		})
	}
}

// TestSnapshotIsIndependentCopy asserts the port's ownership contract for the
// static provider: the returned snapshot matches the served loads, and mutating
// it must not affect the provider (the static provider is read-only, so only the
// mutate-snapshot direction applies).
func TestSnapshotIsIndependentCopy(test1 *testing.T) {
	roadGraph := testGraph(test1, "27583001:0:F")
	index := static.BuildSegmentEdgeIndex(roadGraph)
	records := []domain.SegmentCongestion{
		makeRecord("27583001:0:F", "2026-06-08T08:00:00Z", "2026-06-08T08:07:00Z", 35, true),
	}
	provider, err := static.NewProvider(records, index, roadGraph.EdgeCount())
	if err != nil {
		test1.Fatalf("NewProvider: %v", err)
	}

	snapshot := provider.Snapshot()
	if got := snapshot.Load(0); math.Abs(got-420) > tolerance {
		test1.Fatalf("snapshot.Load(0) = %v, want 420 (35×12)", got)
	}

	// Mutating the snapshot must not affect the provider.
	snapshot[0] = 0
	if got := provider.Load(0); math.Abs(got-420) > tolerance {
		test1.Errorf("provider.Load(0) after mutating snapshot = %v, want 420 (snapshot must be a copy)", got)
	}
}

// loadFixtureRecords reads the golden corpus and strips each row's
// documentation-only `note` before strict-decoding through the production
// domain.DecodeSegmentCongestion. The fixture carries `note` as row commentary,
// not schema; a real topic carries no `note`, so the production decoder rightly
// rejects it (the domain conformance test proves that). Stripping it here lets
// the conformance run exercise the real decoder against the frozen corpus. Any
// OTHER extra field would still trip the strict decode.
func loadFixtureRecords(test *testing.T) []domain.SegmentCongestion {
	test.Helper()
	data, err := os.ReadFile(fixturePath)
	if err != nil {
		test.Fatalf("read fixture %s: %v", fixturePath, err)
	}
	var raws []map[string]json.RawMessage
	if err := json.Unmarshal(data, &raws); err != nil {
		test.Fatalf("unmarshal fixture rows: %v", err)
	}
	for _, raw := range raws {
		delete(raw, "note") // documentation, not schema — consciously stripped.
	}
	stripped, err := json.Marshal(raws)
	if err != nil {
		test.Fatalf("re-marshal stripped rows: %v", err)
	}
	records, err := domain.DecodeSegmentCongestion(bytes.NewReader(stripped))
	if err != nil {
		test.Fatalf("DecodeSegmentCongestion (note-stripped fixture): %v", err)
	}
	return records
}

// makeRecord builds a domain.SegmentCongestion with the ten §3 fields populated,
// varying only the dedup-relevant fields, for the keep-latest table tests.
func makeRecord(segmentID, windowStart, emitTime string, vehicleCount int, isFinal bool) domain.SegmentCongestion {
	return domain.SegmentCongestion{
		SchemaVersion: 1,
		SegmentID:     segmentID,
		WindowStart:   windowStart,
		WindowEnd:     "2026-06-08T08:05:00Z",
		VehicleCount:  vehicleCount,
		AvgSpeedKmh:   22.4,
		SamplePings:   200,
		IsFinal:       isFinal,
		EmitTime:      emitTime,
		Producer:      "test",
	}
}
