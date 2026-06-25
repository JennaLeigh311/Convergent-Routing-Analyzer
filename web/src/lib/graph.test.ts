import { describe, expect, it } from "vitest";

import { boundsCenter, buildGeometry } from "./graph";
import type { GraphFeatureCollection } from "./protocol";

const fc: GraphFeatureCollection = {
  type: "FeatureCollection",
  features: [
    {
      type: "Feature",
      geometry: {
        type: "LineString",
        coordinates: [
          [-73.9749, 40.7374],
          [-73.9724, 40.7386],
        ],
      },
      properties: { segment_id: "905512:0:F" },
    },
    {
      type: "Feature",
      geometry: {
        type: "LineString",
        coordinates: [
          [-73.97, 40.74],
          [-73.96, 40.745],
        ],
      },
      properties: { segment_id: "905512:1:F" },
    },
  ],
};

describe("buildGeometry", () => {
  it("builds one SegmentGeometry per LineString feature, keyed by segment_id", () => {
    const g = buildGeometry(fc);
    expect(g.segments).toHaveLength(2);
    expect(g.segments.map((s) => s.segmentId)).toEqual(["905512:0:F", "905512:1:F"]);
    // Coordinates are kept verbatim in [lon, lat] order.
    expect(g.segments[0].path[0]).toEqual([-73.9749, 40.7374]);
  });

  it("computes a bounds box covering all vertices", () => {
    const g = buildGeometry(fc);
    expect(g.bounds).toEqual([-73.9749, 40.7374, -73.96, 40.745]);
    expect(boundsCenter(g.bounds)).toEqual([(-73.9749 + -73.96) / 2, (40.7374 + 40.745) / 2]);
  });

  it("falls back to a default bounds when the collection is empty", () => {
    const g = buildGeometry({ type: "FeatureCollection", features: [] });
    expect(g.segments).toHaveLength(0);
    expect(g.bounds).toHaveLength(4);
    expect(Number.isFinite(g.bounds[0])).toBe(true);
  });
});
