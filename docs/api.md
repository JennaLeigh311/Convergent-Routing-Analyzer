# API Reference

The `routing-server` binary (`engine/cmd/routing-server`) serves the REST surface
in front of the routing engine. The handler logic lives in `engine/internal/api`;
the binary owns only the listener, server timeouts, and lifecycle.

All routing endpoints operate over an **immutable** road graph loaded **once** at
startup (the toy `edge_attributes` GeoJSON, compiled into the binary so the
container needs no testdata on disk). The graph is shared read-only across
goroutines; a `Server` is safe for concurrent use.

Responses are JSON (`Content-Type: application/json; charset=utf-8`). Errors use a
uniform envelope:

```json
{ "error": "human-readable message" }
```

Error messages are sanitized: an unroutable request logs the raw node ids /
coordinates server-side (via `slog`) but the response body never echoes
coordinates or PII.

The base URL is the configured listen address (`ROUTING_SERVER_ADDR`, default
`:8080`).

---

## Authentication & rate limiting

The **routing surface** (`/route`, `/compare`, `/congestion`, `/graph`,
`/benchmark`, `/benchmark/{id}`, `/stream`) is fronted by a security middleware.
The liveness/readiness/observability endpoints (`/healthz`, `/readyz`,
`/metrics`) are deliberately **exempt** — probes and Prometheus scrapes are never
gated by auth or throttling.

### Bearer-token auth (off by default)

| Env var            | Default | Effect                                                            |
| ------------------ | ------- | ----------------------------------------------------------------- |
| `ROUTING_API_TOKEN` | _(unset)_ | Static bearer token. **Unset/empty ⇒ auth disabled** (the open demo/dev default). |

When `ROUTING_API_TOKEN` is set, every routing request must present
`Authorization: Bearer <token>`. A missing or wrong token is a **`401`** with the
uniform error envelope. The token is compared in **constant time**
(`crypto/subtle`), so a wrong token cannot be distinguished by response timing.

`/stream` is a WebSocket upgrade; the `Authorization` header travels on the
handshake, so the same auth applies (the request is checked **before** the
upgrade).

### Per-client rate limiting (on by default)

A hand-rolled token-bucket limiter throttles each client independently.

| Env var                    | Default | Effect                                              |
| -------------------------- | ------- | --------------------------------------------------- |
| `ROUTING_RATE_LIMIT_RPS`   | `20`    | Steady-state requests/second per client. **`<= 0` ⇒ rate limiting disabled.** |
| `ROUTING_RATE_LIMIT_BURST` | `40`    | Burst capacity (max instantaneous requests) per client. |

- **Per-client key:** the **bearer token** when auth is enabled, otherwise the
  **client IP** (the host of `r.RemoteAddr`).
- Exceeding the limit is a **`429`** with the uniform error envelope and a
  `Retry-After` header (seconds until a token refills).
- The per-client bucket map is **bounded**: idle buckets are evicted lazily on
  access (no background goroutine), so memory cannot grow without bound as
  clients come and go.

> **Reverse-proxy assumption:** the client IP is taken from `r.RemoteAddr`;
> `X-Forwarded-For` is **not** trusted (it is client-spoofable when not behind a
> proxy). A **TLS-terminating reverse proxy is assumed** in front of the service;
> trust `X-Forwarded-For` only when such a proxy sets it. For IP-keyed limiting to
> be accurate behind a proxy, the proxy must preserve/rewrite the peer address (or
> be rate-limited itself).

---

## Liveness / readiness / observability

| Method | Path       | Description                                                      |
| ------ | ---------- | ---------------------------------------------------------------- |
| GET    | `/healthz` | Liveness — `200 ok` once the process is up.                      |
| GET    | `/readyz`  | Readiness — `200 ok` once the graph has loaded.                  |
| GET    | `/metrics` | Prometheus scrape: Go runtime/process series + routing counters. |

The routing counter `routing_requests_total{endpoint,outcome}` is incremented for
every handled routing request, partitioned by the logical endpoint (`route`,
`compare`, `congestion`, `graph`, `benchmark`, `benchmark_status`, `stream`) and a
coarse `outcome` (`ok` | `error`). It is registered against the same registry
`/metrics` scrapes. The `stream` endpoint records one `ok`/`error` per WebSocket
connection attempt (not per frame).

