package session

import "testing"

// TestCanonicalStates_ReturnsACopy is the mutation fixture for the defensive
// copy in CanonicalStates.
//
// AGENTS.md: a guard a change ADDS has no "before" to run red, so the thing it
// protects gets mutated instead. Removing the copy (`return canonicalStates`)
// left the entire suite green, which meant the copy was justified only by its
// own comment. This is that mutation, committed as a test rather than
// described in a PR body nothing re-runs.
//
// The stakes are not stylistic: canonicalStates backs IsCanonicalState, so a
// caller that sorted or truncated the returned slice in place would redefine
// what the whole daemon considers a valid state — and the filesystem
// repository deletes nothing but SKIPS sessions whose state fails that
// predicate, so every affected session would silently vanish from the UI.
func TestCanonicalStates_ReturnsACopy(t *testing.T) {
	first := CanonicalStates()
	if len(first) == 0 {
		t.Fatal("precondition: the vocabulary must be non-empty")
	}

	// A caller mangling what it was handed, as clumsily as possible.
	original := first[0]
	first[0] = "clobbered"
	first = first[:1]
	_ = first

	if got := CanonicalStates(); got[0] != original {
		t.Errorf("CanonicalStates()[0] = %q after a caller overwrote its result, want %q — "+
			"the function hands out the package's own backing array", got[0], original)
	}
	if len(CanonicalStates()) < 4 {
		t.Errorf("the vocabulary shrank to %d after a caller re-sliced its result",
			len(CanonicalStates()))
	}
	for _, s := range []string{StateWorking, StateWaiting, StateReady, StateError} {
		if !IsCanonicalState(s) {
			t.Errorf("IsCanonicalState(%q) is false after a caller mutated a CanonicalStates result "+
				"— the predicate and the vocabulary share a backing array", s)
		}
	}
}

// TestSignalSessionError_HoldAppliesItsPayload is the direct test of the
// SignalSessionError policy row, and exists because the ladder property test
// cannot stand in for it.
//
// Review found that neutralizing that test's payload helper left the whole
// suite green: its three transcript fixtures set SessionError directly, so
// they satisfy its coverage assertion on their own and the out-of-band HOLD
// path contributed no *verified* coverage at all. This asserts the row itself
// — that a held error reaches metrics, and that a hook-delivered turn end
// retires it.
func TestSignalSessionError_HoldAppliesItsPayload(t *testing.T) {
	// holdT0 / holdSID: the package's shared hold fixtures, so this test moves
	// with the other ~20 TestSignalHolds_* cases rather than pinning a second
	// time origin nobody would think to update.
	now, sid := holdT0, holdSID

	holds := NewSignalHolds()
	holds.Hold(sid, SignalSessionError, SignalPayload{
		SessionError: &SessionError{Phase: ErrorPhaseTerminal, Class: "process_death"},
	}, now)

	m := &SessionMetrics{LastEventType: "assistant"}
	holds.Overlay(sid, m, now)

	if m.SessionError == nil {
		t.Fatal("a held session error did not reach the metrics — the policy row's apply " +
			"never ran, so nothing out-of-band can ever put a session in StateError")
	}
	if m.SessionError.Class != "process_death" {
		t.Errorf("Class = %q, want process_death", m.SessionError.Class)
	}

	// Persistent, not consume-once: an error stands until the session
	// recovers, and an errored session produces no further transcript activity
	// to re-derive it from.
	again := &SessionMetrics{LastEventType: "assistant"}
	holds.Overlay(sid, again, now)
	if again.SessionError == nil {
		t.Error("the hold was consumed on first use — an errored session would leave " +
			"StateError on the very next pass")
	}

	// The clearing rule's hook half: a Stop hook is an authoritative turn
	// boundary, so the turn completed and the hold must retire.
	recovered := &SessionMetrics{LastEventType: "assistant", HookTurnDone: true}
	holds.Overlay(sid, recovered, now)
	if recovered.SessionError != nil {
		t.Errorf("a hook-delivered turn end must retire the error hold, got %+v", recovered.SessionError)
	}
	if holds.Held(sid, SignalSessionError) {
		t.Error("the hold must be dropped, not merely skipped for one pass")
	}
}
