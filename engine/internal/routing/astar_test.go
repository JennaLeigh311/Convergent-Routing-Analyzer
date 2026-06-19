package routing

// Internal (package routing) tests for the A* single-pair query path. They live
// here, not in the external routing_test package, because the settle-count
// acceptance test calls the unexported shortestPath core directly (with and
// without the heuristic) so the two algorithms it compares are provably the SAME
// loop differing only by the heuristic — the whole point of the pqItem.priority
// seam. A public Route-level parity test (TestAStarRouteSameCostAsNaive) is kept
// here too so the parity is also exercised end-to-end through the exported surface.

import (
	"context"
	"math"
	"testing"

	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/domain"
	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/graph"
)

// The shared toy fixture (loaded by loadToyGraphInternal, defined in
// scratch_internal_test.go) has curated length_m values that, as of issue #81, are
// >= the great-circle chord between the edge endpoints it declares (the geometry was
// scaled toward the centroid so the §2 length_m >= chord invariant holds; the loader
// now enforces it). The chord-based heuristic divisor (maxFreeFlowSpeedMS) is kept as
// defense-in-depth: it stays admissible BY CONSTRUCTION even if some future graph
// violated that invariant, since it is derived from endpoint geometry, not length_m.
// The admissibility and parity tests below run on BOTH that real fixture and the
// synthetic buildGeoConsistentGraph lattice, whose free-flow time is derived FROM the
// node geometry so length_m == chord by construction.

// buildGeoConsistentGraph builds a sideLen x sideLen 4-neighbor lattice whose
// per-edge free-flow time is derived from the SAME node geometry the A* heuristic
// reads, so it models a real OSM export's invariant (polyline length ≥ straight-line
// chord) rather than the toy fixture's hand-written, geometry-inconsistent lengths.
//
// Each grid cell is a fixed lat/lon step, so neighboring nodes are a real
// great-circle distance apart. An edge's LengthM is set to exactly that chord and
// its FreeFlowS to chord/speed for a per-edge speed drawn from a small set of class
// speeds (so the network has a genuine max speed and slower edges). Because every
// edge's speed ≤ the network max and LengthM == the straight-line chord, the
// heuristic distance/maxSpeed is a guaranteed lower bound on any real path's
// free-flow time — admissible (and consistent) by construction.
func buildGeoConsistentGraph(sideLen int) *graph.AdjacencyGraph {
	const (
		latStep = 0.002
		lonStep = 0.002
		baseLat = 40.73
		baseLon = -73.99
	)
	// A few class speeds in m/s; the max (the last) is the network's fastest edge.
	speedsMS := []float64{8.33, 13.89, 27.78} // ~30, 50, 100 km/h

	nodeCount := sideLen * sideLen
	pos := make([]domain.LatLon, nodeCount)
	nodes := make([]graph.Node, nodeCount)
	for row := 0; row < sideLen; row++ {
		for col := 0; col < sideLen; col++ {
			id := domain.NodeID(row*sideLen + col)
			pos[id] = domain.LatLon{Lat: baseLat + float64(row)*latStep, Lon: baseLon + float64(col)*lonStep}
			nodes[id] = graph.Node{ID: id, Pos: pos[id]}
		}
	}

	var edges []graph.Edge
	addEdge := func(from, to domain.NodeID) {
		chord := graph.GreatCircleM(pos[from], pos[to])
		// Vary speed deterministically by edge index so the network has a real max
		// and slower edges; every speed ≤ speedsMS[last], the network max.
		speed := speedsMS[len(edges)%len(speedsMS)]
		edges = append(edges, graph.Edge{
			ID:          domain.EdgeID(len(edges)),
			Segment:     domain.SegmentID("grid"),
			From:        from,
			To:          to,
			LengthM:     chord,
			FreeFlowS:   chord / speed,
			CapacityVPH: 1800,
		})
	}
	for row := 0; row < sideLen; row++ {
		for col := 0; col < sideLen; col++ {
			from := domain.NodeID(row*sideLen + col)
			if row+1 < sideLen {
				addEdge(from, domain.NodeID((row+1)*sideLen+col))
			}
			if row-1 >= 0 {
				addEdge(from, domain.NodeID((row-1)*sideLen+col))
			}
			if col+1 < sideLen {
				addEdge(from, domain.NodeID(row*sideLen+col+1))
			}
			if col-1 >= 0 {
				addEdge(from, domain.NodeID(row*sideLen+col-1))
			}
		}
	}

	roadGraph, err := graph.New(nodes, edges)
	if err != nil {
		panic("buildGeoConsistentGraph: graph.New: " + err.Error())
	}
	return roadGraph
}

