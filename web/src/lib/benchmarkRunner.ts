// Benchmark request controller (issue #104) — the glue between a settled §R6 tuple and
// the async /benchmark client. It is framework-agnostic (it takes a store handle, not a
// React hook) so the cache-dedupe + stale-run-cancellation logic is unit-testable in
// node without rendering a component; the React layer (hooks/useBenchmarkControls) is a
// thin wrapper that instantiates one against the app store.
//
// Its whole job is the issue's acceptance contract:
//   - A repeated identical tuple NEVER re-fires a job: an exact-key repeat is ignored,
//     and a cached result renders straight from the client cache (no POST).
//   - Only the latest settled tuple wins: a new request aborts the previous in-flight
//     run, so a stale poll can't clobber the displayed result.
//   - A completed run is ALWAYS cached (even if superseded), so returning to a prior
//     tuple is a cache hit rather than a fresh job.

import {
  BenchmarkError,
  benchmarkKey,
  runBenchmark,
  type BenchmarkReport,
  type BenchmarkTuple,
  type RunBenchmarkOptions,
} from "./benchmark";

/** The store slice the controller reads/writes — the app store satisfies this. */
export interface BenchmarkStoreApi {
  getState: () => BenchmarkStoreSlice;
}

/** The benchmark actions + cache the controller drives (a subset of the app store). */
export interface BenchmarkStoreSlice {
  benchCache: Record<string, BenchmarkReport>;
  benchRequestStart: (key: string, params: BenchmarkTuple) => void;
  benchResolve: (key: string, report: BenchmarkReport) => void;
  benchReject: (key: string, message: string) => void;
  benchUseCached: (key: string, report: BenchmarkReport, params: BenchmarkTuple) => void;
}

/** A live controller: fire a settled tuple, or tear down on unmount. */
export interface BenchmarkController {
  /** Request a benchmark for a settled tuple (cache-checked, deduped, abortable). */
  request: (params: BenchmarkTuple) => void;
  /** Abort any in-flight run and reset (call on unmount). */
  dispose: () => void;
}

/** Construction seams; tests inject `run` to avoid real fetches/timers. */
export interface BenchmarkControllerOptions {
  run?: (params: BenchmarkTuple, opts?: RunBenchmarkOptions) => Promise<BenchmarkReport>;
  runOptions?: RunBenchmarkOptions;
}

export function createBenchmarkController(
  store: BenchmarkStoreApi,
  options: BenchmarkControllerOptions = {},
): BenchmarkController {
  const run = options.run ?? runBenchmark;
  let activeKey: string | null = null;
  let inFlight: AbortController | null = null;

  function request(params: BenchmarkTuple): void {
    const key = benchmarkKey(params);

    // Already showing/running this exact tuple — never re-fire (handles a settle that
    // lands on the same value, and a slider released back to where it started).
    if (key === activeKey) return;
    activeKey = key;

    // Client cache hit — render the cached report without firing a job.
    const cached = store.getState().benchCache[key];
    if (cached) {
      inFlight?.abort();
      inFlight = null;
      store.getState().benchUseCached(key, cached, params);
      return;
    }

    // Fresh tuple — cancel any stale run so its late poll can't clobber this one.
    inFlight?.abort();
    const ctrl = new AbortController();
    inFlight = ctrl;
    store.getState().benchRequestStart(key, params);

    run(params, { ...options.runOptions, signal: ctrl.signal })
      .then((report) => {
        // Always cache, even if a newer request has since superseded this key; the
        // store only flips the VISIBLE status when this key is still the active one.
        store.getState().benchResolve(key, report);
      })
      .catch((err: unknown) => {
        // A run we deliberately aborted is not a user-facing failure.
        if (ctrl.signal.aborted) return;
        // A failed run is not cached, so it must stay retryable: clear activeKey (only
        // if this run is still the active one) so re-settling the SAME tuple re-fires
        // rather than being dropped by the `key === activeKey` dedupe above. Without
        // this a transient failure (503/network, or the on-mount default run while the
        // engine is briefly down) would stick until the user moved to another tuple.
        if (activeKey === key) activeKey = null;
        const message =
          err instanceof BenchmarkError || err instanceof Error ? err.message : "benchmark failed";
        store.getState().benchReject(key, message);
      });
  }

  function dispose(): void {
    inFlight?.abort();
    inFlight = null;
    activeKey = null;
  }

  return { request, dispose };
}
