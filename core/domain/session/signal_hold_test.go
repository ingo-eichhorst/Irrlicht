package session

import (
	"sync"
	"testing"
	"time"
)

const holdSID = "sess-1"

// holdT0 is the instant these tests hold signals at and classify at. Any fixed
// instant does: only the time-based compact policy reads the clock at all, and
// it reads Now-HeldSince, never the wall clock. Passing one value for both is
// what keeps every clockless policy's test unchanged by #1297.
var holdT0 = time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)

// TestSignalHolds_ConsumeOnce pins the Stop-hook policy: the hold applies to
// exactly the pass it triggered and is gone afterwards, so an authoritative
// turn-done can never bleed into the following turn.
func TestSignalHolds_ConsumeOnce(t *testing.T) {
	h := NewSignalHolds()
	h.Hold(holdSID, SignalTurnDone, SignalPayload{LastAssistantText: "done", WaitingCue: true}, holdT0)

	m := &SessionMetrics{}
	h.Overlay(holdSID, m, holdT0)
	if !m.HookTurnDone || m.LastAssistantText != "done" || !m.PendingWaitingCue {
		t.Fatalf("turn-done payload not applied: %+v", m)
	}
	if h.Held(holdSID, SignalTurnDone) {
		t.Error("consume-once hold must be released as it is applied")
	}

	next := &SessionMetrics{}
	h.Overlay(holdSID, next, holdT0)
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
	h.Hold(holdSID, SignalIdlePrompt, SignalPayload{}, holdT0)

	for pass := 1; pass <= 3; pass++ {
		m := &SessionMetrics{LastEventType: "turn_done"}
		h.Overlay(holdSID, m, holdT0)
		if !m.IdlePromptPending {
			t.Fatalf("pass %d: IdlePromptPending must re-apply while the turn is idle", pass)
		}
		if !h.Held(holdSID, SignalIdlePrompt) {
			t.Fatalf("pass %d: persistent hold must not be consumed", pass)
		}
	}

	// The user replied — IsAgentDone goes false, so the idle window is over.
	m := &SessionMetrics{LastEventType: "user"}
	h.Overlay(holdSID, m, holdT0)
	if m.IdlePromptPending {
		t.Error("IdlePromptPending must not be applied once the turn is no longer idle")
	}
	if h.Held(holdSID, SignalIdlePrompt) {
		t.Error("a stale hold must be dropped")
	}
}

// TestSignalHolds_TurnDonePreservesPriorText pins that an empty hook message
// does not clobber the text the transcript already has: the Stop hook can
// arrive carrying nothing, and the display text must survive it.
func TestSignalHolds_TurnDonePreservesPriorText(t *testing.T) {
	h := NewSignalHolds()
	h.Hold(holdSID, SignalTurnDone, SignalPayload{}, holdT0)

	m := &SessionMetrics{LastAssistantText: "prior text"}
	h.Overlay(holdSID, m, holdT0)
	if m.LastAssistantText != "prior text" {
		t.Errorf("LastAssistantText = %q, want the prior text preserved", m.LastAssistantText)
	}
	if !m.HookTurnDone {
		t.Error("HookTurnDone must still be set even with an empty message")
	}
}

// TestSignalHolds_TurnDoneCueIsAdditive pins the asymmetry: the hook's cue
// verdict can add a waiting cue but must never clear one the parser already
// found, so a hook that saw no question cannot mask the transcript's own.
func TestSignalHolds_TurnDoneCueIsAdditive(t *testing.T) {
	h := NewSignalHolds()
	h.Hold(holdSID, SignalTurnDone, SignalPayload{LastAssistantText: "done"}, holdT0)

	m := &SessionMetrics{PendingWaitingCue: true} // the parser already found a cue
	h.Overlay(holdSID, m, holdT0)
	if !m.PendingWaitingCue {
		t.Error("a false hook cue must not clear a PendingWaitingCue the parser set")
	}
}

