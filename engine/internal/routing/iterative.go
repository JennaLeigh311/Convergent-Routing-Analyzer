package routing

import (
	"context"
	"fmt"
	"math"
	"runtime"
	"sync"

	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/cost"
	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/domain"
	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/graph"
)

// iterative.go is the shared iterative-assignment core that incremental and msa
// (and, in #76, systemoptimal) are thin wrappers over. The machine is the same in
// every iterative strategy:
//
//  1. weight every edge with BPR (or, for systemoptimal, BPR.MarginalCost) over an
//     IMMUTABLE per-iteration load vector;
//  2. fan the OD pairs across goroutines, each with its OWN dijkstraScratch buffer
//     and its OWN partial-flow shard (never a shared mutable map under lock);
//  3. reduce the shards ONCE per iteration into a single round-load vector
//     (combineFlows, in deterministic edge order);
//  4. fold that round load into the running flow via a strategy-specific update
//     (incremental: cumulative batches; msa: 1/k average toward all-or-nothing);
//  5. measure the relative gap and stop at < gapTolerance or maxIterations.
//
// The strategies differ ONLY in (1) the weightFunc factory and (4) the per-iteration
// flow update — everything else (the fan-out, the reduce, the convergence loop, the
// snapshot discipline) lives here once, so a strategy is a weightFunc + an update
// rule. project-spec.md §R5; docs/algorithms.md §2, §5.

const (
	// gapTolerance is the relative-gap convergence threshold (docs/algorithms.md §5,
	// project-spec.md §R5): an iterative assignment is "converged" once the relative
	// gap between the cost on the current flows and the cost of the all-or-nothing
	// (shortest-path) assignment under those same weights drops below this.
	gapTolerance = 1e-4

	// maxIterations is the hard iteration budget (docs/algorithms.md §5): the loop
	// stops after this many iterations even if it has not reached gapTolerance, so a
	// non-converging or slow run is always bounded. The reported AssignResult then
	// has Converged = false with the achieved (still-finite) gap.
	maxIterations = 100
)

// weightFromFlows returns the weightFunc the Dijkstra core relaxes against for one
// iteration: the BPR cost of each edge under the load that iteration's IMMUTABLE
// flow vector reports for it. Closing over one fixed flows slice is what makes every
// request in an iteration see the identical load (the per-round consistency
// project-spec.md §R5 requires) — flows is never mutated while this closure is live;
// the next iteration builds a fresh closure over the next round's load.
//
// A missing/out-of-range edge reads as zero load (loadAt), i.e. free-flow cost — the
// snapshot-discipline rule that a missing-segment load is free-flow, not an error.
// BPR.Cost is always >= edge.FreeFlowS >= 0, so the weights stay non-negative as
// Dijkstra requires.
//
// systemoptimal (#76) will supply a sibling factory of this same shape with
// bpr.MarginalCost in place of bpr.Cost — the only thing that distinguishes its routing
// weights from this UE-family core. (The gap metric stays on bpr.Cost for both, so the
// swap is confined to the routing weightFunc.)
func weightFromFlows(bpr cost.BPR, flows []float64) weightFunc {
	return func(edge graph.Edge) float64 {
		return bpr.Cost(edge, loadAt(flows, edge.ID))
	}
}

// routeSingleFreeFlow answers a single request on free-flow weights, the shared
// Route-method body for the iterative routers (msa, incremental). A lone request has
// no demand to equilibrate against, so its user-equilibrium / converged path is just
// its free-flow shortest path — the iteration that distinguishes these strategies only
// bites on a BATCH where requests interact. strategy prefixes the error messages so
// each router's single-request errors read with its own name. Same-node From/To is a
// clean zero-edge, zero-cost Route, never an error.
func routeSingleFreeFlow(roadGraph graph.Graph, req RouteRequest, strategy string) (Route, error) {
	src, found := roadGraph.NearestNode(req.From)
	if !found {
		return Route{}, fmt.Errorf("%s: request %q: no graph node near origin %+v", strategy, req.ID, req.From)
	}
	dst, found := roadGraph.NearestNode(req.To)
	if !found {
		return Route{}, fmt.Errorf("%s: request %q: no graph node near destination %+v", strategy, req.ID, req.To)
	}
	path, pathCost, found := dijkstra(roadGraph, src, dst, freeFlowWeight)
	if !found {
		return Route{}, fmt.Errorf("%s: request %q: no path from node %d to node %d", strategy, req.ID, src, dst)
	}
	return Route{RequestID: req.ID, Edges: path, CostS: pathCost}, nil
}

