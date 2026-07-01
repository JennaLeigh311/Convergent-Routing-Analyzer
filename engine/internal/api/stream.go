package api

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/benchmark"
	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/domain"
)

// stream.go is the §R6 / #93 WebSocket streaming endpoint: GET /stream upgrades to
// a WebSocket, runs ALL SIX routers in parallel over the §R5 mesoscopic simulator
// (benchmark.RunParallel) from a chosen start time-of-day/date, and streams each
// algorithm's evolving congestion — one snapshot per algorithm on connect, then
// bucketed deltas at a FIXED server tick — alongside its per-algo compute-time and
// realized-traffic metrics. It is the data source the Phase-6 comparison frontend
// animates (project-outline.md "Functionality clarification").
//
// THE TWO CLOCKS AND THE TWO KNOBS THAT RELATE THEM (decoupled, §R6).
//
// Two actual clocks:
//
//  1. The SIMULATED clock — the sim's internal Δt (≈30s/tick), how the model
//     advances congestion. Owned by the simulator.
//  2. WALL CLOCK — real time on the server.
//
// Two knobs map one onto the other:
//
//   - The REPLAY SPEED (the `speed` query param) — how many SIMULATED seconds elapse
//     per wall-clock second; it is a RATIO between the two clocks, not a clock itself.
//     speed compresses simulated time: speed=60 plays an hour of simulation in a
//     wall-clock minute. It does NOT change the message rate.
//   - The fixed SERVER TICK (the `tick_hz` param, default 1 Hz) — the wall-clock
//     cadence at which delta frames are emitted (1–2 Hz). It stays FIXED regardless of
//     speed: a faster speed advances the simulated clock further between frames (so
//     each frame carries a bigger jump), but the frames still arrive once per server
//     tick. This is the decoupling §R6 requires.
//
// I/O DECOUPLING (the critical constraint). The simulator's TickObserver runs on the
// simulation goroutine and BLOCKS the model. The orchestrator forwards each tick to
// emit, which here only APPENDS the tick to an in-memory per-algo buffer (cheap,
// non-blocking) — it never writes to the socket. A separate replay loop reads from
// those buffers at the server-tick cadence and does the (blocking) socket writes, so
// a slow or stalled WebSocket client can never stall — or deadlock — a simulation.
// The sim runs to completion as fast as it can; the replay loop paces the OUTPUT.

// endpointStream is the metric label for the WebSocket stream endpoint.
const endpointStream = "stream"

// Bucketing scheme (§R6: quantize v/c to ~16–32 buckets). A segment's v/c is
// quantized to one of vcBucketCount = 24 buckets of width vcBucketWidth = 0.1, so
// bucket b covers v/c ∈ [0.1·b, 0.1·(b+1)). Bucket 0 is free-flow (v/c < 0.1) and
// the top bucket (vcBucketCount-1 = 23) is SATURATING: it absorbs everything at v/c
// ≥ 2.3, so an arbitrarily over-capacity edge maps to a finite bucket rather than an
// unbounded index. 24 buckets at width 0.1 covers the v/c ∈ [0, 2.3+] band the toy
// network exercises at fine-enough resolution that color steps read smoothly while
// keeping deltas small: a delta carries a segment ONLY when its BUCKET changed, so
// sub-bucket jitter between ticks produces no frame.
const (
	vcBucketCount = 24
	vcBucketWidth = 0.1
)

// bucketOf quantizes a v/c ratio to its bucket index in [0, vcBucketCount-1]. A
// negative/NaN v/c floors to bucket 0 (free-flow); a v/c at or above the top band
// saturates to the last bucket. The mapping is total and deterministic so two runs
// at a fixed seed bucket identically.
func bucketOf(vc float64) int {
	if vc <= 0 || math.IsNaN(vc) {
		return 0
	}
	// A tiny epsilon before the floor so an exact band boundary (e.g. v/c = 2.3,
	// which 2.3/0.1 represents as 22.999… in float) lands in the band it names rather
	// than the one below it. The epsilon is far smaller than any v/c the run produces,
	// so it only corrects representation error, never a genuine sub-band value.
	b := int(math.Floor(vc/vcBucketWidth + 1e-9))
	if b >= vcBucketCount {
		return vcBucketCount - 1
	}
	return b
}

