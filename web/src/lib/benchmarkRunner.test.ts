// Tests for the benchmark controller (#104) — the acceptance contract, exercised
// without React or a real network by injecting a fake `run` and a tiny in-memory store
// that mirrors the real store's benchmark actions:
//   - a fresh tuple fires exactly one run and transitions running -> done (report cached);
//   - a repeated identical tuple hits the cache and NEVER re-fires a run;
//   - a new tuple while one is pending aborts the previous run (latest wins);
//   - a failed run surfaces its message rather than being swallowed.

import { describe, expect, it, vi } from "vitest";

import {
  BenchmarkError,
  benchmarkKey,
  type BenchmarkReport,
  type BenchmarkTuple,
} from "./benchmark";
import { createBenchmarkController, type BenchmarkStoreSlice } from "./benchmarkRunner";

const PARAMS_A: BenchmarkTuple = {
  algorithm: "reactive",
  alpha: 0.2,
  beta: 4,
  capacity_scale: 1,
  request_count: 500,
  seed: 0,
};
const PARAMS_B: BenchmarkTuple = { ...PARAMS_A, alpha: 0.5 };

function fakeReport(seed: number): BenchmarkReport {
  return {
    seed,
    od_count: 500,
    total_demand_vph: 5000,
    cells: [],
    poa_by_level: [],
    headline_improvement: {
      demand_level: "vc0.9",
      target_vc: 0.9,
      best_router: "systemoptimal",
      percent_reduction: 0,
      naive_total_s: 0,
      best_total_s: 0,
    },
  };
}

/** A tiny store mirroring store.ts's benchmark slice semantics (active-key gated). */
function makeStore() {
  const state = {
    benchActiveKey: null as string | null,
    benchStatus: "idle" as "idle" | "running" | "done" | "failed",
    benchReport: null as BenchmarkReport | null,
    benchError: null as string | null,
    benchCache: {} as Record<string, BenchmarkReport>,
  };
  const slice: BenchmarkStoreSlice = {
    get benchCache() {
      return state.benchCache;
    },
    benchRequestStart: (key) => {
      state.benchActiveKey = key;
      state.benchStatus = "running";
      state.benchReport = null;
      state.benchError = null;
    },
    benchResolve: (key, report) => {
      if (!state.benchCache[key]) state.benchCache[key] = report;
      if (state.benchActiveKey !== key) return;
      state.benchReport = report;
      state.benchStatus = "done";
      state.benchError = null;
    },
    benchReject: (key, message) => {
      if (state.benchActiveKey !== key) return;
      state.benchStatus = "failed";
      state.benchError = message;
    },
    benchUseCached: (key, report) => {
      state.benchActiveKey = key;
      state.benchReport = report;
      state.benchStatus = "done";
      state.benchError = null;
    },
  };
  return { state, getState: () => slice };
}

