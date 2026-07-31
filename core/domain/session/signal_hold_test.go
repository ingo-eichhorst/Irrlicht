package session

import (
	"sync"
	"testing"
)

const holdSID = "sess-1"

// TestSignalHolds_ConsumeOnce pins the Stop-hook policy: the hold applies to
// exactly the pass it triggered and is gone afterwards, so an authoritative
// turn-done can never bleed into the following turn.
func TestSignalHolds_ConsumeOnce(t *testing.T) {
	h := NewSignalHolds()
	h.Hold(holdSID, SignalTurnDone, SignalPayload{LastAssistantText: "done", WaitingCue: true})

	m := &SessionMetrics{}
	if tier := h.Overlay(holdSID, m); tier != TierHook {
		t.Errorf("Overlay tier = %v, want TierHook", tier)
	}
	if !m.HookTurnDone || m.LastAssistantText != "done" || !m.PendingWaitingCue {
		t.Fatalf("turn-done payload not applied: %+v", m)
	}
	if h.Held(holdSID, SignalTurnDone) {
		t.Error("consume-once hold must be released as it is applied")
	}

	next := &SessionMetrics{}
	if tier := h.Overlay(holdSID, next); tier != TierNone {
		t.Errorf("second Overlay tier = %v, want TierNone", tier)
	}
	if next.HookTurnDone {
		t.Error("a consumed signal must not re-apply on the next pass")
	}
}

// TestSignalHolds_PersistentUntilStale pins the idle_prompt policy — the one
// that makes the ~6s-late correction stick. The hold must survive repeated
// passes while the turn stays idle, then drop itself the moment the turn is no
// longer idle.
func TestSignalHolds_PersistentUntilStale(t *testing.T) {
	h := NewSignalHolds()
	h.Hold(holdSID, SignalIdlePrompt, SignalPayload{})

	for pass := 1; pass <= 3; pass++ {
		m := &SessionMetrics{LastEventType: "turn_done"}
		h.Overlay(holdSID, m)
		if !m.IdlePromptPending {
			t.Fatalf("pass %d: IdlePromptPending must re-apply while the turn is idle", pass)
		}
		if !h.Held(holdSID, SignalIdlePrompt) {
			t.Fatalf("pass %d: persistent hold must not be consumed", pass)
		}
	}

	// The user replied — IsAgentDone goes false, so the idle window is over.
	m := &SessionMetrics{LastEventType: "user"}
	h.Overlay(holdSID, m)
	if m.IdlePromptPending {
		t.Error("IdlePromptPending must not be applied once the turn is no longer idle")
	}
	if h.Held(holdSID, SignalIdlePrompt) {
		t.Error("a stale hold must be dropped")
	}
}

// TestSignalHolds_PermissionStaleOnDenial pins the permission policy's one
// unusual edge: Claude Code fires no PostToolUseFailure when the user rejects a
// prompt, so the transcript's own denial marker is the only end-of-life notice
// the hold ever gets.
func TestSignalHolds_PermissionStaleOnDenial(t *testing.T) {
	h := NewSignalHolds()
	h.Hold(holdSID, SignalPermissionPrompt, SignalPayload{})

	open := &SessionMetrics{}
	h.Overlay(holdSID, open)
	if !open.PermissionPending {
		t.Error("an open permission prompt must be applied")
	}

	denied := &SessionMetrics{LastWasToolDenial: true}
	h.Overlay(holdSID, denied)
	if denied.PermissionPending {
		t.Error("a denied prompt must not be applied")
	}
	if h.Held(holdSID, SignalPermissionPrompt) {
		t.Error("denial must drop the hold")
	}
}

// TestSignalHolds_ApplicationOrderIsLoadBearing is the regression test for the
// subtlety that signalOrder exists to defend, and the reason a map range would
// be a latent bug rather than a style question.
//
// SignalIdlePrompt's staleness test calls IsAgentDone, which reads the
// HookTurnDone that SignalTurnDone.apply sets. When a Stop hook and an
// idle_prompt hook both land for the same finished turn — the exact live
// sequence on claudecode — turn-done must be applied first, or idle_prompt
// evaluates staleness against metrics that do not yet know the turn ended,
// drops itself, and throws away the correction it exists to deliver.
//
// The transcript tail here is deliberately NOT "turn_done": that is what makes
// the ordering observable. With a turn_done tail, IsAgentDone is true either
// way and the bug hides.
func TestSignalHolds_ApplicationOrderIsLoadBearing(t *testing.T) {
	h := NewSignalHolds()
	h.Hold(holdSID, SignalTurnDone, SignalPayload{LastAssistantText: "All set."})
	h.Hold(holdSID, SignalIdlePrompt, SignalPayload{})

	m := &SessionMetrics{LastEventType: "assistant_message"}
	h.Overlay(holdSID, m)

	if !m.HookTurnDone {
		t.Fatal("turn-done must be applied")
	}
	if !m.IdlePromptPending {
		t.Error("idle_prompt must survive: turn-done ran first, so IsAgentDone was already true")
	}
	if !h.Held(holdSID, SignalIdlePrompt) {
		t.Error("idle_prompt must still be held after the pass")
	}
}

