package benchmark

import (
	"testing"
)

// TestMedianNanos pins the pure median reducer directly (the #112 per-route figure),
// so the fair-metric CORRECTNESS is proven deterministically here rather than riding on
// the wall-clock timing tests in parallel_test.go. It covers empty, single, odd, even
// (integer truncation of the mid pair), and unsorted input, plus the no-mutation
// guarantee the observer relies on (medianNanos must never reorder the live buffer).
func TestMedianNanos(t *testing.T) {
	cases := []struct {
		name    string
		samples []int64
		want    int64
	}{
		{"empty", nil, 0},
		{"empty-slice", []int64{}, 0},
		{"single", []int64{7}, 7},
		{"odd-sorted", []int64{1, 2, 3}, 2},
		{"odd-unsorted", []int64{3, 1, 2}, 2},
		{"even-truncates", []int64{1, 2, 3, 4}, 2}, // (2+3)/2 = 2 (integer truncation)
		{"even-unsorted", []int64{4, 1, 3, 2}, 2},
		{"duplicates", []int64{5, 5, 5, 5}, 5},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := medianNanos(tc.samples); got != tc.want {
				t.Errorf("medianNanos(%v) = %d, want %d", tc.samples, got, tc.want)
			}
		})
	}
}

// TestMedianNanosDoesNotMutate asserts the live sample buffer is left untouched: the
// observer calls medianNanos on the SAME growing slice every tick while Route keeps
// appending to it, so any in-place sort would corrupt the ordering the appends assume.
func TestMedianNanosDoesNotMutate(t *testing.T) {
	samples := []int64{3, 1, 2}
	_ = medianNanos(samples)
	want := []int64{3, 1, 2}
	for i := range want {
		if samples[i] != want[i] {
			t.Fatalf("medianNanos mutated its input: got %v, want %v", samples, want)
		}
	}
}
