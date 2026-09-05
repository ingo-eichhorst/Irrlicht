package main

import (
	"testing"

	"irrlicht/core/domain/session"
	"irrlicht/core/ports/outbound"
)

// projectsFor builds the session→project join the reconstruction needs.
func projectsFor(sessions ...string) map[string]string {
	out := map[string]string{}
	for _, s := range sessions {
		out[s] = "proj"
	}
	return out
}

func tr(ts int64, sid, state string) transition {
	return transition{TS: ts, Session: sid, State: state}
}

// The span rules are a re-implementation of the daemon's own
// (core/application/services/autonomy_span.go), so each rule is pinned
// separately rather than through one long scenario.
func TestSpanRules(t *testing.T) {
	cases := []struct {
		name        string
		transitions []transition
		wantSpans   []rawSpan
	}{
		{
			name:        "a turn that finished on its own",
			transitions: []transition{tr(100, "s", session.StateWorking), tr(200, "s", session.StateReady), tr(1000, "s", session.StateWorking)},
			wantSpans:   []rawSpan{{Start: 100, End: 200, Session: "s", Reason: session.StateReady}},
		},
		{
			name:        "a turn that stopped to ask gets NO grace",
			transitions: []transition{tr(100, "s", session.StateWorking), tr(200, "s", session.StateWaiting)},
			wantSpans:   []rawSpan{{Start: 100, End: 200, Session: "s", Reason: session.StateWaiting}},
		},
		{
			name:        "a turn that broke gets no grace either",
			transitions: []transition{tr(100, "s", session.StateWorking), tr(200, "s", session.StateError)},
			wantSpans:   []rawSpan{{Start: 100, End: 200, Session: "s", Reason: session.StateError}},
		},
		{
			// The flicker rule: a working→ready→working round trip inside the
			// grace is a tool-call boundary, not the end of a run. Merging it
			// is the whole reason the grace exists.
			name: "a ready inside the grace does not close the span",
			transitions: []transition{
				tr(100, "s", session.StateWorking), tr(200, "s", session.StateReady),
				tr(205, "s", session.StateWorking), tr(300, "s", session.StateWaiting),
			},
			wantSpans: []rawSpan{{Start: 100, End: 300, Session: "s", Reason: session.StateWaiting}},
		},
		{
			name: "a ready outside the grace closes it, and the next working opens a new one",
			transitions: []transition{
				tr(100, "s", session.StateWorking), tr(200, "s", session.StateReady),
				tr(215, "s", session.StateWorking), tr(300, "s", session.StateWaiting),
			},
			wantSpans: []rawSpan{
				{Start: 100, End: 200, Session: "s", Reason: session.StateReady},
				{Start: 215, End: 300, Session: "s", Reason: session.StateWaiting},
			},
		},
		{
			// A pending ready WINS over the state that followed it: the span
			// ended when the session left `working`, which was to `ready`.
			name: "a pending ready wins over a later waiting",
			transitions: []transition{
				tr(100, "s", session.StateWorking),
				tr(200, "s", session.StateReady),
				tr(400, "s", session.StateWaiting),
			},
			wantSpans: []rawSpan{{Start: 100, End: 200, Session: "s", Reason: session.StateReady}},
		},
		{
			// THE ORPHAN-CLOSE RULE. The close half of a run whose open half
			// is not in the retained log emits nothing: no start means no
			// duration, and inventing one would fabricate the number the whole
			// section is about.
			name:        "a close with no open emits nothing",
			transitions: []transition{tr(200, "s", session.StateWaiting)},
			wantSpans:   nil,
		},
		{
			name: "two sessions do not interfere",
			transitions: []transition{
				tr(100, "a", session.StateWorking), tr(110, "b", session.StateWorking),
				tr(200, "a", session.StateWaiting), tr(300, "b", session.StateError),
			},
			wantSpans: []rawSpan{
				{Start: 100, End: 200, Session: "a", Reason: session.StateWaiting},
				{Start: 110, End: 300, Session: "b", Reason: session.StateError},
			},
		},
		{
			name:        "a zero-length span is a transition artefact, not a run",
			transitions: []transition{tr(100, "s", session.StateWorking), tr(100, "s", session.StateWaiting)},
			wantSpans:   nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := newSpanBuilder(graceSeconds)
			for _, tn := range tc.transitions {
				b.apply(tn)
			}
			b.finish(1_000_000) // long past every fixture, so a pending ready settles
			assertSpans(t, b.spans, tc.wantSpans)
		})
	}
}

