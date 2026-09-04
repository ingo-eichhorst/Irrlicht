package main

import (
	"net/http"
	"sort"
	"time"

	"irrlicht/core/pkg/stats"
	"irrlicht/core/ports/outbound"
)

// Autonomy (#1905) — the History view's own top-level section, two elements
// over one data source: a percentile line chart of autonomous run duration
// over time, and a per-project run strip.
//
// Both are served from the always-on span log (outbound.AutonomySpanStore),
// never from the opt-in lifecycle recordings that back chart=agents/state.

const (
	// chartAutonomyDuration is element 1: one entry per time bucket carrying
	// p95/p50/p5 plus the true min/max and the sample count.
	chartAutonomyDuration = "autonomy_duration"

	// chartAutonomySpans is element 2: the individual spans in a trailing
	// window, for the per-project run strip.
	chartAutonomySpans = "autonomy_spans"
)

// autonomySampleFloor is the minimum number of spans a bucket needs before its
// p95 and p5 are percentiles rather than restatements of its own max and min.
//
// It bites harder than a p90 would, which is why it exists at all: at n < 20
// the R-7 p95 of a bucket interpolates within its top pair and the p5 within
// its bottom pair, so the two outer lines collapse onto the min/max envelope
// element 1 explicitly rejected — three lines drawn from what are really two
// points. A bucket under the floor is MARKED (see historyAutonomyBucket.Thin)
// and drawn differently by both clients, never hidden and never smoothed:
// hiding it would turn a low-activity day into a gap, which reads as "no
// runs", which is a different and false claim.
const autonomySampleFloor = 20

// autonomySpanLimit caps how many spans one strip request returns. A year of
// heavy use is tens of thousands of spans, and the strip cannot draw more than
// its own pixel width anyway; the cap keeps the payload bounded. When it bites
// the response says so (Truncated), because a strip silently drawn from part
// of its window is exactly the "wrong number with nothing on screen saying so"
// this feature is supposed to avoid.
const autonomySpanLimit = 20000

// autonomyDurationSpec pairs one Range's bucket width with its bucket count.
//
// SEPARATE FROM historyGranularitySpecs ON PURPOSE, and the separation is
// load-bearing (#1905). The keys look alike and mean opposite things: there, a
// key names a BUCKET WIDTH which is then multiplied by a count, so "24h"
// resolves to a THIRTY-DAY window. Here and in autonomySpanWindowSeconds a key
// names the WINDOW ITSELF. Merging the two tables in a later refactor would
// silently redefine what a user's "24h" means;
// TestAutonomyWindows_AreNotHistoryGranularities is the tripwire.
type autonomyDurationSpec struct {
	bucketSeconds int64
	buckets       int64
}

// autonomyDurationSpecs is element 1's Range vocabulary. Two ranges, and 30
// days is the floor: anything shorter has too few spans per bucket for a
// percentile to mean anything.
var autonomyDurationSpecs = map[string]autonomyDurationSpec{
	"30d": {86400, 30},     // 30 daily buckets
	"1y":  {7 * 86400, 52}, // 52 weekly buckets
}

// autonomySpanWindowSeconds is element 2's Span vocabulary — WINDOW LENGTHS,
// each the whole trailing period the strip draws.
var autonomySpanWindowSeconds = map[string]int64{
	"8h":   8 * 3600,
	"24h":  24 * 3600,
	"7d":   7 * 86400,
	"30d":  30 * 86400,
	"12mo": 365 * 86400,
}

// isAutonomyChart reports whether a ?chart= value is one of the two autonomy
// elements — both of which resolve their window from ?window= instead of the
// usual ?range=/?bucket= pair.
func isAutonomyChart(chart string) bool {
	return chart == chartAutonomyDuration || chart == chartAutonomySpans
}

// autonomyDefaultWindow is the ?window= value assumed when the client sends
// none, per chart.
func autonomyDefaultWindow(chart string) string {
	if chart == chartAutonomySpans {
		return "24h"
	}
	return "30d"
}

// resolveAutonomyWindow resolves a chart's ?window= into a trailing
// [start, end) window plus, for the duration chart, its bucket width. ok is
// false for a window the chart does not offer — the two charts have different
// vocabularies and neither accepts the other's keys.
func resolveAutonomyWindow(chart, window string) (bucketSeconds, start, end int64, ok bool) {
	end = time.Now().Unix()
	if chart == chartAutonomySpans {
		secs, known := autonomySpanWindowSeconds[window]
		if !known {
			return 0, 0, 0, false
		}
		return 0, end - secs, end, true
	}
	spec, known := autonomyDurationSpecs[window]
	if !known {
		return 0, 0, 0, false
	}
	return spec.bucketSeconds, end - spec.bucketSeconds*spec.buckets, end, true
}

// autonomyWindowError is the 400 body for an unrecognized ?window=, naming the
// vocabulary the requested chart actually has.
func autonomyWindowError(chart string) string {
	if chart == chartAutonomySpans {
		return "invalid window for chart=autonomy_spans: use 8h|24h|7d|30d|12mo"
	}
	return "invalid window for chart=autonomy_duration: use 30d|1y"
}

