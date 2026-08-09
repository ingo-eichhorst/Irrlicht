package services

import (
	"fmt"
	"sync"
	"testing"

	"irrlicht/core/domain/session"
)

const (
	livenessAdapter = "claude-code"
	livenessOther   = "codex"
)

// livenessHarness is a watchdog plus the two mutable collaborators the
// production one reads through: the adapter-side receipt counters and the
// consent predicate. Both are maps the test writes directly, so a test reads as
// "grant consent, run turns, deliver a hook" rather than as plumbing.
type livenessHarness struct {
	w        *HookLivenessWatchdog
	receipts map[string]uint64
	ready    map[string]bool
}

func newLivenessHarness(threshold int) *livenessHarness {
	h := &livenessHarness{
		receipts: map[string]uint64{},
		ready:    map[string]bool{livenessAdapter: true},
	}
	h.w = NewHookLivenessWatchdog(HookLivenessConfig{
		SilentTurns:    threshold,
		DefaultAdapter: livenessAdapter,
		Adapters:       []string{livenessAdapter, livenessOther},
		Receipts:       func(a string) uint64 { return h.receipts[a] },
	})
	h.w.SetChannelReady(func(a string) bool { return h.ready[a] })
	return h
}

// turn drives one complete turn: a pass with the agent busy, then a pass with
// it done. That false→true edge is the same one processActivity produces, and
// it is deliberately built from transcript-derived fields only — the watchdog
// must not be able to count a turn using anything the hook channel supplies,
// or a dead channel would suppress the measurement that convicts it.
func (h *livenessHarness) turn(sessionID string) HookLivenessChange {
	h.pass(sessionID, false)
	return h.pass(sessionID, true)
}

// pass drives one processActivity pass. done=false is modelled with an open
// tool call rather than an absent turn_done so the metrics stay a shape the
// classifier would actually produce.
func (h *livenessHarness) pass(sessionID string, done bool) HookLivenessChange {
	return h.passAs(sessionID, livenessAdapter, done)
}

// passAs is pass for a session belonging to some other adapter. The metrics
// shape a turn is recognised by lives here alone, so a change to it cannot
// leave one test asserting against a shape the detector no longer produces.
func (h *livenessHarness) passAs(sessionID, adapter string, done bool) HookLivenessChange {
	m := &session.SessionMetrics{LastEventType: "turn_done"}
	if !done {
		m.HasOpenToolCall = true
	}
	return h.w.OnActivity(&session.SessionState{
		SessionID: sessionID,
		Adapter:   adapter,
		Metrics:   m,
	})
}

// turnAs is turn for a session belonging to some other adapter.
func (h *livenessHarness) turnAs(sessionID, adapter string) HookLivenessChange {
	h.passAs(sessionID, adapter, false)
	return h.passAs(sessionID, adapter, true)
}

// A hook arriving is the only thing that clears silence.
func (h *livenessHarness) hookArrives() { h.receipts[livenessAdapter]++ }

// TestHookLiveness_DemotesAfterThresholdTurnsWithNoReceipts is the core claim:
// N completed turns with zero hook requests, on a channel whose consent is
// granted and whose install succeeded, is a verdict — and it is reached at N,
// not before.
func TestHookLiveness_DemotesAfterThresholdTurnsWithNoReceipts(t *testing.T) {
	h := newLivenessHarness(3)

	for i := 1; i < 3; i++ {
		if got := h.turn("s"); got.Transition != HookLivenessUnchanged {
			t.Fatalf("turn %d: transition = %v, want unchanged — convicting early makes the threshold decorative", i, got.Transition)
		}
		if h.w.Silent(livenessAdapter) {
			t.Fatalf("turn %d: channel reported silent before the threshold was reached", i)
		}
	}

	got := h.turn("s")
	if got.Transition != HookLivenessSilent {
		t.Fatalf("transition on the threshold turn = %v, want HookLivenessSilent", got.Transition)
	}
	if got.Adapter != livenessAdapter {
		t.Errorf("Adapter = %q, want %q — a verdict that names the wrong channel sends its reader to the wrong config file", got.Adapter, livenessAdapter)
	}
	if got.SilentTurns != 3 || got.Threshold != 3 {
		t.Errorf("SilentTurns/Threshold = %d/%d, want 3/3 — both are recorded so a trace stays readable after the threshold is retuned", got.SilentTurns, got.Threshold)
	}
	if got.Receipts != 0 {
		t.Errorf("Receipts = %d, want 0 — zero says this channel never worked, non-zero says it worked and stopped, and those are different bugs", got.Receipts)
	}
	if !h.w.Silent(livenessAdapter) {
		t.Error("Silent() must report the demotion the transition just announced")
	}
}

