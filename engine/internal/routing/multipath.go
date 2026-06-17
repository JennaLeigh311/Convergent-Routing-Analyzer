package routing

import (
	"context"
	"fmt"
	"math/rand"

	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/cost"
	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/domain"
	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/graph"
)

// defaultMultipathK is the number of alternative paths multipath computes per OD
// pair when none is configured. Three is the catalog-demo default: enough to show
// demand spreading across distinct routes without the K-set degenerating to near-
// identical paths on a small network.
const defaultMultipathK = 3

// MultipathRouter is the demand-spreading strategy (algorithm catalog row 6,
// project-spec.md §4): for each OD pair it computes K alternative loopless paths
// with Yen's algorithm and then SPLITS the requests across those K paths with a
// seeded proportional/probabilistic rule, rather than piling every request onto
// the single cheapest path (what naive does). Spreading demand is the point — a
// cheaper path attracts proportionally more requests, but the others still draw
// load, so no single road absorbs the whole OD's demand.
//
// DETERMINISM — per-REQUEST seeding (issue #75, carried forward from the PR #80
// review). The probabilistic split is seeded PER REQUEST, deriving each request's
// RNG from its stable input index XORed with the base seed (NewSeededRNG), NOT per
// worker. Per-worker streams stay race-free but a request would draw from a
// different stream from run to run under nondeterministic request→worker
// scheduling, so a fixed seed would NOT reproduce the same split. Per-request
// seeding makes a request's draw depend only on (baseSeed, request index) — never
// on which goroutine handled it — so a fixed seed produces a byte-identical split
// across runs (see repro.go's NewSeededRNG warning, and the determinism test).
//
// The router holds only the immutable graph, a stateless cost function, the base
// seed, and K — no mutable state — so its methods are safe for concurrent use by
// multiple goroutines.
type MultipathRouter struct {
	g      graph.Graph
	costFn cost.CostFunction
	seed   int64
	k      int
}

// NewMultipathRouter returns a MultipathRouter over the (immutable, already-loaded)
// graph that computes k alternative paths per OD with Yen over free-flow-derived
// BPR weights at zero load (so the K-set is the K cheapest distinct routes on the
// uncongested network — the demand-spreading candidates), splitting requests across
// them with a fixed-seed probabilistic rule. A non-positive k falls back to
// defaultMultipathK. seed is the single base seed every per-request RNG is derived
// from; a fixed seed yields a reproducible split.
func NewMultipathRouter(roadGraph graph.Graph, costFn cost.CostFunction, seed int64, k int) *MultipathRouter {
	if k <= 0 {
		k = defaultMultipathK
	}
	return &MultipathRouter{g: roadGraph, costFn: costFn, seed: seed, k: k}
}

// Compile-time assertion: *MultipathRouter satisfies the Router port.
var _ Router = (*MultipathRouter)(nil)

// Name identifies this strategy in benchmark output and the API.
func (router *MultipathRouter) Name() string { return "multipath" }

// splitWeight is the weightFunc Yen's K-shortest search runs against: the BPR cost
// of each edge at ZERO load. multipath spreads demand over the K cheapest distinct
// uncongested routes, so the candidate set is computed on the free-flow network
// (load 0); the split itself, not a congested re-route, is what distributes load.
// BPR.Cost at load 0 is exactly FreeFlowS, so this stays >= 0 as Dijkstra requires.
func (router *MultipathRouter) splitWeight() weightFunc {
	return func(edge graph.Edge) float64 {
		return router.costFn.Cost(edge, 0)
	}
}

