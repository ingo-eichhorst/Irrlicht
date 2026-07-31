package services

import (
	"testing"

	"irrlicht/core/domain/session"
)

// idlePromptSID is the session id every case below drives the overlay with.
const idlePromptSID = "s"

// idleDoneMetrics returns metrics for a turn that has genuinely finished and is
// idle at the prompt (IsAgentDone true): no open tool, no live background
// process, transcript tail ends on turn_done.
func idleDoneMetrics() *session.SessionMetrics {
	return &session.SessionMetrics{LastEventType: "turn_done"}
}

// TestOverlayIdlePrompt covers the Notification/idle_prompt signal (#1173):
// persistent (not consume-once) while the turn stays idle, cleared the moment a
// new turn begins, and the no-op guards.
//
// These assertions predate #1288 and are deliberately unchanged by it: the
// signal moved from a bespoke overlay method to a declared policy in
// session.signalPolicies, and this suite is the lock proving that move altered
// no behaviour. White-box so it can drive the detector's unexported holds
// directly.
func TestOverlayIdlePrompt(t *testing.T) {
	t.Run("applied while the turn is idle, and held (not consumed)", idlePromptHeldWhileIdle)
	t.Run("cleared when a new turn begins (IsAgentDone false)", idlePromptClearedOnNewTurn)
	t.Run("cleared when an open tool blocks the turn", idlePromptClearedByOpenTool)
	t.Run("no pending signal is a no-op", idlePromptNoSignalIsNoOp)
	t.Run("nil metrics is a no-op and preserves the signal", idlePromptNilMetricsIsNoOp)
}

func idlePromptHeldWhileIdle(t *testing.T) {
	d := &SessionDetector{signals: session.NewSignalHolds()}
	d.signals.Hold(idlePromptSID, session.SignalIdlePrompt, session.SignalPayload{})

	state := &session.SessionState{SessionID: idlePromptSID, Metrics: idleDoneMetrics()}
	d.signals.Overlay(state.SessionID, state.Metrics)
	if !state.Metrics.IdlePromptPending {
		t.Error("IdlePromptPending must be set while the finished turn is idle")
	}
	if !d.signals.Held(idlePromptSID, session.SignalIdlePrompt) {
		t.Error("signal must be held (not consumed) after an overlay pass")
	}

	// A second pass on fresh metrics must re-apply — persistent, unlike the
	// consume-once Stop overlay, so a lower-tier reclassify can't revert the
	// corrected waiting back to ready.
	next := &session.SessionState{SessionID: idlePromptSID, Metrics: idleDoneMetrics()}
	d.signals.Overlay(next.SessionID, next.Metrics)
	if !next.Metrics.IdlePromptPending {
		t.Error("held signal must re-apply on the next pass")
	}
}

func idlePromptClearedOnNewTurn(t *testing.T) {
	d := &SessionDetector{signals: session.NewSignalHolds()}
	d.signals.Hold(idlePromptSID, session.SignalIdlePrompt, session.SignalPayload{})
	// New user activity: not turn_done → IsAgentDone false.
	state := &session.SessionState{SessionID: idlePromptSID, Metrics: &session.SessionMetrics{LastEventType: "user"}}

	d.signals.Overlay(state.SessionID, state.Metrics)
	if state.Metrics.IdlePromptPending {
		t.Error("IdlePromptPending must NOT be set once a new turn started")
	}
	if d.signals.Held(idlePromptSID, session.SignalIdlePrompt) {
		t.Error("signal must be dropped when the idle window ends")
	}
}

func idlePromptClearedByOpenTool(t *testing.T) {
	d := &SessionDetector{signals: session.NewSignalHolds()}
	d.signals.Hold(idlePromptSID, session.SignalIdlePrompt, session.SignalPayload{})
	// An open tool call makes IsAgentDone false — the open-tool rules own
	// that case, not idle-prompt.
	state := &session.SessionState{SessionID: idlePromptSID, Metrics: &session.SessionMetrics{
		LastEventType:   "turn_done",
		HasOpenToolCall: true,
	}}

	d.signals.Overlay(state.SessionID, state.Metrics)
	if state.Metrics.IdlePromptPending {
		t.Error("IdlePromptPending must NOT be set while a tool call is open")
	}
	if d.signals.Held(idlePromptSID, session.SignalIdlePrompt) {
		t.Error("signal must be dropped when an open tool blocks the turn")
	}
}

func idlePromptNoSignalIsNoOp(t *testing.T) {
	d := &SessionDetector{signals: session.NewSignalHolds()}
	state := &session.SessionState{SessionID: idlePromptSID, Metrics: idleDoneMetrics()}
	d.signals.Overlay(state.SessionID, state.Metrics)
	if state.Metrics.IdlePromptPending {
		t.Error("a session with no pending idle-prompt signal must not be marked waiting")
	}
}

func idlePromptNilMetricsIsNoOp(t *testing.T) {
	d := &SessionDetector{signals: session.NewSignalHolds()}
	d.signals.Hold(idlePromptSID, session.SignalIdlePrompt, session.SignalPayload{})
	state := &session.SessionState{SessionID: idlePromptSID, Metrics: nil}
	d.signals.Overlay(state.SessionID, state.Metrics) // must not panic
	if !d.signals.Held(idlePromptSID, session.SignalIdlePrompt) {
		t.Error("nil metrics must leave the pending signal untouched")
	}
}

// TestHasPendingIdlePrompt covers the guard forceReadyToWorkingIfActive reads to
// skip the ready→working bounce on the idle-prompt hook's synthetic reclassify.
func TestHasPendingIdlePrompt(t *testing.T) {
	d := &SessionDetector{signals: session.NewSignalHolds()}
	d.signals.Hold("has", session.SignalIdlePrompt, session.SignalPayload{})
	if !d.hasPendingIdlePrompt("has") {
		t.Error("hasPendingIdlePrompt must report true for a session with a pending signal")
	}
	if d.hasPendingIdlePrompt("none") {
		t.Error("hasPendingIdlePrompt must report false for a session with no signal")
	}
}
