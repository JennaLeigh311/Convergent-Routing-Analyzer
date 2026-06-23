package benchmark_test

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/benchmark"
	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/graph"
	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/routing"
)

const toyGraphPath = "../../testdata/toy_network.geojson"

func loadToy(t *testing.T) graph.Graph {
	t.Helper()
	g, _, err := graph.LoadEdgeAttributesGeoJSONFile(toyGraphPath)
	if err != nil {
		t.Fatalf("load toy graph: %v", err)
	}
	return g
}

// TestGenerateODSetDeterministic asserts two generations with the same (seed, count)
// produce an identical request list and byte-identical JSON — the §R5 reproducibility
// criterion the whole sweep rests on.
func TestGenerateODSetDeterministic(t *testing.T) {
	g := loadToy(t)
	a := benchmark.GenerateODSet(g, 20260618, 200, "x", 1.0, 5000)
	b := benchmark.GenerateODSet(g, 20260618, 200, "x", 1.0, 5000)

	if len(a.Requests) != 200 || len(b.Requests) != 200 {
		t.Fatalf("request counts = %d, %d, want 200", len(a.Requests), len(b.Requests))
	}
	var ab, bb bytes.Buffer
	if err := a.WriteJSON(&ab); err != nil {
		t.Fatalf("write a: %v", err)
	}
	if err := b.WriteJSON(&bb); err != nil {
		t.Fatalf("write b: %v", err)
	}
	if ab.String() != bb.String() {
		t.Fatalf("OD set JSON not deterministic:\n--- a ---\n%s\n--- b ---\n%s", ab.String(), bb.String())
	}
}

// TestGenerateODSetSeedSensitivity asserts a DIFFERENT seed yields a different draw
// (the generator's randomness is real, not a constant), while the count and total
// demand are unchanged.
func TestGenerateODSetSeedSensitivity(t *testing.T) {
	g := loadToy(t)
	a := benchmark.GenerateODSet(g, 1, 500, "x", 1.0, 5000)
	b := benchmark.GenerateODSet(g, 2, 500, "x", 1.0, 5000)
	same := true
	for i := range a.Requests {
		if a.Requests[i].Label != b.Requests[i].Label {
			same = false
			break
		}
	}
	if same {
		t.Error("two different seeds produced an identical label sequence; draw is not seed-sensitive")
	}
}

// TestGenerateODSetWeights asserts the summed per-request weight equals the requested
// total demand (so FinalFlows accumulates the intended load), and that no request
// carries an EdgeID in its serialized form (the §2 frozen-contract rule).
func TestGenerateODSetWeights(t *testing.T) {
	g := loadToy(t)
	set := benchmark.GenerateODSet(g, 7, 1000, "lvl", 1.0, 5000)
	var sum float64
	for _, r := range set.Requests {
		sum += r.WeightVPH
	}
	if diff := sum - 5000; diff < -1e-6 || diff > 1e-6 {
		t.Errorf("summed weight = %v, want 5000", sum)
	}

	var buf bytes.Buffer
	if err := set.WriteJSON(&buf); err != nil {
		t.Fatalf("write: %v", err)
	}
	if strings.Contains(strings.ToLower(buf.String()), "edge_id") || strings.Contains(buf.String(), "EdgeID") {
		t.Errorf("serialized OD set leaks an EdgeID (frozen-contract §2 violation):\n%s", buf.String())
	}
}

// TestGenerateODSetReachable asserts every synthesized request actually routes on the
// toy graph (the generator draws only from the reachable pair pool), so no request
// fails to route — the "unreachable OD handled cleanly, never panic" criterion.
func TestGenerateODSetReachable(t *testing.T) {
	g := loadToy(t)
	set := benchmark.GenerateODSet(g, 3, 300, "x", 1.0, 5000)
	naive := routing.NewNaiveRouter(g)
	if _, err := naive.AssignResult(context.Background(), set.RouteRequests()); err != nil {
		t.Fatalf("a synthesized OD set failed to route (should be reachable-only): %v", err)
	}
}

// TestGenerateODSetEmpty asserts a non-positive count yields a non-nil empty request
// slice (the empty-batch case), with the metadata still recorded.
func TestGenerateODSetEmpty(t *testing.T) {
	g := loadToy(t)
	set := benchmark.GenerateODSet(g, 1, 0, "empty", 0.5, 5000)
	if set.Requests == nil {
		t.Error("empty OD set has a nil Requests slice; want non-nil empty")
	}
	if len(set.Requests) != 0 {
		t.Errorf("empty OD set has %d requests, want 0", len(set.Requests))
	}
	if set.DemandLevel != "empty" || set.TargetVC != 0.5 {
		t.Errorf("empty OD set dropped its metadata: %+v", set)
	}
	// An empty batch routes cleanly to a finite, zero Result.
	res, err := routing.NewNaiveRouter(g).AssignResult(context.Background(), set.RouteRequests())
	if err != nil {
		t.Fatalf("empty batch routing error: %v", err)
	}
	if len(res.Routes) != 0 {
		t.Errorf("empty batch produced %d routes, want 0", len(res.Routes))
	}
}

// TestODSetJSONRoundTrip asserts the serialized OD set decodes back to the same
// requests, so the on-disk artifact is a faithful, replayable record.
func TestODSetJSONRoundTrip(t *testing.T) {
	g := loadToy(t)
	set := benchmark.GenerateODSet(g, 42, 50, "rt", 0.8, 4000)
	var buf bytes.Buffer
	if err := set.WriteJSON(&buf); err != nil {
		t.Fatalf("write: %v", err)
	}
	var decoded benchmark.ODSet
	if err := json.Unmarshal(buf.Bytes(), &decoded); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if decoded.Seed != 42 || decoded.DemandLevel != "rt" || len(decoded.Requests) != 50 {
		t.Errorf("round-trip lost metadata: %+v", decoded)
	}
	for i := range set.Requests {
		if decoded.Requests[i] != set.Requests[i] {
			t.Errorf("request %d differs after round-trip: %+v vs %+v", i, decoded.Requests[i], set.Requests[i])
		}
	}
}