// streamDefaults / bounds for the WebSocket query parameters.
const (
	defaultServerTickHz = 1.0
	minServerTickHz     = 0.5 // a slower floor than 1 Hz so a client can dial it down
	maxServerTickHz     = 2.0 // §R6 caps the fixed server tick at 2 Hz
	defaultReplaySpeed  = 60.0
	maxReplaySpeed      = 100000.0
	// maxStreamCount caps the per-run OD count R for the LIVE stream. Each connect
	// runs six full sims and routes R requests six times, so the live endpoint uses a
	// tighter ceiling than the batch /benchmark sweep's maxRequestCount (which runs
	// async and dedupes identical tuples): an interactive view never needs the full
	// 100k, and the lower cap bounds the per-connection CPU an unauthenticated client
	// can demand.
	maxStreamCount = 20_000
	// maxConcurrentStreams bounds simultaneous live stream runs. Each /stream connect
	// synchronously launches RunParallel (six concurrent sims) before its first frame,
	// so without a cap N connects = 6N sims with no backpressure. A connect that would
	// exceed this is refused with 503 BEFORE the WebSocket upgrade (see handleStream),
	// mirroring the sweepSlots admission control on /benchmark.
	maxConcurrentStreams = 8
)

// streamParams is the parsed GET /stream query: the §R6 time-slider controls plus
// the reproducibility knobs. Every field has a defined default so a bare /stream
// connect runs the canonical scenario.
type streamParams struct {
	// StartTime is the time-of-day/date the simulation starts at (the slider value),
	// RFC3339. Default: a fixed 08:00 rush-hour origin so a bare connect is reproducible.
	StartTime time.Time
	// Speed is the replay speed: simulated seconds per wall-clock second.
	Speed float64
	// ServerTickHz is the fixed wall-clock frame cadence (1–2 Hz).
	ServerTickHz float64
	// Seed / Count / CapacityScale parameterize the shared OD set and BPR.
	Seed          int64
	Count         int
	CapacityScale float64
}

// defaultStreamStart is the fixed rush-hour origin a bare connect uses — the same
// 08:00 instant the sweep pins, so the stream and the sweep share a clock origin.
var defaultStreamStart = time.Date(2026, 6, 22, 8, 0, 0, 0, time.UTC)

// parseStreamParams reads the §R6 query tuple with defaults and bounds, returning a
// client-facing error for an out-of-range value so a bad connect is a clean close,
// not a panic. Unknown params are ignored (a query string is forgiving), but every
// recognized one is validated.
func parseStreamParams(q map[string][]string) (streamParams, error) {
	get := func(key string) string {
		if v, ok := q[key]; ok && len(v) > 0 {
			return v[0]
		}
		return ""
	}

	p := streamParams{
		StartTime:     defaultStreamStart,
		Speed:         defaultReplaySpeed,
		ServerTickHz:  defaultServerTickHz,
		Seed:          0,
		Count:         benchmark.DefaultODCount,
		CapacityScale: 1.0,
	}

	if s := get("start"); s != "" {
		t, err := time.Parse(time.RFC3339, s)
		if err != nil {
			return p, errors.New("invalid start: must be RFC3339 (e.g. 2026-06-22T08:00:00Z)")
		}
		p.StartTime = t
	}
	if s := get("speed"); s != "" {
		v, err := strconv.ParseFloat(s, 64)
		if err != nil || v <= 0 || v > maxReplaySpeed {
			return p, errors.New("invalid speed: must be a number in (0, 100000]")
		}
		p.Speed = v
	}
	if s := get("tick_hz"); s != "" {
		v, err := strconv.ParseFloat(s, 64)
		if err != nil || v < minServerTickHz || v > maxServerTickHz {
			return p, errors.New("invalid tick_hz: must be in [0.5, 2]")
		}
		p.ServerTickHz = v
	}
	if s := get("seed"); s != "" {
		v, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			return p, errors.New("invalid seed: must be an integer")
		}
		p.Seed = v
	}
	if s := get("count"); s != "" {
		v, err := strconv.Atoi(s)
		if err != nil || v < 0 || v > maxStreamCount {
			return p, fmt.Errorf("invalid count: must be in [0, %d]", maxStreamCount)
		}
		p.Count = v
	}
	if s := get("cap_scale"); s != "" {
		v, err := strconv.ParseFloat(s, 64)
		if err != nil || v <= 0 {
			return p, errors.New("invalid cap_scale: must be > 0")
		}
		p.CapacityScale = v
	}
	return p, nil
}

