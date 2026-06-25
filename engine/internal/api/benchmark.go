package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"

	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/benchmark"
	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/graph"
)

// endpointBenchmark and endpointBenchmarkStatus are the metric labels for the
// async benchmark endpoints.
const (
	endpointBenchmark       = "benchmark"
	endpointBenchmarkStatus = "benchmark_status"
)

// jobStatus is a benchmark job's lifecycle state.
type jobStatus string

const (
	statusRunning jobStatus = "running"
	statusDone    jobStatus = "done"
	statusFailed  jobStatus = "failed"
)

// Async benchmark resource bounds. The job store is long-lived and the sweep is
// CPU-heavy, so the endpoint is bounded on three axes so an unauthenticated
// caller cannot exhaust the process by varying the §R6 tuple:
//
//	maxRequestCount     — ceiling on the per-level OD count R, so one POST cannot
//	                      size an arbitrarily large (memory/CPU) sweep.
//	maxConcurrentSweeps — cap on in-flight async sweeps; a POST that would exceed
//	                      it gets a clean 503 instead of oversubscribing the box.
//	maxBenchmarkJobs    — cap on retained jobs; the store evicts the oldest
//	                      completed job (FIFO) rather than growing without bound.
const (
	maxRequestCount     = 100_000
	maxConcurrentSweeps = 4
	maxBenchmarkJobs    = 256
)

// benchmarkParams is the §R6 parameter tuple a /benchmark run is keyed by:
// (algorithm, α, β, capacity_scale, requestCount, seed). It is BOTH the request
// body and the cache key — two POSTs with the same tuple return the SAME job
// (the cached result) rather than re-running an expensive systemoptimal sweep.
//
// The `algorithm` field DISPATCHES between two modes, and every field flows into
// the result it selects — none are inert:
//
//   - algorithm == "all" (the default when omitted/empty) runs the canonical #91
//     six-router demand sweep, benchmark.RunSweep: it routes ALL six routers across
//     the FOUR v/c levels and SWEEPS cost.BPR.CapacityScale as its axis, so it reads
//     Seed and RequestCount and derives α/β/capacity-scale per level itself. In this
//     mode the request's α/β/CapacityScale do NOT alter the grid (the sweep owns
//     those); they remain part of the cache identity so the response echoes the
//     request faithfully. This path is unchanged and regression-safe.
//
//   - algorithm ∈ benchmark.RouterOrder (naive, reactive, incremental, msa,
//     systemoptimal, multipath) runs SINGLE-ALGORITHM mode, benchmark.RunSingle: it
//     builds ONE cost.BPR from the request's α/β/CapacityScale and routes that one
//     named router (plus the systemoptimal reference, for an honest PoA) at a single
//     level PINNED to the client's CapacityScale. Here α/β/CapacityScale actually
//     drive the cost function — two runs differing only in those params return
//     DIFFERENT metrics. This is the surface the Phase-6 parameter sliders (#104)
//     expose.
//
// Any other `algorithm` value is rejected at validate() time as a clean 400, never
// run. This keeps the endpoint a faithful composition over the existing harness
// (RunSweep / RunSingle) rather than a reimplementation; see docs/api.md.
type benchmarkParams struct {
	// Algorithm dispatches the run: "" / "all" is the full six-router sweep (the
	// harness default); one of benchmark.RouterOrder selects single-algorithm mode.
	Algorithm string `json:"algorithm"`
	// Alpha and Beta are the BPR coefficients (defaults 0.15 / 4 when zero).
	Alpha float64 `json:"alpha"`
	Beta  float64 `json:"beta"`
	// CapacityScale is the §R3 capacity knob (default 1.0 when zero).
	CapacityScale float64 `json:"capacity_scale"`
	// RequestCount is the per-level OD request count R (default DefaultODCount).
	RequestCount int `json:"request_count"`
	// Seed is the fixed RNG seed making the run reproducible (default 0).
	Seed int64 `json:"seed"`
}

