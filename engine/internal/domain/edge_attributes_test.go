package domain

import (
	"math"
	"testing"
)

// edgeAttributesFixtureDir is the path (relative to this package directory) of
// the frozen edge_attributes golden fixture. The engine and the frontend both
// read the exported edge_attributes artifact, while the database engineer owns
// its derivation rules. Re-deriving the contract's formulas here and asserting
// them against the SAME golden file the human spec describes is what keeps the
// data side (the exporter) and the service side (this engine) from silently
// drifting apart: a miscomputed capacity or free-flow value doesn't crash
// anything, it just quietly corrupts the BPR cost and the headline numbers.
// See docs/contracts.md §2.
const edgeAttributesFixtureDir = "../../../docs/fixtures/edge_attributes"

// classFactor is the §2 class_factor table, hardcoded here as the executable
// counterpart to docs/contracts.md §2: a monotonic ramp from motorway 1.0 down
// to service 0.4, applied in the capacity formula. It doubles as the canonical
// set of the exactly-seven legal highway_class enum values (its keys), so the
// enum and coverage checks below derive from this one source of truth rather
// than a second hand-maintained list that could drift from it.
var classFactor = map[string]float64{
	"motorway":    1.0,
	"trunk":       0.9,
	"primary":     0.8,
	"secondary":   0.7,
	"tertiary":    0.6,
	"residential": 0.5,
	"service":     0.4,
}

// saturationFlowVPHPerLane is the §2 saturation flow (≈ 1800 veh/h/lane) that
// a single lane discharges at; capacity_vph scales it by lanes and class_factor.
const saturationFlowVPHPerLane = 1800.0

// floatTol is the absolute tolerance for the derived-float comparisons. The
// class_factor values (0.8, 0.7, ...) and the km/h→m/s conversion are not exact
// in binary, so exact equality would produce spurious failures.
const floatTol = 1e-6

// edgeAttrRow mirrors the contract columns of one directed-edge row in
// example_export.json that this conformance test exercises. The fixture also
// carries geometry/source_node/target_node and a non-contract `note` string;
// those are intentionally omitted here as they are not part of the derivations
// under test. Field→column mapping follows docs/contracts.md §2.
type edgeAttrRow struct {
	SegmentID      string  `json:"segment_id"`
	EdgeID         int     `json:"edge_id"`
	OSMWayID       int64   `json:"osm_way_id"`
	HighwayClass   string  `json:"highway_class"`
	LanesEffective int     `json:"lanes_effective"`
	LengthM        float64 `json:"length_m"`
	MaxspeedKmh    float64 `json:"maxspeed_kmh"`
	FreeflowTimeS  float64 `json:"freeflow_time_s"`
	CapacityVPH    float64 `json:"capacity_vph"`
}

