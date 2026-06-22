package benchmark

import (
	"context"
	"errors"
	"sync"

	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/routing"
)

// simulator_support.go holds the mesoscopic simulator's small support pieces kept
// out of the main loop for readability: the sharded concurrent route fan-out
// (mirroring routing.assignAONConcurrent's per-worker-state, no-shared-mutable-map
// discipline) and the nil-factory error. The per-tick frozen congestion provider is
// NOT re-implemented here: the simulator serves each tick's immutable load snapshot
// through the shared static.Provider adapter (static.NewFromSnapshot), so the
// per-round snapshot's Load/Snapshot/View contract is the SAME vetted one the offline
// congestion path uses, not a one-off duplicate. Likewise the worker-count and
// per-request-weight rules call the exported routing.WorkersFor / routing.RequestWeight
// so the fan-out caps and the vehicle load contributions match the routers exactly,
// rather than drifting from a byte-copy.

// errNilRouterFactory is returned by Simulate when called with a nil RouterFactory —
// a usage error surfaced as a returned error, never a panic, so a caller wiring the
// seam wrong gets a clean diagnostic rather than a crash deep in the loop.
var errNilRouterFactory = errors.New("benchmark: Simulate requires a non-nil RouterFactory")

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

	workerCount := routing.WorkersFor(len(batch))

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
