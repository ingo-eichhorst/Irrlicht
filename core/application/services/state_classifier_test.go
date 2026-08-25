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

		// the permission_prompt rule: PermissionPending → waiting.
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
		// the transcript_permission_prompt rule: an adapter whose permission gate is
		// visible in the TRANSCRIPT rather than delivered by a hook (#1256 —
		// GitHub Copilot pairs permission.requested/permission.completed on
		// requestId, so it needs no hook install to reach waiting).
		{
			name:    "working → waiting (transcript permission pending)",
			current: session.StateWorking,
			metrics: &session.SessionMetrics{
				TranscriptPermissionPending: true,
			},
			wantState:  session.StateWaiting,
			wantReason: true,
		},
		{
			name:    "ready → waiting (transcript permission pending)",
			current: session.StateReady,
			metrics: &session.SessionMetrics{
				TranscriptPermissionPending: true,
			},
			wantState:  session.StateWaiting,
			wantReason: true,
		},
		{
			// PRECEDENCE, and the reason this rule sits ABOVE agent_done in the
			// ladder rather than below it. An agent may write a turn-ending event
			// before it blocks on the prompt — Copilot does exactly that — so if
			// agent_done won here the session would report as finished while it
			// is actually waiting on the user. This case is what makes the
			// ordering load-bearing rather than incidental.
			name:    "open transcript prompt outranks a finished turn",
			current: session.StateWorking,
			metrics: &session.SessionMetrics{
				TranscriptPermissionPending: true,
				LastEventType:               "turn_done",
			},
			wantState:  session.StateWaiting,
			wantReason: true,
		},
		{
			// The other direction: once the prompt is answered the flag clears and
			// the same finished turn settles normally. Without this, the rule above
			// would also pass if the classifier simply never routed to ready.
			name:    "resolved transcript prompt lets the finished turn settle",
			current: session.StateWorking,
			metrics: &session.SessionMetrics{
				TranscriptPermissionPending: false,
				LastEventType:               "turn_done",
			},
			wantState:  session.StateReady,
			wantReason: true,
		},
		// the compact_in_progress rule: CompactInProgress (manual /compact) → working, holding the
		// session busy through the silent compaction window (#657).
		{
			name:    "ready → working (compact in progress)",
			current: session.StateReady,
			metrics: &session.SessionMetrics{
				CompactInProgress: true,
				// The pre-compact turn_done that would otherwise read as ready.
				LastEventType: "turn_done",
			},
			wantState:  session.StateWorking,
			wantReason: true,
		},
		{
			name:    "working stays working (compact in progress, already working)",
			current: session.StateWorking,
			metrics: &session.SessionMetrics{
				CompactInProgress: true,
				LastEventType:     "turn_done",
			},
			wantState: session.StateWorking,
		},
		{
			// Once the boundary lands the detector clears CompactInProgress, so
			// the same turn_done metrics route to ready via the agent_done rule (#656).
			name:    "working → ready (compact cleared, turn_done)",
			current: session.StateWorking,
			metrics: &session.SessionMetrics{
				CompactInProgress: false,
				LastEventType:     "turn_done",
			},
			wantState:  session.StateReady,
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

		// the open_tool_stalled rule: OpenToolStalled → waiting (transcript fallback, #488).
		{
			name:    "working → waiting (stalled edit tool)",
			current: session.StateWorking,
			metrics: &session.SessionMetrics{
				HasOpenToolCall:   true,
				LastOpenToolNames: []string{"Edit"},
				OpenToolStalled:   true,
			},
			wantState:  session.StateWaiting,
			wantReason: true,
		},
		{
			name:    "waiting stays waiting (stalled edit tool, already waiting)",
			current: session.StateWaiting,
			metrics: &session.SessionMetrics{
				HasOpenToolCall:   true,
				LastOpenToolNames: []string{"Write"},
				OpenToolStalled:   true,
			},
			wantState: session.StateWaiting,
		},
		{
			// Regression guard: an open edit tool the detector has NOT yet
			// flagged stalled must stay working (no premature waiting flicker).
			name:    "working stays working (edit open, not stalled)",
			current: session.StateWorking,
			metrics: &session.SessionMetrics{
				HasOpenToolCall:   true,
				LastOpenToolNames: []string{"Edit"},
				OpenToolStalled:   false,
			},
			wantState: session.StateWorking,
		},

		// the idle_prompt rule: IdlePromptPending (Notification/idle_prompt hook) → waiting.
		{
			name:    "ready → waiting (idle prompt hook)",
			current: session.StateReady,
			metrics: &session.SessionMetrics{
				IdlePromptPending: true,
			},
			wantState:  session.StateWaiting,
			wantReason: true,
		},
		{
			name:    "working → waiting (idle prompt hook)",
			current: session.StateWorking,
			metrics: &session.SessionMetrics{
				IdlePromptPending: true,
			},
			wantState:  session.StateWaiting,
			wantReason: true,
		},
		{
			name:    "waiting stays waiting (idle prompt hook, already waiting)",
			current: session.StateWaiting,
			metrics: &session.SessionMetrics{
				IdlePromptPending: true,
			},
			wantState: session.StateWaiting,
		},
		{
			// The core correction: a turn that ended on a plain statement (no
			// question/cue) would route to ready via the agent_done rule, but the idle-prompt
			// hook overrides it to waiting — the false-negative gap #1173 closes.
			name:    "ready → waiting (idle prompt overrides turn-done ready verdict)",
			current: session.StateReady,
			metrics: &session.SessionMetrics{
				IdlePromptPending: true,
				LastEventType:     "turn_done",
				LastAssistantText: "Done. All tests pass.",
			},
			wantState:  session.StateWaiting,
			wantReason: true,
		},
		{
			// Regression guard: with no idle-prompt signal, the same plain
			// turn-done metrics still route to ready (the idle_prompt rule is inert without
			// the live hook — no behavior change for the non-hook path).
			name:    "working → ready (turn done, no idle prompt, no cue)",
			current: session.StateWorking,
			metrics: &session.SessionMetrics{
				IdlePromptPending: false,
				LastEventType:     "turn_done",
				LastAssistantText: "Done. All tests pass.",
			},
			wantState:  session.StateReady,
			wantReason: true,
		},

		// the user_blocking_tool rule: NeedsUserAttention → waiting.
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
		// Rule 2a (issue #381): turn ended with an imperative cue (no `?`)
		// still routes to waiting via ExtractWaitingCue.
		{
			name:    "working → waiting (turn_done + imperative cue)",
			current: session.StateWorking,
			metrics: &session.SessionMetrics{
				LastEventType:     "turn_done",
				HasOpenToolCall:   false,
				LastAssistantText: "Take a look at the icon and let me know if it's right before I commit.",
			},
			wantState:  session.StateWaiting,
			wantReason: true,
		},
		// Rule 2a (issue #1138): an explicit irrlicht-question marker routes to
		// waiting even when the (tail-truncated) LastAssistantText carries no
		// question or cue — the real question sat earlier in a long final
		// message. Reproduces the 71f27332 session's shape.
		{
			name:    "working → waiting (turn_done + question marker, declarative tail)",
			current: session.StateWorking,
			metrics: &session.SessionMetrics{
				LastEventType:         "turn_done",
				HasOpenToolCall:       false,
				LastAssistantText:     "I can stand up the throwaway OTLP sink and drive a session through a permission prompt to capture the payload.",
				PendingQuestionMarker: true,
			},
			wantState:  session.StateWaiting,
			wantReason: true,
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
		// Rule 2b guard (issue #1138): QuestionHeadline is populated from the
		// LastAssistantText fallback on nearly every turn, so it must NOT by
		// itself force waiting — only the real PendingQuestionMarker does.
		{
			name:    "working → ready (turn_done, QuestionHeadline set but no marker)",
			current: session.StateWorking,
			metrics: &session.SessionMetrics{
				LastEventType:     "turn_done",
				HasOpenToolCall:   false,
				LastAssistantText: "Done. The tests pass.",
				QuestionHeadline:  "Done. The tests pass.",
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

		// the agent_done rule via the Stop hook (#1161): HookTurnDone is authoritative even
		// when the transcript-tail signal (LastEventType) hasn't landed yet.
		{
			name:    "working → ready (hook Stop, no cue, no turn_done)",
			current: session.StateWorking,
			metrics: &session.SessionMetrics{
				HookTurnDone:      true,
				LastEventType:     "assistant_streaming", // not a transcript-done signal
				LastAssistantText: "Done. The tests pass.",
			},
			wantState:  session.StateReady,
			wantReason: true,
		},
		{
			name:    "working → waiting (hook Stop carried a waiting cue)",
			current: session.StateWorking,
			metrics: &session.SessionMetrics{
				HookTurnDone:      true,
				PendingWaitingCue: true,
				LastAssistantText: "Which option do you prefer?",
			},
			wantState:  session.StateWaiting,
			wantReason: true,
		},
		{
			// A Stop that fires while a sub-agent tool is still open must NOT
			// flip the session to ready — the open-tool guard in IsAgentDone
			// wins over HookTurnDone.
			name:    "working stays working (hook Stop but tool still open)",
			current: session.StateWorking,
			metrics: &session.SessionMetrics{
				HookTurnDone:    true,
				HasOpenToolCall: true,
			},
			wantState: session.StateWorking,
		},
		{
			// Likewise a live background process (Bash run_in_background)
			// outlives the turn — HookTurnDone does not override it (#445).
			name:    "working stays working (hook Stop but live background process)",
			current: session.StateWorking,
			metrics: &session.SessionMetrics{
				HookTurnDone:             true,
				HasLiveBackgroundProcess: true,
			},
			wantState: session.StateWorking,
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

		// the user_interrupt rule: ESC cancellation → ready. The signal is LastWasUserInterrupt
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

		// the transcript_activity rule: Default → working.
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
			name:    "Case A: collapsed + denial → the user_interrupt rule returns ready",
			current: session.StateWorking,
			newS:    session.StateReady,
			metrics: &session.SessionMetrics{SawUserBlockingToolClosedThisPass: true},
			want:    true,
		},
		{
			name:    "Case B: collapsed with cleared denial → the transcript_activity rule returns working",
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

// TestShouldSynthesizeCollapsedTurnBoundary covers issue #988's gate
// function — the batch-scan analog of TestShouldSynthesizeCollapsedWaiting
// (#150) for a mid-pass turn_done boundary instead of a user-blocking tool.
func TestShouldSynthesizeCollapsedTurnBoundary(t *testing.T) {
	tests := []struct {
		name    string
		current string
		metrics *session.SessionMetrics
		want    bool
	}{
		{
			name:    "collapsed queued turn while working → synthesize",
			current: session.StateWorking,
			metrics: &session.SessionMetrics{SawMidPassTurnBoundary: true},
			want:    true,
		},
		{
			name:    "no synthesis when no mid-pass boundary was seen",
			current: session.StateWorking,
			metrics: &session.SessionMetrics{SawMidPassTurnBoundary: false},
			want:    false,
		},
		{
			name:    "no synthesis from ready state (force-r2w flips ready to working BEFORE this check)",
			current: session.StateReady,
			metrics: &session.SessionMetrics{SawMidPassTurnBoundary: true},
			want:    false,
		},
		{
			name:    "no synthesis from waiting state",
			current: session.StateWaiting,
			metrics: &session.SessionMetrics{SawMidPassTurnBoundary: true},
			want:    false,
		},
		{
			name:    "no synthesis while a real permission prompt is pending",
			current: session.StateWorking,
			metrics: &session.SessionMetrics{SawMidPassTurnBoundary: true, PermissionPending: true},
			want:    false,
		},
		{
			name:    "no synthesis while a manual compact is in progress",
			current: session.StateWorking,
			metrics: &session.SessionMetrics{SawMidPassTurnBoundary: true, CompactInProgress: true},
			want:    false,
		},
		{
			name:    "nil metrics — no synthesis",
			current: session.StateWorking,
			metrics: nil,
			want:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ShouldSynthesizeCollapsedTurnBoundary(tt.current, tt.metrics); got != tt.want {
				t.Errorf("ShouldSynthesizeCollapsedTurnBoundary(%q) = %v, want %v",
					tt.current, got, tt.want)
			}
		})
	}
}

// TestClassify_SessionErrorRoutesToError is the direct behavioural test of
// #1798's rule: a session carrying an unrecovered failure classifies to
// StateError, and one that does not carry one is unaffected.
//
// TestStateRules_LadderIsTierConsistent covers the rule's PLACEMENT as a
// property; this covers its OUTCOME, which that test deliberately says
// nothing about — it checks tier ordering, not which state won.
func TestClassify_SessionErrorRoutesToError(t *testing.T) {
	tests := []struct {
		name    string
		current string
		metrics *session.SessionMetrics
		want    string
	}{
		{
			// The headline case: a terminal failure whose transcript tail
			// reads exactly like a finished turn. Without the rule sitting
			// above agent_done this is `ready` — a failed session painted
			// green, which is the defect the fourth state exists to fix.
			name:    "terminal error on a turn that looks finished → error, not ready",
			current: session.StateWorking,
			metrics: &session.SessionMetrics{
				LastEventType: "turn_done",
				SessionError:  &session.SessionError{Phase: session.ErrorPhaseTerminal, Class: "provider"},
			},
			want: session.StateError,
		},
		{
			name:    "retry in progress → error",
			current: session.StateWorking,
			metrics: &session.SessionMetrics{
				LastEventType: "assistant",
				SessionError:  &session.SessionError{Phase: session.ErrorPhaseRetrying, Class: "rate_limit"},
			},
			want: session.StateError,
		},
		{
			// A failure with no phase reported is still a failure. Real case:
			// copilot's errorType "query" says nothing about retrying.
			name:    "unknown phase is still an error",
			current: session.StateReady,
			metrics: &session.SessionMetrics{
				LastEventType: "assistant",
				SessionError:  &session.SessionError{Class: "query", Message: "could not connect"},
			},
			want: session.StateError,
		},
		{
			// #1798 wrote this case as `→ ready`, reading a Stop hook as
			// "the turn completed, so the failure is over". #1799 flipped it,
			// because the first real producers showed the premise is false:
			// BOTH of them emit their terminal failure inside a turn that then
			// ends normally, so the Stop hook fires for the very turn that
			// failed. claudecode writes `system`/`turn_duration` on the line
			// after its "API Error: …" message; copilot writes
			// `assistant.turn_end` ~6ms BEFORE its `session.error`.
			//
			// A Stop hook says the turn ENDED. It does not say the turn
			// SUCCEEDED, and nothing else in the hook payload can. Reading it
			// as a recovery painted the epic's headline scenario green on any
			// machine with hooks installed — which is every default install.
			//
			// The error is retired instead by the tailer, under the same
			// ClearedByTurnBoundary rule the transcript arm uses, so a
			// SessionError still standing when agent_done decides is one no
			// turn boundary retires. See classifyAgentDone.
			name:    "hook-delivered turn done on a FAILED turn → error, not ready",
			current: session.StateError,
			metrics: &session.SessionMetrics{
				LastEventType: "assistant_message",
				HookTurnDone:  true,
				SessionError:  &session.SessionError{Phase: session.ErrorPhaseTerminal, Class: "provider"},
			},
			want: session.StateError,
		},
		{
			// Same shape with no phase reported — copilot's `query`. An
			// absence of information about a further attempt is not evidence
			// the turn recovered, so this must not be the case that leaks
			// green.
			name:    "hook-delivered turn done with an unknown-phase error → error",
			current: session.StateWorking,
			metrics: &session.SessionMetrics{
				LastEventType: "assistant_message",
				HookTurnDone:  true,
				SessionError:  &session.SessionError{Class: "query", Message: "could not connect"},
			},
			want: session.StateError,
		},
		{
			// The genuine recovery: a retrying error whose turn then
			// completed. The tailer's ClearedByTurnBoundary retires it before
			// the classifier ever sees it, so what reaches here is a nil
			// SessionError and agent_done answers ready — the arm below is
			// what proves this path is not accidentally red.
			name:    "a retry that completed leaves no error → ready",
			current: session.StateError,
			metrics: &session.SessionMetrics{
				LastEventType: "assistant_message",
				HookTurnDone:  true,
			},
			want: session.StateReady,
		},
		{
			// An open permission prompt outranks a past failure: the session
			// is blocked on a human right now, which is actionable, while the
			// error is not.
			name:    "an open permission prompt still outranks an error",
			current: session.StateError,
			metrics: &session.SessionMetrics{
				LastEventType:     "assistant",
				PermissionPending: true,
				SessionError:      &session.SessionError{Phase: session.ErrorPhaseTerminal},
			},
			want: session.StateWaiting,
		},
		{
			// The tailer cleared the error (the transcript half of the rule),
			// so the ladder never sees one and the ordinary verdict stands.
			name:    "no error → unchanged verdict",
			current: session.StateWorking,
			metrics: &session.SessionMetrics{LastEventType: "turn_done"},
			want:    session.StateReady,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, _ := ClassifyState(tt.current, tt.metrics)
			if got != tt.want {
				t.Errorf("ClassifyState(%q) = %q, want %q", tt.current, got, tt.want)
			}
		})
	}
}

// TestClassify_SessionErrorIsAttributedToItsRule pins the provenance a
// recorded trace needs: the verdict must name the session_error rule and its
// tier, not merely land on the right state. Two rules could produce StateError
// in future (#1800 adds a process-death producer at TierProcess), and a
// recording has to say which one decided.
func TestClassify_SessionErrorIsAttributedToItsRule(t *testing.T) {
	v := ClassifyStateTiered(session.StateWorking, &session.SessionMetrics{
		LastEventType: "turn_done",
		SessionError:  &session.SessionError{Phase: session.ErrorPhaseTerminal, Class: "provider"},
	})

	if v.State != session.StateError {
		t.Fatalf("state = %q, want error", v.State)
	}
	if v.Rule != string(session.SignalSessionError) {
		t.Errorf("rule = %q, want %q", v.Rule, session.SignalSessionError)
	}
	if v.Tier != session.TierTranscript {
		t.Errorf("tier = %v, want TierTranscript — a transcript-derived failure must not "+
			"claim hook authority", v.Tier)
	}
	if v.Reason == "" {
		t.Error("an error transition must carry a reason")
	}
}