// TestHookLiveness_ReportsTheDemotionOnce is the noise floor. A channel that
// stays dead stays dead for every turn afterwards; a log line and a lifecycle
// event per turn would bury the one that mattered — the same argument #1364
// makes for reporting an unrecognized event name once.
func TestHookLiveness_ReportsTheDemotionOnce(t *testing.T) {
	h := newLivenessHarness(2)

	h.turn("s")
	if got := h.turn("s"); got.Transition != HookLivenessSilent {
		t.Fatalf("precondition: expected the demotion on turn 2, got %v", got.Transition)
	}
	for i := 3; i <= 6; i++ {
		if got := h.turn("s"); got.Transition != HookLivenessUnchanged {
			t.Fatalf("turn %d re-reported the demotion (%v); it must be announced once per silent episode", i, got.Transition)
		}
	}
	if !h.w.Silent(livenessAdapter) {
		t.Error("staying quiet about it must not mean forgetting it")
	}
}

// TestHookLiveness_RecoversWhenEventsResume is scope item 3 stated literally:
// a health signal, not a latch.
func TestHookLiveness_RecoversWhenEventsResume(t *testing.T) {
	h := newLivenessHarness(2)
	h.turn("s")
	h.turn("s")
	if !h.w.Silent(livenessAdapter) {
		t.Fatal("precondition: the channel must be silent before recovery can mean anything")
	}

	h.hookArrives()
	got := h.pass("s", true)

	if got.Transition != HookLivenessRecovered {
		t.Fatalf("transition = %v, want HookLivenessRecovered", got.Transition)
	}
	if got.Receipts != 1 {
		t.Errorf("Receipts = %d, want 1", got.Receipts)
	}
	if h.w.Silent(livenessAdapter) {
		t.Error("a recovered channel must stop being treated as dead without a daemon restart")
	}
}

// TestHookLiveness_RecoveryNeedsNoTurnBoundary is the asymmetry, asserted
// rather than assumed. Convicting a channel requires evidence that turns went
// by; acquitting it requires only that a hook arrived. Making recovery wait for
// the next turn boundary would be a latch wearing a different hat — and a user
// whose next action is the one blocked by the stale hold would never produce
// that boundary.
func TestHookLiveness_RecoveryNeedsNoTurnBoundary(t *testing.T) {
	h := newLivenessHarness(2)
	h.turn("s")
	h.turn("s")
	if !h.w.Silent(livenessAdapter) {
		t.Fatal("precondition: expected a silent channel")
	}

	h.hookArrives()
	// A mid-turn pass: the agent is busy, so there is no rising edge at all.
	if got := h.pass("s", false); got.Transition != HookLivenessRecovered {
		t.Fatalf("transition on a non-boundary pass = %v, want HookLivenessRecovered", got.Transition)
	}
	if h.w.Silent(livenessAdapter) {
		t.Error("recovery must take effect on the pass that observed the receipt")
	}
}

