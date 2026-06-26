// Tests for the /compare client + segment_id -> geometry route-path resolution
// (#102). The pure bits (OD default, query building, path resolution) are exercised
// directly; loadCompare is exercised against a stubbed fetch. restUrl reads
// window.location.origin, so we stub window per the same-origin proxy default.

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import {
  buildSegmentIndex,
  compareUrl,
  defaultOD,
  formatLatLon,
  loadCompare,
  resolveRoutePath,
  type CompareResponse,
} from "./compare";
import type { GraphGeometry } from "./graph";

const geometry: GraphGeometry = {
  segments: [
    { segmentId: "a:0:F", path: [[-74, 40.7], [-73.99, 40.71]] },
    { segmentId: "b:1:F", path: [[-73.99, 40.71], [-73.98, 40.72]] },
    { segmentId: "c:2:F", path: [[-73.98, 40.72], [-73.97, 40.73]] },
  ],
  bounds: [-74, 40.7, -73.97, 40.73],
};

beforeEach(() => {
  vi.stubEnv("VITE_ENGINE_BASE", "");
  vi.stubGlobal("window", { location: { origin: "https://app.test" } });
});

afterEach(() => {
  vi.unstubAllGlobals();
  vi.unstubAllEnvs();
  vi.restoreAllMocks();
});

describe("defaultOD", () => {
  it("derives the bottom-left -> top-right diagonal from bounds", () => {
    const od = defaultOD(geometry.bounds);
    expect(od.from).toEqual({ lat: 40.7, lon: -74 });
    expect(od.to).toEqual({ lat: 40.73, lon: -73.97 });
  });
});

describe("formatLatLon + compareUrl", () => {
  it("formats coordinates as the engine's lat,lon token", () => {
    expect(formatLatLon({ lat: 40.7, lon: -74 })).toBe("40.7,-74");
  });

  it("builds a same-origin /compare URL with from/to query params", () => {
    const url = compareUrl(defaultOD(geometry.bounds));
    expect(url.startsWith("https://app.test/api/compare?")).toBe(true);
    const qs = new URL(url).searchParams;
    expect(qs.get("from")).toBe("40.7,-74");
    expect(qs.get("to")).toBe("40.73,-73.97");
  });
});

describe("buildSegmentIndex + resolveRoutePath", () => {
  it("joins an ordered segment_id list to geometry paths, in order", () => {
    const index = buildSegmentIndex(geometry);
    const resolved = resolveRoutePath(index, ["c:2:F", "a:0:F"]);
    expect(resolved.map((s) => s.segmentId)).toEqual(["c:2:F", "a:0:F"]);
    expect(resolved[0].path).toEqual([[-73.98, 40.72], [-73.97, 40.73]]);
  });

  it("skips segment_ids with no geometry rather than throwing", () => {
    const index = buildSegmentIndex(geometry);
    const resolved = resolveRoutePath(index, ["a:0:F", "missing:9:F", "b:1:F"]);
    expect(resolved.map((s) => s.segmentId)).toEqual(["a:0:F", "b:1:F"]);
  });
});

describe("loadCompare", () => {
  it("parses the naive + reactive routes from the /compare body", async () => {
    const body: CompareResponse = {
      from: { lat: 40.7, lon: -74 },
      to: { lat: 40.73, lon: -73.97 },
      naive: { algorithm: "naive", segments: ["a:0:F", "b:1:F"], cost_s: 120 },
      reactive: { algorithm: "reactive", segments: ["a:0:F", "c:2:F"], cost_s: 95 },
    };
    const fetchMock = vi.fn(async () => new Response(JSON.stringify(body), { status: 200 }));
    vi.stubGlobal("fetch", fetchMock);

    const got = await loadCompare(defaultOD(geometry.bounds));
    expect(got.naive.segments).toEqual(["a:0:F", "b:1:F"]);
    expect(got.reactive.cost_s).toBe(95);
    expect(fetchMock).toHaveBeenCalledOnce();
  });

  it("throws on a non-2xx response", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => new Response("nope", { status: 422, statusText: "Unprocessable Entity" })),
    );
    await expect(loadCompare(defaultOD(geometry.bounds))).rejects.toThrow(/422/);
  });
});