// --- Wire protocol frames -------------------------------------------------------
//
// WIRE-SEMANTICS CHANGES in the per-frame `metrics` object (#112). The stream protocol
// carries no version token, so these are flagged here for the #114 leaderboard
// consumer:
//
//   - `poa` KEEPS its name and type (float) but CHANGED MEANING: it is now the CONSTANT
//     static-equilibrium Price of Anarchy per algo (this algo's real iterative
//     assignment total ÷ systemoptimal's, over the shared OD set) — the same value the
//     static /benchmark endpoint reports, so the live leaderboard now agrees with
//     /benchmark. Previously it was a per-tick ratio of best-response mesoscopic totals
//     ("six flavors of greedy"), which is why it moved to the honest static reference.
//     It no longer varies tick to tick.
//   - `route_median_ns` is a NEW additive int64 field: the fair MEDIAN wall-clock
//     nanoseconds of a single router.Route call so far — stable run-to-run and
//     independent of the six-sim concurrency. PREFER it over `compute_ms` for the
//     "fastest to route" ranking; `compute_ms` is retained for back-compat but is the
//     jittery fan-out-summed cumulative timer.
//
// `realized_total_s` is unchanged (the live best-response congestion magnitude), and
// the segment/bucket frame shape (the §1 segment_id contract) is untouched.

// frameSnapshot is the initial per-algorithm frame emitted on connect: the algo's
// FULL bucketed congestion state. The client seeds its Map<segment_id, bucket> from
// it and applies subsequent deltas. segments is keyed by segment_id (the frozen §1
// contract), never EdgeID.
type frameSnapshot struct {
	Type     string      `json:"type"` // always "snapshot"
	Algo     string      `json:"algo"`
	Tick     int         `json:"tick"`
	SimTime  string      `json:"sim_time"` // RFC3339, the simulated clock at this tick
	Segments []segmentVC `json:"segments"`
	Metrics  algoMetrics `json:"metrics"`
}

// frameDelta is a subsequent per-algorithm frame: ONLY the segments whose v/c BUCKET
// changed since this algo's last emitted frame. Reconstructing snapshot + all deltas
// in order reproduces the full bucketed state (the delta-correctness invariant the
// tests assert).
type frameDelta struct {
	Type    string      `json:"type"` // always "delta"
	Algo    string      `json:"algo"`
	Tick    int         `json:"tick"`
	SimTime string      `json:"sim_time"`
	Changed []segmentVC `json:"changed"`
	Metrics algoMetrics `json:"metrics"`
}

// segmentVC is one segment's bucketed congestion: its segment_id, the raw v/c, and
// the bucket the v/c quantizes to. The client colors by bucket; raw vc is carried
// for tooltips/debugging. Segments are sorted by segment_id so a frame is
// deterministic and diffable.
type segmentVC struct {
	SegmentID string  `json:"segment_id"`
	VC        float64 `json:"vc"`
	Bucket    int     `json:"bucket"`
}

