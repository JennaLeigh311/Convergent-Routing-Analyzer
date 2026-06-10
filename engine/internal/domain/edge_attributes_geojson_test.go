package domain

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// edge_attributes_geojson_test.go executes the GeoJSON serialization of the
// frozen edge_attributes export (docs/contracts.md §2, "Serializations" +
// "Envelope schema_version"). It proves example_export.geojson is row-equivalent
// to the logical-row example_export.json (the two must carry identical values
// per segment_id) and that the GeoJSON envelope carries the integer
// schema_version 1 — NOT the stringified "1" that is the Parquet footer's form.
// It also guards the malformed_exports.json reject corpus that feeds the #25
// loader's validate-and-reject path: here we only assert the corpus is
// well-formed and labels each mandated invariant; the rejection logic is #25's.

// geojsonGeometry mirrors a GeoJSON LineString geometry object.
type geojsonGeometry struct {
	Type        string      `json:"type"`
	Coordinates [][]float64 `json:"coordinates"`
}

// edgeProps is the full set of 11 non-geometry contract columns: edgeAttrRow
// (edge_attributes_test.go) carries the 9 derivation-relevant columns, and this
// adds source_node/target_node, which it omits. The row-equivalence check below
// compares this whole struct, so all 11 columns are diffed — the reversed F/R
// pairs (48800123, 8123456) differ ONLY in source_node/target_node, so omitting
// the node columns would blind the check to exactly the swap those rows exist to
// verify. It stays comparable (all scalar fields) so `==` works.
type edgeProps struct {
	edgeAttrRow
	SourceNode int `json:"source_node"`
	TargetNode int `json:"target_node"`
}

// geojsonFeature mirrors one Feature in the FeatureCollection. The 11 non-geometry
// contract columns live under properties (edgeProps); geometry is a sibling, and
// `note` is a documentation foreign member.
type geojsonFeature struct {
	Type       string          `json:"type"`
	Geometry   geojsonGeometry `json:"geometry"`
	Properties edgeProps       `json:"properties"`
	Note       string          `json:"note"`
}

// geojsonFeatureCollection mirrors the top-level FeatureCollection envelope.
// SchemaVersion is captured as json.RawMessage so the test can assert the raw
// bytes are the integer `1` and not a quoted string.
type geojsonFeatureCollection struct {
	Type          string           `json:"type"`
	SchemaVersion json.RawMessage  `json:"schema_version"`
	Features      []geojsonFeature `json:"features"`
}

// sourceRow mirrors a full row of example_export.json for the row-equivalence
// check: the 11 contract columns (edgeProps) plus the geometry edgeProps omits
// and the non-contract `note` (compared so the README's documentation-parity
// claim — the per-row note survives into the GeoJSON Feature — is actually
// backed by a test rather than left to rot).
type sourceRow struct {
	edgeProps
	Geometry geojsonGeometry `json:"geometry"`
	Note     string          `json:"note"`
}

// contractPropertyKeys is the exact set of 11 non-geometry columns §2 permits in
// a Feature's `properties` — and nothing else. The typed edgeProps decode above
// silently ignores unknown keys, so a leaked or renamed extra property would pass
// the row-equivalence check; this set lets the test re-decode properties raw and
// reject any key outside it, enforcing the README's "and nothing else" guarantee.
var contractPropertyKeys = map[string]bool{
	"segment_id": true, "edge_id": true, "source_node": true, "target_node": true,
	"osm_way_id": true, "highway_class": true, "lanes_effective": true,
	"length_m": true, "maxspeed_kmh": true, "freeflow_time_s": true, "capacity_vph": true,
}

// loadGeoJSON reads and unmarshals the FeatureCollection. Unlike loadFixture
// (segmentid_test.go) the GeoJSON top level is an object, not an array, so this
// is a focused bespoke loader in the same spirit.
func loadGeoJSON(t *testing.T, dir, name string) geojsonFeatureCollection {
	t.Helper()
	path := filepath.Join(dir, name)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture %s: %v", path, err)
	}
	var fc geojsonFeatureCollection
	if err := json.Unmarshal(data, &fc); err != nil {
		t.Fatalf("unmarshal fixture %s: %v", path, err)
	}
	return fc
}

// assertExactPropertyKeys re-decodes the FeatureCollection with each Feature's
// properties as a raw key map and asserts every Feature carries exactly the 11
// §2 contract columns (contractPropertyKeys) and no others. This catches the one
// thing a typed decode cannot: an unexpected EXTRA property silently ignored by
// json.Unmarshal.
func assertExactPropertyKeys(t *testing.T, dir, name string) {
	t.Helper()
	path := filepath.Join(dir, name)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture %s: %v", path, err)
	}
	var rawFC struct {
		Features []struct {
			Properties map[string]json.RawMessage `json:"properties"`
		} `json:"features"`
	}
	if err := json.Unmarshal(data, &rawFC); err != nil {
		t.Fatalf("unmarshal fixture %s: %v", path, err)
	}
	for i, f := range rawFC.Features {
		if len(f.Properties) != len(contractPropertyKeys) {
			t.Errorf("feature %d: properties has %d keys, want exactly %d (the §2 contract columns)",
				i, len(f.Properties), len(contractPropertyKeys))
		}
		for k := range f.Properties {
			if !contractPropertyKeys[k] {
				t.Errorf("feature %d: unexpected property key %q — properties must be exactly the 11 §2 contract columns", i, k)
			}
		}
	}
}

