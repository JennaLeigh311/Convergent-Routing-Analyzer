import { describe, expect, it } from "vitest";

import { bucketColor } from "./colorRamp";
import { VC_BUCKET_COUNT } from "./protocol";

describe("bucketColor", () => {
  it("maps free-flow (bucket 0) to green and saturation to red", () => {
    expect(bucketColor(0)).toEqual([38, 166, 91]);
    expect(bucketColor(VC_BUCKET_COUNT - 1)).toEqual([192, 57, 43]);
  });

  it("clamps out-of-range buckets to the ramp endpoints", () => {
    expect(bucketColor(-5)).toEqual(bucketColor(0));
    expect(bucketColor(999)).toEqual(bucketColor(VC_BUCKET_COUNT - 1));
  });

  it("returns a valid RGB triple for every bucket", () => {
    for (let b = 0; b < VC_BUCKET_COUNT; b++) {
      const c = bucketColor(b);
      expect(c).toHaveLength(3);
      for (const ch of c) {
        expect(ch).toBeGreaterThanOrEqual(0);
        expect(ch).toBeLessThanOrEqual(255);
        expect(Number.isInteger(ch)).toBe(true);
      }
    }
  });

  it("trends warmer: the high-congestion end is redder than free-flow", () => {
    const freeFlow = bucketColor(0);
    const saturated = bucketColor(VC_BUCKET_COUNT - 1);
    expect(saturated[0]).toBeGreaterThan(freeFlow[0]); // more red
    expect(saturated[1]).toBeLessThan(freeFlow[1]); // less green
  });
});
