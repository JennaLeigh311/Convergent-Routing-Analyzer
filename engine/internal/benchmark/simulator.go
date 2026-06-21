package benchmark

import (
	"context"
	"sort"
	"time"

	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/congestion"
	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/cost"
	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/domain"
	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/graph"
	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/routing"
)

// simulator.go is the discrete-time MESOSCOPIC simulator (project-spec.md §R5):
// the time-domain counterpart to the Phase-3 static-equilibrium assignment the
// iterative routers produce. Where a static assignment answers "if every request
// departed at once and the flow settled, what would the network look like", this
// simulator answers the question §R5 actually requires: requests DEPART ON A
// CLOCK, congestion BUILDS as the peak fills and DISSIPATES as it drains, and the
// travel time a driver experiences depends on WHEN they were on each edge. That
// time-evolution is what makes the "1,000 requests over a rush hour" narrative
// defensible, and it is the data source the Phase-5 live frontend animates
// (project-outline.md "Functionality clarification"; this issue, #90).
//
// It is DELIBERATELY DISTINCT from internal/congestion/simulator (the simple
// injector that just perturbs loads per tick with an RNG, NOT a vehicle model):
// this one moves individual vehicles along their routed paths at BPR-derived
// speeds and derives the per-edge load from who is actually on each edge. The two
// share the name "simulator" but model different things; this is the §R5 one.
//
// The loop, per Δt tick (Run):
//
//  1. RELEASE every request whose DepartAt ≤ the sim clock and is not yet departed,
//     in a DETERMINISTIC order (departure time, then input index).
//  2. ROUTE each newly-released request against the CURRENT per-edge load — a SINGLE
//     immutable congestion snapshot value taken once for the tick, so every request
//     released this tick sees an identical, stable view (the §R5 per-round snapshot
//     discipline). Routing fans out across goroutines, each worker owning its own
//     state; there is NO shared mutable map under a lock.
//  3. ADVANCE every in-flight vehicle along its path: distance += speed × Δt, where
//     speed is the BPR-derived edge speed edge.LengthM / BPR.Cost(edge, load); the
//     remainder carries across edge boundaries within the tick.
//  4. RE-DERIVE the per-edge load from who is on each edge AFTER the advance, so the
//     next tick routes and moves against the load this tick produced.
//  5. RECORD each vehicle's realized experienced travel time the tick it completes.
//
// Determinism (§R5): the OD set is processed in a fixed sorted order, the per-tick
// congestion snapshot is one immutable value, the sharded route accumulation is
// reduced once per tick in fixed worker-then-edge order (mirroring routing's
// combineFlows), and the per-tick state stream is emitted in clock order. A fixed
// seed plus a serialized OD set therefore yields a BYTE-IDENTICAL tick-by-tick
// trace run to run, and the loop is `go test -race` clean.

// defaultTickSeconds is the simulator's default Δt: 30 simulated seconds per tick
// (§R5 "Δt≈30s"). A tick is the granularity at which requests are released, loads
// are re-derived, and the per-tick state stream emits — fine enough that congestion
// builds and drains smoothly, coarse enough that an hour-long peak is a few hundred
// ticks rather than tens of thousands.
const defaultTickSeconds = 30.0

// SimConfig configures one mesoscopic simulation run. The zero value is NOT usable
// as-is — TickSeconds defaults to defaultTickSeconds when non-positive and the BPR
// must be a constructed one (cost.DefaultBPR) — so callers build it explicitly.
type SimConfig struct {
	// StartTime is the time-of-day/date origin of the sim clock: the wall-clock
	// instant tick 0 corresponds to. Each request's DepartAt (seconds-from-start)
	// is interpreted RELATIVE to this origin, and every per-tick state snapshot
	// stamps its SimTime as StartTime + elapsed. Shifting StartTime shifts the
	// whole run's clock observably (the §R5 / #93 "time-of-day slider sets the
	// start clock" requirement) without changing the relative dynamics — it is the
	// label the frontend renders and the demand window the live view is anchored
	// to. The zero Time is a valid origin (it simply labels tick 0 as the zero
	// instant); a caller animating a rush hour passes e.g. 08:00 of a chosen date.
	StartTime time.Time

	// TickSeconds is Δt, the simulated seconds advanced per tick. A non-positive
	// value is replaced by defaultTickSeconds (≈30s, §R5) so a zero-value config
	// still runs; a caller wanting a finer/coarser clock sets it explicitly.
	TickSeconds float64

	// MaxTicks bounds the run so a vehicle that can never complete (a degenerate
	// route, an absurd Δt) cannot spin forever. When 0 it defaults to a generous
	// derivedMaxTicks based on the latest departure; set it explicitly to cap a
	// run shorter. Reaching MaxTicks with vehicles still in flight is reported via
	// SimResult.Completed < total, never a hang.
	MaxTicks int

	// BPR is the cost function speeds and the time-weighted realized times are
	// derived from. Pass a constructed BPR (cost.DefaultBPR); the zero BPR is not
	// usable (its zero CapacityScale collapses every edge to free-flow).
	BPR cost.BPR
}

