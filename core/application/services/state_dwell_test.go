package services

import (
	"testing"
	"time"

	"irrlicht/core/domain/agent"
	"irrlicht/core/domain/lifecycle"
	"irrlicht/core/domain/session"
)

// dwellDetector builds a detector wired for the classify pipeline and nothing
// else, driving both the signal holds and the #1366 dwell off one virtual
// clock the test advances by hand.
//
// The clock is a pointer the caller moves, not a fixed value, because a dwell
// is only observable across passes: the whole mechanism is "what the second
// pass decides about what the first pass proposed".
func dwellDetector(clock *time.Time) (*SessionDetector, *ceilingRecorder) {
	d, _, rec := detectorWithSinks()
	d.now = func() time.Time { return *clock }
	return d, rec
}

// workingMetrics is a transcript shape the classifier resolves to working via
// the transcript_activity default: a tool call is open (so IsAgentDone is
// false), the tool is neither user-blocking nor permission-gated (so neither
// the user_blocking_tool rule nor the #488 stalled-edit arming reaches it).
func workingMetrics() *session.SessionMetrics {
	return &session.SessionMetrics{
		LastEventType:     "assistant",
		HasOpenToolCall:   true,
		LastOpenToolNames: []string{"Bash"},
	}
}

// dwellSession is the fixture every test here varies by one field. Note
// ParentSessionID: it short-circuits holdParentForActiveChildren, keeping these
// tests on the classify/publish path they are about rather than the
// parent-child rule.
func dwellSession(state string, m *session.SessionMetrics) *session.SessionState {
	return &session.SessionState{
		SessionID:       "s",
		Adapter:         "claudecode",
		State:           state,
		ParentSessionID: "p",
		Metrics:         m,
	}
}

// transitions extracts the published state changes, which is the observable
// #1366 is actually about — what reached the UI, not what the classifier
// privately decided.
func transitions(rec *ceilingRecorder) []lifecycle.Event {
	var out []lifecycle.Event
	for _, ev := range rec.events {
		if ev.Kind == lifecycle.KindStateTransition {
			out = append(out, ev)
		}
	}
	return out
}

// TestClassify_AReversedWaitingExitNeverReachesTheUI is #1366's defect test,
// at the altitude the issue states it: not "a map entry was cleared" but "the
// badge never flickered".
//
// The sequence is the one #1355 multiplies by eight. A session is pinned at
// waiting by a permission-prompt hook. One pass sees a transcript that no
// longer looks blocked — the hook's release has landed but the authoritative
// re-assertion has not, which is #1141's ~6s Notification lag — and the
// classifier says working. Two seconds later the prompt is visibly open again
// and the classifier says waiting.
//
// Pre-#1366 both of those reached applyStateTransition, so the user saw
// waiting→working→waiting: a badge that flapped to "no attention needed" and
// back on a session that never stopped needing a human.
func TestClassify_AReversedWaitingExitNeverReachesTheUI(t *testing.T) {
	clock := holdT0
	d, rec := dwellDetector(&clock)

	state := dwellSession(session.StateWaiting, workingMetrics())
	ev := agent.Event{SessionID: "s"}

	// Pass 1: the transcript momentarily reads as working.
	d.classifyAndTransition(state, ev)

	// Pass 2, two seconds later and well inside the dwell: the prompt is open
	// again, so the classifier is back to waiting.
	clock = clock.Add(2 * time.Second)
	state.Metrics = workingMetrics()
	d.signals.Hold("s", session.SignalPermissionPrompt, session.SignalPayload{}, clock)
	d.classifyAndTransition(state, ev)

	if got := transitions(rec); len(got) != 0 {
		t.Fatalf("a waiting exit reversed inside the dwell must never be published, got %d transition(s): %+v",
			len(got), got)
	}
	if state.State != session.StateWaiting {
		t.Errorf("the session must still read waiting, got %q", state.State)
	}
}

// TestClassify_AWaitingExitThatHoldsIsPublished is the other half, and the
// reason the test above is not satisfied by simply never publishing anything.
// A grace timer that defers is correct; one that drops is the failure
// graceFor's asymmetry argues against in the opposite direction.
func TestClassify_AWaitingExitThatHoldsIsPublished(t *testing.T) {
	clock := holdT0
	d, rec := dwellDetector(&clock)

	state := dwellSession(session.StateWaiting, workingMetrics())
	ev := agent.Event{SessionID: "s"}

	d.classifyAndTransition(state, ev)
	if state.State != session.StateWaiting {
		t.Fatalf("precondition: the first pass must not publish, got %q", state.State)
	}

	// Far enough past the dwell that retuning waitingExitDwell downward cannot
	// make this test lie; the constant is unexported in core/domain/session.
	clock = clock.Add(time.Minute)
	state.Metrics = workingMetrics()
	d.classifyAndTransition(state, ev)

	got := transitions(rec)
	if len(got) != 1 {
		t.Fatalf("expected exactly one published transition, got %d: %+v", len(got), got)
	}
	if got[0].PrevState != session.StateWaiting || got[0].NewState != session.StateWorking {
		t.Errorf("published %q→%q, want waiting→working", got[0].PrevState, got[0].NewState)
	}
	if state.State != session.StateWorking {
		t.Errorf("session state = %q, want working", state.State)
	}
}

