package graph_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/domain"
	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/graph"
)

// fixtureDir is the shared, language-neutral edge_attributes fixture directory,
// the same path the package-domain conformance tests (#15/#22) use.
const fixtureDir = "../../../docs/fixtures/edge_attributes"

func goldenBytes(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(fixtureDir, "example_export.geojson"))
	if err != nil {
		t.Fatalf("read golden fixture: %v", err)
	}
	return data
}

// TestGoldenLoads asserts the golden fixture loads cleanly with the expected
// edge/node counts, the adopted in-memory EdgeID == the export edge_id, a
// segment_id↔EdgeID round-trip for every edge, and dense nodes.
func TestGoldenLoads(t *testing.T) {
	g, geom, err := graph.LoadEdgeAttributesGeoJSON(bytes.NewReader(goldenBytes(t)))
	if err != nil {
		t.Fatalf("golden fixture must load cleanly, got: %v", err)
	}

	const wantEdges = 12
	if g.EdgeCount() != wantEdges {
		t.Errorf("EdgeCount = %d, want %d", g.EdgeCount(), wantEdges)
	}
	if len(geom) != wantEdges {
		t.Errorf("geometry map has %d entries, want %d", len(geom), wantEdges)
	}

	// edge_id from the export, in fixture order, with its segment_id.
	wantSeg := map[domain.EdgeID]domain.SegmentID{
		0: "27583001:0:F", 1: "48800123:0:F", 2: "48800123:0:R",
		3: "905512:0:F", 4: "905512:1:F", 5: "9876543:0:F",
		6: "33112200:0:F", 7: "71200:0:F", 8: "60500777:0:F",
		9: "5051000:0:F", 10: "8123456:0:F", 11: "8123456:0:R",
	}
	for id, seg := range wantSeg {
		e, ok := g.Edge(id)
		if !ok {
			t.Errorf("edge id %d missing", id)
			continue
		}
		if e.ID != id {
			t.Errorf("edge at id %d has ID %d (in-memory EdgeID must equal export edge_id)", id, e.ID)
		}
		if e.Segment != seg {
			t.Errorf("edge id %d segment = %q, want %q", id, e.Segment, seg)
		}
	}

	// segment_id ↔ EdgeID round-trips for every edge.
	for id := domain.EdgeID(0); int(id) < g.EdgeCount(); id++ {
		e, _ := g.Edge(id)
		// reverse lookup via edges[id].Segment should round-trip
		if _, ok := geom[e.Segment]; !ok {
			t.Errorf("edge id %d segment %q missing from geometry map", id, e.Segment)
		}
	}

	// Nodes dense & compact: the fixture references 18 distinct export node ids
	// (10..12,20..22,30,31,40,41,50,51,60,61,70,71,80,81); the loader compacts
	// them to a dense 0..17 in-memory NodeID space.
	const wantNodes = 18
	if g.NodeCount() != wantNodes {
		t.Errorf("NodeCount = %d, want %d", g.NodeCount(), wantNodes)
	}
	for id := domain.NodeID(0); int(id) < g.NodeCount(); id++ {
		n, ok := g.Node(id)
		if !ok {
			t.Errorf("node id %d missing — nodes must be dense", id)
		}
		if n.ID != id {
			t.Errorf("node at id %d has ID %d", id, n.ID)
		}
	}
}

// TestGeometryFidelity round-trips a known coordinate (no axis swap) and proves
// the 3-vertex LineString retains its interior shape point WITHOUT promoting it
// to a node.
func TestGeometryFidelity(t *testing.T) {
	g, geom, err := graph.LoadEdgeAttributesGeoJSON(bytes.NewReader(goldenBytes(t)))
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	// edge_id 0, segment 27583001:0:F: [[-73.99012,40.73456],[-73.98801,40.73502]]
	want := graph.LineString{{-73.99012, 40.73456}, {-73.98801, 40.73502}}
	got := geom["27583001:0:F"]
	if !reflect.DeepEqual(got, want) {
		t.Errorf("geometry for 27583001:0:F = %v, want %v ([lon,lat], no swap)", got, want)
	}

	// 3-vertex LineString, segment 33112200:0:F. Interior coord must be retained.
	threeVtx := geom["33112200:0:F"]
	if len(threeVtx) != 3 {
		t.Fatalf("33112200:0:F geometry has %d coords, want 3 (interior shape point retained)", len(threeVtx))
	}
	wantInterior := [2]float64{-73.93720, 40.75640}
	if threeVtx[1] != wantInterior {
		t.Errorf("33112200:0:F interior coord = %v, want %v", threeVtx[1], wantInterior)
	}

	// The interior shape point must NOT have become a node. Its endpoints are
	// node ids 40 (source) and 41 (target); the interior coord is neither. The
	// total NodeCount (82) already excludes it; assert no node sits at the
	// interior position.
	for id := domain.NodeID(0); int(id) < g.NodeCount(); id++ {
		n, _ := g.Node(id)
		if n.Pos.Lon == wantInterior[0] && n.Pos.Lat == wantInterior[1] {
			t.Errorf("interior shape point %v was promoted to node %d", wantInterior, id)
		}
	}
}

