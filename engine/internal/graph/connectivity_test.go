package graph_test

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/graph"
)

// adversarialFixture is the §2-conformant adversarial export (#73): main
// component {0,1,2,3} plus a disconnected island {4,5}. It is the right fixture
// for the connectivity diagnostic because its weak-connectivity split (the
// island) is real while its one-way trap (node 3) is NOT a weak split — proving
// the diagnostic uses weak, not strong, connectivity.
const adversarialFixture = "../../testdata/toy_network_adversarial.geojson"

// captureWarn installs a fresh slog default that writes Warn-level text to the
// returned buffer, restoring the previous default when the test's cleanup runs.
func captureWarn(test *testing.T) *bytes.Buffer {
	test.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	test.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

// TestConnectivityWarnFiresOnDisconnected asserts that, with WithConnectivityWarn
// enabled, the adversarial fixture (which has a disconnected {4,5} island) still
// loads successfully AND emits the non-fatal weak-connectivity warning reporting
// 2 components with nodes 4 and 5 in the unreachable sample.
func TestConnectivityWarnFiresOnDisconnected(test *testing.T) {
	buf := captureWarn(test)

	roadGraph, geom, err := graph.LoadEdgeAttributesGeoJSONFile(adversarialFixture, graph.WithConnectivityWarn())
	if err != nil {
		test.Fatalf("adversarial fixture must still load with WithConnectivityWarn, got error: %v", err)
	}
	if roadGraph == nil {
		test.Fatal("graph must be non-nil even when a connectivity warning fires")
	}
	if geom == nil {
		test.Fatal("geometry map must be non-nil even when a connectivity warning fires")
	}

	logged := buf.String()
	if !strings.Contains(logged, "not weakly connected") {
		test.Errorf("expected a 'not weakly connected' warning, got log: %q", logged)
	}
	if !strings.Contains(logged, "components=2") {
		test.Errorf("expected the warning to report components=2, got log: %q", logged)
	}
	// The island {4,5} is the unreachable set; both ids must appear in the sample.
	if !strings.Contains(logged, "4") || !strings.Contains(logged, "5") {
		test.Errorf("expected unreachable sample to include nodes 4 and 5, got log: %q", logged)
	}
	if !strings.Contains(logged, "unreachable_nodes=2") {
		test.Errorf("expected unreachable_nodes=2, got log: %q", logged)
	}
}

// TestConnectivityWarnOnGolden asserts WithConnectivityWarn's behavior on the
// golden export. The issue #82 design called for a "silent on a fully-connected
// golden" test IF the golden is fully weakly connected — but it is NOT. The
// golden example_export.geojson is a synthetic §2-conformance fixture built from
// independent edge pairs, not a connected road network: its 18 nodes form 8
// disconnected weak components (sizes 3,3,2,2,2,2,2,2 over export ids 10..81). So
// this test instead asserts the (correct) outcome that the warning DOES fire and
// the export still loads — which equally proves the diagnostic is non-fatal and
// that weak connectivity is computed as intended. (Noted in the PR body.)
func TestConnectivityWarnOnGolden(test *testing.T) {
	buf := captureWarn(test)

	roadGraph, _, err := graph.LoadEdgeAttributesGeoJSON(bytes.NewReader(goldenBytes(test)), graph.WithConnectivityWarn())
	if err != nil {
		test.Fatalf("golden fixture must load cleanly with WithConnectivityWarn, got: %v", err)
	}
	if roadGraph == nil {
		test.Fatal("golden graph must be non-nil even when a connectivity warning fires")
	}

	logged := buf.String()
	if !strings.Contains(logged, "not weakly connected") {
		test.Errorf("golden export is fragmented (8 weak components), so a warning must fire, got log: %q", logged)
	}
	if !strings.Contains(logged, "components=8") {
		test.Errorf("expected the golden warning to report components=8, got log: %q", logged)
	}
}

// TestConnectivityWarnDefaultOffEmitsNothing asserts the default (option-off)
// load of the adversarial fixture — which IS disconnected — emits no warning,
// preserving byte-identical default behavior.
func TestConnectivityWarnDefaultOffEmitsNothing(test *testing.T) {
	buf := captureWarn(test)

	roadGraph, _, err := graph.LoadEdgeAttributesGeoJSONFile(adversarialFixture)
	if err != nil {
		test.Fatalf("adversarial fixture must load without options, got: %v", err)
	}
	if roadGraph == nil {
		test.Fatal("graph must be non-nil")
	}

	if logged := buf.String(); logged != "" {
		test.Errorf("default load (no WithConnectivityWarn) must emit no connectivity warning, got: %q", logged)
	}
}
