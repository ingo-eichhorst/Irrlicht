package session

import (
	"sync"
	"testing"
	"time"
)

// dwellT0 is a fixed virtual instant far from any real clock, deliberately.
// Every elapsed-time assertion below is measured from it, so a StateDwell that
// secretly consulted time.Now would compute an elapsed time of decades and
// publish everything immediately — see
// TestStateDwell_MeasuresTheInjectedClockNotTheWallClock, which is the whole
// point of anchoring here rather than at time.Now().
var dwellT0 = time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)

// TestStateDwell_LeavingWaitingServesTheDwell is the mechanism in one line: a
// proposal to leave waiting is not published on the pass that decides it, and
// is published once it has survived waitingExitDwell.
func TestStateDwell_LeavingWaitingServesTheDwell(t *testing.T) {
	d := NewStateDwell()

	if d.Admit("s", StateWaiting, StateWorking, dwellT0) {
		t.Fatal("the pass that first decides a waiting exit must not publish it")
	}
	if d.Admit("s", StateWaiting, StateWorking, dwellT0.Add(waitingExitDwell-time.Nanosecond)) {
		t.Error("one nanosecond short of the dwell is still short")
	}
	if !d.Admit("s", StateWaiting, StateWorking, dwellT0.Add(waitingExitDwell)) {
		t.Error("a waiting exit that survived the full dwell must publish")
	}
	if d.Pending("s") {
		t.Error("publishing must clear the proposal, or the next pass would re-publish it")
	}
}

// TestStateDwell_AReversedExitNeverPublishes is the defect #1366 names: a
// transition that is immediately reversed must never reach the UI. The
// reversal is observed as the classifier agreeing with the current state
// again, which is why Admit has to be called on every pass and not only on
// the passes that produce a change.
func TestStateDwell_AReversedExitNeverPublishes(t *testing.T) {
	d := NewStateDwell()

	d.Admit("s", StateWaiting, StateWorking, dwellT0)                  // decided
	d.Admit("s", StateWaiting, StateWaiting, dwellT0.Add(time.Second)) // reversed

	if d.Pending("s") {
		t.Fatal("a reversed proposal must be forgotten, not left to mature")
	}
	// Long past the original proposal's dwell. If the reversal had not voided
	// it, this pass would publish a change decided — and abandoned — 10s ago.
	if d.Admit("s", StateWaiting, StateWorking, dwellT0.Add(10*time.Second)) {
		t.Error("the clock must restart after a reversal, not resume")
	}
}

// TestStateDwell_EnteringWaitingIsNeverDelayed is the asymmetry, asserted
// rather than left to the comment on graceFor. It is the half of #1366 that
// must NOT happen: a session that needs a human says so on the pass that
// works it out, from every state, with no grace at all.
func TestStateDwell_EnteringWaitingIsNeverDelayed(t *testing.T) {
	for _, from := range []string{StateWorking, StateReady, ""} {
		d := NewStateDwell()
		if !d.Admit("s", from, StateWaiting, dwellT0) {
			t.Errorf("%q→waiting must publish immediately; delaying a waiting is the expensive error", from)
		}
		if d.Pending("s") {
			t.Errorf("%q→waiting must leave nothing outstanding", from)
		}
	}
}

// TestStateDwell_OnlyWaitingToWorkingIsDebounced pins the scope of the grace
// to the single edge that withdraws the come-look cue. Every other edge — the
// other waiting exit included — publishes on the pass that decides it. See
// graceFor for the reasoning; widening this is a decision, not a refactor, and
// this test is what makes it one.
func TestStateDwell_OnlyWaitingToWorkingIsDebounced(t *testing.T) {
	undebounced := [][2]string{
		{StateWorking, StateReady},
		{StateReady, StateWorking},
		{StateWorking, StateWaiting},
		{StateReady, StateWaiting},
		// The other waiting exit. ready still advertises that a human is
		// needed, so this edge cannot commit the silent error the grace exists
		// to prevent — and debouncing it broke ESC-cancel and #1173's
		// idle-prompt reconciliation.
		{StateWaiting, StateReady},
	}
	for _, edge := range undebounced {
		d := NewStateDwell()
		if !d.Admit("s", edge[0], edge[1], dwellT0) {
			t.Errorf("%s→%s must not be debounced", edge[0], edge[1])
		}
	}

	d := NewStateDwell()
	if d.Admit("s", StateWaiting, StateWorking, dwellT0) {
		t.Error("waiting→working must serve the dwell — it is the only edge that withdraws the cue")
	}
}

// TestStateDwell_MeasuresTheInjectedClockNotTheWallClock is the replay
// constraint from #1366's scope note 4, made mechanical. The virtual timeline
// here sits in the year 2000; a time.Now() anywhere inside StateDwell would
// read decades of elapsed time and publish on the second pass.
func TestStateDwell_MeasuresTheInjectedClockNotTheWallClock(t *testing.T) {
	d := NewStateDwell()

	if d.Admit("s", StateWaiting, StateWorking, dwellT0) {
		t.Fatal("first pass must not publish")
	}
	if d.Admit("s", StateWaiting, StateWorking, dwellT0.Add(time.Second)) {
		t.Fatal("one virtual second in, the dwell is unmet — a wall clock would say decades")
	}
	if !d.Admit("s", StateWaiting, StateWorking, dwellT0.Add(waitingExitDwell)) {
		t.Error("the dwell must be measured on the timeline it was handed")
	}
}

