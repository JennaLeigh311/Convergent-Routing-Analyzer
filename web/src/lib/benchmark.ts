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
import { ROUTER_ORDER, type Algo } from "./protocol";

/** A benchmark job's lifecycle state (engine: jobStatus). */
export type JobStatus = "running" | "done" | "failed";

// ---- §R6 parameter axis (issue #104) -----------------------------------------

/** The benchmark algorithm axis: "all" (six-router sweep) or one named router. */
export type BenchAlgo = "all" | Algo;

/** Selectable algorithm options for the #104 picker — engine order, "all" first. */
export const BENCH_ALGO_OPTIONS: readonly BenchAlgo[] = ["all", ...ROUTER_ORDER];

/**
 * The fully-defaulted §R6 tuple the #104 controls drive. It is identical in shape to
 * ResolvedBenchmarkParams (the engine's echoed params) but narrows `algorithm` to the
 * BenchAlgo union the picker offers. A full tuple is assignable to the all-optional
 * BenchmarkParams the HTTP client accepts, so it serializes verbatim as the POST body.
 */
export interface BenchmarkTuple extends ResolvedBenchmarkParams {
  algorithm: BenchAlgo;
}

/** The harness defaults, mirrored from benchmark.go withDefaults / DefaultODCount. */
export const DEFAULT_BENCH_PARAMS: BenchmarkTuple = {
  algorithm: "all",
  alpha: 0.15,
  beta: 4,
  capacity_scale: 1.0,
  request_count: 1000,
  seed: 0,
};

/**
 * Fill zero/empty fields from the harness defaults, mirroring the engine's
 * withDefaults() so the CLIENT cache key matches the SERVER's dedupe identity. The
 * contract treats a zero as "use the default" — e.g. alpha 0 is NOT a requestable BPR
 * coefficient (the engine maps it to 0.15) — so alpha 0 and alpha 0.15 are the SAME
 * tuple and must collapse to one cache entry here too.
 */
export function withBenchDefaults(p: BenchmarkTuple): BenchmarkTuple {
  return {
    algorithm: p.algorithm && String(p.algorithm).trim() !== "" ? p.algorithm : "all",
    alpha: p.alpha === 0 ? 0.15 : p.alpha,
    beta: p.beta === 0 ? 4 : p.beta,
    capacity_scale: p.capacity_scale === 0 ? 1.0 : p.capacity_scale,
    request_count: p.request_count === 0 ? 1000 : p.request_count,
    seed: p.seed,
  };
}

/**
 * Canonical string key for the §R6 tuple, the client-side cache key. Built from the
 * defaulted params so two tuples differing only in an omitted-vs-explicit default
 * collapse to one key — the same dedupe the server performs, so a repeat settle on an
 * identical tuple never re-fires a job (issue #104 acceptance).
 */
export function benchmarkKey(p: BenchmarkTuple): string {
  const d = withBenchDefaults(p);
  return `algo=${d.algorithm}|a=${d.alpha}|b=${d.beta}|cap=${d.capacity_scale}|n=${d.request_count}|seed=${d.seed}`;
}

// ---- classified errors -------------------------------------------------------

/** Classifies a benchmark failure so the UI can surface it specifically (#104). */
export type BenchErrorKind =
  | "bad_params" // 400: out-of-contract tuple
  | "capacity" // 503: box at max concurrent sweeps
  | "job_failed" // job ran but the harness failed (or finished without a report)
  | "not_found" // 404: polled an unknown/evicted job id
  | "network" // fetch threw (offline, CORS, …)
  | "unknown"; // any other non-OK status / timeout

/** A benchmark error carrying its classification and the originating HTTP status. */
export class BenchmarkError extends Error {
  readonly kind: BenchErrorKind;
  readonly status?: number;
  constructor(kind: BenchErrorKind, message: string, status?: number) {
    super(message);
    this.name = "BenchmarkError";
    this.kind = kind;
    this.status = status;
  }
}