// loadAt reads flows[edgeID], treating an out-of-range edge id as zero load (the
// missing-segment-is-free-flow rule). It is the single read point the iterative
// weightFuncs and the gap computation share so the "missing load = 0, never an
// error" decision lives in one place.
func loadAt(flows []float64, edgeID domain.EdgeID) float64 {
	if edgeID < 0 || int(edgeID) >= len(flows) {
		return 0
	}
	return flows[edgeID]
}

// combineFlows is the REDUCE half of the sharded map-reduce flow accumulator (#71
// deferred it; #74 makes the fan-out real): it sums the per-worker shards element-wise
// into one dense round-load vector of length n. Each worker writes only its OWN shard
// during the MAP phase (no shared mutable state, no lock); this runs once per iteration,
// single-goroutine, after every worker has finished — so there is never a concurrent
// writer.
//
// The sum is taken in a FIXED order (shard 0, 1, ...; edge 0..n-1 ascending) because
// floating-point addition is not associative, so a fixed order is what makes the output
// byte-identical run to run. A shard shorter than n contributes 0 for the missing tail
// and a shard element past n is ignored (defensive; the fan-out always sizes shards to
// EdgeCount).
func combineFlows(n int, shards [][]float64) []float64 {
	combined := make([]float64, n)
	for _, shard := range shards {
		limit := len(shard)
		if limit > n {
			limit = n
		}
		for edgeID := 0; edgeID < limit; edgeID++ {
			combined[edgeID] += shard[edgeID]
		}
	}
	return combined
}

// assignOutcome is the result of one all-or-nothing (AON) fan-out over a batch of
// OD pairs under a fixed weightFunc: the chosen route per request (input order), the
// reduced per-edge flow those routes place (each request's weight on every edge of
// its path), and the summed routing cost of the assignment (Σ weight × path cost),
// which the relative-gap metric uses as the shortest-path total cost SPTT.
type assignOutcome struct {
	routes  []Route
	flows   []float64
	totalSP float64
}

