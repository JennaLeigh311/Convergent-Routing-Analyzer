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
import { PathLayer } from "@deck.gl/layers";
import type { Color, PickingInfo } from "@deck.gl/core";

import { bucketColor } from "../lib/colorRamp";
import { boundsCenter, type GraphGeometry, type SegmentGeometry } from "../lib/graph";
import type { BucketMap } from "../lib/congestion";

interface Props {
  geometry: GraphGeometry;
  /** segment_id -> bucket for the currently selected algorithm. */
  buckets: BucketMap;
}

/** Fit the initial camera to the network bounds. */
function initialViewState(geometry: GraphGeometry) {
  const [lon, lat] = boundsCenter(geometry.bounds);
  const [minLon, minLat, maxLon, maxLat] = geometry.bounds;
  // A rough zoom from the bounds span; the toy graph is small so this is generous.
  const span = Math.max(maxLon - minLon, maxLat - minLat, 1e-4);
  const zoom = Math.min(18, Math.max(10, Math.log2(360 / span)));
  return { longitude: lon, latitude: lat, zoom, pitch: 0, bearing: 0 };
}

export function CongestionMap({ geometry, buckets }: Props) {
  const view = useMemo(() => initialViewState(geometry), [geometry]);

  // The PathLayer. `data` is the immutable geometry table. getColor reads the live
  // bucket map by segment_id; updateTriggers.getColor changes identity whenever the
  // bucket map reference changes, so deck.gl recomputes colors WITHOUT rebuilding
  // the path geometry.
  const layer = new PathLayer<SegmentGeometry>({
    id: "congestion-paths",
    data: geometry.segments,
    pickable: true,
    widthUnits: "pixels",
    getWidth: 4,
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
