# `segment_id` golden fixtures

Language-neutral test vectors for the canonical `segment_id` wire contract
(`"{osm_way_id}:{seq}:{dir}"`). These files are the **single source of truth**
for the format/parse rules: every system that produces or consumes a
`segment_id` must test against them so the Go engine and the PySpark pipeline
cannot drift apart. The human specification is in [`../../contracts.md` §1](../../contracts.md);
these fixtures make it executable.

## Files

### `format_cases.json`

An array of valid `segment_id`s, each as an object:

```json
{ "osm_way_id": 123456789, "seq": 0, "dir": "F", "segment_id": "123456789:0:F", "note": "…" }
```

- `osm_way_id` — OpenStreetMap way id (int64, positive).
- `seq` — 0-based ordinal of the sub-segment within the way (int, non-negative).
- `dir` — `"F"` (forward) or `"R"` (reverse).
- `segment_id` — the canonical string the other three fields must produce.
- `note` — human description of what the row exercises (ignore in tests).

Both round-trip directions must hold for every row:

- **format**: `format(osm_way_id, seq, dir) == segment_id`
- **parse**: `parse(segment_id) == (osm_way_id, seq, dir)`

Coverage: the canonical example, a one-way way, both halves of a two-way
(`…:F` and `…:R` sharing way+seq), a multi-segment way split at an intersection
(multiple `seq` values, including a reverse), a max-`int64` way id, a way id
above `int32` range, and a large `seq`.

### `parse_invalid.json`

An array of malformed strings that **must be rejected** by any conformant
parser, each as an object:

```json
{ "segment_id": "123456789:0:X", "reason": "invalid dir letter: must be F or R" }
```

- `segment_id` — the malformed input.
- `reason` — why it is invalid (documentation; tests only assert that parsing
  fails, not on the message text).

Coverage: empty string, wrong field count (too few / too many colons, trailing
colon, empty fields), non-integer way/seq, negative values, explicit `+` sign,
`osm_way_id` of 0 (must be positive, `>= 1`), non-canonical leading zeros on
`osm_way_id`/`seq`, bad/`lowercase`/multi-char `dir`, surrounding whitespace,
hex, and run-together fields.

## Consumers

- **Go engine** — `engine/internal/domain/segmentid_test.go` loads both files via
  the relative path `../../../docs/fixtures/segment_id/` and exercises
  `FormatSegmentID` / `ParseSegmentID`.
- **PySpark pipeline** (future) — loads the same two files to validate its
  `segment_id` builder/parser against the identical vectors.

## Changing these fixtures

These fixtures are part of a **frozen contract**. Do not edit them to make a
failing implementation pass. A genuine format change requires bumping the
contract version in [`../../contracts.md` §1](../../contracts.md) and updating
every consumer in lockstep.
