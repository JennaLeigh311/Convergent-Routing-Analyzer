# Frozen Cross-Team Contracts

> The three contracts the board froze at kickoff (`project-spec.md §R0`). This file is the single source
> of truth shared by the database, data-pipeline, and routing-engine teams. **Do not change a frozen
> contract without bumping its version and notifying all consumers.**

## 1. Canonical `segment_id`

**Contract owner:** Database / Geospatial Engineer.
**Contract version: 1.**
**Consumers / depended-on-by:** the `edge_attributes` export (#4, §2) and the `segment-congestion` message
(#5, §3), and later the frontend.
**Conformance status:** proven against Go (engine); PySpark (pipeline, #5) pending.

`segment_id` is the single cross-system **wire key** for a directed piece of road. It is the only road
identifier shared by the database export, the Spark map-matcher/pipeline, and the Go engine. If these three
disagree on what a segment is called, congestion silently attaches to the wrong road (or to nothing) and every
downstream number is garbage — so this is the most important frozen contract in the project.

### Scheme

```
segment_id = "{osm_way_id}:{seq}:{dir}"
```

Three colon-separated fields, exactly two colons. Example: `"123456789:0:F"`.

| Field        | Type            | Definition |
|--------------|-----------------|------------|
| `osm_way_id` | int64, positive | The OpenStreetMap **way** id, taken from `ways.osm_id`. Stable across re-imports of the same OSM extract. |
| `seq`        | int, >= 0       | 0-based ordinal of this sub-segment within the way, after pgRouting splits the way at intersections. A way that crosses two intersections yields sub-segments `seq = 0, 1, 2`. |
| `dir`        | `F` or `R`      | Travel direction. `F` (forward) runs **along** the way's node ordering; `R` (reverse) runs against it. Case-sensitive, exactly one letter. |

### Directed rule

`segment_id` is **directed** — it names a direction of travel, not just a stretch of road:

- A **two-way** street is **two** segment_ids that share `osm_way_id` and `seq` and differ only in `dir`:
  `…:F` and `…:R`.
- A **one-way** street is **one** segment_id: only its permitted direction exists.
- The map-matcher resolves which direction a GPS trace is travelling from its heading and emits the matching
  `dir`.

### `gid`/`id` is BANNED as a wire key — and why

The wire key is built from `osm_way_id` (`ways.osm_id`), **never** from the `osm2pgrouting`
auto-increment `gid`/`id` columns. Those auto-increment surrogate keys are forbidden in any `segment_id`
because:

1. **They are non-deterministic across re-imports.** Re-running the import (new OSM extract, different load
   order, a schema rebuild) renumbers `gid`/`id`. The same physical road would get a different id, so
   congestion computed against an old export would land on the wrong road after any reload.
2. **They are unknown outside PostGIS.** The Spark map-matcher matches GPS pings to OSM geometry by
   `osm_id`; it never sees the database's internal row numbers. Only `osm_id` is computable by all three
   systems independently.

`osm_id` is assigned by OpenStreetMap and is stable for a given extract, so all three systems can derive the
identical `segment_id` without coordinating through the database's internal counters.

### `segment_id` ↔ `EdgeID`

There are two identifiers for an edge, with distinct jobs:

- **`segment_id`** (this contract) — the stable, directed, cross-system **wire key**. It travels between
  systems: in the `edge_attributes` export, on the `segment-congestion` Kafka topic, and in the API. It is a
  string and is never used on hot in-memory paths.
- **`EdgeID`** (`engine/internal/domain`) — a compact `int32` the engine assigns at load time, dense and
  contiguous `0..EdgeCount-1`, used to directly index flat per-edge slices.

At graph build time the engine constructs a **lookup** in both directions
(`segment_id → EdgeID` and `EdgeID → segment_id`). Inbound congestion keyed by `segment_id` is translated to
an `EdgeID` once, at ingest; everything on the hot path uses `EdgeID`. `EdgeID` values are an in-memory
detail of one process and MUST NOT appear on the wire.

### Reference implementation & golden fixtures

- **Go:** `FormatSegmentID` / `ParseSegmentID` in `engine/internal/domain/segmentid.go`. Parsing is strict
  (see below).
- **Fixtures (language-neutral, shared):** `docs/fixtures/segment_id/` —
  `format_cases.json` (valid rows that must round-trip) and `parse_invalid.json` (malformed strings that
  must be rejected). Both the Go engine and the (future) PySpark pipeline load these **same** files so the
  two implementations cannot drift. See that directory's `README.md`.

A conformant **parser is strict** and rejects anything that is not exactly the scheme: it requires exactly
two colons (three fields); `osm_way_id` must be a canonical positive base-10 integer (`>= 1`) and `seq` a
canonical non-negative base-10 integer (`>= 0`), both with no sign, no leading zeros, no whitespace, and no
hex; and `dir` must be exactly `F` or `R`. It returns a descriptive error and never panics.

### Worked examples

1. **One-way way.** OSM way `27583001` is a one-way street that pgRouting did not split (single sub-segment).
   It produces exactly one segment_id:
   - `"27583001:0:F"`
2. **Two-way way → two ids.** OSM way `48800123` is a two-way street, one sub-segment. It produces a pair
   sharing way+seq, differing only in `dir`:
   - `"48800123:0:F"` (with the node ordering)
   - `"48800123:0:R"` (against the node ordering)
3. **Multi-segment way split at an intersection → multiple `seq`.** OSM way `905512` crosses two
   intersections, so pgRouting splits it into three sub-segments `seq = 0, 1, 2`. Forward direction:
   - `"905512:0:F"`, `"905512:1:F"`, `"905512:2:F"`

   And because it is two-way, each sub-segment also has its reverse, e.g. `"905512:2:R"`.

### Versioning

This is **Contract version: 1**. The scheme above is frozen. **Changing it — the field set, their order,
the delimiter, the `dir` tokens, or the strict-parsing rules — requires bumping the contract version here
and notifying every consumer (database export, pipeline, engine, frontend) so they update in lockstep.** The
shared fixtures in `docs/fixtures/segment_id/` are part of this contract and must be updated in the same
change; do not edit a fixture to paper over a non-conformant implementation.

Because `segment_id` carries **no in-wire version token** of its own, the `edge_attributes` (§2) and
`segment-congestion` (§3) **envelope schema versions are its operational version-carriers**: a §1 format
change REQUIRES bumping those envelope schema versions too, so consumers reading those envelopes can detect
the change.

## 2. `edge_attributes` export schema — *to be filled by issue #4*

One row per directed edge; carries the BPR capacity/free-flow fields derived from OSM tags.

The `segment_id` field conforms to §1 and MUST NOT be redefined locally.

## 3. `segment-congestion` schema v2 — *to be filled by issue #5*

Event-time windowed congestion records on a compacted Kafka topic keyed by `segment_id`.

The `segment_id` field conforms to §1 and MUST NOT be redefined locally.
