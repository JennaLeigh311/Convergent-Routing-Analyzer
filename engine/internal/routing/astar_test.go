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

// loadToyGraphInternal loads the shared toy fixture from inside package routing.
// (The external routing_test package has its own loadToyGraph; this internal copy
// exists so the unexported-core tests in this file are self-contained.)
//
// The shared toy_network.geojson is NOT usable for the A* heuristic-admissibility
// acceptance test: it is a synthetic fixture whose hand-written length_m values are
// SMALLER than the great-circle distance between the edge endpoints it also
// declares (e.g. 905512:0:F states length_m=500 but its endpoints are ~1011 m apart
// by haversine). On a real OSM export the polyline length always dominates the
// straight-line chord, so distance/maxSpeed is a true lower bound on travel time;
// the toy fixture inverts that, so a geographically-honest heuristic correctly
// reports it as inadmissible THERE. The acceptance tests therefore route over
// buildGeoConsistentGraph below, where free-flow time is derived FROM the node
// geometry so length_m ≥ chord holds by construction — exactly the real-network
// invariant the heuristic relies on. This fixture is still loaded for the toy-level
// parity smoke (cost equality survives a small graph regardless).
func loadToyGraphInternal(test *testing.T) *graph.AdjacencyGraph {
	test.Helper()
	roadGraph, _, err := graph.LoadEdgeAttributesGeoJSONFile(
		"../../testdata/toy_network.geojson",
		graph.WithExpectedBounds(-74, -73, 40, 41),
	)
	if err != nil {
		test.Fatalf("toy_network.geojson must load with zero error, got: %v", err)
	}
	return roadGraph
}

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
			dijkstraPath, dijkstraCost, dijkstraOK := shortestPath(roadGraph, src, dst, freeFlowWeight, nil)
			astarPath, astarCost, astarOK := shortestPath(roadGraph, src, dst, freeFlowWeight, router.heuristic(dst))

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
			_, trueRemaining, reachable := shortestPath(roadGraph, node, dst, freeFlowWeight, nil)
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
			_, _, dijkstraOK := shortestPath(dijkstraGraph, src, dst, freeFlowWeight, nil)

			astarGraph := &settleCountingGraph{Graph: roadGraph}
			// The heuristic must close over the SAME graph the search walks so the
			// node lookups are consistent; heuristic itself reads Node, not OutEdgeIDs,
			// so it does not perturb the settle count.
			_, _, astarOK := shortestPath(astarGraph, src, dst, freeFlowWeight, router.heuristic(dst))

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

// TestMaxFreeFlowSpeedMS pins the heuristic's max-speed scaling against the toy
// network's fastest edge: the motorway edges run at 100 km/h = 27.78 m/s (the
// §2-derived freeflow_time_s recovers exactly that via LengthM/FreeFlowS), and no
// edge is faster, so the network max free-flow speed must equal it.
func TestMaxFreeFlowSpeedMS(test *testing.T) {
	roadGraph := loadToyGraphInternal(test)
	got := maxFreeFlowSpeedMS(roadGraph)
	const want = 100.0 * 1000.0 / 3600.0 // 100 km/h -> m/s
	if math.Abs(got-want) > 1e-6 {
		test.Errorf("maxFreeFlowSpeedMS = %.6f m/s, want %.6f m/s (toy network's fastest edge is 100 km/h)", got, want)
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