// withDefaults returns a copy of p with zero fields filled from the harness
// defaults, so the cache key and the run agree on the effective tuple (two POSTs
// that differ only in an omitted-vs-explicit default hit the same cache entry).
func (p benchmarkParams) withDefaults() benchmarkParams {
	out := p
	if strings.TrimSpace(out.Algorithm) == "" {
		out.Algorithm = "all"
	}
	if out.Alpha == 0 {
		out.Alpha = 0.15
	}
	if out.Beta == 0 {
		out.Beta = 4
	}
	if out.CapacityScale == 0 {
		out.CapacityScale = 1.0
	}
	if out.RequestCount == 0 {
		out.RequestCount = benchmark.DefaultODCount
	}
	return out
}

// cacheKey is the canonical string form of the §R6 tuple, used to dedupe jobs. It
// is built from the defaulted params so the identity is stable regardless of which
// fields the client omitted.
func (p benchmarkParams) cacheKey() string {
	d := p.withDefaults()
	return fmt.Sprintf("algo=%s|a=%g|b=%g|cap=%g|n=%d|seed=%d",
		d.Algorithm, d.Alpha, d.Beta, d.CapacityScale, d.RequestCount, d.Seed)
}

// validate rejects out-of-contract params with a client-facing message, so a bad
// tuple is a clean 400 rather than a panic deep in the harness (NewBPR panics on
// a non-positive capacity scale; RunSweep would mis-size a negative count).
func (p benchmarkParams) validate() error {
	d := p.withDefaults()
	// algorithm must be "all" (the six-router sweep) or one of the six router names
	// (single-algorithm mode). Anything else is a client typo, rejected here as a 400
	// rather than dispatched into a run that has no such mode.
	if d.Algorithm != "all" && !benchmark.IsRouter(d.Algorithm) {
		return fmt.Errorf("algorithm must be \"all\" or one of %s", strings.Join(benchmark.RouterOrder, ", "))
	}
	if d.Alpha < 0 {
		return fmt.Errorf("alpha must be >= 0")
	}
	if d.Beta < 0 {
		return fmt.Errorf("beta must be >= 0")
	}
	if d.CapacityScale <= 0 {
		return fmt.Errorf("capacity_scale must be > 0")
	}
	if d.RequestCount < 0 {
		return fmt.Errorf("request_count must be >= 0")
	}
	if d.RequestCount > maxRequestCount {
		return fmt.Errorf("request_count must be <= %d", maxRequestCount)
	}
	return nil
}

// job is one benchmark run's tracked state. It is guarded by jobStore.mu; a
// handler never reads its fields without the lock.
type job struct {
	ID     string
	Params benchmarkParams
	Status jobStatus
	// Report is the harness output, populated when Status == done.
	Report *benchmark.Report
	// Err is the failure message when Status == failed (client-safe, no PII).
	Err string
}

// jobStore is the in-process registry of benchmark jobs: it maps a job id to its
// state and a §R6 cache key to the job that ran (or is running) that tuple, so a
// repeat POST returns the existing job instead of launching a duplicate
// systemoptimal sweep. It guards its maps with one mutex; the long-running sweep
// itself runs OUTSIDE the lock (the lock is held only to read/swap state), so a
// running benchmark never blocks a status poll.
//
// The store is BOUNDED: order tracks insertion order so that, once byID reaches
// maxJobs, the oldest COMPLETED job is evicted to make room — the maps cannot
// grow without bound as a caller varies the tuple. Running jobs are never evicted
// (their sweep is still writing back), but the concurrency cap keeps the running
// set tiny relative to maxJobs, so room is always reclaimable.
type jobStore struct {
	mu      sync.Mutex
	byID    map[string]*job
	byKey   map[string]string // cache key -> job id
	order   []string          // job ids in insertion order, for FIFO eviction
	maxJobs int
}

func newJobStore(maxJobs int) *jobStore {
	return &jobStore{
		byID:    make(map[string]*job),
		byKey:   make(map[string]string),
		maxJobs: maxJobs,
	}
}

