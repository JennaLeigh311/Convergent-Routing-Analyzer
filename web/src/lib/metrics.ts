// Pure formatting + PoA-reference helpers shared by the single-algo metrics panel
// (#100) and the per-algorithm comparison table (#101). These NEVER recompute the
// engine's metrics — they only format the values the store already holds and express
// each algorithm's Price of Anarchy relative to the system-optimal reference.
//
// `systemoptimal` is the reference router: it routes to the social optimum, so its
// PoA ≈ 1. Every other algorithm's PoA is reported as a ratio against it, making
// "how much worse than optimal is this algorithm right now" legible at a glance.

import type { Algo } from "./protocol";

/** The reference router whose PoA ≈ 1 anchors the relative-PoA column. */
export const REFERENCE_ALGO: Algo = "systemoptimal";

/** Format a metric number to a fixed precision; em dash for absent/non-finite. */
export function fmtMetric(n: number | null | undefined, digits = 2): string {
  return n != null && Number.isFinite(n) ? n.toFixed(digits) : "—";
}

/**
 * An algorithm's PoA relative to the system-optimal reference: poa / refPoa. Returns
 * null when either side is missing/non-finite or the reference is zero (not yet
 * computable), so callers render an em dash rather than NaN/∞.
 */
export function relativePoa(
  poa: number | null | undefined,
  refPoa: number | null | undefined,
): number | null {
  if (poa == null || refPoa == null) return null;
  if (!Number.isFinite(poa) || !Number.isFinite(refPoa) || refPoa === 0) return null;
  return poa / refPoa;
}

/** Render a relative-PoA ratio as e.g. "1.27×"; em dash when not computable. */
export function fmtRelativePoa(rel: number | null): string {
  return rel == null ? "—" : `${rel.toFixed(2)}×`;
}