---

## `GET /route`

Single A→B route over the graph.

**Query parameters**

| Param  | Required | Description                                                        |
| ------ | -------- | ------------------------------------------------------------------ |
| `from` | yes      | Origin as `lat,lon` decimal degrees (e.g. `40.7374,-73.9749`).     |
| `to`   | yes      | Destination as `lat,lon` decimal degrees.                          |
| `algo` | no       | `naive` (default, free-flow shortest path) or `reactive` (BPR over the shared congestion snapshot). |

**`200` response**

```json
{
  "algorithm": "naive",
  "segments": ["905512:0:F", "905512:1:F"],
  "cost_s": 42.0
}
```

- `segments` — the chosen path as an ordered list of `segment_id`s. These join to
  `/graph` geometry on the client (§R2). An origin that snaps to the destination
  node is a clean empty list (a path to where you already are), not an error.
- `cost_s` — the routing cost in seconds the path was **optimized against** (summed
  free-flow time for `naive`; summed congested BPR cost for `reactive`). It is
  **not** a realized travel time.

**Errors**

| Status | When                                                          |
| ------ | ------------------------------------------------------------ |
| `400`  | Missing/malformed `from`/`to`, or an unknown `algo`.         |
| `405`  | Non-`GET` method.                                           |
| `422`  | The OD pair is unreachable / an endpoint snaps to no node.   |

An unreachable OD pair is always a clean `422` — never a panic or a `NaN` cost.

---

## `GET /compare`

Naive vs. congestion-aware routing on the **same** OD pair — the §6 divert demo.

**Query parameters:** `from`, `to` (as for `/route`).

**`200` response**

```json
{
  "from": { "lat": 40.7374, "lon": -73.9749 },
  "to":   { "lat": 40.7399, "lon": -73.9699 },
  "naive":    { "algorithm": "naive",    "segments": ["9000001:0:F"], "cost_s": 30.0 },
  "reactive": { "algorithm": "reactive", "segments": ["905512:0:F", "905512:1:F"], "cost_s": 55.0 }
}
```

Both sides must route for a meaningful comparison: if **either** the naive or the
reactive route is unreachable the request fails with `422`. Bad coordinates are a
`400`; a non-`GET` is a `405`.

---

## `GET /congestion`

Current per-segment congestion snapshot.

**`200` response**

```json
{
  "segments": [
    { "segment_id": "905512:0:F", "load_vph": 1200.0 }
  ]
}
```

- Keyed by `segment_id` (the frozen §1 contract), **never** by the internal
  numeric `EdgeID`.
- Only segments with a positive load are listed; an absent segment reads as zero
  load by convention. The list is sorted by `segment_id` so successive polls are
  diffable.

The snapshot comes from the shared, deterministic simulator congestion provider
(fixed seed); with no load injected it is an empty list — an honest "no
congestion", not a placeholder.

A non-`GET` is a `405`.

---

## `GET /graph`

The network geometry as GeoJSON — the frontend's geometry source.

**`200` response** (a GeoJSON `FeatureCollection`)

```json
{
  "type": "FeatureCollection",
  "features": [
    {
      "type": "Feature",
      "geometry": { "type": "LineString", "coordinates": [[-73.9749, 40.7374], [-73.9724, 40.7386]] },
      "properties": { "segment_id": "905512:0:F" }
    }
  ]
}
```

- Coordinates are `[lon, lat]` (GeoJSON axis order), verbatim from the loader.
- Each feature's `properties` carries **only** the `segment_id` join key.
- **No congestion is baked in** (§R2): the client colors the network by joining
  `/congestion` to these `segment_id`s on the client side. This keeps the
  cacheable, static geometry decoupled from the live, polled congestion.
- Features are sorted by `segment_id` for a stable, cacheable body.

**Caching.** The geometry is immutable for the process lifetime, so `/graph` is
cacheable:

- A `200` carries a strong `ETag` (`"sha256:<hex>"`, computed once over the body)
  and `Cache-Control: public, max-age=3600`.
- A conditional `GET` with a matching `If-None-Match` returns **`304 Not Modified`**
  with an empty body (the FeatureCollection is not re-sent). A `304` is a
  successful cache validation, counted as an `ok` outcome, not an error.

