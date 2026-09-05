package stats

import (
	"math"
	"sort"
	"testing"
)

// nearestRankPercentile is the COMMITTED MUTATION for Percentile's convention
// (AGENTS.md: a check that a change adds has no "before the fix" to run red,
// so mutate the thing it protects and confirm the check goes red).
//
// It is the other convention "the p95" plausibly means — nearest rank, R-1,
// the definition every "just take the element at index ceil(p*n)" hand-roll
// arrives at. The tests below assert that production does NOT agree with it on
// the fixture, so a future edit that quietly turns Percentile into this
// function fails here instead of silently redrawing every autonomy chart.
func nearestRankPercentile(xs []float64, p float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	s := append([]float64(nil), xs...)
	sort.Float64s(s)
	rank := int(math.Ceil(p * float64(len(s))))
	if rank < 1 {
		rank = 1
	}
	if rank > len(s) {
		rank = len(s)
	}
	return s[rank-1]
}

// twelveSamples is the fixture the issue calls out: "p95 over 12 samples
// differs by a whole sample depending on the convention". Values are
// 100, 200, … 1200 so a whole-sample difference is unmistakable.
func twelveSamples() []float64 {
	xs := make([]float64, 12)
	for i := range xs {
		xs[i] = float64((i + 1) * 100)
	}
	return xs
}

func TestPercentile_UsesR7NotNearestRank(t *testing.T) {
	// R-7 by hand: h = (12-1)*0.95 = 10.45, lo = 10, frac = 0.45,
	// P = xs[10] + 0.45*(xs[11]-xs[10]) = 1100 + 0.45*100 = 1145.
	const wantR7 = 1145.0
	got := Percentile(twelveSamples(), 0.95)
	if math.Abs(got-wantR7) > 1e-9 {
		t.Fatalf("Percentile(12 samples, 0.95) = %v, want %v (R-7 / PERCENTILE.INC: "+
			"h=(n-1)p, linear interpolation between closest ranks)", got, wantR7)
	}

	// The mutation must actually disagree here, or this test proves nothing:
	// a fixture on which both conventions agree would pass no matter which one
	// production used (AGENTS.md — absence of a finding and inability to look
	// must never produce the same output).
	mutant := nearestRankPercentile(twelveSamples(), 0.95)
	if math.Abs(mutant-got) < 1e-9 {
		t.Fatalf("the nearest-rank mutation agrees with production (%v) on this fixture, "+
			"so the fixture cannot tell the two conventions apart — pick another one", got)
	}
	if math.Abs(mutant-1200) > 1e-9 {
		t.Fatalf("nearest-rank p95 of the fixture = %v, want 1200 — the mutation drifted", mutant)
	}
}

func TestPercentile_EdgesAreExactMinAndMax(t *testing.T) {
	xs := twelveSamples()
	if got := Percentile(xs, 0); got != 100 {
		t.Errorf("Percentile(p=0) = %v, want the minimum 100", got)
	}
	if got := Percentile(xs, 1); got != 1200 {
		t.Errorf("Percentile(p=1) = %v, want the maximum 1200", got)
	}
}

func TestPercentile_P5AndP50OverTheFixture(t *testing.T) {
	// h = 11*0.05 = 0.55 → 100 + 0.55*100 = 155.
	if got := Percentile(twelveSamples(), 0.05); math.Abs(got-155) > 1e-9 {
		t.Errorf("p5 = %v, want 155", got)
	}
	// h = 11*0.5 = 5.5 → 600 + 0.5*100 = 650.
	if got := Percentile(twelveSamples(), 0.50); math.Abs(got-650) > 1e-9 {
		t.Errorf("p50 = %v, want 650", got)
	}
}

func TestPercentile_EmptyAndSingle(t *testing.T) {
	if got := Percentile(nil, 0.95); got != 0 {
		t.Errorf("Percentile(nil) = %v, want 0", got)
	}
	if got := Percentile([]float64{42}, 0.95); got != 42 {
		t.Errorf("Percentile(one sample) = %v, want 42", got)
	}
}

func TestPercentileSorted_MatchesPercentile(t *testing.T) {
	xs := []float64{9, 1, 7, 3, 5}
	sorted := append([]float64(nil), xs...)
	sort.Float64s(sorted)
	for _, p := range []float64{0, 0.05, 0.25, 0.5, 0.95, 1} {
		want := Percentile(append([]float64(nil), xs...), p)
		if got := PercentileSorted(sorted, p); math.Abs(got-want) > 1e-9 {
			t.Errorf("PercentileSorted(p=%v) = %v, Percentile = %v — the two must agree", p, got, want)
		}
	}
}