// TestDeterminism loads the same bytes twice and asserts identical edge/node id
// assignment and identical sorted neighbor order.
func TestDeterminism(t *testing.T) {
	data := goldenBytes(t)
	g1, geom1, err := graph.LoadEdgeAttributesGeoJSON(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("load 1: %v", err)
	}
	g2, geom2, err := graph.LoadEdgeAttributesGeoJSON(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("load 2: %v", err)
	}

	if g1.EdgeCount() != g2.EdgeCount() || g1.NodeCount() != g2.NodeCount() {
		t.Fatalf("counts differ across loads")
	}
	for id := domain.EdgeID(0); int(id) < g1.EdgeCount(); id++ {
		e1, _ := g1.Edge(id)
		e2, _ := g2.Edge(id)
		if e1 != e2 {
			t.Errorf("edge id %d differs across loads: %+v vs %+v", id, e1, e2)
		}
	}
	for id := domain.NodeID(0); int(id) < g1.NodeCount(); id++ {
		n1, _ := g1.Node(id)
		n2, _ := g2.Node(id)
		if n1 != n2 {
			t.Errorf("node id %d differs across loads: %+v vs %+v", id, n1, n2)
		}
		nb1 := g1.Neighbors(id)
		nb2 := g2.Neighbors(id)
		if !reflect.DeepEqual(nb1, nb2) {
			t.Errorf("node id %d neighbors differ across loads: %v vs %v", id, nb1, nb2)
		}
	}
	if !reflect.DeepEqual(geom1, geom2) {
		t.Errorf("geometry maps differ across loads")
	}
}

// malformedEntry mirrors one entry of malformed_exports.json.
type malformedEntry struct {
	Violates          string          `json:"violates"`
	FeatureCollection json.RawMessage `json:"feature_collection"`
}

// TestRejectCorpus iterates ALL entries in malformed_exports.json and asserts
// each is rejected with a non-nil error and no partial graph (g == nil).
func TestRejectCorpus(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(fixtureDir, "malformed_exports.json"))
	if err != nil {
		t.Fatalf("read corpus: %v", err)
	}
	var corpus []malformedEntry
	if err := json.Unmarshal(data, &corpus); err != nil {
		t.Fatalf("unmarshal corpus: %v", err)
	}
	if len(corpus) != 13 {
		t.Fatalf("corpus has %d entries, want 13", len(corpus))
	}

	// The fixture corpus is an NYC extract. Case 8 (a uniform [lat,lon] axis
	// swap whose swapped values BOTH stay inside WGS84) is only detectable
	// against the dataset's expected region, so we supply the NYC box: lon ∈
	// [-75,-73], lat ∈ [40,41] contains every golden coordinate and excludes the
	// swap (lon 40.73 > -73; lat -73.99 < 40). The other 12 entries reject
	// region-agnostically regardless of whether bounds are supplied.
	nycBounds := graph.WithExpectedBounds(-75, -73, 40, 41)
	for i, m := range corpus {
		g, geom, err := graph.LoadEdgeAttributesGeoJSON(bytes.NewReader(m.FeatureCollection), nycBounds)
		if err == nil {
			t.Errorf("corpus[%d] (%q): expected rejection, got nil error", i, m.Violates)
			continue
		}
		if g != nil {
			t.Errorf("corpus[%d] (%q): rejected but returned non-nil graph (must be fail-closed, no partial)", i, m.Violates)
		}
		if geom != nil {
			t.Errorf("corpus[%d] (%q): rejected but returned non-nil geometry map", i, m.Violates)
		}
		t.Logf("corpus[%d] rejected: %v", i, err)
	}
}

// TestRejectErrorMessages spot-checks that a few rejections mention the specific
// invariant they violate, so the errors are descriptive and actionable.
func TestRejectErrorMessages(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(fixtureDir, "malformed_exports.json"))
	if err != nil {
		t.Fatalf("read corpus: %v", err)
	}
	var corpus []malformedEntry
	if err := json.Unmarshal(data, &corpus); err != nil {
		t.Fatalf("unmarshal corpus: %v", err)
	}

	wantSubstr := map[int]string{
		0:  "schema_version",
		1:  "schema_version",
		2:  "schema_version",
		3:  "must match",
		6:  "highway_class",
		8:  "outside",
		9:  "length_m",
		10: "maxspeed_kmh",
	}
	// Case 8 (in-range axis swap) needs the dataset's region to be caught; supply
	// the NYC box for it. The others reject region-agnostically, so a uniform
	// bounds arg here is harmless and keeps every golden coord inside the box.
	nycBounds := graph.WithExpectedBounds(-75, -73, 40, 41)
	for idx, sub := range wantSubstr {
		_, _, err := graph.LoadEdgeAttributesGeoJSON(bytes.NewReader(corpus[idx].FeatureCollection), nycBounds)
		if err == nil {
			t.Errorf("corpus[%d]: expected error", idx)
			continue
		}
		if !bytes.Contains([]byte(err.Error()), []byte(sub)) {
			t.Errorf("corpus[%d]: error %q does not mention %q", idx, err.Error(), sub)
		}
	}
}