A non-`GET` is a `405`.

---

## `POST /benchmark` + `GET /benchmark/{id}` — async job flow

Running the six-router demand sweep (including a `systemoptimal` pass) can be slow,
so `/benchmark` is **asynchronous**: the POST returns a **job id immediately** and
never blocks the request on the sweep. Results are delivered by polling
`GET /benchmark/{id}`.

### `POST /benchmark`

**Request body** (JSON, all fields optional — an empty body runs the canonical
harness defaults). The fields are the §R6 parameter tuple `(algorithm, α, β,
capacity_scale, requestCount, seed)`:

| Field            | Default          | Description                                            |
| ---------------- | ---------------- | ------------------------------------------------------ |
| `algorithm`      | `"all"`          | Dispatch: `"all"` runs the six-router sweep; one of `naive`, `reactive`, `incremental`, `msa`, `systemoptimal`, `multipath` runs single-algorithm mode (see below). Matched **case-sensitively** (`"Naive"`/`"ALL"` are a `400`). Any other value is a `400`. |
| `alpha`          | `0.15`           | BPR α coefficient. **Drives the cost function in single-algorithm mode.** |
| `beta`           | `4`              | BPR β coefficient. **Drives the cost function in single-algorithm mode.** |
| `capacity_scale` | `1.0`            | §R3 capacity knob (must be `> 0`). **Drives the cost function in single-algorithm mode** (the level it pins to). |
| `request_count`  | `1000`           | Per-level OD request count R (must be `0 <= R <= 100000`). |
| `seed`           | `0`              | Fixed RNG seed for a reproducible run.                  |

Unknown fields are rejected (`400`), and so is any `algorithm` that is neither
`"all"` nor one of the six router names. The tuple is also the **cache key** (§R6):
a repeat POST with the same effective tuple returns the **same job** rather than
launching a duplicate run.

`algorithm` **dispatches** between two modes:

- **`"all"` (the default) — six-router demand sweep** (`benchmark.RunSweep`). Runs
  all six routers across the four v/c levels and **sweeps `capacity_scale` as its
  axis**, so it consumes `seed` and `request_count` and derives α/β/capacity-scale
  per level itself. In this mode the request's `alpha`/`beta`/`capacity_scale` do
  **not** change the grid (the sweep owns them); they remain part of the cache
  identity and are echoed in `params`. This path is unchanged.
- **A named router — single-algorithm mode** (`benchmark.RunSingle`). Builds **one**
  BPR from the request's `alpha`/`beta`/`capacity_scale` and routes that one router
  (plus the `systemoptimal` reference, so the reported Price of Anarchy is honest) at
  a **single level pinned to the client's `capacity_scale`**. Here
  `alpha`/`beta`/`capacity_scale` **actually drive the cost function** — two runs
  differing only in those params return **different** metrics. The report's `cells`
  hold the named router's cell (and the systemoptimal reference cell, except when the
  named router *is* systemoptimal); `poa_by_level`/`headline_improvement` degrade
  gracefully on this one-or-two-cell grid (the helpers return `1`/zero on degenerate
  input, never NaN). This is the surface the Phase-6 parameter sliders expose.

**Resource bounds.** The sweep is CPU-heavy and the job store is long-lived, so
the endpoint is bounded: `request_count` is capped at `100000`; at most a handful
of sweeps run concurrently — a POST that would exceed that is rejected with `503`
(retry shortly), not queued; and the job store retains a bounded number of jobs,
evicting the oldest completed one as needed.

**`202 Accepted` response**

```json
{
  "job_id": "9f86d081884c7d65...",
  "status": "running",
  "params": { "algorithm": "all", "alpha": 0.15, "beta": 4, "capacity_scale": 1.0, "request_count": 1000, "seed": 0 }
}
```

The job id is a random, unguessable token. A bad tuple (e.g. `capacity_scale <= 0`,
`request_count` over the cap, or an `algorithm` that is neither `"all"` nor one of
the six router names) is a `400`; a non-`POST` is a `405`; a POST that
would exceed the concurrent-sweep cap is a `503` (retry shortly). A tuple whose
previous run **failed** is re-runnable: a repeat POST starts a fresh job rather
than returning the stuck failure.

