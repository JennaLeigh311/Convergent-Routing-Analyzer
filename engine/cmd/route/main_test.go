package main

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

// toyGraph is the module-shared toy fixture; from cmd/route the relative path is
// ../../testdata. Tests pass it explicitly rather than relying on the flag
// default (which is resolved against the engine/ CWD of `go run ./cmd/route`).
const toyGraph = "../../testdata/toy_network.geojson"

// canonicalRouteGolden is the single source of truth for the canonical demo
// route's STDOUT (node 0 -> node 2). CI's route-CLI smoke asserts the real
// binary against this same file, so the expected output lives in exactly one
// place. Path is ../../testdata for the same reason as toyGraph.
const canonicalRouteGolden = "../../testdata/canonical_route.golden"

// TestRunCanonicalLowestCostPath pins the headline acceptance property: from
// node 0 to node 2 the router takes the 2-hop motorway path (32.4 s), NOT the
// 1-hop residential edge (108.0 s) — lowest-cost ≠ fewest-hops. The expected
// output is the golden file (an exact match is strictly stronger than the old
// contains/not-contains checks: it also rejects the residential edge and cost).
func TestRunCanonicalLowestCostPath(test *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{
		"-graph", toyGraph,
		"-from", defaultFrom, // node 0
		"-to", defaultTo, // node 2
	}, &stdout, &stderr)

	if code != 0 {
		test.Fatalf("exit code = %d, want 0; stderr=%q", code, stderr.String())
	}
	want, err := os.ReadFile(canonicalRouteGolden)
	if err != nil {
		test.Fatalf("read golden %q: %v", canonicalRouteGolden, err)
	}
	if got := stdout.String(); got != string(want) {
		test.Errorf("route output = %q, want it to equal %s = %q", got, canonicalRouteGolden, string(want))
	}
}

// TestRunReactiveJamDiverts pins the Phase-2 headline property (project-spec.md
// §6): the SAME node 0 -> node 2 request diverts under congestion. With
// --algo reactive --jam 905512:0:F the motorway corridor is jammed, so reactive
// must NOT return the naive free-flow path (the 2-hop motorway) but the 1-hop
// residential edge 9000001:0:F instead. We assert the reactive output differs
// from the naive output, names the residential segment, exits 0, and carries a
// cost line.
func TestRunReactiveJamDiverts(test *testing.T) {
	var naiveStdout, naiveStderr bytes.Buffer
	naiveCode := run([]string{
		"-graph", toyGraph,
		"-from", defaultFrom, // node 0
		"-to", defaultTo, // node 2
		"-algo", "naive",
	}, &naiveStdout, &naiveStderr)
	if naiveCode != 0 {
		test.Fatalf("naive exit code = %d, want 0; stderr=%q", naiveCode, naiveStderr.String())
	}

	var reactiveStdout, reactiveStderr bytes.Buffer
	reactiveCode := run([]string{
		"-graph", toyGraph,
		"-from", defaultFrom, // node 0
		"-to", defaultTo, // node 2
		"-algo", "reactive",
		"-jam", "905512:0:F", // jam the motorway corridor
	}, &reactiveStdout, &reactiveStderr)
	if reactiveCode != 0 {
		test.Fatalf("reactive exit code = %d, want 0; stderr=%q", reactiveCode, reactiveStderr.String())
	}

	naiveOut := naiveStdout.String()
	reactiveOut := reactiveStdout.String()
	if reactiveOut == naiveOut {
		test.Errorf("reactive-under-jam output = %q, want it to DIFFER from naive %q (the divert)", reactiveOut, naiveOut)
	}
	// Pin the diverted PATH exactly (the single 1-hop residential edge) rather
	// than a substring, so a wrong-but-different path can't pass. The cost line
	// stays a loose check: reactive's cost is the congested BPR value, which is
	// sensitive to cost-function tuning, so pinning it would be a brittle golden.
	if reactivePath := firstLine(reactiveOut); reactivePath != "9000001:0:F" {
		test.Errorf("reactive-under-jam path = %q, want exactly the residential edge %q", reactivePath, "9000001:0:F")
	}
	if !strings.Contains(reactiveOut, "cost") {
		test.Errorf("reactive-under-jam output = %q, want a cost line", reactiveOut)
	}
}

