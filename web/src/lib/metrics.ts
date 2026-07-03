// Pure metric formatting shared by the live views (e.g. each mini-map's PoA label).
// This NEVER recomputes the engine's metrics — it only formats the values the store
// already holds.

/** Format a metric number to a fixed precision; em dash for absent/non-finite. */
export function fmtMetric(n: number | null | undefined, digits = 2): string {
  return n != null && Number.isFinite(n) ? n.toFixed(digits) : "—";
}