func coordsEqual(a, b [][]float64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if len(a[i]) != len(b[i]) {
			return false
		}
		for j := range a[i] {
			if a[i][j] != b[i][j] {
				return false
			}
		}
	}
	return true
}

// TestEdgeAttributesGeoJSONConformance parses example_export.geojson, asserts the
// envelope (type + integer schema_version 1), and asserts row-equivalence to the
// logical-row example_export.json: every segment_id's 11 contract properties and
// geometry coordinates must match the source row exactly, with no missing/extra
// rows. It also checks the named must-survive rows are present.
func TestEdgeAttributesGeoJSONConformance(t *testing.T) {
	fc := loadGeoJSON(t, edgeAttributesFixtureDir, "example_export.geojson")

	// (1) envelope type.
	if fc.Type != "FeatureCollection" {
		t.Errorf("top-level type = %q, want %q", fc.Type, "FeatureCollection")
	}

	// (2) schema_version is the JSON integer 1, NOT the stringified "1". Compare
	// the raw bytes: any surrounding quotes (the Parquet-footer form) fail here.
	raw := bytes.TrimSpace(fc.SchemaVersion)
	if !bytes.Equal(raw, []byte("1")) {
		t.Errorf("top-level schema_version raw bytes = %q, want integer 1 (no quotes; \"1\" is the Parquet footer form)", string(raw))
	}

	// (2b) properties must be EXACTLY the 11 contract columns — no extras. The
	// typed edgeProps decode in (3) silently drops unknown keys, so re-decode each
	// Feature's properties as a raw key map and reject anything outside the §2 set.
	// Without this, a leaked internal column (e.g. a `gid`) would ship green and
	// break the frontend's "properties.segment_id is a pure §1 join, no extras"
	// guarantee. Re-reads the file once into a raw shape (the typed loader can't
	// also surface the raw key set).
	assertExactPropertyKeys(t, edgeAttributesFixtureDir, "example_export.geojson")

	// (3) row-equivalence against the logical-row fixture. Build the source map
	// keyed by segment_id, then match each GeoJSON feature against it. Equivalence
	// is asserted as a SET keyed by segment_id, deliberately NOT positionally:
	// feature order is not part of the contract, so re-ordering the GeoJSON must
	// not fail this test (the README is worded to match — "same set of rows").
	srcRows := loadFixture[sourceRow](t, edgeAttributesFixtureDir, "example_export.json")
	srcByID := make(map[string]sourceRow, len(srcRows))
	for _, r := range srcRows {
		srcByID[r.SegmentID] = r
	}

	if len(fc.Features) != len(srcRows) {
		t.Errorf("geojson has %d features, source has %d rows", len(fc.Features), len(srcRows))
	}

	seenGeo := make(map[string]bool, len(fc.Features))
	for i, f := range fc.Features {
		if f.Type != "Feature" {
			t.Errorf("feature %d type = %q, want %q", i, f.Type, "Feature")
		}
		id := f.Properties.SegmentID
		if seenGeo[id] {
			t.Errorf("duplicate segment_id %q in geojson features", id)
		}
		seenGeo[id] = true

		src, ok := srcByID[id]
		if !ok {
			t.Errorf("geojson feature segment_id %q has no matching source row", id)
			continue
		}

		// all 11 contract properties, including source_node/target_node (the
		// reversed F/R pairs differ only in those two).
		if f.Properties != src.edgeProps {
			t.Errorf("segment_id %q: geojson properties %+v != source %+v", id, f.Properties, src.edgeProps)
		}

		// the non-contract `note` must survive verbatim (README documentation
		// parity), even though it is a foreign member, not a contract property.
		if f.Note != src.Note {
			t.Errorf("segment_id %q: geojson note %q != source note %q", id, f.Note, src.Note)
		}

		// geometry coordinates must match exactly.
		if f.Geometry.Type != src.Geometry.Type {
			t.Errorf("segment_id %q: geometry type %q != source %q", id, f.Geometry.Type, src.Geometry.Type)
		}
		if !coordsEqual(f.Geometry.Coordinates, src.Geometry.Coordinates) {
			t.Errorf("segment_id %q: geometry coordinates %v != source %v", id, f.Geometry.Coordinates, src.Geometry.Coordinates)
		}

		// [lon, lat] axis order (§2). Cross-checking coords against
		// example_export.json cannot catch a transposition if BOTH files swapped,
		// so assert each coordinate falls in this NYC fixture's lon/lat envelope
		// (lon ≈ -73.9, lat ≈ 40.7). A [lat, lon] swap puts ~40 in the lon slot
		// and ~-73 in the lat slot, both out of these bounds — the classic
		// transposition bug fails loudly instead of shipping green.
		for _, c := range f.Geometry.Coordinates {
			if len(c) != 2 {
				t.Errorf("segment_id %q: coordinate %v is not a [lon, lat] pair", id, c)
				continue
			}
			if lon := c[0]; lon < -75 || lon > -73 {
				t.Errorf("segment_id %q: lon %v outside NYC bounds [-75,-73] — [lat,lon] axis swap?", id, lon)
			}
			if lat := c[1]; lat < 40 || lat > 41 {
				t.Errorf("segment_id %q: lat %v outside NYC bounds [40,41] — [lat,lon] axis swap?", id, lat)
			}
		}
	}

	// every source row must be present in the geojson too (no missing rows).
	for _, r := range srcRows {
		if !seenGeo[r.SegmentID] {
			t.Errorf("source segment_id %q missing from geojson", r.SegmentID)
		}
	}

	// (4) named must-survive rows.
	mustSurvive := []string{
		"33112200:0:F",                 // 3-vertex LineString
		"48800123:0:F", "48800123:0:R", // F/R pair + congestion overlap
		"8123456:0:F", "8123456:0:R", // F/R pair
		"27583001:0:F", // congestion overlap
	}
	for _, id := range mustSurvive {
		f, ok := findFeature(fc, id)
		if !ok {
			t.Errorf("must-survive segment_id %q missing from geojson", id)
			continue
		}
		if id == "33112200:0:F" && len(f.Geometry.Coordinates) != 3 {
			t.Errorf("segment_id %q must be a 3-coordinate LineString, got %d coords", id, len(f.Geometry.Coordinates))
		}
	}
}

