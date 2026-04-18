package services

import (
	"testing"

	"irrlicht/core/domain/session"
)

func TestClassifyState(t *testing.T) {
	tests := []struct {
		name       string
		current    string
		metrics    *session.SessionMetrics
		wantState  string
		wantReason bool // true if a transition reason is expected
	}{
		// Nil metrics — no transition.
		{
			name:      "nil metrics, working stays working",
			current:   session.StateWorking,
			metrics:   nil,
			wantState: session.StateWorking,
		},
		{
			name:      "nil metrics, ready stays ready",
			current:   session.StateReady,
			metrics:   nil,
			wantState: session.StateReady,
		},

		// Rule 0: PermissionPending → waiting.
		{
			name:    "working → waiting (permission pending)",
			current: session.StateWorking,
			metrics: &session.SessionMetrics{
				PermissionPending: true,
			},
			wantState:  session.StateWaiting,
			wantReason: true,
		},
		{
			name:    "waiting stays waiting (permission pending, already waiting)",
			current: session.StateWaiting,
			metrics: &session.SessionMetrics{
				PermissionPending: true,
			},
			wantState: session.StateWaiting,
		},
		{
			name:    "ready → waiting (permission pending)",
			current: session.StateReady,
			metrics: &session.SessionMetrics{
				PermissionPending: true,
			},
			wantState:  session.StateWaiting,
			wantReason: true,
		},
		{
			// Regression guard: Bash open without permission pending must NOT
			// trigger waiting — only the hook signal does.
			name:    "working stays working (Bash open, no permission pending)",
			current: session.StateWorking,
			metrics: &session.SessionMetrics{
				HasOpenToolCall:   true,
				LastOpenToolNames: []string{"Bash"},
				PermissionPending: false,
			},
			wantState: session.StateWorking,
		},

		// Rule 1: NeedsUserAttention → waiting.
		{
			name:    "working → waiting (AskUserQuestion)",
			current: session.StateWorking,
			metrics: &session.SessionMetrics{
				HasOpenToolCall:   true,
				LastOpenToolNames: []string{"AskUserQuestion"},
			},
			wantState:  session.StateWaiting,
			wantReason: true,
		},
		{
			name:    "ready → waiting (ExitPlanMode)",
			current: session.StateReady,
			metrics: &session.SessionMetrics{
				HasOpenToolCall:   true,
				LastOpenToolNames: []string{"ExitPlanMode"},
			},
			wantState:  session.StateWaiting,
			wantReason: true,
		},
		{
			name:    "waiting stays waiting (already waiting)",
			current: session.StateWaiting,
			metrics: &session.SessionMetrics{
				HasOpenToolCall:   true,
				LastOpenToolNames: []string{"AskUserQuestion"},
			},
			wantState: session.StateWaiting,
		},

		// Rule 2a: Turn ended with question → waiting.
		{
			name:    "working → waiting (turn_done + question)",
			current: session.StateWorking,
			metrics: &session.SessionMetrics{
				LastEventType:     "turn_done",
				HasOpenToolCall:   false,
				LastAssistantText: "Should I proceed with the migration?",
			},
			wantState:  session.StateWaiting,
			wantReason: true,
		},
		{
			name:    "ready → waiting (turn_done + question)",
			current: session.StateReady,
			metrics: &session.SessionMetrics{
				LastEventType:     "turn_done",
				HasOpenToolCall:   false,
				LastAssistantText: "Do you want me to fix this?",
			},
			wantState:  session.StateWaiting,
			wantReason: true,
		},
		{
			name:    "waiting stays waiting (turn_done + question, already waiting)",
			current: session.StateWaiting,
			metrics: &session.SessionMetrics{
				LastEventType:     "turn_done",
				HasOpenToolCall:   false,
				LastAssistantText: "Which approach do you prefer?",
			},
			wantState: session.StateWaiting,
		},

		// Rule 2b: IsAgentDone without question → ready.
		{
			name:    "working → ready (turn_done, no question)",
			current: session.StateWorking,
			metrics: &session.SessionMetrics{
				LastEventType:     "turn_done",
				HasOpenToolCall:   false,
				LastAssistantText: "Done. The tests pass.",
			},
			wantState:  session.StateReady,
			wantReason: true,
		},
		{
			name:    "working → ready (turn_done, empty text)",
			current: session.StateWorking,
			metrics: &session.SessionMetrics{
				LastEventType:   "turn_done",
				HasOpenToolCall: false,
			},
			wantState:  session.StateReady,
			wantReason: true,
		},
		{
			name:    "waiting → ready (turn_done)",
			current: session.StateWaiting,
			metrics: &session.SessionMetrics{
				LastEventType:   "turn_done",
				HasOpenToolCall: false,
			},
			wantState:  session.StateReady,
			wantReason: true,
		},
		{
			name:    "ready stays ready (turn_done, no transition)",
			current: session.StateReady,
			metrics: &session.SessionMetrics{
				LastEventType:   "turn_done",
				HasOpenToolCall: false,
			},
			wantState: session.StateReady,
		},
		{
			// Codex emits preliminary assistant_message events BEFORE tool
			// calls in the same turn — treating them as terminal would cause
			// working→ready→working flicker. The real terminal signal is
			// turn_done (from task_complete).
			name:    "working stays working (codex assistant_message is NOT terminal)",
			current: session.StateWorking,
			metrics: &session.SessionMetrics{
				LastEventType:   "assistant_message",
				HasOpenToolCall: false,
			},
			wantState: session.StateWorking,
		},
		{
			name:    "working → ready (codex turn_done)",
			current: session.StateWorking,
			metrics: &session.SessionMetrics{
				LastEventType:   "turn_done",
				HasOpenToolCall: false,
			},
			wantState:  session.StateReady,
			wantReason: true,
		},
		{
			name:    "working → ready (assistant with stop_reason)",
			current: session.StateWorking,
			metrics: &session.SessionMetrics{
				LastEventType:   "assistant",
				HasOpenToolCall: false,
			},
			wantState:  session.StateReady,
			wantReason: true,
		},
		{
			name:    "working stays working (assistant_streaming, no stop_reason)",
			current: session.StateWorking,
			metrics: &session.SessionMetrics{
				LastEventType:   "assistant_streaming",
				HasOpenToolCall: false,
			},
			wantState: session.StateWorking,
		},

		// Rule 3: ESC cancellation → ready. The signal is LastWasUserInterrupt
		// (the exact "[Request interrupted by user]" text marker), NOT
		// LastToolResultWasError (issue #102 Bug B), and NOT LastWasToolDenial
		// (the "for tool use" suffix variant — denial doesn't end the turn,
		// see the parser-level split in claudecode/parser.go).
		{
			name:    "working → ready (ESC cancellation)",
			current: session.StateWorking,
			metrics: &session.SessionMetrics{
				LastEventType:        "user",
				HasOpenToolCall:      false,
				LastWasUserInterrupt: true,
			},
			wantState:  session.StateReady,
			wantReason: true,
		},
		{
			name:    "waiting → ready (ESC cancellation)",
			current: session.StateWaiting,
			metrics: &session.SessionMetrics{
				LastEventType:        "user",
				HasOpenToolCall:      false,
				LastWasUserInterrupt: true,
			},
			wantState:  session.StateReady,
			wantReason: true,
		},
		{
			name:    "user event without interrupt stays working",
			current: session.StateWorking,
			metrics: &session.SessionMetrics{
				LastEventType:   "user",
				HasOpenToolCall: false,
			},
			wantState: session.StateWorking,
		},
		{
			// Tool denial triggers ready — Claude Code returns to the prompt
			// after a denial. If the agent does continue, the next transcript
			// activity will transition back to working.
			name:    "working → ready on tool denial (LastWasToolDenial)",
			current: session.StateWorking,
			metrics: &session.SessionMetrics{
				LastEventType:     "user",
				HasOpenToolCall:   false,
				LastWasToolDenial: true,
			},
			wantState:  session.StateReady,
			wantReason: true,
		},

		// Rule 4: Default → working.
		{
			name:    "ready → working (activity)",
			current: session.StateReady,
			metrics: &session.SessionMetrics{
				LastEventType:   "user",
				HasOpenToolCall: false,
			},
			wantState:  session.StateWorking,
			wantReason: true,
		},
		{
			name:    "working stays working (no transition needed)",
			current: session.StateWorking,
			metrics: &session.SessionMetrics{
				LastEventType:     "assistant",
				HasOpenToolCall:   true,
				LastOpenToolNames: []string{"Bash"},
			},
			wantState: session.StateWorking,
		},
	}

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

// TestShouldSynthesizeCollapsedWaiting covers issue #150: a user-blocking
// tool (AskUserQuestion / ExitPlanMode) whose tool_use and tool_result
// land in the same tailer pass skips the natural working→waiting
// transition. The helper decides whether the caller should emit a
// synthetic one.
func TestShouldSynthesizeCollapsedWaiting(t *testing.T) {
	tests := []struct {
		name    string
		current string
		newS    string
		metrics *session.SessionMetrics
		want    bool
	}{
		{
			name:    "Case A: collapsed + denial → rule 3 returns ready",
			current: session.StateWorking,
			newS:    session.StateReady,
			metrics: &session.SessionMetrics{SawUserBlockingToolClosedThisPass: true},
			want:    true,
		},
		{
			name:    "Case B: collapsed with cleared denial → rule 4 returns working",
			current: session.StateWorking,
			newS:    session.StateWorking,
			metrics: &session.SessionMetrics{SawUserBlockingToolClosedThisPass: true},
			want:    true,
		},
		{
			name:    "no synthesis when classifier already returns waiting (natural path)",
			current: session.StateWorking,
			newS:    session.StateWaiting,
			metrics: &session.SessionMetrics{SawUserBlockingToolClosedThisPass: true},
			want:    false,
		},
		{
			name:    "no synthesis when no user-blocking tool closed",
			current: session.StateWorking,
			newS:    session.StateReady,
			metrics: &session.SessionMetrics{SawUserBlockingToolClosedThisPass: false},
			want:    false,
		},
		{
			name:    "no synthesis from waiting state (cross-pass tool_result — waiting already emitted)",
			current: session.StateWaiting,
			newS:    session.StateReady,
			metrics: &session.SessionMetrics{SawUserBlockingToolClosedThisPass: true},
			want:    false,
		},
		{
			name:    "no synthesis from ready state (force-r2w flips ready to working BEFORE this check)",
			current: session.StateReady,
			newS:    session.StateReady,
			metrics: &session.SessionMetrics{SawUserBlockingToolClosedThisPass: true},
			want:    false,
		},
		{
			name:    "nil metrics — no synthesis",
			current: session.StateWorking,
			newS:    session.StateReady,
			metrics: nil,
			want:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ShouldSynthesizeCollapsedWaiting(tt.current, tt.newS, tt.metrics); got != tt.want {
				t.Errorf("ShouldSynthesizeCollapsedWaiting(%q, %q) = %v, want %v",
					tt.current, tt.newS, got, tt.want)
			}
		})
	}
}

