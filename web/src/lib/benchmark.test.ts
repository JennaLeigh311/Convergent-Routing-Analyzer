// Tests for the /benchmark client (#102): the start-then-poll loop, the failure
// path, and the pure report selectors (peak per-level PoA, per-level cell lookup).
// fetch is stubbed; restUrl reads window.location.origin so we stub the same-origin
// proxy default. runBenchmark polls at a fast interval here so the loop test is quick.

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import {
  BENCH_ALGO_OPTIONS,
  BenchmarkError,
  DEFAULT_BENCH_PARAMS,
  benchmarkKey,
  peakPoaLevel,
  runBenchmark,
  selectLevelCells,
  withBenchDefaults,
  type BenchmarkReport,
  type BenchmarkTuple,
  type StartResponse,
  type StatusResponse,
  type SweepCell,
} from "./benchmark";

function cell(router: string, level: string, mean: number, p95: number, total: number, poa: number): SweepCell {
  return {
    result: {
      router,
      demand_level: level,
      mean_realized_s: mean,
      p95_realized_s: p95,
      total_network_time_s: total,
      gini_vc: 0,
      max_vc: 0,
      iters: 1,
      converged: true,
      gap: 0,
    },
    capacity_scale: 1,
    target_vc: 1,
    poa,
    sim_mean_realized_s: mean,
    sim_p95_realized_s: p95,
  };
}

const report: BenchmarkReport = {
  seed: 0,
  od_count: 200,
  total_demand_vph: 5000,
  cells: [
    cell("naive", "vc0.8", 100, 200, 1000, 1.4),
    cell("reactive", "vc0.8", 80, 150, 820, 1.15),
    cell("systemoptimal", "vc0.8", 75, 140, 800, 1.0),
    cell("naive", "vc1.2", 130, 260, 1300, 1.1),
    cell("systemoptimal", "vc1.2", 120, 240, 1200, 1.0),
  ],
  poa_by_level: [
    { demand_level: "vc0.8", target_vc: 0.8, poa: 1.4 },
    { demand_level: "vc1.2", target_vc: 1.2, poa: 1.1 },
  ],
  headline_improvement: {
    demand_level: "vc0.8",
    target_vc: 0.8,
    best_router: "systemoptimal",
    percent_reduction: 20,
    naive_total_s: 1000,
    best_total_s: 800,
  },
};

beforeEach(() => {
  vi.stubEnv("VITE_ENGINE_BASE", "");
  vi.stubGlobal("window", { location: { origin: "https://app.test" } });
});

afterEach(() => {
  vi.unstubAllGlobals();
  vi.unstubAllEnvs();
  vi.restoreAllMocks();
});

function jsonResponse(body: unknown): Response {
  return new Response(JSON.stringify(body), { status: 200 });
}

