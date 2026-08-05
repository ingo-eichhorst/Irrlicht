package services

import (
	"testing"

	"irrlicht/core/domain/session"
)

// TestClassify_TranscriptPermissionPending pins the transcript-tier permission
// rule added for GitHub Copilot (#1256).
//
// The pre-existing permission rule reads SessionMetrics.PermissionPending,
// which only a hook can set (it is driven by the SignalPermissionPrompt hold,
// is json:"-", and is never populated under replay). An agent that writes its
// permission prompts into the transcript — Copilot emits permission.requested
// / permission.completed as ordinary events — therefore had no path to
// `waiting` at all. These cases assert the new path, including that it does
// NOT disturb the hook-tier rule or the turn-done verdict.
func TestClassify_TranscriptPermissionPending(t *testing.T) {
	tests := []struct {
		name      string
		current   string
		metrics   *session.SessionMetrics
		wantState string
		wantRule  string
	}{
		{
			// The core case: mid-turn, a tool call is open and the agent is
			// blocked on the user's answer. Without the rule this falls to
			// transcript_activity → working.
			name:    "working → waiting (transcript permission open)",
			current: session.StateWorking,
			metrics: &session.SessionMetrics{
				TranscriptPermissionPending: true,
				HasOpenToolCall:             true,
				LastOpenToolNames:           []string{"bash"},
				LastEventType:               "function_call",
			},
			wantState: session.StateWaiting,
			wantRule:  "transcript_permission_prompt",
		},
		{
			// Resolved: the user answered, the agent resumed. The flag clears
			// and ordinary activity resumes working.
			name:    "waiting → working (permission resolved)",
			current: session.StateWaiting,
			metrics: &session.SessionMetrics{
				TranscriptPermissionPending: false,
				HasOpenToolCall:             true,
				LastOpenToolNames:           []string{"bash"},
				LastEventType:               "function_call_output",
			},
			wantState: session.StateWorking,
			wantRule:  "transcript_activity",
		},
		{
			// An open prompt outranks a turn-done verdict: if the transcript
			// says the agent is still blocked, a stale turn_done must not
			// route the session to ready behind its back.
			name:    "turn_done does not beat an open permission prompt",
			current: session.StateWorking,
			metrics: &session.SessionMetrics{
				TranscriptPermissionPending: true,
				LastEventType:               "turn_done",
			},
			wantState: session.StateWaiting,
			wantRule:  "transcript_permission_prompt",
		},
		{
			// The hook-tier rule keeps precedence when both are set, so a
			// recorded trace still attributes the decision to the hook.
			name:    "hook permission rule still wins when both are set",
			current: session.StateWorking,
			metrics: &session.SessionMetrics{
				PermissionPending:           true,
				TranscriptPermissionPending: true,
			},
			wantState: session.StateWaiting,
			wantRule:  string(session.SignalPermissionPrompt),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			v := ClassifyStateTiered(tc.current, tc.metrics)
			if v.State != tc.wantState {
				t.Errorf("State = %q, want %q (reason %q)", v.State, tc.wantState, v.Reason)
			}
			if v.Rule != tc.wantRule {
				t.Errorf("Rule = %q, want %q", v.Rule, tc.wantRule)
			}
		})
	}
}

// TestTranscriptPermissionRule_IsTranscriptTier pins the rule's declared
// authority. It must NOT claim hook tier: the whole point of keeping it
// separate from PermissionPending is that a recorded lifecycle event stays
// able to say whether a waiting verdict was pushed by a hook or inferred from
// the transcript.
func TestTranscriptPermissionRule_IsTranscriptTier(t *testing.T) {
	v := ClassifyStateTiered(session.StateWorking, &session.SessionMetrics{
		TranscriptPermissionPending: true,
	})
	if v.Tier != session.TierTranscript {
		t.Errorf("Tier = %v, want %v (transcript-derived, not hook-pushed)", v.Tier, session.TierTranscript)
	}
}
