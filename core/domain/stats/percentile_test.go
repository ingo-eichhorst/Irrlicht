package stats

import (
	"math"
	"testing"
)

func almostEqual(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestPercentileEmpty(t *testing.T) {
	if _, ok := Percentile(nil, 0.25); ok {
		t.Fatal("expected ok=false for empty input")
	}
	if _, ok := Median([]float64{}); ok {
		t.Fatal("expected ok=false for empty median")
	}
}

func TestPercentileSingle(t *testing.T) {
	v, ok := Percentile([]float64{42}, 0.25)
	if !ok || v != 42 {
		t.Fatalf("single-element: got %v ok=%v", v, ok)
	}
}

func TestPercentileInterpolation(t *testing.T) {
	xs := []float64{1, 2, 3, 4}
	// p25 over [1,2,3,4]: rank = 0.25*3 = 0.75 → 1 + 0.75*(2-1) = 1.75
	if v, _ := Percentile(xs, 0.25); !almostEqual(v, 1.75) {
		t.Fatalf("p25 = %v, want 1.75", v)
	}
	// median over [1,2,3,4]: rank = 0.5*3 = 1.5 → 2 + 0.5*(3-2) = 2.5
	if v, _ := Median(xs); !almostEqual(v, 2.5) {
		t.Fatalf("median = %v, want 2.5", v)
	}
}

func TestPercentileOddMedian(t *testing.T) {
	if v, _ := Median([]float64{5, 1, 3}); !almostEqual(v, 3) {
		t.Fatalf("median = %v, want 3", v)
	}
}

func TestPercentileClampAndBounds(t *testing.T) {
	xs := []float64{10, 20, 30}
	if v, _ := Percentile(xs, -1); !almostEqual(v, 10) {
		t.Fatalf("p<0 = %v, want 10 (min)", v)
	}
	if v, _ := Percentile(xs, 2); !almostEqual(v, 30) {
		t.Fatalf("p>1 = %v, want 30 (max)", v)
	}
}

func TestPercentileDoesNotMutate(t *testing.T) {
	xs := []float64{3, 1, 2}
	_, _ = Median(xs)
	if xs[0] != 3 || xs[1] != 1 || xs[2] != 2 {
		t.Fatalf("input was mutated: %v", xs)
	}
}