// algoMetrics is the per-algorithm running metrics carried on every frame (§R6): the
// compute time (answers "fastest to route") and the realized-traffic figures
// (answer "best at minimizing traffic"), updated per tick to match the #89/#90
// evaluators.
type algoMetrics struct {
	// ComputeMs is the CUMULATIVE wall-clock milliseconds spent in router.Route so
	// far this run. KEPT FOR BACK-COMPAT (#112): it is a sum over the concurrent
	// per-tick fan-out, so it over-counts overlapping Route calls and jitters under
	// the six-sim CPU contention — read RouteMedianNanos instead for a fair figure.
	ComputeMs float64 `json:"compute_ms"`
	// RouteMedianNanos is the MEDIAN wall-clock nanoseconds of a single router.Route
	// call so far — the fair, per-call "fastest to route" metric (#112). Unlike
	// ComputeMs it is stable run-to-run and independent of how many algos stream
	// concurrently. It carries AlgoTick.RouteMedianNanos straight through (no unit
	// conversion — both are nanoseconds; the name matches deliberately, unlike the
	// ComputeNanos→ComputeMs ns→ms conversion).
	RouteMedianNanos int64 `json:"route_median_ns"`
	// RealizedTotalS is the realized total network time (seconds) at this tick,
	// matching routing.TotalNetworkTime over the tick's per-edge load — the live
	// best-response congestion magnitude (NOT a PoA numerator, see PoA below).
	RealizedTotalS float64 `json:"realized_total_s"`
	// PoA is this algo's TRUE static-equilibrium Price of Anarchy versus the
	// system-optimal reference, computed once over the shared OD set via the real
	// iterative AssignResult (#112) — the same value the static /benchmark sweep
	// reports. It is constant across this algo's frames and ≥ 1 (SO ≤ UE). It replaces
	// the former per-tick greedy pairing, which measured "six flavors of greedy".
	PoA float64 `json:"poa"`
	// InFlight / Completed are the live vehicle counts (context for the metrics).
	InFlight  int `json:"in_flight"`
	Completed int `json:"completed"`
}

// handleStream serves GET /stream: it upgrades to a WebSocket and streams the
// six-algorithm parallel simulation. A non-GET is rejected before the upgrade. The
// upgrade itself does not go through writeJSON (it hijacks the connection), so the
// metric is recorded once for the connection attempt.
func (s *Server) handleStream(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.writeError(w, endpointStream, http.StatusMethodNotAllowed, "method not allowed: use GET")
		return
	}

	params, err := parseStreamParams(r.URL.Query())
	if err != nil {
		s.writeError(w, endpointStream, http.StatusBadRequest, err.Error())
		return
	}

	// Admission control BEFORE the upgrade: each stream run launches six concurrent
	// sims, so bound how many run at once. A connect that would exceed the cap is a
	// clean 503 (no socket is opened), mirroring /benchmark's sweepSlots. The slot is
	// held for the whole connection and released when runStream returns.
	select {
	case s.streamSlots <- struct{}{}:
		defer func() { <-s.streamSlots }()
	default:
		s.writeError(w, endpointStream, http.StatusServiceUnavailable,
			"stream capacity reached, please retry shortly")
		return
	}

	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		// InsecureSkipVerify disables Origin checking: the frontend is served from a
		// different origin in development and this is a read-only data stream behind
		// the deployment's own ingress, not a credentialed endpoint. Tighten with an
		// allowed-origins list when the deployment origin is known.
		InsecureSkipVerify: true,
	})
	if err != nil {
		// Accept already wrote an error response; just record the outcome.
		s.metrics.Observe(endpointStream, outcomeError)
		s.logger.Info("stream: websocket accept failed", "err", err)
		return
	}
	s.metrics.Observe(endpointStream, outcomeOK)
	defer func() { _ = conn.CloseNow() }()

	if err := s.runStream(r.Context(), conn, params); err != nil {
		s.logger.Info("stream: connection ended", "err", err)
	}
}

