package session

import "testing"

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

		// Payload-gated: the name alone does not determine the effect, so
		// these are absent from the table by design (see hookSignalEffects).
		{HookStop, "", false, false},
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