// RouterFactory builds the routing strategy the simulator routes a tick's newly-
// released requests with, GIVEN that tick's frozen congestion provider. It is the
// router-agnostic seam: the factory receives a read-only provider holding the
// tick's immutable per-edge load snapshot and returns a routing.Router that routes
// against it. A reactive/BPR router (ReactiveBPRFactory) routes against the load so
// congestion visibly reshapes paths as the peak fills; a naive router ignores the
// provider and routes free-flow. The factory is called ONCE per tick over a fresh
// frozen provider, never sharing mutable state across ticks.
type RouterFactory func(provider congestion.CongestionProvider) routing.Router

// ReactiveBPRFactory is the default RouterFactory: it builds a routing.ReactiveRouter
// that best-responds to the tick's frozen load under the given BPR. Each tick's
// newly-released requests are therefore routed against the congestion that has
// built up so far — the behavior §R5's "newly-departed requests route against
// current loads" calls for. The returned factory closes over bpr only (immutable),
// so it is safe to reuse across runs.
func ReactiveBPRFactory(roadGraph graph.Graph, bpr cost.BPR) RouterFactory {
	return func(provider congestion.CongestionProvider) routing.Router {
		return routing.NewReactiveRouter(roadGraph, bpr, provider)
	}
}

// TickState is one immutable per-tick state snapshot emitted to the per-tick seam
// (the Phase-5 #93 WebSocket plug point). It carries the sim clock instant plus the
// per-edge load (vehicles/hour) and v/c ratio AFTER this tick's advance, so a
// consumer can stream or record the network's evolving congestion frame by frame.
// Every field is a caller-owned value or freshly-allocated slice — the simulator
// never retains or mutates an already-emitted TickState — so a consumer may keep it
// across ticks without aliasing the simulator's live state.
type TickState struct {
	// Tick is the 1-based tick index (tick 1 is the first Δt advance). It is the
	// stable ordinal a recorder keys frames on.
	Tick int

	// SimTime is the wall-clock instant this tick represents: StartTime advanced by
	// Tick × Δt. It is what the frontend's time-of-day display renders; shifting
	// SimConfig.StartTime shifts every TickState.SimTime by the same offset.
	SimTime time.Time

	// InFlight is the number of vehicles on the network at the end of this tick
	// (released and not yet completed). It is the headline "how full is the peak"
	// number the live view plots over time.
	InFlight int

	// Completed is the cumulative number of vehicles that have finished their trip
	// as of this tick. Completed rises monotonically; (released − Completed) is
	// InFlight.
	Completed int

	// Load is the per-edge load in vehicles/hour at the end of this tick, indexed
	// by EdgeID with length graph.EdgeCount() — a fresh copy, safe to retain. It is
	// the field the WebSocket streams as the animated congestion layer.
	Load congestion.LoadSnapshot

	// VC is the per-edge v/c ratio at the end of this tick, indexed by EdgeID
	// (length graph.EdgeCount()), computed exactly as the evaluator defines v/c
	// (effective capacity = CapacityVPH × BPR.CapacityScale; a non-positive-capacity
	// edge is 0, never +Inf). It is the color scale the frontend maps to congestion.
	VC []float64
}