// assignAONConcurrent is the MAP+REDUCE fan-out at the heart of every iteration:
// it routes every OD pair against weight, sharding the requests across goroutines so
// the thousands of Dijkstras one iteration runs go in parallel, then reduces the
// per-worker shards into one round-flow vector.
//
// Concurrency model (project-spec.md §R5): each worker gets its OWN dijkstraScratch
// buffer (newDijkstraScratch — scratch is single-goroutine state, never shared) and
// its OWN partial-flow shard ([]float64 length EdgeCount). Workers share only the
// immutable graph and the read-only weight closure; there is NO shared mutable map
// and NO lock on the flow accumulation. The reduce (combineFlows) runs once, after
// the WaitGroup, single-goroutine.
//
// Determinism: routes are written to result.routes by the request's stable input
// index (never append order), and the shards are reduced in fixed worker-then-edge
// order, so the output is byte-identical run to run for a fixed weight + OD set
// regardless of how the scheduler interleaves the workers.
//
// An unreachable OD pair is a hard error (no path from src to dst); the first worker
// to hit one records it and the function returns that error with a nil outcome,
// matching the on-first-error contract Assign documents. ctx cancellation is checked
// per request so a cancelled context stops the fan-out promptly.
func assignAONConcurrent(
	ctx context.Context,
	roadGraph graph.Graph,
	pairs []odPair,
	reqs []RouteRequest,
	weight weightFunc,
	strategy string,
) (assignOutcome, error) {
	edgeCount := roadGraph.EdgeCount()
	routes := make([]Route, len(pairs))

	workerCount := workersFor(len(pairs))
	shards := make([][]float64, workerCount)
	totals := make([]float64, workerCount)

	var firstErr error
	var errOnce sync.Once
	setErr := func(err error) { errOnce.Do(func() { firstErr = err }) }

	var wg sync.WaitGroup
	for worker := 0; worker < workerCount; worker++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			// Per-worker state: its own scratch (NOT concurrency-safe, so never
			// shared) and its own flow shard (no shared mutable accumulator).
			scratch := newDijkstraScratch(roadGraph.NodeCount())
			shard := make([]float64, edgeCount)
			var localTotal float64
			// Static striding by index keeps the request→worker mapping fixed
			// (request i always handled by worker i%workerCount), so it does not
			// depend on goroutine scheduling — only the per-shard reduce needs to be
			// order-fixed, which combineFlows guarantees.
			for index := worker; index < len(pairs); index += workerCount {
				if err := ctx.Err(); err != nil {
					setErr(err)
					return
				}
				path, pathCost, found := dijkstraScratch(roadGraph, pairs[index].src, pairs[index].dst, weight, scratch)
				if !found {
					setErr(fmt.Errorf("%s: request %q: no path from node %d to node %d", strategy, reqs[index].ID, pairs[index].src, pairs[index].dst))
					return
				}
				route := Route{RequestID: reqs[index].ID, Edges: path, CostS: pathCost}
				routes[index] = route // distinct index per request: no data race
				w := requestWeight(reqs[index])
				addRouteFlow(shard, route, w)
				localTotal += w * pathCost
			}
			shards[worker] = shard
			totals[worker] = localTotal
		}(worker)
	}
	wg.Wait()

	if firstErr != nil {
		return assignOutcome{}, firstErr
	}

	flows := combineFlows(edgeCount, shards)
	var totalSP float64
	for _, t := range totals { // fixed worker order, deterministic sum
		totalSP += t
	}
	return assignOutcome{routes: routes, flows: flows, totalSP: totalSP}, nil
}

// workersFor picks the worker count for a fan-out of n requests: at most GOMAXPROCS,
// never more workers than requests, and at least 1 (so an empty batch still spins one
// no-op worker rather than dividing by zero). Capping at the request count avoids
// idle goroutines for small batches; capping at GOMAXPROCS avoids oversubscription on
// the large (1,000-request) demand batch.
func workersFor(n int) int {
	if n <= 0 {
		return 1
	}
	workers := runtime.GOMAXPROCS(0)
	if workers < 1 {
		workers = 1
	}
	if workers > n {
		workers = n
	}
	return workers
}

// totalSystemCost returns Σ_e flows[e] × BPR.Cost(e, flows[e]) — the total travel
// time the network experiences under the given flow (TSTT), summed in deterministic
// edge order. It is the numerator side of the relative gap: the cost actually being
// paid on the current flows, against which the all-or-nothing shortest-path total
// (SPTT) is compared. A missing/out-of-range edge contributes nothing.
func totalSystemCost(roadGraph graph.Graph, bpr cost.BPR, flows []float64) float64 {
	var total float64
	for edgeID := domain.EdgeID(0); int(edgeID) < roadGraph.EdgeCount(); edgeID++ { // 0..EdgeCount-1, deterministic order
		edge, ok := roadGraph.Edge(edgeID)
		if !ok {
			continue
		}
		load := loadAt(flows, edgeID)
		total += load * bpr.Cost(edge, load)
	}
	return total
}

