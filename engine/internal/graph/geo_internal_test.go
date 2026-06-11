package graph

import (
	"math"
	"testing"

	"github.com/JennaLeigh311/Convergent-Routing-Analyzer/engine/internal/domain"
)

// TestHaversineKnownDistances pins haversine against analytically-known
// great-circle distances. haversine is unexported, so this lives in an internal
// package graph test rather than the external graph_test package.
func TestHaversineKnownDistances(t *testing.T) {
	cases := []struct {
		name    string
		a, b    domain.LatLon
		wantM   float64
		tolM    float64
		comment string
	}{
		{
			name:  "one degree of latitude",
			a:     domain.LatLon{Lat: 41.0, Lon: -8.61},
			b:     domain.LatLon{Lat: 42.0, Lon: -8.61},
			wantM: 111_190, // 1° lat ≈ 111.19 km on a 6371 km sphere
			tolM:  50,
		},
		{
			name:  "one degree of longitude at the equator",
			a:     domain.LatLon{Lat: 0.0, Lon: 0.0},
			b:     domain.LatLon{Lat: 0.0, Lon: 1.0},
			wantM: 111_190, // at the equator 1° lon ≈ 1° lat
			tolM:  50,
		},
		{
			name:  "zero distance identity",
			a:     domain.LatLon{Lat: 41.15, Lon: -8.61},
			b:     domain.LatLon{Lat: 41.15, Lon: -8.61},
			wantM: 0,
			tolM:  1e-6,
		},
		{
			name:  "Porto-area short hop",
			a:     domain.LatLon{Lat: 41.15, Lon: -8.61},
			b:     domain.LatLon{Lat: 41.16, Lon: -8.62},
			wantM: 1_383, // ~1.11 km lat + ~0.84 km lon components
			tolM:  20,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := haversine(tc.a, tc.b)
			if math.Abs(got-tc.wantM) > tc.tolM {
				t.Errorf("haversine(%v, %v) = %.3f m, want %.0f ± %.0f m", tc.a, tc.b, got, tc.wantM, tc.tolM)
			}
			// Symmetry: distance is direction-independent.
			if rev := haversine(tc.b, tc.a); math.Abs(rev-got) > 1e-6 {
				t.Errorf("haversine not symmetric: a→b = %.6f, b→a = %.6f", got, rev)
			}
		})
	}
}

// lonCosFor mirrors the per-query longitude-cosine factor the search computes:
// cos(max(maxAbsLat, |query lat|)). Kept in the test so the admissibility guards
// exercise the exact factor nearest() threads into axisLowerBound.
func lonCosFor(tr *kdTree, qLat float64) float64 {
	return math.Cos(degToRad(math.Max(tr.maxAbsLat, math.Abs(qLat))))
}

// TestKDTreeAxisLowerBoundIsAdmissible is a focused guard on the pruning bound:
// the per-axis lower bound must never exceed the true haversine distance for a
// pure single-axis displacement, or the search could prune the true nearest.
func TestKDTreeAxisLowerBoundIsAdmissible(t *testing.T) {
	// Build a tree so the longitude factor is populated from a realistic max latitude.
	tr := newKDTree([]kdPoint{
		{pos: domain.LatLon{Lat: 41.15, Lon: -8.61}, idx: 0},
		{pos: domain.LatLon{Lat: 41.30, Lon: -8.50}, idx: 1},
	})

	base := domain.LatLon{Lat: 41.15, Lon: -8.61}
	for _, dDeg := range []float64{0.001, 0.01, 0.1, 0.5} {
		// Pure latitude displacement: bound vs true haversine.
		latTrue := haversine(base, domain.LatLon{Lat: base.Lat + dDeg, Lon: base.Lon})
		if lb := tr.axisLowerBound(0, dDeg, lonCosFor(tr, base.Lat)); lb > latTrue+1e-6 {
			t.Errorf("lat lower bound %.4f m exceeds true %.4f m at %g°", lb, latTrue, dDeg)
		}
		// Pure longitude displacement at the largest latitude in the tree, where
		// the bound is tightest; it must still not exceed the true distance.
		lonTrue := haversine(domain.LatLon{Lat: 41.30, Lon: base.Lon}, domain.LatLon{Lat: 41.30, Lon: base.Lon + dDeg})
		if lb := tr.axisLowerBound(1, dDeg, lonCosFor(tr, 41.30)); lb > lonTrue+1e-6 {
			t.Errorf("lon lower bound %.4f m exceeds true %.4f m at %g°", lb, lonTrue, dDeg)
		}
	}
}

// TestKDTreeLonBoundAdmissibleQueryAboveBand is the regression guard for the
// high-latitude pruning bug: when the QUERY latitude exceeds every tree point's
// latitude, cosφ_q < cos(maxAbsLat), so a longitude bound built from maxAbsLat
// alone over-estimates and can mis-prune the true nearest. The bound must use
// cos(max(maxAbsLat, |q.Lat|)) and so never exceed the true haversine distance.
//
// The worst case below (nodes at 80°, query at 80.2°, gap 100°) makes the old
// maxAbsLat-only bound inadmissible: cos²(80°)·sin²(50°) > the true h. Threading
// the query latitude into the factor restores admissibility.
func TestKDTreeLonBoundAdmissibleQueryAboveBand(t *testing.T) {
	tr := newKDTree([]kdPoint{
		{pos: domain.LatLon{Lat: 80.0, Lon: 0.0}, idx: 0},
		{pos: domain.LatLon{Lat: 80.0, Lon: 100.0}, idx: 1},
	})

	const (
		qLat = 80.2  // above the tree's maxAbsLat of 80°
		pLat = 80.0  // far point's latitude (a tree point)
		gap  = 100.0 // longitude gap to the far point
	)
	lonCos := lonCosFor(tr, qLat)

	// True haversine from the query to the far-side point across the full gap, the
	// tightest distance any far-side point could have on this longitude split.
	q := domain.LatLon{Lat: qLat, Lon: 0.0}
	far := domain.LatLon{Lat: pLat, Lon: gap}
	trueD := haversine(q, far)

	if lb := tr.axisLowerBound(1, gap, lonCos); lb > trueD+1e-6 {
		t.Errorf("lon lower bound %.4f m exceeds true %.4f m for query lat %g° above band (maxAbsLat %g°): bound is inadmissible",
			lb, trueD, qLat, tr.maxAbsLat)
	}
}