// runStream drives one WebSocket connection: it launches the parallel six-algorithm
// simulation (buffering ticks off the socket's hot path), then replays the buffered
// per-algo ticks to the client at the fixed server-tick cadence, pacing the
// SIMULATED clock against wall clock by the replay speed. It returns when the run is
// fully replayed, the client disconnects, or ctx is cancelled.
func (s *Server) runStream(ctx context.Context, conn *websocket.Conn, params streamParams) error {
	// CloseRead starts a background reader that drains (and rejects) any client message,
	// responds to pings, and — the reason it is here — cancels the returned context the
	// moment the peer closes or the connection errors. Without it a client that vanishes
	// during the buffering phase below (when nothing reads or writes the socket) would
	// go undetected until the first replay write; with it, a disconnect cancels ctx and
	// aborts collectParallel promptly. This is a read-only stream, so we never expect a
	// client message.
	ctx = conn.CloseRead(ctx)

	// Run the whole parallel simulation up front into per-algo ordered buffers. The
	// orchestrator's emit only appends (no I/O), so a slow client cannot stall a sim;
	// the run is bounded and deterministic, so buffering it is cheap on the toy graph.
	// Note this means time-to-first-frame equals the full simulation wall-time: nothing
	// is delivered until the run completes. (At city scale this becomes a streaming
	// bounded buffer that emits as it goes; the buffer boundary here is the seam where
	// that swap lands.) The sim runs under the connection ctx so a disconnect mid-build
	// cancels it promptly.
	buffers, err := s.collectParallel(ctx, params)
	if err != nil {
		_ = conn.Close(websocket.StatusInternalError, "simulation failed")
		return err
	}

	return s.replay(ctx, conn, params, buffers)
}

// collectParallel runs benchmark.RunParallel to completion, collecting each
// algorithm's AlgoTicks into an ordered per-algo slice keyed by algo name. The emit
// callback appends under a mutex (the six sims run concurrently); it does NO I/O, so
// it never blocks a sim on the network — the §R6 decoupling requirement.
func (s *Server) collectParallel(ctx context.Context, params streamParams) (map[string][]benchmark.AlgoTick, error) {
	buffers := make(map[string][]benchmark.AlgoTick, len(benchmark.RouterOrder))
	for _, name := range benchmark.RouterOrder {
		buffers[name] = nil
	}
	var mu sync.Mutex

	emit := func(tick benchmark.AlgoTick) {
		mu.Lock()
		buffers[tick.Algo] = append(buffers[tick.Algo], tick)
		mu.Unlock()
	}

	cfg := benchmark.ParallelConfig{
		StartTime:     params.StartTime,
		Seed:          params.Seed,
		Count:         params.Count,
		CapacityScale: params.CapacityScale,
	}
	if err := benchmark.RunParallel(ctx, s.graph, cfg, emit); err != nil {
		return nil, err
	}
	return buffers, nil
}

// replay streams the collected per-algo ticks to the client: one snapshot per
// algorithm first (the full bucketed state at tick 1), then bucketed deltas at the
// fixed server-tick cadence. It advances a SIMULATED clock by (speed / tickHz)
// simulated seconds each server tick and, for each algorithm, emits the latest tick
// whose simulated time has been reached — so a higher speed jumps further per frame
// while the frame rate stays fixed (the §R6 decoupling).
//
// The per-server-tick stepping and the snapshot construction live in replayState
// (below) so they can be unit-tested without a real wall-clock ticker; replay itself
// is just the I/O loop that drives that state machine and writes frames.
func (s *Server) replay(ctx context.Context, conn *websocket.Conn, params streamParams, buffers map[string][]benchmark.AlgoTick) error {
	rs, snapshots := s.newReplayState(buffers, params)

	// Send the initial snapshot for each algorithm (in RouterOrder).
	for _, frame := range snapshots {
		if err := s.writeFrame(ctx, conn, frame); err != nil {
			return err
		}
	}

	// The replay loop: a fixed wall-clock ticker at the server tick rate. Each tick
	// advances the simulated clock and flushes any algo ticks now due as deltas.
	ticker := time.NewTicker(rs.tickPeriod)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
		for _, frame := range rs.advance(s) {
			if err := s.writeFrame(ctx, conn, frame); err != nil {
				return err
			}
		}
		if rs.done() {
			return nil
		}
	}
}

// replayState is the wall-clock-free core of the replay loop: it holds the per-algo
// cursors and last-emitted bucket maps and, on each advance, advances a SIMULATED
// clock and returns the delta frames now due. Separating it from replay's ticker/
// socket I/O is what lets the pacing and coalescing logic be unit-tested directly
// (see stream_test.go) rather than only through a wall-clock end-to-end dial.
type replayState struct {
	buffers map[string][]benchmark.AlgoTick
	// lastBuckets[algo][segment_id] is the last bucket emitted for a segment, the diff
	// base for the next delta. cursor[algo] is the index of the next unsent tick.
	lastBuckets  map[string]map[string]int
	cursor       map[string]int
	firstSimTime map[string]time.Time

	tickPeriod              time.Duration
	simSecondsPerServerTick float64
	// elapsedSimSeconds is the simulated time reached so far past tick 1's instant.
	elapsedSimSeconds float64
}

