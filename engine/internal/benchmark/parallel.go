package benchmark

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/congestion"
	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/congestion/static"
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
// HONESTY NOTE (#112). The per-tick stream is driven by the §R5 mesoscopic simulator,
// which ONLY ever calls the single-request router.Route path — it never runs the
// iterative AssignResult/MSA/equilibrium loop. So the "systemoptimal", "msa",
// "incremental" and "multipath" streams here are per-request BEST-RESPONSE VARIANTS
// against each tick's frozen snapshot, NOT the real iterative routers. Their per-tick
// RealizedTotalS is therefore an honest mesoscopic congestion magnitude, but it is
// NOT a system optimum and MUST NOT be used as a Price-of-Anarchy denominator (doing
// so was the #112 "six flavors of greedy" bug). The true PoA reference is instead
// StaticPoA below — computed once from the real iterative AssignResult over the same
// OD set, exactly as the static /benchmark sweep does.
//
// Every field is a value or freshly-allocated slice owned by the consumer — the
// orchestrator never retains or mutates an already-forwarded AlgoTick — so a
// consumer may keep it across ticks without aliasing a sim's live state.
type AlgoTick struct {
	// Algo is the router name (one of benchmark.RouterOrder) this frame belongs to.
	// In the stream it identifies the BEST-RESPONSE variant of that router (see the
	// honesty note above), not the iterative router of the same name.
	Algo string

	// State is the simulator's immutable per-tick snapshot (clock, in-flight/
	// completed counts, per-edge Load and VC). The WebSocket layer maps State.VC to
	// segment_id-keyed buckets.
	State TickState

	// ComputeNanos is the CUMULATIVE wall-clock nanoseconds this algorithm has spent
	// inside router.Route across all ticks so far — kept ON THE WIRE FOR BACK-COMPAT
	// (compute_ms). It is a SUM over the concurrent per-tick route fan-out, so it
	// over-counts overlapping concurrent Route calls and jitters run-to-run under the
	// six-sim CPU contention; prefer RouteMedianNanos below as the fair figure (#112).
	ComputeNanos int64

	// RouteMedianNanos is the MEDIAN wall-clock nanoseconds of a SINGLE router.Route
	// call so far this run — the fair per-route compute metric (#112). Unlike
	// ComputeNanos it is a per-CALL figure (it divides out how many routes/ticks/algos
	// ran), so it is stable run-to-run and does not grow with the concurrent fan-out.
	// The median (not the mean) makes it robust to GC/scheduler outliers. It is
	// accumulated in this algo's OWN per-route sample buffer, so the measurement never
	// contends across the six concurrent algos.
	RouteMedianNanos int64

	// RealizedTotalS is the realized TOTAL network time (seconds) at this tick: the
	// sum over edges of BPR.Cost(edge, load) × load at the tick's per-edge load,
	// matching routing.TotalNetworkTime exactly (the #89/#90 evaluator). It is the
	// live "minimizing traffic" magnitude of this BEST-RESPONSE variant as congestion
	// builds and drains — NOT a PoA numerator (see the honesty note and StaticPoA).
	RealizedTotalS float64

	// StaticPoA is the TRUE static-equilibrium Price of Anarchy of this algo versus
	// the system-optimal reference, computed ONCE per run from the real iterative
	// AssignResult over the SAME shared OD set + BPR — the exact computation the
	// static /benchmark sweep performs (sweep.go), so the live PoA matches /benchmark
	// for a fixed (start, seed). It is CONSTANT across this algo's ticks (an
	// equilibrium is a property of the OD set, not of a mesoscopic instant) and is
	// ≥ 1 by construction (systemoptimal minimizes the total, so SO ≤ every other
	// total — the SO ≤ UE invariant). It replaces the former per-tick greedy pairing
	// as the honest live PoA reference.
	StaticPoA float64
}

// AlgoTickFunc is the orchestrator's consumer callback, invoked once per (algorithm,
// tick) in that sim's tick order. It runs on the algorithm's simulation goroutine
// and BLOCKS that sim until it returns, so it MUST NOT do blocking I/O — buffer and
// flush off the hot path (the WebSocket layer does exactly this).
type AlgoTickFunc func(tick AlgoTick)

// timedRouter wraps a routing.Router to measure the per-algo compute cost. Each
// Route call's individual wall-clock latency is recorded so the observer can report
// BOTH the back-compat cumulative sum (nanos) AND the fair per-route median
// (samples). The simulator calls Route once per newly-released request via the
// per-tick fan-out (concurrently within a tick), so the two accumulators are guarded
// by a mutex; the contention is limited to one tick's release batch, is per-ALGO
// (each sim owns its own mu/nanos/samples — nothing is shared across the six
// concurrent algos), and is off the model's critical advance path.
type timedRouter struct {
	inner   routing.Router
	mu      *sync.Mutex
	nanos   *int64   // cumulative sum of Route latencies (back-compat compute_ms)
	samples *[]int64 // per-Route latencies, reduced to a median by the observer
}

