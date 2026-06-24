package benchmark

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/congestion"
	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/cost"
	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/graph"
	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/routing"
)

// parallel.go is the §R6 / #93 multi-algorithm parallel simulation orchestrator:
// it runs ALL SIX routers (benchmark.RouterOrder) concurrently over the §R5
// mesoscopic simulator from a chosen start time-of-day/date and streams each
// algorithm's evolving per-tick state — the data source the Phase-6 comparison
// frontend animates (project-outline.md "Functionality clarification").
//
// CONCURRENCY MODEL. The road graph is IMMUTABLE after load and shared read-only
// across every goroutine. Each router runs its OWN independent simulation on its
// OWN goroutine via benchmark.Simulate: six sims, no shared mutable state, no lock
// on the hot path. Each sim takes ONE frozen per-tick congestion snapshot (the §R5
// per-round discipline the simulator already enforces) and emits an immutable
// TickState; the orchestrator forwards each TickState — enriched with the per-algo
// running metrics this file computes — to a consumer-supplied callback. The same OD
// set is fed to every router so the six streams are directly comparable.
//
// I/O DECOUPLING. The simulator's TickObserver runs ON the simulation goroutine and
// BLOCKS the model (the next tick does not advance until it returns). The
// orchestrator's per-algo observer therefore does NO blocking I/O: it only computes
// the tick's metrics (cheap, CPU-bound) and hands an AlgoTick to the consumer
// callback. The WebSocket layer's callback must itself be non-blocking (it buffers
// onto a channel and flushes off the hot path) so a slow client cannot stall a sim
// — see internal/api/stream.go.

// ParallelConfig configures one multi-algorithm parallel run. It is the §R6 control
// surface the frontend's time-of-day slider and replay-speed control map onto.
type ParallelConfig struct {
	// StartTime is the time-of-day/date origin of every sim clock: the wall-clock
	// instant tick 0 corresponds to (SimConfig.StartTime). Shifting it shifts every
	// algorithm's stream observably — the #93 "slider sets the start clock"
	// requirement — without changing the relative dynamics. The zero Time is valid
	// (it labels tick 0 as the zero instant).
	StartTime time.Time

	// TickSeconds is the SIMULATED Δt each sim advances per tick (§R5 ≈30s). A
	// non-positive value falls back to the simulator's default. This is the
	// simulation's internal resolution; it is DISTINCT from the wall-clock cadence at
	// which the WebSocket layer emits frames (that is the fixed server tick, owned by
	// the stream layer) and from Speed (which compresses simulated time relative to
	// wall clock).
	TickSeconds float64

	// MaxTicks bounds each sim (see SimConfig.MaxTicks); 0 uses the simulator's
	// derived generous default.
	MaxTicks int

	// Seed is the fixed RNG seed making the run reproducible: the same seed yields a
	// byte-identical per-tick trace for every algorithm (modulo wall clock), the §R5
	// / #93 determinism criterion.
	Seed int64

	// Count is the per-run OD request count R drawn for the shared demand set. A
	// non-positive value falls back to DefaultODCount.
	Count int

	// CapacityScale is the §R3 capacity knob (cost.BPR.CapacityScale) the run's BPR
	// is built with; v/c scales inversely with it. A non-positive value falls back to
	// 1.0 (the saturated reference scale).
	CapacityScale float64
}

// AlgoTick is one algorithm's enriched per-tick frame: the simulator's immutable
// TickState plus the per-algo running metrics §R6 requires (compute time — "fastest
// to route" — and realized total network time / PoA — "best at minimizing
// traffic"). It is the unit the orchestrator forwards to its consumer; the
// WebSocket layer turns a stream of these into snapshot/delta frames.
//
// Every field is a value or freshly-allocated slice owned by the consumer — the
// orchestrator never retains or mutates an already-forwarded AlgoTick — so a
// consumer may keep it across ticks without aliasing a sim's live state.
type AlgoTick struct {
	// Algo is the router name (one of benchmark.RouterOrder) this frame belongs to.
	Algo string

	// State is the simulator's immutable per-tick snapshot (clock, in-flight/
	// completed counts, per-edge Load and VC). The WebSocket layer maps State.VC to
	// segment_id-keyed buckets.
	State TickState

	// ComputeNanos is the CUMULATIVE wall-clock nanoseconds this algorithm has spent
	// inside router.Route across all ticks so far — the "fastest to route" metric.
	// It is measured around the simulator's per-tick route fan-out (wrapped router),
	// so it is the real routing cost the algorithm incurs as the run progresses, not
	// a synthetic figure.
	ComputeNanos int64

	// RealizedTotalS is the realized TOTAL network time (seconds) at this tick: the
	// sum over edges of BPR.Cost(edge, load) × load at the tick's per-edge load,
	// matching routing.TotalNetworkTime exactly (the #89/#90 evaluator). It is the
	// "minimizing traffic" magnitude — and the input the consumer divides to form the
	// per-tick Price of Anarchy.
	//
	// PoA IS NOT CARRIED HERE BY DESIGN. PoA is a RELATIONSHIP between this algo's
	// realized total and the systemoptimal algo's realized total AT THE SAME TICK
	// (poa.go), and systemoptimal runs on its own goroutine — so computing PoA inside
	// the live observer would divide against whatever systemoptimal total happened to
	// be published first, which is cross-goroutine TIMING-DEPENDENT and therefore not
	// deterministic. Instead the orchestrator emits each algo's deterministic
	// per-tick RealizedTotalS, and the consumer pairs same-tick totals to compute PoA
	// (PriceOfAnarchy) in its own single-threaded, ordered pass — see
	// internal/api/stream.go and PoAFromTotals. This keeps the byte-identical-trace
	// determinism criterion intact.
	RealizedTotalS float64
}

