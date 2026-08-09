package session

import (
	"testing"
	"time"
)

var releaseT0 = time.Date(2026, 8, 9, 8, 0, 0, 0, time.UTC)

// TestReleaseTier_DropsOnlyTheNamedTier is the core guarantee. The watchdog
// declares one CHANNEL dead, not one session broken, so a hold sourced from a
// different tier must survive untouched — otherwise "demote the hook channel"
// would quietly take out the transcript-based stalled-edit-tool fallback too,
// which is the very tier the adapter is being demoted TO.
func TestReleaseTier_DropsOnlyTheNamedTier(t *testing.T) {
	h := NewSignalHolds()
	h.Hold("s", SignalPermissionPrompt, SignalPayload{}, releaseT0)
	h.Hold("s", SignalOpenToolStalled, SignalPayload{}, releaseT0)

	released := h.ReleaseTier("s", TierHook, releaseT0.Add(time.Hour))

	if len(released) != 1 {
		t.Fatalf("released %d holds, want 1: %+v", len(released), released)
	}
	if released[0].Kind != SignalPermissionPrompt {
		t.Errorf("Kind = %q, want %q", released[0].Kind, SignalPermissionPrompt)
	}
	if released[0].Tier != TierHook {
		t.Errorf("Tier = %v, want %v", released[0].Tier, TierHook)
	}
	if released[0].HeldFor != time.Hour {
		t.Errorf("HeldFor = %v, want 1h — measured against the injected pass clock, not wall time", released[0].HeldFor)
	}
	if h.Held("s", SignalPermissionPrompt) {
		t.Error("the hook-tier hold survived the release")
	}
	if !h.Held("s", SignalOpenToolStalled) {
		t.Error("a transcript-tier hold was collateral damage of a hook-channel demotion")
	}
}

// TestReleaseTier_DropsEveryHoldOfThatTier: one dead channel can be pinning a
// session through more than one signal at a time, and leaving any of them
// standing leaves the session pinned.
func TestReleaseTier_DropsEveryHoldOfThatTier(t *testing.T) {
	h := NewSignalHolds()
	for _, k := range []SignalKind{SignalPermissionPrompt, SignalIdlePrompt, SignalCompactInProgress} {
		h.Hold("s", k, SignalPayload{}, releaseT0)
	}

	released := h.ReleaseTier("s", TierHook, releaseT0.Add(time.Minute))

	if len(released) != 3 {
		t.Fatalf("released %d holds, want all 3 hook-tier holds: %+v", len(released), released)
	}
	if h.HasAny("s") {
		t.Error("the session still holds something after every one of its holds was released")
	}
}

// TestReleaseTier_OrderIsThePolicyTableOrder: the result reaches a log and a
// recorded lifecycle trace, so a map-iteration order would make two identical
// runs produce different recordings and defeat replay comparison.
func TestReleaseTier_OrderIsThePolicyTableOrder(t *testing.T) {
	var want []SignalKind
	for _, p := range signalPolicies {
		if p.tier == TierHook {
			want = append(want, p.kind)
		}
	}

	for attempt := 0; attempt < 20; attempt++ {
		h := NewSignalHolds()
		for _, k := range want {
			h.Hold("s", k, SignalPayload{}, releaseT0)
		}
		released := h.ReleaseTier("s", TierHook, releaseT0)
		if len(released) != len(want) {
			t.Fatalf("released %d, want %d", len(released), len(want))
		}
		for i, r := range released {
			if r.Kind != want[i] {
				t.Fatalf("attempt %d: order = %v at index %d, want %v — a nondeterministic order breaks recorded traces", attempt, r.Kind, i, want[i])
			}
		}
	}
}