/** Pull the engine's `{ error: string }` message off a non-OK response, best effort. */
async function errorMessage(res: Response, fallback: string): Promise<string> {
  try {
    const body = (await res.json()) as { error?: unknown };
    if (body && typeof body.error === "string" && body.error.trim() !== "") {
      return body.error;
    }
  } catch {
    // non-JSON / empty body — fall through to the fallback.
  }
  return fallback;
}

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

/** POST /benchmark: start (or fetch the cached) sweep and return its job id.
 * Internal to runBenchmark; re-export if #104's controls ever need it directly. */
async function startBenchmark(
  params: BenchmarkParams = {},
  signal?: AbortSignal,
): Promise<StartResponse> {
  let res: Response;
  try {
    res = await fetch(restUrl("/benchmark"), {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(params),
      signal,
    });
  } catch (err) {
    if (signal?.aborted) throw err;
    const message = err instanceof Error ? err.message : "network error";
    throw new BenchmarkError("network", `POST /benchmark failed: ${message}`);
  }
  // Map the contract's failure statuses to a classified error so the caller can react
  // specifically (retry a 503, show a 400's reason) rather than swallowing them.
  if (res.status === 503) {
    throw new BenchmarkError("capacity", await errorMessage(res, "benchmark capacity reached, please retry shortly"), 503);
  }
  if (res.status === 400) {
    throw new BenchmarkError("bad_params", await errorMessage(res, "invalid benchmark parameters"), 400);
  }
  if (!res.ok) {
    throw new BenchmarkError("unknown", await errorMessage(res, `POST /benchmark failed: ${res.status} ${res.statusText}`), res.status);
  }
  return (await res.json()) as StartResponse;
}

/** GET /benchmark/{id}: poll one job's status (and report, once done).
 * Internal to runBenchmark; re-export if #104's controls ever need it directly. */
async function fetchBenchmarkStatus(
  jobId: string,
  signal?: AbortSignal,
): Promise<StatusResponse> {
  let res: Response;
  try {
    res = await fetch(restUrl(`/benchmark/${jobId}`), { signal });
  } catch (err) {
    if (signal?.aborted) throw err;
    const message = err instanceof Error ? err.message : "network error";
    throw new BenchmarkError("network", `GET /benchmark/${jobId} failed: ${message}`);
  }
  if (res.status === 404) {
    throw new BenchmarkError("not_found", await errorMessage(res, "no such benchmark job"), 404);
  }
  if (!res.ok) {
    throw new BenchmarkError("unknown", await errorMessage(res, `GET /benchmark/${jobId} failed: ${res.status} ${res.statusText}`), res.status);
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
  /** How many times to retry a 503 capacity rejection before surfacing it. */
  capacityRetries?: number;
  /** Base backoff between capacity retries (ms); grows linearly per attempt. */
  capacityBackoffMs?: number;
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
  const { signal, pollIntervalMs = 750, maxPolls = 240, capacityRetries = 3, capacityBackoffMs = 600 } = opts;

  // POST with bounded capacity retry/backoff: a 503 means the box is at its sweep cap
  // and asks the client to "retry shortly", so we back off briefly rather than failing
  // the run outright; any other error (or exhausting the retries) surfaces.
  let start: StartResponse | undefined;
  for (let attempt = 0; ; attempt++) {
    try {
      start = await startBenchmark(params, signal);
      break;
    } catch (err) {
      if (err instanceof BenchmarkError && err.kind === "capacity" && attempt < capacityRetries) {
        await sleep(capacityBackoffMs * (attempt + 1), signal);
        continue;
      }
      throw err;
    }
  }

  for (let i = 0; i < maxPolls; i++) {
    const status = await fetchBenchmarkStatus(start.job_id, signal);
    if (status.status === "done") {
      if (!status.report) {
        throw new BenchmarkError("job_failed", "benchmark reported done but carried no report");
      }
      return status.report;
    }
    if (status.status === "failed") {
      throw new BenchmarkError("job_failed", status.error || "benchmark failed");
    }
    await sleep(pollIntervalMs, signal);
  }
  throw new BenchmarkError("unknown", `benchmark did not finish within ${maxPolls} polls`);
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
