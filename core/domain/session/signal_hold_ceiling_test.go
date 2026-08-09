package session

import (
	"testing"
	"time"
)

// beyondAnyHookCeiling is an elapsed time that must exceed every ceiling
// declared on a TierHook persistent row. Deliberately framed as "absurdly
// long" rather than as any particular constant: these tests assert that a
// ceiling *exists*, not what it is tuned to, so retuning a ceiling must not be
// able to make them lie. A row whose ceiling approached this value would be a
// ceiling in name only, which is what
// TestSignalPolicies_HookPersistentHoldsDeclareACeiling exists to catch.
const beyondAnyHookCeiling = 48 * time.Hour

// maxDefensibleHookCeiling and minDefensibleHookCeiling bracket the range a
// hook ceiling may sit in. They are not tuning — they are typo detection at
// both ends. Below the floor, an unsuffixed literal (ceiling: 5, which Go
// reads as five nanoseconds) would expire every hold on the pass that placed
// it, turning the fix into a worse bug than the one it replaces. Above the
// cap, a ceiling is one in name only and the session is pinned for longer than
// any daemon process realistically lives, which is the #1360 defect wearing a
// number.
const (
	minDefensibleHookCeiling = time.Minute
	maxDefensibleHookCeiling = 24 * time.Hour
)

// ceilingProbeFixtures are metric states used to find one under which a given
// row is *not* already stale, so its ceiling can be exercised for real rather
// than vacuously. Ordered cheapest-first; the first match wins.
//
// This is not a per-row table and deliberately so — nothing here has to be
// updated when a row is added, unless none of these states happens to leave
// the new row live, in which case the structural test says exactly that and
// asks for one more entry.
func ceilingProbeFixtures() []*SessionMetrics {
	return []*SessionMetrics{
		{},
		{LastEventType: "turn_done"},
		{LastEventType: "turn_done", HookTurnDone: true},
		{LastEventType: "assistant"},
		{HasOpenToolCall: true, LastOpenToolNames: []string{"Edit"}},
	}
}

// TestSignalPolicies_HookPersistentHoldsDeclareACeiling is the structural
// invariant #1360 exists to install, and the part of it that outlives the two
// rows fixed here.
//
// The rule it enforces: a hold that is TierHook and persistent MUST declare a
// wall-clock ceiling. Those two properties together are what make a missed
// release unrecoverable — TierHook means no lower-tier signal may retire the
// hold, and persistent means it re-applies on every pass until something does.
// A row with both and no ceiling can pin a session at waiting for the life of
// the daemon process, and nothing in the system can talk it down. That is not
// a tuning question; it is a correctness property of the tier ladder, and the
// author of the next hook adapter should not have to know the history to get
// it right.
//
// consumeOnce rows are exempt because they cannot pin anything: the hold is
// dropped by the pass that applies it. Non-TierHook rows are exempt because a
// lower tier can still correct them — SignalOpenToolStalled is TierTranscript,
// and its staleness reads freshly-rebuilt transcript metrics, so the parse
// failing retires it rather than freezing it.
//
// This is a structural test, so it passes by construction against a correct
// table and its whole value is that it CAN fail. Demonstrated by mutating the
// table three ways:
//
//   - deleting `ceiling: permissionPromptHoldTimeout` from the
//     SignalPermissionPrompt row: "declares a wall-clock ceiling" fails,
//     naming that row — this is the exact regression the test exists for, and
//     the state main was in before #1360;
//   - `ceiling: 5` on that row (the unsuffixed-literal typo): the floor check
//     fails at 5ns;
//   - `ceiling: 30 * 24 * time.Hour`: the cap check fails at 720h.
//
// The first catches a forgotten ceiling, the other two catch a ceiling that is
// present but does not bound anything useful.
func TestSignalPolicies_HookPersistentHoldsDeclareACeiling(t *testing.T) {
	for _, p := range signalPolicies {
		if p.tier != TierHook || p.consumeOnce {
			continue
		}

		if p.ceiling <= 0 {
			t.Errorf("policy %q is TierHook and persistent but declares no ceiling — "+
				"a missed release pins the session at waiting forever, and no lower tier may correct it (#1360). "+
				"Add a `ceiling:` with a comment stating the real-world duration it is calibrated against; "+
				"see permissionPromptHoldTimeout for the shape",
				p.kind)
			continue
		}
		if p.ceiling < minDefensibleHookCeiling {
			t.Errorf("policy %q declares ceiling %v, below the %v floor — a duration literal without a unit? "+
				"A ceiling this short expires the hold on the pass that placed it",
				p.kind, p.ceiling, minDefensibleHookCeiling)
		}
		if p.ceiling > maxDefensibleHookCeiling {
			t.Errorf("policy %q declares ceiling %v, above the %v cap — that is a ceiling in name only; "+
				"the session stays pinned longer than the daemon is likely to run",
				p.kind, p.ceiling, maxDefensibleHookCeiling)
		}

		// A declared ceiling that Overlay does not act on would satisfy every
		// check above and fix nothing, so exercise it end to end: find a
		// metric state under which this row is not already stale, then assert
		// Overlay both drops the hold at the deadline and *reports* the
		// expiry. Reporting is the discriminating half — stale drops a hold
		// too, but only a ceiling returns a SignalExpiry, so this cannot pass
		// by accident on a row whose staleness rule fired instead.
		live := liveFixtureFor(p)
		if live == nil {
			t.Errorf("policy %q is stale under every fixture in ceilingProbeFixtures, so its ceiling cannot be "+
				"exercised — add a metric state to that helper under which this row is still live at t0",
				p.kind)
			continue
		}

		h := NewSignalHolds()
		h.Hold(holdSID, p.kind, SignalPayload{}, holdT0)
		expiries := h.Overlay(holdSID, live, holdT0.Add(p.ceiling))

		if h.Held(holdSID, p.kind) {
			t.Errorf("policy %q declares ceiling %v but the hold survived it — the field is not being enforced",
				p.kind, p.ceiling)
		}
		if !reportsExpiryFor(expiries, p.kind) {
			t.Errorf("policy %q expired at %v without reporting a SignalExpiry — an unobservable ceiling is a "+
				"debugging blind spot, not a fix (#1360 scope item 4)",
				p.kind, p.ceiling)
		}
	}
}