// TestClassify_EnteringWaitingIsNotDelayed is a LOCK, and the one that matters
// most: it passes on main by construction and must keep passing. #1366 buys
// flap suppression by spending latency, and the entire argument in graceFor is
// that none of that latency may be spent on the edge that tells a human they
// are needed.
func TestClassify_EnteringWaitingIsNotDelayed(t *testing.T) {
	clock := holdT0
	d, rec := dwellDetector(&clock)

	state := dwellSession(session.StateWorking, workingMetrics())

	d.signals.Hold("s", session.SignalPermissionPrompt, session.SignalPayload{}, clock)
	d.classifyAndTransition(state, agent.Event{SessionID: "s"})

	if state.State != session.StateWaiting {
		t.Fatalf("working→waiting must publish on the pass that decides it, got %q", state.State)
	}
	got := transitions(rec)
	if len(got) != 1 || got[0].NewState != session.StateWaiting {
		t.Fatalf("expected one working→waiting transition, got %+v", got)
	}
}

// TestClassify_FirstClassificationIsNotDelayed is #1366's scope note 3.
//
// A session's first classification has no previously published waiting to be
// leaving, so no dwell can attach to it. Asserted from the empty current state
// a freshly constructed session carries before buildNewSessionState stamps
// one, and for the waiting target specifically, since that is the case a dwell
// would be most damaging to. LOCK: passes on main.
func TestClassify_FirstClassificationIsNotDelayed(t *testing.T) {
	clock := holdT0
	d, _ := dwellDetector(&clock)

	state := dwellSession("", workingMetrics()) // "" — never classified before

	d.signals.Hold("s", session.SignalPermissionPrompt, session.SignalPayload{}, clock)
	d.classifyAndTransition(state, agent.Event{SessionID: "s"})

	if state.State != session.StateWaiting {
		t.Fatalf("a session's first classification must publish immediately, got %q", state.State)
	}
}

// TestClassify_CeilingExpiryIsNotSwallowedByTheDwell is the interaction test
// between #1366 and #1360, and it is the sharpest test in this change because
// the two features pull in opposite directions on the same edge.
//
// #1360 bounds a hook-asserted hold at twelve hours so a lost release cannot
// pin a session at waiting forever. The unpinning it produces is, by
// construction, a waiting exit — the exact edge #1366 debounces. So the naive
// composition is: the ceiling fires, the classifier finally says working, and
// the dwell holds it back.
//
// That is not merely a delay, it is a permanent loss, and the mechanism is
// worth stating because it is invisible from either feature's own tests.
// refreshStaleSessions is the only thing that revisits a session no transcript
// event is arriving for, and its non-working arm selects sessions where
// SignalHolds.HasAny is true. The ceiling firing is what DELETES the hold — so
// the same pass that starts the dwell is the pass that stops the ticker from
// ever coming back. The session stays pinned at waiting for the life of the
// process, which is precisely the defect #1360 was opened to remove.
//
// This test FAILED against the first version of this branch — a dwell with no
// expiry exemption. It asserts the fix at the altitude that matters: the
// unpinning is published on the pass the ceiling fires, not one dwell later
// and not never.
func TestClassify_CeilingExpiryIsNotSwallowedByTheDwell(t *testing.T) {
	// Past every ceiling any TierHook persistent row declares, framed as an
	// absurd elapsed time rather than the (unexported) constant so retuning a
	// ceiling cannot make this test lie in either direction.
	const beyondAnyHookCeiling = 48 * time.Hour

	clock := holdT0
	d, rec := dwellDetector(&clock)

	state := dwellSession(session.StateWaiting, workingMetrics())

	// The PermissionRequest hook landed and pinned the session. PostToolUse
	// never came: nothing releases this hold but its ceiling.
	d.signals.Hold("s", session.SignalPermissionPrompt, session.SignalPayload{}, clock)

	clock = clock.Add(beyondAnyHookCeiling)
	d.classifyAndTransition(state, agent.Event{SessionID: "s"})

	if state.State != session.StateWorking {
		t.Fatalf("the ceiling expiry must unpin the session on the pass it fires, got %q — "+
			"a dwell that defers this can never be satisfied, because dropping the hold is "+
			"what stops refreshStaleSessions from revisiting the session at all", state.State)
	}

	var sawExpiry, sawTransition bool
	for _, ev := range rec.events {
		switch ev.Kind {
		case lifecycle.KindHoldExpired:
			sawExpiry = true
		case lifecycle.KindStateTransition:
			sawTransition = true
			if ev.PrevState != session.StateWaiting || ev.NewState != session.StateWorking {
				t.Errorf("published %q→%q, want waiting→working", ev.PrevState, ev.NewState)
			}
		}
	}
	if !sawExpiry {
		t.Error("precondition: no hold_expired event — the ceiling did not fire and this test proves nothing")
	}
	if !sawTransition {
		t.Error("the unpinning must be published, not merely applied in memory")
	}
}