describe("runBenchmark", () => {
  it("starts a job then polls GET until done and returns the report", async () => {
    const start: StartResponse = {
      job_id: "abc123",
      status: "running",
      params: { algorithm: "all", alpha: 0.15, beta: 4, capacity_scale: 1, request_count: 200, seed: 0 },
    };
    const running: StatusResponse = { job_id: "abc123", status: "running", params: start.params };
    const done: StatusResponse = { job_id: "abc123", status: "done", params: start.params, report };

    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(jsonResponse(start)) // POST /benchmark
      .mockResolvedValueOnce(jsonResponse(running)) // first poll: still running
      .mockResolvedValueOnce(jsonResponse(done)); // second poll: done
    vi.stubGlobal("fetch", fetchMock);

    const got = await runBenchmark({}, { pollIntervalMs: 1 });
    expect(got.poa_by_level).toHaveLength(2);
    expect(fetchMock).toHaveBeenCalledTimes(3);
    // First call is the POST.
    expect(fetchMock.mock.calls[0][1]?.method).toBe("POST");
  });

  it("throws the engine's error message when a job fails", async () => {
    const start: StartResponse = {
      job_id: "x",
      status: "running",
      params: { algorithm: "all", alpha: 0.15, beta: 4, capacity_scale: 1, request_count: 200, seed: 0 },
    };
    const failed: StatusResponse = {
      job_id: "x",
      status: "failed",
      params: start.params,
      error: "benchmark failed",
    };
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValueOnce(jsonResponse(start)).mockResolvedValueOnce(jsonResponse(failed)),
    );
    await expect(runBenchmark({}, { pollIntervalMs: 1 })).rejects.toThrow(/benchmark failed/);
  });

  it("rejects after maxPolls without finishing, and polls the job id", async () => {
    const start: StartResponse = {
      job_id: "stuck9",
      status: "running",
      params: { algorithm: "all", alpha: 0.15, beta: 4, capacity_scale: 1, request_count: 200, seed: 0 },
    };
    const running: StatusResponse = { job_id: "stuck9", status: "running", params: start.params };
    const fetchMock = vi.fn().mockResolvedValueOnce(jsonResponse(start)); // POST /benchmark
    fetchMock.mockImplementation(async () => jsonResponse(running)); // a fresh response each poll
    vi.stubGlobal("fetch", fetchMock);

    await expect(runBenchmark({}, { pollIntervalMs: 1, maxPolls: 2 })).rejects.toThrow(/within 2 polls/);
    // The poll GET must carry the started job's id in the path (calls[1] is the first poll).
    expect(String(fetchMock.mock.calls[1][0])).toContain("stuck9");
  });

  it("rejects when a job reports done but carries no report", async () => {
    const start: StartResponse = {
      job_id: "d",
      status: "running",
      params: { algorithm: "all", alpha: 0.15, beta: 4, capacity_scale: 1, request_count: 200, seed: 0 },
    };
    const doneNoReport: StatusResponse = { job_id: "d", status: "done", params: start.params }; // report omitted
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValueOnce(jsonResponse(start)).mockResolvedValueOnce(jsonResponse(doneNoReport)),
    );
    await expect(runBenchmark({}, { pollIntervalMs: 1 })).rejects.toThrow(/no report/);
  });

  it("rejects with AbortError when the signal aborts between polls", async () => {
    const ctrl = new AbortController();
    const start: StartResponse = {
      job_id: "abort1",
      status: "running",
      params: { algorithm: "all", alpha: 0.15, beta: 4, capacity_scale: 1, request_count: 200, seed: 0 },
    };
    const running: StatusResponse = { job_id: "abort1", status: "running", params: start.params };
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(jsonResponse(start)) // POST /benchmark
      .mockImplementationOnce(async () => {
        ctrl.abort(); // abort while the loop is about to sleep before the next poll
        return jsonResponse(running);
      });
    vi.stubGlobal("fetch", fetchMock);

    // sleep() sees the already-aborted signal and rejects, surfacing the unmount-cancel path.
    await expect(runBenchmark({}, { signal: ctrl.signal, pollIntervalMs: 50 })).rejects.toThrow(/abort/i);
  });
});

describe("peakPoaLevel", () => {
  it("returns the level with the highest per-level PoA", () => {
    expect(peakPoaLevel(report)).toEqual({ demand_level: "vc0.8", target_vc: 0.8, poa: 1.4 });
  });

  it("returns null when there are no levels", () => {
    expect(peakPoaLevel({ ...report, poa_by_level: [] })).toBeNull();
  });
});

describe("selectLevelCells", () => {
  it("indexes one level's cells by router name", () => {
    const cells = selectLevelCells(report, "vc0.8");
    expect(Object.keys(cells).sort()).toEqual(["naive", "reactive", "systemoptimal"]);
    expect(cells.reactive.result.mean_realized_s).toBe(80);
    expect(cells.naive.result.total_network_time_s).toBe(1000);
  });

  it("returns an empty map for an unknown level", () => {
    expect(selectLevelCells(report, "nope")).toEqual({});
  });
});

// ---- §R6 tuple key + defaults (issue #104) ----------------------------------

describe("benchmarkKey / withBenchDefaults", () => {
  it("offers 'all' plus the six routers as options", () => {
    expect(BENCH_ALGO_OPTIONS).toEqual([
      "all",
      "naive",
      "reactive",
      "incremental",
      "msa",
      "systemoptimal",
      "multipath",
    ]);
  });

  it("collapses zero fields to the harness defaults so omitted == explicit-default", () => {
    const zeros: BenchmarkTuple = {
      algorithm: "all",
      alpha: 0,
      beta: 0,
      capacity_scale: 0,
      request_count: 0,
      seed: 0,
    };
    expect(withBenchDefaults(zeros)).toEqual(DEFAULT_BENCH_PARAMS);
    // ...and therefore keys identically to the explicit-default tuple.
    expect(benchmarkKey(zeros)).toBe(benchmarkKey(DEFAULT_BENCH_PARAMS));
  });

  it("keys differing tuples differently and identical tuples identically", () => {
    const a: BenchmarkTuple = { ...DEFAULT_BENCH_PARAMS, algorithm: "reactive", alpha: 0.2 };
    const b: BenchmarkTuple = { ...DEFAULT_BENCH_PARAMS, algorithm: "reactive", alpha: 0.2 };
    const c: BenchmarkTuple = { ...DEFAULT_BENCH_PARAMS, algorithm: "reactive", alpha: 0.3 };
    expect(benchmarkKey(a)).toBe(benchmarkKey(b));
    expect(benchmarkKey(a)).not.toBe(benchmarkKey(c));
  });
});

