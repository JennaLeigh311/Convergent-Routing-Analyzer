// POST /benchmark + GET /benchmark/{id} client for the before/after PoA panel (#102).
//
// The benchmark report is the SOURCE OF TRUTH for the money-shot numbers: the engine
// already computes the realized-time Price of Anarchy per demand level and the
// per-(router, level) mean/p95/total aggregates, so the frontend renders those
// verbatim rather than re-deriving a PoA from the live stream. POST /benchmark starts
// (or returns the cached) async sweep and 202s a job id; the report only arrives by
// polling GET /benchmark/{id} until status === "done" (a POST never carries the
// report itself), so runBenchmark always starts-then-polls.
//
// This client is deliberately small and self-contained so issue #104 (debounced
// async controls + result caching) can build its layer ON TOP of it without a
// rewrite: the param/response types mirror engine/internal/api/benchmark.go and the
// report types mirror engine/internal/benchmark, narrowed to the fields the panel
// consumes. The selectors (peakPoaLevel / selectLevelCells) are pure and tested.

import { restUrl } from "./engine";

/** A benchmark job's lifecycle state (engine: jobStatus). */
export type JobStatus = "running" | "done" | "failed";

/**
 * The §R6 parameter tuple POST /benchmark is keyed by. All optional: an omitted
 * field takes the engine's default, and an empty/absent algorithm runs the canonical
 * six-router sweep ("all") — which is what the before/after panel wants (it needs the
 * naive vs systemoptimal per-level PoA the sweep produces).
 */
export interface BenchmarkParams {
  algorithm?: string;
  alpha?: number;
  beta?: number;
  capacity_scale?: number;
  request_count?: number;
  seed?: number;
}

/** The echoed, fully-defaulted tuple the engine ran (benchmarkParams). */
export interface ResolvedBenchmarkParams {
  algorithm: string;
  alpha: number;
  beta: number;
  capacity_scale: number;
  request_count: number;
  seed: number;
}

/** POST /benchmark body: the job id to poll plus the initial status. */
export interface StartResponse {
  job_id: string;
  status: JobStatus;
  params: ResolvedBenchmarkParams;
}

/** GET /benchmark/{id} body: status, the §R6 tuple, the report when done, error when failed. */
export interface StatusResponse {
  job_id: string;
  status: JobStatus;
  params: ResolvedBenchmarkParams;
  report?: BenchmarkReport;
  error?: string;
}

// ---- Report types (mirror engine/internal/benchmark) -------------------------

/** One (router, demand level) cell's realized-time metric bundle (benchmark.Result). */
export interface Result {
  router: string;
  demand_level: string;
  mean_realized_s: number;
  p95_realized_s: number;
  total_network_time_s: number;
  gini_vc: number;
  max_vc: number;
  iters: number;
  converged: boolean;
  gap: number;
}

/** One comparison-grid cell: the Result plus its cross-router PoA + sim columns. */
export interface SweepCell {
  result: Result;
  capacity_scale: number;
  target_vc: number;
  poa: number;
  sim_mean_realized_s: number;
  sim_p95_realized_s: number;
}

/** One demand level's headline naive-vs-systemoptimal Price of Anarchy. */
export interface LevelPoA {
  demand_level: string;
  target_vc: number;
  poa: number;
}

/** The headline improvement %, with the demand level it was measured at. */
export interface Improvement {
  demand_level: string;
  target_vc: number;
  best_router: string;
  percent_reduction: number;
  naive_total_s: number;
  best_total_s: number;
}

/** The full benchmark artifact (benchmark.Report). */
export interface BenchmarkReport {
  seed: number;
  od_count: number;
  total_demand_vph: number;
  cells: SweepCell[];
  poa_by_level: LevelPoA[];
  headline_improvement: Improvement;
}

// ---- HTTP ---------------------------------------------------------------------

/** POST /benchmark: start (or fetch the cached) sweep and return its job id. */
export async function startBenchmark(
  params: BenchmarkParams = {},
  signal?: AbortSignal,
): Promise<StartResponse> {
  const res = await fetch(restUrl("/benchmark"), {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(params),
    signal,
  });
  if (!res.ok) {
    throw new Error(`POST /benchmark failed: ${res.status} ${res.statusText}`);
  }
  return (await res.json()) as StartResponse;
}