// TestSignalHolds_SignalOrderCoversEveryPolicy guards the other half of that
// invariant: a signal added to signalPolicies but forgotten in signalOrder
// would simply never be applied — a silent no-op, not a compile error.
func TestSignalHolds_SignalOrderCoversEveryPolicy(t *testing.T) {
	inOrder := map[SignalKind]bool{}
	for _, k := range signalOrder {
		if inOrder[k] {
			t.Errorf("signalOrder lists %q twice", k)
		}
		inOrder[k] = true
		if _, ok := signalPolicies[k]; !ok {
			t.Errorf("signalOrder lists %q, which has no policy", k)
		}
	}
	for k := range signalPolicies {
		if !inOrder[k] {
			t.Errorf("policy %q is missing from signalOrder — it would never be applied", k)
		}
	}
}

// TestSignalHolds_EveryPolicyIsWellFormed keeps a future Phase 4-7 row from
// landing half-declared: a policy with no apply would be silently inert, and
// one with no tier would report TierNone and lose every arbitration.
func TestSignalHolds_EveryPolicyIsWellFormed(t *testing.T) {
	for kind, p := range signalPolicies {
		if p.apply == nil {
			t.Errorf("policy %q has no apply func", kind)
		}
		if !p.tier.Known() {
			t.Errorf("policy %q has tier %v, which is not a real tier", kind, p.tier)
		}
		if p.consumeOnce && p.stale != nil {
			t.Errorf("policy %q is both consume-once and staleness-gated; the stale rule can never be reached on a second pass", kind)
		}
	}
}

// TestSignalHolds_ReleaseAndDropSession covers the explicit end-of-life paths.
func TestSignalHolds_ReleaseAndDropSession(t *testing.T) {
	h := NewSignalHolds()
	h.Hold(holdSID, SignalPermissionPrompt, SignalPayload{})
	h.Hold(holdSID, SignalIdlePrompt, SignalPayload{})

	h.Release(holdSID, SignalPermissionPrompt)
	if h.Held(holdSID, SignalPermissionPrompt) {
		t.Error("Release must drop the named signal")
	}
	if !h.Held(holdSID, SignalIdlePrompt) {
		t.Error("Release must not touch other signals")
	}

	h.DropSession(holdSID)
	if h.Held(holdSID, SignalIdlePrompt) {
		t.Error("DropSession must forget every hold for the session")
	}
}

// TestSignalHolds_SessionsAreIsolated guards against a recycled or concurrent
// session inheriting another's signal.
func TestSignalHolds_SessionsAreIsolated(t *testing.T) {
	h := NewSignalHolds()
	h.Hold("a", SignalIdlePrompt, SignalPayload{})

	m := &SessionMetrics{LastEventType: "turn_done"}
	h.Overlay("b", m)
	if m.IdlePromptPending {
		t.Error("session b must not see session a's held signal")
	}
}

// TestSignalHolds_NilMetricsPreservesHolds pins the guard that keeps a
// classify pass with no metrics from silently eating an authoritative signal.
func TestSignalHolds_NilMetricsPreservesHolds(t *testing.T) {
	h := NewSignalHolds()
	h.Hold(holdSID, SignalTurnDone, SignalPayload{})
	if tier := h.Overlay(holdSID, nil); tier != TierNone {
		t.Errorf("Overlay(nil) tier = %v, want TierNone", tier)
	}
	if !h.Held(holdSID, SignalTurnDone) {
		t.Error("nil metrics must leave the hold intact for a later pass")
	}
}

// TestSignalHolds_ConcurrentAccess exercises the lock under -race: hooks arrive
// on HTTP handler goroutines while the event loop classifies.
func TestSignalHolds_ConcurrentAccess(t *testing.T) {
	h := NewSignalHolds()
	var wg sync.WaitGroup
	for range 16 {
		wg.Add(3)
		go func() { defer wg.Done(); h.Hold(holdSID, SignalPermissionPrompt, SignalPayload{}) }()
		go func() { defer wg.Done(); h.Overlay(holdSID, &SessionMetrics{}) }()
		go func() { defer wg.Done(); h.Held(holdSID, SignalPermissionPrompt) }()
	}
	wg.Wait()
}
