// Package chart renders a cumulative star-count-over-time SVG from a
// repo's stargazer timestamps, so the README doesn't depend on a live
// render from a third-party service at page-load time.
package chart

import (
	"fmt"
	"html"
	"sort"
	"strings"
	"time"
)

// Point is the cumulative star count as of Time.
type Point struct {
	Time  time.Time
	Count int
}

// BuildSeries turns raw stargazer timestamps into an ascending cumulative series.
func BuildSeries(starredAt []time.Time) []Point {
	sorted := append([]time.Time(nil), starredAt...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Before(sorted[j]) })
	series := make([]Point, len(sorted))
	for i, t := range sorted {
		series[i] = Point{Time: t, Count: i + 1}
	}
	return series
}

const (
	width        = 800
	height       = 320
	marginLeft   = 40
	marginRight  = 20
	marginTop    = 30
	marginBottom = 40
)

// RenderSVG renders series as a cumulative star-count line chart. asOf is
// the chart's right edge (time.Now() in production, a fixed time in tests)
// so the line extends to "today" even if the most recent star is older.
func RenderSVG(series []Point, repo string, asOf time.Time) string {
	var b strings.Builder
	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 %d %d" font-family="-apple-system,BlinkMacSystemFont,Segoe UI,Helvetica,Arial,sans-serif">`, width, height)
	b.WriteString(`<style>
    .line { fill: none; stroke: #2f81f7; stroke-width: 2; }
    .grid { stroke: #d0d7de; stroke-width: 1; }
    .label { fill: #57606a; font-size: 11px; }
    .title { fill: #24292f; font-size: 13px; font-weight: 600; }
    @media (prefers-color-scheme: dark) {
      .grid { stroke: #30363d; }
      .label { fill: #8b949e; }
      .title { fill: #c9d1d9; }
      .line { stroke: #58a6ff; }
    }
  </style>`)
	fmt.Fprintf(&b, `<text x="%d" y="18" class="title">%s stargazers over time</text>`, marginLeft, html.EscapeString(repo))

	if len(series) == 0 {
		fmt.Fprintf(&b, `<text x="%d" y="%d" class="label">No stars yet</text></svg>`, width/2-30, height/2)
		return b.String()
	}

	xMin := series[0].Time
	xMax := asOf
	if xMax.Before(series[len(series)-1].Time) {
		xMax = series[len(series)-1].Time
	}
	xSpan := xMax.Sub(xMin).Seconds()
	if xSpan <= 0 {
		xSpan = 1
	}

	// 15% headroom so the line doesn't touch the top edge.
	yMax := float64(series[len(series)-1].Count) * 1.15
	if yMax <= 0 {
		yMax = 1
	}

	plotW := float64(width - marginLeft - marginRight)
	plotH := float64(height - marginTop - marginBottom)

	xAt := func(t time.Time) float64 {
		return marginLeft + t.Sub(xMin).Seconds()/xSpan*plotW
	}
	yAt := func(count int) float64 {
		return float64(marginTop) + plotH - float64(count)/yMax*plotH
	}

	// Gridlines + y-axis labels at 0/25/50/75/100% of yMax.
	for i := 0; i <= 4; i++ {
		frac := float64(i) / 4
		y := float64(marginTop) + plotH - frac*plotH
		fmt.Fprintf(&b, `<line x1="%d" y1="%.1f" x2="%d" y2="%.1f" class="grid"/>`, marginLeft, y, width-marginRight, y)
		fmt.Fprintf(&b, `<text x="4" y="%.1f" class="label">%d</text>`, y+4, int(frac*yMax))
	}

	// X-axis labels: first star date, chart's right-edge date.
	fmt.Fprintf(&b, `<text x="%d" y="%d" class="label">%s</text>`, marginLeft, height-10, xMin.Format("2006-01-02"))
	fmt.Fprintf(&b, `<text x="%d" y="%d" text-anchor="end" class="label">%s</text>`, width-marginRight, height-10, xMax.Format("2006-01-02"))

	// Polyline through the cumulative series, starting from (xMin, 0) and
	// extending flat to the right edge if the last star predates asOf.
	var pts strings.Builder
	fmt.Fprintf(&pts, "%.1f,%.1f", xAt(xMin), yAt(0))
	for _, p := range series {
		fmt.Fprintf(&pts, " %.1f,%.1f", xAt(p.Time), yAt(p.Count))
	}
	fmt.Fprintf(&pts, " %.1f,%.1f", xAt(xMax), yAt(series[len(series)-1].Count))
	fmt.Fprintf(&b, `<polyline points="%s" class="line"/>`, pts.String())

	b.WriteString(`</svg>`)
	return b.String()
}