// TestHookLiveness_CountsResetOnAnyReceipt guards the other direction: a
// channel that delivers on most turns and misses one must never accumulate its
// way to a verdict.
func TestHookLiveness_CountsResetOnAnyReceipt(t *testing.T) {
	h := newLivenessHarness(3)
	for i := 0; i < 20; i++ {
		if i%3 != 2 {
			h.hookArrives()
		}
		if got := h.turn("s"); got.Transition != HookLivenessUnchanged {
			t.Fatalf("turn %d: a channel delivering two turns in three was reported %v", i, got.Transition)
		}
	}
	if h.w.Silent(livenessAdapter) {
		t.Error("an intermittently quiet but live channel must not be demoted")
	}
}

// TestHookLiveness_DisarmedWhenConsentOrInstallIsNot is the boundary against
// the two neighbouring diagnoses. A channel nobody granted, and a channel whose
// install effect FAILED (#1362 — which is what HookChannelReady returning false
// encodes), must produce no verdict at all: the first because there is nothing
// to be silent about, the second because that failure is already reported
// elsewhere with its actual error string, and re-reporting it in weaker words
// sends the reader to the wrong fix.
func TestHookLiveness_DisarmedWhenConsentOrInstallIsNot(t *testing.T) {
	h := newLivenessHarness(2)
	h.ready[livenessAdapter] = false

	for i := 0; i < 10; i++ {
		if got := h.turn("s"); got.Transition != HookLivenessUnchanged {
			t.Fatalf("turn %d on a disarmed channel produced %v", i, got.Transition)
		}
	}
	if h.w.Silent(livenessAdapter) {
		t.Error("a channel that was never installed cannot be silent — it is absent, which is a different fact")
	}
}

// TestHookLiveness_DisarmingClearsSilenceWithoutClaimingRecovery: revoking
// consent while a channel is demoted must drop the demotion — the watchdog has
// no business holding a verdict about a channel it no longer watches — but must
// NOT emit a recovered event, which would put a false all-clear in the trace.
func TestHookLiveness_DisarmingClearsSilenceWithoutClaimingRecovery(t *testing.T) {
	h := newLivenessHarness(2)
	h.turn("s")
	h.turn("s")
	if !h.w.Silent(livenessAdapter) {
		t.Fatal("precondition: expected a silent channel")
	}

	h.ready[livenessAdapter] = false
	if got := h.pass("s", true); got.Transition != HookLivenessUnchanged {
		t.Errorf("revoking consent produced %v; the channel did not come back, it stopped being watched", got.Transition)
	}
	if h.w.Silent(livenessAdapter) {
		t.Error("a channel that is no longer watched must not stay demoted")
	}
}

// TestHookLiveness_DoesNotBankTurnsFromBeforeItWasArmed: turns that happened
// while consent was withheld are not evidence against the channel, so
// re-granting starts the count over rather than tripping immediately.
func TestHookLiveness_DoesNotBankTurnsFromBeforeItWasArmed(t *testing.T) {
	h := newLivenessHarness(3)
	h.ready[livenessAdapter] = false
	for i := 0; i < 10; i++ {
		h.turn("s")
	}

	h.ready[livenessAdapter] = true
	for i := 1; i < 3; i++ {
		if got := h.turn("s"); got.Transition != HookLivenessUnchanged {
			t.Fatalf("turn %d after re-granting produced %v — turns from the disarmed period were banked", i, got.Transition)
		}
	}
	if got := h.turn("s"); got.Transition != HookLivenessSilent {
		t.Fatalf("the count should restart, not reset forever: transition = %v, want silent on turn 3", got.Transition)
	}
}

// TestHookLiveness_BaselinesAgainstReceiptsAlreadySeen: hooks served before the
// watchdog started watching an adapter are not a recovery. Starting the
// baseline at zero would credit them on the very next pass and make the first
// verdict unreachable.
func TestHookLiveness_BaselinesAgainstReceiptsAlreadySeen(t *testing.T) {
	h := newLivenessHarness(2)
	h.receipts[livenessAdapter] = 500

	h.turn("s")
	if got := h.turn("s"); got.Transition != HookLivenessSilent {
		t.Fatalf("transition = %v, want silent — a healthy history must not immunise a channel that has since died", got.Transition)
	}
}

