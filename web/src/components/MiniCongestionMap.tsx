// MiniCongestionMap — one small-multiple congestion map for a single algorithm in
// the six-up parallel comparison (#114). It mirrors the recolor-only PathLayer
// contract exactly: the PathLayer `data` is the shared static geometry, built ONCE,
// and recoloring as deltas arrive flows entirely through getColor +
// updateTriggers.getColor — never a geometry rebuild.
//
// Every visible fact is a NARROW Zustand slice selector, so a delta for one algorithm
// re-renders only the tiles that visibly changed — normally just that algorithm's own
// tile (plus, on a leader flip, the previous leader's tile so it can drop its ring):
//   - buckets[algo]        → this tile's coloring (its reference is the recolor trigger).
//   - metrics[algo]?.poa   → the PoA context label (systemoptimal ≈ 1 is the SO reference).
//   - leaderAlgo(...)===algo for each dimension → a BOOLEAN, so the winner ring only
//     re-renders the tile when its leader status actually flips, not every tick.
//   - selectedAlgo===algo  → the single-algo focus highlight; clicking a tile selects it.

import { useMemo } from "react";
import DeckGL from "@deck.gl/react";

import { ALGO_LABELS } from "../lib/algoLabels";
import { buildCongestionLayer } from "../lib/congestionLayer";
import { initialViewState, type GraphGeometry } from "../lib/graph";
import { leaderAlgo } from "../lib/leaderboard";
import { fmtMetric } from "../lib/metrics";
import { REFERENCE_ALGO, type Algo } from "../lib/protocol";
import { useAppStore } from "../store";

interface Props {
  geometry: GraphGeometry;
  algo: Algo;
}

export function MiniCongestionMap({ geometry, algo }: Props) {
  // Narrow per-algo subscriptions: a delta for another algorithm never re-renders us.
  const buckets = useAppStore((s) => s.congestion.buckets[algo]);
  const poa = useAppStore((s) => s.congestion.metrics[algo]?.poa ?? null);
  // Boolean leader selectors: identity-stable until the leader flips, so the winner
  // rings don't churn this tile every tick.
  const isComputeLeader = useAppStore(
    (s) => leaderAlgo(s.congestion.metrics, "route_median_ns") === algo,
  );
  const isTrafficLeader = useAppStore(
    (s) => leaderAlgo(s.congestion.metrics, "realized_total_s") === algo,
  );
  const isSelected = useAppStore((s) => s.selectedAlgo === algo);
  const setSelectedAlgo = useAppStore((s) => s.setSelectedAlgo);

  const view = useMemo(() => initialViewState(geometry), [geometry]);
  const isReference = algo === REFERENCE_ALGO;

  // Same recolor-only recipe as the full map (shared — see lib/congestionLayer.ts),
  // but thinner strokes and non-pickable since small multiples are read-only.
  const layer = buildCongestionLayer({
    id: `congestion-paths-${algo}`,
    segments: geometry.segments,
    buckets,
    width: 2,
    pickable: false,
  });

  const className = [
    "mini-map",
    isReference && "reference",
    isSelected && "selected",
    (isComputeLeader || isTrafficLeader) && "leader",
  ]
    .filter(Boolean)
    .join(" ");

  return (
    <figure className={className}>
      <figcaption className="mini-map-caption">
        <button
          type="button"
          className="mini-map-name"
          onClick={() => setSelectedAlgo(algo)}
          aria-pressed={isSelected}
        >
          {ALGO_LABELS[algo]}
          {isReference && <span className="ref-badge">ref</span>}
        </button>
        <span className="mini-map-poa">PoA {fmtMetric(poa, 3)}</span>
      </figcaption>
      {(isComputeLeader || isTrafficLeader) && (
        <div className="mini-map-badges">
          {isComputeLeader && <span className="leader-badge compute">⚡ fastest route</span>}
          {isTrafficLeader && <span className="leader-badge traffic">🏆 least traffic</span>}
        </div>
      )}
      <div className="mini-map-canvas">
        <DeckGL
          initialViewState={view}
          controller={false}
          layers={[layer]}
          style={{ position: "absolute", inset: "0" }}
        />
      </div>
    </figure>
  );
}