// AlgoTickFunc is the orchestrator's consumer callback, invoked once per (algorithm,
// tick) in that sim's tick order. It runs on the algorithm's simulation goroutine
// and BLOCKS that sim until it returns, so it MUST NOT do blocking I/O — buffer and
// flush off the hot path (the WebSocket layer does exactly this).
type AlgoTickFunc func(tick AlgoTick)

// PoAFromTotals computes a per-tick Price of Anarchy from an algorithm's realized
// total and the systemoptimal algorithm's realized total AT THE SAME TICK, via
// PriceOfAnarchy (so a degenerate/zero reference yields 1, never NaN/Inf). It is the
// deterministic seam the consumer uses to pair same-tick totals — keeping PoA a
// pure function of two already-deterministic per-tick totals, with no cross-goroutine
// timing in the result. systemoptimal's own PoA against itself is 1 by construction.
func PoAFromTotals(algoTotal, systemOptimalTotal float64) float64 {
	return PriceOfAnarchy(algoTotal, systemOptimalTotal)
}

// timedRouter wraps a routing.Router to accumulate the cumulative wall-clock time
// spent in Route — the per-algo compute-time metric. The simulator calls Route once
// per newly-released request via the per-tick fan-out (concurrently within a tick),
// so the counter is guarded by a mutex; the contention is limited to one tick's
// release batch and is off the model's critical advance path.
type timedRouter struct {
	inner routing.Router
	mu    *sync.Mutex
	nanos *int64
}

func (t timedRouter) Route(ctx context.Context, req routing.RouteRequest) (routing.Route, error) {
	start := time.Now()
	route, err := t.inner.Route(ctx, req)
	elapsed := time.Since(start).Nanoseconds()
	t.mu.Lock()
	*t.nanos += elapsed
	t.mu.Unlock()
	return route, err
}

func (t timedRouter) Assign(ctx context.Context, reqs []routing.RouteRequest) ([]routing.Route, error) {
	return t.inner.Assign(ctx, reqs)
}

func (t timedRouter) AssignResult(ctx context.Context, reqs []routing.RouteRequest) (routing.AssignResult, error) {
	return t.inner.AssignResult(ctx, reqs)
}

func (t timedRouter) Name() string { return t.inner.Name() }

