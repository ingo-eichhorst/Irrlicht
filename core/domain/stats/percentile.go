// Package stats provides small statistical helpers used by the cost and
// cache-regression detectors. The helpers operate on copies of their input
// so callers' slices are never mutated.
package stats

import "sort"

// Percentile returns the p-th percentile of xs using linear interpolation
// between the two closest ranks (the same method as NumPy's default). p is a
// fraction in [0,1]; values outside that range are clamped. The second return
// value is false when xs is empty (no percentile is defined).
func Percentile(xs []float64, p float64) (float64, bool) {
	if len(xs) == 0 {
		return 0, false
	}
	if p < 0 {
		p = 0
	}
	if p > 1 {
		p = 1
	}
	sorted := make([]float64, len(xs))
	copy(sorted, xs)
	sort.Float64s(sorted)

	if len(sorted) == 1 {
		return sorted[0], true
	}
	// Rank position on a 0-based index scale.
	rank := p * float64(len(sorted)-1)
	lo := int(rank)
	if lo >= len(sorted)-1 {
		return sorted[len(sorted)-1], true
	}
	frac := rank - float64(lo)
	return sorted[lo] + frac*(sorted[lo+1]-sorted[lo]), true
}

// Median returns the 50th percentile of xs. The second return value is false
// when xs is empty.
func Median(xs []float64) (float64, bool) {
	return Percentile(xs, 0.5)
}