// iterationStep is one strategy-specific iteration of the shared convergence loop.
// The loop hands it the 1-based iteration number k and the CURRENT running flow
// (immutable for the duration of the call — the step must not mutate it in place) and
// expects back the NEXT running flow plus the relative gap achieved at this iteration.
// done lets a strategy that has structurally finished (incremental, once every batch
// is loaded) stop the loop early without forcing the gap below the tolerance.
//
// The step owns the two things that distinguish the strategies — which requests it
// routes this iteration and how it folds the all-or-nothing result into the running
// flow — and reuses assignAONConcurrent / combineFlows / relativeGap for everything
// else. routes is the chosen path per request to surface in AssignResult; on the
// loop's final iteration these are the routes reported.
type iterationStep func(ctx context.Context, k int, flow []float64) (next []float64, routes []Route, gap float64, done bool, err error)

// runConvergenceLoop drives the shared iterative-assignment loop: starting from an
// all-zero flow, it calls step for k = 1, 2, ... until the relative gap drops below
// gapTolerance, the step reports done, or it reaches maxIterations — whichever comes
// first. It returns the populated AssignResult (FinalFlows from the final iteration,
// the final routes, and Gap/Iters/Converged metadata).
//
// Convergence (project-spec.md §R5, docs/algorithms.md §5): Converged is true iff the
// loop stopped because the gap fell below gapTolerance (or a step reported done having
// reached equilibrium). Hitting maxIterations with the gap still above tolerance
// reports Converged = false with the achieved (finite) gap — an honest "did not
// converge in budget", never a crash or a fabricated success.
//
// The loop checks ctx.Err() before each iteration so a cancelled or deadline-exceeded
// context stops promptly with that error and a zero AssignResult. An empty batch
// converges immediately at iteration 1 with a zero flow and gap 0.
func runConvergenceLoop(ctx context.Context, roadGraph graph.Graph, step iterationStep) (AssignResult, error) {
	flow := newFlowVector(roadGraph)
	var routes []Route
	var gap float64
	iters := 0
	converged := false

	for k := 1; k <= maxIterations; k++ {
		if err := ctx.Err(); err != nil {
			return AssignResult{}, err
		}
		next, stepRoutes, stepGap, done, err := step(ctx, k, flow)
		if err != nil {
			return AssignResult{}, err
		}
		flow = next
		routes = stepRoutes
		gap = stepGap
		iters = k
		if gap < gapTolerance {
			converged = true
			break
		}
		if done {
			// The strategy has structurally finished (e.g. incremental loaded every
			// batch). Its assignment is complete; report it as converged at the gap it
			// achieved — there are no further iterations that would refine it. Per the
			// AssignResult.Converged contract (routing.go), Converged means "reached its
			// stopping criterion", not "is at user equilibrium"; the honest
			// distance-from-equilibrium is carried in Gap.
			converged = true
			break
		}
	}

	if routes == nil {
		routes = []Route{} // empty batch: a non-nil, zero-length routes slice
	}
	return AssignResult{
		Routes:     routes,
		FinalFlows: flow,
		Gap:        gap,
		Iters:      iters,
		Converged:  converged,
	}, nil
}

// relativeGap is the standard traffic-assignment relative gap (docs/algorithms.md §5):
//
//	gap = (TSTT - SPTT) / SPTT
//
// where TSTT is the total system travel time on the current flows and SPTT is the
// total cost of the all-or-nothing assignment computed under those same edge weights
// (every traveler on their current-cost shortest path). At User Equilibrium nobody can
// do better, so TSTT == SPTT and the gap is 0; a positive gap means there is still a
// cheaper assignment available. The fraction is taken as an absolute value so a tiny
// negative from floating-point round-off near convergence reads as a small magnitude,
// not a spurious "negative gap".
//
// When SPTT is 0 (an empty batch, or every OD pair is same-node so no flow moves) the
// gap is defined as 0: there is no demand to misroute, so the assignment is trivially
// at equilibrium. This keeps the gap finite (never a 0/0 NaN or x/0 +Inf) on the
// degenerate inputs the adversarial fixture and empty batches produce.
func relativeGap(tstt, sptt float64) float64 {
	if sptt <= 0 {
		return 0
	}
	return math.Abs(tstt-sptt) / sptt
}