// TestStateDwell_AChangedTargetVoidsTheProposal covers the classifier changing
// its mind about where the session is going while still agreeing it is leaving
// waiting. The new target is judged on its own terms — waiting→ready is not
// debounced at all — and must not inherit elapsed time measured for a
// different edge.
//
// Note what this does and does not reach: it exits at Admit's `grace == 0`
// branch, so it pins the DELETE on an undebounced publish, not pendingExit's
// from/to shape check. That check is unreachable while graceFor debounces a
// single edge; see its comment.
func TestStateDwell_AChangedTargetVoidsTheProposal(t *testing.T) {
	d := NewStateDwell()

	d.Admit("s", StateWaiting, StateWorking, dwellT0)
	if !d.Admit("s", StateWaiting, StateReady, dwellT0.Add(time.Second)) {
		t.Fatal("waiting→ready is undebounced and must publish on its own terms")
	}
	if d.Pending("s") {
		t.Error("publishing an undebounced edge must clear the outstanding proposal")
	}
}

// TestStateDwell_APublishedStateMovingUnderneathRestartsTheClock covers
// something outside the classifier (a terminal UI signal, a synthesizer)
// moving the session's published state while a proposal was outstanding: the
// outstanding entry must not survive it. Same caveat as above — this exits at
// the `grace == 0` branch and asserts the delete, not the shape check.
func TestStateDwell_APublishedStateMovingUnderneathRestartsTheClock(t *testing.T) {
	d := NewStateDwell()

	d.Admit("s", StateWaiting, StateWorking, dwellT0)
	// The session is now published as ready; ready→working is not debounced
	// at all, so it publishes — and must not be treated as the maturing of
	// the waiting→working proposal.
	if !d.Admit("s", StateReady, StateWorking, dwellT0.Add(time.Second)) {
		t.Fatal("ready→working is undebounced and must publish")
	}
	if d.Pending("s") {
		t.Error("an undebounced publish must clear whatever was outstanding")
	}
}

// TestStateDwell_SessionsAreIsolated guards the obvious sharing bug: one
// session's dwell must not satisfy another's.
func TestStateDwell_SessionsAreIsolated(t *testing.T) {
	d := NewStateDwell()

	d.Admit("a", StateWaiting, StateWorking, dwellT0)
	if d.Admit("b", StateWaiting, StateWorking, dwellT0.Add(waitingExitDwell)) {
		t.Error("session b's first sighting must not inherit session a's elapsed time")
	}
	if !d.Admit("a", StateWaiting, StateWorking, dwellT0.Add(waitingExitDwell)) {
		t.Error("session a's own dwell must still complete")
	}
}

// TestStateDwell_DropSessionForgets covers both callers: session teardown, and
// the authoritative-event bypass in admitTransition.
func TestStateDwell_DropSessionForgets(t *testing.T) {
	d := NewStateDwell()

	d.Admit("s", StateWaiting, StateWorking, dwellT0)
	if !d.Pending("s") {
		t.Fatal("precondition: the proposal must be outstanding")
	}
	d.DropSession("s")
	if d.Pending("s") {
		t.Fatal("DropSession must forget the proposal")
	}
	if d.Admit("s", StateWaiting, StateWorking, dwellT0.Add(waitingExitDwell)) {
		t.Error("after DropSession the next sighting is a first sighting")
	}
}

// TestStateDwell_NilDisablesHysteresis pins the nil behaviour the many
// SessionDetector struct literals in the test suite rely on: no grace, and no
// panic. A nil dwell must behave exactly as the code did before #1366.
func TestStateDwell_NilDisablesHysteresis(t *testing.T) {
	var d *StateDwell

	if !d.Admit("s", StateWaiting, StateWorking, dwellT0) {
		t.Error("a nil dwell must publish a real change immediately")
	}
	if d.Admit("s", StateWaiting, StateWaiting, dwellT0) {
		t.Error("a nil dwell must still report 'nothing to publish' when nothing changed")
	}
	if d.Pending("s") {
		t.Error("a nil dwell never has anything outstanding")
	}
	d.DropSession("s") // must not panic
}

// TestStateDwell_ConcurrentAccess is a -race guard. Hooks arrive on HTTP
// handler goroutines while the event loop classifies, which is why StateDwell
// carries its own lock rather than borrowing the detector's.
func TestStateDwell_ConcurrentAccess(t *testing.T) {
	d := NewStateDwell()
	var wg sync.WaitGroup

	for i := range 8 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sid := string(rune('a' + i))
			for n := range 200 {
				at := dwellT0.Add(time.Duration(n) * time.Second)
				d.Admit(sid, StateWaiting, StateWorking, at)
				d.Pending(sid)
				if n%17 == 0 {
					d.DropSession(sid)
				}
			}
		}(i)
	}
	wg.Wait()
}