// TestClassify_ADwellOutstandingKeepsTheTickerComing is the second, general
// half of the same liveness argument, and it is deliberately not satisfied by
// the expiry exemption above.
//
// Any dwell, whatever started it, needs a later pass to end it. On a session
// with no holds outstanding, refreshStaleSessions' non-working arm would skip
// it — so a dwell started on the last pass a session ever receives would be a
// drop rather than a delay. Pending() is what closes that.
func TestClassify_ADwellOutstandingKeepsTheTickerComing(t *testing.T) {

	t.Run("nothing outstanding is still left alone", func(t *testing.T) {
		// LOCK on the scoping #1376 introduced: widening this to every idle
		// session is a per-tick transcript re-read of the whole machine.
		if newSessionDetector().shouldRevisitIdleSession("s") {
			t.Error("a session with neither a hold nor a dwell must not be revisited")
		}
	})

	t.Run("a hold alone selects the session", func(t *testing.T) {
		d := newSessionDetector()
		d.signals.Hold("s", session.SignalPermissionPrompt, session.SignalPayload{}, holdT0)
		if !d.shouldRevisitIdleSession("s") {
			t.Error("the #1360 arm must still select a held session")
		}
	})

	t.Run("a dwell alone selects the session", func(t *testing.T) {
		d := newSessionDetector()
		d.dwell.Admit("s", session.StateWaiting, session.StateWorking, holdT0)

		if d.signals.HasAny("s") {
			t.Fatal("precondition: no holds — the #1360 arm must not be what selects this session")
		}
		if !d.shouldRevisitIdleSession("s") {
			t.Error("a deferred waiting exit must keep the ticker coming, " +
				"or the grace timer is a dropper and not a delay")
		}
	})

	t.Run("publishing the dwell stops the extra work", func(t *testing.T) {
		d := newSessionDetector()
		d.dwell.Admit("s", session.StateWaiting, session.StateWorking, holdT0)
		d.dwell.Admit("s", session.StateWaiting, session.StateWorking, holdT0.Add(time.Minute))

		if d.shouldRevisitIdleSession("s") {
			t.Error("once the dwell has published, the session goes back to being left alone")
		}
	})
}

// TestClassify_ASynthesizedWaitingIsNotLeftStranded pins the second bypass in
// admitTransition, which a review found was load-bearing, reachable, and held
// up by nothing but a comment: deleting the `state.State != stateBeforeSynthesis`
// disjunct left the whole package green.
//
// The collapsed-waiting synthesizer (#150) reconstructs a waiting episode the
// fswatcher coalesced away, by emitting a synthetic working→waiting and then
// re-classifying from that new base. For a just-closed user-blocking tool the
// re-classification is working — which is the one edge the dwell debounces. So
// without the bypass the pass emits a waiting it has itself already concluded
// is over, and then withholds the exit: a fabricated "needs a human" badge, on
// a session demonstrably not blocked, for a dwell plus a ticker interval.
func TestClassify_ASynthesizedWaitingIsNotLeftStranded(t *testing.T) {
	clock := holdT0
	d, rec := dwellDetector(&clock)

	m := workingMetrics()
	m.SawUserBlockingToolClosedThisPass = true

	state := dwellSession(session.StateWorking, m)

	d.classifyAndTransition(state, agent.Event{SessionID: "s"})

	got := transitions(rec)
	if len(got) != 2 {
		t.Fatalf("expected the synthesized pair (working→waiting, waiting→working), got %d: %+v",
			len(got), got)
	}
	if got[0].PrevState != session.StateWorking || got[0].NewState != session.StateWaiting {
		t.Errorf("first transition %q→%q, want working→waiting", got[0].PrevState, got[0].NewState)
	}
	if got[1].PrevState != session.StateWaiting || got[1].NewState != session.StateWorking {
		t.Errorf("second transition %q→%q, want waiting→working — the dwell must not "+
			"strand the session in a waiting this same pass already ended",
			got[1].PrevState, got[1].NewState)
	}
	if state.State != session.StateWorking {
		t.Errorf("session state = %q, want working", state.State)
	}
}

