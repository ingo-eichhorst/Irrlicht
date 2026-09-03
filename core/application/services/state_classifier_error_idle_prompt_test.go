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
