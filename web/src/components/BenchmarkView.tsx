// BenchmarkView — the main-area render target for the #104 async parameter panel. It
// is READ-ONLY over the store: the controls (sidebar) fire jobs through the controller,
// and this paints whatever the benchmark slice currently holds — a spinner while a job
// is pending, a graceful error banner when one fails, and the report table when done.
//
// It NEVER recomputes: every figure comes straight from the engine's report (the same
// realized-time aggregates and per-level PoA the CLI emits), formatted via the shared
// metrics helpers. It subscribes to its own slices only, so it is fully decoupled from
// the live-stream map (which keeps rendering independently).

import { fmtMetric } from "../lib/metrics";
import type { BenchmarkReport, BenchmarkTuple } from "../lib/benchmark";
import { useAppStore } from "../store";

function Spinner({ label }: { label: string }) {
  return (
    <div className="bench-spinner" role="status" aria-live="polite">
      <span className="bench-spinner-ring" aria-hidden="true" />
      <span>{label}</span>
    </div>
  );
}

function Headline({ report }: { report: BenchmarkReport }) {
  const h = report.headline_improvement;
  return (
    <div className="bench-headline">
      <span className="bench-headline-label">headline improvement</span>
      <span className="bench-headline-value">{fmtMetric(h.percent_reduction, 1)}%</span>
      <span className="bench-headline-sub">
        {h.best_router || "—"} vs naive at {h.demand_level || "—"} (target v/c {fmtMetric(h.target_vc, 1)})
      </span>
    </div>
  );
}

function PoaByLevel({ report }: { report: BenchmarkReport }) {
  if (report.poa_by_level.length === 0) return null;
  return (
    <div className="bench-poa-levels">
      <div className="bench-section-title">Price of Anarchy by demand level</div>
      <dl>
        {report.poa_by_level.map((lvl) => (
          <div key={lvl.demand_level} className="bench-poa-row">
            <dt>
              {lvl.demand_level} <span className="bench-muted">v/c {fmtMetric(lvl.target_vc, 1)}</span>
            </dt>
            <dd>{fmtMetric(lvl.poa, 3)}×</dd>
          </div>
        ))}
      </dl>
    </div>
  );
}

function CellsTable({ report }: { report: BenchmarkReport }) {
  return (
    <table className="metrics-table bench-cells-table">
      <caption>
        Per-(router, level) realized-time metrics — straight from the benchmark report
        (seed {report.seed}, {report.od_count} requests/level)
      </caption>
      <thead>
        <tr>
          <th scope="col">router</th>
          <th scope="col">demand</th>
          <th scope="col">mean (s)</th>
          <th scope="col">p95 (s)</th>
          <th scope="col">total (s)</th>
          <th scope="col">max v/c</th>
          <th scope="col">gini</th>
          <th scope="col">PoA</th>
        </tr>
      </thead>
      <tbody>
        {report.cells.map((cell) => (
          <tr key={`${cell.result.demand_level}/${cell.result.router}`} className="metrics-row">
            <th scope="row">{cell.result.router}</th>
            <td>{cell.result.demand_level}</td>
            <td>{fmtMetric(cell.result.mean_realized_s, 1)}</td>
            <td>{fmtMetric(cell.result.p95_realized_s, 1)}</td>
            <td>{fmtMetric(cell.result.total_network_time_s, 1)}</td>
            <td>{fmtMetric(cell.result.max_vc, 2)}</td>
            <td>{fmtMetric(cell.result.gini_vc, 3)}</td>
            <td className="rel-poa">{fmtMetric(cell.poa, 3)}×</td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}

function ParamsSummary({ params }: { params: BenchmarkTuple }) {
  return (
    <div className="bench-params-summary">
      algorithm <b>{params.algorithm}</b> · α {fmtMetric(params.alpha, 2)} · β {fmtMetric(params.beta, 1)} ·
      capacity {fmtMetric(params.capacity_scale, 1)}× · {params.request_count || "default"} requests · seed{" "}
      {params.seed}
    </div>
  );
}

export function BenchmarkView() {
  const status = useAppStore((s) => s.benchStatus);
  const report = useAppStore((s) => s.benchReport);
  const error = useAppStore((s) => s.benchError);
  const params = useAppStore((s) => s.benchParams);

  return (
    <div className="benchmark-view">
      <ParamsSummary params={params} />

      {status === "running" && <Spinner label="Running benchmark…" />}

      {status === "failed" && (
        <p className="error bench-error" role="alert">
          Benchmark failed: {error ?? "unknown error"}
        </p>
      )}

      {report && status !== "running" && (
        <div className="bench-report">
          <Headline report={report} />
          <PoaByLevel report={report} />
          <CellsTable report={report} />
        </div>
      )}

      {status === "idle" && !report && (
        <p className="bench-empty">Adjust a parameter to run a benchmark.</p>
      )}
    </div>
  );
}
