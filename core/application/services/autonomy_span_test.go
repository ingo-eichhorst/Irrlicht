package services

import (
	"testing"
	"time"

	"irrlicht/core/domain/session"
	"irrlicht/core/ports/outbound"
)

// recordingSpanStore captures every span the detector closes.
type recordingSpanStore struct {
	spans []outbound.AutonomySpan
	err   error
}

func (r *recordingSpanStore) RecordSpan(s outbound.AutonomySpan) error {
	if r.err != nil {
		return r.err
	}
	r.spans = append(r.spans, s)
	return nil
}
func (r *recordingSpanStore) SpansInWindow(outbound.AutonomySpanQuery) (*outbound.AutonomySpanResult, error) {
	return &outbound.AutonomySpanResult{}, nil
}
func (r *recordingSpanStore) Prune(int) error { return nil }

// spanFixture builds a detector wired to a recording store, plus a session to
// drive transitions on. Struct-literal detector: the span code touches only
// the store and the logger.
func spanFixture() (*SessionDetector, *recordingSpanStore, *session.SessionState) {
	store := &recordingSpanStore{}
	// newSessionDetector, not a bare literal: construction_test.go's guard
	// refuses the literal because it skips every map allocation (#1400/#1450).
	d := newSessionDetector()
	d.log = &gateLogger{}
	d.autonomySpans = store
	st := &session.SessionState{
		SessionID:   "sess-1",
		ProjectName: "irrlicht",
		Adapter:     "claude-code",
		Model:       "claude-opus-4",
	}
	return d, store, st
}

// drive applies a script of (state, unix-second) transitions.
func drive(d *SessionDetector, st *session.SessionState, script ...struct {
	state string
	at    int64
}) {
	for _, step := range script {
		d.applyAutonomySpanTransition(st, step.state, step.at)
		st.State = step.state
	}
}

type step = struct {
	state string
	at    int64
}

// TestAutonomySpan_ReadyFlickerInsideGraceDoesNotBreakTheSpan is the
// grace-period rule (#1905 design decision 1). A working → ready → working
// round trip completed inside AutonomySpanGrace is tool-call flicker, and the
// span must survive it as ONE run.
//
// Both boundary cases are derived from autonomySpanGraceSeconds rather than
// typed, so changing the constant moves the test with it instead of leaving a
// green that no longer measures the constant it names.
func TestAutonomySpan_ReadyFlickerInsideGraceDoesNotBreakTheSpan(t *testing.T) {
	const t0 = 1_700_000_000
	inside := autonomySpanGraceSeconds - 1

	d, store, st := spanFixture()
	drive(d, st,
		step{session.StateWorking, t0},
		step{session.StateReady, t0 + 100},
		step{session.StateWorking, t0 + 100 + inside},
		step{session.StateWaiting, t0 + 500},
	)

	if len(store.spans) != 1 {
		t.Fatalf("got %d spans, want 1 — a %ds dip through ready is inside the %ds grace and must "+
			"NOT break the run", len(store.spans), inside, autonomySpanGraceSeconds)
	}
	got := store.spans[0]
	if got.Start != t0 {
		t.Errorf("span start = %d, want %d — a resumed span keeps its ORIGINAL start", got.Start, t0)
	}
	if got.End != t0+500 || got.Reason != session.StateWaiting {
		t.Errorf("span = [%d,%d] reason %q, want end %d reason %q",
			got.Start, got.End, got.Reason, t0+500, session.StateWaiting)
	}
}

// TestAutonomySpanGrace_IsTenSeconds is a LOCK, not red-first evidence: it
// passes by construction and exists so that changing the window is a
// deliberate act with a second line to update, rather than a one-character
// edit nothing notices. The behavioural tests around it derive their
// boundaries from the constant, so they follow it wherever it goes — which is
// exactly why one test has to hold it still.
func TestAutonomySpanGrace_IsTenSeconds(t *testing.T) {
	if AutonomySpanGrace != 10*time.Second {
		t.Errorf("AutonomySpanGrace = %v, want 10s. Changing it is allowed — but it decides whether "+
			"a dip out of `working` is flicker or the end of a run, so update this lock and say why",
			AutonomySpanGrace)
	}
	if autonomySpanGraceSeconds != 10 {
		t.Errorf("autonomySpanGraceSeconds = %d, want 10 — it must stay AutonomySpanGrace in seconds",
			autonomySpanGraceSeconds)
	}
}

