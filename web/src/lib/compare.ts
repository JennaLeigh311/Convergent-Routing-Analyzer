// GET /compare client + segment_id -> geometry route-path resolution for the
// before/after route-overlay view (#102). /compare routes the SAME OD pair through
// naive (free-flow selfish) and reactive (congestion-aware BPR) and returns each as
// an ordered segment_id list; this module fetches that body and joins each route's
// segment_ids to the shared /graph geometry (the immutable segment_id -> [lon,lat][]
// table built once in lib/graph.ts), so the overlay PathLayers reuse the same
// geometry the live map does — never a second geometry source.
//
// It is pure and side-effect-free apart from the single fetch: the OD default, the
// query building, and the route-path resolution are all separately testable. The
// engine contract (engine/internal/api/route.go) is mirrored narrowly — only the
// fields the overlay consumes.

import { restUrl } from "./engine";
import type { GraphGeometry } from "./graph";

/** One routed alternative from /compare: the algorithm, its ordered segment_ids,
 * and the routing cost it was optimized against (cost_s — NOT a realized time). */
export interface RouteResult {
  algorithm: string;
  segments: string[];
  cost_s: number;
}

/** An echoed endpoint coordinate ({lat, lon}), as /compare returns it. */
export interface Coordinate {
  lat: number;
  lon: number;
}

/** The GET /compare body: the OD pair plus the naive and reactive routes over it. */
export interface CompareResponse {
  from: Coordinate;
  to: Coordinate;
  naive: RouteResult;
  reactive: RouteResult;
}

/** An origin/destination pair for a /compare request, in {lat, lon} form. */
export interface ODPair {
  from: Coordinate;
  to: Coordinate;
}

/**
 * A default OD pair derived from opposite corners of the network bounds: the
 * bottom-left (minLat, minLon) to the top-right (maxLat, maxLon). The engine snaps
 * each endpoint to the nearest graph node, so the corners need not lie on a road —
 * this just gives the longest-diagonal demo route without requiring the user to
 * click. bounds is [minLon, minLat, maxLon, maxLat] (GeoJSON axis order from /graph).
 */
export function defaultOD(bounds: [number, number, number, number]): ODPair {
  const [minLon, minLat, maxLon, maxLat] = bounds;
  return {
    from: { lat: minLat, lon: minLon },
    to: { lat: maxLat, lon: maxLon },
  };
}

/** Format a coordinate as the engine's "lat,lon" query token. */
export function formatLatLon(c: Coordinate): string {
  return `${c.lat},${c.lon}`;
}

/** Build the GET /compare URL for an OD pair (same-origin via restUrl). */
export function compareUrl(od: ODPair): string {
  const qs = new URLSearchParams({
    from: formatLatLon(od.from),
    to: formatLatLon(od.to),
  });
  return `${restUrl("/compare")}?${qs.toString()}`;
}

/** Fetch and parse GET /compare for an OD pair. Throws on a non-2xx response. */
export async function loadCompare(od: ODPair, signal?: AbortSignal): Promise<CompareResponse> {
  const res = await fetch(compareUrl(od), { signal });
  if (!res.ok) {
    throw new Error(`GET /compare failed: ${res.status} ${res.statusText}`);
  }
  return (await res.json()) as CompareResponse;
}

/** A resolved route segment: its id and the [lon, lat] path from /graph geometry. */
export interface RoutePathSegment {
  segmentId: string;
  path: [number, number][];
}

/** Build a segment_id -> path lookup from the shared geometry, once per geometry. */
export function buildSegmentIndex(geometry: GraphGeometry): Map<string, [number, number][]> {
  const index = new Map<string, [number, number][]>();
  for (const seg of geometry.segments) {
    index.set(seg.segmentId, seg.path);
  }
  return index;
}

/**
 * Resolve an ordered segment_id list to its geometry paths. A segment_id with no
 * geometry (a route over an edge /graph did not emit) is skipped rather than
 * throwing — the overlay then draws the segments it can rather than failing the
 * whole route — so the returned list may be shorter than the input. Order is
 * preserved for the segments that do resolve.
 */
export function resolveRoutePath(
  index: Map<string, [number, number][]>,
  segments: string[],
): RoutePathSegment[] {
  const out: RoutePathSegment[] = [];
  for (const segmentId of segments) {
    const path = index.get(segmentId);
    if (path) out.push({ segmentId, path });
  }
  return out;
}
