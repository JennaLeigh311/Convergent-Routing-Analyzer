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

  it("orders the full field of six finite values ascending (the steady-state board)", () => {
    // Mid/late run: every algorithm has a distinct finite metric. Give each a value
    // and assert the complete ascending permutation, not just the podium.
    const m = emptyMetrics();
    m.naive = metric({ route_median_ns: 600 });
    m.reactive = metric({ route_median_ns: 100 });
    m.incremental = metric({ route_median_ns: 500 });
    m.msa = metric({ route_median_ns: 300 });
    m.systemoptimal = metric({ route_median_ns: 200 });
    m.multipath = metric({ route_median_ns: 400 });

    const ranked = rankByMetric(m, "route_median_ns");
    expect(ranked.map((e) => e.algo)).toEqual([
      "reactive",
      "systemoptimal",
      "msa",
      "multipath",
      "incremental",
      "naive",
    ]);
    expect(ranked.map((e) => e.value)).toEqual([100, 200, 300, 400, 500, 600]);
  });

  it("breaks ties by ROUTER_ORDER so the board never jitters between equals", () => {
    const m = emptyMetrics();
    // incremental and reactive tie; the one earlier in ROUTER_ORDER wins.
    m.incremental = metric({ route_median_ns: 500 });
    m.reactive = metric({ route_median_ns: 500 });

    const [first, second] = ["reactive", "incremental"].sort(
      (a, b) => ROUTER_ORDER.indexOf(a as Algo) - ROUTER_ORDER.indexOf(b as Algo),
    );
    const ranked = rankByMetric(m, "route_median_ns");
    expect(ranked.slice(0, 2).map((e) => e.algo)).toEqual([first, second]);
  });

  it("sinks algorithms with no metrics (or non-finite values) to the bottom", () => {
    const m = emptyMetrics();
    m.naive = metric({ realized_total_s: 100 });
    m.reactive = metric({ realized_total_s: Number.NaN }); // non-finite → treated as null
    m.msa = metric({ realized_total_s: Number.POSITIVE_INFINITY }); // also non-finite
    m.multipath = metric({ realized_total_s: Number.NEGATIVE_INFINITY }); // also non-finite

    const ranked = rankByMetric(m, "realized_total_s");
    expect(ranked[0].algo).toBe("naive");
    expect(ranked[0].value).toBe(100);
    // Everyone else — including the ±Infinity/NaN rows — has value null and trails in
    // ROUTER_ORDER (non-finite never ranks ahead of a real value, and −Infinity is NOT
    // treated as the smallest).
    expect(ranked.slice(1).every((e) => e.value === null)).toBe(true);
    expect(ranked.slice(1).map((e) => e.algo)).toEqual(
      ROUTER_ORDER.filter((a) => a !== "naive"),
    );
  });

  it("always returns all six algorithms and does not mutate its input", () => {
    const m = emptyMetrics();
    m.naive = metric({ route_median_ns: 700 });
    m.msa = metric({ route_median_ns: 200 });
    const before = structuredClone(m);

    const ranked = rankByMetric(m, "route_median_ns");
    expect(ranked).toHaveLength(ROUTER_ORDER.length);
    // The input record is untouched (ranking builds and sorts a fresh array).
    expect(m).toEqual(before);
  });

  it("keeps the canonical order when no algorithm has a metric yet", () => {
    const ranked = rankByMetric(emptyMetrics(), "route_median_ns");
    expect(ranked.map((e) => e.algo)).toEqual([...ROUTER_ORDER]);
    expect(ranked.every((e) => e.value === null)).toBe(true);
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
    const winner = ["multipath", "reactive"].sort(
      (a, b) => ROUTER_ORDER.indexOf(a as Algo) - ROUTER_ORDER.indexOf(b as Algo),
    )[0];
    expect(leaderAlgo(m, "realized_total_s")).toBe(winner);
  });

  // The tile winner-ring (leaderAlgo) and the board crown (rankByMetric[0]) are two
  // independent implementations that must never disagree on screen. Lock their
  // equivalence — the leader is the top-ranked algo, or null when it has no value.
  it("always agrees with rankByMetric()[0] across empty, partial, tied, and full fields", () => {
    const cases: Record<Algo, AlgoMetrics | null>[] = [
      emptyMetrics(),
      (() => {
        const m = emptyMetrics();
        m.naive = metric({ route_median_ns: 800 });
        m.msa = metric({ route_median_ns: 200 });
        return m;
      })(),
      (() => {
        const m = emptyMetrics(); // tie for the lead
        m.multipath = metric({ route_median_ns: 400 });
        m.reactive = metric({ route_median_ns: 400 });
        return m;
      })(),
      (() => {
        const m = emptyMetrics(); // full field
        ROUTER_ORDER.forEach((a, i) => (m[a] = metric({ route_median_ns: (6 - i) * 100 })));
        return m;
      })(),
    ];
    for (const m of cases) {
      for (const key of ["route_median_ns", "realized_total_s"] as const) {
        const top = rankByMetric(m, key)[0];
        const expected = top.value != null ? top.algo : null;
        expect(leaderAlgo(m, key)).toBe(expected);
      }
    }
  });
});
