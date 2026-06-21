package benchmark_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/benchmark"
)

// sampleResult is a fully-populated Result with distinct, recognizable field values
// so the JSON and Markdown assertions below can tell every field apart.
var sampleResult = benchmark.Result{
	Router:            "naive",
	DemandLevel:       "congested",
	MeanRealizedS:     23.0,
	P95RealizedS:      34.5,
	TotalNetworkTimeS: 5750.0,
	GiniVC:            0.5,
	MaxVC:             2.0,
	Iters:             7,
	Converged:         true,
	Gap:               0.0001,
}

// TestResultJSONTags pins the wire contract: Result marshals to the exact lowercase
// json keys the dashboard (Phase 5) and any saved artifact consume. A renamed field
// or a dropped tag is a contract break that must surface as a test failure here,
// not silently in a downstream consumer.
func TestResultJSONTags(t *testing.T) {
	raw, err := json.Marshal(sampleResult)
	if err != nil {
		t.Fatalf("json.Marshal error = %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("json.Unmarshal error = %v", err)
	}
	wantKeys := []string{
		"router", "demand_level", "mean_realized_s", "p95_realized_s",
		"total_network_time_s", "gini_vc", "max_vc", "iters", "converged", "gap",
	}
	if len(decoded) != len(wantKeys) {
		t.Errorf("JSON has %d keys, want %d: %v", len(decoded), len(wantKeys), decoded)
	}
	for _, k := range wantKeys {
		if _, ok := decoded[k]; !ok {
			t.Errorf("JSON missing key %q (got %v)", k, decoded)
		}
	}
}

// TestResultJSONRoundTrip confirms a Result survives marshal→unmarshal unchanged, so
// a serialized results artifact reloads to the same value.
func TestResultJSONRoundTrip(t *testing.T) {
	raw, err := json.Marshal(sampleResult)
	if err != nil {
		t.Fatalf("json.Marshal error = %v", err)
	}
	var back benchmark.Result
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("json.Unmarshal error = %v", err)
	}
	if back != sampleResult {
		t.Errorf("round-trip Result = %+v, want %+v", back, sampleResult)
	}
}

// TestMarkdownRendering pins the Markdown table surface: the header and separator
// have matching column counts, the row renders this Result's values, and a header +
// separator + row assemble into a valid 3-line GitHub-flavored table.
func TestMarkdownRendering(t *testing.T) {
	header := benchmark.MarkdownHeader()
	separator := benchmark.MarkdownSeparator()
	row := sampleResult.MarkdownRow()

	// Column count is the number of '|' delimiters; all three rows must match so the
	// table is well-formed.
	if hc, sc, rc := strings.Count(header, "|"), strings.Count(separator, "|"), strings.Count(row, "|"); hc != sc || sc != rc {
		t.Errorf("column counts differ: header=%d separator=%d row=%d", hc, sc, rc)
	}

	// The row must carry this Result's recognizable values (formatted), proving the
	// fields land in the row, not just placeholders.
	for _, want := range []string{"naive", "congested", "23.00", "34.50", "5750.00", "0.5000", "2.0000", "7", "true", "0.0001"} {
		if !strings.Contains(row, want) {
			t.Errorf("MarkdownRow %q missing %q", row, want)
		}
	}

	// The separator is all dashes (no stray content).
	if strings.ContainsAny(strings.ReplaceAll(separator, "-", ""), "abcdefghijklmnopqrstuvwxyz0123456789") {
		t.Errorf("MarkdownSeparator has non-dash content: %q", separator)
	}
}

// TestMarkdownRowDeterministic confirms a fixed Result always renders the same row,
// the byte-stability the docs/CLI table relies on.
func TestMarkdownRowDeterministic(t *testing.T) {
	if a, b := sampleResult.MarkdownRow(), sampleResult.MarkdownRow(); a != b {
		t.Errorf("MarkdownRow not stable: %q vs %q", a, b)
	}
}
