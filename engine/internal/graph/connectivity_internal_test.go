package graph

// connectivity_internal_test.go is a WHITE-BOX test (package graph) so it can
// call warnIfNotWeaklyConnected directly with synthetic, hand-built nodes and
// edges, exercising the diagnostic's branches that the black-box, fixture-driven
// tests in connectivity_test.go cannot reach: the <2-node early return, the
// cap-at-10 sample, the reported fraction, and the deterministic size-tie
// tie-break. The helper reads only Edge.From/To and len(nodes), so the other
// Node/Edge fields are left zero. Assertions are on the structured slog text the
// warning emits (slog's text handler quotes the []int render because it contains
// a space).

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/domain"
)

// captureWarnInternal installs a fresh slog default writing Warn-level text to
// the returned buffer, restoring the previous default on cleanup. It is the
// package-graph counterpart of captureWarn in connectivity_test.go (which lives
// in package graph_test and is not visible here).
func captureWarnInternal(test *testing.T) *bytes.Buffer {
	test.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	test.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

// densNodes builds n nodes with dense ids 0..n-1 (positions left zero — the
// helper never reads them).
func densNodes(n int) []Node {
	nodes := make([]Node, n)
	for index := range nodes {
		nodes[index] = Node{ID: domain.NodeID(index)}
	}
	return nodes
}

// edge builds an Edge whose only meaningful fields for the diagnostic are
// From/To.
func edge(from, to int) Edge {
	return Edge{From: domain.NodeID(from), To: domain.NodeID(to)}
}

// TestWarnLessThanTwoNodesSilent covers the <2-node early return: zero and one
// node are each trivially a single weak component and must emit nothing.
func TestWarnLessThanTwoNodesSilent(test *testing.T) {
	for _, count := range []int{0, 1} {
		buf := captureWarnInternal(test)
		warnIfNotWeaklyConnected(densNodes(count), nil)
		if logged := buf.String(); logged != "" {
			test.Errorf("a %d-node graph must emit no connectivity warning, got: %q", count, logged)
		}
	}
}

// TestWarnCapsSampleAtTen builds a 2-node main component plus 12 isolated
// singletons (14 nodes total): the full unreachable COUNT is 12 (>10), so
// unreachable_nodes must report 12 while sample_unreachable_nodes lists only the
// first 10 ascending ids [2..11]. The fraction is 2/14.
func TestWarnCapsSampleAtTen(test *testing.T) {
	buf := captureWarnInternal(test)
	// Main component {0,1}; nodes 2..13 are isolated singletons.
	warnIfNotWeaklyConnected(densNodes(14), []Edge{edge(0, 1)})

	logged := buf.String()
	if !strings.Contains(logged, "unreachable_nodes=12") {
		test.Errorf("expected full unreachable count unreachable_nodes=12, got: %q", logged)
	}
	// The sample is capped at 10: exactly the first ten unreachable ids ascending.
	if !strings.Contains(logged, `sample_unreachable_nodes="[2 3 4 5 6 7 8 9 10 11]"`) {
		test.Errorf(`expected capped 10-element sample [2 3 4 5 6 7 8 9 10 11], got: %q`, logged)
	}
	if !strings.Contains(logged, "components=13") {
		test.Errorf("expected components=13 (1 pair + 12 singletons), got: %q", logged)
	}
	if !strings.Contains(logged, "largest_component_nodes=2") {
		test.Errorf("expected largest_component_nodes=2, got: %q", logged)
	}
	if !strings.Contains(logged, "largest_component_fraction=0.14285714285714285") {
		test.Errorf("expected fraction 2/14 = 0.14285714285714285, got: %q", logged)
	}
}

// TestWarnReportsFraction asserts the exact largest_component_fraction for a
// known split: main {0,1,2} (size 3) plus island {3,4} (size 2), total 5, so the
// fraction is 3/5 = 0.6.
func TestWarnReportsFraction(test *testing.T) {
	buf := captureWarnInternal(test)
	warnIfNotWeaklyConnected(densNodes(5), []Edge{
		edge(0, 1), edge(1, 2), // main component {0,1,2}
		edge(3, 4), // island {3,4}
	})

	logged := buf.String()
	if !strings.Contains(logged, "largest_component_fraction=0.6") {
		test.Errorf("expected largest_component_fraction=0.6 for a 3/5 split, got: %q", logged)
	}
	if !strings.Contains(logged, "largest_component_nodes=3") {
		test.Errorf("expected largest_component_nodes=3, got: %q", logged)
	}
	if !strings.Contains(logged, `sample_unreachable_nodes="[3 4]"`) {
		test.Errorf(`expected sample_unreachable_nodes="[3 4]", got: %q`, logged)
	}
}

// TestWarnSizeTieBreaksOnLowestNodeID builds two EQUAL-largest components — {0,1}
// and {2,3}, both size 2 — so the deterministic tie-break (largest = the
// component with the lowest member id) must pick {0,1}, leaving {2,3} as the
// unreachable set. This proves "largest" is reproducible regardless of map order.
func TestWarnSizeTieBreaksOnLowestNodeID(test *testing.T) {
	buf := captureWarnInternal(test)
	warnIfNotWeaklyConnected(densNodes(4), []Edge{
		edge(0, 1), // component {0,1}
		edge(2, 3), // component {2,3}
	})

	logged := buf.String()
	if !strings.Contains(logged, "components=2") {
		test.Errorf("expected components=2, got: %q", logged)
	}
	if !strings.Contains(logged, "unreachable_nodes=2") {
		test.Errorf("expected unreachable_nodes=2, got: %q", logged)
	}
	// The lower-id component {0,1} wins the size tie; {2,3} is the unreachable set.
	if !strings.Contains(logged, `sample_unreachable_nodes="[2 3]"`) {
		test.Errorf(`expected the higher-id component {2,3} to be unreachable (lowest-id tie-break), got: %q`, logged)
	}
}
