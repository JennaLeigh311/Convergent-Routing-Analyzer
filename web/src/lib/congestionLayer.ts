// buildCongestionLayer — the single source of truth for the recolor-only PathLayer
// recipe shared by the full CongestionMap (#100) and the comparison view's
// MiniCongestionMaps (#101). Centralizing it here keeps the load-bearing invariant in
// ONE place: immutable `data` (the static geometry table), color joined to live
// buckets by segment_id at draw time, and `updateTriggers.getColor` keyed on the
// bucket-map reference so a delta re-runs ONLY the color accessor — never a geometry
// rebuild. Callers vary only the cosmetic/interaction params (id, stroke width,
// pickability); controller/tooltip/DeckGL wiring stays in each component.

import { PathLayer } from "@deck.gl/layers";
import type { Color } from "@deck.gl/core";

import { bucketColor } from "./colorRamp";
import type { BucketMap } from "./congestion";
import type { SegmentGeometry } from "./graph";

interface CongestionLayerOptions {
  /** Stable layer id (deck.gl reconciles layers by id across renders). */
  id: string;
  /** The immutable geometry table — the same reference for the layer's life. */
  segments: SegmentGeometry[];
  /** segment_id -> bucket for this layer's algorithm; its reference is the recolor trigger. */
  buckets: BucketMap;
  /** Stroke width in pixels (4 for the full map, 2 for small multiples). */
  width: number;
  /** Whether the layer participates in picking (the full map; not the mini maps). */
  pickable: boolean;
}

/** Build the recolor-only congestion PathLayer. See module header for the invariant. */
export function buildCongestionLayer({
  id,
  segments,
  buckets,
  width,
  pickable,
}: CongestionLayerOptions): PathLayer<SegmentGeometry> {
  return new PathLayer<SegmentGeometry>({
    id,
    data: segments,
    pickable,
    widthUnits: "pixels",
    getWidth: width,
    capRounded: true,
    jointRounded: true,
    getPath: (d) => d.path,
    getColor: (d): Color => {
      const [r, g, b] = bucketColor(buckets.get(d.segmentId) ?? 0);
      return [r, g, b, 230];
    },
    updateTriggers: {
      // Recolor-only trigger: identity changes per new bucket-map reference.
      getColor: buckets,
    },
  });
}
