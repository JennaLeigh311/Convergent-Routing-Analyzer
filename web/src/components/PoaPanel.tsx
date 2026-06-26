// PoaPanel — the before/after metrics side panel (#102): the one large Price-of-
// Anarchy "money shot" number, the per-level PoA breakdown, and mean/p95/total bar
// charts comparing naive (before) vs reactive (after) vs systemoptimal (reference).
//
// Every figure comes STRAIGHT from the benchmark report (the engine's realized-time
// PoA and per-(router, level) aggregates) via the pure selectors in lib/benchmark —
// nothing is recomputed here, so "displayed metrics match the API response" holds by
// construction. The headline number is the PEAK per-level PoA (PoA peaks at moderate
// load and → 1 at the extremes), shown with the demand level it occurs at so it is
// never a cherry-picked figure. The bars are lightweight CSS (no charting dep),
// matching the repo's existing table/SVG-free approach.

import { fmtMetric } from "../lib/metrics";
import { peakPoaLevel, selectLevelCells, type BenchmarkReport, type SweepCell } from "../lib/benchmark";
import { ROUTE_CSS } from "../lib/routeStyle";

interface Props {
  report: BenchmarkReport | null;
  loading: boolean;
  error: string | null;
}

// The three routers the before/after panel contrasts, in before -> after -> reference
// order. A router absent from the report (single-algorithm mode) is simply skipped.
const PANEL_ROUTERS = ["naive", "reactive", "systemoptimal"] as const;
const ROUTER_LABEL: Record<string, string> = {
  naive: "naive",
  reactive: "reactive",
  systemoptimal: "system-opt",
};

// The realized-time aggregates charted as grouped bars, with display precision.
const METRICS: { key: keyof SweepCell["result"]; label: string; digits: number }[] = [
  { key: "mean_realized_s", label: "mean (s)", digits: 1 },
  { key: "p95_realized_s", label: "p95 (s)", digits: 1 },
  { key: "total_network_time_s", label: "total (s)", digits: 0 },
];

/** One metric's grouped bars: one bar per present router, scaled to the group max. */
function BarGroup({
  label,
  digits,
  cells,
}: {
  label: string;
  digits: number;
  cells: { router: string; value: number }[];
}) {
  const max = cells.reduce((m, c) => Math.max(m, c.value), 0);
  return (
    <div className="poa-bargroup">
      <div className="poa-bargroup-label">{label}</div>
      {cells.map(({ router, value }) => (
        <div key={router} className="poa-bar-row">
          <span className="poa-bar-name">{ROUTER_LABEL[router] ?? router}</span>
          <div className="poa-bar-track">
            <div
              className="poa-bar-fill"
              style={{
                width: max > 0 ? `${(value / max) * 100}%` : "0%",
                background: ROUTE_CSS[router] ?? "var(--muted)",
              }}
            />
          </div>
          <span className="poa-bar-value">{value.toFixed(digits)}</span>
        </div>
      ))}
    </div>
  );
}

export function PoaPanel({ report, loading, error }: Props) {
  if (error) {
    return (
      <div className="poa-panel">
        <p className="error">benchmark: {error}</p>
      </div>
    );
  }
  if (!report) {
    return (
      <div className="poa-panel">
        <p className="poa-loading">{loading ? "Running benchmark sweep…" : "No benchmark report."}</p>
      </div>
    );
  }

  const peak = peakPoaLevel(report);
  const level = peak?.demand_level ?? "";
  const cells = selectLevelCells(report, level);
  const present = PANEL_ROUTERS.filter((r) => cells[r]);

  return (
    <div className="poa-panel">
      <div className="poa-headline">
        <span className="poa-headline-label">Price of Anarchy</span>
        <span className="poa-headline-value">{fmtMetric(peak?.poa, 2)}×</span>
        <span className="poa-headline-sub">
          peak at {peak ? `${level} (v/c ${fmtMetric(peak.target_vc, 1)})` : "—"} — naive vs system-optimal
        </span>
      </div>

      <div className="poa-by-level">
        <div className="poa-by-level-title">PoA by demand level</div>
        <dl>
          {report.poa_by_level.map((lvl) => (
            <div key={lvl.demand_level} className="poa-level-row">
              <dt>
                {lvl.demand_level} <span className="poa-level-vc">v/c {fmtMetric(lvl.target_vc, 1)}</span>
              </dt>
              <dd>{fmtMetric(lvl.poa, 3)}×</dd>
            </div>
          ))}
        </dl>
      </div>

      <div className="poa-bars">
        <div className="poa-bars-title">
          Realized travel time at {level || "—"} — before (naive) vs after (reactive)
        </div>
        {present.length === 0 ? (
          <p className="poa-loading">No comparable cells at this level.</p>
        ) : (
          METRICS.map((m) => (
            <BarGroup
              key={m.key}
              label={m.label}
              digits={m.digits}
              cells={present.map((router) => ({
                router,
                value: cells[router].result[m.key] as number,
              }))}
            />
          ))
        )}
      </div>

      <div className="poa-improvement">
        <span className="poa-improvement-label">Headline improvement</span>
        <span className="poa-improvement-value">
          {fmtMetric(report.headline_improvement.percent_reduction, 1)}%
        </span>
        <span className="poa-improvement-sub">
          {report.headline_improvement.best_router} vs naive at {report.headline_improvement.demand_level}
        </span>
      </div>
    </div>
  );
}
