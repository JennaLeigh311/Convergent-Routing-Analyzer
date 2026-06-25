// MetricsPanel — the selected algorithm's live per-tick metrics (the §R6 figures
// carried on every frame: compute time, realized total network time, Price of
// Anarchy, in-flight / completed vehicle counts) plus the current sim time.

import type { AlgoMetrics } from "../lib/protocol";

interface Props {
  tick: number;
  simTime: string;
  metrics: AlgoMetrics | null;
}

function fmt(n: number, digits = 2): string {
  return Number.isFinite(n) ? n.toFixed(digits) : "—";
}

/** Local wall-clock for an RFC3339 sim_time; em dash if absent or unparseable. */
function fmtSimTime(simTime: string): string {
  if (!simTime) return "—";
  const d = new Date(simTime);
  return Number.isNaN(d.getTime()) ? "—" : d.toLocaleTimeString();
}

export function MetricsPanel({ tick, simTime, metrics }: Props) {
  return (
    <div className="metrics-panel">
      <h2>Metrics</h2>
      <dl>
        <dt>tick</dt>
        <dd>{tick || "—"}</dd>
        <dt>sim time</dt>
        <dd>{fmtSimTime(simTime)}</dd>
        <dt>compute (ms)</dt>
        <dd>{metrics ? fmt(metrics.compute_ms) : "—"}</dd>
        <dt>realized total (s)</dt>
        <dd>{metrics ? fmt(metrics.realized_total_s, 1) : "—"}</dd>
        <dt>PoA</dt>
        <dd>{metrics ? fmt(metrics.poa, 3) : "—"}</dd>
        <dt>in flight</dt>
        <dd>{metrics ? metrics.in_flight : "—"}</dd>
        <dt>completed</dt>
        <dd>{metrics ? metrics.completed : "—"}</dd>
      </dl>
    </div>
  );
}
