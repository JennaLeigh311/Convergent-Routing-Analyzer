package api

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/benchmark"
	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/graph"
	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/metrics"
)

// portoSliceServerPath is the committed real-Porto-geometry slice (75 edges / 45 nodes,
// carved from issue #120's Porto export) the streaming tests boot a Server over — real road
// geometry and real exporter-derived capacities, not the synthetic toy network.
const portoSliceServerPath = "../../testdata/porto_slice.geojson"

// newPortoSliceServer builds a Server over the committed Porto geometry slice via the SAME
// contract loader the -graph-file flag uses (issue #121), so the handler/stream tests
// exercise the real-network path without the full 1.7 MB export.
func newPortoSliceServer(t *testing.T) *Server {
	t.Helper()
	g, geom, err := graph.LoadEdgeAttributesGeoJSONFile(portoSliceServerPath)
	if err != nil {
		t.Fatalf("load Porto slice: %v", err)
	}
	srv, err := NewServer(g, geom, metrics.NewRegistry(), nil)
	if err != nil {
		t.Fatalf("NewServer(Porto slice): %v", err)
	}
	return srv
}

// stream_live_test.go covers the issue-#121 bounded-buffer STREAMING path: the replay must
// drain ticks AS the producer appends them (not from a pre-collected buffer) and must not
// declare the run done until the producer has finished. These are the two properties that
// make /stream's time-to-first-frame a small fraction of the total sim wall-time on a
// city-scale graph, and they are asserted here DETERMINISTICALLY (no wall-clock threshold),
// so the guarantee holds regardless of CI timing.

// TestLiveBuffersStreamAsProduced proves the core streaming property without any timing
// assertion: a live replayState built over a liveBuffers that holds ONLY each algo's first
// tick still reconstructs the FULL final bucketed state after the producer later appends the
// remaining ticks — which is only possible if advance reads the growing buffer rather than a
// snapshot taken when the replay state was built. It also pins the producer-gating in done():
// a fully DRAINED buffer is NOT "done" while the producer is still active, and becomes done
// only once finish() has been recorded.
func TestLiveBuffersStreamAsProduced(t *testing.T) {
	srv := newTestServer(t)
	full := collectStreamTicks(t, srv, streamTestConfig())

	lb := newLiveBuffers()
	// Seed only the FIRST tick of each algo — exactly what waitFirstTicks gates on before
	// the snapshots are built and sent.
	for _, name := range benchmark.RouterOrder {
		if len(full[name]) == 0 {
			t.Fatalf("algo %q produced no ticks", name)
		}
		lb.emit(full[name][0])
	}

	// Build the replay state over the LIVE buffers, wiring mu/producerActive exactly as
	// runStream does. At high speed so a single advance coalesces all due ticks.
	params := streamParams{Speed: 100000, ServerTickHz: 2}
	lb.mu.Lock()
	rs, snapshots := srv.newReplayState(lb.ticks, params)
	lb.mu.Unlock()
	rs.mu = &lb.mu
	rs.producerActive = lb.producerActive

	recon := reconstructFromSnapshots(snapshots)

	// Only the first ticks are buffered and the producer has not finished: the run is not
	// done (more ticks may still arrive).
	if rs.done() {
		t.Fatal("done() reported true while the producer is still active")
	}

	// The producer now appends every remaining tick — AFTER the replay state was built.
	for _, name := range benchmark.RouterOrder {
		for i := 1; i < len(full[name]); i++ {
			lb.emit(full[name][i])
		}
	}

	// Drain what is buffered (one coalesced pass per algo at this speed). The buffer is now
	// fully consumed, but the producer has NOT finished — done() must still be false, gating
	// on the producer, not on a momentary catch-up.
	for _, d := range rs.advance(srv) {
		for _, ch := range d.Changed {
			recon[d.Algo][ch.SegmentID] = ch.Bucket
		}
	}
	if rs.done() {
		t.Fatal("done() reported true on a drained buffer while the producer is still active")
	}

	// The producer finishes: now the drained run is genuinely done.
	lb.finish(nil)
	if !rs.done() {
		t.Fatal("done() reported false after the producer finished and the buffer drained")
	}

	// The reconstruction from snapshots + live-streamed deltas must equal ground truth at
	// each algo's FINAL tick — only reachable if advance saw the post-build appends.
	for _, name := range benchmark.RouterOrder {
		ticks := full[name]
		if !equalBuckets(recon[name], srv.fullBuckets(ticks[len(ticks)-1])) {
			t.Errorf("%q: live-streamed reconstruction != final ground truth", name)
		}
	}
}

