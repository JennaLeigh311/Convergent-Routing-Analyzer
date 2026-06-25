// Green -> red color ramp over the 24 v/c buckets. Bucket 0 (free-flow) is green,
// the saturating top bucket (23) is deep red, interpolated through yellow/orange.
// Returned as an [r, g, b] tuple deck.gl PathLayer consumes directly. A missing
// segment (no congestion observed) reads as bucket 0 by convention.

import { VC_BUCKET_COUNT } from "./protocol";

export type RGB = [number, number, number];

// Stops along the ramp (free-flow -> saturated), interpolated by bucket fraction.
const STOPS: RGB[] = [
  [38, 166, 91], // green     (free-flow)
  [241, 196, 15], // yellow
  [230, 126, 34], // orange
  [192, 57, 43], // red       (saturated)
];

function lerp(a: number, b: number, t: number): number {
  return Math.round(a + (b - a) * t);
}

/**
 * Map a v/c bucket index to an [r, g, b] color. The index is clamped to
 * [0, VC_BUCKET_COUNT-1]; the saturating top bucket maps to the deepest red.
 */
export function bucketColor(bucket: number): RGB {
  const maxBucket = VC_BUCKET_COUNT - 1;
  const b = Math.max(0, Math.min(maxBucket, bucket));
  const frac = maxBucket === 0 ? 0 : b / maxBucket; // 0..1 across the ramp

  const seg = frac * (STOPS.length - 1);
  const i = Math.min(STOPS.length - 2, Math.floor(seg));
  const t = seg - i;
  const lo = STOPS[i];
  const hi = STOPS[i + 1];
  return [lerp(lo[0], hi[0], t), lerp(lo[1], hi[1], t), lerp(lo[2], hi[2], t)];
}