func TestAutonomySpan_ReadyBeyondGraceClosesTheSpan(t *testing.T) {
	const t0 = 1_700_000_000
	outside := autonomySpanGraceSeconds + 1

	d, store, st := spanFixture()
	drive(d, st,
		step{session.StateWorking, t0},
		step{session.StateReady, t0 + 100},
		step{session.StateWorking, t0 + 100 + outside},
		step{session.StateReady, t0 + 400},
	)
	// The second span is still pending (its grace has not run out), so only
	// the first is on record at this point.
	if len(store.spans) != 1 {
		t.Fatalf("got %d spans, want 1 closed so far", len(store.spans))
	}
	first := store.spans[0]
	if first.Start != t0 || first.End != t0+100 || first.Reason != session.StateReady {
		t.Fatalf("first span = [%d,%d] reason %q, want [%d,%d] reason %q — a gap of %ds is beyond the "+
			"%ds grace and ends the run at the moment it went ready",
			first.Start, first.End, first.Reason, t0, t0+100, session.StateReady,
			outside, autonomySpanGraceSeconds)
	}

	// Now let the sweep settle the pending second span.
	if !d.flushExpiredAutonomySpan(st, t0+400+autonomySpanGraceSeconds) {
		t.Fatal("flushExpiredAutonomySpan reported no change once the grace had run out")
	}
	if len(store.spans) != 2 {
		t.Fatalf("got %d spans after the flush, want 2", len(store.spans))
	}
	second := store.spans[1]
	if second.Start != t0+100+outside || second.End != t0+400 {
		t.Errorf("second span = [%d,%d], want [%d,%d]",
			second.Start, second.End, t0+100+outside, t0+400)
	}
}

// TestAutonomySpan_WaitingGetsNoGrace is the asymmetry the grace exists to
// preserve, and the failure this feature must not ship: an agent that asked a
// question and got an answer four seconds later has NOT run continuously
// through the question.
//
// wrongTotal below is the committed mutation — the single fabricated span a
// build that granted `waiting` the same grace would produce. The test names it
// so the failure says what broke, not merely that a count changed.
func TestAutonomySpan_WaitingGetsNoGrace(t *testing.T) {
	const t0 = 1_700_000_000
	const askedAt = t0 + 600 // ran ten minutes, then asked
	const answeredAt = askedAt + 4
	const finishedAt = answeredAt + 300

	d, store, st := spanFixture()
	drive(d, st,
		step{session.StateWorking, t0},
		step{session.StateWaiting, askedAt},
		step{session.StateWorking, answeredAt},
		step{session.StateWaiting, finishedAt},
	)

	if len(store.spans) != 2 {
		wrongTotal := finishedAt - t0
		t.Fatalf("got %d spans, want 2 — granting `waiting` the flicker grace would merge a "+
			"%ds question-and-answer into ONE fabricated %ds run", len(store.spans), answeredAt-askedAt, wrongTotal)
	}
	if store.spans[0].End != askedAt || store.spans[0].Reason != session.StateWaiting {
		t.Errorf("first span ends at %d reason %q, want %d reason %q — the run ends when it needs a human",
			store.spans[0].End, store.spans[0].Reason, askedAt, session.StateWaiting)
	}
	if store.spans[1].Start != answeredAt {
		t.Errorf("second span starts at %d, want %d — answering starts a NEW run",
			store.spans[1].Start, answeredAt)
	}
}

// TestAutonomySpan_ErrorEndsTheSpan pins design decision 3 and its trade-off:
// a resumed run after a usage limit is a NEW span, not a continuation.
func TestAutonomySpan_ErrorEndsTheSpan(t *testing.T) {
	const t0 = 1_700_000_000

	d, store, st := spanFixture()
	drive(d, st,
		step{session.StateWorking, t0},
		step{session.StateError, t0 + 900},
		step{session.StateWorking, t0 + 900 + 1}, // resumed immediately, still a new span
		step{session.StateWaiting, t0 + 2000},
	)
	if len(store.spans) != 2 {
		t.Fatalf("got %d spans, want 2 — `error` ends a span with no grace at all", len(store.spans))
	}
	if store.spans[0].Reason != session.StateError {
		t.Errorf("first span reason = %q, want %q", store.spans[0].Reason, session.StateError)
	}
	if store.spans[1].Start != t0+901 {
		t.Errorf("resumed span start = %d, want %d (a NEW span, not the old one extended)",
			store.spans[1].Start, t0+901)
	}
}

func TestAutonomySpan_PendingSpanSettlesOnTeardown(t *testing.T) {
	const t0 = 1_700_000_000
	d, store, st := spanFixture()
	drive(d, st,
		step{session.StateWorking, t0},
		step{session.StateReady, t0 + 120},
	)
	if len(store.spans) != 0 {
		t.Fatalf("a pending ready close must not be written before the grace runs out; got %d spans",
			len(store.spans))
	}
	// The session goes away (process exit): it can never return to working, so
	// the pending close is filed immediately rather than waiting for a ticker
	// that will never see it again.
	d.settleAutonomySpanOnTeardown(st)
	if len(store.spans) != 1 {
		t.Fatalf("got %d spans after teardown, want 1 — a finished run must not be lost when the "+
			"session is reaped inside the grace window", len(store.spans))
	}
	if store.spans[0].End != t0+120 || store.spans[0].Reason != session.StateReady {
		t.Errorf("span = end %d reason %q, want %d/%q",
			store.spans[0].End, store.spans[0].Reason, t0+120, session.StateReady)
	}
}

