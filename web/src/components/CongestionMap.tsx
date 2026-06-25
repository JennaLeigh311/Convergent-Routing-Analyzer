// CongestionMap — the deck.gl live congestion view.
//
// The geometry (the PathLayer's `data`) is built ONCE from /graph and never
// changes. Recoloring as deltas arrive flows entirely through `getColor` +
// `updateTriggers`: when the selected algorithm's bucket map updates, we bump the
// update trigger so deck.gl re-runs ONLY the color accessor over the existing
// geometry — no geometry rebuild. This is the explicit issue-#100 acceptance
// criterion (recolor-only). The bucket map is read at color time and joined to each
// segment by segment_id, keeping static geometry decoupled from live congestion.

import { useMemo } from "react";
import DeckGL from "@deck.gl/react";
import type { PickingInfo } from "@deck.gl/core";

import { buildCongestionLayer } from "../lib/congestionLayer";
import { initialViewState, type GraphGeometry, type SegmentGeometry } from "../lib/graph";
import type { BucketMap } from "../lib/congestion";

interface Props {
  geometry: GraphGeometry;
  /** segment_id -> bucket for the currently selected algorithm. */
  buckets: BucketMap;
}

export function CongestionMap({ geometry, buckets }: Props) {
  const view = useMemo(() => initialViewState(geometry), [geometry]);

  // The recolor-only PathLayer (shared recipe — see lib/congestionLayer.ts). The full
  // map is the interactive one: wide strokes and pickable for the tooltip below.
  const layer = buildCongestionLayer({
    id: "congestion-paths",
    segments: geometry.segments,
    buckets,
    width: 4,
    pickable: true,
  });

  return (
    <DeckGL
      initialViewState={view}
      controller={true}
      layers={[layer]}
      style={{ position: "absolute", top: "0", left: "0", right: "0", bottom: "0" }}
      getTooltip={({ object }: PickingInfo<SegmentGeometry>) =>
        object
          ? {
              text: `${object.segmentId}\nbucket ${buckets.get(object.segmentId) ?? 0}`,
            }
          : null
      }
    />
  );
}