// ---- error classification + capacity backoff (issue #104) -------------------

function errorResponse(status: number, message: string): Response {
  return new Response(JSON.stringify({ error: message }), { status });
}

describe("runBenchmark error handling", () => {
  it("surfaces a 400 as a classified bad_params BenchmarkError", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValueOnce(errorResponse(400, "alpha must be >= 0")));
    try {
      await runBenchmark({ alpha: -1 }, { pollIntervalMs: 1 });
      expect.unreachable("should have thrown");
    } catch (err) {
      expect(err).toBeInstanceOf(BenchmarkError);
      expect((err as BenchmarkError).kind).toBe("bad_params");
      expect((err as BenchmarkError).status).toBe(400);
      expect((err as BenchmarkError).message).toMatch(/alpha/);
    }
  });

  it("retries a 503 capacity rejection with backoff, then completes", async () => {
    const start: StartResponse = {
      job_id: "cap1",
      status: "running",
      params: { algorithm: "all", alpha: 0.15, beta: 4, capacity_scale: 1, request_count: 200, seed: 0 },
    };
    const done: StatusResponse = { job_id: "cap1", status: "done", params: start.params, report };
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(errorResponse(503, "benchmark capacity reached, please retry shortly")) // POST #1
      .mockResolvedValueOnce(jsonResponse(start)) // POST #2 (after backoff)
      .mockResolvedValueOnce(jsonResponse(done)); // poll: done
    vi.stubGlobal("fetch", fetchMock);

    const got = await runBenchmark({}, { pollIntervalMs: 1, capacityBackoffMs: 1 });
    expect(got.poa_by_level).toHaveLength(2);
    expect(fetchMock.mock.calls[0][1]?.method).toBe("POST");
    expect(fetchMock.mock.calls[1][1]?.method).toBe("POST"); // the retry
  });

  it("gives up after exhausting capacity retries", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(errorResponse(503, "benchmark capacity reached, please retry shortly")),
    );
    await expect(
      runBenchmark({}, { pollIntervalMs: 1, capacityBackoffMs: 1, capacityRetries: 2 }),
    ).rejects.toMatchObject({ kind: "capacity" });
  });

  it("backs off linearly between capacity retries (delay grows per attempt)", async () => {
    // Capture the requested sleep durations while collapsing the real wait to 0ms, so the
    // linear growth (capacityBackoffMs * (attempt + 1)) is asserted deterministically and
    // a regression to a constant backoff would fail.
    const delays: number[] = [];
    const realSetTimeout = globalThis.setTimeout;
    const setTimeoutSpy = vi
      .spyOn(globalThis, "setTimeout")
      .mockImplementation(((fn: () => void, ms?: number) => {
        delays.push(ms ?? 0);
        return realSetTimeout(fn, 0);
      }) as typeof globalThis.setTimeout);

    const start: StartResponse = {
      job_id: "cap2",
      status: "running",
      params: { algorithm: "all", alpha: 0.15, beta: 4, capacity_scale: 1, request_count: 200, seed: 0 },
    };
    const done: StatusResponse = { job_id: "cap2", status: "done", params: start.params, report };
    vi.stubGlobal(
      "fetch",
      vi
        .fn()
        .mockResolvedValueOnce(errorResponse(503, "capacity")) // POST #1: 503 -> backoff 100
        .mockResolvedValueOnce(errorResponse(503, "capacity")) // POST #2: 503 -> backoff 200
        .mockResolvedValueOnce(jsonResponse(start)) // POST #3: accepted
        .mockResolvedValueOnce(jsonResponse(done)), // first poll: done (no poll sleep)
    );

    await runBenchmark({}, { capacityBackoffMs: 100, capacityRetries: 3 });
    expect(delays).toEqual([100, 200]); // two backoffs, growing linearly, before success
    setTimeoutSpy.mockRestore();
  });
});
