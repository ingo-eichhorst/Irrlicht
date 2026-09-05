package main

import (
	"encoding/json"
	"testing"

	"irrlicht/core/domain/session"
	"irrlicht/core/ports/outbound"
)

// The measurement half of #1905's recording fix, at the API: what a run whose
// duration is a FLOOR rather than a measurement does to the figures.
//
// Two kinds of floor and they are treated differently on purpose:
//
//   - a RUNNING run has not ended, so its length is unknowable — it is counted
//     and drawn, and kept out of every percentile;
//   - a LOWER-BOUND START has ended, and its length is known to within the
//     daemon's own uptime — it is a sample, and it is marked.
//
// Mutation fixtures: neither had a "before the fix" to run red against, so each
// drives the wrong answer into its own assertion.

// runningSpan is a run that has not ended, `length` seconds in so far.
func runningSpan(end, length int64, project string) outbound.AutonomySpan {
	s := spanEndingAt(end, length, project, "")
	s.Running = true
	return s
}

// A RUNNING RUN IS NOT A PERCENTILE SAMPLE.
//
// MUTATION CAUGHT: folding it in. The fixture is built so the wrong answer is a
// different number rather than a rounding difference — four finished 60s runs
// and one 4-hour run still going. Counted, the p95 leaps from 60 to over 11000;
// left out, it stays 60 and the run is reported separately as still going.
func TestAutonomyDuration_RunningRunIsCountedButNotAveraged(t *testing.T) {
	const end = 1_700_000_000
	store := &fakeAutonomyStore{
		spans: []outbound.AutonomySpan{
			spanEndingAt(end-4000, 60, "p", session.StateReady),
			spanEndingAt(end-3000, 60, "p", session.StateReady),
			spanEndingAt(end-2000, 60, "p", session.StateReady),
			spanEndingAt(end-1000, 60, "p", session.StateReady),
			runningSpan(end-10, 4*3600, "p"),
		},
		measurement: outbound.AutonomySpanMeasurement{Running: 1},
	}

	rec := getAutonomy(t, store, "chart=autonomy_duration&window=30d")
	var got historyAutonomyDurationResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if got.Summary.Count != 4 {
		t.Fatalf("summary count = %d, want 4 — a run that has not ended is a LOWER BOUND on its own "+
			"length, and folding it into a percentile treats a floor as a measurement", got.Summary.Count)
	}
	if got.Summary.P95 != 60 || got.Summary.Max != 60 {
		t.Fatalf("p95/max = %.0f/%.0f, want 60/60 — the 4h run still in progress reached the "+
			"envelope", got.Summary.P95, got.Summary.Max)
	}
	// …but it is not hidden. The payload says it is there, which is what lets
	// both clients state the gap between what is drawn and what is measured.
	if got.Measurement.Running != 1 {
		t.Errorf("measurement.running = %d, want 1 — a running run excluded from the percentiles "+
			"and not reported anywhere is a run that was silently dropped", got.Measurement.Running)
	}
	for _, b := range got.Buckets {
		if b.Max > 60 {
			t.Fatalf("bucket at %d has max %.0f — the running run reached a bucket envelope", b.TS, b.Max)
		}
	}
}

// A LOWER-BOUND START IS A SAMPLE. It is a finished run whose start Irrlicht
// did not see, and dropping such runs is the under-count this whole change
// exists to end — a restart re-discovers every live session as one of these.
//
// MUTATION CAUGHT: treating it like a running run (excluding it) takes the
// count from 2 back to 1 and removes the longest run in the window.
func TestAutonomyDuration_LowerBoundStartIsStillASample(t *testing.T) {
	const end = 1_700_000_000
	bounded := spanEndingAt(end-100, 7200, "p", session.AutonomyReasonUnknown)
	bounded.StartLowerBound = true
	store := &fakeAutonomyStore{
		spans:       []outbound.AutonomySpan{spanEndingAt(end-1000, 60, "p", session.StateReady), bounded},
		measurement: outbound.AutonomySpanMeasurement{LowerBoundStart: 1},
	}

	rec := getAutonomy(t, store, "chart=autonomy_duration&window=30d")
	var got historyAutonomyDurationResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Summary.Count != 2 {
		t.Fatalf("summary count = %d, want 2 — a run whose start was not measured has still "+
			"FINISHED, and dropping it is the under-count this fix is about", got.Summary.Count)
	}
	if got.Summary.Max != 7200 {
		t.Errorf("max = %.0f, want 7200", got.Summary.Max)
	}
	if got.Measurement.LowerBoundStart != 1 {
		t.Errorf("measurement.start_lower_bound = %d, want 1 — the panel cannot mark what the "+
			"payload does not carry", got.Measurement.LowerBoundStart)
	}
}

// Every span row carries both marks, so a client can tell a run in progress
// from a finished one without recomputing anything.
//
// MUTATION CAUGHT: dropping either flag from the row leaves the strip drawing a
// still-running run exactly like a completed one.
func TestAutonomySpanRows_CarryRunningAndLowerBound(t *testing.T) {
	const end = 1_700_000_000
	bounded := spanEndingAt(end-500, 300, "p", session.StateReady)
	bounded.Session = "bounded"
	bounded.StartLowerBound = true
	running := runningSpan(end-10, 900, "p")
	running.Session = "running"

	store := &fakeAutonomyStore{
		spans: []outbound.AutonomySpan{
			spanEndingAt(end-900, 60, "p", session.StateReady),
			bounded,
			running,
		},
	}
	rec := getAutonomy(t, store, "chart=autonomy_spans&window=24h")
	var got historyAutonomySpansResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Spans) != 3 {
		t.Fatalf("got %d spans, want 3", len(got.Spans))
	}
	byID := map[string]historyAutonomySpanRow{}
	for _, r := range got.Spans {
		byID[r.Session] = r
	}
	if !byID["running"].Running {
		t.Error("the in-progress row is not marked `running` on the wire")
	}
	if byID["running"].StartLowerBound {
		t.Error("the in-progress row claims an unmeasured start it does not have")
	}
	if !byID["bounded"].StartLowerBound {
		t.Error("the unmeasured-start row lost its mark on the wire")
	}
	if byID["bounded"].Running {
		t.Error("a finished row is marked running")
	}
	// The two marks are omitempty, so an ordinary finished run carries neither
	// key — a client testing for presence must not see one on every row.
	var raw struct {
		Spans []map[string]any `json:"spans"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode raw: %v", err)
	}
	plain := raw.Spans[0]
	if _, present := plain["running"]; present {
		t.Errorf("an ordinary finished run carries a `running` key: %v", plain)
	}
	if _, present := plain["start_lower_bound"]; present {
		t.Errorf("an ordinary finished run carries a `start_lower_bound` key: %v", plain)
	}
}
