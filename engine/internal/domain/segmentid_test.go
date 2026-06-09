package domain

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// fixtureDir is the path (relative to this package directory) of the shared,
// language-neutral golden fixtures that the PySpark pipeline tests against too.
// Testing both sides against the SAME files is what keeps the segment_id wire
// contract from silently drifting between systems. See docs/contracts.md §1.
const fixtureDir = "../../../docs/fixtures/segment_id"

// formatCase mirrors one object in format_cases.json.
type formatCase struct {
	OSMWayID  int64  `json:"osm_way_id"`
	Seq       int    `json:"seq"`
	Dir       string `json:"dir"`
	SegmentID string `json:"segment_id"`
	Note      string `json:"note"`
}

// invalidCase mirrors one object in parse_invalid.json.
type invalidCase struct {
	SegmentID string `json:"segment_id"`
	Reason    string `json:"reason"`
}

func loadFixture[T any](t *testing.T, name string) []T {
	t.Helper()
	path := filepath.Join(fixtureDir, name)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture %s: %v", path, err)
	}
	var out []T
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("unmarshal fixture %s: %v", path, err)
	}
	if len(out) == 0 {
		t.Fatalf("fixture %s is empty", path)
	}
	return out
}

func dirToken(t *testing.T, tok string) Direction {
	t.Helper()
	switch tok {
	case "F":
		return Forward
	case "R":
		return Reverse
	default:
		t.Fatalf("fixture uses unknown dir token %q", tok)
		return 0 // unreachable
	}
}

// TestFormatCasesRoundTrip asserts that every valid fixture row both formats to
// its expected segment_id and parses back to its source fields.
func TestFormatCasesRoundTrip(t *testing.T) {
	cases := loadFixture[formatCase](t, "format_cases.json")
	for _, tc := range cases {
		t.Run(tc.SegmentID, func(t *testing.T) {
			wantDir := dirToken(t, tc.Dir)

			// Format: fields -> string.
			if got := FormatSegmentID(tc.OSMWayID, tc.Seq, wantDir); string(got) != tc.SegmentID {
				t.Errorf("FormatSegmentID(%d, %d, %s) = %q, want %q",
					tc.OSMWayID, tc.Seq, tc.Dir, got, tc.SegmentID)
			}

			// Parse: string -> fields.
			gotWay, gotSeq, gotDir, err := ParseSegmentID(SegmentID(tc.SegmentID))
			if err != nil {
				t.Fatalf("ParseSegmentID(%q) unexpected error: %v", tc.SegmentID, err)
			}
			if gotWay != tc.OSMWayID {
				t.Errorf("ParseSegmentID(%q) osm_way_id = %d, want %d", tc.SegmentID, gotWay, tc.OSMWayID)
			}
			if gotSeq != tc.Seq {
				t.Errorf("ParseSegmentID(%q) seq = %d, want %d", tc.SegmentID, gotSeq, tc.Seq)
			}
			if gotDir != wantDir {
				t.Errorf("ParseSegmentID(%q) dir = %s, want %s", tc.SegmentID, gotDir, tc.Dir)
			}
		})
	}
}

// TestParseInvalidRejected asserts that every malformed fixture string is
// rejected with an error and never panics.
func TestParseInvalidRejected(t *testing.T) {
	cases := loadFixture[invalidCase](t, "parse_invalid.json")
	for _, tc := range cases {
		t.Run(tc.Reason, func(t *testing.T) {
			_, _, _, err := ParseSegmentID(SegmentID(tc.SegmentID))
			if err == nil {
				t.Errorf("ParseSegmentID(%q) = nil error, want rejection (%s)", tc.SegmentID, tc.Reason)
			}
		})
	}
}