// getOrCreate returns the existing job for params' cache key (cached or still
// running) and created=false, or registers a new running job and returns
// created=true. It is the dedupe point: the caller launches the sweep only when
// created is true, so the §R6 tuple maps to at most one run. When the store is at
// capacity it first evicts the oldest completed job (see evictOldestDoneLocked).
//
// It returns the job's id, status, and params BY VALUE (read under the lock) so
// the caller never reads the shared *job's fields off-lock — the async sweep
// mutates that job through complete() concurrently, so the handler must render
// its response from these copies, not from the live struct.
func (st *jobStore) getOrCreate(params benchmarkParams, id string) (gotID string, status jobStatus, gotParams benchmarkParams, created bool) {
	st.mu.Lock()
	defer st.mu.Unlock()
	key := params.cacheKey()
	if existingID, ok := st.byKey[key]; ok {
		j := st.byID[existingID]
		return j.ID, j.Status, j.Params, false
	}
	if st.maxJobs > 0 && len(st.byID) >= st.maxJobs {
		st.evictOldestDoneLocked()
	}
	j := &job{ID: id, Params: params.withDefaults(), Status: statusRunning}
	st.byID[id] = j
	st.byKey[key] = id
	st.order = append(st.order, id)
	return j.ID, j.Status, j.Params, true
}

// evictOldestDoneLocked removes the oldest job that is no longer running, freeing
// a store slot (and its retained Report). It is a no-op if every tracked job is
// still running — which the concurrency cap makes effectively impossible, since
// maxConcurrentSweeps is far below maxBenchmarkJobs. The caller must hold st.mu.
func (st *jobStore) evictOldestDoneLocked() {
	for i, id := range st.order {
		j, ok := st.byID[id]
		if ok && j.Status == statusRunning {
			continue
		}
		st.order = append(st.order[:i:i], st.order[i+1:]...)
		st.deleteLocked(id, j)
		return
	}
}

// deleteLocked drops a job from byID and, when byKey still points at it, byKey.
// It does not touch order (the caller owns that). The caller must hold st.mu.
func (st *jobStore) deleteLocked(id string, j *job) {
	delete(st.byID, id)
	if j != nil {
		if curID, ok := st.byKey[j.Params.cacheKey()]; ok && curID == id {
			delete(st.byKey, j.Params.cacheKey())
		}
	}
}

// remove drops a job entirely (byID, byKey, and order). It is the rollback path
// for a job that was created but could not be started (no sweep capacity), so a
// reserved-but-never-run job does not linger as a permanently "running" entry.
func (st *jobStore) remove(id string) {
	st.mu.Lock()
	defer st.mu.Unlock()
	j := st.byID[id]
	for i, oid := range st.order {
		if oid == id {
			st.order = append(st.order[:i:i], st.order[i+1:]...)
			break
		}
	}
	st.deleteLocked(id, j)
}

// complete records a finished sweep's report (or error) under the lock. A nil
// report with a non-empty errMsg marks the job failed; otherwise it is done. On
// failure it also drops the cache mapping for the tuple so the SAME params can be
// retried: a later POST starts a fresh run rather than returning this stuck
// failure forever. The failed job itself stays in byID (so a client already
// holding its id still polls the failure) until FIFO eviction reclaims it.
func (st *jobStore) complete(id string, report *benchmark.Report, errMsg string) {
	st.mu.Lock()
	defer st.mu.Unlock()
	j, ok := st.byID[id]
	if !ok {
		return
	}
	if errMsg != "" {
		j.Status = statusFailed
		j.Err = errMsg
		if curID, ok := st.byKey[j.Params.cacheKey()]; ok && curID == id {
			delete(st.byKey, j.Params.cacheKey())
		}
		return
	}
	j.Status = statusDone
	j.Report = report
}

// snapshot copies a job's current state under the lock so the handler renders a
// consistent view without holding the lock across JSON marshaling.
func (st *jobStore) snapshot(id string) (status jobStatus, params benchmarkParams, report *benchmark.Report, errMsg string, ok bool) {
	st.mu.Lock()
	defer st.mu.Unlock()
	j, found := st.byID[id]
	if !found {
		return "", benchmarkParams{}, nil, "", false
	}
	return j.Status, j.Params, j.Report, j.Err, true
}

// benchmarkStartResponse is the POST /benchmark body: the job id to poll plus the
// status, so a client knows immediately whether a fresh run started (running) or
// a cached identical run was returned (running/done/failed).
type benchmarkStartResponse struct {
	JobID  string          `json:"job_id"`
	Status jobStatus       `json:"status"`
	Params benchmarkParams `json:"params"`
}

