package session

import (
	"testing"
	"time"
)

// TestHookSignalTable pins the hook-name → SignalKind mapping that
// SessionDetector.HandlePermissionHook and the replay harness's applyHookEvent
// both read. Before #1320 each had its own copy and they had drifted: the
// harness ignored PreToolUse, which the daemon holds on.
//
// A lock, not a defect test — it passes on main by construction, because the
// daemon's side of the mapping was already correct. What it prevents is a
// future edit silently changing what a hook name means for one caller.
func TestHookSignalTable(t *testing.T) {
	tests := []struct {
		hookName    string
		wantSignal  SignalKind
		wantRelease bool
		wantOK      bool
	}{
		{HookPermissionRequest, SignalPermissionPrompt, false, true},
		{HookPreToolUse, SignalPermissionPrompt, false, true},
		{HookPostToolUse, SignalPermissionPrompt, true, true},
		{HookPostToolUseFailure, SignalPermissionPrompt, true, true},

		// Payload-ENRICHED rather than payload-gated (#1695): the name alone
		// determines the signal, and the payload only ever adds to it. A
		// caller holding this row's effect gets a correct-but-weaker verdict —
		// see hookSignalEffects, and
		// TestPayloadFreeTurnDoneNeverClearsATranscriptCue for the property
		// that makes "weaker" true rather than "wrong".
		{HookStop, SignalTurnDone, false, true},

		// Gated adapter-side: no recording in replaydata/ fires one yet.
		{HookNotification, "", false, false},
		{HookPreCompact, "", false, false},

		// Genuinely unknown.
		{"SessionStart", "", false, false},
		{"", "", false, false},
	}

	for _, tt := range tests {
		t.Run(tt.hookName, func(t *testing.T) {
			effect, ok := HookSignal(tt.hookName)
			if ok != tt.wantOK {
				t.Fatalf("HookSignal(%q) ok = %v, want %v", tt.hookName, ok, tt.wantOK)
			}
			if effect.Signal != tt.wantSignal {
				t.Errorf("HookSignal(%q).Signal = %q, want %q", tt.hookName, effect.Signal, tt.wantSignal)
			}
			if effect.Release != tt.wantRelease {
				t.Errorf("HookSignal(%q).Release = %v, want %v", tt.hookName, effect.Release, tt.wantRelease)
			}
		})
	}
}

// TestHookSignalEffectsReferenceKnownSignals guards the other end of the table:
// every row must name a SignalKind that actually has a policy, or the hold it
// creates would never be applied by Overlay.
func TestHookSignalEffectsReferenceKnownSignals(t *testing.T) {
	for hookName, effect := range hookSignalEffects {
		if TierOf(effect.Signal) == TierNone {
			t.Errorf("hook %q maps to SignalKind %q, which has no signalPolicies row",
				hookName, effect.Signal)
		}
	}
}

// TestPayloadFreeTurnDoneNeverClearsATranscriptCue is the property that makes
// HookStop safe as a hookSignalEffects row (#1695). The row hands a name-only
// caller a SignalTurnDone hold with an EMPTY SignalPayload, and the question
// that decides whether that is a floor or a lie is what an empty payload does
// to a cue the transcript already found: if it overwrote PendingWaitingCue and
// LastAssistantText, a turn that ended on a question would be forced to ready
// and the row would be actively wrong. It does not — signalPolicies' turn-done
// apply guards both fields — so the degradation is one-directional: the hook
// can push a finished turn to waiting, and can never pull one out of it.
//
// Seen red by deliberately unguarding that apply (`c.Metrics.PendingWaitingCue
// = c.Payload.WaitingCue`, and the same for LastAssistantText): the first arm
// fails on all three of its assertions, the second stays green — it locks the
// enrichment direction, which the mutation does not break.
//
// Stated rather than left implied, because it changes what this test is worth:
// the two GUARDS were already locked, by TestSignalHolds_TurnDonePreservesPriorText
// and TestSignalHolds_TurnDoneCueIsAdditive in signal_hold_test.go, and both go
// red on the same mutation. What is new here is the CONCLUSION those locks
// support and neither states — that after an empty-payload overlay the metrics
// still answer IsWaitingForUserInput() true — which is the whole of the
// argument for the HookStop row and is the assertion that would survive a
// future apply rewritten to reach the same fields another way.
func TestPayloadFreeTurnDoneNeverClearsATranscriptCue(t *testing.T) {
	const sid = "s1"
	at := time.Unix(1_700_000_000, 0)

	t.Run("cue found by the transcript survives an empty payload", func(t *testing.T) {
		h := NewSignalHolds()
		h.Hold(sid, SignalTurnDone, SignalPayload{}, at)

		m := &SessionMetrics{
			LastEventType:     "assistant_message",
			LastAssistantText: "Which branch should I target?",
			PendingWaitingCue: true,
		}
		h.Overlay(sid, m, at)

		if !m.HookTurnDone {
			t.Fatal("empty-payload turn-done hold did not set HookTurnDone: the row asserts nothing")
		}
		if !m.PendingWaitingCue {
			t.Error("empty payload cleared PendingWaitingCue: the row would force a question-ended turn to ready")
		}
		if m.LastAssistantText != "Which branch should I target?" {
			t.Errorf("empty payload overwrote LastAssistantText = %q, want the transcript's own text", m.LastAssistantText)
		}
		if !m.IsWaitingForUserInput() {
			t.Error("IsWaitingForUserInput() is false after an empty-payload turn-done overlay: the cue was lost")
		}
	})

	t.Run("payload can add a cue the transcript missed", func(t *testing.T) {
		h := NewSignalHolds()
		h.Hold(sid, SignalTurnDone, SignalPayload{
			LastAssistantText: "…and which branch should I target?",
			WaitingCue:        true,
		}, at)

		// The transcript tail carried no cue (#1150: the question sits before
		// the 200-rune tail). This is what the daemon's payload buys and what
		// a name-only caller gives up.
		m := &SessionMetrics{LastEventType: "assistant_message", LastAssistantText: "Done."}
		h.Overlay(sid, m, at)

		if !m.PendingWaitingCue {
			t.Error("payload WaitingCue did not reach the metrics: HandleStopHook's enrichment is a no-op")
		}
		if !m.IsWaitingForUserInput() {
			t.Error("IsWaitingForUserInput() is false: the hook-delivered cue did not route the turn to waiting")
		}
	})
}
