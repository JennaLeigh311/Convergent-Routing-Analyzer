package benchmark_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"testing"

	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/benchmark"
	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/cost"
	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/graph"
	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/routing"
)

// pigouNetworkPath is the Pigou price-of-anarchy fixture (#89): two parallel
// one-way edges, a cheap low-capacity link and a near-constant high-capacity link,
// node 0 → node 1. See engine/testdata/README.md and toy_network_pigou.geojson.
const pigouNetworkPath = "../../testdata/toy_network_pigou.geojson"

// loadPigou loads the Pigou fixture under the same loose NYC-ish bounds the routing
// tests use, asserting it loads cleanly (the #87 length_m ≥ chord guard and every
// §2 invariant must pass).
func loadPigou(t *testing.T) *graph.AdjacencyGraph {
	t.Helper()
	g, _, err := graph.LoadEdgeAttributesGeoJSONFile(pigouNetworkPath, graph.WithExpectedBounds(-74, -73, 40, 41))
	if err != nil {
		t.Fatalf("toy_network_pigou.geojson must load with zero error, got: %v", err)
	}
	return g
}

// pigouDemand builds n node0→node1 requests at the given per-request weight (vph),
// the congested demand level the Pigou structure needs to exhibit a price of
// anarchy: every selfish request piles onto the cheap link and overloads it.
func pigouDemand(t *testing.T, g *graph.AdjacencyGraph, n int, weight float64) []routing.RouteRequest {
	t.Helper()
	n0, _ := g.Node(0)
	n1, _ := g.Node(1)
	reqs := make([]routing.RouteRequest, n)
	for i := range reqs {
		reqs[i] = routing.RouteRequest{ID: fmt.Sprintf("r%d", i), From: n0.Pos, To: n1.Pos, Weight: weight}
	}
	return reqs
}

// TestPriceOfAnarchyHandComputed pins the PoA ratio function on hand numbers,
// independent of any router: PoA = selfishTotal / referenceTotal. 220320 / 101760
// ≈ 2.165 (the Pigou totals at D = 1800 vph), and the degenerate guards (a
// non-positive reference or selfish total ⇒ PoA = 1) are exercised directly.
func TestPriceOfAnarchyHandComputed(t *testing.T) {
	if got := benchmark.PriceOfAnarchy(220320, 101760); math.Abs(got-2.1651) > 1e-4 {
		t.Errorf("PriceOfAnarchy(220320,101760) = %v, want ≈ 2.1651", got)
	}
	if got := benchmark.PriceOfAnarchy(5000, 0); got != 1 {
		t.Errorf("PriceOfAnarchy with zero reference = %v, want 1", got)
	}
	if got := benchmark.PriceOfAnarchy(0, 5000); got != 1 {
		t.Errorf("PriceOfAnarchy with zero selfish = %v, want 1", got)
	}
}

// TestPriceOfAnarchyOnPigouFixture is the issue-#89 acceptance criterion: on the
// congested Pigou demand level the SELFISH (naive) router's realized total network
// time strictly exceeds the systemoptimal reference's, so PoA(naive) > 1 — and the
// SO-targeting router's realized total is ≤ naive's. This is the strict PoA the toy
// network (PoA ≈ 1) cannot produce; the Pigou fixture is built precisely to.
//
// Hand intuition (D = 1800 vph): naive piles all 1800 onto the cheap 900-vph link
// (v/c = 2 ⇒ realized total ≈ 220320); systemoptimal splits it ~half onto the
// high-capacity link (realized total ≈ 101760), so PoA ≈ 2.17. The asserts use
// only the structural inequalities (> 1, ≤) so they are robust to the SO router's
// partial-convergence exact split, not pinned to a brittle decimal.
func TestPriceOfAnarchyOnPigouFixture(t *testing.T) {
	g := loadPigou(t)
	bpr := cost.DefaultBPR()
	reqs := pigouDemand(t, g, 9, 200) // 9 × 200 = 1800 vph, the congested level

	naive, err := routing.NewNaiveRouter(g).AssignResult(context.Background(), reqs)
	if err != nil {
		t.Fatalf("naive AssignResult error = %v", err)
	}
	so, err := routing.NewSystemOptimalRouter(g, bpr).AssignResult(context.Background(), reqs)
	if err != nil {
		t.Fatalf("systemoptimal AssignResult error = %v", err)
	}

	naiveResult := benchmark.Evaluate(g, bpr, "naive", "congested", naive)
	soResult := benchmark.Evaluate(g, bpr, "systemoptimal", "congested", so)

	poa := benchmark.PriceOfAnarchy(naiveResult.TotalNetworkTimeS, soResult.TotalNetworkTimeS)
	t.Logf("Pigou congested: naive total = %.1f, systemoptimal total = %.1f, PoA = %.4f",
		naiveResult.TotalNetworkTimeS, soResult.TotalNetworkTimeS, poa)

	if !(poa > 1) {
		t.Errorf("PoA(naive) = %v, want > 1 on the congested Pigou fixture", poa)
	}
	if !(soResult.TotalNetworkTimeS <= naiveResult.TotalNetworkTimeS+1e-6) {
		t.Errorf("systemoptimal realized total %v > naive realized total %v — SO must not be worse than the selfish baseline",
			soResult.TotalNetworkTimeS, naiveResult.TotalNetworkTimeS)
	}
	// The naive baseline overloads the cheap link past capacity, so its MaxVC must
	// exceed 1 — a direct read that the selfish assignment genuinely congests an edge.
	if !(naiveResult.MaxVC > 1) {
		t.Errorf("naive MaxVC = %v, want > 1 (the selfish baseline should overload the cheap link)", naiveResult.MaxVC)
	}
}

// TestEvaluateDeterministic is the §R5 determinism criterion for the evaluator: over
// a serialized OD set and a fixed router (systemoptimal, whose split is the
// non-trivial case), two independent Evaluate runs produce an == Result AND
// byte-identical JSON. The OD set is round-tripped through Write/ReadODSet (the real
// on-disk form) so this also pins that replaying a saved OD set reproduces the
// metrics exactly.
func TestEvaluateDeterministic(t *testing.T) {
	g := loadPigou(t)
	bpr := cost.DefaultBPR()
	original := pigouDemand(t, g, 9, 200)

	var buf bytes.Buffer
	if err := routing.WriteODSet(&buf, original); err != nil {
		t.Fatalf("WriteODSet error = %v", err)
	}
	serialized := buf.Bytes()

	runOnce := func() benchmark.Result {
		reqs, err := routing.ReadODSet(bytes.NewReader(serialized))
		if err != nil {
			t.Fatalf("ReadODSet error = %v", err)
		}
		res, err := routing.NewSystemOptimalRouter(g, bpr).AssignResult(context.Background(), reqs)
		if err != nil {
			t.Fatalf("AssignResult error = %v", err)
		}
		return benchmark.Evaluate(g, bpr, "systemoptimal", "congested", res)
	}

	first := runOnce()
	second := runOnce()

	if !reflect.DeepEqual(first, second) {
		t.Errorf("Result not identical across runs:\n%+v\nvs\n%+v", first, second)
	}

	firstJSON, err := json.Marshal(first)
	if err != nil {
		t.Fatalf("json.Marshal(first) error = %v", err)
	}
	secondJSON, err := json.Marshal(second)
	if err != nil {
		t.Fatalf("json.Marshal(second) error = %v", err)
	}
	if !bytes.Equal(firstJSON, secondJSON) {
		t.Errorf("Result JSON not byte-identical across runs:\n%s\nvs\n%s", firstJSON, secondJSON)
	}
}
