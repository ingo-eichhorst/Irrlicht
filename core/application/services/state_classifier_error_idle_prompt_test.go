package services

import (
	"testing"

	"irrlicht/core/domain/session"
)

// TestClassify_TerminalUsageLimitOutranksIdlePrompt is the regression test
// for #1871. Claude Code reports the terminal HTTP 429 first, then sends an
// idle_prompt notification about a minute later. That notification is not a
// recovery event, so it must not mask the standing failure with waiting.
func TestClassify_TerminalUsageLimitOutranksIdlePrompt(t *testing.T) {
	status := 429
	v := ClassifyStateTiered(session.StateError, &session.SessionMetrics{
		LastEventType:     "turn_done",
		IdlePromptPending: true,
		SessionError: &session.SessionError{
			Phase:      session.ErrorPhaseTerminal,
			Class:      "rate_limit",
			HTTPStatus: &status,
		},
	})

	if v.State != session.StateError {
		t.Errorf("state = %q, want error — idle_prompt is not recovery from a terminal usage limit", v.State)
	}
	if v.Rule != string(session.SignalSessionError) {
		t.Errorf("rule = %q, want %q", v.Rule, session.SignalSessionError)
	}
}

// TestClassify_IdlePromptDoesNotPromoteStatementTurnToWaiting is the #2176
// defect test. A finished turn that ends on a plain statement classifies ready
// whether or not the idle_prompt hook has landed: the 60 s idle notification
// says the prompt is idle, not that the user is asked anything.
func TestClassify_IdlePromptDoesNotPromoteStatementTurnToWaiting(t *testing.T) {
	v := ClassifyStateTiered(session.StateReady, &session.SessionMetrics{
		LastEventType:     "turn_done",
		IdlePromptPending: true,
		LastAssistantText: "Done. All tests pass.",
	})
	if v.State != session.StateReady {
		t.Errorf("state = %q, want ready — idle_prompt must not promote a statement-ending turn", v.State)
	}
}

// TestClassify_IdlePromptAttributesWaitingTurn is a lock: a turn that already
// reads waiting (a trailing question) keeps waiting under idle_prompt, now
// attributed to the hook rule as provenance.
func TestClassify_IdlePromptAttributesWaitingTurn(t *testing.T) {
	v := ClassifyStateTiered(session.StateReady, &session.SessionMetrics{
		LastEventType:     "turn_done",
		IdlePromptPending: true,
		LastAssistantText: "Which option do you want?",
	})
	if v.State != session.StateWaiting {
		t.Errorf("state = %q, want waiting", v.State)
	}
	if v.Rule != string(session.SignalIdlePrompt) {
		t.Errorf("rule = %q, want %q", v.Rule, session.SignalIdlePrompt)
	}
}
