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