// TestHookLiveness_KillSwitch: threshold <= 0 disables everything, matching
// CacheBloatThreshold's convention.
func TestHookLiveness_KillSwitch(t *testing.T) {
	h := newLivenessHarness(0)
	for i := 0; i < 50; i++ {
		if got := h.turn("s"); got.Transition != HookLivenessUnchanged {
			t.Fatalf("the kill switch let a transition through: %v", got.Transition)
		}
	}
	if h.w.enabled() || h.w.Silent(livenessAdapter) {
		t.Error("SilentTurns <= 0 must disable the watchdog completely")
	}
}

// TestHookLiveness_TurnsAreAttributedPerAdapter: one adapter going dark must
// not convict another. After #1355's rollout there are up to ten channels that
// can fail independently, which is the whole reason this is keyed by adapter.
func TestHookLiveness_TurnsAreAttributedPerAdapter(t *testing.T) {
	h := newLivenessHarness(2)
	h.ready[livenessOther] = true

	h.hookArrives()
	h.turn("s")
	h.hookArrives()
	h.turn("s")

	h.turnAs("o", livenessOther)
	if got := h.turnAs("o", livenessOther); got.Transition != HookLivenessSilent || got.Adapter != livenessOther {
		t.Fatalf("transition/adapter = %v/%q, want silent/%q", got.Transition, got.Adapter, livenessOther)
	}
	if h.w.Silent(livenessAdapter) {
		t.Error("the healthy adapter was demoted alongside the dead one")
	}
}

// TestHookLiveness_EmptyAdapterResolvesToTheDefault mirrors
// DiagnosticsService's handling: a claudecode session may carry an empty
// Adapter, and attributing its turns to "" would silently split one channel's
// evidence in two.
func TestHookLiveness_EmptyAdapterResolvesToTheDefault(t *testing.T) {
	h := newLivenessHarness(2)
	h.turnAs("b", "")
	if got := h.turnAs("b", ""); got.Transition != HookLivenessSilent || got.Adapter != livenessAdapter {
		t.Fatalf("transition/adapter = %v/%q, want silent/%q", got.Transition, got.Adapter, livenessAdapter)
	}
	if !h.w.Silent("") {
		t.Error("Silent(\"\") must resolve to the default adapter too, or the release path misses these sessions")
	}
}

// TestHookLiveness_SnapshotPublishesEveryConfiguredChannel: a channel with no
// traffic at all is the single most telling row in a bundle from a user whose
// hooks never worked, and it is exactly the row a map built from live counters
// cannot contain.
func TestHookLiveness_SnapshotPublishesEveryConfiguredChannel(t *testing.T) {
	h := newLivenessHarness(2)
	h.turn("s")
	h.turn("s")

	rows := h.w.Snapshot()
	if len(rows) != 2 {
		t.Fatalf("Snapshot returned %d rows, want one per configured adapter: %+v", len(rows), rows)
	}
	if rows[0].Adapter != livenessAdapter || rows[1].Adapter != livenessOther {
		t.Fatalf("rows are not sorted by adapter: %+v", rows)
	}
	if !rows[0].Armed || !rows[0].Silent || rows[0].Receipts != 0 || rows[0].TurnsSinceReceipt != 2 {
		t.Errorf("dead channel row = %+v, want armed+silent with 0 receipts and 2 silent turns", rows[0])
	}
	if rows[1].Armed || rows[1].Silent {
		t.Errorf("unwatched channel row = %+v, want neither armed nor silent — publishing it as healthy would be a false all-clear", rows[1])
	}
}