// settleCountingGraph wraps a graph.Graph and counts OutEdgeIDs calls. The
// shortest-path core calls OutEdgeIDs exactly once per SETTLED, non-stale node
// (the pop site, after the staleness skip and the dst break), so the call count is
// the number of nodes whose out-edges the search expanded — the settlement metric
// the acceptance criterion measures. Both Dijkstra and A* run the same core, so
// the same wrapper yields a comparable count for each.
type settleCountingGraph struct {
	graph.Graph
	settles int
}

func (g *settleCountingGraph) OutEdgeIDs(node domain.NodeID) []domain.EdgeID {
	g.settles++
	return g.Graph.OutEdgeIDs(node)
}

// TestAStarMatchesDijkstraEveryODPair is the optimality acceptance criterion: for
// EVERY ordered (src, dst) node pair on the toy network, the A* core (shortestPath
// with the admissible heuristic) returns a path of identical COST to the Dijkstra
// core (shortestPath with a nil heuristic). Cost equality — not edge-by-edge path
// equality — is the contract: ties can be broken differently, but an admissible
// heuristic must never change the optimal cost.
func TestAStarMatchesDijkstraEveryODPair(test *testing.T) {
	roadGraph := buildGeoConsistentGraph(6)
	router := NewAStarRouter(roadGraph)
	n := roadGraph.NodeCount()

	for src := domain.NodeID(0); int(src) < n; src++ {
		for dst := domain.NodeID(0); int(dst) < n; dst++ {
			dijkstraPath, dijkstraCost, dijkstraOK := shortestPath(roadGraph, src, dst, freeFlowWeight, nil, nil)
			astarPath, astarCost, astarOK := shortestPath(roadGraph, src, dst, freeFlowWeight, router.heuristic(dst), nil)

			if dijkstraOK != astarOK {
				test.Errorf("%d->%d: reachability mismatch: dijkstra ok=%v, astar ok=%v", src, dst, dijkstraOK, astarOK)
				continue
			}
			if !dijkstraOK {
				continue // both agree dst is unreachable; nothing to compare
			}
			if math.Abs(dijkstraCost-astarCost) > 1e-9 {
				test.Errorf("%d->%d: cost mismatch: dijkstra=%.9f, astar=%.9f (paths %v vs %v)",
					src, dst, dijkstraCost, astarCost, dijkstraPath, astarPath)
			}
		}
	}
}

// TestAStarHeuristicAdmissible is the admissibility acceptance criterion: for every
// sampled node n and a fixed destination, h(n) must not exceed the TRUE shortest
// remaining free-flow time from n to dst (computed by a full Dijkstra from n). An
// inadmissible heuristic — one that ever over-estimates — can make A* return a
// non-optimal path, so this guards the property the optimality proof rests on.
func TestAStarHeuristicAdmissible(test *testing.T) {
	roadGraph := buildGeoConsistentGraph(6)
	router := NewAStarRouter(roadGraph)
	n := roadGraph.NodeCount()

	for dst := domain.NodeID(0); int(dst) < n; dst++ {
		h := router.heuristic(dst)
		for node := domain.NodeID(0); int(node) < n; node++ {
			_, trueRemaining, reachable := shortestPath(roadGraph, node, dst, freeFlowWeight, nil, nil)
			if !reachable {
				continue // h need only bound reachable destinations
			}
			if estimate := h(node); estimate > trueRemaining+1e-9 {
				test.Errorf("heuristic inadmissible: h(%d->%d)=%.6f s exceeds true remaining %.6f s",
					node, dst, estimate, trueRemaining)
			}
		}
	}
}

