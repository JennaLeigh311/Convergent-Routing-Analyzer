// Tests for the /benchmark client (#102): the start-then-poll loop, the failure
// path, and the pure report selectors (peak per-level PoA, per-level cell lookup).
// fetch is stubbed; restUrl reads window.location.origin so we stub the same-origin
// proxy default. runBenchmark polls at a fast interval here so the loop test is quick.

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import {
  peakPoaLevel,
  runBenchmark,
  selectLevelCells,
  type BenchmarkReport,
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
