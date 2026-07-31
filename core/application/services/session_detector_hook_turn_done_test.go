package services

import (
	"testing"

	"irrlicht/core/domain/session"
)

// hookTurnDoneSID is the session id every case below drives the overlay with.
const hookTurnDoneSID = "s"

// TestOverlayHookTurnDone covers the Stop-hook turn-done overlay (#1161):
// consume-once semantics, the display-text overwrite, the additive waiting-cue
// verdict, and the no-op guards. White-box so it can drive the unexported
// overlay + hookTurnDone map directly. Each case is a named function rather
// than an inline closure so no single function accumulates all their branches.
func TestOverlayHookTurnDone(t *testing.T) {
	t.Run("fresh signal is applied and consumed once", hookTurnDoneAppliedOnce)
	t.Run("empty text does not clobber existing LastAssistantText", hookTurnDoneKeepsPriorText)
	t.Run("waitingCue is additive — never clears the parser's verdict", hookTurnDoneCueIsAdditive)
	t.Run("no pending signal is a no-op", hookTurnDoneNoSignalIsNoOp)
	t.Run("nil metrics is a no-op", hookTurnDoneNilMetricsIsNoOp)
}

func hookTurnDoneAppliedOnce(t *testing.T) {
	d := &SessionDetector{hookTurnDone: map[string]hookStopSignal{
		hookTurnDoneSID: {lastAssistantText: "Which option?", waitingCue: true},
	}}
	state := &session.SessionState{SessionID: hookTurnDoneSID, Metrics: &session.SessionMetrics{}}

	d.overlayHookTurnDone(state)
	if !state.Metrics.HookTurnDone {
		t.Error("HookTurnDone must be set for a session with a pending Stop signal")
	}
	if state.Metrics.LastAssistantText != "Which option?" {
		t.Errorf("LastAssistantText = %q, want the hook's message", state.Metrics.LastAssistantText)
	}
	if !state.Metrics.PendingWaitingCue {
		t.Error("PendingWaitingCue must be set when the hook reported a waiting cue")
	}
	if _, ok := d.hookTurnDone[hookTurnDoneSID]; ok {
		t.Error("signal must be consumed (deleted) after one overlay pass")
	}

	// Consume-once: a second pass on a fresh metrics struct must NOT re-apply.
	next := &session.SessionState{SessionID: hookTurnDoneSID, Metrics: &session.SessionMetrics{}}
	d.overlayHookTurnDone(next)
	if next.Metrics.HookTurnDone {
		t.Error("consumed signal must not bleed into the next pass")
	}
}

func hookTurnDoneKeepsPriorText(t *testing.T) {
	d := &SessionDetector{hookTurnDone: map[string]hookStopSignal{
		hookTurnDoneSID: {lastAssistantText: "", waitingCue: false},
	}}
	state := &session.SessionState{SessionID: hookTurnDoneSID, Metrics: &session.SessionMetrics{
		LastAssistantText: "prior text",
	}}
	d.overlayHookTurnDone(state)
	if state.Metrics.LastAssistantText != "prior text" {
		t.Errorf("LastAssistantText = %q, want the prior text preserved", state.Metrics.LastAssistantText)
	}
	if !state.Metrics.HookTurnDone {
		t.Error("HookTurnDone must still be set even with an empty message")
	}
}

func hookTurnDoneCueIsAdditive(t *testing.T) {
	d := &SessionDetector{hookTurnDone: map[string]hookStopSignal{
		hookTurnDoneSID: {lastAssistantText: "done", waitingCue: false},
	}}
	state := &session.SessionState{SessionID: hookTurnDoneSID, Metrics: &session.SessionMetrics{
		PendingWaitingCue: true, // the parser already found a cue
	}}
	d.overlayHookTurnDone(state)
	if !state.Metrics.PendingWaitingCue {
		t.Error("a false hook cue must not clear a PendingWaitingCue the parser set")
	}
}

func hookTurnDoneNoSignalIsNoOp(t *testing.T) {
	d := &SessionDetector{hookTurnDone: map[string]hookStopSignal{}}
	state := &session.SessionState{SessionID: hookTurnDoneSID, Metrics: &session.SessionMetrics{}}
	d.overlayHookTurnDone(state)
	if state.Metrics.HookTurnDone {
		t.Error("a session with no pending Stop signal must not be marked done")
	}
}

func hookTurnDoneNilMetricsIsNoOp(t *testing.T) {
	d := &SessionDetector{hookTurnDone: map[string]hookStopSignal{hookTurnDoneSID: {}}}
	state := &session.SessionState{SessionID: hookTurnDoneSID, Metrics: nil}
	d.overlayHookTurnDone(state) // must not panic
	if _, ok := d.hookTurnDone[hookTurnDoneSID]; !ok {
		t.Error("nil metrics must leave the pending signal untouched")
	}
}
