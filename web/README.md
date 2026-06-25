# Web — live per-segment congestion map

The Convergent Routing Analyzer frontend (Phase 6, issue #100): a **Vite + React +
TypeScript + Zustand + deck.gl** app that renders the live per-segment congestion
map driven by the engine's §R6 `/stream` WebSocket, joined to the static `/graph`
geometry on the client by `segment_id`. See `../docs/api.md` and `project-spec.md
§R6`.

## What it does

- Loads the road network geometry **once** from `GET /graph` (GeoJSON, `[lon,lat]`
  order, `properties.segment_id` join key) and builds a `segment_id → geometry`
  lookup that never changes.
- Opens the `GET /stream` WebSocket (browser-native `WebSocket`) and folds each
  algorithm's `snapshot` + bucketed `delta` frames into a `Map<segment_id, bucket>`
  per algorithm via a **pure reducer** (`src/lib/congestion.ts`) — snapshot + all
  deltas in order reproduce the engine's full bucketed state (the delta-correctness
  invariant, asserted in `congestion.test.ts`).
- Colors a deck.gl `PathLayer` green→red by v/c bucket. Recoloring as deltas arrive
  flows through `getColor` + `updateTriggers` only — **the geometry is never
  rebuilt**.
- An algorithm selector switches which of the six canonical `RouterOrder` streams
  (`naive`, `reactive`, `incremental`, `msa`, `systemoptimal`, `multipath`) the map
  paints; all six stream concurrently over the one socket, so switching is instant.
  (Issue #100 renders one at a time; the six-up comparison is #101.)

## Engine endpoint resolution (no hardcoded localhost)

The app speaks to its **own origin** under stable paths and lets the serving layer
route to the engine (`src/lib/engine.ts`):

- **Production (nginx image):** `nginx.conf` reverse-proxies `/api/*` → engine REST
  and `/stream` → engine WebSocket. The upstream is `ENGINE_UPSTREAM`
  (default `engine:8080`, the compose service), templated in at container start.
- **Dev (`npm run dev`):** the Vite dev server proxies the same paths
  (`vite.config.ts`); target overridable via `VITE_DEV_ENGINE_TARGET`.
- A deployment can override the engine origin without a rebuild via
  `window.__ENGINE_BASE__`, or at build time via `VITE_ENGINE_BASE`.

## Develop

```sh
npm install
npm run dev        # Vite dev server (proxies /api + /stream to localhost:8080)
npm run build      # tsc -b && vite build  -> dist/
npm run lint       # eslint
npm test           # vitest (reducer delta-correctness + color ramp + graph)
```

Run the engine alongside dev: `(cd ../engine && go run ./cmd/routing-server)`.

## Run in compose (the deliverable)

```sh
docker compose up --build      # core profile: engine + web
```

Then open the published web port (default `http://localhost:3000`). The map
animates live per-segment congestion for the selected algorithm against the running
engine.
