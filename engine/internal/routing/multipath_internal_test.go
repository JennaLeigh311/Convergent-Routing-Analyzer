package routing

import (
	"testing"
)

// TestProportionalSplitFavorsCheaper asserts the intended split puts more mass on
// cheaper paths and normalizes to 1.
func TestProportionalSplitFavorsCheaper(test *testing.T) {
	paths := []kPath{{cost: 10}, {cost: 20}, {cost: 40}}
	split := proportionalSplit(paths)
	if len(split) != 3 {
		test.Fatalf("split len = %d, want 3", len(split))
	}
	sum := 0.0
	for _, mass := range split {
		sum += mass
	}
	if sum < 1-1e-9 || sum > 1+1e-9 {
		test.Errorf("split sums to %.6f, want 1.0", sum)
	}
	if !(split[0] > split[1] && split[1] > split[2]) {
		test.Errorf("split not strictly decreasing for increasing cost: %v", split)
	}
}

// TestProportionalSplitZeroCostUniform asserts an all-zero-cost K-set falls back to
// a uniform split (no divide-by-zero, no NaN).
func TestProportionalSplitZeroCostUniform(test *testing.T) {
	split := proportionalSplit([]kPath{{cost: 0}, {cost: 0}})
	if len(split) != 2 || split[0] != 0.5 || split[1] != 0.5 {
		test.Errorf("zero-cost split = %v, want [0.5 0.5]", split)
	}
}

// TestPerRequestSeedingIndependentOfOrder is the carry-forward determinism proof
// from the PR #80 review: the seed is derived PER REQUEST (from the request index),
// NOT per worker, so a request's drawn path depends ONLY on (baseSeed, index) and
// is invariant to the order in which requests are processed. We draw each request's
// choice in forward order and again in reverse order and require the per-index
// choice to be identical — which it can only be if each request seeds its own RNG
// rather than consuming from a shared/per-worker stream.
func TestPerRequestSeedingIndependentOfOrder(test *testing.T) {
	const baseSeed = 555
	intended := []float64{0.5, 0.3, 0.2}
	const n = 64

	forward := make([]int, n)
	for index := 0; index < n; index++ {
		forward[index] = drawPath(intended, requestRNG(baseSeed, index))
	}

	reverse := make([]int, n)
	for index := n - 1; index >= 0; index-- {
		reverse[index] = drawPath(intended, requestRNG(baseSeed, index))
	}

	for index := 0; index < n; index++ {
		if forward[index] != reverse[index] {
			test.Fatalf("request %d drew path %d forward but %d in reverse order — "+
				"choice depends on processing order, so seeding is NOT per-request",
				index, forward[index], reverse[index])
		}
	}
}

// TestRequestRNGDistinctStreams asserts different request indices draw from
// distinct streams (the seed actually varies with the index), so requests are not
// all making the identical draw.
func TestRequestRNGDistinctStreams(test *testing.T) {
	intended := []float64{0.5, 0.3, 0.2}
	choices := make(map[int]bool)
	for index := 0; index < 40; index++ {
		choices[drawPath(intended, requestRNG(123, index))] = true
	}
	if len(choices) < 2 {
		test.Errorf("all 40 requests drew the same path index — streams are not distinct per request")
	}
}
