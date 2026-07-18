package services

import (
	"testing"

	"irrlicht/core/domain/session"
)

// idleDoneMetrics returns metrics for a turn that has genuinely finished and is
// idle at the prompt (IsAgentDone true): no open tool, no live background
// process, transcript tail ends on turn_done.
func idleDoneMetrics() *session.SessionMetrics {
	return &session.SessionMetrics{LastEventType: "turn_done"}
}

// TestOverlayIdlePrompt covers the Notification/idle_prompt overlay (#1173):
// persistent (not consume-once) while the turn stays idle, cleared the moment a
// new turn begins, and the no-op guards. White-box so it can drive the
// unexported overlay + idlePromptPending map directly.
func TestOverlayIdlePrompt(t *testing.T) {
	const sid = "s"

	t.Run("applied while the turn is idle, and held (not consumed)", func(t *testing.T) {
		d := &SessionDetector{idlePromptPending: map[string]bool{sid: true}}

		state := &session.SessionState{SessionID: sid, Metrics: idleDoneMetrics()}
		d.overlayIdlePrompt(state)
		if !state.Metrics.IdlePromptPending {
			t.Error("IdlePromptPending must be set while the finished turn is idle")
		}
		if !d.idlePromptPending[sid] {
			t.Error("signal must be held (not consumed) after an overlay pass")
		}

		// A second pass on fresh metrics must re-apply — persistent, unlike the
		// consume-once Stop overlay, so a lower-tier reclassify can't revert the
		// corrected waiting back to ready.
		next := &session.SessionState{SessionID: sid, Metrics: idleDoneMetrics()}
		d.overlayIdlePrompt(next)
		if !next.Metrics.IdlePromptPending {
			t.Error("held signal must re-apply on the next pass")
		}
	})

	t.Run("cleared when a new turn begins (IsAgentDone false)", func(t *testing.T) {
		d := &SessionDetector{idlePromptPending: map[string]bool{sid: true}}
		// New user activity: not turn_done → IsAgentDone false.
		state := &session.SessionState{SessionID: sid, Metrics: &session.SessionMetrics{LastEventType: "user"}}

		d.overlayIdlePrompt(state)
		if state.Metrics.IdlePromptPending {
			t.Error("IdlePromptPending must NOT be set once a new turn started")
		}
		if _, ok := d.idlePromptPending[sid]; ok {
			t.Error("signal must be dropped when the idle window ends")
		}
	})

	t.Run("cleared when an open tool blocks the turn", func(t *testing.T) {
		d := &SessionDetector{idlePromptPending: map[string]bool{sid: true}}
		// An open tool call makes IsAgentDone false — the open-tool rules own
		// that case, not idle-prompt.
		state := &session.SessionState{SessionID: sid, Metrics: &session.SessionMetrics{
			LastEventType:   "turn_done",
			HasOpenToolCall: true,
		}}

		d.overlayIdlePrompt(state)
		if state.Metrics.IdlePromptPending {
			t.Error("IdlePromptPending must NOT be set while a tool call is open")
		}
		if _, ok := d.idlePromptPending[sid]; ok {
			t.Error("signal must be dropped when an open tool blocks the turn")
		}
	})

	t.Run("no pending signal is a no-op", func(t *testing.T) {
		d := &SessionDetector{idlePromptPending: map[string]bool{}}
		state := &session.SessionState{SessionID: sid, Metrics: idleDoneMetrics()}
		d.overlayIdlePrompt(state)
		if state.Metrics.IdlePromptPending {
			t.Error("a session with no pending idle-prompt signal must not be marked waiting")
		}
	})

	t.Run("nil metrics is a no-op and preserves the signal", func(t *testing.T) {
		d := &SessionDetector{idlePromptPending: map[string]bool{sid: true}}
		state := &session.SessionState{SessionID: sid, Metrics: nil}
		d.overlayIdlePrompt(state) // must not panic
		if !d.idlePromptPending[sid] {
			t.Error("nil metrics must leave the pending signal untouched")
		}
	})
}

// TestHasPendingIdlePrompt covers the guard forceReadyToWorkingIfActive reads to
// skip the ready→working bounce on the idle-prompt hook's synthetic reclassify.
func TestHasPendingIdlePrompt(t *testing.T) {
	d := &SessionDetector{idlePromptPending: map[string]bool{"has": true}}
	if !d.hasPendingIdlePrompt("has") {
		t.Error("hasPendingIdlePrompt must report true for a session with a pending signal")
	}
	if d.hasPendingIdlePrompt("none") {
		t.Error("hasPendingIdlePrompt must report false for a session with no signal")
	}
}