// A span still open when the log ends is DROPPED. An open span has no measured
// end, and "it must have stopped by now" is the single easiest way to
// manufacture a multi-day run out of nothing.
func TestUnclosedSpanIsDroppedNotInvented(t *testing.T) {
	b := newSpanBuilder(graceSeconds)
	b.apply(tr(100, "s", session.StateWorking))
	b.finish(1_000_000)
	if len(b.spans) != 0 {
		t.Fatalf("an unclosed span produced %d span(s): %+v", len(b.spans), b.spans)
	}
	if b.loss.UnclosedAtEnd != 1 {
		t.Fatalf("UnclosedAtEnd = %d, want 1 — a silent drop is the same as no drop at all", b.loss.UnclosedAtEnd)
	}
}

func TestOrphanClosesAreCounted(t *testing.T) {
	b := newSpanBuilder(graceSeconds)
	b.apply(tr(200, "s", session.StateWaiting))
	b.apply(tr(300, "s", session.StateError))
	b.apply(tr(400, "s", session.StateReady)) // provisional; counted separately
	b.finish(1_000_000)
	if b.loss.OrphanCloses != 2 {
		t.Errorf("OrphanCloses = %d, want 2", b.loss.OrphanCloses)
	}
	if b.loss.OrphanReady != 1 {
		t.Errorf("OrphanReady = %d, want 1", b.loss.OrphanReady)
	}
}

func TestStraddlesRestart(t *testing.T) {
	restarts := []int64{100, 500, 900}
	cases := []struct {
		name string
		span rawSpan
		want bool
	}{
		{"wholly before every restart", rawSpan{Start: 10, End: 90}, false},
		{"wholly between two restarts", rawSpan{Start: 150, End: 450}, false},
		{"wholly after every restart", rawSpan{Start: 950, End: 990}, false},
		{"a restart strictly inside", rawSpan{Start: 400, End: 600}, true},
		{"two restarts inside", rawSpan{Start: 50, End: 600}, true},
		// A restart exactly at a boundary is the transition that opened or
		// closed the span, not something that happened during it.
		{"restart exactly at the start", rawSpan{Start: 100, End: 200}, false},
		{"restart exactly at the end", rawSpan{Start: 50, End: 100}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := straddlesRestart(tc.span, restarts); got != tc.want {
				t.Fatalf("straddlesRestart(%+v) = %v, want %v", tc.span, got, tc.want)
			}
		})
	}
}

// THE RESTART RULE, end to end. A span that crosses a daemon restart is not
// one run — it is several, merged because the transitions between them were
// never logged. It is dropped, never split: splitting would invent the
// boundaries the log does not contain.
func TestRestartStraddlingSpanIsDropped(t *testing.T) {
	log := &eventLog{
		Transitions: []transition{
			tr(100, "s", session.StateWorking), tr(200, "s", session.StateWaiting), // clean
			tr(300, "s", session.StateWorking), tr(900, "s", session.StateWaiting), // straddles the restart at 500
		},
		Restarts: []int64{500},
	}
	spans, loss := reconstructEventSpans(log, projectsFor("s"), 1_000_000)
	if len(spans) != 1 {
		t.Fatalf("kept %d spans, want 1: %+v", len(spans), spans)
	}
	if spans[0].Start != 100 || spans[0].End != 200 {
		t.Fatalf("kept the wrong span: %+v", spans[0])
	}
	if loss.RestartStraddlers != 1 {
		t.Fatalf("RestartStraddlers = %d, want 1", loss.RestartStraddlers)
	}
	// Dropped, NOT split: two half-spans would be exactly the invented
	// boundaries this rule exists to refuse.
	for _, s := range spans {
		if s.Start == 300 || s.End == 900 {
			t.Fatalf("the straddling span was split rather than dropped: %+v", s)
		}
	}
}

// Every span from the event log is marked source=log and keeps the REAL end
// reason the transition recorded.
func TestEventSpansAreMarkedAndKeepTheirRealReason(t *testing.T) {
	log := &eventLog{Transitions: []transition{
		tr(100, "s", session.StateWorking), tr(200, "s", session.StateWaiting),
	}}
	spans, _ := reconstructEventSpans(log, projectsFor("s"), 1_000_000)
	if len(spans) != 1 {
		t.Fatalf("spans = %d, want 1", len(spans))
	}
	if spans[0].Source != session.AutonomySourceLog {
		t.Errorf("Source = %q, want %q", spans[0].Source, session.AutonomySourceLog)
	}
	if !session.IsAutonomyReconstructed(spans[0].Source) {
		t.Error("an event-log span does not read back as reconstructed")
	}
	if spans[0].Reason != session.StateWaiting {
		t.Errorf("Reason = %q, want the measured %q", spans[0].Reason, session.StateWaiting)
	}
}