// tokyoEdge is a minimal, §2-valid single-edge FeatureCollection in the Eastern
// hemisphere (near Tokyo: lon ~139.7, lat ~35.7). It is the regression guard
// against re-hardcoding a hemisphere: dense edge_id 0, a §1-valid segment_id
// whose embedded way matches osm_way_id, a highway_class in the enum, positive
// metrics, and a 2-coordinate [lon,lat] LineString. With the default (no-bounds)
// call it must LOAD, proving the loader is region-agnostic.
const tokyoEdge = `{
  "type": "FeatureCollection",
  "schema_version": 1,
  "features": [
    {
      "type": "Feature",
      "geometry": {
        "type": "LineString",
        "coordinates": [[139.70010, 35.68950], [139.70220, 35.69010]]
      },
      "properties": {
        "segment_id": "4001234:0:F",
        "edge_id": 0,
        "source_node": 10,
        "target_node": 11,
        "osm_way_id": 4001234,
        "highway_class": "primary",
        "lanes_effective": 2,
        "length_m": 210.0,
        "maxspeed_kmh": 50.0,
        "freeflow_time_s": 15.1,
        "capacity_vph": 1800.0
      }
    }
  ]
}`

// TestEasternHemisphereLoadsDefault proves the decoupling goal: an
// Eastern-hemisphere (Tokyo) export that is otherwise §2-valid LOADS with the
// default no-bounds call. The longitude +139.7 would have been silently rejected
// by the old hardcoded Western-hemisphere window; region-agnostic by default, it
// is accepted. This is the guard against ever re-baking a hemisphere into the
// engine.
func TestEasternHemisphereLoadsDefault(t *testing.T) {
	g, geom, err := graph.LoadEdgeAttributesGeoJSON(bytes.NewReader([]byte(tokyoEdge)))
	if err != nil {
		t.Fatalf("eastern-hemisphere export must load with the default (no-bounds) call, got: %v", err)
	}
	if g.EdgeCount() != 1 {
		t.Errorf("EdgeCount = %d, want 1", g.EdgeCount())
	}
	// Coordinates retained verbatim as [lon,lat], no swap.
	want := graph.LineString{{139.70010, 35.68950}, {139.70220, 35.69010}}
	if got := geom["4001234:0:F"]; !reflect.DeepEqual(got, want) {
		t.Errorf("geometry = %v, want %v", got, want)
	}
}

// TestExpectedBoundsGatesGolden proves WithExpectedBounds actually gates: a box
// that EXCLUDES the golden NYC coordinates (a Tokyo box) causes the golden to be
// rejected as an apparent axis swap, even though it loads fine by default.
func TestExpectedBoundsGatesGolden(t *testing.T) {
	tokyoBox := graph.WithExpectedBounds(139, 140, 35, 36)
	g, geom, err := graph.LoadEdgeAttributesGeoJSON(bytes.NewReader(goldenBytes(t)), tokyoBox)
	if err == nil {
		t.Fatalf("golden must be rejected when expected bounds exclude its coords")
	}
	if g != nil || geom != nil {
		t.Errorf("rejected load must return nil graph/map, got g=%v geom=%v", g, geom)
	}
	if !bytes.Contains([]byte(err.Error()), []byte("axis swap")) {
		t.Errorf("error %q should name the [lat,lon] axis swap", err.Error())
	}
}

// TestExpectedBoundsRejectsNonsenseBox proves a programmer-error bounds box
// (lonMin > lonMax) is reported as a load error rather than silently accepted.
func TestExpectedBoundsRejectsNonsenseBox(t *testing.T) {
	bad := graph.WithExpectedBounds(10, -10, 40, 41)
	g, _, err := graph.LoadEdgeAttributesGeoJSON(bytes.NewReader(goldenBytes(t)), bad)
	if err == nil {
		t.Fatalf("nonsense bounds box must be rejected")
	}
	if g != nil {
		t.Errorf("rejected load must return nil graph")
	}
}

// TestLoadFromFile exercises the file-path convenience wrapper.
func TestLoadFromFile(t *testing.T) {
	g, _, err := graph.LoadEdgeAttributesGeoJSONFile(filepath.Join(fixtureDir, "example_export.geojson"))
	if err != nil {
		t.Fatalf("LoadEdgeAttributesGeoJSONFile: %v", err)
	}
	if g.EdgeCount() != 12 {
		t.Errorf("EdgeCount = %d, want 12", g.EdgeCount())
	}
}