// TestRunReactiveNoJamMatchesNaive pins the zero-load equivalence: reactive with
// NO congestion injected weights edges against an all-zero snapshot, where BPR
// collapses to the free-flow ordering, so reactive must choose the SAME path as
// naive. (The reactive cost line is the congested BPR cost, which equals
// free-flow at zero load, so the whole output coincides here.)
func TestRunReactiveNoJamMatchesNaive(test *testing.T) {
	var naiveStdout, naiveStderr bytes.Buffer
	naiveCode := run([]string{
		"-graph", toyGraph,
		"-from", defaultFrom, // node 0
		"-to", defaultTo, // node 2
		"-algo", "naive",
	}, &naiveStdout, &naiveStderr)
	if naiveCode != 0 {
		test.Fatalf("naive exit code = %d, want 0; stderr=%q", naiveCode, naiveStderr.String())
	}

	var reactiveStdout, reactiveStderr bytes.Buffer
	reactiveCode := run([]string{
		"-graph", toyGraph,
		"-from", defaultFrom, // node 0
		"-to", defaultTo, // node 2
		"-algo", "reactive", // no -jam: zero-load snapshot
	}, &reactiveStdout, &reactiveStderr)
	if reactiveCode != 0 {
		test.Fatalf("reactive exit code = %d, want 0; stderr=%q", reactiveCode, reactiveStderr.String())
	}

	naivePath := firstLine(naiveStdout.String())
	reactivePath := firstLine(reactiveStdout.String())
	if reactivePath != naivePath {
		test.Errorf("reactive no-jam path = %q, want it to equal the naive path %q (zero-load reactive == naive)", reactivePath, naivePath)
	}
}

// TestRunReactiveUnknownJam: an unknown --jam segment_id is a clean user error,
// not a silent no-op that would print the un-diverted route. Expect a non-zero
// exit and a "route:" stderr message NAMING the bad segment.
func TestRunReactiveUnknownJam(test *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{
		"-graph", toyGraph,
		"-from", defaultFrom,
		"-to", defaultTo,
		"-algo", "reactive",
		"-jam", "no-such-segment",
	}, &stdout, &stderr)

	if code == 0 {
		test.Fatalf("exit code = 0, want non-zero; stdout=%q", stdout.String())
	}
	msg := stderr.String()
	if !strings.Contains(msg, "route:") {
		test.Errorf("stderr = %q, want a route:-prefixed error", msg)
	}
	if !strings.Contains(msg, "no-such-segment") {
		test.Errorf("stderr = %q, want it to name the bad -jam segment_id", msg)
	}
}

// firstLine returns text up to (not including) the first newline — the path line
// of the route output, used to compare paths while ignoring the trailing cost
// line (reactive's congested cost differs from naive's free-flow cost even when
// the path is identical).
func firstLine(text string) string {
	if index := strings.IndexByte(text, '\n'); index >= 0 {
		return text[:index]
	}
	return text
}

// jamMotorwayCongestion is a §3 segment-congestion batch that jams the motorway
// corridor (905512:0:F) at 5000 vehicles in a 5-minute window — ×12 annualized to
// 60000 vph, well past the toy-graph divert threshold. It exercises the file
// -congestion branch (DecodeSegmentCongestionFile -> BuildSegmentEdgeIndex ->
// static.NewProvider), which the --jam-injected branches do not cover.
const jamMotorwayCongestion = "../../testdata/toy_congestion_jam_motorway.json"

// TestRunReactiveCongestionFile pins the file-backed congestion path (the most
// logic-heavy reactive branch): a §3 batch jamming the motorway must divert
// reactive onto the residential edge 9000001:0:F, exit 0, and carry a cost line.
// This is the only test exercising decode + segment->edge join + static.Provider
// wiring through the CLI, so a regression in any of those surfaces here.
func TestRunReactiveCongestionFile(test *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{
		"-graph", toyGraph,
		"-from", defaultFrom, // node 0
		"-to", defaultTo, // node 2
		"-algo", "reactive",
		"-congestion", jamMotorwayCongestion,
	}, &stdout, &stderr)

	if code != 0 {
		test.Fatalf("exit code = %d, want 0; stderr=%q", code, stderr.String())
	}
	if path := firstLine(stdout.String()); path != "9000001:0:F" {
		test.Errorf("file-congestion path = %q, want the residential edge %q (the file jams the motorway)", path, "9000001:0:F")
	}
	if !strings.Contains(stdout.String(), "cost") {
		test.Errorf("stdout = %q, want a cost line", stdout.String())
	}
}