// A span with no project cannot be drawn on a per-project strip, and the span
// store no-ops on it anyway. Dropped and counted, never written.
func TestSpanWithNoProjectIsDropped(t *testing.T) {
	log := &eventLog{Transitions: []transition{
		tr(100, "s", session.StateWorking), tr(200, "s", session.StateWaiting),
	}}
	spans, loss := reconstructEventSpans(log, map[string]string{}, 1_000_000)
	if len(spans) != 0 {
		t.Fatalf("spans = %d, want 0", len(spans))
	}
	if loss.NoProject != 1 {
		t.Fatalf("NoProject = %d, want 1", loss.NoProject)
	}
}

// THE HONESTY RULE FOR THE COST ERA. The cost log records that a session was
// consuming tokens and nothing whatsoever about why it stopped, so every span
// from it carries `unknown` — never a guessed end reason, and in particular
// never `ready`, which would paint four months of the strip green under a
// claim nobody measured.
func TestCostSpansCarryUnknownAndNeverAGuessedReason(t *testing.T) {
	cl := &costLog{Series: map[sessionKey][]int64{
		{Project: "proj", Session: "s1"}: {100, 160, 220},
		{Project: "proj", Session: "s2"}: {1000, 1060},
	}}
	spans, _ := reconstructCostSpans(cl, costGapSeconds, 0, 0)
	if len(spans) != 2 {
		t.Fatalf("spans = %d, want 2", len(spans))
	}
	for _, s := range spans {
		if s.Reason != session.AutonomyReasonUnknown {
			t.Errorf("Reason = %q, want %q", s.Reason, session.AutonomyReasonUnknown)
		}
		if session.IsAutonomyEndReason(s.Reason) {
			t.Errorf("Reason %q reads back as a real end reason — a cost-derived span must never "+
				"claim one", s.Reason)
		}
		if s.Source != session.AutonomySourceCost {
			t.Errorf("Source = %q, want %q", s.Source, session.AutonomySourceCost)
		}
	}
}

// `unknown` is deliberately NOT a session state, so nothing derived from
// session.CanonicalStates() can start treating it as a fifth one.
func TestUnknownIsNotASessionState(t *testing.T) {
	if session.IsCanonicalState(session.AutonomyReasonUnknown) {
		t.Fatal("AutonomyReasonUnknown is a canonical state — it must not be")
	}
	if session.IsAutonomyEndReason(session.AutonomyReasonUnknown) {
		t.Fatal("AutonomyReasonUnknown reads back as an end reason — it must not")
	}
	for _, r := range session.AutonomyEndReasons() {
		if r == session.AutonomyReasonUnknown {
			t.Fatal("AutonomyEndReasons() yields `unknown`")
		}
	}
	// It also has to rank below every measured reason on the strip's collapse
	// ladder, or one unknown span would grey out a column holding a real error.
	for _, r := range session.AutonomyEndReasons() {
		if session.AutonomyReasonPriority(session.AutonomyReasonUnknown) >= session.AutonomyReasonPriority(r) {
			t.Fatalf("`unknown` outranks or ties %q on the collapse ladder", r)
		}
	}
}

// The cost era stops where the event log begins. The two sources must never
// both describe the same instant, and the event log is the better witness
// wherever it reaches.
func TestCostSpansStopAtTheEventLogBoundary(t *testing.T) {
	cl := &costLog{Series: map[sessionKey][]int64{
		{Project: "proj", Session: "old"}:      {100, 160},
		{Project: "proj", Session: "straddle"}: {900, 1000, 1100},
		{Project: "proj", Session: "new"}:      {2000, 2060},
	}}
	spans, loss := reconstructCostSpans(cl, costGapSeconds, 1000, 0)
	if len(spans) != 1 || spans[0].Session != "old" {
		t.Fatalf("spans = %+v, want only the pre-boundary one", spans)
	}
	if loss.BoundaryStraddlers != 2 {
		t.Fatalf("BoundaryStraddlers = %d, want 2", loss.BoundaryStraddlers)
	}
}

