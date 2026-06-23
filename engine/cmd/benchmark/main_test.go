package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// toyGraphFromTestDir is the toy fixture resolved relative to this package's
// directory (go test runs with the package dir as cwd). main passes the
// engine-relative toyNetworkPath instead.
const toyGraphFromTestDir = "../../testdata/toy_network.geojson"

// runOnce executes the harness in-process against the toy graph, capturing the
// deterministic stdout JSON and writing the table into a per-test temp docs file
// (so the real docs/benchmarks.md is never touched by the test). Logs are
// discarded — they carry the volatile time=/elapsed= fields the JSON deliberately
// excludes. It fails the test on any load/routing/IO error (which in production
// exits non-zero).
func runOnce(t *testing.T) string {
	t.Helper()
	var stdout bytes.Buffer
	docsPath := filepath.Join(t.TempDir(), "benchmarks.md")
	// Seed the temp doc with the marker region so the harness replaces it in place,
	// exercising the same path the real doc takes.
	seed := tableBeginMarker + "\n\n(placeholder)\n\n" + tableEndMarker + "\n"
	if err := os.WriteFile(docsPath, []byte(seed), 0o644); err != nil {
		t.Fatalf("seed temp docs: %v", err)
	}
	if err := run(&stdout, io.Discard, toyGraphFromTestDir, docsPath); err != nil {
		t.Fatalf("run() returned error: %v", err)
	}
	return stdout.String()
}

// TestBenchDeterministic runs the whole harness twice in-process and asserts the
// stdout JSON is BYTE-IDENTICAL. The JSON carries no wall-clock field (those go to
// the log writer, discarded here), so no normalization is needed — any run-to-run
// drift in OD draw, flow accumulation, the multipath split, or the simulator-mode
// total fails here. Because this runs under `go test -race ./...` (Lane A), it
// auto-gates the merge.
func TestBenchDeterministic(t *testing.T) {
	first := runOnce(t)
	second := runOnce(t)
	if first != second {
		t.Fatalf("bench JSON is not deterministic across two runs:\n--- first ---\n%s\n--- second ---\n%s", first, second)
	}
}

// TestBenchJSONShape decodes the stdout JSON and pins the comparison grid's shape:
// six routers × four demand levels = 24 cells, the four per-level PoA entries, and a
// headline improvement carrying a demand level. This is the structural contract Lane
// A's "every router appears" assertion gates the binary on.
func TestBenchJSONShape(t *testing.T) {
	var report struct {
		Seed    int64 `json:"seed"`
		ODCount int   `json:"od_count"`
		Cells   []struct {
			Result struct {
				Router      string `json:"router"`
				DemandLevel string `json:"demand_level"`
			} `json:"result"`
			PoA float64 `json:"poa"`
		} `json:"cells"`
		PoAByLevel []struct {
			DemandLevel string  `json:"demand_level"`
			PoA         float64 `json:"poa"`
		} `json:"poa_by_level"`
		Headline struct {
			DemandLevel string `json:"demand_level"`
		} `json:"headline_improvement"`
	}
	if err := json.Unmarshal([]byte(runOnce(t)), &report); err != nil {
		t.Fatalf("decode bench JSON: %v", err)
	}

	if got, want := len(report.Cells), 24; got != want {
		t.Errorf("cell count = %d, want %d (6 routers × 4 levels)", got, want)
	}
	// Every one of the six routers must appear at every level.
	wantRouters := []string{"naive", "reactive", "incremental", "msa", "systemoptimal", "multipath"}
	wantLevels := []string{"vc0.5", "vc0.8", "vc1.0", "vc1.2"}
	seen := make(map[string]bool, len(report.Cells))
	for _, c := range report.Cells {
		seen[c.Result.Router+"@"+c.Result.DemandLevel] = true
	}
	for _, r := range wantRouters {
		for _, l := range wantLevels {
			if !seen[r+"@"+l] {
				t.Errorf("missing cell for router %q at level %q", r, l)
			}
		}
	}
	if got := len(report.PoAByLevel); got != len(wantLevels) {
		t.Errorf("poa_by_level entries = %d, want %d", got, len(wantLevels))
	}
	if report.Headline.DemandLevel == "" {
		t.Error("headline improvement is missing its demand level")
	}
}