// TestWaitFirstTicks pins the streaming boundary's gate: waitFirstTicks returns nil once
// every algo has a first tick (so full snapshots can be built), returns nil early if the
// producer finishes (the defensive zero-tick path), and returns the context error promptly
// on a client disconnect / cancellation — so a stalled producer can never wedge a connection.
func TestWaitFirstTicks(t *testing.T) {
	// A cancelled context returns promptly with the ctx error (no first ticks yet).
	lb := newLiveBuffers()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := lb.waitFirstTicks(ctx); err == nil {
		t.Errorf("waitFirstTicks on a cancelled context: err = nil, want the ctx error")
	}

	// A finished producer returns nil even with missing ticks (the caller then surfaces any
	// producer error via terminalErr).
	lbFinished := newLiveBuffers()
	lbFinished.finish(nil)
	if err := lbFinished.waitFirstTicks(context.Background()); err != nil {
		t.Errorf("waitFirstTicks on a finished producer: err = %v, want nil", err)
	}

	// Every algo's first tick present: returns nil.
	lbAll := newLiveBuffers()
	for _, name := range benchmark.RouterOrder {
		lbAll.emit(benchmark.AlgoTick{Algo: name, State: benchmark.TickState{Tick: 1, SimTime: time.Unix(0, 0)}})
	}
	if err := lbAll.waitFirstTicks(context.Background()); err != nil {
		t.Errorf("waitFirstTicks with all first ticks present: err = %v, want nil", err)
	}
}

// TestStreamFirstFramePortoSlice is the issue-#121 real-network first-frame demonstration:
// over a live WebSocket to a server backed by REAL Porto geometry, the first frame (a
// snapshot) arrives well before the full run's paced replay completes — the property the
// former collect-to-completion path could not offer, since it emitted nothing until the
// entire sim had run. The streaming path sends every algo's snapshot as soon as its first
// tick lands (one sim step), THEN paces the deltas at the server tick, so time-to-first-
// frame is a small fraction of the total connection time. The margin here is generous (the
// first frame must land in under half the total run) so the assertion demonstrates the
// property without depending on a brittle absolute-nanosecond threshold; the deterministic
// TestLiveBuffersStreamAsProduced pins the underlying mechanism exactly.
func TestStreamFirstFramePortoSlice(t *testing.T) {
	srv := newPortoSliceServer(t)
	httpSrv := httptest.NewServer(srv.Routes())
	defer httpSrv.Close()

	// tick_hz=2 → the paced replay loop advances at a 500 ms server tick, so the full run
	// spans at least one server-tick period; speed=100000 collapses the sim so it drains in a
	// single coalesced pass. The first snapshot is written before the ticker loop begins.
	wsURL := "ws" + httpSrv.URL[len("http"):] + "/stream?speed=100000&tick_hz=2&seed=7&count=120&cap_scale=0.9"

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.CloseNow() }()

	start := time.Now()
	var firstFrame time.Duration
	snapshots := make(map[string]bool)
	deltas := 0
	for {
		var frame struct {
			Type string `json:"type"`
			Algo string `json:"algo"`
		}
		if err := wsjson.Read(ctx, conn, &frame); err != nil {
			break // normal closure once the run drains
		}
		if firstFrame == 0 {
			firstFrame = time.Since(start)
		}
		switch frame.Type {
		case "snapshot":
			snapshots[frame.Algo] = true
		case "delta":
			deltas++
		}
	}
	total := time.Since(start)

	// Behavioral guarantees: every algo announced (snapshot) and the run progressed (deltas).
	for _, name := range benchmark.RouterOrder {
		if !snapshots[name] {
			t.Errorf("no snapshot received for algo %q on the Porto slice", name)
		}
	}
	if deltas == 0 {
		t.Errorf("expected at least one delta frame on the Porto slice, got 0")
	}

	// The demonstration: the first frame is a small fraction of the total run. The generous
	// 2× margin keeps it robust to CI timing while still failing a precompute-then-replay
	// regression (which would push the first frame out to the full sim wall-time).
	t.Logf("Porto-slice /stream: first-frame=%v total=%v (%.1f%% of total)",
		firstFrame, total, 100*float64(firstFrame)/float64(total))
	if firstFrame >= total {
		t.Errorf("first frame (%v) did not precede run completion (%v) — stream is not live", firstFrame, total)
	}
	if total > 100*time.Millisecond && firstFrame*2 >= total {
		t.Errorf("first-frame latency %v is not a small fraction of the %v total run", firstFrame, total)
	}
}

// TestLiveBuffersTerminalErr pins the pre-frame failure surface: a producer that finishes
// with an error exposes it via terminalErr (so runStream can close with an error BEFORE
// sending any frame — no partial stream on a pre-sim failure), while a still-running or
// clean producer reports no terminal error.
func TestLiveBuffersTerminalErr(t *testing.T) {
	lbRunning := newLiveBuffers()
	if err := lbRunning.terminalErr(); err != nil {
		t.Errorf("terminalErr on a running producer = %v, want nil", err)
	}

	lbErr := newLiveBuffers()
	sentinel := context.Canceled
	lbErr.finish(sentinel)
	if err := lbErr.terminalErr(); err != sentinel {
		t.Errorf("terminalErr after finish(err) = %v, want %v", err, sentinel)
	}
	if err := lbErr.runErr(); err != sentinel {
		t.Errorf("runErr after finish(err) = %v, want %v", err, sentinel)
	}
}