// A --since floor drops older spans without counting them as loss: the caller
// asking for less is not the data failing.
func TestCostSpansHonourTheSinceFloor(t *testing.T) {
	cl := &costLog{Series: map[sessionKey][]int64{
		{Project: "proj", Session: "s1"}: {100, 160},
		{Project: "proj", Session: "s2"}: {5000, 5060},
	}}
	spans, _ := reconstructCostSpans(cl, costGapSeconds, 0, 1000)
	if len(spans) != 1 || spans[0].Session != "s2" {
		t.Fatalf("spans = %+v, want only the one starting after the floor", spans)
	}
}

// THE DOUBLE-COUNT RULE. The daemon's event log keeps being written after the
// Autonomy feature ships, so the log era and the live span log's era OVERLAP.
// A run present in both would be counted twice — the one error a back-fill can
// make that looks like productivity rather than like a bug.
func TestSpansReachingIntoTheMeasuredEraAreDropped(t *testing.T) {
	spans := []outbound.AutonomySpan{
		{Start: 100, End: 200, Project: "p", Session: "a"},  // wholly before
		{Start: 900, End: 1100, Project: "p", Session: "b"}, // reaches into it
		{Start: 2000, End: 2100, Project: "p", Session: "c"},
	}
	kept, dropped := dropOverlappingLive(spans, 1000)
	if dropped != 2 {
		t.Fatalf("dropped = %d, want 2", dropped)
	}
	if len(kept) != 1 || kept[0].Session != "a" {
		t.Fatalf("kept = %+v, want only the span that ends before the live floor", kept)
	}
	// Dropped, not clipped: a clipped span has an end nobody observed.
	for _, s := range kept {
		if s.End > 1000 {
			t.Fatalf("a span was clipped to the floor rather than dropped: %+v", s)
		}
	}
}

// A log the daemon has measured nothing into leaves the reconstruction
// unbounded on the right — which is the first-run case, and must not silently
// drop everything.
func TestNoLiveFloorLeavesEverySpanInPlace(t *testing.T) {
	spans := []outbound.AutonomySpan{{Start: 100, End: 200, Project: "p", Session: "a"}}
	kept, dropped := dropOverlappingLive(spans, 0)
	if dropped != 0 || len(kept) != 1 {
		t.Fatalf("kept %d / dropped %d with no live floor, want 1 / 0", len(kept), dropped)
	}
}

// TRIPWIRE. The 180 s gap threshold strictly dominates the 10 s production
// flicker grace, which is why applying the grace INSIDE a cost stretch would
// be a no-op: two stretches can never be closer together than the threshold.
// If someone lowers the threshold below the grace, that reasoning stops
// holding and this fails rather than quietly producing merged runs.
func TestCostGapDominatesTheProductionGrace(t *testing.T) {
	if graceSeconds <= 0 {
		t.Fatal("graceSeconds is not positive — the import of services.AutonomySpanGrace is broken")
	}
	if costGapSeconds <= graceSeconds {
		t.Fatalf("costGapSeconds (%d) no longer dominates the production grace (%d): the cost-era "+
			"reconstruction would need to apply the flicker rule explicitly", costGapSeconds, graceSeconds)
	}
}

// The sensitivity table must always contain the threshold actually in force,
// or it reports on a choice nobody made.
func TestSensitivityTableIncludesTheThresholdInForce(t *testing.T) {
	for _, g := range sensitivityThresholds {
		if g == costGapSeconds {
			return
		}
	}
	t.Fatalf("sensitivityThresholds %v does not include costGapSeconds (%d)", sensitivityThresholds, costGapSeconds)
}

func TestSessionProjectsJoinsThroughTheCostLog(t *testing.T) {
	cl := &costLog{Series: map[sessionKey][]int64{
		{Project: "beta", Session: "s1"}:  {100},
		{Project: "alpha", Session: "s1"}: {200}, // the same session under two projects
		{Project: "alpha", Session: "s2"}: {300},
	}}
	got := sessionProjects(cl)
	// Deterministic: sorted by project so the same run always resolves the
	// same way. A back-fill whose output depends on map order cannot be
	// checked against its own dry run.
	if got["s1"] != "alpha" {
		t.Errorf("s1 → %q, want the lexicographically first project", got["s1"])
	}
	if got["s2"] != "alpha" {
		t.Errorf("s2 → %q, want alpha", got["s2"])
	}
}

func assertSpans(t *testing.T, got, want []rawSpan) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("spans = %+v, want %+v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("span %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}
