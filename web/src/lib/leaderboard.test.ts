// Tests for the pure leaderboard ranking core (#114). The load-bearing behaviours
// are: ASCENDING order on the honest key, the ROUTER_ORDER stable tie-break, and
// nulls (algorithms with no metrics yet, or non-finite values) sinking to the bottom
// without ever being crowned the leader.

import { describe, expect, it } from "vitest";

import { leaderAlgo, rankByMetric } from "./leaderboard";
import type { Algo, AlgoMetrics } from "./protocol";
import { ROUTER_ORDER } from "./protocol";

/** Build a full metrics record, overriding just the fields a case cares about. */
function metric(over: Partial<AlgoMetrics>): AlgoMetrics {
  return {
    compute_ms: 0,
    route_median_ns: 0,
    realized_total_s: 0,
    poa: 1,
    in_flight: 0,
    completed: 0,
    ...over,
  };
}

/** Start from "no algorithm has metrics yet", then fill the ones a case sets. */
function emptyMetrics(): Record<Algo, AlgoMetrics | null> {
  const m = {} as Record<Algo, AlgoMetrics | null>;
  for (const algo of ROUTER_ORDER) m[algo] = null;
  return m;
}

describe("rankByMetric", () => {
  it("ranks by route_median_ns ascending (smallest = fastest = first)", () => {
    const m = emptyMetrics();
    m.naive = metric({ route_median_ns: 900 });
    m.reactive = metric({ route_median_ns: 300 });
    m.msa = metric({ route_median_ns: 600 });

    const ranked = rankByMetric(m, "route_median_ns");
    // Only the three with metrics rank; the fastest leads.
    expect(ranked.slice(0, 3).map((e) => e.algo)).toEqual(["reactive", "msa", "naive"]);
  });

  it("ranks by realized_total_s ascending (least realized traffic = first)", () => {
    const m = emptyMetrics();
    m.naive = metric({ realized_total_s: 5000 });
    m.systemoptimal = metric({ realized_total_s: 3200 });
    m.multipath = metric({ realized_total_s: 4100 });

    const ranked = rankByMetric(m, "realized_total_s");
    expect(ranked.slice(0, 3).map((e) => e.algo)).toEqual([
      "systemoptimal",
      "multipath",
      "naive",
    ]);
  });

  it("breaks ties by ROUTER_ORDER so the board never jitters between equals", () => {
    const m = emptyMetrics();
    // incremental and reactive tie; reactive is earlier in ROUTER_ORDER so it wins.
    m.incremental = metric({ route_median_ns: 500 });
    m.reactive = metric({ route_median_ns: 500 });

    const ranked = rankByMetric(m, "route_median_ns");
    expect(ranked.slice(0, 2).map((e) => e.algo)).toEqual(["reactive", "incremental"]);
  });

  it("sinks algorithms with no metrics (or non-finite values) to the bottom", () => {
    const m = emptyMetrics();
    m.naive = metric({ realized_total_s: 100 });
    m.reactive = metric({ realized_total_s: Number.NaN }); // non-finite → treated as null

    const ranked = rankByMetric(m, "realized_total_s");
    expect(ranked[0].algo).toBe("naive");
    expect(ranked[0].value).toBe(100);
    // Everyone else has value null and trails in ROUTER_ORDER.
    expect(ranked.slice(1).every((e) => e.value === null)).toBe(true);
    expect(ranked.slice(1).map((e) => e.algo)).toEqual(
      ROUTER_ORDER.filter((a) => a !== "naive"),
    );
  });

  it("always returns all six algorithms and does not mutate its input", () => {
    const m = emptyMetrics();
    const ranked = rankByMetric(m, "route_median_ns");
    expect(ranked).toHaveLength(ROUTER_ORDER.length);
    // All-null input keeps the canonical order.
    expect(ranked.map((e) => e.algo)).toEqual([...ROUTER_ORDER]);
  });
});

describe("leaderAlgo", () => {
  it("returns null when no algorithm has metrics yet (no fake winner)", () => {
    expect(leaderAlgo(emptyMetrics(), "route_median_ns")).toBeNull();
    expect(leaderAlgo(emptyMetrics(), "realized_total_s")).toBeNull();
  });

  it("names the smallest-value algorithm as leader", () => {
    const m = emptyMetrics();
    m.naive = metric({ route_median_ns: 800 });
    m.msa = metric({ route_median_ns: 200 });
    expect(leaderAlgo(m, "route_median_ns")).toBe("msa");
  });

  it("resolves a tie for the lead by ROUTER_ORDER", () => {
    const m = emptyMetrics();
    m.multipath = metric({ realized_total_s: 400 });
    m.reactive = metric({ realized_total_s: 400 });
    expect(leaderAlgo(m, "realized_total_s")).toBe("reactive");
  });
});
