# `segment-congestion` golden fixtures

Language-neutral test vectors for the `segment-congestion` schema v2 message
contract — the live, event-time-windowed congestion feed flowing from the Spark
pipeline (producer) to the Go engine (consumer). These files are the **single
source of truth** for the message schema and the late-data / dedup bookkeeping:
any producer that emits `segment-congestion` messages (and any consumer that
reads them) must reproduce these exact shapes and honor the dedup ordering they
encode, so the data side and the service side cannot drift apart. The human
specification is in [`../../contracts.md` §3](../../contracts.md); this fixture
makes it executable.

Every `segment_id` here conforms to the canonical §1 scheme
(`"{osm_way_id}:{seq}:{dir}"`); see [`../segment_id/`](../segment_id/) for the
`segment_id` rules and their own fixtures, and [`../../contracts.md` §1](../../contracts.md)
for the strict-parsing rules. This section never redefines them. Several
`segment_id`s are reused from the [`../edge_attributes/`](../edge_attributes/)
fixture (`27583001:0:F`, `48800123:0:F`/`:R`) so the engine can join congestion
onto known edges.

## Files

### `example_messages.json`

An array of messages, each an object with all 10 contract fields:

```json
{
  "schema_version": 1,
  "segment_id": "123456789:2:F",
  "window_start": "2026-06-08T08:00:00Z",
  "window_end": "2026-06-08T08:05:00Z",
  "vehicle_count": 42,
  "avg_speed_kmh": 18.3,
  "sample_pings": 137,
  "is_final": false,
  "emit_time": "2026-06-08T08:05:03Z",
  "producer": "spark-structured-stream",
  "note": "…"
}
```

- `schema_version` — envelope wire version, currently `1` (the first frozen v2
  envelope; "v2" is the schema *generation*, `1` is the in-wire version — see
  §3 "Why 'v2'").
- `segment_id` — the §1 canonical wire key and the Kafka **message key** on this
  topic. Valid under §1.
- `window_start` — RFC3339 UTC, inclusive **event-time** lower bound of the
  window.
- `window_end` — RFC3339 UTC, **exclusive** event-time upper bound. The window
  is half-open `[window_start, window_end)` and is **exactly 5 minutes** long on
  every row.
- `vehicle_count` — distinct vehicles in this window. A **per-5-minute count**,
  not hourly; the engine adapter scales it to vph as `× 12` before BPR (§3 /
  §2 unit contract).
- `avg_speed_kmh` — mean matched speed over the window, km/h (`> 0`).
- `sample_pings` — raw GPS pings behind the aggregate; `>= vehicle_count` on
  every row (diagnostic/confidence).
- `is_final` — `false` = provisional (overwritable); `true` = window closed past
  the watermark, will not be updated.
- `emit_time` — RFC3339 UTC producer wall-clock when **this** record was
  emitted; the dedup tie-breaker within a window.
- `producer` — emitting job id; the same value for live and batch run modes.
- `note` — human description of what the row exercises (ignore in tests).

These are the on-wire **message** vectors. The dedup rule is not a field — it is
the consumer behavior the rows are built to exercise: the engine keeps, per
`segment_id`, the record with the latest `(window_start, emit_time)`.

Coverage: the fixture exercises one segment (`27583001:0:F`, a known
`edge_attributes` edge) across the full dedup story — (1) a normal **provisional**
record for window `[08:00,08:05)`; (2) a **later revision of the same
window+segment** with a newer `emit_time` and higher `vehicle_count`, proving
*latest `emit_time` wins within a window*; (3) the **final** record for that same
window (`is_final: true`, newest `emit_time`), the value a consumer must keep; and
(4) a **later window** `[08:05,08:10)` for that segment with a greater
`window_start`, proving *newest window wins* in the `(window_start, emit_time)`
ordering (it supersedes the row-3 final even though it is only provisional). It
then exercises **directionality and per-segment independence**: (5) the **reverse**
(`48800123:0:R`) and (6) the **forward** (`48800123:0:F`) halves of one two-way
street carry independent records for the same window with different
`vehicle_count`s — mirroring §1's directional rule — and (7) a **multi-seq**
`segment_id` (`123456789:2:F`, the verbatim §R4 spec example) shows a non-zero
`seq` flowing on the topic. Every window is exactly 5 minutes, every timestamp is
RFC3339 UTC `Z`, every `emit_time` is consistent with the dedup story (provisional
emits a few seconds after `window_end`; finals after the 2-min watermark), and
`sample_pings >= vehicle_count` throughout.

## Consumers

- **Go engine congestion adapter + static replayer** (future,
  `engine/internal/...`) — will load these to validate that messages parse into
  the in-memory congestion snapshot and that the `(window_start, emit_time)`
  keep-latest dedup rule is applied correctly (rows 1→2→3 collapse to row 3 for
  the window; row 4 then supersedes by newer window; rows 5/6/7 are kept
  independently per `segment_id`).
- **PySpark producer conformance** (future) — will validate that the Spark
  Structured Streaming job serializes the identical 10-field shape (and only
  these fields — `dropped_late` is a producer metric, not a message field).
- **Frontend live-congestion overlay** (later) — colors roads by joining live
  congestion to `segment_id`; these vectors stand in for that feed in tests.

## Changing these fixtures

This fixture is part of a **frozen contract**. Do not edit it to make a failing
producer pass. A genuine schema or dedup-rule change requires bumping the
contract version in [`../../contracts.md` §3](../../contracts.md) (and therefore
the on-wire `schema_version`) and updating every consumer in lockstep.
