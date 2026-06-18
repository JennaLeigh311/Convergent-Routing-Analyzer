package main

import (
	"bytes"
	"regexp"
	"strings"
	"testing"
)

// toyGraphFromTestDir is the toy fixture resolved relative to this package's
// directory (go test runs with the package dir as cwd), mirroring cmd/route's
// test. main passes the engine-relative toyNetworkPath instead.
const toyGraphFromTestDir = "../../testdata/toy_network.geojson"

// volatileFields normalizes the two run-to-run-varying tokens out of the bench
// output so determinism can be asserted on the parts that MUST be stable:
//
//   - time=...   the slog text handler stamps every line with wall-clock time.
//   - elapsed=... the naive line reports its own wall-clock routing duration.
//
// Everything else (router names, node/edge counts, requests_routed, field
// ordering) is the deterministic content this harness exists to gate. A router
// leaking nondeterminism (MSA averaging order, multipath split without the fixed
// seed) would change requests_routed or the line set, which survives this
// normalization and fails the byte-identical check below.
var volatileFields = regexp.MustCompile(`(?:time|elapsed)=\S+`)

func normalize(out string) string {
	return volatileFields.ReplaceAllString(out, "")
}

// runOnce executes the harness in-process and returns its captured output, failing
// the test on any load/routing error (which in production exits non-zero).
func runOnce(t *testing.T) string {
	t.Helper()
	var buf bytes.Buffer
	if err := run(&buf, toyGraphFromTestDir); err != nil {
		t.Fatalf("run() returned error: %v", err)
	}
	return buf.String()
}

// TestBenchDeterministic runs the whole harness twice in-process and asserts the
// output is byte-identical after normalizing the wall-clock fields. This is the
// determinism invariant: MSA/systemoptimal must be deterministic via sorted
// iteration (not RNG), and multipath via the fixed benchSeed. Any run-to-run
// drift in flow/ordering/counts fails here — and because this runs under
// `go test -race ./...` (Lane A), it auto-gates the merge.
func TestBenchDeterministic(t *testing.T) {
	first := normalize(runOnce(t))
	second := normalize(runOnce(t))
	if first != second {
		t.Fatalf("bench output is not deterministic across two runs:\n--- first ---\n%s\n--- second ---\n%s", first, second)
	}
}

// TestBenchSummaryShape pins the summary output shape: the exact set of summary
// lines, each router's name, and the field set + ordering on every line. A golden
// string is brittle against the volatile time/elapsed tokens, so this asserts the
// stable structure explicitly (which doubles as the human-readable contract for
// what Lane A greps).
func TestBenchSummaryShape(t *testing.T) {
	lines := strings.Split(strings.TrimRight(runOnce(t), "\n"), "\n")
	if len(lines) != 5 {
		t.Fatalf("want 5 summary lines (naive + 4 phase-3 routers), got %d:\n%s", len(lines), strings.Join(lines, "\n"))
	}

	// The naive line keeps its original message + field ordering byte-for-byte
	// (CI Lane A has greped requests_routed=2 from it since Phase 1). It is the
	// only line carrying elapsed; the four phase-3 lines must not.
	naive := lines[0]
	for _, want := range []string{
		`msg="phase-1 naive-router toy-graph bench"`,
		`component=benchmark`,
		`router=naive`,
		`nodes=6`,
		`edges=7`,
		`requests_routed=2`,
		`elapsed=`,
	} {
		if !strings.Contains(naive, want) {
			t.Errorf("naive summary line missing %q:\n%s", want, naive)
		}
	}
	// Field ordering on the naive line is part of the contract.
	if got := fieldOrder(naive); got != "component router nodes edges requests_routed elapsed" {
		t.Errorf("naive line field order drifted: %q", got)
	}

	// The four phase-3 routers, in this exact order, each on its own line with the
	// same deterministic field set and NO elapsed.
	wantRouters := []string{"incremental", "msa", "systemoptimal", "multipath"}
	for i, name := range wantRouters {
		line := lines[i+1]
		for _, want := range []string{
			`msg="phase-3 router toy-graph bench"`,
			`component=benchmark`,
			"router=" + name,
			`nodes=6`,
			`edges=7`,
			`requests_routed=2`,
		} {
			if !strings.Contains(line, want) {
				t.Errorf("%s summary line missing %q:\n%s", name, want, line)
			}
		}
		if strings.Contains(line, "elapsed=") {
			t.Errorf("%s summary line must not carry a volatile elapsed= field:\n%s", name, line)
		}
		if got := fieldOrder(line); got != "component router nodes edges requests_routed" {
			t.Errorf("%s line field order drifted: %q", name, got)
		}
	}
}

// fieldKey matches the slog text-handler key=value tokens AFTER the leading
// time/level/msg, capturing the field key. msg= can contain quoted spaces, so we
// only collect keys that appear after it; the structured fields this harness emits
// are all unquoted key=value, so a simple key regex over the tail is sufficient.
var fieldKey = regexp.MustCompile(`(\w+)=`)

// fieldOrder returns the space-joined sequence of structured field keys on a
// summary line, excluding the handler's own time/level/msg, so a field reordering
// (which would silently change what CI greps) is caught.
func fieldOrder(line string) string {
	// Drop everything up to and including the msg="..." token so the quoted
	// message body never contributes spurious keys.
	if idx := strings.Index(line, `msg=`); idx >= 0 {
		// msg value is quoted; advance past the closing quote.
		rest := line[idx+len(`msg=`):]
		if strings.HasPrefix(rest, `"`) {
			if end := strings.Index(rest[1:], `"`); end >= 0 {
				line = rest[end+2:]
			}
		}
	}
	var keys []string
	for _, m := range fieldKey.FindAllStringSubmatch(line, -1) {
		keys = append(keys, m[1])
	}
	return strings.Join(keys, " ")
}
