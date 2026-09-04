// Package stats holds the small numeric primitives the daemon computes on
// behalf of its clients, so the two surfaces cannot disagree about a figure
// they both display.
package stats

import "sort"

// Percentile returns the p-th percentile (p in [0, 1]) of xs using the R-7
// convention: LINEAR INTERPOLATION BETWEEN CLOSEST RANKS, the same definition
// as Excel's PERCENTILE.INC, NumPy's default `percentile`, and R's `type=7`
// quantile.
//
// NAMING THE CONVENTION IS THE POINT (#1905). "The p95" is not one number:
// over 12 samples, R-7 and the nearest-rank convention differ by a whole
// sample, and two surfaces each computing "the p95" independently will draw
// two different lines from the same data and neither will be wrong. The
// daemon therefore computes these once and ships the result; this function is
// the single definition, and TestPercentile_UsesR7NotNearestRank pins it
// against the convention it is most likely to silently become.
//
// The definition, for sorted xs of length n:
//
//	h    = (n-1) * p
//	lo   = floor(h)
//	frac = h - lo
//	P    = xs[lo] + frac * (xs[lo+1] - xs[lo])
//
// so P(0) is the minimum and P(1) the maximum exactly, and every percentile in
// between moves continuously as a sample is added. xs is sorted in place.
// An empty xs returns 0.
func Percentile(xs []float64, p float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	sort.Float64s(xs)
	return PercentileSorted(xs, p)
}

// PercentileSorted is Percentile for input that is already sorted ascending —
// the form the bucket builders use, which sort each bucket once and then read
// several percentiles out of it.
func PercentileSorted(sorted []float64, p float64) float64 {
	n := len(sorted)
	if n == 0 {
		return 0
	}
	if p <= 0 {
		return sorted[0]
	}
	if p >= 1 {
		return sorted[n-1]
	}
	h := float64(n-1) * p
	lo := int(h)
	if lo >= n-1 {
		return sorted[n-1]
	}
	frac := h - float64(lo)
	return sorted[lo] + frac*(sorted[lo+1]-sorted[lo])
}