// TestAStarSettlesNoMoreThanDijkstra is the pruning acceptance criterion: on at
// least one OD pair A* must settle strictly fewer nodes than Dijkstra, and on NO
// pair may it settle more (an admissible, consistent heuristic can only prune the
// frontier, never enlarge it). It counts settlements by wrapping the graph in a
// settleCountingGraph and running the SAME core both ways.
func TestAStarSettlesNoMoreThanDijkstra(test *testing.T) {
	roadGraph := buildGeoConsistentGraph(6)
	router := NewAStarRouter(roadGraph)
	n := roadGraph.NodeCount()

	prunedSomewhere := false
	for src := domain.NodeID(0); int(src) < n; src++ {
		for dst := domain.NodeID(0); int(dst) < n; dst++ {
			dijkstraGraph := &settleCountingGraph{Graph: roadGraph}
			_, _, dijkstraOK := shortestPath(dijkstraGraph, src, dst, freeFlowWeight, nil, nil)

			astarGraph := &settleCountingGraph{Graph: roadGraph}
			// The heuristic must close over the SAME graph the search walks so the
			// node lookups are consistent; heuristic itself reads Node, not OutEdgeIDs,
			// so it does not perturb the settle count.
			_, _, astarOK := shortestPath(astarGraph, src, dst, freeFlowWeight, router.heuristic(dst), nil)

			if !dijkstraOK || !astarOK {
				continue // only compare settlement counts on pairs both searches complete
			}
			if astarGraph.settles > dijkstraGraph.settles {
				test.Errorf("%d->%d: A* settled %d nodes, MORE than Dijkstra's %d (admissible heuristic must not enlarge the frontier)",
					src, dst, astarGraph.settles, dijkstraGraph.settles)
			}
			if astarGraph.settles < dijkstraGraph.settles {
				prunedSomewhere = true
				test.Logf("%d->%d: A* settled %d nodes vs Dijkstra's %d (pruned %d)",
					src, dst, astarGraph.settles, dijkstraGraph.settles, dijkstraGraph.settles-astarGraph.settles)
			}
		}
	}
	if !prunedSomewhere {
		test.Error("A* settled the same node count as Dijkstra on every OD pair; expected strictly fewer on at least one")
	}
}

// TestMaxFreeFlowSpeedMS pins the heuristic divisor to the maximum per-edge
// STRAIGHT-LINE (endpoint-chord) speed over the graph — chord/FreeFlowS — NOT the
// LengthM/FreeFlowS edge speed. As of issue #81 the toy fixture satisfies length_m >=
// endpoint chord, so chord/FreeFlowS <= LengthM/FreeFlowS now; the divisor is still
// the largest chord/FreeFlowS so that chord_i/divisor ≤ FreeFlowS_i holds for every
// edge (the admissibility guarantee — see maxFreeFlowSpeedMS). The test recomputes the
// expectation from the fixture's live geometry, so it tracks the regenerated coords.
func TestMaxFreeFlowSpeedMS(test *testing.T) {
	roadGraph := loadToyGraphInternal(test)
	got := maxFreeFlowSpeedMS(roadGraph)

	// Independently recompute max chord/FreeFlowS so the expectation tracks the
	// fixture's geometry rather than a hand-copied magic number.
	want := 0.0
	for edgeID := domain.EdgeID(0); int(edgeID) < roadGraph.EdgeCount(); edgeID++ {
		edge, ok := roadGraph.Edge(edgeID)
		if !ok || edge.FreeFlowS <= 0 {
			continue
		}
		fromNode, fromOK := roadGraph.Node(edge.From)
		toNode, toOK := roadGraph.Node(edge.To)
		if !fromOK || !toOK {
			continue
		}
		if speed := graph.GreatCircleM(fromNode.Pos, toNode.Pos) / edge.FreeFlowS; speed > want {
			want = speed
		}
	}
	if math.Abs(got-want) > 1e-6 {
		test.Errorf("maxFreeFlowSpeedMS = %.6f m/s, want %.6f m/s (max chord/FreeFlowS over the toy network)", got, want)
	}
}

// TestAStarHeuristicAdmissibleOnToyNetwork is the item-3 guarantee that the
// chord-based divisor keeps the heuristic admissible on the REAL toy fixture. The
// fixture used to have hand-written length_m values SHORTER than the endpoint chord
// (the geometry quirk that made the old LengthM/FreeFlowS divisor inadmissible); as
// of issue #81 the geometry was scaled so length_m >= chord holds, but the divisor is
// still derived from endpoint geometry rather than length_m, so this admissibility
// proof does not depend on that invariant. h ≤ true remaining free-flow time must
// hold for every reachable node→dst pair on the toy network itself.
func TestAStarHeuristicAdmissibleOnToyNetwork(test *testing.T) {
	roadGraph := loadToyGraphInternal(test)
	router := NewAStarRouter(roadGraph)
	n := roadGraph.NodeCount()

	for dst := domain.NodeID(0); int(dst) < n; dst++ {
		h := router.heuristic(dst)
		for node := domain.NodeID(0); int(node) < n; node++ {
			_, trueRemaining, reachable := shortestPath(roadGraph, node, dst, freeFlowWeight, nil, nil)
			if !reachable {
				continue
			}
			if estimate := h(node); estimate > trueRemaining+1e-9 {
				test.Errorf("toy network: heuristic inadmissible: h(%d->%d)=%.6f s exceeds true remaining %.6f s",
					node, dst, estimate, trueRemaining)
			}
		}
	}
}

