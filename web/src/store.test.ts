// Tests for the real store's benchmark slice (#104). The controller suite exercises a
// hand-rolled mock of these actions; this pins the ACTUAL store.ts implementation so the
// active-key-gated no-clobber + always-cache-on-complete logic can't silently drift from
// the mock (a change here that the mock doesn't mirror is exactly the regression risk).

import { beforeEach, describe, expect, it } from "vitest";

import { DEFAULT_BENCH_PARAMS, type BenchmarkReport, type BenchmarkTuple } from "./lib/benchmark";
import { useAppStore } from "./store";

const PARAMS: BenchmarkTuple = {
  algorithm: "reactive",
  alpha: 0.2,
  beta: 4,
  capacity_scale: 1,
  request_count: 500,
  seed: 0,
};

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

beforeEach(() => {
  useAppStore.setState({
    benchParams: DEFAULT_BENCH_PARAMS,
    benchActiveKey: null,
    benchStatus: "idle",
    benchReport: null,
    benchError: null,
    benchCache: {},
  });
});

describe("store benchmark slice", () => {
  it("benchRequestStart turns on the spinner and clears the previous result/error", () => {
    const s = useAppStore.getState();
    useAppStore.setState({ benchReport: fakeReport(9), benchError: "old", benchStatus: "failed" });
    s.benchRequestStart("keyA", PARAMS);

    const st = useAppStore.getState();
    expect(st.benchActiveKey).toBe("keyA");
    expect(st.benchParams).toBe(PARAMS);
    expect(st.benchStatus).toBe("running");
    expect(st.benchReport).toBeNull();
    expect(st.benchError).toBeNull();
  });

  it("benchResolve for the active key flips to done and caches the report", () => {
    const s = useAppStore.getState();
    s.benchRequestStart("keyA", PARAMS);
    const report = fakeReport(1);
    s.benchResolve("keyA", report);

    const st = useAppStore.getState();
    expect(st.benchStatus).toBe("done");
    expect(st.benchReport).toBe(report);
    expect(st.benchCache["keyA"]).toBe(report);
  });

  it("benchResolve for a SUPERSEDED key caches but does not clobber the visible view", () => {
    const s = useAppStore.getState();
    s.benchRequestStart("keyB", PARAMS); // keyB is now active + running
    const staleReport = fakeReport(7);
    s.benchResolve("keyA", staleReport); // a late resolve from a superseded run

    const st = useAppStore.getState();
    expect(st.benchCache["keyA"]).toBe(staleReport); // still cached
    expect(st.benchStatus).toBe("running"); // view untouched
    expect(st.benchReport).toBeNull();
  });

  it("benchReject for the active key surfaces the failure; for a superseded key it is a no-op", () => {
    const s = useAppStore.getState();
    s.benchRequestStart("keyB", PARAMS);

    s.benchReject("keyA", "stale failure"); // superseded — ignored
    expect(useAppStore.getState().benchStatus).toBe("running");
    expect(useAppStore.getState().benchError).toBeNull();

    s.benchReject("keyB", "real failure"); // active — surfaced
    expect(useAppStore.getState().benchStatus).toBe("failed");
    expect(useAppStore.getState().benchError).toBe("real failure");
  });

  it("benchUseCached renders a cached report as done without touching the cache", () => {
    const s = useAppStore.getState();
    const report = fakeReport(3);
    s.benchUseCached("keyA", report, PARAMS);

    const st = useAppStore.getState();
    expect(st.benchActiveKey).toBe("keyA");
    expect(st.benchParams).toBe(PARAMS);
    expect(st.benchStatus).toBe("done");
    expect(st.benchReport).toBe(report);
    expect(st.benchError).toBeNull();
  });
});