// newReplayState builds the replay state from the collected buffers and returns it
// alongside the per-algorithm snapshot frames (in RouterOrder) the caller sends first.
func (s *Server) newReplayState(buffers map[string][]benchmark.AlgoTick, params streamParams) (*replayState, []frameSnapshot) {
	rs := &replayState{
		buffers:                 buffers,
		lastBuckets:             make(map[string]map[string]int, len(benchmark.RouterOrder)),
		cursor:                  make(map[string]int, len(benchmark.RouterOrder)),
		firstSimTime:            make(map[string]time.Time, len(benchmark.RouterOrder)),
		tickPeriod:              time.Duration(float64(time.Second) / params.ServerTickHz),
		simSecondsPerServerTick: params.Speed / params.ServerTickHz,
	}

	// Build the initial snapshot for each algorithm that produced any tick: the FULL
	// bucketed state at its first tick. An algorithm whose sim produced no tick sends an
	// empty snapshot so the client still learns the algo exists. (In practice the
	// simulator always emits at least tick 1, so the empty-snapshot path is a defensive
	// guard rather than a reachable orchestrator outcome — it is covered directly in the
	// tests.)
	snapshots := make([]frameSnapshot, 0, len(benchmark.RouterOrder))
	for _, name := range benchmark.RouterOrder {
		ticks := buffers[name]
		var first benchmark.AlgoTick
		if len(ticks) > 0 {
			first = ticks[0]
			rs.cursor[name] = 1
			rs.firstSimTime[name] = first.State.SimTime
		}
		buckets, frame := s.buildSnapshot(name, first, len(ticks) > 0)
		rs.lastBuckets[name] = buckets
		snapshots = append(snapshots, frame)
	}
	return rs, snapshots
}

// advance steps the simulated clock by one server tick and returns the delta frames
// now due, one per algorithm that has a newly-reached tick (in RouterOrder). Ticks
// whose simulated time has been reached are coalesced to the LAST one so a high speed
// sends a single delta covering the jump rather than a burst; the delta diffs against
// the last emitted bucket map so coalescing stays correct.
func (rs *replayState) advance(s *Server) []frameDelta {
	rs.elapsedSimSeconds += rs.simSecondsPerServerTick

	frames := make([]frameDelta, 0, len(benchmark.RouterOrder))
	for _, name := range benchmark.RouterOrder {
		ticks := rs.buffers[name]
		latestIdx := -1
		for rs.cursor[name] < len(ticks) {
			// A tick's simulated position is its SimTime relative to the run's
			// StartTime — derived from the stamp the simulator already set, so the
			// pacing needs no separate Δt. Tick 1 is at simSecond ≈ Δt; we offset by
			// tick 1's instant so the FIRST delta is due one server tick in.
			simSecondOfTick := ticks[rs.cursor[name]].State.SimTime.Sub(rs.firstSimTime[name]).Seconds()
			if simSecondOfTick > rs.elapsedSimSeconds {
				break
			}
			latestIdx = rs.cursor[name]
			rs.cursor[name]++
		}
		if latestIdx >= 0 {
			frames = append(frames, s.buildDelta(name, ticks[latestIdx], rs.lastBuckets[name]))
		}
	}
	return frames
}

// done reports whether every algorithm's buffer has been fully replayed.
func (rs *replayState) done() bool {
	for _, name := range benchmark.RouterOrder {
		if rs.cursor[name] < len(rs.buffers[name]) {
			return false
		}
	}
	return true
}