// TickObserver receives each tick's immutable TickState as the simulation advances
// — the per-tick state seam (§R5; Phase-5 #93). It is a synchronous callback rather
// than a channel so the seam has no buffering/lifecycle of its own to get wrong and
// stays deterministic: the simulator calls observer exactly once per tick, in
// strict tick order, on the simulation goroutine, before advancing the clock. A
// channel-based consumer wraps this trivially (send ts on a channel inside the
// callback); a recorder appends ts to a slice. A nil observer means "no per-tick
// stream" — the run still produces its end-of-run SimResult.
type TickObserver func(state TickState)

// SimResult is the outcome of one mesoscopic run: the TIME-WEIGHTED realized-time
// aggregates plus the run's bookkeeping. Its mean/p95 are DISTINCT from the Phase-3
// static-equilibrium one-shot number and MUST be read and labeled as such (the §R5
// honesty distinction): the static number assumes every request is simultaneously
// present at the converged flow, whereas these are the times vehicles ACTUALLY
// experienced as congestion built and drained over the run. On a congested toy
// demand the two differ — that difference is the whole point of modeling time.
//
// All fields are finite and defined on the EMPTY batch (no requests ⇒ zero
// completions ⇒ mean/p95 default to 0 via the reused evaluator helpers, Ticks 0),
// so a SimResult is always safe to serialize and render.
type SimResult struct {
	// MeanRealizedS is the TIME-WEIGHTED mean realized travel time over the run, in
	// seconds: the mean of every completed vehicle's experienced trip time. It is
	// computed with the #89 evaluator's mean helper over the realized times the
	// simulator accumulated — NOT re-derived — so it shares the empty-batch contract.
	// It is labeled time-weighted to keep it explicitly distinct from the static
	// MeanRealizedS in benchmark.Result.
	MeanRealizedS float64

	// P95RealizedS is the TIME-WEIGHTED 95th-percentile realized travel time, by the
	// evaluator's nearest-rank percentileNearestRank — the unlucky driver in the
	// peak. Like the mean it is the over-the-run experienced time, distinct from the
	// static p95.
	P95RealizedS float64

	// Completed is the number of vehicles that finished within the run. Equal to the
	// request count on a run that drains fully; less if MaxTicks cut the run short
	// with vehicles still in flight.
	Completed int

	// Released is the number of requests that departed (DepartAt ≤ the final clock).
	// Released − Completed vehicles were still in flight when the run ended.
	Released int

	// Ticks is the number of ticks the run advanced. Ticks × TickSeconds is the
	// simulated wall-clock span.
	Ticks int

	// TickSeconds is the Δt the run used (the resolved value after the non-positive
	// default), echoed so a consumer can convert ticks to seconds without re-deriving.
	TickSeconds float64
}

// vehicle is one in-flight (or completed) trip: its routed path, how far it has
// advanced, and the experienced-time bookkeeping. It is private simulation state,
// never exposed; the per-tick seam exposes only aggregate load, and SimResult
// exposes only the realized-time aggregates.
type vehicle struct {
	// requestIndex is the vehicle's stable index into the sorted request order, the
	// determinism key for any per-vehicle iteration.
	requestIndex int

	// weight is the request's flow contribution (vehicles/hour) on every edge it
	// occupies — requestWeight semantics surfaced into the sim so a request can
	// stand for many vehicles. Defaults to 1 for an unset/negative weight.
	weight float64

	// edges is the routed path (ordered EdgeIDs). An empty path is an origin ==
	// destination trip: it completes the tick it is released with zero travel time.
	edges []domain.EdgeID

	// edgeIndex is the index into edges of the edge the vehicle is currently on; it
	// equals len(edges) once the vehicle has completed.
	edgeIndex int

	// distOnEdge is how far (meters) the vehicle has advanced along its current edge.
	distOnEdge float64

	// experiencedS is the travel time the vehicle has accumulated so far (seconds),
	// the sum over the portions of each tick it spent moving. On completion it is the
	// realized travel time recorded for the run's time-weighted aggregates.
	experiencedS float64

	// done is true once the vehicle has traversed its whole path; a done vehicle is
	// skipped by the advance and contributes no load.
	done bool
}

