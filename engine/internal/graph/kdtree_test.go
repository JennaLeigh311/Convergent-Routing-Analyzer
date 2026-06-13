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
func bruteHaversineM(pointA, pointB domain.LatLon) float64 {
	const r = 6_371_000.0
	rad := math.Pi / 180.0
	lat1, lat2 := pointA.Lat*rad, pointB.Lat*rad
	dLat := (pointB.Lat - pointA.Lat) * rad
	dLon := (pointB.Lon - pointA.Lon) * rad
	value := math.Sin(dLat/2)*math.Sin(dLat/2) +
		math.Cos(lat1)*math.Cos(lat2)*math.Sin(dLon/2)*math.Sin(dLon/2)
	return 2 * r * math.Asin(math.Sqrt(value))
}

// bruteNearest returns the node id closest to q over nodes by brute force, the
// oracle the k-d tree must match exactly.
func bruteNearest(nodes []graph.Node, queryPoint domain.LatLon) (domain.NodeID, float64) {
	best := domain.NodeID(-1)
	bestD := math.MaxFloat64
	for _, node := range nodes {
		if distance := bruteHaversineM(node.Pos, queryPoint); distance < bestD {
			bestD = distance
			best = node.ID
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
func TestNearestNodeMatchesBruteForce(test *testing.T) {
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
	randIn := func(low, high float64) float64 { return low + rng.Float64()*(high-low) }

	nodes := make([]graph.Node, numNodes)
	for index := range nodes {
		nodes[index] = graph.Node{
			ID:  domain.NodeID(index),
			Pos: domain.LatLon{Lat: randIn(latLo, latHi), Lon: randIn(lonLo, lonHi)},
		}
	}
	roadGraph, err := graph.New(nodes, nil)
	if err != nil {
		test.Fatalf("graph.New: %v", err)
	}

	for count := 0; count < numQ; count++ {
		// Query a slightly larger box so some queries land outside the node hull.
		query := domain.LatLon{Lat: randIn(latLo-0.05, latHi+0.05), Lon: randIn(lonLo-0.05, lonHi+0.05)}
		gotID, found := roadGraph.NearestNode(query)
		if !found {
			test.Fatalf("NearestNode(%v) ok = false on non-empty graph", query)
		}
		wantID, wantD := bruteNearest(nodes, query)
		if gotID == wantID {
			continue
		}
		// Not bit-identical: only acceptable if it is a genuine distance tie
		// (the k-d tree found an equally-near node). Otherwise it's a bug.
		gotNode, _ := roadGraph.Node(gotID)
		gotD := bruteHaversineM(gotNode.Pos, query)
		if math.Abs(gotD-wantD) > 1e-6 {
			test.Fatalf("NearestNode(%v) = node %d (%.6f m), brute = node %d (%.6f m): not nearest",
				query, gotID, gotD, wantID, wantD)
		}
	}
}

// TestNearestNodeEmptyGraph covers the zero-node boundary: NearestNode reports
// ok=false rather than returning a bogus id.
func TestNearestNodeEmptyGraph(test *testing.T) {
	roadGraph, err := graph.New(nil, nil)
	if err != nil {
		test.Fatalf("graph.New(nil, nil): %v", err)
	}
	if nodeID, found := roadGraph.NearestNode(domain.LatLon{Lat: 41.15, Lon: -8.61}); found {
		test.Errorf("NearestNode on empty graph = (%d, true), want ok=false", nodeID)
	}
}

// TestNearestNodeSingleNode covers the one-node tree: every query resolves to
// that sole node.
func TestNearestNodeSingleNode(test *testing.T) {
	nodes := []graph.Node{{ID: 0, Pos: domain.LatLon{Lat: 41.15, Lon: -8.61}}}
	roadGraph, err := graph.New(nodes, nil)
	if err != nil {
		test.Fatalf("graph.New: %v", err)
	}
	if nodeID, found := roadGraph.NearestNode(domain.LatLon{Lat: 0, Lon: 0}); !found || nodeID != 0 {
		test.Errorf("NearestNode = (%d, ok=%v), want (0, true)", nodeID, found)
	}
}

// TestNearestNodeNaNQuery guards the NaN-query path: a NaN lat and/or lon makes
// every haversine NaN, so no candidate ever beats the initial best and the
// search ends with nothing found. NearestNode must report ok=false rather than
// index t.pts[-1] and panic. Run on a non-empty graph so only the NaN — not an
// empty tree — can produce the miss.
func TestNearestNodeNaNQuery(test1 *testing.T) {
	roadGraph := buildTestGraph(test1)
	nan := math.NaN()
	cases := []struct {
		name  string
		query domain.LatLon
	}{
		{"NaN lat", domain.LatLon{Lat: nan, Lon: -8.61}},
		{"NaN lon", domain.LatLon{Lat: 41.15, Lon: nan}},
		{"NaN both", domain.LatLon{Lat: nan, Lon: nan}},
	}
	for _, testCase := range cases {
		test1.Run(testCase.name, func(test2 *testing.T) {
			nodeID, found := roadGraph.NearestNode(testCase.query) // must not panic
			if found {
				test2.Errorf("NearestNode(%v) = (%d, true), want ok=false for a NaN query", testCase.query, nodeID)
			}
		})
	}
}

// TestNearestNodeHighLatitudeAboveBand is the integration counterpart to the
// unit guard TestKDTreeLonBoundAdmissibleQueryAboveBand: it proves search()
// threads the per-query lonCos factor correctly through the recursion. Nodes are
// spread across a wide longitude range at ~80° latitude and queries sit at
// ~80.2°, ABOVE the tree's maxAbsLat, where a maxAbsLat-only longitude bound
// would over-estimate and could mis-prune. NearestNode must still equal the
// brute-force nearest for every query.
func TestNearestNodeHighLatitudeAboveBand(test *testing.T) {
	const seed = 0x5eed_80
	rng := rand.New(rand.NewSource(seed))

	// A handful of nodes spread across a wide longitude band at high latitude.
	lons := []float64{-150, -90, -30, 0, 30, 90, 150}
	nodes := make([]graph.Node, len(lons))
	for index, lon := range lons {
		nodes[index] = graph.Node{
			ID:  domain.NodeID(index),
			Pos: domain.LatLon{Lat: 80.0, Lon: lon},
		}
	}
	roadGraph, err := graph.New(nodes, nil)
	if err != nil {
		test.Fatalf("graph.New: %v", err)
	}

	for count := 0; count < 500; count++ {
		// Query latitude sits above every node's latitude (the failure regime),
		// across the full longitude span.
		query := domain.LatLon{Lat: 80.2, Lon: -180 + rng.Float64()*360}
		gotID, found := roadGraph.NearestNode(query)
		if !found {
			test.Fatalf("NearestNode(%v) ok = false on non-empty graph", query)
		}
		wantID, wantD := bruteNearest(nodes, query)
		if gotID == wantID {
			continue
		}
		// Identity mismatch is only acceptable as a genuine distance tie.
		gotNode, _ := roadGraph.Node(gotID)
		gotD := bruteHaversineM(gotNode.Pos, query)
		if math.Abs(gotD-wantD) > 1e-6 {
			test.Fatalf("NearestNode(%v) = node %d (%.6f m), brute = node %d (%.6f m): not nearest above band",
				query, gotID, gotD, wantID, wantD)
		}
	}
}

// TestNearestNodeDuplicateCoordinates exercises the tie branch and the build's
// equal-split-key handling that the seeded-random test cannot reach (it never
// produces exact coordinate ties). Several nodes share one Pos (different IDs)
// and one node sits a hair closer to the query. NearestNode must return a node
// at the true-minimum distance. When the query is equidistant from the tied
// cluster, ANY of those tied IDs is acceptable, so the assertion is on distance,
// not a specific ID (sort.Slice is unstable, so the surviving tied ID is not
// determined).
func TestNearestNodeDuplicateCoordinates(test1 *testing.T) {
	dup := domain.LatLon{Lat: 41.15, Lon: -8.61}
	nodes := []graph.Node{
		{ID: 0, Pos: dup},
		{ID: 1, Pos: dup},
		{ID: 2, Pos: dup},
		{ID: 3, Pos: dup},
		// Node 4 sits a hair closer to the query than the duplicated cluster.
		{ID: 4, Pos: domain.LatLon{Lat: 41.1501, Lon: -8.61}},
	}
	roadGraph, err := graph.New(nodes, nil)
	if err != nil {
		test1.Fatalf("graph.New: %v", err)
	}

	test1.Run("query nearest the lone closer node", func(test2 *testing.T) {
		// Query just past node 4: node 4 is unambiguously the unique nearest.
		query := domain.LatLon{Lat: 41.1502, Lon: -8.61}
		gotID, found1 := roadGraph.NearestNode(query)
		if !found1 {
			test2.Fatalf("NearestNode(%v) ok = false on non-empty graph", query)
		}
		if gotID != 4 {
			test2.Errorf("NearestNode(%v) = node %d, want node 4 (the lone closer node)", query, gotID)
		}
	})

	test1.Run("query equidistant from the tied cluster", func(test3 *testing.T) {
		// Query exactly at the duplicated coordinate: all four tied nodes are at
		// distance 0, node 4 is farther. Assert on the returned distance (any tied
		// id is acceptable), not on a specific id.
		query := dup
		gotID, found2 := roadGraph.NearestNode(query)
		if !found2 {
			test3.Fatalf("NearestNode(%v) ok = false on non-empty graph", query)
		}
		gotNode, _ := roadGraph.Node(gotID)
		gotD := bruteHaversineM(gotNode.Pos, query)
		_, wantD := bruteNearest(nodes, query)
		if math.Abs(gotD-wantD) > 1e-6 {
			test3.Errorf("NearestNode(%v) = node %d at %.6f m, want a node at the minimum %.6f m",
				query, gotID, gotD, wantD)
		}
	})
}