// ODKPaths is the K-shortest path set computed for one distinct OD pair, the
// per-OD half of the split provenance. paths[i] is the i-th cheapest loopless
// route (non-decreasing cost); Intended[i] is the probability mass the split rule
// assigned path i; Realized[i] is the fraction of this OD's requests that actually
// drew path i. Intended vs. Realized is exactly the before/after demand-spreading
// comparison the visualization engineer needs — surfaced here, not reconstructed
// by deduping edge sequences after the fact.
type ODKPaths struct {
	// Src and Dst are the resolved graph node ids this K-set routes between. ODs
	// are keyed by (Src, Dst), so requests sharing endpoints share one ODKPaths
	// entry (and one K-set) rather than recomputing Yen per request.
	Src domain.NodeID
	Dst domain.NodeID

	// Paths is the up-to-K loopless paths in non-decreasing cost order, as returned
	// by Yen. Each carries its ordered edge ids and the cost it was found under.
	Paths []ProvenancePath

	// Intended is the split rule's target probability per path (sums to 1 over a
	// non-empty K-set); Intended[i] pairs with Paths[i]. It is the proportional
	// mass the seeded rule aimed to place on path i.
	Intended []float64

	// Realized is the achieved fraction per path: Realized[i] is the count of this
	// OD's requests that drew path i divided by the OD's request count. It pairs
	// with Paths[i] and sums to 1 over a non-empty K-set with at least one request.
	// Intended vs. Realized diverges at small request counts (the seeded draw is
	// probabilistic) and converges as demand grows.
	Realized []float64

	// requestCount is the number of requests routed over this OD, the denominator
	// Realized is normalized by. Unexported: consumers read Realized, not the raw
	// count.
	requestCount int
}

// ProvenancePath is one path in an OD's K-set as carried on the provenance: its
// ordered edge ids and the cost it was found under. It mirrors kPath but is the
// EXPORTED provenance shape (kPath is package-internal to the Yen core).
type ProvenancePath struct {
	Edges []domain.EdgeID
	Cost  float64
}

// MultipathProvenance is the split-provenance ADJUNCT — the structure issue #75
// requires multipath to surface so the demand-spreading visual can show which of
// the K paths each request took, the per-OD K-path set, and intended-vs-realized
// proportions. It is carried ALONGSIDE the shared AssignResult (on MultipathResult,
// never by mutating AssignResult — issue #74 is concurrently building on the
// routing package and a change to the shared type would collide), so the K-set
// structure is retrievable directly, not reconstructed by deduping the flattened
// Routes.
type MultipathProvenance struct {
	// ODPaths is the per-OD K-path set, one entry per DISTINCT (Src, Dst) pair, in
	// a stable order (first-appearance of the OD in the request batch). Each entry
	// carries that OD's K paths and its intended/realized split proportions.
	ODPaths []ODKPaths

	// ChosenPathIndex[i] is the index into its OD's ODKPaths.Paths that request i
	// (in input order) drew — request→path index, the per-request half of the
	// provenance. ChosenODIndex[i] is the index into ODPaths of that request's OD,
	// so a consumer pairs request i to its K-set with ODPaths[ChosenODIndex[i]] and
	// to its chosen path with .Paths[ChosenPathIndex[i]] — no edge-sequence
	// deduping required.
	ChosenPathIndex []int
	ChosenODIndex   []int
}

// MultipathResult is multipath's full return: the shared AssignResult (routes,
// final flows, convergence metadata — exactly what every other router returns) PLUS
// the split-provenance adjunct. Embedding AssignResult keeps every existing
// AssignResult consumer working unchanged (a MultipathResult IS an AssignResult for
// field access), while Provenance exposes the K-set structure the visualization
// engineer reads. AssignResult itself is left untouched so issue #74's concurrent
// work on the routing package does not collide.
type MultipathResult struct {
	AssignResult
	Provenance MultipathProvenance
}

// Route answers a single request by computing its K-shortest paths and drawing one
// with the request's per-request-seeded RNG, then returning that path as a Route.
// A single request is a degenerate split (one draw), so Route is mostly a
// convenience over the batch path; the demand-spreading story lives in Assign,
// where many requests share an OD's K-set. A cancelled context, an endpoint that
// snaps to no node, or an unreachable destination is returned as an error.
func (router *MultipathRouter) Route(ctx context.Context, req RouteRequest) (Route, error) {
	if err := ctx.Err(); err != nil {
		return Route{}, err
	}
	src, found := router.g.NearestNode(req.From)
	if !found {
		return Route{}, fmt.Errorf("multipath: request %q: no graph node near origin %+v", req.ID, req.From)
	}
	dst, found := router.g.NearestNode(req.To)
	if !found {
		return Route{}, fmt.Errorf("multipath: request %q: no graph node near destination %+v", req.ID, req.To)
	}

	paths := kShortestPaths(router.g, src, dst, router.k, router.splitWeight())
	if len(paths) == 0 {
		return Route{}, fmt.Errorf("multipath: request %q: no path from node %d to node %d", req.ID, src, dst)
	}
	intended := proportionalSplit(paths)
	choice := drawPath(intended, requestRNG(router.seed, 0))
	return Route{RequestID: req.ID, Edges: paths[choice].edges, CostS: paths[choice].cost}, nil
}