// Simulate runs one discrete-time mesoscopic simulation over reqs and returns the
// time-weighted SimResult, streaming each tick's state to observer (nil for no
// stream). It is the package's §R5 entry point — the time-domain counterpart to
// benchmark.Evaluate's static metrics.
//
// reqs is the departure-time OD set: each request's DepartAt (seconds from
// cfg.StartTime) sets when it is released and its Weight its per-edge flow
// contribution. The set is processed in a fixed sorted order (DepartAt, then the
// caller's input index) so the run is deterministic regardless of the input slice's
// order; a fixed seed + serialized OD set yields a byte-identical tick trace.
//
// routerFactory builds the routing strategy each tick (ReactiveBPRFactory for the
// congestion-aware default). It is called once per tick over that tick's frozen
// provider; a nil factory is a usage error and returns an error rather than panicking.
//
// Degenerate inputs are defined, never a panic: an empty/nil reqs runs zero ticks
// and returns an all-zero-but-finite SimResult; an origin == destination request
// completes immediately with zero travel time; a request that snaps to no node or is
// unreachable surfaces the router's error (the run stops and returns it), matching
// the routers' on-first-error contract. A cancelled ctx stops the run promptly with
// ctx.Err().
func Simulate(
	ctx context.Context,
	roadGraph graph.Graph,
	reqs []routing.RouteRequest,
	cfg SimConfig,
	routerFactory RouterFactory,
	observer TickObserver,
) (SimResult, error) {
	if routerFactory == nil {
		return SimResult{}, errNilRouterFactory
	}
	tickSeconds := cfg.TickSeconds
	if tickSeconds <= 0 {
		tickSeconds = defaultTickSeconds
	}

	// Process the OD set in a fixed sorted order (DepartAt, then input index) so the
	// release order — and therefore the whole trace — does not depend on how the
	// caller ordered reqs. This is the "serialized OD set ⇒ deterministic" guarantee.
	order := sortedRequestOrder(reqs)

	edgeCount := roadGraph.EdgeCount()
	maxTicks := cfg.MaxTicks
	if maxTicks <= 0 {
		maxTicks = derivedMaxTicks(reqs, tickSeconds)
	}

	// pending walks the sorted order releasing requests as the clock reaches their
	// DepartAt; nextPending is the index of the earliest not-yet-released request.
	nextPending := 0
	inFlight := make([]*vehicle, 0, len(reqs))
	released := 0
	completed := 0

	// realized collects each completed vehicle's experienced travel time, in
	// completion order, for the time-weighted aggregates. It is the input the reused
	// #89 evaluator helpers (mean, percentileNearestRank) reduce — NOT re-derived here.
	realized := make([]float64, 0, len(reqs))

	// load is the live per-edge load store (vehicles/hour). It is re-derived from the
	// in-flight vehicles' occupancy each tick (loadFromVehicles) and never mutated
	// concurrently — only the single simulation goroutine writes it, between the
	// concurrent route fan-out and the next tick.
	load := congestion.NewLoadStore(edgeCount)

	for tick := 1; tick <= maxTicks; tick++ {
		if err := ctx.Err(); err != nil {
			return SimResult{}, err
		}

		clockSeconds := float64(tick-1) * tickSeconds // clock at the START of this tick

		// (a) RELEASE: every request whose DepartAt ≤ the current clock, in sorted
		// order. Collect them so this tick's whole batch routes against ONE snapshot.
		releaseStart := nextPending
		for nextPending < len(order) && reqs[order[nextPending]].DepartAt <= clockSeconds {
			nextPending++
		}
		newlyReleased := order[releaseStart:nextPending]

		// (b) ROUTE the newly-released batch against the CURRENT load — ONE immutable
		// snapshot value for the whole tick (frozenProvider). Every request released
		// this tick sees the identical, stable view (§R5 per-round snapshot). The
		// fan-out owns per-worker state; no shared mutable map under a lock.
		if len(newlyReleased) > 0 {
			provider := frozenProvider{snapshot: load.Snapshot()}
			router := routerFactory(provider)
			routes, err := routeBatchConcurrent(ctx, router, reqs, newlyReleased)
			if err != nil {
				return SimResult{}, err
			}
			for i, reqIndex := range newlyReleased {
				inFlight = append(inFlight, &vehicle{
					requestIndex: reqIndex,
					weight:       requestWeight(reqs[reqIndex]),
					edges:        routes[i].Edges,
				})
			}
			released += len(newlyReleased)
		}

		// (c) ADVANCE every in-flight vehicle along its path at BPR-derived speed, and
		// (e) RECORD the realized time of any that complete this tick. advance reads the
		// load this tick STARTED with (the same snapshot routing saw), so a vehicle's
		// speed on an edge reflects the congestion present when it traversed it.
		stillInFlight := inFlight[:0]
		for _, veh := range inFlight {
			advance(veh, roadGraph, cfg.BPR, load, tickSeconds)
			if veh.done {
				realized = append(realized, veh.experiencedS)
				completed++
				continue
			}
			stillInFlight = append(stillInFlight, veh)
		}
		inFlight = stillInFlight

		// (d) RE-DERIVE the per-edge load from who is on each edge AFTER the advance,
		// so the NEXT tick routes and moves against the load this tick produced. This
		// single-goroutine write happens after the route fan-out has fully joined.
		loadFromVehicles(load, inFlight)

		// Emit this tick's immutable state to the per-tick seam (Phase-5 #93) in
		// strict tick order, after the load is settled for the tick.
		if observer != nil {
			observer(buildTickState(tick, cfg, tickSeconds, len(inFlight), completed, load, roadGraph))
		}

		// Stop early once the network has drained AND there is nothing left to release
		// — the run is complete; running to maxTicks would only emit empty ticks.
		if len(inFlight) == 0 && nextPending >= len(order) {
			return SimResult{
				MeanRealizedS: mean(realized),
				P95RealizedS:  percentileNearestRank(realized, 0.95),
				Completed:     completed,
				Released:      released,
				Ticks:         tick,
				TickSeconds:   tickSeconds,
			}, nil
		}
	}

	// Reached the tick budget with work outstanding: report honestly (Completed <
	// Released possible), never hang.
	return SimResult{
		MeanRealizedS: mean(realized),
		P95RealizedS:  percentileNearestRank(realized, 0.95),
		Completed:     completed,
		Released:      released,
		Ticks:         maxTicks,
		TickSeconds:   tickSeconds,
	}, nil
}