// TestBenchDocsRefresh asserts the harness rewrites ONLY the marker-bounded region
// of the docs file, leaving surrounding prose intact, and that the refreshed region
// holds the rendered table header.
func TestBenchDocsRefresh(t *testing.T) {
	docsPath := filepath.Join(t.TempDir(), "benchmarks.md")
	const prose = "# Title\n\nKeep me.\n\n"
	seed := prose + tableBeginMarker + "\n\n(placeholder)\n\n" + tableEndMarker + "\n\nAlso keep me.\n"
	if err := os.WriteFile(docsPath, []byte(seed), 0o644); err != nil {
		t.Fatalf("seed temp docs: %v", err)
	}
	if err := run(io.Discard, io.Discard, toyGraphFromTestDir, docsPath); err != nil {
		t.Fatalf("run() returned error: %v", err)
	}
	got, err := os.ReadFile(docsPath)
	if err != nil {
		t.Fatalf("read refreshed docs: %v", err)
	}
	out := string(got)
	for _, want := range []string{"Keep me.", "Also keep me.", "| router | demand | cap_scale |"} {
		if !strings.Contains(out, want) {
			t.Errorf("refreshed docs missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "(placeholder)") {
		t.Errorf("refreshed docs still contains the placeholder; region not replaced:\n%s", out)
	}
	// Exactly one marker pair survives a refresh (idempotent region replacement).
	if n := strings.Count(out, tableBeginMarker); n != 1 {
		t.Errorf("begin marker count = %d, want 1", n)
	}
	if n := strings.Count(out, tableEndMarker); n != 1 {
		t.Errorf("end marker count = %d, want 1", n)
	}
}

// TestBenchDocsRefreshIdempotent runs the refresh twice over the same temp doc and
// asserts the file is byte-identical the second time — a re-run must not duplicate
// the table region or drift the surrounding prose.
func TestBenchDocsRefreshIdempotent(t *testing.T) {
	docsPath := filepath.Join(t.TempDir(), "benchmarks.md")
	seed := "# T\n\n" + tableBeginMarker + "\n\n(x)\n\n" + tableEndMarker + "\n"
	if err := os.WriteFile(docsPath, []byte(seed), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := run(io.Discard, io.Discard, toyGraphFromTestDir, docsPath); err != nil {
		t.Fatalf("first run: %v", err)
	}
	afterFirst, _ := os.ReadFile(docsPath)
	if err := run(io.Discard, io.Discard, toyGraphFromTestDir, docsPath); err != nil {
		t.Fatalf("second run: %v", err)
	}
	afterSecond, _ := os.ReadFile(docsPath)
	if !bytes.Equal(afterFirst, afterSecond) {
		t.Fatalf("docs refresh is not idempotent:\n--- first ---\n%s\n--- second ---\n%s", afterFirst, afterSecond)
	}
}

// TestBenchBadGraphPath covers run()'s load-failure branch: a nonexistent graph path
// must return a non-nil error (which main turns into a non-zero exit), so a
// missing/unreadable network fails the build rather than silently routing nothing.
func TestBenchBadGraphPath(t *testing.T) {
	docsPath := filepath.Join(t.TempDir(), "benchmarks.md")
	if err := run(io.Discard, io.Discard, "../../testdata/does-not-exist.geojson", docsPath); err == nil {
		t.Fatal("run() with a nonexistent graph path returned nil error; want non-nil")
	}
}

// TestBenchBadDocsPath covers the docs-write failure branch: a docs path inside a
// nonexistent directory must return a non-nil error so a doc that cannot be
// refreshed fails the build rather than silently shipping stale.
func TestBenchBadDocsPath(t *testing.T) {
	bad := filepath.Join(t.TempDir(), "no-such-dir", "benchmarks.md")
	if err := run(io.Discard, io.Discard, toyGraphFromTestDir, bad); err == nil {
		t.Fatal("run() with an unwritable docs path returned nil error; want non-nil")
	}
}