// TestHookLiveness_SnapshotIsSafeAlongsideOnActivity pins the one property the
// CacheBloatDetector it mirrors does not need: its writer is the detector's
// event loop, but Snapshot is read from the diagnostics HTTP handler. Run under
// -race, this fails on an unsynchronised implementation.
func TestHookLiveness_SnapshotIsSafeAlongsideOnActivity(t *testing.T) {
	h := newLivenessHarness(3)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			h.turn("s")
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			_ = h.w.Snapshot()
			_ = h.w.Silent(livenessAdapter)
		}
	}()
	wg.Wait()
}

// TestHookLiveness_NilWatchdogIsInert: SetHookLivenessWatchdog(nil) is the
// documented way to disable it, so every entry point must tolerate a nil
// receiver the way CacheBloatDetector's does.
func TestHookLiveness_NilWatchdogIsInert(t *testing.T) {
	var w *HookLivenessWatchdog
	w.SetChannelReady(nil)
	w.Forget("s")
	if w.enabled() || w.Silent("x") || w.Snapshot() != nil {
		t.Error("a nil watchdog must be completely inert")
	}
	if got := w.OnActivity(&session.SessionState{SessionID: "s", Metrics: &session.SessionMetrics{}}); got.Transition != HookLivenessUnchanged {
		t.Errorf("OnActivity on a nil watchdog returned %v", got.Transition)
	}
}

// TestHookLiveness_FirstObservationOfASessionIsNotATurn is the review finding
// that a daemon restart alone could convict a healthy channel.
//
// lastDone starts unset, so without a "have I seen this session before" flag an
// unseeded lookup reads false and a session whose transcript is ALREADY at a
// turn boundary registers a rising edge on its very first pass — for a turn
// that completed before the watchdog existed. refreshStaleSessions re-reads
// every persisted working session at startup, so N such sessions donate N
// phantom turns in one tick, with no agent having run and no hook having had a
// chance to arrive.
func TestHookLiveness_FirstObservationOfASessionIsNotATurn(t *testing.T) {
	h := newLivenessHarness(3)

	// Six distinct sessions, each observed exactly once, each already done —
	// the shape of a startup re-read.
	for i := 0; i < 6; i++ {
		sid := fmt.Sprintf("restored-%d", i)
		got := h.w.OnActivity(&session.SessionState{
			SessionID: sid,
			Adapter:   livenessAdapter,
			Metrics:   &session.SessionMetrics{LastEventType: "turn_done"},
		})
		if got.Transition != HookLivenessUnchanged {
			t.Fatalf("session %s: first observation produced %v — a turn that finished before the watchdog was watching is not evidence of silence", sid, got.Transition)
		}
	}
	if h.w.Silent(livenessAdapter) {
		t.Fatal("a daemon restart re-reading persisted sessions convicted a channel that was never given a chance to deliver")
	}

	// And the seeding must not deafen it: real turns on those same sessions
	// still count.
	for i := 0; i < 3; i++ {
		h.turn("restored-0")
	}
	if !h.w.Silent(livenessAdapter) {
		t.Error("seeding the first observation swallowed the real turns that followed")
	}
}

// TestHookLiveness_ForgottenSessionDoesNotDonateAPhantomTurn: Forget deletes
// the rising-edge entry, so a session reaped and later revived is a first
// observation again and must be seeded, not counted.
func TestHookLiveness_ForgottenSessionDoesNotDonateAPhantomTurn(t *testing.T) {
	// Threshold 1, so the single phantom turn this guards against is on its own
	// enough to convict. At 2 the test passes against the bug.
	h := newLivenessHarness(1)
	h.pass("s", false)
	h.w.Forget("s")

	if got := h.pass("s", true); got.Transition != HookLivenessUnchanged {
		t.Fatalf("a revived session's first pass produced %v", got.Transition)
	}
	if h.w.Silent(livenessAdapter) {
		t.Error("reap-then-revive donated a phantom turn")
	}
}