// RunParallel runs ALL SIX routers (RouterOrder) concurrently over the §R5
// mesoscopic simulator from cfg.StartTime, forwarding each algorithm's enriched
// per-tick AlgoTick to emit. It blocks until every sim has finished (drained or hit
// MaxTicks) or ctx is cancelled, and returns the first error any sim produced.
//
// All six sims share the SAME deterministic OD set (GenerateODSet with cfg.Seed) and
// the SAME immutable graph and BPR, so the streams are directly comparable and the
// run is reproducible. Each sim runs on its own goroutine with no shared mutable
// state except the lock-free poaRef and per-algo compute counters; emit is the only
// thing that crosses back to the consumer.
//
// emit is called on the per-algo simulation goroutines (NOT serialized across algos),
// so a consumer that needs a single-threaded view must funnel the callbacks itself
// (the WebSocket layer sends each onto its per-algo buffered channel). emit must not
// block — see AlgoTickFunc.
func RunParallel(ctx context.Context, g graph.Graph, cfg ParallelConfig, emit AlgoTickFunc) error {
	count := cfg.Count
	if count <= 0 {
		count = DefaultODCount
	}
	capacityScale := cfg.CapacityScale
	if capacityScale <= 0 {
		capacityScale = 1.0
	}
	bpr := cost.NewBPR(0.15, 4, capacityScale)

	// One shared, deterministic OD set every router replays — the comparability +
	// reproducibility anchor. The label is fixed (the parallel run is a single
	// scenario, not a sweep) and the demand is the canonical SweepDemandVPH so the
	// realized magnitudes line up with the sweep's reference scale.
	set := GenerateODSet(g, cfg.Seed, count, "parallel", 1.0, SweepDemandVPH)
	reqs := set.RouteRequests()

	simCfg := SimConfig{
		StartTime:   cfg.StartTime,
		TickSeconds: cfg.TickSeconds,
		MaxTicks:    cfg.MaxTicks,
		BPR:         bpr,
	}

	var wg sync.WaitGroup
	errs := make([]error, len(RouterOrder))
	for i, name := range RouterOrder {
		wg.Add(1)
		go func(i int, name string) {
			defer wg.Done()
			errs[i] = runOneAlgo(ctx, g, bpr, reqs, simCfg, name, cfg.Seed, emit)
		}(i, name)
	}
	wg.Wait()

	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

// runOneAlgo runs a single algorithm's mesoscopic simulation, wrapping its router so
// compute time is measured and installing a TickObserver that computes the tick's
// realized total / PoA and forwards an AlgoTick to emit. It is the per-goroutine body
// RunParallel fans out.
//
// The router factory builds the named router fresh per tick over the tick's frozen
// provider (the simulator's seam), wrapped by timedRouter so EVERY Route call across
// every tick accumulates into one cumulative compute counter for the algorithm. The
// counter is shared via pointer so the observer reads the running total each tick.
func runOneAlgo(
	ctx context.Context,
	g graph.Graph,
	bpr cost.BPR,
	reqs []routing.RouteRequest,
	simCfg SimConfig,
	name string,
	seed int64,
	emit AlgoTickFunc,
) error {
	var computeMu sync.Mutex
	var computeNanos int64

	factory := func(provider congestion.CongestionProvider) routing.Router {
		inner, err := buildSimRouter(name, g, bpr, provider, seed)
		if err != nil {
			// A bad name cannot occur (RouterOrder is fixed), but if it did the
			// simulator would route against a nil router and error; surface a naive
			// router so the run fails cleanly on the next route rather than panicking.
			inner = routing.NewNaiveRouter(g)
		}
		return timedRouter{inner: inner, mu: &computeMu, nanos: &computeNanos}
	}

	observer := func(state TickState) {
		// Realized total network time at this tick's per-edge load — exactly the
		// #89/#90 evaluator (routing.TotalNetworkTime over the load vector). This is
		// deterministic per (seed, tick); the consumer pairs it with systemoptimal's
		// same-tick total to derive PoA (PoAFromTotals).
		realizedTotal := routing.TotalNetworkTime(g, bpr, state.Load)

		computeMu.Lock()
		nanos := computeNanos
		computeMu.Unlock()

		emit(AlgoTick{
			Algo:           name,
			State:          state,
			ComputeNanos:   nanos,
			RealizedTotalS: realizedTotal,
		})
	}

	_, err := Simulate(ctx, g, reqs, simCfg, factory, observer)
	return err
}

// buildSimRouter builds the named single-shot router the simulator drives per tick,
// given the tick's frozen congestion provider. It mirrors buildRouter (the sweep's
// builder) but threads the LIVE per-tick provider into the reactive router so each
// tick best-responds to the congestion built up so far — the time-domain behavior
// the parallel run animates. The iterative routers (incremental/msa/systemoptimal/
// multipath) are exercised through the simulator's single-request Route path only
// (the simulator never calls AssignResult), so in the time domain they behave as
// their per-request shortest-path under the current load — which is the honest
// "how each strategy routes a freshly-released request against current congestion"
// the live view shows.
func buildSimRouter(name string, g graph.Graph, bpr cost.BPR, provider congestion.CongestionProvider, seed int64) (routing.Router, error) {
	switch name {
	case "naive":
		return routing.NewNaiveRouter(g), nil
	case "reactive":
		return routing.NewReactiveRouter(g, bpr, provider), nil
	case "incremental":
		return routing.NewIncrementalRouter(g, bpr), nil
	case "msa":
		return routing.NewMSARouter(g, bpr), nil
	case "systemoptimal":
		return routing.NewSystemOptimalRouter(g, bpr), nil
	case "multipath":
		return routing.NewMultipathRouter(g, bpr, seed, sweepK), nil
	default:
		return nil, fmt.Errorf("benchmark: unknown router %q", name)
	}
}
