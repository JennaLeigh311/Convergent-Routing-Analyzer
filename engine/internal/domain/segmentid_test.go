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

// loadFixture reads and unmarshals a golden fixture from dir/name into a slice
// of T. dir is passed explicitly (rather than hardcoding fixtureDir) so the
// edge_attributes conformance test in this package can share this loader against
// its own fixture directory; see edge_attributes_test.go.
func loadFixture[T any](test *testing.T, dir, name string) []T {
	test.Helper()
	path := filepath.Join(dir, name)
	data, err := os.ReadFile(path)
	if err != nil {
		test.Fatalf("read fixture %s: %v", path, err)
	}
	var out []T
	if err := json.Unmarshal(data, &out); err != nil {
		test.Fatalf("unmarshal fixture %s: %v", path, err)
	}
	if len(out) == 0 {
		test.Fatalf("fixture %s is empty", path)
	}
	return out
}

func dirToken(test *testing.T, tok string) Direction {
	test.Helper()
	switch tok {
	case "F":
		return Forward
	case "R":
		return Reverse
	default:
		test.Fatalf("fixture uses unknown dir token %q", tok)
		return 0 // unreachable
	}
}

// TestFormatCasesRoundTrip asserts that every valid fixture row both formats to
// its expected segment_id and parses back to its source fields.
func TestFormatCasesRoundTrip(test1 *testing.T) {
	cases := loadFixture[formatCase](test1, fixtureDir, "format_cases.json")
	for _, testCase := range cases {
		test1.Run(testCase.SegmentID, func(test2 *testing.T) {
			wantDir := dirToken(test2, testCase.Dir)

			// Format: fields -> string.
			if got := FormatSegmentID(testCase.OSMWayID, testCase.Seq, wantDir); string(got) != testCase.SegmentID {
				test2.Errorf("FormatSegmentID(%d, %d, %s) = %q, want %q",
					testCase.OSMWayID, testCase.Seq, testCase.Dir, got, testCase.SegmentID)
			}

			// Parse: string -> fields.
			gotWay, gotSeq, gotDir, err := ParseSegmentID(SegmentID(testCase.SegmentID))
			if err != nil {
				test2.Fatalf("ParseSegmentID(%q) unexpected error: %v", testCase.SegmentID, err)
			}
			if gotWay != testCase.OSMWayID {
				test2.Errorf("ParseSegmentID(%q) osm_way_id = %d, want %d", testCase.SegmentID, gotWay, testCase.OSMWayID)
			}
			if gotSeq != testCase.Seq {
				test2.Errorf("ParseSegmentID(%q) seq = %d, want %d", testCase.SegmentID, gotSeq, testCase.Seq)
			}
			if gotDir != wantDir {
				test2.Errorf("ParseSegmentID(%q) dir = %s, want %s", testCase.SegmentID, gotDir, testCase.Dir)
			}
		})
	}
}

// TestParseInvalidRejected asserts that every malformed fixture string is
// rejected with an error and never panics.
func TestParseInvalidRejected(test1 *testing.T) {
	cases := loadFixture[invalidCase](test1, fixtureDir, "parse_invalid.json")
	for _, testCase := range cases {
		test1.Run(testCase.Reason, func(test2 *testing.T) {
			_, _, _, err := ParseSegmentID(SegmentID(testCase.SegmentID))
			if err == nil {
				test2.Errorf("ParseSegmentID(%q) = nil error, want rejection (%s)", testCase.SegmentID, testCase.Reason)
			}
		})
	}
}