// TestClassify_AHookTierVerdictIsNotDebounced covers the third bypass, found
// by review: the dwell absorbs a lower-tier guess being corrected, and a
// TierHook verdict *is* the correction.
//
// The concrete edge is compact_in_progress — the one TierHook rule that
// decides working. A manual /compact issued while the session reads waiting
// would otherwise serve the full dwell plus a ticker interval, contradicting
// that hook handler's own stated reason for existing ("there is no transcript
// flush coming during the compaction window").
func TestClassify_AHookTierVerdictIsNotDebounced(t *testing.T) {
	clock := holdT0
	d, rec := dwellDetector(&clock)

	state := dwellSession(session.StateWaiting, workingMetrics())

	// The PreCompact hook fires while the session is pinned at waiting.
	d.signals.Hold("s", session.SignalCompactInProgress, session.SignalPayload{}, clock)
	d.classifyAndTransition(state, agent.Event{SessionID: "s"})

	if state.State != session.StateWorking {
		t.Fatalf("a hook-tier verdict must publish on the pass that decides it, got %q", state.State)
	}
	got := transitions(rec)
	if len(got) != 1 || got[0].NewState != session.StateWorking {
		t.Fatalf("expected one waiting→working transition, got %+v", got)
	}
}

// TestSessionDetector_ReapingASessionEvictsItsDwellAndHolds covers the
// teardown path a review found uncovered.
//
// onRemoved drops both maps, but onRemoved fires on the TRANSCRIPT FILE
// disappearing — and a process dying does not delete its transcript. Every
// PIDManager-driven deletion (dead process, duplicate-PID dedup, ready-TTL
// age-out, parent cleanup, pre-session supersession) tears down through
// removeFromProjectSessions instead, whose own doc comment already says
// "Every PIDManager session removal must route through here so no path leaks
// history again" — and which dropped neither the signal holds nor the dwell.
//
// Nothing revisits a deleted session, so an entry left behind is permanent for
// the life of the process, and a recycled session ID inherits a transition
// proposed for its predecessor.
func TestSessionDetector_ReapingASessionEvictsItsDwellAndHolds(t *testing.T) {
	d := newSessionDetector()
	d.projectSessions["s"] = "proj"

	d.dwell.Admit("s", session.StateWaiting, session.StateWorking, holdT0)
	d.signals.Hold("s", session.SignalPermissionPrompt, session.SignalPayload{}, holdT0)
	if !d.dwell.Pending("s") || !d.signals.HasAny("s") {
		t.Fatal("precondition: both a dwell and a hold must be outstanding")
	}

	// The reap path — not onRemoved.
	d.removeFromProjectSessions("s")

	if d.dwell.Pending("s") {
		t.Error("a reaped session must not leave a pending transition behind")
	}
	if d.signals.HasAny("s") {
		t.Error("a reaped session must not leave a signal hold behind")
	}
}

// TestSessionDetector_WiresAStateDwell pins the CALLER's side of the nil-dwell
// contract: that a detector on the nil path publishes immediately rather than
// panicking.
//
// Since #1450 the "constructor must wire one" half is also covered, by
// mustBeNonZero["dwell"] over both construction paths — so this test is no
// longer the only thing that would notice. It is kept anyway, because the two
// assert different things: the guard asserts the field is set, this asserts
// what happens when it deliberately is not, which is the property that makes
// opting out safe at all. (core/domain/session's own state_dwell_test.go pins
// the same transparency from inside the domain type; this is the detector-side
// lock on it.)
//
// The nil is now written by hand rather than obtained by skipping the
// allocator: since #1450 there are no bare SessionDetector literals left in
// this package, so a test that wants the nil path opts into it explicitly. That
// is the improvement, not a workaround — before, the same nil arrived silently
// in fourteen places that had not chosen it.
func TestSessionDetector_WiresAStateDwell(t *testing.T) {
	bare := newSessionDetector()
	if bare.dwell == nil {
		t.Fatal("precondition: the allocator is supposed to wire a dwell, so " +
			"clearing it below is what puts this detector on the nil path")
	}
	bare.dwell = nil
	// The nil path must be transparent rather than panicking — session.StateDwell
	// promises nil-receiver safety, and this is what pins it from the caller's
	// side.
	if !bare.dwell.Admit("s", session.StateWaiting, session.StateWorking, holdT0) {
		t.Error("a nil dwell must publish immediately (pre-#1366 behaviour)")
	}

	// …and the real constructor must not leave production on that path.
	det := NewSessionDetector(nil, SessionDetectorDeps{})
	if det.dwell == nil {
		t.Fatal("NewSessionDetector must wire a StateDwell, or hysteresis is silently off in the daemon")
	}
}
