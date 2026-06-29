package benchmark

import (
	"math"
	"time"
)

// demand.go is the diurnal (time-of-day) demand model that makes SimConfig.StartTime
// actually DRIVE traffic instead of merely relabeling the clock (issue #111).
//
// Two seams live here, both pure, documented, and seed-reproducible:
//
//  1. DiurnalDemandFactor(t) — a dimensionless multiplier in [nightTrough, 1] giving
//     how heavy traffic is at a given time-of-day. It is bimodal (an AM rush and a PM
//     rush) with a night trough, so a peak-hour start scales the OD-set total demand
//     UP and a 2 a.m. start scales it DOWN. RunParallel multiplies SweepDemandVPH by
//     this factor, so the chosen start hour changes the network load.
//  2. SpreadDepartures / departureSpread — a deterministic DepartAt assignment that
//     RELEASES the OD set across a demand window instead of all at t=0, with the
//     instantaneous release rate following the diurnal curve over the window. So the
//     peak BUILDS and DRAINS rather than arriving in one static burst.
//
// Both are fully deterministic functions of their inputs (time + request count, NO
// wall clock and NO unseeded rand), so a fixed (StartTime, Seed, Count) yields a
// byte-identical run — the §R5 determinism criterion. The whole model is intentionally
// a thin analytic stand-in: it is the seam a real Spark/Uber demand feed replaces later
// (issue #111 owner note), so it lives in one file behind two small functions.

// Diurnal-curve shape parameters. The curve is the sum of two Gaussian "rush" bumps
// (AM and PM) over a constant night-time floor. The values are ordinary commuting
// hours; they are not calibrated to any particular city (the toy graph has no real
// geography) — they exist to give the start-time slider a believable, monotone-at-the-
// peaks response.
const (
	// amPeakHour / pmPeakHour are the fractional-hour centers of the morning and
	// evening rush peaks (08:00 and 17:30).
	amPeakHour = 8.0
	pmPeakHour = 17.5

	// peakSigma is the spread (in hours) of each rush bump: a larger sigma widens the
	// peak. ~1.75 h gives a rush that is clearly elevated for roughly a two-hour band
	// around its center and decays to the trough well before the opposite peak.
	peakSigma = 1.75

	// nightTrough is the demand floor as a fraction of the peak: the dead-of-night
	// baseline (≈3 a.m.) is nightTrough × the 08:00/17:30 peak. It is strictly > 0 so
	// an off-peak run still has real (if light) traffic and never a degenerate empty
	// demand.
	nightTrough = 0.18
)

// DefaultDemandWindowSeconds is the span over which a run's demand is RELEASED when a
// caller does not pin its own window: one hour of simulated time. Vehicles depart
// spread across [StartTime, StartTime+window] following the diurnal density, so the
// peak builds over the window and drains after it. One hour at the ≈30 s tick is ~120
// release ticks — long enough for congestion to visibly accumulate and dissipate,
// short enough that the run stays cheap on the toy graph.
const DefaultDemandWindowSeconds = 3600.0

// DiurnalDemandFactor returns the time-of-day demand multiplier for instant t, in
// [nightTrough, 1]: 1 at a rush-hour peak (08:00 / 17:30) and nightTrough in the dead
// of night. It is the bimodal AM/PM curve RunParallel scales SweepDemandVPH by so the
// chosen StartTime changes how much traffic the network carries.
//
// It is a PURE function of t's clock time-of-day (evaluated in UTC, the project's sim
// clock): only the hour-of-day matters, not the date, so two runs on different dates
// at the same wall-clock hour see identical demand. There is no randomness, so it is
// trivially reproducible.
func DiurnalDemandFactor(t time.Time) float64 {
	h := hourOfDay(t)
	// Each bump is a unit-height Gaussian centered on its peak; the night floor lifts
	// the whole curve so the minimum is nightTrough, the maximum (at a peak center) 1.
	peak := math.Max(gaussianBump(h, amPeakHour), gaussianBump(h, pmPeakHour))
	return nightTrough + (1.0-nightTrough)*peak
}

// gaussianBump is a unit-height Gaussian of the hour-of-day h about center, width
// peakSigma: exp(-½((h-center)/σ)²). It peaks at 1 when h == center and decays toward
// 0 away from it. h and center are both in [0,24); the rush peaks sit mid-day so the
// natural (non-wrapping) distance is correct here — the night hours fall in the tails
// of both bumps and land on the trough.
func gaussianBump(h, center float64) float64 {
	z := (h - center) / peakSigma
	return math.Exp(-0.5 * z * z)
}

