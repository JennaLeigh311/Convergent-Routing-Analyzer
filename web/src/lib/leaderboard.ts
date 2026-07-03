// leaderboard.ts — the pure, side-effect-free ranking core behind the two live
// leaderboards (#114). It ranks the six RouterOrder algorithms by ONE of the two
// HONEST per-frame metrics from #112 and returns the ordered list plus the leader.
//
// The two ranking keys are deliberate, and both rank ASCENDING (smaller is better):
//   - route_median_ns  → "fastest to compute a route": the fair per-route latency.
//     NOT compute_ms (that field is cumulative/contended/jittery and deprecated).
//   - realized_total_s → "best at minimizing traffic": the realized travel time.
//     NOT poa (since #112 poa is a CONSTANT static-equilibrium reference — same every
//     frame — so it is useless as a live ranking key).
//
// Ranking is stable in ROUTER_ORDER: ties (and algorithms with no metric yet) fall
// back to the canonical order, so the board never jitters between equal contenders.
// Algorithms without usable metrics yet (null / non-finite) always sink to the bottom
// so an unstarted run shows placeholders rather than a bogus winner.

import type { Algo, AlgoMetrics } from "./protocol";
import { ROUTER_ORDER } from "./protocol";

/** The two honest, per-frame ranking keys (#112/#114); both rank ASCENDING. */
export type RankKey = "route_median_ns" | "realized_total_s";

/** One algorithm's place in a ranking: its value for the key, or null if not yet known. */
export interface RankEntry {
  algo: Algo;
  /** The ranked value (finite), or null when the algo has no usable metric yet. */
  value: number | null;
}

/** Canonical order index for the stable tie-break (earlier in ROUTER_ORDER wins). */
const ORDER_INDEX: Record<Algo, number> = Object.fromEntries(
  ROUTER_ORDER.map((algo, i) => [algo, i]),
) as Record<Algo, number>;

/** The key's value for an algo, or null when metrics are absent or non-finite. */
function metricValue(metrics: AlgoMetrics | null, key: RankKey): number | null {
  if (metrics == null) return null;
  const v = metrics[key];
  return Number.isFinite(v) ? v : null;
}

/**
 * Rank all six algorithms by `key` ascending. Algorithms with a usable value come
 * first (smallest = best), ties broken by ROUTER_ORDER; algorithms without a metric
 * yet trail, also in ROUTER_ORDER. Returns a fresh array (never mutates its input).
 */
export function rankByMetric(
  metrics: Record<Algo, AlgoMetrics | null>,
  key: RankKey,
): RankEntry[] {
  const entries: RankEntry[] = ROUTER_ORDER.map((algo) => ({
    algo,
    value: metricValue(metrics[algo], key),
  }));
  return entries.sort((a, b) => {
    if (a.value === null && b.value === null) return ORDER_INDEX[a.algo] - ORDER_INDEX[b.algo];
    if (a.value === null) return 1; // nulls sink to the bottom
    if (b.value === null) return -1;
    if (a.value !== b.value) return a.value - b.value; // ascending
    return ORDER_INDEX[a.algo] - ORDER_INDEX[b.algo]; // stable tie-break
  });
}

/**
 * The current leader for `key`: the algorithm with the smallest usable value, ties
 * resolved by ROUTER_ORDER. Returns null when NO algorithm has a metric yet (so the
 * UI shows a "waiting" placeholder rather than a fake winner). This is a direct scan
 * — cheap enough to call from a per-tile store selector on every frame.
 */
export function leaderAlgo(
  metrics: Record<Algo, AlgoMetrics | null>,
  key: RankKey,
): Algo | null {
  let best: Algo | null = null;
  let bestValue = Infinity;
  for (const algo of ROUTER_ORDER) {
    const v = metricValue(metrics[algo], key);
    if (v === null) continue;
    if (v < bestValue) {
      bestValue = v;
      best = algo;
    }
  }
  return best;
}