### `GET /benchmark/{id}`

Poll a job's status / result.

**`200` response while running**

```json
{ "job_id": "9f86...", "status": "running", "params": { ... } }
```

**`200` response when done** — the cached `benchmark.Report` (the #91/#89 metrics:
the full per-(router, level) sweep grid, per-level Price of Anarchy, and the
headline improvement):

```json
{
  "job_id": "9f86...",
  "status": "done",
  "params": { ... },
  "report": {
    "seed": 0,
    "od_count": 1000,
    "total_demand_vph": 5000,
    "cells": [ { "result": { "router": "naive", "...": "..." }, "poa": 1.0, "...": "..." } ],
    "poa_by_level": [ { "demand_level": "vc0.5", "target_vc": 0.5, "poa": 1.0 } ],
    "headline_improvement": { "demand_level": "vc0.8", "best_router": "systemoptimal", "percent_reduction": 0.0 }
  }
}
```

**`200` response when failed**

```json
{ "job_id": "9f86...", "status": "failed", "params": { ... }, "error": "benchmark failed" }
```

An unknown job id is a `404`; a missing/malformed id is a `400`; a non-`GET` is a
`405`.

> The sweep runs under `context.Background()` (not the request context), so a
> client disconnecting after the immediate job-id response does not cancel an
> in-flight run — the job completes and is cached for the next poll.

---

## `GET /stream` — WebSocket multi-algorithm parallel simulation

The headline live view's data source (§R6). On connect, `/stream` upgrades to a
WebSocket and runs **all six routers in parallel** over the §R5 mesoscopic simulator
from a chosen start time-of-day/date, then streams each algorithm's evolving
congestion — one **snapshot** per algorithm, then **bucketed deltas** at a fixed
server tick — alongside each algorithm's running **compute-time** and
**realized-traffic** metrics. The frontend keeps a `Map<segment_id, bucket>` per
algorithm and re-colors via deck.gl `updateTriggers` without rebuilding geometry.

The six algorithms are `naive`, `reactive`, `incremental`, `msa`, `systemoptimal`,
`multipath` (the canonical `RouterOrder`). All six run the **same** deterministic OD
set over the **same** immutable graph, so their streams are directly comparable.

### Query parameters

| Param       | Default                | Description                                                                 |
| ----------- | ---------------------- | --------------------------------------------------------------------------- |
| `start`     | `2026-06-22T08:00:00Z` | Simulation start time-of-day/date (the slider value), **RFC3339**.          |
| `speed`     | `60`                   | **Replay speed**: simulated seconds per wall-clock second (`(0, 100000]`).   |
| `tick_hz`   | `1`                    | **Fixed server tick**: wall-clock frames per second (`[0.5, 2]`).           |
| `seed`      | `0`                    | RNG seed — the run is byte-identical per seed (modulo wall clock).           |
| `count`     | `1000`                 | Per-run OD request count R (`[0, 20000]`).                                   |
| `cap_scale` | `1.0`                  | §R3 capacity knob; v/c scales inversely with it (`> 0`).                     |

`count` is capped lower than the batch `/benchmark` sweep's `100000`: each `/stream`
connect runs six full simulations and routes R requests six times, so the live
endpoint uses a tighter ceiling to bound the per-connection work an unauthenticated
client can demand.

A malformed/out-of-range parameter is a clean `400` **before** the upgrade (no socket
is opened); a non-`GET` is a `405`. Concurrent live runs are bounded: each connect
launches six parallel simulations, so a connect that would exceed the server's stream
capacity is refused with a `503` **before** the upgrade (retry shortly), mirroring the
`/benchmark` admission control.

### The time model (two clocks, two knobs that relate them)

Two clocks are deliberately **decoupled**, with two knobs mapping one onto the other:

1. **Simulated clock** — the simulator's internal Δt (≈30 s/tick): how congestion
   builds and drains in the model. `start` sets its origin; shifting `start` shifts
   every frame's `sim_time` by the same offset, observably, without changing the
   relative dynamics.
2. **Wall clock** — real time on the server.

- **Replay speed** (`speed`) — how many **simulated** seconds elapse per wall-clock
  second (a **ratio** between the two clocks, not a clock itself). `speed=60` plays an
  hour of simulation in a wall-clock minute. It **compresses simulated time**; it does
  **not** change the message rate.