// hourOfDay returns t's time-of-day as a fractional hour in [0,24), evaluated in UTC
// (the sim clock's zone). It is the only thing the diurnal curve depends on.
func hourOfDay(t time.Time) float64 {
	t = t.UTC()
	return float64(t.Hour()) + float64(t.Minute())/60.0 + float64(t.Second())/3600.0
}

// departureSpread returns the DepartAt (seconds from the run start) for each of n
// requests, in request order, by inverse-CDF sampling the diurnal density across the
// window [start, start+windowSeconds]. The result is sorted ascending, so request i
// departs no later than request i+1, and the instantaneous release rate over the
// window follows DiurnalDemandFactor — a start AT a peak releases densely early then
// tails off (peak drains), a start BEFORE a peak releases more densely later (peak
// builds).
//
// It is FULLY DETERMINISTIC: a pure function of (start, windowSeconds, n) with no RNG
// at all — the quantiles are the fixed mid-points (j+0.5)/n — so a fixed (start, n)
// always produces the identical departure schedule (the §R5 determinism guidance:
// "a fixed deterministic mapping from request index/time").
//
// Degenerate inputs are defined: n ≤ 0 returns an empty slice; a non-positive window
// (or a degenerate all-zero density, which cannot occur while nightTrough > 0 but is
// guarded anyway) collapses to all-at-t=0 / a uniform spread respectively, never a
// divide-by-zero or NaN.
func departureSpread(start time.Time, windowSeconds float64, n int) []float64 {
	out := make([]float64, n)
	if n <= 0 {
		return out
	}
	if windowSeconds <= 0 {
		// No window: fall back to the legacy static all-at-once batch (DepartAt = 0).
		return out
	}

	// Build a fine trapezoidal CDF of the diurnal density over the window. The density
	// at offset s is DiurnalDemandFactor(start + s): sampling the SAME curve that scales
	// the magnitude keeps the model self-consistent (the shape that says "how much"
	// also says "when").
	const steps = 240 // 15 s resolution over a 1 h window; finer than the 30 s tick
	dt := windowSeconds / float64(steps)
	cdf := make([]float64, steps+1)
	prev := densityAt(start, 0)
	for i := 1; i <= steps; i++ {
		cur := densityAt(start, float64(i)*dt)
		cdf[i] = cdf[i-1] + 0.5*(prev+cur)*dt // trapezoid rule
		prev = cur
	}
	total := cdf[steps]
	if total <= 0 {
		// Degenerate density (unreachable while nightTrough > 0): spread uniformly.
		for j := 0; j < n; j++ {
			out[j] = (float64(j) + 0.5) / float64(n) * windowSeconds
		}
		return out
	}

	// Map each request's fixed quantile mid-point to a departure offset via the inverse
	// CDF. Mid-points (j+0.5)/n keep the schedule symmetric and avoid piling a request
	// exactly on s=0 or s=window.
	for j := 0; j < n; j++ {
		q := (float64(j) + 0.5) / float64(n) * total
		out[j] = invCDF(cdf, dt, q)
	}
	return out
}

// densityAt evaluates the diurnal demand density at offset s seconds past the run
// start — the curve the departure spread samples. It is just the demand factor at the
// shifted instant.
func densityAt(start time.Time, s float64) float64 {
	return DiurnalDemandFactor(start.Add(time.Duration(s * float64(time.Second))))
}

// invCDF returns the window offset (seconds) at which the trapezoidal CDF first
// reaches q, linearly interpolating within the crossing bin. cdf[i] is the cumulative
// density at offset i*dt; the function binary-searches the smallest i with cdf[i] ≥ q
// and interpolates between bin i-1 and i so the inverse is continuous, not stair-stepped.
func invCDF(cdf []float64, dt, q float64) float64 {
	lo, hi := 0, len(cdf)-1
	for lo < hi {
		mid := (lo + hi) / 2
		if cdf[mid] < q {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	if lo == 0 {
		return 0
	}
	span := cdf[lo] - cdf[lo-1]
	frac := 0.0
	if span > 0 {
		frac = (q - cdf[lo-1]) / span
	}
	return (float64(lo-1) + frac) * dt
}