// advance moves one vehicle along its path by one tick of Δt seconds, at the
// BPR-derived speed of each edge it is on under the current load. It carries the
// remainder of the tick across edge boundaries: a vehicle that finishes an edge
// part-way through the tick immediately continues onto the next edge with the time
// it has left, so a short edge does not waste a whole tick. The speed of an edge is
// edge.LengthM / BPR.Cost(edge, load) (m/s) — the §R5 BPR-derived speed; a zero/
// missing/zero-length edge is treated as instantly traversed (it contributes no
// travel time and is stepped over) so a degenerate edge never stalls a vehicle.
//
// An empty path (origin == destination) completes immediately with zero experienced
// time. experiencedS accumulates only the time actually spent moving, so it is the
// realized travel time independent of how many ticks the vehicle waited in flight.
func advance(veh *vehicle, roadGraph graph.Graph, bpr cost.BPR, load *congestion.LoadStore, tickSeconds float64) {
	if veh.done {
		return
	}
	remaining := tickSeconds // seconds of this tick the vehicle has left to spend moving
	for remaining > 0 {
		if veh.edgeIndex >= len(veh.edges) {
			veh.done = true // traversed the whole path
			return
		}
		edge, ok := roadGraph.Edge(veh.edges[veh.edgeIndex])
		if !ok || edge.LengthM <= 0 {
			// Unknown or zero-length edge: nothing to traverse, step straight onto the
			// next edge without consuming time (it adds no realized travel time).
			veh.edgeIndex++
			veh.distOnEdge = 0
			continue
		}
		speed := edge.LengthM / bpr.Cost(edge, load.Load(edge.ID)) // m/s, BPR-derived
		if speed <= 0 {
			// Degenerate (infinite-cost) edge: treat as instantly stepped over rather
			// than stalling the vehicle forever — keeps the run bounded.
			veh.edgeIndex++
			veh.distOnEdge = 0
			continue
		}
		distLeftOnEdge := edge.LengthM - veh.distOnEdge
		timeToFinishEdge := distLeftOnEdge / speed
		if timeToFinishEdge <= remaining {
			// The vehicle finishes this edge within the tick: spend that time, advance
			// onto the next edge, and keep going with the remainder.
			veh.experiencedS += timeToFinishEdge
			remaining -= timeToFinishEdge
			veh.edgeIndex++
			veh.distOnEdge = 0
			continue
		}
		// The vehicle spends the rest of the tick still on this edge.
		veh.distOnEdge += speed * remaining
		veh.experiencedS += remaining
		remaining = 0
	}
	if veh.edgeIndex >= len(veh.edges) {
		veh.done = true
	}
}

