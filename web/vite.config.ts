/// <reference types="vitest/config" />
import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

// Vite config for the Convergent Routing Analyzer web app.
//
// Dev proxy: in `npm run dev` the browser talks to the Vite dev server, which
// proxies the engine's REST + WebSocket surface to the routing-server so there is
// NO hardcoded cross-origin URL in the app code. The proxy target is configurable
// via VITE_DEV_ENGINE_TARGET (default the engine's compose/local :8080). In
// production (the nginx image) the same `/api` + `/stream` paths are proxied by
// nginx to the `engine` service — so the app always speaks to its own origin and
// never bakes in a dev-only localhost (see web/nginx.conf and src/lib/engine.ts).
const engineTarget = process.env.VITE_DEV_ENGINE_TARGET ?? "http://localhost:8080";

export default defineConfig({
  plugins: [react()],
  server: {
    proxy: {
      // REST endpoints are reached under /api/* and rewritten to the engine root.
      "/api": {
        target: engineTarget,
        changeOrigin: true,
        rewrite: (path) => path.replace(/^\/api/, ""),
      },
      // The WebSocket stream keeps its path and upgrades through the proxy.
      "/stream": {
        target: engineTarget,
        changeOrigin: true,
        ws: true,
      },
    },
  },
  test: {
    globals: true,
    environment: "node",
    include: ["src/**/*.test.ts", "src/**/*.test.tsx"],
  },
});