// historyAutonomyBucket is one time bucket of element 1. Buckets with no spans
// are OMITTED from the response entirely rather than sent with zeros — a day
// with no runs is a gap, not a run of length zero, and a zero would pull the
// line to the axis (the same rule appendSparsePoints applies to the cost
// series). Count is therefore always ≥ 1 on a bucket that is present.
type historyAutonomyBucket struct {
	TS    int64   `json:"ts"`
	P95   float64 `json:"p95"`
	P50   float64 `json:"p50"`
	P5    float64 `json:"p5"`
	Min   float64 `json:"min"`
	Max   float64 `json:"max"`
	Count int     `json:"count"`
	// Thin marks a bucket below autonomySampleFloor, whose p95/p5 are its own
	// max/min. Both clients render such buckets visibly differently.
	Thin bool `json:"thin,omitempty"`
}

// historyAutonomySummary is the window-wide figure row under the chart: the
// drawn envelope's percentiles plus the true extremes, which are figures and
// deliberately NOT lines (one overnight run would otherwise redraw the whole
// Y scale and flatten every other bucket onto the floor).
type historyAutonomySummary struct {
	P95   float64 `json:"p95"`
	P50   float64 `json:"p50"`
	P5    float64 `json:"p5"`
	Min   float64 `json:"min"`
	Max   float64 `json:"max"`
	Count int     `json:"count"`
}

// historyAutonomyDurationResponse is the chart=autonomy_duration payload.
type historyAutonomyDurationResponse struct {
	Window        string                  `json:"window"`
	Chart         string                  `json:"chart"`
	Start         int64                   `json:"start"`
	End           int64                   `json:"end"`
	BucketSeconds int64                   `json:"bucket_seconds"`
	BucketStarts  []int64                 `json:"bucket_starts"`
	Buckets       []historyAutonomyBucket `json:"buckets"`
	Summary       historyAutonomySummary  `json:"summary"`
	SampleFloor   int                     `json:"sample_floor"`
	// EarliestSpan is the earliest span on record across the WHOLE log, not
	// this window — 0 when nothing has ever been recorded. It is what lets
	// both clients say "collecting since <date>" instead of leaving an empty
	// chart to be read as "you did nothing" (#1905).
	EarliestSpan  int64 `json:"earliest_span"`
	TotalRecorded int   `json:"total_recorded"`
}

// historyAutonomySpanRow is one span on the wire for element 2.
type historyAutonomySpanRow struct {
	Start   int64  `json:"start"`
	End     int64  `json:"end"`
	Project string `json:"project"`
	Session string `json:"session"`
	// Reason is one of session.AutonomyEndReasons(); "" for a span recorded
	// by a build that could not name it.
	Reason string `json:"reason,omitempty"`
}

// historyAutonomySpansResponse is the chart=autonomy_spans payload: the spans
// themselves plus the row order the strip draws them in.
type historyAutonomySpansResponse struct {
	Window string                   `json:"window"`
	Chart  string                   `json:"chart"`
	Start  int64                    `json:"start"`
	End    int64                    `json:"end"`
	Spans  []historyAutonomySpanRow `json:"spans"`
	// Projects is strip row order — most autonomous seconds first — computed
	// once here so the two clients cannot order the same strip differently.
	Projects      []string `json:"projects"`
	EarliestSpan  int64    `json:"earliest_span"`
	TotalRecorded int      `json:"total_recorded"`
	Truncated     bool     `json:"truncated"`
}

// serveHistoryAutonomyDurationChart serves chart=autonomy_duration. A nil
// store yields an empty-but-valid payload rather than an error, mirroring
// serveHistoryAgentsChart.
func serveHistoryAutonomyDurationChart(w http.ResponseWriter, store outbound.AutonomySpanStore, window string, bucketSeconds, start, end int64) {
	res, ok := readAutonomySpans(w, store, outbound.AutonomySpanQuery{Start: start, End: end})
	if !ok {
		return
	}
	writeHistoryJSON(w, buildAutonomyDurationResponse(window, bucketSeconds, start, end, res))
}

// serveHistoryAutonomySpansChart serves chart=autonomy_spans.
func serveHistoryAutonomySpansChart(w http.ResponseWriter, store outbound.AutonomySpanStore, window string, start, end int64) {
	res, ok := readAutonomySpans(w, store, outbound.AutonomySpanQuery{Start: start, End: end, Limit: autonomySpanLimit})
	if !ok {
		return
	}
	writeHistoryJSON(w, buildAutonomySpansResponse(window, start, end, res))
}

// readAutonomySpans performs the store read shared by both elements,
// substituting an empty result for a nil store and writing a 500 on error.
// ok is false once it has written a response.
func readAutonomySpans(w http.ResponseWriter, store outbound.AutonomySpanStore, q outbound.AutonomySpanQuery) (*outbound.AutonomySpanResult, bool) {
	if store == nil {
		return &outbound.AutonomySpanResult{Spans: []outbound.AutonomySpan{}}, true
	}
	res, err := store.SpansInWindow(q)
	if err != nil {
		http.Error(w, errInternalErrorMsg, http.StatusInternalServerError)
		return nil, false
	}
	if res == nil {
		res = &outbound.AutonomySpanResult{Spans: []outbound.AutonomySpan{}}
	}
	return res, true
}

