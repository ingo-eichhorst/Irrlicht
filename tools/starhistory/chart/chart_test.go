package chart

import (
	"strings"
	"testing"
	"time"
)

func mustParse(t *testing.T, s string) time.Time {
	t.Helper()
	tm, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	return tm
}

func TestBuildSeriesSortsAndAccumulates(t *testing.T) {
	stars := []time.Time{
		mustParse(t, "2025-03-01T00:00:00Z"),
		mustParse(t, "2025-01-01T00:00:00Z"),
		mustParse(t, "2025-02-01T00:00:00Z"),
	}
	series := BuildSeries(stars)
	if len(series) != 3 {
		t.Fatalf("len = %d, want 3", len(series))
	}
	for i, want := range []string{"2025-01-01", "2025-02-01", "2025-03-01"} {
		if got := series[i].Time.Format("2006-01-02"); got != want {
			t.Errorf("series[%d].Time = %s, want %s", i, got, want)
		}
		if series[i].Count != i+1 {
			t.Errorf("series[%d].Count = %d, want %d", i, series[i].Count, i+1)
		}
	}
}

func TestBuildSeriesDoesNotMutateInput(t *testing.T) {
	stars := []time.Time{
		mustParse(t, "2025-03-01T00:00:00Z"),
		mustParse(t, "2025-01-01T00:00:00Z"),
	}
	original := append([]time.Time(nil), stars...)
	BuildSeries(stars)
	for i := range stars {
		if !stars[i].Equal(original[i]) {
			t.Errorf("BuildSeries mutated its input at index %d", i)
		}
	}
}

func TestRenderSVGEmptySeries(t *testing.T) {
	svg := RenderSVG(nil, "owner/repo", mustParse(t, "2025-06-01T00:00:00Z"))
	if !strings.Contains(svg, "No stars yet") {
		t.Errorf("expected empty-state text, got: %s", svg)
	}
	if !strings.HasPrefix(svg, "<svg") {
		t.Errorf("expected SVG output, got: %s", svg)
	}
}

func TestRenderSVGContainsSeriesData(t *testing.T) {
	series := BuildSeries([]time.Time{
		mustParse(t, "2025-01-01T00:00:00Z"),
		mustParse(t, "2025-04-01T00:00:00Z"),
	})
	svg := RenderSVG(series, "owner/repo", mustParse(t, "2025-06-01T00:00:00Z"))
	if !strings.Contains(svg, "owner/repo") {
		t.Errorf("expected repo name in svg title, got: %s", svg)
	}
	if !strings.Contains(svg, "<polyline") {
		t.Errorf("expected a polyline element, got: %s", svg)
	}
	if !strings.Contains(svg, "2025-01-01") || !strings.Contains(svg, "2025-06-01") {
		t.Errorf("expected axis labels for start/end dates, got: %s", svg)
	}
}

func TestRenderSVGEscapesRepoName(t *testing.T) {
	series := BuildSeries([]time.Time{mustParse(t, "2025-01-01T00:00:00Z")})
	svg := RenderSVG(series, `<script>alert(1)</script>`, mustParse(t, "2025-02-01T00:00:00Z"))
	if strings.Contains(svg, "<script>") {
		t.Errorf("expected repo name to be escaped, got: %s", svg)
	}
}

func TestRenderSVGHandlesAsOfBeforeLastStar(t *testing.T) {
	// asOf can be in the past relative to the last star when the caller
	// passes a stale timestamp; the chart should still cover the full range.
	series := BuildSeries([]time.Time{
		mustParse(t, "2025-01-01T00:00:00Z"),
		mustParse(t, "2025-06-01T00:00:00Z"),
	})
	svg := RenderSVG(series, "owner/repo", mustParse(t, "2025-03-01T00:00:00Z"))
	if !strings.Contains(svg, "2025-06-01") {
		t.Errorf("expected chart to extend to the last star's date, got: %s", svg)
	}
}