// Assign solves the batch problem and returns just the routes, in input order. It
// is the paths-only face of AssignResult for callers (the benchmark, the route
// CLI) that do not need flows, convergence metadata, or the split provenance.
func (router *MultipathRouter) Assign(ctx context.Context, reqs []RouteRequest) ([]Route, error) {
	return AssignFromResult(ctx, reqs, router.AssignResult)
}

// AssignResult solves the batch problem and returns the shared AssignResult shape.
// It delegates to AssignMultipath and returns the embedded AssignResult, dropping
// the provenance adjunct — a caller that needs the K-set structure calls
// AssignMultipath directly. This keeps multipath a drop-in Router (its AssignResult
// matches every other strategy's) while the richer split provenance stays available
// on the side.
func (router *MultipathRouter) AssignResult(ctx context.Context, reqs []RouteRequest) (AssignResult, error) {
	result, err := router.AssignMultipath(ctx, reqs)
	if err != nil {
		return AssignResult{}, err
	}
	return result.AssignResult, nil
}

// AssignMultipath is multipath's real work: compute K alternative paths per
// DISTINCT OD pair (Yen over the free-flow split weights), split the requests
// across them with the per-request-seeded probabilistic rule, and return both the
// shared AssignResult and the split-provenance adjunct.
//
// Determinism: each request draws from an RNG seeded from (baseSeed, request
// index) via requestRNG — never from a per-worker stream — so the chosen-path
// vector is byte-identical across runs for a fixed seed regardless of how requests
// would be scheduled across workers (the carried-forward PR #80 review point). ODs
// are processed in first-appearance order and the K-set per OD is Yen's
// deterministic output, so the whole result is reproducible.
//
// It snaps every request's endpoints to node ids ONCE up front (prefetchOD), checks
// ctx.Err() before each request, and on the first routing error returns a zero
// MultipathResult and that error (never a partial result). RequestID is preserved
// on every returned Route so the frontend can pair routes back to OD pairs.
func (router *MultipathRouter) AssignMultipath(ctx context.Context, reqs []RouteRequest) (MultipathResult, error) {
	pairs, err := prefetchOD(router.g, reqs, router.Name())
	if err != nil {
		return MultipathResult{}, err
	}

	weight := router.splitWeight()

	// One K-set per DISTINCT OD, in first-appearance order. odIndexByKey maps an OD
	// key to its slot in odPaths; requestODSlot[i] records request i's slot.
	type odEntry struct {
		paths    []kPath
		intended []float64
		counts   []int
		src      domain.NodeID
		dst      domain.NodeID
	}
	odIndexByKey := make(map[odPair]int)
	var entries []*odEntry
	requestODSlot := make([]int, len(reqs))

	routes := make([]Route, len(reqs))
	flows := newFlowVector(router.g)
	chosenPathIndex := make([]int, len(reqs))

	for index, req := range reqs {
		if err := ctx.Err(); err != nil {
			return MultipathResult{}, err
		}
		pair := pairs[index]
		slot, ok := odIndexByKey[pair]
		if !ok {
			paths := kShortestPaths(router.g, pair.src, pair.dst, router.k, weight)
			if len(paths) == 0 {
				return MultipathResult{}, fmt.Errorf("multipath: request %q: no path from node %d to node %d", req.ID, pair.src, pair.dst)
			}
			slot = len(entries)
			entries = append(entries, &odEntry{
				paths:    paths,
				intended: proportionalSplit(paths),
				counts:   make([]int, len(paths)),
				src:      pair.src,
				dst:      pair.dst,
			})
			odIndexByKey[pair] = slot
		}
		requestODSlot[index] = slot

		entry := entries[slot]
		// PER-REQUEST seed: depends only on (baseSeed, request index), never on a
		// worker. This is what makes the split reproducible under a fixed seed.
		choice := drawPath(entry.intended, requestRNG(router.seed, index))
		entry.counts[choice]++
		chosenPathIndex[index] = choice

		route := Route{RequestID: req.ID, Edges: entry.paths[choice].edges, CostS: entry.paths[choice].cost}
		routes[index] = route
		addRouteFlow(flows, route, requestWeight(req))
	}

	// Materialize the provenance from the accumulated per-OD counts.
	odPaths := make([]ODKPaths, len(entries))
	for slot, entry := range entries {
		total := 0
		for _, count := range entry.counts {
			total += count
		}
		realized := make([]float64, len(entry.paths))
		if total > 0 {
			for pathIndex, count := range entry.counts {
				realized[pathIndex] = float64(count) / float64(total)
			}
		}
		provPaths := make([]ProvenancePath, len(entry.paths))
		for pathIndex, p := range entry.paths {
			provPaths[pathIndex] = ProvenancePath{Edges: p.edges, Cost: p.cost}
		}
		odPaths[slot] = ODKPaths{
			Src:          entry.src,
			Dst:          entry.dst,
			Paths:        provPaths,
			Intended:     entry.intended,
			Realized:     realized,
			requestCount: total,
		}
	}

	return MultipathResult{
		AssignResult: AssignResult{
			Routes:     routes,
			FinalFlows: flows,
			Gap:        0,
			Iters:      1,
			Converged:  true,
		},
		Provenance: MultipathProvenance{
			ODPaths:         odPaths,
			ChosenPathIndex: chosenPathIndex,
			ChosenODIndex:   requestODSlot,
		},
	}, nil
}

