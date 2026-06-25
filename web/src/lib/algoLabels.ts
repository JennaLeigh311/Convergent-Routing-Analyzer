// Human-readable names for the six canonical RouterOrder algorithms. Kept in one
// place so the selector (#100), the comparison small multiples and the per-algo
// metrics table (#101) all label algorithms identically instead of each component
// re-typing the strings. The keys are the wire ids; the values are display-only.

import type { Algo } from "./protocol";

/** Human labels for the canonical router ids. */
export const ALGO_LABELS: Record<Algo, string> = {
  naive: "Naive (free-flow)",
  reactive: "Reactive (BPR)",
  incremental: "Incremental",
  msa: "MSA",
  systemoptimal: "System-optimal",
  multipath: "Multipath",
};
