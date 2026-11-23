package services

import (
	"testing"

	"irrlicht/core/domain/session"
)

// TestClassifyState_IdlePromptRule covers the idle_prompt rule's table cases,
// split out of TestClassifyState (#2176): the hook gives hook-tier provenance
// to a turn that already reads waiting and never promotes a finished
// statement-ending turn (#1173 used to).
func TestClassifyState_IdlePromptRule(t *testing.T) {
	tests := []classifyStateCase{
		// the idle_prompt rule: IdlePromptPending (Notification/idle_prompt hook)
		// gives hook-tier provenance to a turn that already reads waiting, and
		// never promotes a finished statement-ending turn (#2176; #1173 used to).
		{
			// Flipped by #2176. Was: empty metrics + IdlePromptPending, ready → waiting.
			// The hook alone no longer says waiting; the turn must read waiting too.
			name:    "ready → waiting (idle prompt hook, turn ended on a question)",
			current: session.StateReady,
			metrics: &session.SessionMetrics{
				IdlePromptPending: true,
				LastEventType:     "turn_done",
				LastAssistantText: "Which option do you want?",
			},
			wantState:  session.StateWaiting,
			wantReason: true,
		},
		{
			// Flipped by #2176. Was: working → waiting on the hook alone. The turn
			// is not done, so the hook is inert and the transcript verdict stands.
			name:    "working stays working (idle prompt hook, turn not done)",
			current: session.StateWorking,
			metrics: &session.SessionMetrics{
				IdlePromptPending: true,
			},
			wantState: session.StateWorking,
		},
		{
			// Flipped by #2176 (metrics only): the already-waiting turn now has to
			// read waiting itself for the hook rule to hold it there.
			name:    "waiting stays waiting (idle prompt hook, already waiting)",
			current: session.StateWaiting,
			metrics: &session.SessionMetrics{
				IdlePromptPending: true,
				LastEventType:     "turn_done",
				LastAssistantText: "Which option do you want?",
			},
			wantState: session.StateWaiting,
		},
		{
			// Issue #1982: a live background process (Bash run_in_background,
			// or Claude Code's Monitor tool once registered in the same
			// ledger) wakes the agent by itself, so an idle_prompt hook that
			// arrives while it is still open must not promote the session to
			// waiting — the session must keep reading working for as long as
			// the process/task is alive. This is a deliberate behaviour
			// change from #1173's plain idle_prompt -> waiting rule, stated
			// in the PR body.
			name:    "working stays working (idle prompt hook suppressed by live background process)",
			current: session.StateWorking,
			metrics: &session.SessionMetrics{
				IdlePromptPending:        true,
				HasLiveBackgroundProcess: true,
			},
			wantState: session.StateWorking,
		},
		{
			// Flipped by #2176. Was "ready → waiting (idle prompt overrides turn-done
			// ready verdict)": #1173 let the hook promote a plain-statement turn.
			// It now stays ready — the 60 s idle notification asks nothing of the user.
			name:    "ready stays ready (idle prompt does not override a statement-ending turn)",
			current: session.StateReady,
			metrics: &session.SessionMetrics{
				IdlePromptPending: true,
				LastEventType:     "turn_done",
				LastAssistantText: "Done. All tests pass.",
			},
			wantState: session.StateReady,
		},
	}

	runClassifyStateCases(t, tests)
}

// classifyStateCase is one row of a ClassifyState table.
type classifyStateCase struct {
	name       string
	current    string
	metrics    *session.SessionMetrics
	wantState  string
	wantReason bool // true if a transition reason is expected
}

// runClassifyStateCases runs a ClassifyState table, shared by TestClassifyState
// and TestClassifyState_IdlePromptRule.
func runClassifyStateCases(t *testing.T, tests []classifyStateCase) {
	t.Helper()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotState, gotReason := ClassifyState(tt.current, tt.metrics)
			if gotState != tt.wantState {
				t.Errorf("ClassifyState(%q) state = %q, want %q", tt.current, gotState, tt.wantState)
			}
			if tt.wantReason && gotReason == "" {
				t.Error("expected a transition reason, got empty")
			}
			if !tt.wantReason && gotReason != "" {
				t.Errorf("expected no transition reason, got %q", gotReason)
			}
		})
	}
}
