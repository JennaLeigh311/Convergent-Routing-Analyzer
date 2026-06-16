# Data Pipeline

The path from raw GPS pings to the per-edge load the routing engine runs BPR on. The streaming machinery
(Kafka topics, the Spark Structured Streaming map-match + windowed aggregation job, watermark semantics) is
**planned** for Phase 7 and is not built yet; this document covers it as a forward-looking design. The
Phase-2 concept that **is** live and load-bearing today — the vehicles/hour unit contract and the `× 12`
annualization, and **where it is enforced in code** — is documented below and cross-references the canonical
contract. Expanded from `project-spec.md §R4` (streaming) and `§R3` (units). The wire schema is frozen in
`docs/contracts.md §3`.

## 1. The vehicles/hour unit contract (live in Phase 2)

BPR routes on a flow `v` and a capacity `c` that **must both be in vehicles/hour** (`project-spec.md §R3`).
The canonical statement of this contract lives in `docs/contracts.md` — `§2` ("Unit contract (BPR flow
units)") and the `§3` `vehicle_count` field. **This document does not fork or restate that rule**; it states
its essence and points to where it is enforced.

The essence:

- `segment-congestion`'s `vehicle_count` is a **per-5-minute-window count, NOT an hourly rate** (`docs/contracts.md §3`).
- The engine's congestion adapter **annualizes** it to vehicles/hour before BPR:

  ```
  v_vph = vehicle_count × 12          (twelve 5-minute windows per hour)
  ```

- `capacity_vph` (from the `edge_attributes` export, `docs/contracts.md §2`) is **already** vehicles/hour, so
  once the adapter applies `× 12`, `v` and `c` share a unit and `v/c` is **dimensionless**.

**Where it is enforced in code.** The `× 12` scaling lives in `engine/internal/congestion/static/static.go`:
`annualizationFactor = 12`, applied in `NewProvider` (each surviving per-segment count is multiplied by the
factor before being written onto the edge's load). This is the **static / replay adapter** — the offline
counterpart to the future Kafka consumer: same wire schema and same keep-latest dedup, but fed from a file or
in-memory slice rather than a live topic. When the Kafka consumer ships (Phase 7) it shares the same message
type, decoder, and dedup (in `engine/internal/domain`) and applies the identical `× 12`.

**Why it matters enough to be a written contract.** A unit mismatch does not crash — and with `β = 4`, BPR
raises any mismatch to the **fourth power**, silently corrupting the headline Price-of-Anarchy number
(`project-spec.md §R3`). A producer MUST emit the raw per-window count and MUST NOT pre-multiply by 12; the
`× 12` is the consumer (engine adapter) side's responsibility (`docs/contracts.md §3`).

## 2. The streaming machinery (planned, Phase 7+)

The live ingestion path is **not yet built**; the engine is developed against the static replay adapter (§1)
and the in-memory provider first, satisfying the decoupling requirement and de-risking the distributed pieces
until the routing core is proven (`project-spec.md §6`, `§R8`). The frozen design (`project-spec.md §R4`):

### 2.1 Kafka topics

Two topics frame the pipeline (pinned in `docs/contracts.md §3`, "Kafka topics"):

- **`gps-pings`** (input, raw pings) — keyed by **`taxi_id`** (preserves per-trajectory ping ordering within a
  partition; keying by anything else shatters a vehicle's trajectory across partitions and breaks
  map-matching). 12–24 partitions. Retention `≥` a full replay window so the batch run can re-read the entire
  ping file.
- **`segment-congestion`** (output) — keyed by **`segment_id`**, `cleanup.policy=compact` (latest-per-segment
  survives). Compaction *approximates* the dedup rule but is not identical to it; the
  `(window_start, emit_time)` dedup rule is authoritative and every consumer applies it in-memory. See
  `docs/contracts.md §3` for the full keying/compaction reasoning.

### 2.2 Spark Structured Streaming job

The producer is a **Spark Structured Streaming** job (not legacy DStreams — Structured Streaming is required for
first-class event-time windowing and watermarks):

- **Windowing** — a sliding window: **5-minute window length, 1-minute slide**, over event time. (This is why
  `vehicle_count` is a 5-minute count, and the `§1` `× 12` annualization is exactly the conversion to an hourly
  rate.)
- **Watermark** — an event-time watermark of **2 minutes**; a ping past the watermark is dropped and counted in
  a producer-side `dropped_late` operational metric (not a message field).
- **Dual trigger, one job** — `Trigger.ProcessingTime` drives the live heatmap; `Trigger.AvailableNow` drives
  the batch pass over the full ~15M-ping file for the "Spark batch processing" headline number. **Same code,
  two run modes** — so both emit the identical schema and the same `producer` value.

The full windowing, watermark, and dedup semantics are frozen in `docs/contracts.md §3` ("Windowing & streaming
semantics", "Dedup rule"); this section does not redefine them.

### 2.3 Map-matching

Snapping raw GPS pings to OSM edges (`project-spec.md §R4`, ships Phase 7): greedy nearest-edge suffices for
sparse Porto, but **ST-Matching (HMM) is required before any T-Drive/Beijing number** — greedy mis-snaps on
dense parallel roads and manufactures phantom congestion.

## 3. The wire schema

The `segment-congestion` message schema (the 10-field v2 envelope: `schema_version`, `segment_id`,
`window_start`, `window_end`, `vehicle_count`, `avg_speed_kmh`, `sample_pings`, `is_final`, `emit_time`,
`producer`) is **frozen in `docs/contracts.md §3`** and is not restated here. The `edge_attributes` export that
supplies `capacity_vph` is frozen in `docs/contracts.md §2`, and the canonical `segment_id` wire key in `§1`.
This document points to those contracts as the single source of truth; do not duplicate or fork them.