// buildSnapshot builds an algorithm's initial snapshot frame from its first tick and
// returns the bucket map it establishes (so deltas diff against it). When the algo
// produced no tick (have == false), it returns an empty snapshot and an empty map.
func (s *Server) buildSnapshot(name string, first benchmark.AlgoTick, have bool) (map[string]int, frameSnapshot) {
	buckets := make(map[string]int)
	segs := make([]segmentVC, 0)
	frame := frameSnapshot{Type: "snapshot", Algo: name}
	if !have {
		frame.Segments = segs
		return buckets, frame
	}
	for edgeID, segment := range s.segmentByEdge {
		vc := vcAt(first.State.VC, edgeID)
		b := bucketOf(vc)
		segID := string(segment)
		buckets[segID] = b
		segs = append(segs, segmentVC{SegmentID: segID, VC: vc, Bucket: b})
	}
	sort.Slice(segs, func(i, j int) bool { return segs[i].SegmentID < segs[j].SegmentID })
	frame.Tick = first.State.Tick
	frame.SimTime = first.State.SimTime.UTC().Format(time.RFC3339)
	frame.Segments = segs
	frame.Metrics = metricsOf(first)
	return buckets, frame
}

// buildDelta builds an algorithm's delta frame carrying ONLY the segments whose
// bucket changed since lastBuckets, and MUTATES lastBuckets in place to this tick's
// buckets so the next delta diffs against the current state. Segments are sorted by
// segment_id for a deterministic, diffable frame.
func (s *Server) buildDelta(name string, tick benchmark.AlgoTick, lastBuckets map[string]int) frameDelta {
	changed := make([]segmentVC, 0)
	for edgeID, segment := range s.segmentByEdge {
		vc := vcAt(tick.State.VC, edgeID)
		b := bucketOf(vc)
		segID := string(segment)
		if prev, ok := lastBuckets[segID]; !ok || prev != b {
			changed = append(changed, segmentVC{SegmentID: segID, VC: vc, Bucket: b})
			lastBuckets[segID] = b
		}
	}
	sort.Slice(changed, func(i, j int) bool { return changed[i].SegmentID < changed[j].SegmentID })
	return frameDelta{
		Type:    "delta",
		Algo:    name,
		Tick:    tick.State.Tick,
		SimTime: tick.State.SimTime.UTC().Format(time.RFC3339),
		Changed: changed,
		Metrics: metricsOf(tick),
	}
}

// metricsOf maps an AlgoTick's metrics onto the wire shape: the back-compat
// cumulative compute_ms (nanoseconds → ms) alongside the fair per-route median, and
// the honest static-equilibrium PoA the orchestrator computed once over the shared OD
// set (#112). PoA is now carried on the tick (AlgoTick.StaticPoA) rather than derived
// here from a per-tick greedy pairing, so it matches the static /benchmark reference
// and stays deterministic without any cross-goroutine same-tick indexing.
func metricsOf(tick benchmark.AlgoTick) algoMetrics {
	return algoMetrics{
		ComputeMs:        float64(tick.ComputeNanos) / 1e6,
		RouteMedianNanos: tick.RouteMedianNanos,
		RealizedTotalS:   tick.RealizedTotalS,
		PoA:              tick.StaticPoA,
		InFlight:         tick.State.InFlight,
		Completed:        tick.State.Completed,
	}
}

// vcAt reads the v/c on an edge from a tick's VC vector, returning 0 for an
// out-of-range id (matching the evaluator's out-of-range tolerance).
func vcAt(vc []float64, edgeID domain.EdgeID) float64 {
	if edgeID < 0 || int(edgeID) >= len(vc) {
		return 0
	}
	return vc[edgeID]
}

// writeFrame marshals and writes one frame as a JSON text message, under a short
// per-write deadline so a stalled client cannot pin the writer forever (the
// long-lived connection has no server WriteTimeout — see cmd/routing-server — so the
// per-write deadline is enforced here instead).
func (s *Server) writeFrame(ctx context.Context, conn *websocket.Conn, frame any) error {
	writeCtx, cancel := context.WithTimeout(ctx, streamWriteTimeout)
	defer cancel()
	return wsjson.Write(writeCtx, conn, frame)
}

// streamWriteTimeout bounds a single frame write so one stalled client cannot block
// the replay loop indefinitely; the frames are small so this is generous.
const streamWriteTimeout = 10 * time.Second
