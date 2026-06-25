// Static geometry source: GET /graph once, build the segment_id -> geometry lookup
// a single time, and never touch it again. Congestion is joined to these segments
// by segment_id at color time (updateTriggers), so a recolor is NEVER a geometry
// rebuild — the explicit §R6 / issue-#100 acceptance criterion.

import { restUrl } from "./engine";
import type { GraphFeatureCollection } from "./protocol";

/** One road segment's static geometry: its id and its [lon, lat] path. */
export interface SegmentGeometry {
  segmentId: string;
  /** Ordered [lon, lat] vertices, verbatim from /graph (GeoJSON axis order). */
  path: [number, number][];
}

/** The immutable geometry table the PathLayer renders; built once from /graph. */
export interface GraphGeometry {
  segments: SegmentGeometry[];
  /** Bounding box [minLon, minLat, maxLon, maxLat] for the initial camera fit. */
  bounds: [number, number, number, number];
}

/** Fetch /graph and build the segment_id -> geometry table exactly once. */
export async function loadGraph(signal?: AbortSignal): Promise<GraphGeometry> {
  const res = await fetch(restUrl("/graph"), { signal });
  if (!res.ok) {
    throw new Error(`GET /graph failed: ${res.status} ${res.statusText}`);
  }
  const fc = (await res.json()) as GraphFeatureCollection;
  return buildGeometry(fc);
}

/** Build the geometry table + bounds from a GeoJSON FeatureCollection. */
export function buildGeometry(fc: GraphFeatureCollection): GraphGeometry {
  const segments: SegmentGeometry[] = [];
  let minLon = Infinity;
  let minLat = Infinity;
  let maxLon = -Infinity;
  let maxLat = -Infinity;

  for (const feat of fc.features ?? []) {
    if (feat.geometry?.type !== "LineString") continue;
    const path = feat.geometry.coordinates;
    segments.push({ segmentId: feat.properties.segment_id, path });
    for (const [lon, lat] of path) {
      if (lon < minLon) minLon = lon;
      if (lat < minLat) minLat = lat;
      if (lon > maxLon) maxLon = lon;
      if (lat > maxLat) maxLat = lat;
    }
  }

  const bounds: [number, number, number, number] = Number.isFinite(minLon)
    ? [minLon, minLat, maxLon, maxLat]
    : [-74, 40.7, -73.9, 40.8]; // sane fallback if /graph is empty

  return { segments, bounds };
}

/** Center [lon, lat] of a bounds box, for the deck.gl initial view state. */
export function boundsCenter(bounds: [number, number, number, number]): [number, number] {
  return [(bounds[0] + bounds[2]) / 2, (bounds[1] + bounds[3]) / 2];
}

/**
 * Fit a deck.gl camera to the network bounds. Shared by the single-algo map and the
 * comparison view's small multiples so every panel frames the same network identically
 * (the only thing that differs between panels is the live coloring, not the geometry).
 */
export function initialViewState(geometry: GraphGeometry) {
  const [lon, lat] = boundsCenter(geometry.bounds);
  const [minLon, minLat, maxLon, maxLat] = geometry.bounds;
  // A rough zoom from the bounds span; the toy graph is small so this is generous.
  const span = Math.max(maxLon - minLon, maxLat - minLat, 1e-4);
  const zoom = Math.min(18, Math.max(10, Math.log2(360 / span)));
  return { longitude: lon, latitude: lat, zoom, pitch: 0, bearing: 0 };
}