- **Fixed server tick** (`tick_hz`) — the wall-clock cadence at which delta frames
  are emitted (1–2 Hz). It stays **fixed regardless of `speed`**: a higher speed
  advances the simulated clock further between frames (each delta carries a bigger
  jump, and intermediate sim ticks are coalesced into the latest one), but frames
  still arrive once per server tick.

The simulation runs to completion off the socket's hot path (the simulator's
per-tick observer only buffers in memory — it never blocks on the network), and a
separate replay loop paces the **output** by the server tick. A slow or stalled
client therefore can never stall — or deadlock — a simulation; it only slows its own
delivery (each frame write has its own short deadline). The stream ends with a clean
WebSocket close once every algorithm's run has drained.

### Bucketing scheme

v/c is quantized to **24 buckets of width 0.1**: bucket `b` covers v/c ∈
`[0.1·b, 0.1·(b+1))`. Bucket `0` is free-flow (v/c < 0.1); the **top bucket `23` is
saturating** — it absorbs everything at v/c ≥ 2.3, so an arbitrarily over-capacity
edge maps to a finite bucket. A **delta carries a segment only when its bucket
changed**, so sub-bucket jitter between ticks produces no frame and deltas stay
small. 24 buckets covers the v/c ∈ [0, 2.3+] band at fine-enough resolution that
color steps read smoothly.

### Frames

Every frame is a JSON text message. **Segments are keyed by `segment_id`** (the
frozen §1 contract), never by the internal `EdgeID`, so a frame joins to `/graph`
geometry exactly like `/congestion`. Segments/changes are sorted by `segment_id` for
a deterministic, diffable frame.

**`snapshot`** — one per algorithm on connect: the algorithm's **full** bucketed
state at its first tick. The client seeds its `Map<segment_id, bucket>` from it.

```json
{
  "type": "snapshot",
  "algo": "reactive",
  "tick": 1,
  "sim_time": "2026-06-22T08:00:30Z",
  "segments": [
    { "segment_id": "905512:0:F", "vc": 0.42, "bucket": 4 }
  ],
  "metrics": { "compute_ms": 0.18, "realized_total_s": 1234.5, "poa": 1.0, "in_flight": 120, "completed": 0 }
}
```

**`delta`** — subsequent frames: **only** the segments whose v/c **bucket** changed
since this algorithm's last frame. Applying `snapshot` + all `delta`s in order
reproduces the full bucketed state at every tick (the delta-correctness invariant the
tests assert).

```json
{
  "type": "delta",
  "algo": "reactive",
  "tick": 7,
  "sim_time": "2026-06-22T08:03:30Z",
  "changed": [
    { "segment_id": "905512:0:F", "vc": 0.91, "bucket": 9 }
  ],
  "metrics": { "compute_ms": 1.04, "realized_total_s": 1402.7, "poa": 1.13, "in_flight": 240, "completed": 30 }
}
```

### Per-algorithm metric fields (`metrics`)

Carried on **every** frame, updated per tick, matching the #89/#90 evaluators:

| Field              | Meaning                                                                                          |
| ------------------ | ------------------------------------------------------------------------------------------------ |
| `compute_ms`       | **Cumulative** wall-clock ms spent in `router.Route` so far — answers *"fastest to route"*.       |
| `realized_total_s` | Realized **total network time** (s) at this tick — `routing.TotalNetworkTime` over the tick load. |
| `poa`              | Realized-time **Price of Anarchy** vs. `systemoptimal` **at the same tick** — *"minimizes traffic"*. |
| `in_flight`        | Vehicles on the network at this tick.                                                             |
| `completed`        | Cumulative vehicles that have finished their trip.                                                |

`poa` is computed by pairing this algorithm's `realized_total_s` with
`systemoptimal`'s realized total **at the same tick** (so `systemoptimal`'s own `poa`
is `1`). Pairing same-tick totals — rather than reading whichever total a concurrent
goroutine happened to publish first — is what keeps the stream deterministic: at a
fixed `seed` the whole trace (ticks, `sim_time`, per-segment buckets, and metrics) is
byte-identical run to run, modulo wall-clock `compute_ms`.
