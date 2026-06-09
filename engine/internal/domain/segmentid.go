package domain

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// This file is the Go reference implementation of the canonical segment_id wire
// contract. The authoritative human specification lives in docs/contracts.md §1
// and the language-neutral golden fixtures (shared with the PySpark pipeline)
// live in docs/fixtures/segment_id/. Any change to the format or parsing rules
// here MUST be made in lockstep with that spec, those fixtures, and a contract
// version bump — see docs/contracts.md §1 "Versioning".

// FormatSegmentID builds the canonical, directed wire key for a road segment as
// specified in docs/contracts.md §1: "{osm_way_id}:{seq}:{dir}", e.g.
// "123456789:0:F".
//
//   - osmWayID is the OpenStreetMap way id (positive int64), stable across
//     re-imports.
//   - seq is the 0-based ordinal of this sub-segment within the way after
//     pgRouting splits the way at intersections.
//   - dir is the travel direction along the way's node ordering (Forward → "F",
//     Reverse → "R").
//
// FormatSegmentID does not validate its inputs (osmWayID is expected to be
// positive and seq non-negative); ParseSegmentID is the strict half of the
// round-trip and is what rejects malformed wire keys. Format and Parse are exact
// inverses for every row in docs/fixtures/segment_id/format_cases.json.
func FormatSegmentID(osmWayID int64, seq int, dir Direction) SegmentID {
	return SegmentID(strconv.FormatInt(osmWayID, 10) + ":" + strconv.Itoa(seq) + ":" + dir.String())
}

// ParseSegmentID is the strict inverse of FormatSegmentID. It decomposes a
// canonical wire key "{osm_way_id}:{seq}:{dir}" back into its fields, returning a
// descriptive (never panicking) error for any string that does not exactly match
// the contract in docs/contracts.md §1.
//
// A segment_id is rejected unless ALL of the following hold:
//
//   - it splits into exactly three colon-delimited fields (exactly two colons);
//   - osm_way_id is a canonical base-10 integer >= 1 (positive) with no sign,
//     leading zeros, leading/trailing space, or other cruft;
//   - seq is a canonical base-10 integer >= 0 under the same rules;
//   - dir is exactly the single byte "F" or "R" (case-sensitive).
//
// Every string listed in docs/fixtures/segment_id/parse_invalid.json must return
// an error here; every segment_id in format_cases.json must parse back to its
// source fields.
func ParseSegmentID(id SegmentID) (osmWayID int64, seq int, dir Direction, err error) {
	s := string(id)

	// Exactly two colons → exactly three fields. SplitN with a cap of 4 lets us
	// detect "too many colons" instead of silently swallowing extras.
	fields := strings.SplitN(s, ":", 4)
	if len(fields) != 3 {
		return 0, 0, 0, fmt.Errorf("invalid segment_id %q: want exactly 3 colon-separated fields, got %d", s, len(fields))
	}
	wayField, seqField, dirField := fields[0], fields[1], fields[2]

	osmWayID, err = parseNonNegInt64(wayField)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("invalid segment_id %q: osm_way_id: %w", s, err)
	}
	if osmWayID < 1 {
		return 0, 0, 0, fmt.Errorf("invalid segment_id %q: osm_way_id must be positive (>= 1)", s)
	}

	seq64, err := parseNonNegInt64(seqField)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("invalid segment_id %q: seq: %w", s, err)
	}
	// This guard only fires on 32-bit builds; on 64-bit, math.MaxInt == the
	// int64 max, so ParseInt's 64-bit bound already rejects anything larger.
	// Keep it so a future reader doesn't delete it as unreachable.
	if seq64 > int64(math.MaxInt) {
		return 0, 0, 0, fmt.Errorf("invalid segment_id %q: seq %d overflows int", s, seq64)
	}
	seq = int(seq64)

	switch dirField {
	case "F":
		dir = Forward
	case "R":
		dir = Reverse
	default:
		return 0, 0, 0, fmt.Errorf("invalid segment_id %q: dir: want %q or %q, got %q", s, "F", "R", dirField)
	}

	return osmWayID, seq, dir, nil
}

// parseNonNegInt64 accepts only a canonical non-negative base-10 integer: no
// sign, no whitespace, no leading "+", no leading zeros, and not empty.
// strconv.ParseInt already rejects whitespace, non-digits, and overflow; we
// additionally reject any explicit sign so "+5" and "-5" are both invalid, and
// we reject any value whose base-10 rendering differs from its input so
// non-canonical forms like "007" (which ParseInt would happily read as 7) are
// rejected as a distinct, aliasing wire key. "0" stays canonical because
// FormatInt(0) == "0".
func parseNonNegInt64(field string) (int64, error) {
	if field == "" {
		return 0, fmt.Errorf("empty field")
	}
	if field[0] == '+' || field[0] == '-' {
		return 0, fmt.Errorf("must be a non-negative integer, got %q", field)
	}
	v, err := strconv.ParseInt(field, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("must be a non-negative integer, got %q", field)
	}
	// Reject non-canonical renderings (e.g. leading zeros: "007" -> 7). A
	// segment_id is a string key; "007:0:F" and "7:0:F" must not both parse to
	// the same fields and alias each other on the wire.
	if strconv.FormatInt(v, 10) != field {
		return 0, fmt.Errorf("non-canonical integer (leading zeros not allowed), got %q", field)
	}
	return v, nil
}