// handleBenchmark serves POST /benchmark: it parses the §R6 tuple, returns a job
// id IMMEDIATELY, and runs the #91 harness async — the request NEVER blocks on a
// systemoptimal sweep. A repeat POST with the same tuple returns the existing job
// (the §R6 cache) instead of launching a duplicate run. The body is optional; an
// empty body runs the canonical harness defaults.
func (s *Server) handleBenchmark(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.writeError(w, endpointBenchmark, http.StatusMethodNotAllowed, "method not allowed: use POST")
		return
	}

	params, err := decodeBenchmarkParams(r.Body)
	if err != nil {
		s.writeError(w, endpointBenchmark, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}
	if err := params.validate(); err != nil {
		s.writeError(w, endpointBenchmark, http.StatusBadRequest, err.Error())
		return
	}

	id, err := newJobID()
	if err != nil {
		s.logger.Error("generate job id failed", "endpoint", endpointBenchmark, "err", err)
		s.writeError(w, endpointBenchmark, http.StatusInternalServerError, "could not start benchmark")
		return
	}

	jobID, status, jobParams, created := s.jobs.getOrCreate(params, id)
	if created {
		// A fresh tuple needs a sweep slot. Reserve one without blocking; if the
		// box is already running maxConcurrentSweeps, roll the just-created job back
		// (so it does not linger as a never-completing "running" entry) and tell the
		// client to retry rather than oversubscribing CPU. The goroutine releases
		// the slot when the sweep finishes.
		select {
		case s.sweepSlots <- struct{}{}:
			s.runBenchmarkAsync(jobID, jobParams)
		default:
			s.jobs.remove(jobID)
			s.writeError(w, endpointBenchmark, http.StatusServiceUnavailable,
				"benchmark capacity reached, please retry shortly")
			return
		}
	}

	s.writeJSON(w, endpointBenchmark, http.StatusAccepted, benchmarkStartResponse{
		JobID:  jobID,
		Status: status,
		Params: jobParams,
	})
}

// sweepFunc is the seam runBenchmarkAsync runs the "all" (six-router sweep) path
// through: in production it is benchmark.RunSweep, and tests substitute a stub to
// exercise the failure, panic, and capacity paths without launching a real (slow)
// six-router sweep. It deliberately carries ONLY (seed, count) — the sweep owns
// α/β/capacity_scale (it sweeps capacity_scale as its axis), so widening this seam
// would only add params the sweep ignores. Single-algorithm mode does NOT go through
// this seam: it calls benchmark.RunSingle directly with the full param tuple (see
// runBenchmarkAsync), so the seam stays exactly the shape the existing tests stub.
type sweepFunc func(ctx context.Context, g graph.Graph, seed int64, count int) ([]benchmark.SweepCell, error)

// defaultSweep is the production "all" path: the canonical #91 six-router demand sweep.
var defaultSweep sweepFunc = benchmark.RunSweep

// runBenchmarkAsync launches the harness for the job in a goroutine and records
// the result on completion. It DISPATCHES on params.Algorithm: "all" runs the
// six-router sweep through the s.sweepFn seam (so the existing failure/panic/capacity
// tests stub exactly that path); any other algorithm — validated to be one of
// benchmark.RouterOrder — runs single-algorithm mode via benchmark.RunSingle with the
// full α/β/capacity_scale tuple, so those params actually drive the cost function.
//
// It uses context.Background() (NOT the request's context) so a client disconnecting
// after the immediate job-id response does not cancel an in-flight run — the job runs
// to completion and is cached for the next poll. A panic in the harness is recovered
// and recorded as a failed job rather than crashing the server. The caller has
// already reserved a sweep slot; this goroutine releases it on exit so the next
// queued POST can start.
func (s *Server) runBenchmarkAsync(id string, params benchmarkParams) {
	go func() {
		defer func() {
			<-s.sweepSlots
			if rec := recover(); rec != nil {
				s.logger.Error("benchmark job panicked", "endpoint", endpointBenchmark, "job_id", id, "panic", rec)
				s.jobs.complete(id, nil, "benchmark failed")
			}
		}()

		cells, err := s.runBenchmarkCells(params)
		if err != nil {
			s.logger.Error("benchmark run failed", "endpoint", endpointBenchmark, "job_id", id, "err", err)
			s.jobs.complete(id, nil, "benchmark failed")
			return
		}
		report := benchmark.BuildReport(params.Seed, params.RequestCount, cells)
		s.jobs.complete(id, &report, "")
		s.logger.Info("benchmark job complete", "endpoint", endpointBenchmark, "job_id", id,
			"algorithm", params.Algorithm, "cells", len(cells))
	}()
}

