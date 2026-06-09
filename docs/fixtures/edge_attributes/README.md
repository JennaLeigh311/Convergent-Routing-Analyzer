# `edge_attributes` golden fixtures

Language-neutral test vectors for the `edge_attributes` export contract — the
immutable directed-edge road-network snapshot the engine runs on. These files
are the **single source of truth** for the column schema and the capacity /
free-flow derivation rules: any exporter that produces `edge_attributes` (and
any consumer that reads it) must reproduce these exact values, so the data side
and the service side cannot drift apart. The human specification is in
[`../../contracts.md` §2](../../contracts.md); this fixture makes it executable.

Every `segment_id` here conforms to the canonical §1 scheme
(`"{osm_way_id}:{seq}:{dir}"`); see [`../segment_id/`](../segment_id/) for the
`segment_id` rules and their own fixtures. This section never redefines them.

## Files

### `example_export.json`

An array of directed-edge rows, each as an object with all 12 contract columns:

```json
{
  "segment_id": "27583001:0:F",
  "edge_id": 0,
  "source_node": 10,
  "target_node": 11,
  "osm_way_id": 27583001,
  "highway_class": "primary",
  "lanes_effective": 2,
  "length_m": 240.0,
  "maxspeed_kmh": 60.0,
  "freeflow_time_s": 14.4,
  "capacity_vph": 2880.0,
  "geometry": { "type": "LineString", "coordinates": [[-73.99012, 40.73456], [-73.98801, 40.73502]] },
  "note": "…"
}
```

- `segment_id` — the §1 canonical wire key. Valid under §1, and the `osm_way_id`
  embedded in it **equals** this row's `osm_way_id` field (self-consistency).
- `edge_id` — the engine's dense int32 edge index for this snapshot (`>= 0`).
- `source_node` / `target_node` — int32 §1 NodeIDs of the tail/head vertices.
- `osm_way_id` — int64 positive OSM way id this edge came from.
- `highway_class` — OSM `highway` enum (`motorway`…`service`); drives the
  default and `class_factor` tables in §2.
- `lanes_effective` — lanes in this direction (`>= 1`).
- `length_m` — geodesic edge length in meters.
- `maxspeed_kmh` — free-flow speed limit, km/h.
- `freeflow_time_s` — `length_m / (maxspeed_kmh × 1000 / 3600)`, seconds.
- `capacity_vph` — `lanes_effective × 1800 × class_factor × capacity_scale`,
  vehicles/hour. These rows are generated at `capacity_scale = 1.0`, so the
  stored value equals `lanes_effective × 1800 × class_factor`.
- `geometry` — GeoJSON `LineString`, coordinates in **`[lon, lat]`** order, drawn
  in the edge's travel direction (source → target). A LineString may have more
  than two coordinates; interior coordinates are **shape points only**, not graph
  nodes — only the first/last coordinate map to `source_node`/`target_node`.
- `note` — human description of what the row exercises (ignore in tests).

`capacity_vph` and `freeflow_time_s` are **computed from the §2 rules** for each
row's class / lanes / maxspeed / length — they are not free-form. A conformant
exporter must reproduce these values exactly, so the fixture is self-checking.

Coverage: a one-way street (single row), both halves of a two-way (`…:F` and
`…:R` sharing way+seq, with source/target and geometry reversed), a
multi-segment way split at an intersection (two `seq` values on the same way),
a spread of highway classes (`primary`, `secondary`, `motorway`, `residential`,
`trunk`, `service`) that exercises every `class_factor` value, a three-vertex
LineString (the `trunk` row, whose middle coordinate is a shape point, not a
node), and a row whose OSM-tagged `lanes`/`maxspeed` deliberately differ from the
class defaults (the second `primary` row) so a defaulting-vs-tagged exporter bug
is caught.

These are the **logical row** vectors. The envelope-level `schema_version` (§2
"Envelope `schema_version`") is a property of the serialized Parquet/GeoJSON
artifacts, not of an individual row, so it is not represented here.

## Consumers

- **Go engine graph loader** (future, `engine/internal/graph`) — will load this
  to validate that the materialized `edge_id`/`segment_id`/length/freeflow/
  capacity round-trip into `graph.Edge` as documented in §2.
- **Frontend `/graph` endpoint** (future) — will validate the GeoJSON
  serialization (geometry → `Feature.geometry`, all other columns →
  `Feature.properties`) against these rows.

## Changing these fixtures

This fixture is part of a **frozen contract**. Do not edit it to make a failing
exporter pass. A genuine schema or derivation-rule change requires bumping the
contract version in [`../../contracts.md` §2](../../contracts.md) and updating
every consumer in lockstep.