// liveFixtureFor returns the first probe fixture under which p is not already
// stale at holdT0, or nil if every one of them retires it immediately.
func liveFixtureFor(p signalPolicy) *SessionMetrics {
	for _, m := range ceilingProbeFixtures() {
		c := holdContext{Metrics: m, HeldSince: holdT0, Now: holdT0}
		if p.stale == nil || !p.stale(c) {
			return m
		}
	}
	return nil
}

func reportsExpiryFor(expiries []SignalExpiry, kind SignalKind) bool {
	for _, e := range expiries {
		if e.Kind == kind {
			return true
		}
	}
	return false
}

// TestSignalHolds_CeilingExpiryIsReported pins the payload of the expiry
// report itself — the data the detector turns into a log line and a
// KindHoldExpired lifecycle event. Without this, SignalExpiry could be
// returned with zero-valued fields and every other test in this file would
// still pass.
//
// A defect test for #1360 scope item 4: nothing reported ceiling expiries
// before, so there was no shape to assert.
func TestSignalHolds_CeilingExpiryIsReported(t *testing.T) {
	h := NewSignalHolds()
	h.Hold(holdSID, SignalPermissionPrompt, SignalPayload{}, holdT0)

	overdue := permissionPromptHoldTimeout + 7*time.Minute
	expiries := h.Overlay(holdSID, &SessionMetrics{LastEventType: "turn_done"}, holdT0.Add(overdue))

	if len(expiries) != 1 {
		t.Fatalf("expected exactly one reported expiry, got %d: %+v", len(expiries), expiries)
	}
	e := expiries[0]
	if e.Kind != SignalPermissionPrompt {
		t.Errorf("Kind = %q, want %q", e.Kind, SignalPermissionPrompt)
	}
	if e.Tier != TierHook {
		t.Errorf("Tier = %v, want %v — the tier is what makes the expiry consequential", e.Tier, TierHook)
	}
	if e.HeldFor != overdue {
		t.Errorf("HeldFor = %v, want %v — the trace must say how long the session was pinned", e.HeldFor, overdue)
	}
	if e.Ceiling != permissionPromptHoldTimeout {
		t.Errorf("Ceiling = %v, want %v — carried so a recorded event stays readable after retuning",
			e.Ceiling, permissionPromptHoldTimeout)
	}
}

// TestSignalHolds_NormalStalenessIsNotReportedAsAnExpiry is the other half of
// the discrimination: a hold that ended for its own declared reason must not
// be reported as having run out of time. If it were, the new log line and
// lifecycle event would fire on every ordinary permission denial and the
// signal-to-noise of the trace — the entire point of scope item 4 — would be
// gone.
func TestSignalHolds_NormalStalenessIsNotReportedAsAnExpiry(t *testing.T) {
	h := NewSignalHolds()
	h.Hold(holdSID, SignalPermissionPrompt, SignalPayload{}, holdT0)

	// The user rejected the prompt: the transcript marker is the end-of-life
	// notice, well inside the ceiling.
	denied := &SessionMetrics{LastWasToolDenial: true}
	expiries := h.Overlay(holdSID, denied, holdT0.Add(time.Minute))

	if h.Held(holdSID, SignalPermissionPrompt) {
		t.Fatal("a denial must still retire the hold — #1360 must not weaken release semantics")
	}
	if len(expiries) != 0 {
		t.Errorf("a normal staleness release must not be reported as a ceiling expiry, got %+v", expiries)
	}
}