// buildAnomalousEdgeGraph reproduces the exact wrong-path case the reviewer found.
// Four nodes; dst is node 3. The OPTIMAL route is the two cheap hops 0->1->3 (cost
// 0.2 s). Node 1's two edges are ANOMALOUS: each declares a tiny length_m (1 m) while
// node 1 sits geographically very far away (a ~85 km chord to dst), so on a real
// export-conformant graph length_m ≥ chord would hold but here it is grossly
// violated. Two slower honest paths also reach dst — the 0->2->3 detour (cost 10 s)
// and the direct 0->3 edge (cost 8 s) — whose endpoints are close, so their LengthM
// is near their chord and they pin a SMALL LengthM/FreeFlowS divisor.
//
// Under the OLD divisor max(LengthM/FreeFlowS) that small divisor makes
// h(1) = chord(1,3)/divisor a huge OVERESTIMATE of node 1's true 0.1 s remaining
// time — inadmissible — so A* pushes node 1 to the back of the frontier and settles
// dst on the cheaper-looking but actually-costlier direct 0->3 edge first: it returns
// cost 8.0 instead of the optimal 0.2 (the reviewer's "10.0 vs 0.02" shape). Under the
// chord-based divisor h stays admissible and A* returns the optimal 0.2.
func buildAnomalousEdgeGraph() *graph.AdjacencyGraph {
	// Node 1 is placed far to the north-east; 0, 2, 3 cluster near the origin so the
	// honest edges among them have length ≈ chord (a small LengthM/FreeFlowS divisor).
	pos := []domain.LatLon{
		{Lat: 40.73, Lon: -73.99},     // 0 origin
		{Lat: 41.50, Lon: -72.00},     // 1 far away — the anomalous node
		{Lat: 40.731, Lon: -73.989},   // 2 near origin
		{Lat: 40.7305, Lon: -73.9895}, // 3 destination, near origin
	}
	nodes := make([]graph.Node, len(pos))
	for index := range pos {
		nodes[index] = graph.Node{ID: domain.NodeID(index), Pos: pos[index]}
	}
	edges := []graph.Edge{
		// Optimal path 0->1->3: cheap (0.1 s each) but anomalous — declared LengthM
		// 1 m while node 1's endpoint chord is ~tens of km. This is what the old
		// divisor cannot account for.
		{ID: 0, Segment: "anom", From: 0, To: 1, LengthM: 1.0, FreeFlowS: 0.1, CapacityVPH: 1800},
		{ID: 1, Segment: "anom", From: 1, To: 3, LengthM: 1.0, FreeFlowS: 0.1, CapacityVPH: 1800},
		// Honest detour 0->2->3: length ≈ chord, slow (5 s each), summed cost 10 s.
		{ID: 2, Segment: "leg", From: 0, To: 2, LengthM: graph.GreatCircleM(pos[0], pos[2]), FreeFlowS: 5.0, CapacityVPH: 1800},
		{ID: 3, Segment: "leg", From: 2, To: 3, LengthM: graph.GreatCircleM(pos[2], pos[3]), FreeFlowS: 5.0, CapacityVPH: 1800},
		// Honest direct 0->3: length ≈ chord, cost 8 s — the WRONG path A* returns
		// under the inadmissible old heuristic.
		{ID: 4, Segment: "leg", From: 0, To: 3, LengthM: graph.GreatCircleM(pos[0], pos[3]), FreeFlowS: 8.0, CapacityVPH: 1800},
	}
	roadGraph, err := graph.New(nodes, edges)
	if err != nil {
		panic("buildAnomalousEdgeGraph: graph.New: " + err.Error())
	}
	return roadGraph
}

