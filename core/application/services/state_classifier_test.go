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
			name:    "nil metrics, working stays working",
			current: session.StateWorking,
			metrics: nil,
			wantState: session.StateWorking,
		},
		{
			name:    "nil metrics, ready stays ready",
			current: session.StateReady,
			metrics: nil,
			wantState: session.StateReady,
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
			name:    "working → ready (assistant_message Codex fallback)",
			current: session.StateWorking,
			metrics: &session.SessionMetrics{
				LastEventType:   "assistant_message",
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

		// Rule 3: ESC cancellation → ready.
		{
			name:    "working → ready (ESC cancellation)",
			current: session.StateWorking,
			metrics: &session.SessionMetrics{
				LastEventType:          "user",
				HasOpenToolCall:        false,
				LastToolResultWasError: true,
			},
			wantState:  session.StateReady,
			wantReason: true,
		},
		{
			name:    "waiting → ready (ESC cancellation)",
			current: session.StateWaiting,
			metrics: &session.SessionMetrics{
				LastEventType:          "user",
				HasOpenToolCall:        false,
				LastToolResultWasError: true,
			},
			wantState:  session.StateReady,
			wantReason: true,
		},
		{
			name:    "normal tool completion stays working (is_error=false)",
			current: session.StateWorking,
			metrics: &session.SessionMetrics{
				LastEventType:          "user",
				HasOpenToolCall:        false,
				LastToolResultWasError: false,
			},
			wantState: session.StateWorking,
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
				LastEventType:   "assistant",
				HasOpenToolCall: true,
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

func TestInferSubagents(t *testing.T) {
	tests := []struct {
		name    string
		metrics *session.SessionMetrics
		want    *session.SubagentSummary
	}{
		{
			name:    "nil metrics",
			metrics: nil,
			want:    nil,
		},
		{
			name:    "no open tools",
			metrics: &session.SessionMetrics{HasOpenToolCall: false},
			want:    nil,
		},
		{
			name: "open tools but no Agent",
			metrics: &session.SessionMetrics{
				HasOpenToolCall:   true,
				LastOpenToolNames: []string{"Bash", "Read"},
			},
			want: nil,
		},
		{
			name: "one Agent tool",
			metrics: &session.SessionMetrics{
				HasOpenToolCall:   true,
				LastOpenToolNames: []string{"Agent"},
			},
			want: &session.SubagentSummary{Total: 1, Working: 1},
		},
		{
			name: "multiple Agent tools",
			metrics: &session.SessionMetrics{
				HasOpenToolCall:   true,
				LastOpenToolNames: []string{"Agent", "Agent", "Bash"},
			},
			want: &session.SubagentSummary{Total: 2, Working: 2},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := InferSubagents(tt.metrics)
			if tt.want == nil {
				if got != nil {
					t.Errorf("InferSubagents() = %+v, want nil", got)
				}
				return
			}
			if got == nil {
				t.Fatal("InferSubagents() = nil, want non-nil")
			}
			if got.Total != tt.want.Total || got.Working != tt.want.Working {
				t.Errorf("InferSubagents() = %+v, want %+v", got, tt.want)
			}
		})
	}
}
