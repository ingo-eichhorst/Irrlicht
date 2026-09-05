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
// THE ANSWER CHANGED WITH THE CHART, and the change is deliberate rather than a
// relaxation. Against a percentile a running run had to be excluded: folding a
// floor into a p95 as though it were final always shortens, and shortens the
// longest runs hardest. Against a MAXIMUM there is nothing to distort — "the
// longest run has already lasted three hours" is a true statement, and excluding
// it would make the longest run on the machine the one thing the section cannot
// show. So it counts, and it is marked.
//
// A LOWER-BOUND START has ended, and its length is known to within the daemon's
// own uptime. It counted before and counts now; dropping those is the
// under-count this whole change exists to end.
//
// Mutation fixtures: neither had a "before the fix" to run red against, so each
// drives the wrong answer into its own assertion.

// runningSpan is a run that has not ended, `length` seconds in so far.
func runningSpan(end, length int64, project string) outbound.AutonomySpan {
	s := spanEndingAt(end, length, project, "")
	s.Running = true
	return s
}

// A RUNNING RUN IS THE LONGEST RUN WHEN IT IS THE LONGEST RUN — and it says so.
//
// MUTATION CAUGHT: excludeRunning below, the rule the percentile chart needed
// and this one does not. The fixture is built so the wrong answer is a different
// number rather than a rounding difference — four finished 60 s runs and one
// 4-hour run still going. Excluded, the longest collapses from 14400 to 60.
func TestAutonomy_RunningRunIsTheLongestAndIsMarked(t *testing.T) {
	const end = 1_700_000_000
	spans := []outbound.AutonomySpan{
		spanEndingAt(end-4000, 60, "p", session.StateReady),
		spanEndingAt(end-3000, 60, "p", session.StateReady),
		spanEndingAt(end-2000, 60, "p", session.StateReady),
		spanEndingAt(end-1000, 60, "p", session.StateReady),
		runningSpan(end-10, 4*3600, "p"),
	}
	store := &fakeAutonomyStore{
		spans:       spans,
		measurement: outbound.AutonomySpanMeasurement{Running: 1},
	}

	got := decodeAutonomy(t, store, "chart=autonomy_projects&window=30d")
	panel := panelFor(t, got, "p")
	if panel.Longest != 4*3600 {
		t.Fatalf("longest = %.0f, want %d — a run three hours in is the longest run, whatever a "+
			"percentile would have done with it", panel.Longest, 4*3600)
	}
	if !panel.LongestRunning {
		t.Fatal("the longest run has not ended and carries no mark — a floor drawn as a measurement")
	}
	// …and the payload still says how many runs are floors, which is what lets
	// both clients state it in words rather than leaving it to the mark.
	if got.Measurement.Running != 1 {
		t.Errorf("measurement.running = %d, want 1", got.Measurement.Running)
	}

	// The MUTATION: the percentile chart's exclusion rule, applied here.
	excludeRunning := func(in []outbound.AutonomySpan) float64 {
		var longest float64
		for _, s := range in {
			if s.Running {
				continue
			}
			if d := float64(s.Duration()); d > longest {
				longest = d
			}
		}
		return longest
	}
	if excludeRunning(spans) == panel.Longest {
		t.Fatalf("the exclude-running mutation reports the same longest (%.0f) on this fixture — it "+
			"cannot show that a run in progress counts", panel.Longest)
	}
}

// A LOWER-BOUND START IS A SAMPLE. It is a finished run whose start Irrlicht did
// not see, and dropping such runs is the under-count this whole change exists to
// end — a restart re-discovers every live session as one of these.
//
// MUTATION CAUGHT: treating it like a run to drop removes the longest run in the
// window and takes the run count from 2 to 1.
func TestAutonomy_LowerBoundStartIsStillCounted(t *testing.T) {
	const end = 1_700_000_000
	bounded := spanEndingAt(end-100, 7200, "p", session.AutonomyReasonUnknown)
	bounded.StartLowerBound = true
	store := &fakeAutonomyStore{
		spans:       []outbound.AutonomySpan{spanEndingAt(end-1000, 60, "p", session.StateReady), bounded},
		measurement: outbound.AutonomySpanMeasurement{LowerBoundStart: 1},
	}

	got := decodeAutonomy(t, store, "chart=autonomy_projects&window=30d")
	panel := panelFor(t, got, "p")
	if panel.Runs != 2 {
		t.Fatalf("runs = %d, want 2 — a run whose start was not measured has still FINISHED, and "+
			"dropping it is the under-count this fix is about", panel.Runs)
	}
	if panel.Longest != 7200 {
		t.Errorf("longest = %.0f, want 7200", panel.Longest)
	}
	if got.Measurement.LowerBoundStart != 1 {
		t.Errorf("measurement.start_lower_bound = %d, want 1 — the panel cannot mark what the "+
			"payload does not carry", got.Measurement.LowerBoundStart)
	}
}

// The panel-level marks are omitempty, so a project whose longest run finished
// carries no `longest_running` key at all — a client testing for presence must
// not see one on every panel.
func TestAutonomyPanel_RunningMarkIsAbsentWhenNothingIsRunning(t *testing.T) {
	const end = 1_700_000_000
	store := &fakeAutonomyStore{
		spans: []outbound.AutonomySpan{spanEndingAt(end-1000, 600, "p", session.StateReady)},
	}
	rec := getAutonomy(t, store, "chart=autonomy_projects&window=30d")
	var raw struct {
		Panels []map[string]any `json:"panels"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode raw: %v", err)
	}
	if len(raw.Panels) != 1 {
		t.Fatalf("got %d panels, want 1", len(raw.Panels))
	}
	if _, present := raw.Panels[0]["longest_running"]; present {
		t.Errorf("a finished project carries a `longest_running` key: %v", raw.Panels[0])
	}
}