// TestAStarOptimalWithAnomalousEdge is the item-3 wrong-path regression: on the graph
// the reviewer found returning a non-optimal path (cost 8.0/10.0 vs the optimal 0.2,
// the "10.0 vs 0.02" shape) under the old LengthM/FreeFlowS max-speed divisor, A* must
// now return the SAME optimal cost as Dijkstra for every OD pair. The chord-based
// divisor keeps the heuristic admissible, so A* stays optimal even with the anomalous
// (length_m ≪ chord) edges present.
func TestAStarOptimalWithAnomalousEdge(test *testing.T) {
	roadGraph := buildAnomalousEdgeGraph()
	router := NewAStarRouter(roadGraph)
	n := roadGraph.NodeCount()

	for src := domain.NodeID(0); int(src) < n; src++ {
		for dst := domain.NodeID(0); int(dst) < n; dst++ {
			_, dCost, dOK := shortestPath(roadGraph, src, dst, freeFlowWeight, nil, nil)
			_, aCost, aOK := shortestPath(roadGraph, src, dst, freeFlowWeight, router.heuristic(dst), nil)
			if dOK != aOK {
				test.Errorf("%d->%d: reachability mismatch: dijkstra ok=%v, astar ok=%v", src, dst, dOK, aOK)
				continue
			}
			if !dOK {
				continue
			}
			if math.Abs(dCost-aCost) > 1e-9 {
				test.Errorf("%d->%d: A* returned non-optimal cost %.6f, Dijkstra optimal is %.6f (anomalous-edge wrong-path regression)",
					src, dst, aCost, dCost)
			}
		}
	}

	// Sharpest check: the optimal 0->3 route is the two anomalous cheap hops (cost
	// 0.2 s), NOT the honest direct edge (8 s) the old inadmissible heuristic chose.
	// Pin it explicitly so a future regression names the exact wrong-path cost.
	if _, cost, ok := shortestPath(roadGraph, 0, 3, freeFlowWeight, router.heuristic(3), nil); !ok || math.Abs(cost-0.2) > 1e-9 {
		test.Errorf("A* 0->3 cost = %.6f (ok=%v), want the optimal 0.2 s (0->1->3); the old divisor returned 8.0 here", cost, ok)
	}
}

// TestAStarParityOnToyNetwork is the item-6 toy-network parity criterion: A* returns
// IDENTICAL cost to Dijkstra for EVERY ordered OD pair on the REAL toy_network.geojson
// fixture. It passes despite the fixture's length_m < chord geometry quirk both
// because the new chord-based divisor keeps the heuristic admissible and because the
// graph is tiny — but the assertion is parity, exercised on the real fixture, not the
// synthetic grid. (The geo-consistent-grid parity test above is kept too.)
func TestAStarParityOnToyNetwork(test *testing.T) {
	roadGraph := loadToyGraphInternal(test)
	router := NewAStarRouter(roadGraph)
	n := roadGraph.NodeCount()

	for src := domain.NodeID(0); int(src) < n; src++ {
		for dst := domain.NodeID(0); int(dst) < n; dst++ {
			_, dCost, dOK := shortestPath(roadGraph, src, dst, freeFlowWeight, nil, nil)
			_, aCost, aOK := shortestPath(roadGraph, src, dst, freeFlowWeight, router.heuristic(dst), nil)
			if dOK != aOK {
				test.Errorf("toy %d->%d: reachability mismatch: dijkstra ok=%v, astar ok=%v", src, dst, dOK, aOK)
				continue
			}
			if !dOK {
				continue
			}
			if math.Abs(dCost-aCost) > 1e-9 {
				test.Errorf("toy %d->%d: cost mismatch: dijkstra=%.9f, astar=%.9f", src, dst, dCost, aCost)
			}
		}
	}
}

// TestAStarRouteSameCostAsNaive checks the public Route surface end-to-end: the A*
// router returns the same CostS as the naive (Dijkstra) router for a representative
// OD pair, confirming the seam holds through NearestNode snapping and Route, not
// only at the bare-core level.
func TestAStarRouteSameCostAsNaive(test *testing.T) {
	roadGraph := buildGeoConsistentGraph(6)
	astar := NewAStarRouter(roadGraph)
	naive := NewNaiveRouter(roadGraph)

	origin, _ := roadGraph.Node(0)                    // corner
	dest, _ := roadGraph.Node(domain.NodeID(6*6 - 1)) // opposite corner
	req := RouteRequest{ID: "corner->corner", From: origin.Pos, To: dest.Pos}

	astarRoute, err := astar.Route(context.Background(), req)
	if err != nil {
		test.Fatalf("astar.Route: %v", err)
	}
	naiveRoute, err := naive.Route(context.Background(), req)
	if err != nil {
		test.Fatalf("naive.Route: %v", err)
	}
	if math.Abs(astarRoute.CostS-naiveRoute.CostS) > 1e-9 {
		test.Errorf("A* CostS=%.9f != naive CostS=%.9f", astarRoute.CostS, naiveRoute.CostS)
	}
}