func (t timedRouter) Route(ctx context.Context, req routing.RouteRequest) (routing.Route, error) {
	start := time.Now()
	route, err := t.inner.Route(ctx, req)
	elapsed := time.Since(start).Nanoseconds()
	t.mu.Lock()
	*t.nanos += elapsed
	*t.samples = append(*t.samples, elapsed)
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

	// The TRUE live PoA reference (#112): a static-equilibrium PoA per algo computed
	// ONCE over the shared OD set via the real iterative AssignResult — the same
	// machinery the static /benchmark sweep uses (sweep.go) — rather than the in-stream
	// best-response "systemoptimal", which never runs its iterative loop. This is the
	// cheap, safe fix: six static assignments up front (bounded, deterministic) instead
	// of embedding an iterative optimum inside the per-tick mesoscopic loop. It runs
	// BEFORE the sims so every algo's stream carries the honest, constant PoA.
	staticPoA, err := staticPoARef(ctx, g, bpr, reqs, cfg.Seed)
	if err != nil {
		return err
	}

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
			errs[i] = runOneAlgo(ctx, g, bpr, reqs, simCfg, name, cfg.Seed, staticPoA[name], emit)
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
	staticPoA float64,
	emit AlgoTickFunc,
) error {
	var computeMu sync.Mutex
	var computeNanos int64
	var computeSamples []int64

	factory := func(provider congestion.CongestionProvider) routing.Router {
		// buildRouter is the single six-way constructor shared with the sweep; here it
		// is threaded the LIVE per-tick provider so the reactive router best-responds to
		// the congestion built up so far (the sweep passes a frozen free-flow snapshot).
		inner, err := buildRouter(name, g, bpr, provider, seed)
		if err != nil {
			// A bad name cannot occur (RouterOrder is fixed and buildRouter covers every
			// entry), but if it did the simulator would route against a nil router and
			// error; surface a naive router so the run fails cleanly on the next route
			// rather than panicking.
			inner = routing.NewNaiveRouter(g)
		}
		return timedRouter{inner: inner, mu: &computeMu, nanos: &computeNanos, samples: &computeSamples}
	}

	observer := func(state TickState) {
		// Realized total network time at this tick's per-edge load — exactly the
		// #89/#90 evaluator (routing.TotalNetworkTime over the load vector). This is
		// the live best-response congestion magnitude, deterministic per (seed, tick);
		// it is NOT a PoA numerator (StaticPoA carries the honest PoA — see AlgoTick).
		realizedTotal := routing.TotalNetworkTime(g, bpr, state.Load)

		computeMu.Lock()
		nanos := computeNanos
		routeMedian := medianNanos(computeSamples)
		computeMu.Unlock()

		emit(AlgoTick{
			Algo:             name,
			State:            state,
			ComputeNanos:     nanos,
			RouteMedianNanos: routeMedian,
			RealizedTotalS:   realizedTotal,
			StaticPoA:        staticPoA,
		})
	}

	_, err := Simulate(ctx, g, reqs, simCfg, factory, observer)
	return err
}

// staticPoARef computes the true static-equilibrium Price of Anarchy of each router
// (RouterOrder) against the system-optimal reference, over the shared OD set — the
// honest live PoA reference (#112). It reuses the static sweep's machinery exactly:
// build each router with buildRouter, run its real iterative AssignResult, and take
// routing.TotalNetworkTime over the resulting FinalFlows; the systemoptimal total is
// the denominator. Because the OD set + BPR here are byte-identical to a static
// /benchmark run at the same (seed, count, capacity scale), the returned PoA matches
// /benchmark for the same OD set. It is deterministic (the routers produce
// byte-identical FinalFlows for a fixed OD set) and every entry is ≥ 1 by
// construction (SO minimizes the total ⇒ SO ≤ UE), so a caller can rely on the
// SO ≤ UE invariant holding for the live reference.
func staticPoARef(ctx context.Context, g graph.Graph, bpr cost.BPR, reqs []routing.RouteRequest, seed int64) (map[string]float64, error) {
	totals := make(map[string]float64, len(RouterOrder))
	for _, name := range RouterOrder {
		// A frozen all-zero-load snapshot: reactive reads it as a free-flow snapshot;
		// the other routers ignore the provider — the same wiring RunSweep uses.
		router, err := buildRouter(name, g, bpr, static.NewFromSnapshot(nil), seed)
		if err != nil {
			return nil, err
		}
		res, err := router.AssignResult(ctx, reqs)
		if err != nil {
			return nil, fmt.Errorf("parallel: static PoA reference router %q: %w", name, err)
		}
		totals[name] = routing.TotalNetworkTime(g, bpr, res.FinalFlows)
	}
	soTotal := totals["systemoptimal"]
	poa := make(map[string]float64, len(RouterOrder))
	for name, total := range totals {
		poa[name] = PriceOfAnarchy(total, soTotal)
	}
	return poa, nil
}

// medianNanos returns the median of the per-Route latency samples (nanoseconds), the
// fair per-route compute figure (#112). It is a per-CALL statistic — robust to the
// GC/scheduler outliers a cumulative sum accumulates, and independent of how many
// routes/ticks/algos ran — so it is stable run-to-run. An empty sample set (no route
// yet) is 0. It copies before sorting so it never mutates the live sample buffer.
func medianNanos(samples []int64) int64 {
	if len(samples) == 0 {
		return 0
	}
	sorted := append([]int64(nil), samples...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	n := len(sorted)
	if n%2 == 1 {
		return sorted[n/2]
	}
	return (sorted[n/2-1] + sorted[n/2]) / 2
}