// runBenchmarkCells produces the comparison cells for a (validated) job's params,
// dispatching on Algorithm. "all" goes through the s.sweepFn test seam (the
// six-router sweep, regression-safe); a named router runs single-algorithm mode,
// building one BPR from the client's α/β/capacity_scale. params.Algorithm is assumed
// already defaulted+validated by the handler, so RunSingle only ever sees a name in
// benchmark.RouterOrder here.
func (s *Server) runBenchmarkCells(params benchmarkParams) ([]benchmark.SweepCell, error) {
	if params.Algorithm == "all" {
		return s.sweepFn(context.Background(), s.graph, params.Seed, params.RequestCount)
	}
	return benchmark.RunSingle(context.Background(), s.graph, params.Seed, params.RequestCount,
		params.Alpha, params.Beta, params.CapacityScale, params.Algorithm)
}

// benchmarkStatusResponse is the GET /benchmark/{id} body: the job's status, the
// §R6 tuple it ran, the cached report when done, and a client-safe error when
// failed. report is omitted while running.
type benchmarkStatusResponse struct {
	JobID  string            `json:"job_id"`
	Status jobStatus         `json:"status"`
	Params benchmarkParams   `json:"params"`
	Report *benchmark.Report `json:"report,omitempty"`
	Error  string            `json:"error,omitempty"`
}

// handleBenchmarkStatus serves GET /benchmark/{id}: it returns the job's
// status and, when done, the cached #91 metrics. An unknown id is a clean 404; a
// still-running job returns 200 with status "running" and no report.
func (s *Server) handleBenchmarkStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.writeError(w, endpointBenchmarkStatus, http.StatusMethodNotAllowed, "method not allowed: use GET")
		return
	}

	id := strings.TrimPrefix(r.URL.Path, "/benchmark/")
	if id == "" || strings.Contains(id, "/") {
		s.writeError(w, endpointBenchmarkStatus, http.StatusBadRequest, "missing or malformed job id")
		return
	}

	status, params, report, errMsg, ok := s.jobs.snapshot(id)
	if !ok {
		s.writeError(w, endpointBenchmarkStatus, http.StatusNotFound, "no such benchmark job")
		return
	}

	s.writeJSON(w, endpointBenchmarkStatus, http.StatusOK, benchmarkStatusResponse{
		JobID:  id,
		Status: status,
		Params: params,
		Report: report,
		Error:  errMsg,
	})
}

// decodeBenchmarkParams decodes the optional JSON request body into a
// benchmarkParams. An empty body is the canonical defaults (not an error), so a
// bare `POST /benchmark` runs the standard harness. Unknown fields are rejected
// so a typo'd parameter fails loudly rather than silently running the default.
func decodeBenchmarkParams(body io.Reader) (benchmarkParams, error) {
	var params benchmarkParams
	if body == nil {
		return params, nil
	}
	dec := json.NewDecoder(io.LimitReader(body, maxBenchmarkBodyBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&params); err != nil {
		if err == io.EOF {
			// Empty body: run the defaults.
			return benchmarkParams{}, nil
		}
		return benchmarkParams{}, err
	}
	return params, nil
}

// maxBenchmarkBodyBytes bounds the request body so a runaway POST cannot exhaust
// memory; the tuple is tiny, so a small cap is generous.
const maxBenchmarkBodyBytes = 1 << 16

// newJobID returns a random, URL-safe job id. Crypto-random (not a counter) so a
// client cannot enumerate or guess other clients' job ids.
func newJobID() (string, error) {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf[:]), nil
}