/** GET /benchmark/{id}: poll one job's status (and report, once done). */
export async function fetchBenchmarkStatus(
  jobId: string,
  signal?: AbortSignal,
): Promise<StatusResponse> {
  const res = await fetch(restUrl(`/benchmark/${jobId}`), { signal });
  if (!res.ok) {
    throw new Error(`GET /benchmark/${jobId} failed: ${res.status} ${res.statusText}`);
  }
  return (await res.json()) as StatusResponse;
}

/** Options for runBenchmark's poll loop. */
export interface RunBenchmarkOptions {
  signal?: AbortSignal;
  /** Delay between status polls (ms). */
  pollIntervalMs?: number;
  /** Hard cap on poll attempts so a stuck job cannot loop forever. */
  maxPolls?: number;
}

/** Reject if the signal is already aborted; otherwise resolve after ms (abortable). */
function sleep(ms: number, signal?: AbortSignal): Promise<void> {
  return new Promise((resolve, reject) => {
    if (signal?.aborted) {
      reject(new DOMException("aborted", "AbortError"));
      return;
    }
    const timer = setTimeout(() => {
      signal?.removeEventListener("abort", onAbort);
      resolve();
    }, ms);
    const onAbort = () => {
      clearTimeout(timer);
      reject(new DOMException("aborted", "AbortError"));
    };
    signal?.addEventListener("abort", onAbort, { once: true });
  });
}

/**
 * Start a benchmark and poll until it completes, resolving with the report. A POST
 * never carries the report (it only reserves/looks up the job), so this always polls
 * GET /benchmark/{id} at pollIntervalMs until status is "done" (resolve) or "failed"
 * (throw the engine's client-safe error). It is abortable via opts.signal and bounded
 * by maxPolls so a never-finishing job rejects rather than spinning forever. This is
 * the single entry point #104's debounced/cached controls reuse.
 */
export async function runBenchmark(
  params: BenchmarkParams = {},
  opts: RunBenchmarkOptions = {},
): Promise<BenchmarkReport> {
  const { signal, pollIntervalMs = 750, maxPolls = 240 } = opts;
  const start = await startBenchmark(params, signal);

  for (let i = 0; i < maxPolls; i++) {
    const status = await fetchBenchmarkStatus(start.job_id, signal);
    if (status.status === "done") {
      if (!status.report) {
        throw new Error("benchmark reported done but carried no report");
      }
      return status.report;
    }
    if (status.status === "failed") {
      throw new Error(status.error || "benchmark failed");
    }
    await sleep(pollIntervalMs, signal);
  }
  throw new Error(`benchmark did not finish within ${maxPolls} polls`);
}

// ---- Pure selectors over a report --------------------------------------------

/**
 * The PEAK per-level Price of Anarchy — the honest single money-shot number. PoA
 * peaks at moderate load and → 1 at both extremes (project-spec.md §5), so the peak
 * across the four sweep levels (carrying its demand level) is the figure to headline,
 * never a cherry-picked one. Returns null for a report with no per-level PoA.
 */
export function peakPoaLevel(report: BenchmarkReport): LevelPoA | null {
  let best: LevelPoA | null = null;
  for (const lvl of report.poa_by_level) {
    if (best === null || lvl.poa > best.poa) best = lvl;
  }
  return best;
}

/**
 * The cells at one demand level, indexed by router name. Used to pull naive (before)
 * vs reactive (after) vs systemoptimal (reference) for the mean/p95/total bar charts
 * at the headline level, straight from the report — no recomputation.
 */
export function selectLevelCells(
  report: BenchmarkReport,
  demandLevel: string,
): Record<string, SweepCell> {
  const out: Record<string, SweepCell> = {};
  for (const cell of report.cells) {
    if (cell.result.demand_level === demandLevel) {
      out[cell.result.router] = cell;
    }
  }
  return out;
}