// TestSignalHolds_IdlePromptClearedByOpenTool covers the other way the idle
// window ends: an open tool call makes IsAgentDone false, and the open-tool
// rules own that case rather than idle_prompt.
func TestSignalHolds_IdlePromptClearedByOpenTool(t *testing.T) {
	h := NewSignalHolds()
	h.Hold(holdSID, SignalIdlePrompt, SignalPayload{}, holdT0)

	m := &SessionMetrics{LastEventType: "turn_done", HasOpenToolCall: true}
	h.Overlay(holdSID, m, holdT0)
	if m.IdlePromptPending {
		t.Error("IdlePromptPending must NOT be set while a tool call is open")
	}
	if h.Held(holdSID, SignalIdlePrompt) {
		t.Error("signal must be dropped when an open tool blocks the turn")
	}
}

// TestSignalHolds_PermissionStaleOnDenial pins the permission policy's one
// unusual edge: Claude Code fires no PostToolUseFailure when the user rejects a
// prompt, so the transcript's own denial marker is the only end-of-life notice
// the hold ever gets.
func TestSignalHolds_PermissionStaleOnDenial(t *testing.T) {
	h := NewSignalHolds()
	h.Hold(holdSID, SignalPermissionPrompt, SignalPayload{}, holdT0)

	open := &SessionMetrics{}
	h.Overlay(holdSID, open, holdT0)
	if !open.PermissionPending {
		t.Error("an open permission prompt must be applied")
	}

	denied := &SessionMetrics{LastWasToolDenial: true}
	h.Overlay(holdSID, denied, holdT0)
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
	h.Hold(holdSID, SignalTurnDone, SignalPayload{LastAssistantText: "All set."}, holdT0)
	h.Hold(holdSID, SignalIdlePrompt, SignalPayload{}, holdT0)

	m := &SessionMetrics{LastEventType: "assistant_message"}
	h.Overlay(holdSID, m, holdT0)

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

// TestSignalHolds_PolicyTableIsWellFormed replaces the old "is every policy
// listed in the order slice?" check. Merging the map and the order slice into
// one ordered table made that class of mistake unrepresentable rather than
// test-caught, so what remains to verify is that no kind is declared twice.
func TestSignalHolds_PolicyTableIsWellFormed(t *testing.T) {
	seen := map[SignalKind]bool{}
	for _, p := range signalPolicies {
		if p.kind == "" {
			t.Error("a policy row has no kind")
		}
		if seen[p.kind] {
			t.Errorf("signalPolicies declares %q twice", p.kind)
		}
		seen[p.kind] = true
	}
}

// TestTierOf covers the accessor the classifier resolves rule tiers through —
// the single-source-of-truth link that stops a signal's authority being
// declared once in its policy and again in the rule that reads it.
func TestTierOf(t *testing.T) {
	for _, kind := range []SignalKind{SignalPermissionPrompt, SignalTurnDone, SignalIdlePrompt, SignalCompactInProgress} {
		if got := TierOf(kind); got != TierHook {
			t.Errorf("TierOf(%q) = %v, want TierHook", kind, got)
		}
	}
	// The first non-hook row: a transcript-tier inference, not an out-of-band
	// push. The classifier's open_tool_stalled rule resolves its tier through
	// here, so this is the one place that authority is declared.
	if got := TierOf(SignalOpenToolStalled); got != TierTranscript {
		t.Errorf("TierOf(%q) = %v, want TierTranscript", SignalOpenToolStalled, got)
	}
	if got := TierOf("not-a-signal"); got != TierNone {
		t.Errorf("TierOf(unknown) = %v, want TierNone", got)
	}
}

// TestSignalHolds_EveryPolicyIsWellFormed keeps a future Phase 4-7 row from
// landing half-declared: a policy with no apply would be silently inert, and
// one with no tier would report TierNone and lose every arbitration.
//
// It deliberately does NOT forbid combining consumeOnce with stale. Overlay
// evaluates stale before apply on every pass, including the first, so the
// combination is meaningful — "consume this on the pass it fires, unless the
// metrics already contradict it, in which case drop it unapplied". That is
// exactly the shape a retrospective OTel signal would need (#1141): a span that
// exports after the user has already replied must be discarded, not applied.
func TestSignalHolds_EveryPolicyIsWellFormed(t *testing.T) {
	for _, p := range signalPolicies {
		if p.apply == nil {
			t.Errorf("policy %q has no apply func", p.kind)
		}
		if !p.tier.Known() {
			t.Errorf("policy %q has tier %v, which is not a real tier", p.kind, p.tier)
		}
	}
}

// TestSignalHolds_StaleIsCheckedOnTheFirstPass pins the property the
// well-formedness test above relies on: staleness is evaluated before apply on
// every pass, so a signal that is already contradicted when it first arrives is
// dropped rather than applied once and then consumed.
func TestSignalHolds_StaleIsCheckedOnTheFirstPass(t *testing.T) {
	h := NewSignalHolds()
	h.Hold(holdSID, SignalIdlePrompt, SignalPayload{}, holdT0)

	// The user replied before the ~6s-late hook ever landed: the very first
	// Overlay must discard it, not honour it once.
	m := &SessionMetrics{LastEventType: "user"}
	h.Overlay(holdSID, m, holdT0)

	if m.IdlePromptPending {
		t.Error("a signal already contradicted on arrival must not be applied even once")
	}
	if h.Held(holdSID, SignalIdlePrompt) {
		t.Error("it must also be dropped")
	}
}

// TestSignalPolicies_OrderIsPinned turns signalPolicies' load-bearing order
// from prose into an assertion. The table's own header says THE ORDER IS
// LOAD-BEARING, but until #1319 the only thing enforcing it was a reviewer
// noticing — and a row inserted in the wrong place still compiles, still
// applies, and fails nothing.
//
// Two orderings actually carry weight, and both are one-directional reads of a
// field an earlier row writes on the same pass:
//
//   - SignalTurnDone before SignalIdlePrompt: idle_prompt's staleness calls
//     IsAgentDone, which reads the HookTurnDone that turn_done's apply just
//     set. Reversed, a hook-corrected waiting is discarded as stale one
//     instruction before it would have been applied.
//
//   - SignalPermissionPrompt before SignalOpenToolStalled: the stalled row's
//     ripe rule reads PermissionPending, which the permission row's apply
//     sets. Reversed, the transcript fallback fires alongside the hook it is
//     supposed to defer to.
//
//   - SignalTurnDone before SignalPermissionPrompt (#1861): the permission
//     row's staleness reads the HookTurnDone that turn_done's apply sets —
//     the same dependency the three rows above and below it have on that row.
//     The permission row led the table until #1861 and was moved down for
//     exactly this reason. Reversed, a Stop hook arriving alongside a held
//     permission prompt is evaluated against metrics that do not yet know the
//     turn ended, so the turn-end clearing edge never fires and a dialog with
//     no tool behind it (claudecode's Notification/permission_prompt for
//     managed_settings_security or auto_mode_setup_review, which produce no
//     PostToolUse) rides to the 12-hour ceiling instead.
//
//   - SignalTurnDone before SignalSessionError (#1798): the error row's
//     staleness reads HookTurnDone, which turn_done's apply sets on the same
//     pass — the same dependency idle_prompt has, for the same reason.
//     Reversed, a Stop hook arriving alongside a held error is evaluated
//     against metrics that do not yet know the turn ended, so a failure
//     survives a turn that actually completed and the session stays red.
//
// SignalSessionError sits before SignalOpenToolStalled only because that row
// must stay last; nothing reads what the error row writes, so its position is
// otherwise free.
//
// Pinned as the exact full sequence rather than as two pairwise checks so that
// adding a row is a deliberate act — the test names the position, and whoever
// changes it has to say why in the diff.
func TestSignalPolicies_OrderIsPinned(t *testing.T) {
	want := []SignalKind{
		SignalTurnDone,
		SignalPermissionPrompt,
		SignalIdlePrompt,
		SignalCompactInProgress,
		SignalSessionError,
		SignalOpenToolStalled,
	}

	if len(signalPolicies) != len(want) {
		t.Fatalf("signalPolicies has %d rows, want %d — a row was added or removed; "+
			"confirm the ordering invariants in this test's doc comment still hold, then update want",
			len(signalPolicies), len(want))
	}
	for i, kind := range want {
		if got := signalPolicies[i].kind; got != kind {
			t.Errorf("signalPolicies[%d] = %q, want %q", i, got, kind)
		}
	}
}

// TestSignalHolds_RipeGatesTheFirstApply covers the arming predicate #1319
// added, at the mechanism level rather than through any one row: an unripe hold
// is neither applied nor dropped nor consumed, and it applies on the first pass
// where ripe comes back true.
//
// The three-way distinction is the whole point. Before ripe existed the only
// way to say "not now" was stale, which *ends* the hold — so a rule that wanted
// to wait had no way to say so, and Overlay applied on the first pass after
// Hold unconditionally.
func TestSignalHolds_RipeGatesTheFirstApply(t *testing.T) {
	h := NewSignalHolds()
	h.Hold(holdSID, SignalOpenToolStalled, SignalPayload{}, holdT0)

	// Unripe: not applied, and — the part stale would have gotten wrong — the
	// hold survives to be reconsidered later.
	early := editOpenMetrics()
	h.Overlay(holdSID, early, holdT0.Add(stalledEditToolThreshold-time.Nanosecond))
	if early.OpenToolStalled {
		t.Error("an unripe hold must not be applied")
	}
	if !h.Held(holdSID, SignalOpenToolStalled) {
		t.Fatal("an unripe hold must be kept, not dropped: it has not happened yet")
	}

	// Ripe: applied, on a hold that was armed long before.
	due := editOpenMetrics()
	h.Overlay(holdSID, due, holdT0.Add(stalledEditToolThreshold))
	if !due.OpenToolStalled {
		t.Error("a ripe hold must be applied")
	}
}

// TestSignalHolds_StaleBeatsRipe pins the evaluation order: a hold that is both
// over and not yet ripe is dropped, not left waiting for a due date that can
// never arrive. Without it an arm-then-fire hold whose condition ended early
// would leak until the session went away.
func TestSignalHolds_StaleBeatsRipe(t *testing.T) {
	h := NewSignalHolds()
	h.Hold(holdSID, SignalOpenToolStalled, SignalPayload{}, holdT0)

	// The tool closed well before the threshold: stale, and unripe.
	m := &SessionMetrics{HasOpenToolCall: false}
	h.Overlay(holdSID, m, holdT0.Add(time.Second))

	if m.OpenToolStalled {
		t.Error("a stale hold must not be applied")
	}
	if h.Held(holdSID, SignalOpenToolStalled) {
		t.Error("stale must win over unripe, or the hold leaks forever")
	}
}

// TestSignalHolds_ReleaseAndDropSession covers the explicit end-of-life paths.
func TestSignalHolds_ReleaseAndDropSession(t *testing.T) {
	h := NewSignalHolds()
	h.Hold(holdSID, SignalPermissionPrompt, SignalPayload{}, holdT0)
	h.Hold(holdSID, SignalIdlePrompt, SignalPayload{}, holdT0)

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
	h.Hold("a", SignalIdlePrompt, SignalPayload{}, holdT0)

	m := &SessionMetrics{LastEventType: "turn_done"}
	h.Overlay("b", m, holdT0)
	if m.IdlePromptPending {
		t.Error("session b must not see session a's held signal")
	}
}

// TestSignalHolds_NilMetricsPreservesHolds pins the guard that keeps a
// classify pass with no metrics from silently eating an authoritative signal.
func TestSignalHolds_NilMetricsPreservesHolds(t *testing.T) {
	h := NewSignalHolds()
	h.Hold(holdSID, SignalTurnDone, SignalPayload{}, holdT0)
	h.Overlay(holdSID, nil, holdT0)
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
		go func() { defer wg.Done(); h.Hold(holdSID, SignalPermissionPrompt, SignalPayload{}, holdT0) }()
		go func() { defer wg.Done(); h.Overlay(holdSID, &SessionMetrics{}, holdT0) }()
		go func() { defer wg.Done(); h.Held(holdSID, SignalPermissionPrompt) }()
	}
	wg.Wait()
}
