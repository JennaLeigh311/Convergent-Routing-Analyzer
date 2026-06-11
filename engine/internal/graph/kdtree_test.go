package graph_test

import (
	"math"
	"math/rand"
	"testing"

	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/domain"
	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/graph"
)

// bruteHaversineM re-derives the great-circle distance in meters independently
// of the package-internal haversine, so the brute-force oracle does not share
// code with the implementation under test.
func bruteHaversineM(a, b domain.LatLon) float64 {
	const r = 6_371_000.0
	rad := math.Pi / 180.0
	lat1, lat2 := a.Lat*rad, b.Lat*rad
	dLat := (b.Lat - a.Lat) * rad
	dLon := (b.Lon - a.Lon) * rad
	h := math.Sin(dLat/2)*math.Sin(dLat/2) +
		math.Cos(lat1)*math.Cos(lat2)*math.Sin(dLon/2)*math.Sin(dLon/2)
	return 2 * r * math.Asin(math.Sqrt(h))
}

// bruteNearest returns the node id closest to q over nodes by brute force, the
// oracle the k-d tree must match exactly.
func bruteNearest(nodes []graph.Node, q domain.LatLon) (domain.NodeID, float64) {
	best := domain.NodeID(-1)
	bestD := math.MaxFloat64
	for _, n := range nodes {
		if d := bruteHaversineM(n.Pos, q); d < bestD {
			bestD = d
			best = n.ID
		}
	}
	return best, bestD
}

// TestNearestNodeMatchesBruteForce is the core correctness acceptance test:
// over a seeded random node set and seeded random queries, the k-d tree-backed
// NearestNode must return the same node as brute-force-haversine-nearest for
// every query. A wrong distance would silently pick the wrong node, so the
// comparison is exact on identity (with a distance-tie tolerance only for the
// vanishingly unlikely exact-tie case).
func TestNearestNodeMatchesBruteForce(t *testing.T) {
	const (
		seed     = 0x5eed_24
		numNodes = 2000
		numQ     = 5000
	)
	rng := rand.New(rand.NewSource(seed))

	// Porto-area bounding box (the repo's working region) plus a little margin,
	// so latitudes are well away from the poles where longitude scaling matters.
	const (
		latLo, latHi = 41.00, 41.30
		lonLo, lonHi = -8.75, -8.45
	)
	randIn := func(lo, hi float64) float64 { return lo + rng.Float64()*(hi-lo) }

	nodes := make([]graph.Node, numNodes)
	for i := range nodes {
		nodes[i] = graph.Node{
			ID:  domain.NodeID(i),
			Pos: domain.LatLon{Lat: randIn(latLo, latHi), Lon: randIn(lonLo, lonHi)},
		}
	}
	g, err := graph.New(nodes, nil)
	if err != nil {
		t.Fatalf("graph.New: %v", err)
	}

	for q := 0; q < numQ; q++ {
		// Query a slightly larger box so some queries land outside the node hull.
		query := domain.LatLon{Lat: randIn(latLo-0.05, latHi+0.05), Lon: randIn(lonLo-0.05, lonHi+0.05)}
		gotID, ok := g.NearestNode(query)
		if !ok {
			t.Fatalf("NearestNode(%v) ok = false on non-empty graph", query)
		}
		wantID, wantD := bruteNearest(nodes, query)
		if gotID == wantID {
			continue
		}
		// Not bit-identical: only acceptable if it is a genuine distance tie
		// (the k-d tree found an equally-near node). Otherwise it's a bug.
		gotNode, _ := g.Node(gotID)
		gotD := bruteHaversineM(gotNode.Pos, query)
		if math.Abs(gotD-wantD) > 1e-6 {
			t.Fatalf("NearestNode(%v) = node %d (%.6f m), brute = node %d (%.6f m): not nearest",
				query, gotID, gotD, wantID, wantD)
		}
	}
}

// TestNearestNodeEmptyGraph covers the zero-node boundary: NearestNode reports
// ok=false rather than returning a bogus id.
func TestNearestNodeEmptyGraph(t *testing.T) {
	g, err := graph.New(nil, nil)
	if err != nil {
		t.Fatalf("graph.New(nil, nil): %v", err)
	}
	if id, ok := g.NearestNode(domain.LatLon{Lat: 41.15, Lon: -8.61}); ok {
		t.Errorf("NearestNode on empty graph = (%d, true), want ok=false", id)
	}
}

// TestNearestNodeSingleNode covers the one-node tree: every query resolves to
// that sole node.
func TestNearestNodeSingleNode(t *testing.T) {
	nodes := []graph.Node{{ID: 0, Pos: domain.LatLon{Lat: 41.15, Lon: -8.61}}}
	g, err := graph.New(nodes, nil)
	if err != nil {
		t.Fatalf("graph.New: %v", err)
	}
	if id, ok := g.NearestNode(domain.LatLon{Lat: 0, Lon: 0}); !ok || id != 0 {
		t.Errorf("NearestNode = (%d, ok=%v), want (0, true)", id, ok)
	}
}
