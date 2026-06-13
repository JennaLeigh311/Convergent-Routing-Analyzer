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
func loadGeoJSON(test *testing.T, dir, name string) geojsonFeatureCollection {
	test.Helper()
	path := filepath.Join(dir, name)
	data, err := os.ReadFile(path)
	if err != nil {
		test.Fatalf("read fixture %s: %v", path, err)
	}
	var featureCollection geojsonFeatureCollection
	if err := json.Unmarshal(data, &featureCollection); err != nil {
		test.Fatalf("unmarshal fixture %s: %v", path, err)
	}
	return featureCollection
}

// assertExactPropertyKeys re-decodes the FeatureCollection with each Feature's
// properties as a raw key map and asserts every Feature carries exactly the 11
// §2 contract columns (contractPropertyKeys) and no others. This catches the one
// thing a typed decode cannot: an unexpected EXTRA property silently ignored by
// json.Unmarshal.
func assertExactPropertyKeys(test *testing.T, dir, name string) {
	test.Helper()
	path := filepath.Join(dir, name)
	data, err := os.ReadFile(path)
	if err != nil {
		test.Fatalf("read fixture %s: %v", path, err)
	}
	var rawFC struct {
		Features []struct {
			Properties map[string]json.RawMessage `json:"properties"`
		} `json:"features"`
	}
	if err := json.Unmarshal(data, &rawFC); err != nil {
		test.Fatalf("unmarshal fixture %s: %v", path, err)
	}
	for index, feature := range rawFC.Features {
		if len(feature.Properties) != len(contractPropertyKeys) {
			test.Errorf("feature %d: properties has %d keys, want exactly %d (the §2 contract columns)",
				index, len(feature.Properties), len(contractPropertyKeys))
		}
		for key := range feature.Properties {
			if !contractPropertyKeys[key] {
				test.Errorf("feature %d: unexpected property key %q — properties must be exactly the 11 §2 contract columns", index, key)
			}
		}
	}
}