// TestEdgeAttributesConformance executes the frozen edge_attributes golden
// fixture instead of leaving it to be eyeballed via the `note` strings. Per row
// (keyed by segment_id) it asserts the §2 column invariants, re-derives capacity
// and free-flow from the §2 rules, confirms the segment_id parses and agrees
// with the osm_way_id column, and checks the highway_class enum; across all rows
// it confirms edge_id is a dense 0..N-1 set and that every one of the seven
// highway classes is exercised. The fixture loads via the shared loadFixture
// helper (segmentid_test.go), pointed at the edge_attributes directory.
func TestEdgeAttributesConformance(t *testing.T) {
	rows := loadFixture[edgeAttrRow](t, edgeAttributesFixtureDir, "example_export.json")

	// Accumulators for the two whole-file invariants asserted after the loop.
	// Both are cross-row bookkeeping, so both are written here in the loop body
	// (outside the per-row subtest) under one mental model: the subtest closure
	// validates a single row; the loop body records what the whole-file checks
	// need, and runs even when a subtest fails.
	seenEdgeIDs := make(map[int]bool, len(rows))
	seenClasses := make(map[string]bool, len(classFactor))

	for _, row := range rows {
		t.Run(row.SegmentID, func(t *testing.T) {
			// (4) enum — highway_class must be one of the exactly-seven §2
			// values. We look it up in classFactor (whose keys ARE that enum)
			// and reuse the factor for the capacity check below. A value
			// outside the enum has no derivation rule, so we stop here.
			cf, ok := classFactor[row.HighwayClass]
			if !ok {
				t.Fatalf("highway_class %q is not one of the 7 contract classes", row.HighwayClass)
			}

			// (0) column invariants — §2 freezes hard per-row lower bounds that
			// the derived-value checks below cannot catch on their own: a
			// lanes_effective of 0, for instance, makes capacity_vph = 0 satisfy
			// the capacity formula (0 == 0×1800×cf), so a zero-capacity edge that
			// blows up the BPR (v/c) term would slip through. Assert them
			// explicitly. (Asserting maxspeed_kmh > 0 here also keeps the
			// free-flow division below from producing a misleading Inf/NaN.)
			if row.LanesEffective < 1 {
				t.Errorf("lanes_effective = %d, want >= 1 (§2)", row.LanesEffective)
			}
			if row.LengthM <= 0 {
				t.Errorf("length_m = %g, want > 0 (§2)", row.LengthM)
			}
			if row.MaxspeedKmh <= 0 {
				t.Errorf("maxspeed_kmh = %g, want > 0 (§2)", row.MaxspeedKmh)
			}
			if row.CapacityVPH <= 0 {
				t.Errorf("capacity_vph = %g, want > 0 (§2)", row.CapacityVPH)
			}
			if row.FreeflowTimeS <= 0 {
				t.Errorf("freeflow_time_s = %g, want > 0 (§2)", row.FreeflowTimeS)
			}

			// (1) capacity — §2: capacity_vph = lanes_effective × 1800 ×
			// class_factor (at the export's capacity_scale = 1.0). e.g. row 0
			// primary lanes=2 → 2×1800×0.8 = 2880.
			wantCapacity := float64(row.LanesEffective) * saturationFlowVPHPerLane * cf
			if math.Abs(row.CapacityVPH-wantCapacity) > floatTol {
				t.Errorf("capacity_vph = %g, want %g (= %d lanes × %g × %g class_factor)",
					row.CapacityVPH, wantCapacity, row.LanesEffective, saturationFlowVPHPerLane, cf)
			}

			// (2) freeflow — §2: freeflow_time_s = length_m / (maxspeed_kmh ×
			// 1000/3600). e.g. row 0 length=240, maxspeed=60 → 240/(60×1000/3600)
			// = 14.4. Guarded on maxspeed_kmh > 0 (the invariant above already
			// reports a violation) so the division can't yield a misleading
			// Inf/NaN that the tolerance comparison would silently accept.
			if row.MaxspeedKmh > 0 {
				wantFreeflow := row.LengthM / (row.MaxspeedKmh * 1000.0 / 3600.0)
				if math.Abs(row.FreeflowTimeS-wantFreeflow) > floatTol {
					t.Errorf("freeflow_time_s = %g, want %g (= %g m / (%g km/h → m/s))",
						row.FreeflowTimeS, wantFreeflow, row.LengthM, row.MaxspeedKmh)
				}
			}

			// (3) segment_id consistency — the segment_id must parse under the
			// §1 contract AND the osm_way_id it decodes must equal this row's
			// osm_way_id column. If they ever disagree, congestion keyed by
			// segment_id would land on a different way than the engine's edge.
			gotWay, _, _, err := ParseSegmentID(SegmentID(row.SegmentID))
			if err != nil {
				t.Fatalf("ParseSegmentID(%q) unexpected error: %v", row.SegmentID, err)
			}
			if gotWay != row.OSMWayID {
				t.Errorf("segment_id %q decodes osm_way_id %d, but osm_way_id column is %d",
					row.SegmentID, gotWay, row.OSMWayID)
			}
		})

		// edge_id density and class coverage are whole-file properties, so record
		// both here (rejecting duplicate edge_ids as we go). Done outside the
		// subtest so a failing subtest above doesn't skip the bookkeeping.
		if seenEdgeIDs[row.EdgeID] {
			t.Errorf("duplicate edge_id %d (segment_id %q)", row.EdgeID, row.SegmentID)
		}
		seenEdgeIDs[row.EdgeID] = true
		seenClasses[row.HighwayClass] = true
	}

	// (5) dense edge_id — across all rows the edge_id set must be exactly
	// 0..N-1: contiguous, no gaps, no duplicates. edge_id is the engine's dense
	// array index into the graph, so a gap or dupe would mis-index every edge
	// after it. Duplicates are caught above; here we confirm every index in the
	// 0..N-1 range is present (which, combined with the no-dupe check and the
	// count, forces exact contiguity).
	for i := 0; i < len(rows); i++ {
		if !seenEdgeIDs[i] {
			t.Errorf("edge_id %d missing: edge_id set is not dense 0..%d", i, len(rows)-1)
		}
	}

	// (6) coverage — every one of the seven highway classes must appear at
	// least once. This is the check that motivated the issue: dropping a class
	// (e.g. the tertiary gap) silently leaves its class_factor branch untested,
	// so we fail CI if any class is missing from the fixture.
	for class := range classFactor {
		if !seenClasses[class] {
			t.Errorf("highway_class %q is not exercised by the fixture (coverage gap)", class)
		}
	}
}
