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

## Liveness / readiness / observability

| Method | Path       | Description                                                      |
| ------ | ---------- | ---------------------------------------------------------------- |
| GET    | `/healthz` | Liveness — `200 ok` once the process is up.                      |
| GET    | `/readyz`  | Readiness — `200 ok` once the graph has loaded.                  |
| GET    | `/metrics` | Prometheus scrape: Go runtime/process series + routing counters. |

The routing counter `routing_requests_total{endpoint,outcome}` is incremented for
every handled routing request, partitioned by the logical endpoint (`route`,
`compare`, `congestion`, `graph`, `benchmark`, `benchmark_status`) and a coarse
`outcome` (`ok` | `error`). It is registered against the same registry `/metrics`
scrapes.

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
| `algorithm`      | `"all"`          | Router under test. **Accepted but inert today** (see below). |
| `alpha`          | `0.15`           | BPR α coefficient. **Accepted but inert today.**       |
| `beta`           | `4`              | BPR β coefficient. **Accepted but inert today.**       |
| `capacity_scale` | `1.0`            | §R3 capacity knob (must be `> 0`). **Accepted but inert today.** |
| `request_count`  | `1000`           | Per-level OD request count R (must be `0 <= R <= 100000`). |
| `seed`           | `0`              | Fixed RNG seed for a reproducible run.                  |

Unknown fields are rejected (`400`). The tuple is the **cache key** (§R6): the
six-router demand sweep (`benchmark.RunSweep`) consumes **only** `seed` and
`request_count` and sweeps the capacity axis itself. The remaining fields
(`algorithm`, `alpha`, `beta`, `capacity_scale`) participate in the cache identity
and are echoed back in `params`, but **do not change the sweep's result today** —
two POSTs differing only in `alpha` run identical sweeps under distinct cache
entries. They are reserved for a future single-algorithm benchmark mode. A repeat
POST with the same effective tuple returns the **same job** rather than launching a
duplicate run.

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
or `request_count` over the cap) is a `400`; a non-`POST` is a `405`; a POST that
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

## WebSocket congestion stream

The live congestion stream (snapshot + delta protocol, §R6) is **not** part of this
surface. It lands in a later issue (#93) once the `WriteTimeout` caveat on the
server (long-lived connections do not want a write deadline) is addressed.
