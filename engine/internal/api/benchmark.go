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

// benchmarkParams is the §R6 parameter tuple a /benchmark run is keyed by:
// (algorithm, α, β, capacity_scale, requestCount, seed). It is BOTH the request
// body and the cache key — two POSTs with the same tuple return the SAME job
// (the cached result) rather than re-running an expensive systemoptimal sweep.
//
// Note on which fields the #91 harness actually consumes: benchmark.RunSweep is
// the canonical six-router demand sweep — it runs ALL routers across the four v/c
// levels and derives the BPR's α/β/capacity-scale itself (it SWEEPS capacity
// scale as its axis). Seed and RequestCount are what it reads; Algorithm, Alpha,
// Beta, and CapacityScale are carried in the tuple so the cache key matches the
// §R6 contract and the response echoes the request, but they parameterize the
// CACHE IDENTITY, not RunSweep's internal sweep. When a future single-algorithm
// benchmark mode lands, those fields drive it; today they are validated and keyed
// on. This keeps the endpoint a faithful composition over the existing harness
// rather than a reimplementation.
type benchmarkParams struct {
	// Algorithm is the router under test ("" / "all" means the full six-router
	// sweep, the harness default).
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
// systemoptimal sweep. It guards both maps with one mutex; the long-running sweep
// itself runs OUTSIDE the lock (the lock is held only to read/swap state), so a
// running benchmark never blocks a status poll.
type jobStore struct {
	mu    sync.Mutex
	byID  map[string]*job
	byKey map[string]string // cache key -> job id
}

func newJobStore() *jobStore {
	return &jobStore{
		byID:  make(map[string]*job),
		byKey: make(map[string]string),
	}
}

// getOrCreate returns the existing job for params' cache key (cached or still
// running) and created=false, or registers a new running job and returns
// created=true. It is the dedupe point: the caller launches the sweep only when
// created is true, so the §R6 tuple maps to at most one run.
func (st *jobStore) getOrCreate(params benchmarkParams, id string) (j *job, created bool) {
	st.mu.Lock()
	defer st.mu.Unlock()
	key := params.cacheKey()
	if existingID, ok := st.byKey[key]; ok {
		return st.byID[existingID], false
	}
	j = &job{ID: id, Params: params.withDefaults(), Status: statusRunning}
	st.byID[id] = j
	st.byKey[key] = id
	return j, true
}

// complete records a finished sweep's report (or error) under the lock. A nil
// report with a non-empty errMsg marks the job failed; otherwise it is done.
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

	j, created := s.jobs.getOrCreate(params, id)
	if created {
		s.runBenchmarkAsync(j.ID, j.Params)
	}

	s.writeJSON(w, endpointBenchmark, http.StatusAccepted, benchmarkStartResponse{
		JobID:  j.ID,
		Status: j.Status,
		Params: j.Params,
	})
}

// runBenchmarkAsync launches the #91 harness for the job in a goroutine and
// records the result on completion. It uses context.Background() (NOT the
// request's context) so a client disconnecting after the immediate job-id
// response does not cancel an in-flight sweep — the job runs to completion and is
// cached for the next poll. A panic in the harness is recovered and recorded as a
// failed job rather than crashing the server.
func (s *Server) runBenchmarkAsync(id string, params benchmarkParams) {
	go func() {
		defer func() {
			if rec := recover(); rec != nil {
				s.logger.Error("benchmark job panicked", "endpoint", endpointBenchmark, "job_id", id, "panic", rec)
				s.jobs.complete(id, nil, "benchmark failed")
			}
		}()

		cells, err := benchmark.RunSweep(context.Background(), s.graph, params.Seed, params.RequestCount)
		if err != nil {
			s.logger.Error("benchmark sweep failed", "endpoint", endpointBenchmark, "job_id", id, "err", err)
			s.jobs.complete(id, nil, "benchmark failed")
			return
		}
		report := benchmark.BuildReport(params.Seed, params.RequestCount, cells)
		s.jobs.complete(id, &report, "")
		s.logger.Info("benchmark job complete", "endpoint", endpointBenchmark, "job_id", id, "cells", len(cells))
	}()
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
