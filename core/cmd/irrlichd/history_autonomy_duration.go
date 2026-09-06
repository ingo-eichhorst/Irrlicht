package main

import (
	"net/http"
	"sort"

	"irrlicht/core/pkg/stats"
	"irrlicht/core/ports/outbound"
)

// Autonomy, element 1 (#1905) — THE AGGREGATE PERCENTILE CHART, over every
// project at once: p95 / p50 / p5 of autonomous run duration per time bucket,
// with the plane between p95 and p5 filled as a band.
//
// IT ANSWERS A DIFFERENT QUESTION FROM THE PROJECT PANEL BESIDE IT, which is
// why both are on screen rather than one replacing the other. This chart asks
// "is autonomy getting better ACROSS THE MACHINE" — a trend over the whole
// population of runs, where one project's quiet fortnight cannot move the line
// much. The panel asks "what did THIS project do", where a single run is the
// whole story. A maximum answers the second and is useless for the first: the
// longest run in a bucket is one run, so a per-project maximum charted over all
// projects is a chart of whichever project had the longest run that day.
//
// It was deleted in #1919 and restored here at the maintainer's request. What
// came back is exactly what went: the three percentiles, the translucent band,
// ONE hue at three weights, the sample floor and its thin-bucket marking, and
// the min/max/count figures that stay FIGURES rather than lines. What did NOT
// come back is chart=autonomy_spans, the per-project run strip and its
// end-reason colours — that stays deleted.
//
// Served from the always-on span log, like everything else in this section.
const chartAutonomyDuration = "autonomy_duration"

// autonomySampleFloor is the minimum number of spans a bucket needs before its
// p95 and p5 are percentiles rather than restatements of its own max and min.
//
// It bites harder than a p90 would, which is why it exists at all: at n < 20
// the R-7 p95 of a bucket interpolates within its top pair and the p5 within
// its bottom pair, so the two outer lines collapse onto the min/max envelope
// this chart explicitly rejected — three lines drawn from what are really two
// points. A bucket under the floor is MARKED (see historyAutonomyBucket.Thin)
// and drawn differently by both clients, never hidden and never smoothed:
// hiding it would turn a low-activity day into a gap, which reads as "no runs",
// which is a different and false claim.
const autonomySampleFloor = 20

// historyAutonomyBucket is one time bucket of the aggregate chart. Buckets with
// no spans are OMITTED from the response entirely rather than sent with zeros —
// a day with no runs is a gap, not a run of length zero, and a zero would pull
// the line to the axis (the same rule appendSparsePoints applies to the cost
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

// historyAutonomyDurationSummary is the window-wide figure row under the
// aggregate chart: the drawn envelope's percentiles plus the true extremes,
// which are figures and deliberately NOT lines (one overnight run would
// otherwise redraw the whole Y scale and flatten every other bucket onto the
// floor — an effect the switch to a LINEAR axis in #1905 makes stronger still,
// see autonomyYAt on the web and the Y-scale comment on macOS).
type historyAutonomyDurationSummary struct {
	P95   float64 `json:"p95"`
	P50   float64 `json:"p50"`
	P5    float64 `json:"p5"`
	Min   float64 `json:"min"`
	Max   float64 `json:"max"`
	Count int     `json:"count"`
}

// historyAutonomyDurationResponse is the chart=autonomy_duration payload.
type historyAutonomyDurationResponse struct {
	Window        string                         `json:"window"`
	Chart         string                         `json:"chart"`
	Start         int64                          `json:"start"`
	End           int64                          `json:"end"`
	BucketSeconds int64                          `json:"bucket_seconds"`
	BucketStarts  []int64                        `json:"bucket_starts"`
	Buckets       []historyAutonomyBucket        `json:"buckets"`
	Summary       historyAutonomyDurationSummary `json:"summary"`
	SampleFloor   int                            `json:"sample_floor"`
	// EarliestSpan is the earliest span on record across the WHOLE log, not
	// this window — 0 when nothing has ever been recorded. It is what lets both
	// clients say "collecting since <date>" instead of leaving an empty chart to
	// be read as "you did nothing" (#1905).
	EarliestSpan  int64 `json:"earliest_span"`
	TotalRecorded int   `json:"total_recorded"`
	// Provenance marks how much of THIS window was reconstructed rather than
	// measured (#1905 back-fill). Always present, zero-valued when the whole
	// window was measured live.
	Provenance historyAutonomyProvenance `json:"provenance"`
	// Kinds says what this payload is made of, by run kind (#1905 subagents).
	Kinds historyAutonomyKinds `json:"kinds"`
	// Measurement says how many of the runs in view have a duration that is a
	// floor rather than a measurement (#1905 recording).
	Measurement historyAutonomyMeasurement `json:"measurement"`
}

