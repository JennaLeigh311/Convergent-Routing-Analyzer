package benchmark

import (
	"context"
	"errors"
	"runtime"
	"sync"

	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/congestion"
	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/domain"
	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/routing"
)

// simulator_support.go holds the mesoscopic simulator's small support pieces kept
// out of the main loop for readability: the per-tick frozen congestion provider, the
// sharded concurrent route fan-out (mirroring routing.assignAONConcurrent's
// per-worker-state, no-shared-mutable-map discipline), and the local helpers
// (requestWeight, the nil-factory error).

// errNilRouterFactory is returned by Simulate when called with a nil RouterFactory —
// a usage error surfaced as a returned error, never a panic, so a caller wiring the
// seam wrong gets a clean diagnostic rather than a crash deep in the loop.
var errNilRouterFactory = errors.New("benchmark: Simulate requires a non-nil RouterFactory")

// frozenProvider is the per-tick immutable congestion.CongestionProvider: it serves
// ONE snapshot value taken at the top of a tick and never mutates, so every request
// released that tick — routed concurrently — provably sees the IDENTICAL, stable load
// view (the §R5 per-round snapshot discipline). It is built fresh each tick from
// load.Snapshot() (an owned copy), so the live load store can keep evolving while
// this tick's routing reads a frozen value, with no shared mutable state between them.
//
// It satisfies the full CongestionProvider port (Load/Snapshot/View) so any router
// the RouterFactory builds — reactive's per-batch Snapshot or per-request View —
// reads the same frozen value. Because the snapshot is never written after
// construction, View's borrow is stable for the tick (like the static adapter's),
// so the reactive router's concurrent reads are race-free.
type frozenProvider struct {
	snapshot congestion.LoadSnapshot
}

// Load returns the frozen load on an edge, or 0 out of range, per the port.
func (p frozenProvider) Load(edgeID domain.EdgeID) float64 { return p.snapshot.Load(edgeID) }

// Snapshot returns a fresh owned copy of the frozen load, so a batch router's
// per-round Snapshot is disjoint from this provider's backing array (the port
// requires Snapshot never alias the live store; here "live" is already frozen, but
// the copy is kept so a router mutating its snapshot cannot disturb a sibling).
func (p frozenProvider) Snapshot() congestion.LoadSnapshot {
	out := make(congestion.LoadSnapshot, len(p.snapshot))
	copy(out, p.snapshot)
	return out
}

// View returns a read-only borrow over the frozen snapshot WITHOUT copying — safe
// here because the snapshot is never mutated after the provider is built, so the
// borrow cannot tear (unlike a live mutable store's View). It lets the reactive
// router's single-request path read load allocation-free against a stable value.
func (p frozenProvider) View() congestion.LoadView { return p.snapshot }

// Compile-time assertion: frozenProvider satisfies the CongestionProvider port.
var _ congestion.CongestionProvider = frozenProvider{}

// routeBatchConcurrent routes the tick's newly-released requests (the indices in
// batch, into reqs) concurrently against router, returning the routes in batch
// order. It mirrors routing.assignAONConcurrent's concurrency discipline exactly:
// each worker owns its own slice region, requests are statically strided across
// workers by index (request i handled by worker i%workerCount, independent of
// scheduling), routes are written by their stable batch position (distinct index per
// request ⇒ no data race), and there is NO shared mutable map and NO lock on the
// accumulation. The result is therefore deterministic for a fixed router + batch
// regardless of how the scheduler interleaves the workers, and `-race` clean.
//
// router.Route is the per-request seam; the router the factory built closes over the
// tick's frozen provider, so every concurrent Route reads the same immutable load.
// On the first request that errors (no node near an endpoint, unreachable
// destination, cancelled ctx) the fan-out records it and returns it with a nil
// routes slice — the on-first-error contract the routers document. An empty batch
// returns an empty routes slice and no error.
func routeBatchConcurrent(
	ctx context.Context,
	router routing.Router,
	reqs []routing.RouteRequest,
	batch []int,
) ([]routing.Route, error) {
	routes := make([]routing.Route, len(batch))
	if len(batch) == 0 {
		return routes, nil
	}

	workerCount := workersFor(len(batch))

	var firstErr error
	var errOnce sync.Once
	setErr := func(err error) { errOnce.Do(func() { firstErr = err }) }

	var wg sync.WaitGroup
	for worker := 0; worker < workerCount; worker++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			// Static striding keeps the request→worker mapping fixed and independent of
			// scheduling; each worker writes only its own batch positions, so there is no
			// shared mutable accumulator and no lock.
			for position := worker; position < len(batch); position += workerCount {
				if err := ctx.Err(); err != nil {
					setErr(err)
					return
				}
				route, err := router.Route(ctx, reqs[batch[position]])
				if err != nil {
					setErr(err)
					return
				}
				routes[position] = route // distinct index per request: no data race
			}
		}(worker)
	}
	wg.Wait()

	if firstErr != nil {
		return nil, firstErr
	}
	return routes, nil
}

// workersFor picks the worker count for a fan-out of n requests: at most GOMAXPROCS,
// never more than n, at least 1. It mirrors routing.workersFor (which is unexported
// in that package) so the simulator's fan-out caps the same way the assignment core
// does — capping at n avoids idle goroutines for a small tick, capping at GOMAXPROCS
// avoids oversubscription on a heavy release tick.
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

// requestWeight is the effective per-edge flow contribution of a request: its Weight
// when positive, else the 1.0 default, mirroring routing.requestWeight (unexported in
// that package) so the simulator derives a vehicle's load contribution the same way
// the routers accumulate FinalFlows — a request with an unset or negative Weight
// counts as one vehicle, never subtracting flow.
func requestWeight(req routing.RouteRequest) float64 {
	if req.Weight > 0 {
		return req.Weight
	}
	return 1.0
}