// buildAutonomyDurationResponse buckets the window's spans by the bucket their
// END falls in, and reduces each bucket to its percentile envelope.
//
// Every percentile here is computed ONCE, server-side, with the R-7 convention
// named in stats.Percentile — not because the clients could not divide, but
// because "the p95" is ambiguous enough that two independent implementations
// draw two different lines from the same data (#1905 design decision 5).
func buildAutonomyDurationResponse(window string, bucketSeconds, start, end int64, res *outbound.AutonomySpanResult) historyAutonomyDurationResponse {
	n := 0
	if bucketSeconds > 0 && end > start {
		n = int((end - start + bucketSeconds - 1) / bucketSeconds)
	}
	bucketStarts := make([]int64, n)
	for i := range bucketStarts {
		bucketStarts[i] = start + int64(i)*bucketSeconds
	}

	byBucket := make([][]float64, n)
	all := make([]float64, 0, len(res.Spans))
	for _, s := range res.Spans {
		d := float64(s.Duration())
		if d <= 0 {
			continue
		}
		all = append(all, d)
		if n == 0 {
			continue
		}
		idx := int((s.End - start) / bucketSeconds)
		if idx < 0 || idx >= n {
			continue
		}
		byBucket[idx] = append(byBucket[idx], d)
	}

	buckets := make([]historyAutonomyBucket, 0, n)
	for i, samples := range byBucket {
		// A bucket with no spans is a GAP: omitted, never emitted as a zero.
		if len(samples) == 0 {
			continue
		}
		buckets = append(buckets, autonomyBucketFrom(bucketStarts[i], samples))
	}

	return historyAutonomyDurationResponse{
		Window:        window,
		Chart:         chartAutonomyDuration,
		Start:         start,
		End:           end,
		BucketSeconds: bucketSeconds,
		BucketStarts:  bucketStarts,
		Buckets:       buckets,
		Summary:       autonomySummaryFrom(all),
		SampleFloor:   autonomySampleFloor,
		EarliestSpan:  res.EarliestStart,
		TotalRecorded: res.TotalRecorded,
	}
}

// autonomyBucketFrom reduces one bucket's samples to its envelope. samples is
// sorted in place, then read five times — one sort per bucket, not per line.
func autonomyBucketFrom(ts int64, samples []float64) historyAutonomyBucket {
	sort.Float64s(samples)
	return historyAutonomyBucket{
		TS:    ts,
		P95:   stats.PercentileSorted(samples, 0.95),
		P50:   stats.PercentileSorted(samples, 0.50),
		P5:    stats.PercentileSorted(samples, 0.05),
		Min:   samples[0],
		Max:   samples[len(samples)-1],
		Count: len(samples),
		Thin:  len(samples) < autonomySampleFloor,
	}
}

// autonomySummaryFrom reduces the whole window's samples to the figure row.
func autonomySummaryFrom(samples []float64) historyAutonomySummary {
	if len(samples) == 0 {
		return historyAutonomySummary{}
	}
	sort.Float64s(samples)
	return historyAutonomySummary{
		P95:   stats.PercentileSorted(samples, 0.95),
		P50:   stats.PercentileSorted(samples, 0.50),
		P5:    stats.PercentileSorted(samples, 0.05),
		Min:   samples[0],
		Max:   samples[len(samples)-1],
		Count: len(samples),
	}
}

// buildAutonomySpansResponse renders the window's spans plus the strip's row
// order (most autonomous seconds first, then name for a stable tie-break).
func buildAutonomySpansResponse(window string, start, end int64, res *outbound.AutonomySpanResult) historyAutonomySpansResponse {
	rows := make([]historyAutonomySpanRow, 0, len(res.Spans))
	totals := map[string]int64{}
	for _, s := range res.Spans {
		rows = append(rows, historyAutonomySpanRow{
			Start:   s.Start,
			End:     s.End,
			Project: s.Project,
			Session: s.Session,
			Reason:  s.Reason,
		})
		totals[s.Project] += s.Duration()
	}
	projects := make([]string, 0, len(totals))
	for p := range totals {
		projects = append(projects, p)
	}
	sort.Slice(projects, func(i, j int) bool {
		if totals[projects[i]] != totals[projects[j]] {
			return totals[projects[i]] > totals[projects[j]]
		}
		return projects[i] < projects[j]
	})
	return historyAutonomySpansResponse{
		Window:        window,
		Chart:         chartAutonomySpans,
		Start:         start,
		End:           end,
		Spans:         rows,
		Projects:      projects,
		EarliestSpan:  res.EarliestStart,
		TotalRecorded: res.TotalRecorded,
		Truncated:     res.Truncated,
	}
}