// TestRunReactiveCongestionFileNotFound: a -congestion path that does not exist
// is a clean user error (non-zero exit, a "route:" stderr message naming the load
// failure), not a panic.
func TestRunReactiveCongestionFileNotFound(test *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{
		"-graph", toyGraph,
		"-from", defaultFrom,
		"-to", defaultTo,
		"-algo", "reactive",
		"-congestion", "../../testdata/does_not_exist.json",
	}, &stdout, &stderr)

	if code == 0 {
		test.Fatalf("exit code = 0, want non-zero; stdout=%q", stdout.String())
	}
	if msg := stderr.String(); !strings.Contains(msg, "route:") || !strings.Contains(msg, "load congestion") {
		test.Errorf("stderr = %q, want a route: error naming the congestion load failure", msg)
	}
}

// TestRunReactiveJamWithFileCongestionRejected pins the intentional contradiction
// guard: --jam alongside a file -congestion is rejected (the file already carries
// the loads), rather than silently dropping the jam. Expect a non-zero exit and a
// "route:" stderr message naming the conflict.
func TestRunReactiveJamWithFileCongestionRejected(test *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{
		"-graph", toyGraph,
		"-from", defaultFrom,
		"-to", defaultTo,
		"-algo", "reactive",
		"-congestion", jamMotorwayCongestion,
		"-jam", "905512:0:F",
	}, &stdout, &stderr)

	if code == 0 {
		test.Fatalf("exit code = 0, want non-zero; stdout=%q", stdout.String())
	}
	if msg := stderr.String(); !strings.Contains(msg, "route:") || !strings.Contains(msg, "cannot combine -jam") {
		test.Errorf("stderr = %q, want a route: error rejecting -jam with a file -congestion", msg)
	}
}

// TestRunInvalidAlgo: an -algo value outside {naive, reactive} is a clean user
// error (non-zero exit, a "route:" stderr message naming the bad value), the
// first validation a fat-fingered invocation hits.
func TestRunInvalidAlgo(test *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{
		"-graph", toyGraph,
		"-from", defaultFrom,
		"-to", defaultTo,
		"-algo", "bogus",
	}, &stdout, &stderr)

	if code == 0 {
		test.Fatalf("exit code = 0, want non-zero; stdout=%q", stdout.String())
	}
	if msg := stderr.String(); !strings.Contains(msg, "invalid -algo") || !strings.Contains(msg, "bogus") {
		test.Errorf("stderr = %q, want it to name the invalid -algo value", msg)
	}
}

// TestRunReactiveNonPositiveJamVPH: a non-positive --jam-vph floors to zero load
// and would silently NOT divert, so it is rejected as a likely mistake (non-zero
// exit, a "route:" message naming the bad value) rather than printing the
// un-diverted route. Table-driven over zero and a negative value.
func TestRunReactiveNonPositiveJamVPH(test *testing.T) {
	for _, testCase := range []struct {
		name  string
		value string
	}{
		{name: "zero", value: "0"},
		{name: "negative", value: "-100"},
	} {
		test.Run(testCase.name, func(test *testing.T) {
			var stdout, stderr bytes.Buffer
			code := run([]string{
				"-graph", toyGraph,
				"-from", defaultFrom,
				"-to", defaultTo,
				"-algo", "reactive",
				"-jam", "905512:0:F",
				"-jam-vph", testCase.value,
			}, &stdout, &stderr)

			if code == 0 {
				test.Fatalf("exit code = 0, want non-zero; stdout=%q", stdout.String())
			}
			if msg := stderr.String(); !strings.Contains(msg, "invalid -jam-vph") {
				test.Errorf("stderr = %q, want it to reject the non-positive -jam-vph", msg)
			}
		})
	}
}

// TestRunUnroutablePair: node 5 is a sink (no outgoing edges), so no path
// reaches node 0. Expect a non-zero exit and a stderr message naming the
// unroutable condition.
func TestRunUnroutablePair(test *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{
		"-graph", toyGraph,
		"-from", "40.748,-73.955", // node 5, a sink
		"-to", "40.73,-73.99", // node 0
	}, &stdout, &stderr)

	if code == 0 {
		test.Fatalf("exit code = 0, want non-zero; stdout=%q", stdout.String())
	}
	if msg := stderr.String(); !strings.Contains(msg, "no path") {
		test.Errorf("stderr = %q, want it to name the unroutable condition (\"no path\")", msg)
	}
}