func coordsEqual(matrixA, matrix [][]float64) bool {
	if len(matrixA) != len(matrix) {
		return false
	}
	for index := range matrixA {
		if len(matrixA[index]) != len(matrix[index]) {
			return false
		}
		for innerIndex := range matrixA[index] {
			if matrixA[index][innerIndex] != matrix[index][innerIndex] {
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
func TestEdgeAttributesGeoJSONConformance(test *testing.T) {
	featureCollection := loadGeoJSON(test, edgeAttributesFixtureDir, "example_export.geojson")

	// (1) envelope type.
	if featureCollection.Type != "FeatureCollection" {
		test.Errorf("top-level type = %q, want %q", featureCollection.Type, "FeatureCollection")
	}

	// (2) schema_version is the JSON integer 1, NOT the stringified "1". Compare
	// the raw bytes: any surrounding quotes (the Parquet-footer form) fail here.
	raw := bytes.TrimSpace(featureCollection.SchemaVersion)
	if !bytes.Equal(raw, []byte("1")) {
		test.Errorf("top-level schema_version raw bytes = %q, want integer 1 (no quotes; \"1\" is the Parquet footer form)", string(raw))
	}

	// (2b) properties must be EXACTLY the 11 contract columns — no extras. The
	// typed edgeProps decode in (3) silently drops unknown keys, so re-decode each
	// Feature's properties as a raw key map and reject anything outside the §2 set.
	// Without this, a leaked internal column (e.g. a `gid`) would ship green and
	// break the frontend's "properties.segment_id is a pure §1 join, no extras"
	// guarantee. Re-reads the file once into a raw shape (the typed loader can't
	// also surface the raw key set).
	assertExactPropertyKeys(test, edgeAttributesFixtureDir, "example_export.geojson")

	// (3) row-equivalence against the logical-row fixture. Build the source map
	// keyed by segment_id, then match each GeoJSON feature against it. Equivalence
	// is asserted as a SET keyed by segment_id, deliberately NOT positionally:
	// feature order is not part of the contract, so re-ordering the GeoJSON must
	// not fail this test (the README is worded to match — "same set of rows").
	srcRows := loadFixture[sourceRow](test, edgeAttributesFixtureDir, "example_export.json")
	srcByID := make(map[string]sourceRow, len(srcRows))
	for _, row1 := range srcRows {
		srcByID[row1.SegmentID] = row1
	}

	if len(featureCollection.Features) != len(srcRows) {
		test.Errorf("geojson has %d features, source has %d rows", len(featureCollection.Features), len(srcRows))
	}

	seenGeo := make(map[string]bool, len(featureCollection.Features))
	for index, feature1 := range featureCollection.Features {
		if feature1.Type != "Feature" {
			test.Errorf("feature %d type = %q, want %q", index, feature1.Type, "Feature")
		}
		identifier1 := feature1.Properties.SegmentID
		if seenGeo[identifier1] {
			test.Errorf("duplicate segment_id %q in geojson features", identifier1)
		}
		seenGeo[identifier1] = true

		src, found1 := srcByID[identifier1]
		if !found1 {
			test.Errorf("geojson feature segment_id %q has no matching source row", identifier1)
			continue
		}

		// all 11 contract properties, including source_node/target_node (the
		// reversed F/R pairs differ only in those two).
		if feature1.Properties != src.edgeProps {
			test.Errorf("segment_id %q: geojson properties %+v != source %+v", identifier1, feature1.Properties, src.edgeProps)
		}

		// the non-contract `note` must survive verbatim (README documentation
		// parity), even though it is a foreign member, not a contract property.
		if feature1.Note != src.Note {
			test.Errorf("segment_id %q: geojson note %q != source note %q", identifier1, feature1.Note, src.Note)
		}

		// geometry coordinates must match exactly.
		if feature1.Geometry.Type != src.Geometry.Type {
			test.Errorf("segment_id %q: geometry type %q != source %q", identifier1, feature1.Geometry.Type, src.Geometry.Type)
		}
		if !coordsEqual(feature1.Geometry.Coordinates, src.Geometry.Coordinates) {
			test.Errorf("segment_id %q: geometry coordinates %v != source %v", identifier1, feature1.Geometry.Coordinates, src.Geometry.Coordinates)
		}

		// [lon, lat] axis order (§2). Cross-checking coords against
		// example_export.json cannot catch a transposition if BOTH files swapped,
		// so assert each coordinate falls in this NYC fixture's lon/lat envelope
		// (lon ≈ -73.9, lat ≈ 40.7). A [lat, lon] swap puts ~40 in the lon slot
		// and ~-73 in the lat slot, both out of these bounds — the classic
		// transposition bug fails loudly instead of shipping green.
		for _, coeffs := range feature1.Geometry.Coordinates {
			if len(coeffs) != 2 {
				test.Errorf("segment_id %q: coordinate %v is not a [lon, lat] pair", identifier1, coeffs)
				continue
			}
			if lon := coeffs[0]; lon < -75 || lon > -73 {
				test.Errorf("segment_id %q: lon %v outside NYC bounds [-75,-73] — [lat,lon] axis swap?", identifier1, lon)
			}
			if lat := coeffs[1]; lat < 40 || lat > 41 {
				test.Errorf("segment_id %q: lat %v outside NYC bounds [40,41] — [lat,lon] axis swap?", identifier1, lat)
			}
		}
	}

	// every source row must be present in the geojson too (no missing rows).
	for _, row2 := range srcRows {
		if !seenGeo[row2.SegmentID] {
			test.Errorf("source segment_id %q missing from geojson", row2.SegmentID)
		}
	}

	// (4) named must-survive rows.
	mustSurvive := []string{
		"33112200:0:F",                 // 3-vertex LineString
		"48800123:0:F", "48800123:0:R", // F/R pair + congestion overlap
		"8123456:0:F", "8123456:0:R", // F/R pair
		"27583001:0:F", // congestion overlap
	}
	for _, identifier2 := range mustSurvive {
		feature2, found2 := findFeature(featureCollection, identifier2)
		if !found2 {
			test.Errorf("must-survive segment_id %q missing from geojson", identifier2)
			continue
		}
		if identifier2 == "33112200:0:F" && len(feature2.Geometry.Coordinates) != 3 {
			test.Errorf("segment_id %q must be a 3-coordinate LineString, got %d coords", identifier2, len(feature2.Geometry.Coordinates))
		}
	}
}

func findFeature(featureCollection geojsonFeatureCollection, identifier string) (geojsonFeature, bool) {
	for _, feature := range featureCollection.Features {
		if feature.Properties.SegmentID == identifier {
			return feature, true
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
func TestMalformedExportsCorpus(test *testing.T) {
	corpus := loadFixture[malformedExport](test, edgeAttributesFixtureDir, "malformed_exports.json")

	for index, export1 := range corpus {
		if strings.TrimSpace(export1.Violates) == "" {
			test.Errorf("corpus entry %d has an empty `violates` string", index)
		}
		if export1.FeatureCollection.Type != "FeatureCollection" {
			test.Errorf("corpus entry %d (%q): feature_collection type = %q, want %q",
				index, export1.Violates, export1.FeatureCollection.Type, "FeatureCollection")
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
		for _, export2 := range corpus {
			if strings.Contains(strings.ToLower(export2.Violates), needle) {
				found = true
				break
			}
		}
		if !found {
			test.Errorf("mandated malformed case %q not represented in corpus (no `violates` contains %q)", name, needle)
		}
	}
}