func TestAutonomySpan_FlushBeforeGraceIsANoOp(t *testing.T) {
	const t0 = 1_700_000_000
	d, store, st := spanFixture()
	drive(d, st,
		step{session.StateWorking, t0},
		step{session.StateReady, t0 + 120},
	)
	if d.flushExpiredAutonomySpan(st, t0+120+autonomySpanGraceSeconds-1) {
		t.Error("the sweep settled a span whose grace had not run out")
	}
	if len(store.spans) != 0 {
		t.Errorf("got %d spans, want 0 while the grace is still running", len(store.spans))
	}
}

func TestAutonomySpan_CarriesItsProvenance(t *testing.T) {
	const t0 = 1_700_000_000
	d, store, st := spanFixture()
	st.Metrics = &session.SessionMetrics{ModelName: "claude-sonnet-4"}
	drive(d, st,
		step{session.StateWorking, t0},
		step{session.StateWaiting, t0 + 60},
	)
	if len(store.spans) != 1 {
		t.Fatalf("got %d spans, want 1", len(store.spans))
	}
	got := store.spans[0]
	if got.Project != "irrlicht" || got.Session != "sess-1" || got.Adapter != "claude-code" {
		t.Errorf("span provenance = %+v, want project/session/adapter carried through", got)
	}
	if got.Model != "claude-sonnet-4" {
		t.Errorf("span model = %q, want the live Metrics.ModelName", got.Model)
	}
}

func TestAutonomySpan_NoStoreStillClearsTheOpenSpan(t *testing.T) {
	const t0 = 1_700_000_000
	d := newSessionDetector() // no store wired
	d.log = &gateLogger{}
	st := &session.SessionState{SessionID: "s", ProjectName: "p"}
	drive(d, st,
		step{session.StateWorking, t0},
		step{session.StateWaiting, t0 + 60},
	)
	if st.AutonomySpanStart != nil || st.AutonomySpanPendingEnd != nil {
		t.Error("a detector with no span store must still clear the open span, or the next " +
			"transition reports a run that started days ago")
	}
}

// TestAutonomyReasonLadderMatchesHistoryBar pins the strip's collapse ladder
// against the session-history strip's (#1805), which is where the order came
// from. Two hand-written ladders in one repo is one too many; this is what
// stops them drifting.
func TestAutonomyReasonLadderMatchesHistoryBar(t *testing.T) {
	pairs := []struct {
		state       string
		historyBar  int8
		autonomyBar int
	}{
		{session.StateError, statePriorityError, session.AutonomyReasonPriority(session.StateError)},
		{session.StateWaiting, statePriorityWaiting, session.AutonomyReasonPriority(session.StateWaiting)},
		{session.StateReady, statePriorityReady, session.AutonomyReasonPriority(session.StateReady)},
	}
	for i := 1; i < len(pairs); i++ {
		prev, cur := pairs[i-1], pairs[i]
		if !(prev.historyBar > cur.historyBar) {
			t.Fatalf("the history bar's ladder no longer ranks %q above %q — this test's premise is gone",
				prev.state, cur.state)
		}
		if !(prev.autonomyBar > cur.autonomyBar) {
			t.Errorf("the autonomy strip ranks %q (%d) at or below %q (%d), but the history bar ranks it "+
				"above — one error in a column must paint the whole column",
				prev.state, prev.autonomyBar, cur.state, cur.autonomyBar)
		}
	}
	if session.AutonomyReasonPriority("nonsense") >= session.AutonomyReasonPriority(session.StateReady) {
		t.Error("an unrecognized reason must rank below every real one, or a build that cannot name a " +
			"state outranks activity it can")
	}
}

func TestAutonomyEndReasons_DerivedFromTheVocabulary(t *testing.T) {
	reasons := session.AutonomyEndReasons()
	if len(reasons) != len(session.CanonicalStates())-1 {
		t.Fatalf("AutonomyEndReasons has %d entries, want one fewer than the canonical vocabulary (%d)",
			len(reasons), len(session.CanonicalStates()))
	}
	for _, r := range reasons {
		if r == session.StateWorking {
			t.Error("`working` is not an end reason — a span that is still working has not ended")
		}
		if !session.IsAutonomyEndReason(r) {
			t.Errorf("IsAutonomyEndReason(%q) is false for a reason the list itself returned", r)
		}
	}
	if session.IsAutonomyEndReason(session.StateWorking) {
		t.Error("IsAutonomyEndReason accepted `working`")
	}
}