// TestReleaseTier_IsANoOpWithNothingHeld: the detector calls this on every pass
// while an adapter is silent rather than tracking which sessions it has already
// swept, so the empty case must allocate nothing and report nothing.
func TestReleaseTier_IsANoOpWithNothingHeld(t *testing.T) {
	h := NewSignalHolds()
	if got := h.ReleaseTier("never-seen", TierHook, releaseT0); got != nil {
		t.Errorf("ReleaseTier on an unknown session returned %+v, want nil", got)
	}

	h.Hold("s", SignalOpenToolStalled, SignalPayload{}, releaseT0)
	if got := h.ReleaseTier("s", TierHook, releaseT0); got != nil {
		t.Errorf("ReleaseTier with no matching tier returned %+v, want nil", got)
	}
	if !h.Held("s", SignalOpenToolStalled) {
		t.Error("a no-op release dropped an unrelated hold")
	}
}

// TestReleaseTier_IsIdempotent: called repeatedly while the channel stays
// silent, the second call must report nothing rather than re-announcing holds
// it already dropped.
func TestReleaseTier_IsIdempotent(t *testing.T) {
	h := NewSignalHolds()
	h.Hold("s", SignalPermissionPrompt, SignalPayload{}, releaseT0)

	if got := h.ReleaseTier("s", TierHook, releaseT0); len(got) != 1 {
		t.Fatalf("first release returned %d, want 1", len(got))
	}
	if got := h.ReleaseTier("s", TierHook, releaseT0); got != nil {
		t.Errorf("second release returned %+v, want nil — a repeated sweep must not re-report", got)
	}
}

// TestReleaseTier_DoesNotTouchOtherSessions: the verdict is per adapter, but
// the release is applied per session as each one is next classified. A release
// that leaked across sessions would drop holds for sessions the caller never
// examined, including ones belonging to a different, healthy adapter.
func TestReleaseTier_DoesNotTouchOtherSessions(t *testing.T) {
	h := NewSignalHolds()
	h.Hold("a", SignalPermissionPrompt, SignalPayload{}, releaseT0)
	h.Hold("b", SignalPermissionPrompt, SignalPayload{}, releaseT0)

	h.ReleaseTier("a", TierHook, releaseT0)

	if h.Held("a", SignalPermissionPrompt) {
		t.Error("the targeted session kept its hold")
	}
	if !h.Held("b", SignalPermissionPrompt) {
		t.Error("an untargeted session lost its hold")
	}
}

// TestReleaseTier_KeepsAHoldPlacedAfterTheVerdictClock is the review finding
// about the window between reading the liveness counters and sweeping the
// holds.
//
// The hook receiver runs on its own goroutine. A channel that has just come
// back can land a POST, dispatch it and place a fresh hold in that window, and
// dropping it would throw away the first signal from a recovered channel while
// the very next pass reports the recovery. A hold asserted after the pass clock
// is not covered by evidence gathered before it.
func TestReleaseTier_KeepsAHoldPlacedAfterTheVerdictClock(t *testing.T) {
	h := NewSignalHolds()
	passClock := releaseT0.Add(time.Hour)

	h.Hold("s", SignalPermissionPrompt, SignalPayload{}, releaseT0)            // stale: predates the verdict
	h.Hold("s", SignalIdlePrompt, SignalPayload{}, passClock.Add(time.Second)) // fresh: arrived mid-pass

	released := h.ReleaseTier("s", TierHook, passClock)

	if len(released) != 1 || released[0].Kind != SignalPermissionPrompt {
		t.Fatalf("released %+v, want only the hold that predates the pass clock", released)
	}
	if !h.Held("s", SignalIdlePrompt) {
		t.Error("a hold placed after the verdict was taken was swept away — the first signal from a recovering channel is discarded")
	}
}

// The boundary itself: a hold placed exactly AT the pass clock is covered by
// the verdict (>= not >), matching the ceiling rule's own inclusive boundary.
func TestReleaseTier_ReleasesAHoldPlacedExactlyAtTheClock(t *testing.T) {
	h := NewSignalHolds()
	h.Hold("s", SignalPermissionPrompt, SignalPayload{}, releaseT0)
	if got := h.ReleaseTier("s", TierHook, releaseT0); len(got) != 1 {
		t.Fatalf("released %d holds, want 1 — heldAt == now must still be released", len(got))
	}
}