// TestRunSameNode: origin and destination snap to the same node — a clean
// zero-edge, zero-cost result, exit 0 (not an error).
func TestRunSameNode(test *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{
		"-graph", toyGraph,
		"-from", "40.73,-73.99", // node 0
		"-to", "40.73,-73.99", // node 0
	}, &stdout, &stderr)

	if code != 0 {
		test.Fatalf("exit code = %d, want 0; stderr=%q", code, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "0 edges") {
		test.Errorf("stdout = %q, want a zero-edge result", out)
	}
	if !strings.Contains(out, "0.0") {
		test.Errorf("stdout = %q, want cost 0.0", out)
	}
}

// TestRunMalformedFrom: a non-coordinate -from is rejected before any graph
// work, non-zero exit with a clear stderr message.
func TestRunMalformedFrom(test *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{
		"-graph", toyGraph,
		"-from", "not,coords",
		"-to", "40.74,-73.97",
	}, &stdout, &stderr)

	if code == 0 {
		test.Fatalf("exit code = 0, want non-zero; stdout=%q", stdout.String())
	}
	if msg := stderr.String(); !strings.Contains(msg, "invalid -from") {
		test.Errorf("stderr = %q, want it to name the invalid -from input", msg)
	}
}

// TestRunMalformedFromSingleValue: a single value (no comma) is also rejected.
func TestRunMalformedFromSingleValue(test *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{
		"-graph", toyGraph,
		"-from", "40.73",
		"-to", "40.74,-73.97",
	}, &stdout, &stderr)

	if code == 0 {
		test.Fatalf("exit code = 0, want non-zero; stdout=%q", stdout.String())
	}
	if msg := stderr.String(); !strings.Contains(msg, "invalid -from") {
		test.Errorf("stderr = %q, want it to name the invalid -from input", msg)
	}
}

// TestRunMalformedTo mirrors the -from rejection for -to: the destination parse
// is a separate code path, so it gets its own guard against a future refactor
// dropping or mislabeling it (e.g. saying "invalid -from" for both).
func TestRunMalformedTo(test *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{
		"-graph", toyGraph,
		"-from", "40.73,-73.99",
		"-to", "not,coords",
	}, &stdout, &stderr)

	if code == 0 {
		test.Fatalf("exit code = 0, want non-zero; stdout=%q", stdout.String())
	}
	if msg := stderr.String(); !strings.Contains(msg, "invalid -to") {
		test.Errorf("stderr = %q, want it to name the invalid -to input", msg)
	}
}

// TestRunGraphLoadError: a -graph path that does not exist is a clean user error
// (non-zero exit, a stderr message naming the load failure), not a panic.
func TestRunGraphLoadError(test *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{
		"-graph", "../../testdata/does_not_exist.geojson",
		"-from", "40.73,-73.99",
		"-to", "40.74,-73.97",
	}, &stdout, &stderr)

	if code != 1 {
		test.Fatalf("exit code = %d, want 1; stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if msg := stderr.String(); !strings.Contains(msg, "load graph") {
		test.Errorf("stderr = %q, want it to name the graph load failure", msg)
	}
}

// TestRunFarCoordinateSnaps characterizes a deliberately surprising property:
// there is NO proximity guard. NearestNode snaps any valid coordinate to the
// closest graph node with no distance threshold, so a destination at (0,0) — the
// Gulf of Guinea — still produces a valid route over the NYC toy graph and exits
// 0. Every node is reachable from node 0, so this holds whichever node (0,0)
// snaps to. This pins the snap-anything decision so a future reader does not
// mistake it for a bug, and fails loudly if someone later adds a guard without
// updating this caller.
func TestRunFarCoordinateSnaps(test *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{
		"-graph", toyGraph,
		"-from", "40.73,-73.99", // node 0
		"-to", "0,0", // far off-network — snaps to some NYC node anyway
	}, &stdout, &stderr)

	if code != 0 {
		test.Fatalf("exit code = %d, want 0 (a far coordinate still snaps); stderr=%q", code, stderr.String())
	}
	if out := stdout.String(); !strings.Contains(out, "cost") {
		test.Errorf("stdout = %q, want a routed result with a cost line", out)
	}
	if msg := stderr.String(); strings.Contains(msg, "no graph node near") {
		test.Errorf("stderr = %q, must not claim no node is near — there is no proximity guard", msg)
	}
}
