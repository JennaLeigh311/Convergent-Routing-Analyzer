package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/benchmark"
	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/graph"
)

// pollBenchmark polls GET /benchmark/{id} until the job reaches a terminal state
// (done|failed) or the deadline elapses, returning the final decoded status body.
func pollBenchmark(t *testing.T, srv *Server, id string) benchmarkStatusResponse {
	t.Helper()
	var final benchmarkStatusResponse
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		rec := doRequest(t, srv, http.MethodGet, "/benchmark/"+id, "")
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /benchmark/%s: status = %d, want 200 (body: %s)", id, rec.Code, rec.Body.String())
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &final); err != nil {
			t.Fatalf("decode status: %v", err)
		}
		if final.Status == statusDone || final.Status == statusFailed {
			return final
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("job %s did not reach a terminal state by the deadline (last status %q)", id, final.Status)
	return final
}

// startBenchmark POSTs /benchmark and returns the decoded start response, failing
// the test if the immediate response is not 202.
func startBenchmark(t *testing.T, srv *Server, body string) benchmarkStartResponse {
	t.Helper()
	rec := doRequest(t, srv, http.MethodPost, "/benchmark", body)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("POST /benchmark %s: status = %d, want 202 (body: %s)", body, rec.Code, rec.Body.String())
	}
	var start benchmarkStartResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &start); err != nil {
		t.Fatalf("decode start: %v", err)
	}
	return start
}

// TestBenchmarkFailedJobIsRetriable asserts a sweep that ERRORS surfaces a clean
// failed status with a client-safe message (no internal detail), and that the
// failed tuple can be RE-RUN: a repeat POST with the same params starts a fresh
// job rather than returning the stuck failure forever (the §R6 cache evicts a
// failed tuple).
func TestBenchmarkFailedJobIsRetriable(t *testing.T) {
	srv := newTestServer(t)
	srv.sweepFn = func(context.Context, graph.Graph, int64, int) ([]benchmark.SweepCell, error) {
		return nil, fmt.Errorf("internal sweep boom: node 42 coord 40.7,-73.9")
	}

	body := `{"seed": 99}`
	start := startBenchmark(t, srv, body)
	final := pollBenchmark(t, srv, start.JobID)

	if final.Status != statusFailed {
		t.Fatalf("status = %q, want failed", final.Status)
	}
	if final.Report != nil {
		t.Errorf("failed job carries a report, want none")
	}
	if final.Error != "benchmark failed" {
		t.Errorf("error = %q, want the sanitized %q (no internal detail / coordinates)", final.Error, "benchmark failed")
	}

	// Retriable: a healthy sweep on the SAME tuple now starts a fresh job and
	// completes, proving the failed cache entry was evicted rather than stuck.
	srv.sweepFn = func(context.Context, graph.Graph, int64, int) ([]benchmark.SweepCell, error) {
		return []benchmark.SweepCell{}, nil
	}
	retry := startBenchmark(t, srv, body)
	if retry.JobID == start.JobID {
		t.Fatalf("retry returned the failed job id %q; a failed tuple must be re-runnable", retry.JobID)
	}
	if got := pollBenchmark(t, srv, retry.JobID); got.Status != statusDone {
		t.Errorf("retry status = %q, want done", got.Status)
	}
}

// TestBenchmarkPanicRecovered asserts a sweep that PANICS is recovered and
// recorded as a failed job rather than crashing the server process.
func TestBenchmarkPanicRecovered(t *testing.T) {
	srv := newTestServer(t)
	srv.sweepFn = func(context.Context, graph.Graph, int64, int) ([]benchmark.SweepCell, error) {
		panic("kaboom in the harness")
	}

	start := startBenchmark(t, srv, `{"seed": 7}`)
	final := pollBenchmark(t, srv, start.JobID)
	if final.Status != statusFailed {
		t.Fatalf("status = %q, want failed (panic should be recovered)", final.Status)
	}
	if final.Error != "benchmark failed" {
		t.Errorf("error = %q, want sanitized %q", final.Error, "benchmark failed")
	}

	// The server is still serving — a routing request still succeeds.
	if rec := doRequest(t, srv, http.MethodGet, "/route?from="+coordNode0+"&to="+coordNode2, ""); rec.Code != http.StatusOK {
		t.Errorf("post-panic /route status = %d, want 200 — the server should still be healthy", rec.Code)
	}
}

