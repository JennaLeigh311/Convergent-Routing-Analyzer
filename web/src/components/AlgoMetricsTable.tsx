// AlgoMetricsTable — the per-algorithm metrics table for the comparison view (#101):
// one row per RouterOrder algorithm showing the §R6 figures carried on every frame
// (compute_ms, realized_total_s, poa, in_flight, completed), updating live.
//
// The raw metric cells display EXACTLY what the store holds for that algorithm —
// no recomputation, no derivation (the reducer already guarantees delta-correctness).
// The one derived column is "PoA / SO": each algorithm's Price of Anarchy expressed
// relative to the system-optimal reference (whose PoA ≈ 1), which is the #101
// "highlight live PoA per algorithm against the reference" requirement.
//
// Each row subscribes to just its own algorithm's metrics plus the reference's PoA,
// so a delta for one algorithm re-renders only that row (and, when the reference
// itself updates, every row's relative column — which is correct).

import { ALGO_LABELS } from "../lib/algoLabels";
import { REFERENCE_ALGO, fmtMetric, fmtRelativePoa, relativePoa } from "../lib/metrics";
import { ROUTER_ORDER, type Algo } from "../lib/protocol";
import { useAppStore } from "../store";

function AlgoMetricsRow({ algo }: { algo: Algo }) {
  const metrics = useAppStore((s) => s.congestion.metrics[algo]);
  const refPoa = useAppStore((s) => s.congestion.metrics[REFERENCE_ALGO]?.poa ?? null);
  const isReference = algo === REFERENCE_ALGO;
  // The reference row computes against itself (→ 1.00× once it has data, — before),
  // so it flows through the same formatter as every other row — no hardcoded literal.
  const rel = relativePoa(metrics?.poa ?? null, refPoa);

  return (
    <tr className={isReference ? "metrics-row reference" : "metrics-row"}>
      <th scope="row">
        {ALGO_LABELS[algo]}
        {isReference && <span className="ref-badge">ref</span>}
      </th>
      <td>{fmtMetric(metrics?.compute_ms)}</td>
      <td>{fmtMetric(metrics?.realized_total_s, 1)}</td>
      <td>{fmtMetric(metrics?.poa, 3)}</td>
      <td className="rel-poa">{fmtRelativePoa(rel)}</td>
      <td>{metrics ? metrics.in_flight : "—"}</td>
      <td>{metrics ? metrics.completed : "—"}</td>
    </tr>
  );
}

export function AlgoMetricsTable() {
  return (
    <table className="metrics-table">
      <caption>
        Per-algorithm metrics (live) — PoA / SO is relative to the system-optimal
        reference
      </caption>
      <thead>
        <tr>
          <th scope="col">algorithm</th>
          <th scope="col">compute (ms)</th>
          <th scope="col">realized total (s)</th>
          <th scope="col">PoA</th>
          <th scope="col">PoA / SO</th>
          <th scope="col">in flight</th>
          <th scope="col">completed</th>
        </tr>
      </thead>
      <tbody>
        {ROUTER_ORDER.map((algo) => (
          <AlgoMetricsRow key={algo} algo={algo} />
        ))}
      </tbody>
    </table>
  );
}
