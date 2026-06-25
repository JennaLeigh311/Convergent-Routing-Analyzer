// Tests for engine endpoint resolution — the branchy bit: runtime override vs the
// same-origin /api proxy, the /api strip for the WebSocket path, and the http->ws
// (and https->wss) scheme swap. These read window / import.meta.env at CALL time,
// so we stub them per case.

import { afterEach, describe, expect, it, vi } from "vitest";

import { httpBase, restUrl, streamUrl } from "./engine";

afterEach(() => {
  vi.unstubAllGlobals();
  vi.unstubAllEnvs();
});

describe("engine endpoint resolution", () => {
  describe("runtime override (window.__ENGINE_BASE__)", () => {
    it("wins over the proxy default and trims a trailing slash", () => {
      vi.stubGlobal("window", {
        __ENGINE_BASE__: "http://engine.test:8080/",
        location: { origin: "https://app.test" },
      });
      expect(httpBase()).toBe("http://engine.test:8080");
      expect(restUrl("/graph")).toBe("http://engine.test:8080/graph");
      // No /api prefix on an explicit base, so /stream hangs off the base directly.
      expect(streamUrl("")).toBe("ws://engine.test:8080/stream");
      expect(streamUrl("speed=120")).toBe("ws://engine.test:8080/stream?speed=120");
    });
  });

  describe("same-origin proxy default (no override)", () => {
    it("serves REST under /api and drops /api for the wss stream", () => {
      vi.stubEnv("VITE_ENGINE_BASE", ""); // ensure the build-time override is unset
      vi.stubGlobal("window", { location: { origin: "https://app.test" } });
      expect(httpBase()).toBe("https://app.test/api");
      expect(restUrl("/graph")).toBe("https://app.test/api/graph");
      // /stream lives at the origin root (not under /api) and upgrades to wss.
      expect(streamUrl("")).toBe("wss://app.test/stream");
      expect(streamUrl("seed=1")).toBe("wss://app.test/stream?seed=1");
    });

    it("normalizes a path passed without a leading slash", () => {
      vi.stubEnv("VITE_ENGINE_BASE", "");
      vi.stubGlobal("window", { location: { origin: "http://localhost:3000" } });
      expect(restUrl("graph")).toBe("http://localhost:3000/api/graph");
    });
  });

  describe("build-time override (VITE_ENGINE_BASE)", () => {
    it("is used when no runtime override is present", () => {
      vi.stubEnv("VITE_ENGINE_BASE", "https://api.example:9000");
      vi.stubGlobal("window", { location: { origin: "https://app.test" } });
      expect(httpBase()).toBe("https://api.example:9000");
      expect(streamUrl("")).toBe("wss://api.example:9000/stream");
    });
  });
});
