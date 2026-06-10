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
- `geometry` — `LineString`, coordinates in **`[lon, lat]`** order (CRS
  EPSG:4326), drawn in the edge's travel direction (source → target). A
  LineString may have more than two coordinates; interior coordinates are
  **shape points only**, not graph nodes — only the first/last coordinate map to
  `source_node`/`target_node`. This fixture expresses geometry as GeoJSON
  `coordinates` for readability; per §2 the **Parquet** serialization carries the
  same geometry as WKB (same `[lon, lat]` / EPSG:4326), so both serializations
  decode to these identical coordinates.
- `note` — human description of what the row exercises (ignore in tests).

`capacity_vph` and `freeflow_time_s` are **computed from the §2 rules** for each
row's class / lanes / maxspeed / length — they are not free-form. A conformant
exporter must reproduce these values exactly, so the fixture is self-checking.

Coverage: a one-way street (single row), both halves of a two-way (`…:F` and
`…:R` sharing way+seq, with source/target and geometry reversed), a
multi-segment way split at an intersection (two `seq` values on the same way),
**all seven** highway classes (`motorway`, `trunk`, `primary`, `secondary`,
`tertiary`, `residential`, `service`) so every `class_factor` value is
exercised, a three-vertex LineString (the `trunk` row, whose middle coordinate
is a shape point, not a node), a row whose OSM-tagged `lanes`/`maxspeed`
deliberately differ from the class defaults (the second `primary` row) so a
defaulting-vs-tagged exporter bug is caught, and a two-way pair tagged with a
**bare both-directions `lanes` total** (way `8123456`, `lanes=6 → 3` per
direction) so the lane-split rule is exercised — a row catching an exporter that
applies a bare `lanes` whole or falls back to the class default.

These are the **logical row** vectors. The envelope-level `schema_version` (§2
"Envelope `schema_version`") is a property of the serialized Parquet/GeoJSON
artifacts, not of an individual row, so it is not represented here — it is
carried by `example_export.geojson` below.

### `example_export.geojson`

The golden **GeoJSON `FeatureCollection`** serialization (§2 "Serializations" /
"Envelope `schema_version`") of the very same logical rows. It is the frontend
`/graph` endpoint's golden artifact, and it is **row-equivalent** to
`example_export.json` — same set of contract columns + geometry per `segment_id`,
in the same order (`edge_id` 0..11) — a property the Go test
`TestEdgeAttributesGeoJSONConformance`
(`engine/internal/domain/edge_attributes_geojson_test.go`) asserts
mechanically against both files.

Envelope:

```json
{ "type": "FeatureCollection", "schema_version": 1, "features": [ … ] }
```

- `type` — `"FeatureCollection"`.
- `schema_version` — the JSON **integer** `1` (this section's contract version),
  a top-level member of the collection (not a per-row column). It MUST be the
  integer `1`, **not** the string `"1"`: the string `"1"` is the **Parquet
  footer's** key/value-metadata form (§2), and emitting it in the GeoJSON is a
  bug the Go test catches by comparing the raw `schema_version` bytes.

Each row maps to one `Feature`, in source order:

- `Feature.geometry` — the row's `geometry` object verbatim (`LineString`,
  `[lon, lat]` order, EPSG:4326, unchanged coordinates).
- `Feature.properties` — exactly the **11 non-geometry contract columns** under
  the same keys (`segment_id`, `edge_id`, `source_node`, `target_node`,
  `osm_way_id`, `highway_class`, `lanes_effective`, `length_m`, `maxspeed_kmh`,
  `freeflow_time_s`, `capacity_vph`) — and nothing else, so the frontend's
  `properties.segment_id` join is a pure §1 join.
- `note` — the row's documentation string, preserved as a **Feature-level
  foreign member** (a sibling of `geometry`/`properties`, which GeoJSON permits)
  so the per-row documentation parity with `example_export.json` is kept
  **without** polluting the contract `properties`.

The downstream-relied-on rows are present and cross-checked by the Go test: the
3-vertex `LineString` row `33112200:0:F` (interior coordinate is a shape point,
not a node); both reversed F/R pairs `48800123:0:{F,R}` and `8123456:0:{F,R}`;
and the segment_ids overlapping the `segment_congestion` fixture
(`27583001:0:F`, `48800123:0:F`, `48800123:0:R`).

### `malformed_exports.json`

A **reject corpus** of complete-but-invalid exports, for the future #25 GeoJSON
loader's validate-and-reject path. It mirrors the `{value, reason}` precedent of
[`../segment_id/parse_invalid.json`](../segment_id/parse_invalid.json), scaled up
to whole exports — a single JSON array of:

```json
{ "violates": "<one-line: the single invariant this sample breaks>",
  "feature_collection": { …a complete-but-invalid FeatureCollection… } }
```

Each `feature_collection` is a minimal (1–3 features, derived from real golden
rows) export with **exactly one** invariant broken, so the #25 loader can assert
it is rejected for that specific reason; each `violates` string names exactly one
invariant (no stacked breakages). The Go test `TestMalformedExportsCorpus` is a
light guard only — it asserts the corpus loads, is non-empty, every entry is
annotated and carries a `FeatureCollection`, and that each mandated invariant
category is represented; it does **not** perform the rejection (that is #25's).

Invariants covered:

1. **Wrong `schema_version`** — top-level `"schema_version": 2`.
2. **Absent `schema_version`** — the top-level member omitted entirely.
3. **Stringified `schema_version`** — `"schema_version": "1"` (the Parquet-footer
   string form, wrong for GeoJSON, which requires the integer `1`).
4. **`segment_id` ↔ `osm_way_id` mismatch** — `segment_id` `"48800123:0:F"` but
   `osm_way_id` `27583001` (§2 self-consistency / §1).
5. **Non-dense `edge_id`** — two variants, annotated separately: a **gap**
   (`edge_id`s `0, 1, 3`) and a **duplicate** (two features both `edge_id: 0`).
6. **Out-of-enum `highway_class`** — `"living_street"`, outside the seven legal
   §2 values.
7. **Interior coord treated as a node** — the 3-vertex trunk `33112200:0:F` split
   into two features whose shared endpoint is its interior shape point
   `[-73.93720, 40.75640]`, i.e. an interior vertex promoted to a routable node
   (§2: only first/last coordinates map to `source_node`/`target_node`).
8. **Swapped `[lat, lon]` axis** — geometry emitted as `[lat, lon]`
   (`[40.73456, -73.99012]`) instead of the contract's `[lon, lat]`.

## Consumers

- **Go engine graph loader** (future, `engine/internal/graph`) — will load this
  to validate that the materialized `edge_id`/`segment_id`/length/freeflow/
  capacity round-trip into `graph.Edge` as documented in §2.
- **Frontend `/graph` endpoint** (future) — will validate the GeoJSON
  serialization (geometry → `Feature.geometry`, all other columns →
  `Feature.properties`) against these rows.

## Changing these fixtures

These fixtures — `example_export.json`, `example_export.geojson`, **and**
`malformed_exports.json` — are part of a **frozen contract**. Do not edit them to
make a failing exporter or loader pass. A genuine schema or derivation-rule change
requires bumping the contract version in
[`../../contracts.md` §2](../../contracts.md) (and therefore the GeoJSON
top-level `schema_version`) and updating every consumer in lockstep.