func findFeature(fc geojsonFeatureCollection, id string) (geojsonFeature, bool) {
	for _, f := range fc.Features {
		if f.Properties.SegmentID == id {
			return f, true
		}
	}
	return geojsonFeature{}, false
}

// malformedExport mirrors one entry of malformed_exports.json: a one-line
// `violates` label plus a complete-but-invalid FeatureCollection.
type malformedExport struct {
	Violates          string                   `json:"violates"`
	FeatureCollection geojsonFeatureCollection `json:"feature_collection"`
}

// TestMalformedExportsCorpus is a light guard over the reject corpus that feeds
// the #25 loader's validate-and-reject path. It asserts the corpus loads, is
// non-empty, every entry is annotated with a non-empty `violates` string and a
// feature_collection whose type is "FeatureCollection", and that the mandated
// invariant categories are each represented (matched by substring on the
// normalized `violates` labels). The actual rejection logic is #25's; here we
// only assert the corpus exists, is annotated, and covers the invariants.
func TestMalformedExportsCorpus(t *testing.T) {
	corpus := loadFixture[malformedExport](t, edgeAttributesFixtureDir, "malformed_exports.json")

	for i, m := range corpus {
		if strings.TrimSpace(m.Violates) == "" {
			t.Errorf("corpus entry %d has an empty `violates` string", i)
		}
		if m.FeatureCollection.Type != "FeatureCollection" {
			t.Errorf("corpus entry %d (%q): feature_collection type = %q, want %q",
				i, m.Violates, m.FeatureCollection.Type, "FeatureCollection")
		}
	}

	// Each mandated category, matched by a lowercase substring of `violates`.
	// NOTE: each needle is a verbatim fragment of a `violates` label in
	// malformed_exports.json. The coupling is intentional (it asserts every
	// invariant category stays represented), and it fails LOUD — a reworded label
	// makes this test error here, it does not silently drop coverage. If you edit
	// a `violates` string in the JSON, update its needle below in the same change.
	mandated := map[string]string{
		"schema_version 2":           "schema_version is 2",
		"absent schema_version":      "schema_version member is absent",
		"stringified schema_version": "string \"1\"",
		"segment/way mismatch":       "but the osm_way_id property",
		"non-dense edge_id (gap)":    "not dense 0..n-1: a gap",
		"non-dense edge_id (dup)":    "not dense 0..n-1: a duplicate",
		"out-of-enum highway_class":  "outside the seven legal",
		"interior node":              "interior linestring shape point promoted to a graph node",
		"swapped axis":               "[lat, lon] instead of",
		"non-positive length_m":      "length_m is non-positive",
		"non-positive maxspeed_kmh":  "maxspeed_kmh is non-positive",
		"non-positive osm_way_id":    "osm_way_id is non-positive",
		"segment_id invalid (§1)":    "invalid under §1",
	}
	for name, needle := range mandated {
		found := false
		for _, m := range corpus {
			if strings.Contains(strings.ToLower(m.Violates), needle) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("mandated malformed case %q not represented in corpus (no `violates` contains %q)", name, needle)
		}
	}
}
