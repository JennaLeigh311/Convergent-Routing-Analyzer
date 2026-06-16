package source

import (
	"strings"
	"testing"

	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/congestion/static"
	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/domain"
	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/graph"
)

// toyNetwork is the module-shared toy fixture; from internal/congestion/source the
// relative path to engine/testdata is ../../../testdata.
const toyNetwork = "../../../testdata/toy_network.geojson"

// jamMotorwayCongestion is the §3 segment-congestion batch that jams the motorway
// corridor (905512:0:F); it exercises the file Source branch (decode ->
// segment->edge join -> static.NewProvider).
const jamMotorwayCongestion = "../../../testdata/toy_congestion_jam_motorway.json"

// motorwaySegment is the toy graph's 2-hop motorway corridor edge — the segment the
// Phase-2 divert demo jams. jamLoadVPH is an arbitrary positive load used to assert
// an injection actually lands on the resolved edge.
const (
	motorwaySegment = "905512:0:F"
	jamLoadVPH      = 12345.0
)

// fileJamVPH is the load the jamMotorwayCongestion fixture lands on the motorway
// edge: its single §3 message carries vehicle_count 5000 over a 5-minute window,
// which the ×12 annualization (twelve 5-minute windows per hour, docs/contracts.md
// §3) scales to vehicles/hour. Written as the arithmetic rather than a baked
// literal so it cannot silently rot if the fixture or the window→vph conversion
// changes.
const fileJamVPH = 5000 * 12 // = 60000 vph

// loadToyGraph loads the shared toy network or fails the test; every case needs a
// graph to size the dense provider and resolve a jam segment_id.
func loadToyGraph(test *testing.T) graph.Graph {
	test.Helper()
	roadGraph, _, err := graph.LoadEdgeAttributesGeoJSONFile(toyNetwork)
	if err != nil {
		test.Fatalf("load toy graph %q: %v", toyNetwork, err)
	}
	return roadGraph
}

// motorwayEdgeID resolves the motorway corridor's dense EdgeID via the same
// segment->edge index Build uses, so an injection assertion can read Load at the
// exact edge the jam targets.
func motorwayEdgeID(test *testing.T, roadGraph graph.Graph) domain.EdgeID {
	test.Helper()
	edgeID, found := static.BuildSegmentEdgeIndex(roadGraph)[domain.SegmentID(motorwaySegment)]
	if !found {
		test.Fatalf("toy graph is missing the motorway segment %q", motorwaySegment)
	}
	return edgeID
}

// TestBuildSucceeds covers the three sources that build a provider, with and without
// a jam injection, and asserts that an injected jam actually carries the load through
// the returned provider's Load (so a silently-dropped injection fails the test).
func TestBuildSucceeds(test *testing.T) {
	roadGraph := loadToyGraph(test)
	jammedEdge := motorwayEdgeID(test, roadGraph)

	for _, testCase := range []struct {
		name     string
		spec     Spec
		wantLoad float64 // expected Load at the motorway edge after Build
	}{
		{
			name:     "sim no jam",
			spec:     Spec{Source: SimSource},
			wantLoad: 0,
		},
		{
			name:     "sim with jam",
			spec:     Spec{Source: SimSource, JamSegment: motorwaySegment, JamVPH: jamLoadVPH},
			wantLoad: jamLoadVPH,
		},
		{
			name:     "empty source no jam",
			spec:     Spec{Source: ""},
			wantLoad: 0,
		},
		{
			name:     "empty source with jam",
			spec:     Spec{Source: "", JamSegment: motorwaySegment, JamVPH: jamLoadVPH},
			wantLoad: jamLoadVPH,
		},
		{
			name:     "file source",
			spec:     Spec{Source: jamMotorwayCongestion},
			wantLoad: fileJamVPH,
		},
	} {
		test.Run(testCase.name, func(test *testing.T) {
			provider, err := Build(roadGraph, testCase.spec)
			if err != nil {
				test.Fatalf("Build(%+v) error = %v, want nil", testCase.spec, err)
			}
			if provider == nil {
				test.Fatalf("Build(%+v) provider = nil, want non-nil", testCase.spec)
			}
			if got := provider.Load(jammedEdge); got != testCase.wantLoad {
				test.Errorf("Load(motorway) = %v, want %v", got, testCase.wantLoad)
			}
			// The snapshot must agree with Load at the jammed edge — the provider
			// returns a consistent view through both faces of the port.
			if got := provider.Snapshot().Load(jammedEdge); got != testCase.wantLoad {
				test.Errorf("Snapshot().Load(motorway) = %v, want %v", got, testCase.wantLoad)
			}
			// View() must also agree: it is the allocation-free read-only borrow the
			// single-request Route path takes (issue #67), so a provider whose View
			// aliased the wrong slice (or a static/simulator adapter that returned a
			// stale or empty borrow) would diverge from Load/Snapshot and fail here.
			// This one assertion pins View/Snapshot parity across every source —
			// simulator, in-memory, and the file-backed static provider.
			if got := provider.View().Load(jammedEdge); got != testCase.wantLoad {
				test.Errorf("View().Load(motorway) = %v, want %v", got, testCase.wantLoad)
			}
		})
	}
}