// TestBenchmarkConcurrentDedupe fires many simultaneous POSTs for the SAME tuple
// and asserts the dedupe is atomic: exactly ONE sweep is launched and every
// caller gets the same job id. This exercises getOrCreate's check-then-insert
// under -race with real concurrent writers (the path the sequential flow test
// cannot reach).
func TestBenchmarkConcurrentDedupe(t *testing.T) {
	srv := newTestServer(t)
	var launches int32
	release := make(chan struct{})
	srv.sweepFn = func(context.Context, graph.Graph, int64, int) ([]benchmark.SweepCell, error) {
		atomic.AddInt32(&launches, 1)
		<-release // hold the job "running" for the duration of the burst
		return []benchmark.SweepCell{}, nil
	}

	const n = 24
	ids := make(chan string, n)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			rec := doRequest(t, srv, http.MethodPost, "/benchmark", `{"seed": 5}`)
			var resp benchmarkStartResponse
			_ = json.Unmarshal(rec.Body.Bytes(), &resp)
			ids <- resp.JobID
		}()
	}
	close(start)
	wg.Wait()
	close(ids)
	close(release)

	first := ""
	for id := range ids {
		if id == "" {
			t.Fatalf("a concurrent POST returned an empty job id")
		}
		if first == "" {
			first = id
		} else if id != first {
			t.Fatalf("concurrent POSTs got distinct job ids (%q != %q); dedupe is not atomic", id, first)
		}
	}
	if got := atomic.LoadInt32(&launches); got != 1 {
		t.Errorf("launched %d sweeps for one tuple, want exactly 1", got)
	}
}

// TestBenchmarkCapacityExhausted asserts that once maxConcurrentSweeps distinct
// sweeps are in flight, a further distinct-tuple POST is rejected with a clean
// 503 (rather than spawning an unbounded number of concurrent CPU-heavy sweeps),
// and that no stuck "running" job is left behind for the rejected request.
func TestBenchmarkCapacityExhausted(t *testing.T) {
	srv := newTestServer(t)
	release := make(chan struct{})
	srv.sweepFn = func(context.Context, graph.Graph, int64, int) ([]benchmark.SweepCell, error) {
		<-release // occupy the slot until the test releases it
		return []benchmark.SweepCell{}, nil
	}

	// Fill every sweep slot with a distinct tuple (the slot is acquired
	// synchronously in the handler before the goroutine launches).
	for i := 0; i < maxConcurrentSweeps; i++ {
		startBenchmark(t, srv, fmt.Sprintf(`{"seed": %d}`, i))
	}

	// One more distinct tuple has no slot to run in: a clean 503.
	rec := doRequest(t, srv, http.MethodPost, "/benchmark", `{"seed": 999}`)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("over-capacity POST: status = %d, want 503 (body: %s)", rec.Code, rec.Body.String())
	}
	// The rejected tuple left no job behind — a fresh slot lets it run later.
	close(release)
}

// TestBenchmarkBodyTooLarge asserts the maxBenchmarkBodyBytes LimitReader cap
// turns an oversized body into a clean 400 rather than buffering it unbounded.
func TestBenchmarkBodyTooLarge(t *testing.T) {
	srv := newTestServer(t)
	huge := `{"algorithm":"` + strings.Repeat("a", maxBenchmarkBodyBytes+1024) + `"}`
	rec := doRequest(t, srv, http.MethodPost, "/benchmark", huge)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("oversized body: status = %d, want 400", rec.Code)
	}
}

// TestRouteEmptyPath asserts the documented from==to case: an origin that snaps
// to the destination node is a clean 200 with an empty segment list (a path to
// where you already are), not an error, and a finite (non-NaN) cost.
func TestRouteEmptyPath(t *testing.T) {
	srv := newTestServer(t)
	rec := doRequest(t, srv, http.MethodGet, "/route?from="+coordNode0+"&to="+coordNode0, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("from==to: status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	var resp routeResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Segments) != 0 {
		t.Errorf("segments = %v, want empty (already at destination)", resp.Segments)
	}
	if isNaN(resp.CostS) {
		t.Errorf("cost_s is NaN")
	}
}

// TestJobStoreEvictsOldestDone asserts the store is bounded: once it reaches
// maxJobs, creating another job evicts the oldest COMPLETED job (FIFO) so the
// maps cannot grow without bound as a caller varies the tuple.
func TestJobStoreEvictsOldestDone(t *testing.T) {
	const cap = 3
	st := newJobStore(cap)
	report := benchmark.Report{}

	var firstKey string
	for i := 0; i < cap+4; i++ {
		params := benchmarkParams{Seed: int64(i)}
		id := fmt.Sprintf("id-%d", i)
		gotID, _, _, created := st.getOrCreate(params, id)
		if !created {
			t.Fatalf("seed %d: getOrCreate created=false, want a fresh job", i)
		}
		if i == 0 {
			firstKey = params.cacheKey()
		}
		st.complete(gotID, &report, "")
	}

	st.mu.Lock()
	defer st.mu.Unlock()
	if len(st.byID) > cap {
		t.Errorf("byID has %d entries, want <= %d (store unbounded)", len(st.byID), cap)
	}
	if len(st.order) != len(st.byID) {
		t.Errorf("order (%d) and byID (%d) diverged", len(st.order), len(st.byID))
	}
	if _, ok := st.byKey[firstKey]; ok {
		t.Errorf("oldest tuple still cached after eviction; FIFO eviction did not drop it")
	}
}
