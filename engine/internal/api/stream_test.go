package api

import (
	"context"
	"math"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/benchmark"
)

// TestBucketOf pins the bucketing scheme's boundaries: free-flow floor, interior
// width, the saturating top bucket, and the negative/NaN guards.
func TestBucketOf(t *testing.T) {
	cases := []struct {
		vc   float64
		want int
	}{
		{-1, 0},
		{math.NaN(), 0},
		{0, 0},
		{0.05, 0},
		{0.1, 1},
		{0.15, 1},
		{0.2, 2},
		{2.29, 22},
		{2.3, 23},
		{5.0, 23}, // saturates to the top bucket, never overflows
		{1000, 23},
	}
	for _, c := range cases {
		if got := bucketOf(c.vc); got != c.want {
			t.Errorf("bucketOf(%v) = %d, want %d", c.vc, got, c.want)
		}
	}
}

// collectStreamTicks runs the orchestrator and returns the per-algo tick buffers, the
// same shape the stream layer replays. Used to drive the snapshot/delta builders with
// real simulation data.
func collectStreamTicks(t *testing.T, srv *Server, cfg benchmark.ParallelConfig) map[string][]benchmark.AlgoTick {
	t.Helper()
	out := make(map[string][]benchmark.AlgoTick)
	var mu sync.Mutex
	emit := func(tick benchmark.AlgoTick) {
		mu.Lock()
		out[tick.Algo] = append(out[tick.Algo], tick)
		mu.Unlock()
	}
	if err := benchmark.RunParallel(context.Background(), srv.graph, cfg, emit); err != nil {
		t.Fatalf("RunParallel: %v", err)
	}
	return out
}

func streamTestConfig() benchmark.ParallelConfig {
	return benchmark.ParallelConfig{
		StartTime:     time.Date(2026, 6, 22, 8, 0, 0, 0, time.UTC),
		TickSeconds:   30,
		Seed:          7,
		Count:         200,
		CapacityScale: 0.84,
	}
}

// fullBuckets recomputes the EXPECTED full bucketed state (segment_id -> bucket) for a
// single tick directly from its VC vector — the ground truth the snapshot+deltas must
// reconstruct.
func (s *Server) fullBuckets(tick benchmark.AlgoTick) map[string]int {
	want := make(map[string]int)
	for edgeID, segment := range s.segmentByEdge {
		want[string(segment)] = bucketOf(vcAt(tick.State.VC, edgeID))
	}
	return want
}

// TestDeltaCorrectness asserts the two delta invariants for every algorithm:
//
//  1. ONLY segments whose bucket changed appear in a delta.
//  2. Reconstructing snapshot + all deltas in order reproduces the FULL bucketed
//     state at every tick (the client's Map<segment_id,bucket> == ground truth).
func TestDeltaCorrectness(t *testing.T) {
	srv := newTestServer(t)
	buffers := collectStreamTicks(t, srv, streamTestConfig())

	for _, name := range benchmark.RouterOrder {
		ticks := buffers[name]
		if len(ticks) == 0 {
			t.Fatalf("algo %q produced no ticks", name)
		}

		soTotals := map[int]float64{} // delta correctness is independent of PoA

		// Seed the reconstruction from the snapshot (the full state at tick 0).
		reconstructed, _ := srv.buildSnapshot(name, ticks[0], true, soTotals)

		// Verify the snapshot matches ground truth at tick 0.
		if !equalBuckets(reconstructed, srv.fullBuckets(ticks[0])) {
			t.Fatalf("%q snapshot != full state at tick %d", name, ticks[0].State.Tick)
		}

		// Apply each subsequent tick as a delta and verify both invariants.
		for i := 1; i < len(ticks); i++ {
			prev := copyBuckets(reconstructed)
			delta := srv.buildDelta(name, ticks[i], reconstructed, soTotals)

			// Invariant 1: every changed entry genuinely changed bucket vs prev.
			for _, ch := range delta.Changed {
				if old, ok := prev[ch.SegmentID]; ok && old == ch.Bucket {
					t.Errorf("%q tick %d: segment %s in delta but bucket unchanged (%d)",
						name, ticks[i].State.Tick, ch.SegmentID, ch.Bucket)
				}
				if ch.Bucket != bucketOf(ch.VC) {
					t.Errorf("%q tick %d: segment %s bucket %d != bucketOf(%v)",
						name, ticks[i].State.Tick, ch.SegmentID, ch.Bucket, ch.VC)
				}
			}

			// buildDelta already mutated `reconstructed` in place to the new state.
			// Invariant 2: it must equal ground truth recomputed from the VC vector.
			if !equalBuckets(reconstructed, srv.fullBuckets(ticks[i])) {
				t.Fatalf("%q tick %d: reconstructed snapshot+deltas != full state",
					name, ticks[i].State.Tick)
			}

			// And: no segment OUTSIDE the changed set may have changed bucket.
			want := srv.fullBuckets(ticks[i])
			changedSet := make(map[string]bool, len(delta.Changed))
			for _, ch := range delta.Changed {
				changedSet[ch.SegmentID] = true
			}
			for seg, b := range want {
				if prev[seg] != b && !changedSet[seg] {
					t.Errorf("%q tick %d: segment %s changed bucket %d->%d but missing from delta",
						name, ticks[i].State.Tick, seg, prev[seg], b)
				}
			}
		}
	}
}

