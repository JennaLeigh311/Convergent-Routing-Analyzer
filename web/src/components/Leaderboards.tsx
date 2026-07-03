// Leaderboards — the two live leaderboards over the six-up parallel view (#114):
//   1. "Fastest to compute a route" — ranks by route_median_ns ascending (the fair
//      per-route latency from #112; NOT the deprecated cumulative compute_ms).
//   2. "Best at minimizing traffic" — ranks by realized_total_s ascending (the
//      realized travel time; NOT poa, which #112 froze into a constant reference).
//
// Both boards re-sort live and crown a clear winner. The container subscribes ONCE to
// the metrics record (which the reducer replaces every frame), so the boards re-rank
// each tick; the ranking itself is the pure, unit-tested rankByMetric. The six maps
// keep their own per-algo subscriptions, so re-ranking here never re-renders them.

import { useMemo } from "react";

import { ALGO_LABELS } from "../lib/algoLabels";
import { rankByMetric, type RankEntry } from "../lib/leaderboard";
import { useAppStore } from "../store";

interface BoardProps {
  title: string;
  /** The honest metric this board ranks on, in plain words. */
  hint: string;
  entries: RankEntry[];
  /** Render a ranked value in this board's unit (µs, seconds…). */
  format: (value: number) => string;
}

function Leaderboard({ title, hint, entries, format }: BoardProps) {
  // The winner is #1 only once it actually has a metric; before the first tick every
  // value is null, so there is no winner yet (placeholder rows, no fake crown).
  const winner = entries[0]?.value != null ? entries[0].algo : null;

  return (
    <section className="leaderboard">
      <header className="leaderboard-head">
        <h2 className="leaderboard-title">{title}</h2>
        <span className="leaderboard-hint">{hint}</span>
      </header>
      <ol className="leaderboard-list">
        {entries.map((entry, i) => {
          const isWinner = entry.algo === winner;
          return (
            <li
              key={entry.algo}
              className={isWinner ? "leaderboard-row winner" : "leaderboard-row"}
            >
              <span className="leaderboard-rank">{isWinner ? "🏆" : i + 1}</span>
              <span className="leaderboard-name">
                {ALGO_LABELS[entry.algo]}
                {isWinner && <span className="winner-badge">winner</span>}
              </span>
              <span className="leaderboard-value">
                {entry.value != null ? format(entry.value) : "—"}
              </span>
            </li>
          );
        })}
      </ol>
    </section>
  );
}

/** The two live leaderboards, ranked from the one live metrics record. */
export function Leaderboards() {
  const metrics = useAppStore((s) => s.congestion.metrics);

  // Re-rank only when the metrics record reference changes (i.e. a new frame landed).
  const boards = useMemo(
    () => ({
      compute: rankByMetric(metrics, "route_median_ns"),
      traffic: rankByMetric(metrics, "realized_total_s"),
    }),
    [metrics],
  );

  return (
    <div className="leaderboards">
      <Leaderboard
        title="Fastest to compute a route"
        hint="median µs per route — lower is better"
        entries={boards.compute}
        format={(v) => `${(v / 1000).toFixed(1)} µs`}
      />
      <Leaderboard
        title="Best at minimizing traffic"
        hint="realized total travel time (s) — lower is better"
        entries={boards.traffic}
        format={(v) => `${v.toFixed(1)} s`}
      />
    </div>
  );
}