// TestSignalHolds_PermissionPrompt_MissedReleaseDoesNotPinForever is the #1360
// defect test, and it FAILED before the ceiling existed.
//
// SignalPermissionPrompt is TierHook and persistent, so it outranks every
// transcript-tier signal by design — that is what makes the correction stick,
// and also what makes a missed release unrecoverable. Its normal end-of-life
// is PostToolUse/PostToolUseFailure calling Release; its only staleness rule
// was LastWasToolDenial, one claudecode-shaped transcript marker. If the
// adapter renames that event, or the daemon never sees the POST (crash,
// restart, port change, uninstalled hook), neither path fires and no lower
// tier is permitted to retire the hold — the session reads waiting forever.
//
// The scenario below is exactly that: hold the prompt, never release it, never
// produce a denial marker, and let the clock run. Pre-fix the hold survived
// arbitrary elapsed time and kept re-applying PermissionPending on every pass.
func TestSignalHolds_PermissionPrompt_MissedReleaseDoesNotPinForever(t *testing.T) {
	// missedRelease is what the metrics look like when the release was lost:
	// nothing in the transcript contradicts the prompt, so the metric-driven
	// staleness rule stays false forever.
	missedRelease := func() *SessionMetrics {
		return &SessionMetrics{LastEventType: "turn_done"}
	}

	t.Run("a fresh hold still applies", func(t *testing.T) {
		// A LOCK, not a defect assertion: it passed before the ceiling too.
		// It is here so a ceiling that fired immediately — the opposite
		// failure — could not pass this file.
		h := NewSignalHolds()
		h.Hold(holdSID, SignalPermissionPrompt, SignalPayload{}, holdT0)

		m := missedRelease()
		h.Overlay(holdSID, m, holdT0.Add(time.Second))
		if !m.PermissionPending {
			t.Fatal("a prompt that just opened must still apply — the ceiling must not fire on arrival")
		}
		if !h.Held(holdSID, SignalPermissionPrompt) {
			t.Fatal("a fresh prompt hold must survive the pass that applied it")
		}
	})

	t.Run("an unreleased hold is dropped once its ceiling elapses", func(t *testing.T) {
		h := NewSignalHolds()
		h.Hold(holdSID, SignalPermissionPrompt, SignalPayload{}, holdT0)

		m := missedRelease()
		h.Overlay(holdSID, m, holdT0.Add(beyondAnyHookCeiling))

		if m.PermissionPending {
			t.Error("PermissionPending must NOT be re-applied past the ceiling — this is what pins the session at waiting")
		}
		if h.Held(holdSID, SignalPermissionPrompt) {
			t.Error("the expired hold must be dropped, or it re-applies on every subsequent pass")
		}
	})

	t.Run("the drop is permanent, not one skipped pass", func(t *testing.T) {
		h := NewSignalHolds()
		h.Hold(holdSID, SignalPermissionPrompt, SignalPayload{}, holdT0)
		h.Overlay(holdSID, missedRelease(), holdT0.Add(beyondAnyHookCeiling))

		later := missedRelease()
		h.Overlay(holdSID, later, holdT0.Add(beyondAnyHookCeiling+time.Minute))
		if later.PermissionPending {
			t.Error("a hold dropped by its ceiling must stay dropped")
		}
	})
}

// TestSignalHolds_IdlePrompt_MissedReleaseDoesNotPinForever is the #1360
// defect test for the second in-scope row, and it FAILED before the ceiling
// existed.
//
// SignalIdlePrompt's staleness rule is !IsAgentDone(), which retires the hold
// the moment the user replies or a tool opens. Both of those are transcript
// observations, so the rule is only as good as the parse: a transcript that
// stops being read, an adapter whose turn-done marker changes shape, or a
// session whose transcript is rotated away all leave IsAgentDone stuck true.
// TierHook then forbids any lower tier from correcting it.
//
// Note this row's ceiling is calibrated much longer than the permission
// prompt's — an idle prompt is genuinely long-lived — so this test's absurd
// elapsed time is doing real work.
func TestSignalHolds_IdlePrompt_MissedReleaseDoesNotPinForever(t *testing.T) {
	// stillIdle keeps IsAgentDone true, which is the honest reading of a
	// session sitting at the prompt — and, when the release is missed, is
	// also indistinguishable from one whose transcript stopped moving.
	stillIdle := func() *SessionMetrics {
		return &SessionMetrics{LastEventType: "turn_done"}
	}

	t.Run("a fresh hold still applies", func(t *testing.T) {
		// LOCK — passed before the ceiling; guards the opposite failure.
		h := NewSignalHolds()
		h.Hold(holdSID, SignalIdlePrompt, SignalPayload{}, holdT0)

		m := stillIdle()
		h.Overlay(holdSID, m, holdT0.Add(time.Second))
		if !m.IdlePromptPending {
			t.Fatal("the ~6s-late correction must still land — the ceiling must not fire on arrival")
		}
	})

	t.Run("an unreleased hold is dropped once its ceiling elapses", func(t *testing.T) {
		h := NewSignalHolds()
		h.Hold(holdSID, SignalIdlePrompt, SignalPayload{}, holdT0)

		m := stillIdle()
		h.Overlay(holdSID, m, holdT0.Add(beyondAnyHookCeiling))

		if m.IdlePromptPending {
			t.Error("IdlePromptPending must NOT be re-applied past the ceiling")
		}
		if h.Held(holdSID, SignalIdlePrompt) {
			t.Error("the expired hold must be dropped, or it re-applies on every subsequent pass")
		}
	})
}