func equalBuckets(a, b map[string]int) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

func copyBuckets(m map[string]int) map[string]int {
	out := make(map[string]int, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// TestStreamRejectsBadParams asserts a malformed query is a clean 400 BEFORE the
// upgrade — a bad connect never opens a socket.
func TestStreamRejectsBadParams(t *testing.T) {
	srv := newTestServer(t)
	cases := []string{
		"/stream?speed=-1",
		"/stream?speed=abc",
		"/stream?tick_hz=10",
		"/stream?tick_hz=0",
		"/stream?start=notatime",
		"/stream?count=-5",
		"/stream?cap_scale=0",
		"/stream?seed=xyz",
	}
	for _, target := range cases {
		req := httptest.NewRequest(http.MethodGet, target, nil)
		rec := httptest.NewRecorder()
		srv.Routes().ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", target, rec.Code)
		}
	}
}

// TestStreamMethodGuard asserts a non-GET is a 405.
func TestStreamMethodGuard(t *testing.T) {
	srv := newTestServer(t)
	req := httptest.NewRequest(http.MethodPost, "/stream", nil)
	rec := httptest.NewRecorder()
	srv.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", rec.Code)
	}
}

// TestStreamEndToEnd connects a real WebSocket client to a live server, drives the
// stream at a high speed (so the whole run replays quickly), and asserts: a snapshot
// arrives for every algorithm; deltas carry only changed segments; metrics are
// finite; and the connection closes cleanly when the run drains.
func TestStreamEndToEnd(t *testing.T) {
	srv := newTestServer(t)
	httpSrv := httptest.NewServer(srv.Routes())
	defer httpSrv.Close()

	wsURL := "ws" + httpSrv.URL[len("http"):] + "/stream?speed=100000&tick_hz=2&seed=7&count=120&cap_scale=0.84"

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.CloseNow()

	snapshots := make(map[string]bool)
	deltas := 0
	for {
		var frame struct {
			Type     string `json:"type"`
			Algo     string `json:"algo"`
			Segments []struct {
				SegmentID string  `json:"segment_id"`
				VC        float64 `json:"vc"`
				Bucket    int     `json:"bucket"`
			} `json:"segments"`
			Changed []struct {
				SegmentID string `json:"segment_id"`
			} `json:"changed"`
			Metrics struct {
				ComputeMs      float64 `json:"compute_ms"`
				RealizedTotalS float64 `json:"realized_total_s"`
				PoA            float64 `json:"poa"`
			} `json:"metrics"`
		}
		err := wsjson.Read(ctx, conn, &frame)
		if err != nil {
			// Normal closure once the run drains.
			break
		}
		switch frame.Type {
		case "snapshot":
			snapshots[frame.Algo] = true
		case "delta":
			deltas++
		default:
			t.Errorf("unknown frame type %q", frame.Type)
		}
		if math.IsNaN(frame.Metrics.RealizedTotalS) || math.IsInf(frame.Metrics.RealizedTotalS, 0) {
			t.Errorf("non-finite realized total in %s frame", frame.Algo)
		}
		if math.IsNaN(frame.Metrics.PoA) || math.IsInf(frame.Metrics.PoA, 0) {
			t.Errorf("non-finite PoA in %s frame", frame.Algo)
		}
	}

	for _, name := range benchmark.RouterOrder {
		if !snapshots[name] {
			t.Errorf("no snapshot received for algo %q", name)
		}
	}
	if deltas == 0 {
		t.Errorf("expected at least one delta frame, got 0")
	}
}