describe("createBenchmarkController", () => {
  it("fires one run for a fresh tuple and transitions running -> done, caching the report", async () => {
    const store = makeStore();
    const report = fakeReport(1);
    const run = vi.fn().mockResolvedValue(report);
    const ctl = createBenchmarkController(store, { run });

    ctl.request(PARAMS_A);
    expect(run).toHaveBeenCalledTimes(1);
    expect(store.state.benchStatus).toBe("running"); // spinner is on synchronously

    await Promise.resolve(); // let the run promise settle
    expect(store.state.benchStatus).toBe("done");
    expect(store.state.benchReport).toBe(report);
    expect(Object.keys(store.state.benchCache)).toHaveLength(1);
  });

  it("hits the client cache on a repeated identical tuple (no duplicate run)", async () => {
    const store = makeStore();
    const run = vi.fn().mockResolvedValue(fakeReport(1));
    const ctl = createBenchmarkController(store, { run });

    ctl.request(PARAMS_A);
    await Promise.resolve();
    expect(run).toHaveBeenCalledTimes(1);

    // Move away, then come back to the identical tuple: served from cache, no new run.
    ctl.request(PARAMS_B);
    await Promise.resolve();
    expect(run).toHaveBeenCalledTimes(2);

    ctl.request(PARAMS_A);
    await Promise.resolve();
    expect(run).toHaveBeenCalledTimes(2); // still 2 — A came from cache
    expect(store.state.benchStatus).toBe("done");
    expect(store.state.benchReport?.seed).toBe(1);
  });

  it("ignores a back-to-back identical tuple while one is in flight", () => {
    const store = makeStore();
    const run = vi.fn().mockReturnValue(new Promise<BenchmarkReport>(() => {})); // never resolves
    const ctl = createBenchmarkController(store, { run });

    ctl.request(PARAMS_A);
    ctl.request(PARAMS_A); // same tuple, still running
    expect(run).toHaveBeenCalledTimes(1);
  });

  it("aborts the previous in-flight run when a new tuple arrives (latest wins)", () => {
    const store = makeStore();
    const signals: (AbortSignal | undefined)[] = [];
    const run = vi.fn().mockImplementation((_p, opts) => {
      signals.push(opts?.signal);
      return new Promise<BenchmarkReport>(() => {}); // never resolves
    });
    const ctl = createBenchmarkController(store, { run });

    ctl.request(PARAMS_A);
    ctl.request(PARAMS_B);

    expect(run).toHaveBeenCalledTimes(2);
    expect(signals[0]?.aborted).toBe(true); // first run was cancelled
    expect(signals[1]?.aborted).toBe(false); // second is live
  });

  it("surfaces a failed run's message rather than swallowing it", async () => {
    const store = makeStore();
    const run = vi.fn().mockRejectedValue(new BenchmarkError("job_failed", "benchmark failed"));
    const ctl = createBenchmarkController(store, { run });

    ctl.request(PARAMS_A);
    await Promise.resolve();
    await Promise.resolve();

    expect(store.state.benchStatus).toBe("failed");
    expect(store.state.benchError).toBe("benchmark failed");
  });

  it("caches a superseded run that resolves later, without clobbering the active view", async () => {
    const store = makeStore();
    const reportA = fakeReport(1);
    const reportB = fakeReport(2);
    let resolveA: (r: BenchmarkReport) => void = () => {};
    const run = vi.fn().mockImplementation((p: BenchmarkTuple) =>
      p.alpha === PARAMS_A.alpha
        ? new Promise<BenchmarkReport>((res) => {
            resolveA = res;
          })
        : Promise.resolve(reportB),
    );
    const ctl = createBenchmarkController(store, { run });

    ctl.request(PARAMS_A); // A in flight
    ctl.request(PARAMS_B); // supersedes A; B resolves immediately
    await Promise.resolve();
    expect(store.state.benchReport).toBe(reportB);
    expect(store.state.benchStatus).toBe("done");

    resolveA(reportA); // A resolves AFTER being superseded
    await Promise.resolve();

    // A's late resolve must cache its report but NOT flip the visible view back to A.
    expect(store.state.benchCache[benchmarkKey(PARAMS_A)]).toBe(reportA);
    expect(store.state.benchReport).toBe(reportB);
    expect(store.state.benchStatus).toBe("done");
  });

  it("re-fires a previously failed tuple when it is settled again (failure is retryable)", async () => {
    const store = makeStore();
    const report = fakeReport(1);
    const run = vi
      .fn()
      .mockRejectedValueOnce(new BenchmarkError("capacity", "at capacity"))
      .mockResolvedValueOnce(report);
    const ctl = createBenchmarkController(store, { run });

    ctl.request(PARAMS_A);
    await Promise.resolve();
    await Promise.resolve();
    expect(store.state.benchStatus).toBe("failed");

    // The SAME tuple, settled again after the failure: it must re-fire (not be deduped
    // against the failed key) so a transient failure is recoverable in place.
    ctl.request(PARAMS_A);
    expect(run).toHaveBeenCalledTimes(2);
    await Promise.resolve();
    expect(store.state.benchStatus).toBe("done");
    expect(store.state.benchReport).toBe(report);
  });

  it("does not surface an error for a run we deliberately aborted", async () => {
    const store = makeStore();
    let rejectFn: (e: unknown) => void = () => {};
    const run = vi.fn().mockImplementation((_p, opts) => {
      return new Promise<BenchmarkReport>((_resolve, reject) => {
        rejectFn = reject;
        opts?.signal?.addEventListener("abort", () => reject(new Error("Aborted")));
      });
    });
    const ctl = createBenchmarkController(store, { run });

    ctl.request(PARAMS_A);
    ctl.request(PARAMS_B); // aborts A
    rejectFn(new Error("Aborted")); // A rejects after abort
    await Promise.resolve();

    // B is still running; A's abort rejection must not have flipped the view to failed.
    expect(store.state.benchStatus).toBe("running");
    expect(store.state.benchError).toBeNull();
  });
});
