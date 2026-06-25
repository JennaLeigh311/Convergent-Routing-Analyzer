// MiniCongestionMap — one small-multiple congestion map for a single algorithm in
// the six-up comparison view (#101). It mirrors CongestionMap's recolor-only
// contract exactly: the PathLayer `data` is the shared static geometry, built ONCE,
// and recoloring as deltas arrive flows entirely through getColor +
// updateTriggers.getColor — never a geometry rebuild.
//
// Crucially, each panel subscribes to JUST its own algorithm's slices via narrow
// Zustand selectors (buckets[algo] and metrics[algo].poa). A delta for one algorithm
// changes only that algorithm's bucket-map reference, so React re-renders only that
// panel and deck.gl re-runs only that panel's color accessor; the other five panels
// are untouched.

import { useMemo } from "react";
import DeckGL from "@deck.gl/react";

import { ALGO_LABELS } from "../lib/algoLabels";
import { buildCongestionLayer } from "../lib/congestionLayer";
import { initialViewState, type GraphGeometry } from "../lib/graph";
import { REFERENCE_ALGO, fmtMetric } from "../lib/metrics";
import type { Algo } from "../lib/protocol";
import { useAppStore } from "../store";

interface Props {
  geometry: GraphGeometry;
  algo: Algo;
}

export function MiniCongestionMap({ geometry, algo }: Props) {
  // Narrow per-algo subscriptions: a delta for another algorithm never re-renders us.
  const buckets = useAppStore((s) => s.congestion.buckets[algo]);
  const poa = useAppStore((s) => s.congestion.metrics[algo]?.poa ?? null);

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

  return (
    <figure className={isReference ? "mini-map reference" : "mini-map"}>
      <figcaption className="mini-map-caption">
        <span className="mini-map-name">
          {ALGO_LABELS[algo]}
          {isReference && <span className="ref-badge">ref</span>}
        </span>
        <span className="mini-map-poa">PoA {fmtMetric(poa, 3)}</span>
      </figcaption>
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
