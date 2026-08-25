package codex

import (
	"testing"

	"irrlicht/core/pkg/tailer"
)

func codexTurnAborted(reason string) map[string]interface{} {
	return map[string]interface{}{
		"timestamp": "2026-05-16T20:54:07.895Z",
		"type":      "event_msg",
		"payload": map[string]interface{}{
			"type":         "turn_aborted",
			"turn_id":      "019e3291-4d64-7e80-b513-d0a57d8169c1",
			"reason":       reason,
			"completed_at": float64(1778964847),
			"duration_ms":  float64(4011),
		},
	}
}

// LOCK — passes by construction, and must keep passing. reason:"interrupted"
// is the ONLY value any committed codex fixture carries (census: 4 events,
// 4 × "interrupted"), and it is a user ESC, not a failure.
func TestParser_TurnAbortedInterrupted_IsNotASessionError(t *testing.T) {
	p := &Parser{}
	ev := p.ParseLine(codexTurnAborted("interrupted"))
	if ev == nil {
		t.Fatal("ParseLine returned nil")
	}
	if ev.EventType != "turn_done" {
		t.Errorf("EventType = %q, want \"turn_done\" — unchanged", ev.EventType)
	}
	if ev.SessionError != nil {
		t.Errorf("a user ESC must not be a session error, got %+v", ev.SessionError)
	}
}

// The defect: reason is never read, so an errored abort and an ESC are the
// same event. No recorded fixture carries a non-"interrupted" reason (see the
// census in the PR body), so this case is hand-built on purpose and the test
// says so rather than implying fixture backing.
func TestParser_TurnAbortedError_IsASessionError(t *testing.T) {
	p := &Parser{}
	ev := p.ParseLine(codexTurnAborted("error"))
	if ev == nil {
		t.Fatal("ParseLine returned nil")
	}
	if ev.EventType != "turn_done" {
		t.Errorf("EventType = %q, want \"turn_done\"", ev.EventType)
	}
	if ev.SessionError == nil {
		t.Fatal("SessionError is nil — payload.reason is never read, so an errored " +
			"abort is indistinguishable from an ESC interrupt")
	}
	if ev.SessionError.Phase != tailer.ErrorPhaseTerminal {
		t.Errorf("Phase = %q, want terminal", ev.SessionError.Phase)
	}
}

// An unrecognized future reason must not be silently swallowed as a clean
// turn: codex is free to add values, and "not interrupted" is the honest read.
func TestParser_TurnAbortedUnknownReason_IsASessionError(t *testing.T) {
	p := &Parser{}
	ev := p.ParseLine(codexTurnAborted("some_future_reason"))
	if ev == nil || ev.SessionError == nil {
		t.Fatal("an abort for an unrecognized reason must still report a session error")
	}
}