// proportionalSplit returns the intended split probability per path: a cheaper
// path attracts proportionally MORE demand. It weights each path by the inverse of
// its cost (so cost 0 paths are handled) and normalizes to sum 1. A degenerate
// zero-cost K-set (every path cost 0 — the src == dst case, or a zero-weight
// graph) falls back to a uniform split. The result pairs index-for-index with the
// paths and is the Intended vector carried on the provenance.
func proportionalSplit(paths []kPath) []float64 {
	n := len(paths)
	if n == 0 {
		return nil
	}
	weights := make([]float64, n)
	sum := 0.0
	allZero := true
	for index, p := range paths {
		if p.cost > 0 {
			weights[index] = 1.0 / p.cost
			allZero = false
		}
		sum += weights[index]
	}
	if allZero || sum <= 0 {
		uniform := make([]float64, n)
		for index := range uniform {
			uniform[index] = 1.0 / float64(n)
		}
		return uniform
	}
	for index := range weights {
		weights[index] /= sum
	}
	return weights
}

// drawPath picks a path index from the intended split using one draw from rng. It
// is the inverse-CDF sample over the intended probabilities: draw u in [0,1) and
// return the first index whose cumulative mass exceeds u. With a per-request-seeded
// rng (requestRNG) the draw depends only on the seed and the request, so the split
// is reproducible. An empty split returns 0 defensively.
func drawPath(intended []float64, rng interface{ Float64() float64 }) int {
	if len(intended) == 0 {
		return 0
	}
	u := rng.Float64()
	cumulative := 0.0
	for index, mass := range intended {
		cumulative += mass
		if u < cumulative {
			return index
		}
	}
	return len(intended) - 1 // floating-point slack: u landed at/just below 1.0
}

// requestRNG returns the RNG for one request: seeded from baseSeed XOR the
// request's input index, so a request's draw depends ONLY on (baseSeed, index) and
// never on which worker handles it. This per-REQUEST seeding (not per-worker) is
// what makes a fixed-seed split byte-identical across runs under nondeterministic
// scheduling — the determinism hole the PR #80 review flagged for multipath.
func requestRNG(baseSeed int64, requestIndex int) *rand.Rand {
	return NewSeededRNG(baseSeed ^ int64(requestIndex))
}