// serveHistoryAutonomyDurationChart serves chart=autonomy_duration. A nil store
// yields an empty-but-valid payload rather than an error, mirroring
// serveHistoryAgentsChart.
func serveHistoryAutonomyDurationChart(w http.ResponseWriter, store outbound.AutonomySpanStore, window string, bucketSeconds, start, end int64) {
	res, ok := readAutonomySpans(w, store, outbound.AutonomySpanQuery{Start: start, End: end})
	if !ok {
		return
	}
	writeHistoryJSON(w, buildAutonomyDurationResponse(window, bucketSeconds, start, end, res))
}

// buildAutonomyDurationResponse buckets the window's spans by the bucket their
// END falls in, and reduces each bucket to its percentile envelope.
//
// Every percentile here is computed ONCE, server-side, with the R-7 convention
// named in stats.Percentile — not because the clients could not divide, but
// because "the p95" is ambiguous enough that two independent implementations
// draw two different lines from the same data (#1905 design decision 5).
//
// A RUN STILL IN PROGRESS IS NOT A SAMPLE. This is where that is decided (#1905
// recording), and it is a decision rather than an omission.
//
// A run that is three hours in and continuing has a duration of "at least three
// hours". Folding that into a percentile treats a floor as a measurement, and
// the error is not random: it always shortens, and it shortens the longest runs
// hardest, which is the exact bias this whole fix exists to remove. A p95 that
// counted it would say the top 5% of runs were shorter than they were.
//
// It is also the one place the aggregate chart and the project panel beside it
// disagree on purpose: the panel's figure is a MAXIMUM, and "the longest run
// already lasted 3h" is a true statement about a maximum, so a running run
// counts there. The same run is not a percentile sample here. Both surfaces say
// which they did — response.Measurement carries the count either way.
//
// A LOWER-BOUND START is treated differently, and the asymmetry is the point. A
// run Irrlicht met already in progress has FINISHED: its length is known to
// within however long it had been going when Irrlicht started watching, which
// is bounded by the daemon's own uptime. It is a sample, and it is marked.
// Dropping those would re-create the under-count exactly — every long run that
// crosses a restart is one of them.
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
		if s.Running {
			continue
		}
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
		Summary:       autonomyDurationSummaryFrom(all),
		SampleFloor:   autonomySampleFloor,
		EarliestSpan:  res.EarliestStart,
		TotalRecorded: res.TotalRecorded,
		Provenance:    autonomyProvenanceFrom(res.Provenance),
		Kinds:         autonomyKindsFrom(res.Kinds),
		Measurement:   autonomyMeasurementFrom(res.Measurement),
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

// autonomyDurationSummaryFrom reduces the whole window's samples to the figure
// row. Named for its chart rather than for the section, because the project
// panels have a summary of their own (autonomySummaryFrom) that counts a
// different thing — the two must never be swapped for each other.
func autonomyDurationSummaryFrom(samples []float64) historyAutonomyDurationSummary {
	if len(samples) == 0 {
		return historyAutonomyDurationSummary{}
	}
	sort.Float64s(samples)
	return historyAutonomyDurationSummary{
		P95:   stats.PercentileSorted(samples, 0.95),
		P50:   stats.PercentileSorted(samples, 0.50),
		P5:    stats.PercentileSorted(samples, 0.05),
		Min:   samples[0],
		Max:   samples[len(samples)-1],
		Count: len(samples),
	}
}