// TestBuildJamWithFileSourceRejected pins the intentional contradiction guard: a jam
// alongside a file Source is rejected (the file already carries the loads) rather than
// silently dropped. The error names the conflict so the CLI's "route:"-prefixed wrap
// stays informative.
func TestBuildJamWithFileSourceRejected(test *testing.T) {
	roadGraph := loadToyGraph(test)

	_, err := Build(roadGraph, Spec{
		Source:     jamMotorwayCongestion,
		JamSegment: motorwaySegment,
		JamVPH:     jamLoadVPH,
	})
	if err == nil {
		test.Fatalf("Build with jam + file source error = nil, want a contradiction error")
	}
	if msg := err.Error(); !strings.Contains(msg, "cannot combine -jam") {
		test.Errorf("error = %q, want it to reject combining -jam with a file source", msg)
	}
}

// TestBuildUnknownJamSegment: an unknown jam segment_id is a clean error naming the
// bad segment (a typo fails loudly), not a silent no-op that would yield the
// un-diverted route. Covered for both injectable sources (sim and empty).
func TestBuildUnknownJamSegment(test *testing.T) {
	roadGraph := loadToyGraph(test)

	for _, testCase := range []struct {
		name   string
		source string
	}{
		{name: "sim", source: SimSource},
		{name: "empty", source: ""},
	} {
		test.Run(testCase.name, func(test *testing.T) {
			_, err := Build(roadGraph, Spec{
				Source:     testCase.source,
				JamSegment: "no-such-segment",
				JamVPH:     jamLoadVPH,
			})
			if err == nil {
				test.Fatalf("Build with unknown jam segment error = nil, want an error")
			}
			msg := err.Error()
			if !strings.Contains(msg, "invalid -jam") {
				test.Errorf("error = %q, want an invalid -jam error", msg)
			}
			if !strings.Contains(msg, "no-such-segment") {
				test.Errorf("error = %q, want it to name the bad segment_id", msg)
			}
		})
	}
}

// TestBuildFileSourceNotFound: a file Source that does not exist surfaces a wrapped
// "load congestion" error (not a panic), the same wrap the CLI's
// TestRunReactiveCongestionFileNotFound asserts on.
func TestBuildFileSourceNotFound(test *testing.T) {
	roadGraph := loadToyGraph(test)

	_, err := Build(roadGraph, Spec{Source: "../../../testdata/does_not_exist.json"})
	if err == nil {
		test.Fatalf("Build with missing file source error = nil, want an error")
	}
	if msg := err.Error(); !strings.Contains(msg, "load congestion") {
		test.Errorf("error = %q, want it to name the congestion load failure", msg)
	}
}
