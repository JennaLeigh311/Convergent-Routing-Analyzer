// MetricsPanel — a compact live summary of the selected algorithm: Price of Anarchy
// (how much worse than system-optimal the selfish routing is), the fair per-route
// compute cost, plus the in-flight and completed vehicle counts. Shown as a small card
// over the map so the headline numbers are visible without the dense per-router tables
// the old benchmark views carried.
//
// Since #112 `poa` is the CONSTANT static-equilibrium Price of Anarchy (it matches
// /benchmark and does not vary tick to tick), and `route_median_ns` is the fair
// per-route timer — shown here as microseconds — in place of the jittery cumulative
// `compute_ms`.

import { fmtMetric } from "../lib/metrics";
import type { AlgoMetrics } from "../lib/protocol";

interface Props {
  metrics: AlgoMetrics | null;
}

export function MetricsPanel({ metrics }: Props) {
  const routeMedianUs =
    metrics != null ? metrics.route_median_ns / 1000 : null;
  return (
    <div className="summary-card">
      <div className="summary-stat">
        <span className="summary-value">{fmtMetric(metrics?.poa, 3)}</span>
        <span className="summary-label">Price of Anarchy</span>
      </div>
      <div className="summary-stat">
        <span className="summary-value">{fmtMetric(routeMedianUs, 1)}</span>
        <span className="summary-label">median µs / route</span>
      </div>
      <div className="summary-stat">
        <span className="summary-value">{metrics ? metrics.in_flight.toLocaleString() : "—"}</span>
        <span className="summary-label">in flight</span>
      </div>
      <div className="summary-stat">
        <span className="summary-value">{metrics ? metrics.completed.toLocaleString() : "—"}</span>
        <span className="summary-label">completed</span>
      </div>
    </div>
  );
}