// loadFromVehicles re-derives the per-edge load (vehicles/hour) from the vehicles
// currently in flight: each vehicle contributes its weight to the single edge it
// occupies this tick. It clears the store to zero first (every edge that no longer
// has a vehicle drops to 0, so congestion DISSIPATES as the peak drains — the
// time-evolution that distinguishes this from the static one-shot). It is a single-
// goroutine write, called after the concurrent route fan-out has joined, so there is
// no shared mutable load under a lock. A vehicle past its last edge (done) is not in
// the in-flight slice and contributes nothing.
func loadFromVehicles(load *congestion.LoadStore, inFlight []*vehicle) {
	for index := 0; index < load.Len(); index++ {
		load.Set(domain.EdgeID(index), 0)
	}
	for _, veh := range inFlight {
		if veh.edgeIndex < 0 || veh.edgeIndex >= len(veh.edges) {
			continue
		}
		edgeID := veh.edges[veh.edgeIndex]
		load.Set(edgeID, load.Load(edgeID)+veh.weight)
	}
}

// buildTickState assembles the immutable TickState for one tick: the sim clock
// instant, the in-flight/completed counts, and FRESH copies of the per-edge load and
// v/c (so a consumer may retain the snapshot without aliasing the simulator's live
// store). v/c is computed by the reused evaluator helper edgeVCRatios so the seam's
// v/c agrees exactly with the static metric's definition.
func buildTickState(tick int, cfg SimConfig, tickSeconds float64, inFlight, completed int, load *congestion.LoadStore, roadGraph graph.Graph) TickState {
	elapsed := time.Duration(float64(tick) * tickSeconds * float64(time.Second))
	return TickState{
		Tick:      tick,
		SimTime:   cfg.StartTime.Add(elapsed),
		InFlight:  inFlight,
		Completed: completed,
		Load:      load.Snapshot(),
		VC:        edgeVCRatios(roadGraph, cfg.BPR, load.Snapshot()),
	}
}

// sortedRequestOrder returns request indices ordered by DepartAt then input index —
// the fixed release order that makes the run deterministic regardless of the
// caller's slice order. Sorting indices (not the requests) leaves the caller's slice
// untouched, and the input-index tiebreak makes the order a TOTAL order (no two
// entries compare equal), so sort.Slice's result is unique and reproducible.
func sortedRequestOrder(reqs []routing.RouteRequest) []int {
	order := make([]int, len(reqs))
	for i := range order {
		order[i] = i
	}
	sort.Slice(order, func(a, b int) bool {
		ia, ib := order[a], order[b]
		if reqs[ia].DepartAt != reqs[ib].DepartAt {
			return reqs[ia].DepartAt < reqs[ib].DepartAt
		}
		return ia < ib // stable, total tiebreak on input index
	})
	return order
}

// derivedMaxTicks bounds an unconfigured run: enough ticks for the latest departure
// plus a generous travel allowance, so a normally-draining run finishes by the
// drained-network early-exit and only a genuinely stuck vehicle is cut off. It scales
// with the latest DepartAt (so a long peak gets proportionally more ticks) and never
// returns less than a small floor, so even an all-at-t=0 batch gets room to drain.
func derivedMaxTicks(reqs []routing.RouteRequest, tickSeconds float64) int {
	latestDepart := 0.0
	for _, req := range reqs {
		if req.DepartAt > latestDepart {
			latestDepart = req.DepartAt
		}
	}
	// Ticks to reach the last departure, plus a fixed travel allowance. The allowance
	// is large (the toy network's longest free-flow path is well under it) so a
	// healthy run drains via the early exit, not this cap.
	departTicks := int(latestDepart/tickSeconds) + 1
	const travelAllowanceTicks = 10000
	return departTicks + travelAllowanceTicks
}
