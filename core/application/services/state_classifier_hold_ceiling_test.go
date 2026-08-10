package services

import (
	"strings"
	"testing"
	"time"

	"irrlicht/core/domain/lifecycle"
	"irrlicht/core/domain/session"
)

// ceilingRecorder captures lifecycle events for the #1360 expiry assertions.
// Local rather than the package's fakeRecorder, which implements only Record
// and so does not satisfy outbound.EventRecorder.
type ceilingRecorder struct{ events []lifecycle.Event }

func (r *ceilingRecorder) Record(ev lifecycle.Event) { r.events = append(r.events, ev) }
func (r *ceilingRecorder) Close() error              { return nil }

// TestClassify_HookHoldCeilingUnpinsAStuckSession is #1360 stated end to end,
// at the altitude the defect is actually felt: not "a map entry survived" but
// "the badge says waiting and nothing can talk it down". It FAILED before the
// ceiling existed.
//
// The sequence is the unrecoverable one. A PermissionRequest hook lands and
// correctly pins the session at waiting. The release never arrives — the POST
// was lost to a daemon restart, a port change, or an uninstalled hook — and no
// denial marker is ever written, so the metric-driven staleness rule stays
// false. Because SignalPermissionPrompt is TierHook, every lower-tier signal
// that would otherwise reclassify the session is outranked. Pre-fix the final
// pass below still returned waiting, at hook tier, with no elapsed time able
// to change that.
//
// The middle block — repeated passes staying waiting — is a LOCK, not a defect
// assertion. Pinning is the feature; #1360 is only about it having no end.
func TestClassify_HookHoldCeilingUnpinsAStuckSession(t *testing.T) {
	// Must exceed every ceiling declared on a TierHook persistent row. Framed
	// as an absurd elapsed time rather than as a named constant (which is
	// unexported in core/domain/session) so retuning a ceiling cannot make
	// this test lie in either direction.
	const beyondAnyHookCeiling = 48 * time.Hour

	holds := session.NewSignalHolds()
	const sid = "s"

	// A permission-gated tool opens and the hook lands: the user is blocked.
	holds.Hold(sid, session.SignalPermissionPrompt, session.SignalPayload{}, holdT0)

	m := &session.SessionMetrics{LastEventType: "turn_done"}
	holds.Overlay(sid, m, holdT0)
	v := ClassifyStateTiered(session.StateWorking, m)
	if v.State != session.StateWaiting {
		t.Fatalf("the permission hook must pin the session at waiting, got %q", v.State)
	}
	if v.Tier != session.TierHook {
		t.Fatalf("that verdict must be attributed to the hook tier, got %v", v.Tier)
	}
	state := v.State

	// LOCK: while the prompt is plausibly still open the pin must hold across
	// every re-evaluation. This is the behaviour #1360 must not weaken.
	for pass := 1; pass <= 3; pass++ {
		again := &session.SessionMetrics{LastEventType: "turn_done"}
		holds.Overlay(sid, again, holdT0.Add(time.Duration(pass)*time.Minute))
		v = ClassifyStateTiered(state, again)
		if v.State != session.StateWaiting {
			t.Fatalf("pass %d: the pin must hold inside the ceiling, got %q", pass, v.State)
		}
	}

	// The release never came. Once the ceiling elapses the session must stop
	// being pinned and re-classify from whatever evidence it does have.
	unpinned := &session.SessionMetrics{LastEventType: "turn_done"}
	holds.Overlay(sid, unpinned, holdT0.Add(beyondAnyHookCeiling))
	v = ClassifyStateTiered(state, unpinned)

	if v.State == session.StateWaiting {
		t.Errorf("the session is still pinned at waiting after %v — a missed release must not be permanent", beyondAnyHookCeiling)
	}
	if v.Tier == session.TierHook {
		t.Errorf("an expired hold must stop claiming hook authority, got tier %v deciding %q", v.Tier, v.State)
	}
	if unpinned.PermissionPending {
		t.Error("the expired hold must not keep folding PermissionPending onto fresh metrics")
	}
}

// TestClassify_WhatTheUserSeesAfterAPermissionCeilingExpires pins the claim
// that permissionPromptHoldTimeout's rationale rests on, for both populations
// it splits the world into. Before this, every test here established only that
// the ceiling FIRES; none established what a user actually observes afterwards
// — which is the thing that decides whether an hour or a night is the right
// number.
//
// Neither case is a defect test: both describe behaviour the current code
// already has. The first is a LOCK on #488's fallback, and it is the assertion
// that would have caught the original comment's claim that expiry is "soft and
// self-correcting" across the board. The second is a CHARACTERIZATION of the
// residual that claim got wrong, written down so it stops being invisible.
func TestClassify_WhatTheUserSeesAfterAPermissionCeilingExpires(t *testing.T) {
	// Well past the ceiling, without naming the constant: this asserts the
	// shape of the outcome, not the tuning.
	pastAnyCeiling := holdT0.Add(72 * time.Hour)

	t.Run("covered tool: the transcript tier re-derives waiting", func(t *testing.T) {
		// An Edit prompt — one of the four alternatives of claudecode's
		// matcher that isPermissionGatedEditTool also matches.
		holds := session.NewSignalHolds()
		const sid = "s"

		holds.Hold(sid, session.SignalPermissionPrompt, session.SignalPayload{}, holdT0)
		// #488's fallback, armed by the detector the moment the tool is seen
		// open. Arm-once, so its clock starts with the prompt.
		holds.HoldIfAbsent(sid, session.SignalOpenToolStalled, session.SignalPayload{}, holdT0)

		m := &session.SessionMetrics{
			LastEventType:     "assistant",
			HasOpenToolCall:   true,
			LastOpenToolNames: []string{"Edit"},
		}
		holds.Overlay(sid, m, pastAnyCeiling)

		if m.PermissionPending {
			t.Fatal("precondition: the hook hold must have expired, or this proves nothing about the fallback")
		}
		v := ClassifyStateTiered(session.StateWaiting, m)
		if v.State != session.StateWaiting {
			t.Errorf("an edit-tool prompt must still read waiting after the hook ceiling expires — "+
				"SignalOpenToolStalled is the net; got %q", v.State)
		}
		if v.Tier != session.TierTranscript {
			t.Errorf("the re-derived verdict must be attributed to the transcript tier (correctable), got %v", v.Tier)
		}
	})

	t.Run("uncovered tool: nothing re-derives it, the session reads ready", func(t *testing.T) {
		// ExitPlanMode-shaped: the #307 fast path. No tool is open — a plan
		// approval is not a file edit — so isPermissionGatedEditTool matches
		// nothing and #488 never arms.
		//
		// CHARACTERIZATION, not an endorsement. This is the accepted residual
		// that sets the ceiling: the session silently stops advertising that a
		// human is needed, and only the recorded hold_expired event says why.
		// It is why the constant is calibrated against an overnight rather
		// than a lunch break, and why shortening it is not a free tuning knob.
		holds := session.NewSignalHolds()
		const sid = "s"

		holds.Hold(sid, session.SignalPermissionPrompt, session.SignalPayload{}, holdT0)

		m := &session.SessionMetrics{LastEventType: "turn_done"}
		holds.Overlay(sid, m, pastAnyCeiling)

		if m.OpenToolStalled {
			t.Fatal("no edit tool is open, so the #488 fallback must not have armed")
		}
		v := ClassifyStateTiered(session.StateWaiting, m)
		if v.State == session.StateWaiting {
			t.Fatal("this test documents the residual; if a plan approval now survives its ceiling, " +
				"the fallback story changed and permissionPromptHoldTimeout's comment must be rewritten")
		}
		if v.State != session.StateReady {
			t.Errorf("expected the uncovered case to fall through to ready, got %q — "+
				"if this changed, update the rationale it is cited by", v.State)
		}
	})
}

// TestSessionDetector_HoldCeilingExpiryIsLoggedAndRecorded covers #1360 scope
// item 4: a ceiling that rewrites session state without saying so is a new
// debugging blind spot rather than a fix.
//
// Honest framing on red-first: this is a NEW-CAPABILITY test, not a defect
// test. Nothing reported ceiling expiries before, so there was no shape for it
// to assert and it could not have been run red on main — it would not have
// compiled. Its ability to fail was shown instead by deliberate mutation,
// recorded in the PR: dropping the d.record call fails the event assertions,
// dropping the d.log.LogInfo call fails the log assertion.
//
// Both sinks are asserted because they serve different readers and neither
// implies the other — the log line is what a human tailing the daemon sees
// live, the lifecycle event is what the replay harness reads back out of a
// recording.
func TestSessionDetector_HoldCeilingExpiryIsLoggedAndRecorded(t *testing.T) {
	const beyondAnyHookCeiling = 48 * time.Hour

	lg := &capturingLogger{}
	rec := &ceilingRecorder{}
	d := newSessionDetector()
	d.log = lg
	d.recorder = rec

	state := &session.SessionState{
		SessionID: "s",
		Adapter:   "claudecode",
		Metrics:   &session.SessionMetrics{LastEventType: "turn_done"},
	}

	d.signals.Hold(state.SessionID, session.SignalPermissionPrompt, session.SignalPayload{}, holdT0)

	at := holdT0.Add(beyondAnyHookCeiling)
	d.reportHoldExpiries(state, d.signals.Overlay(state.SessionID, state.Metrics, at), at)

	if lg.calls != 1 {
		t.Fatalf("expected exactly one log line for the expiry, got %d", lg.calls)
	}
	if !strings.Contains(lg.message, string(session.SignalPermissionPrompt)) {
		t.Errorf("the log line must name the signal that expired, got %q", lg.message)
	}
	if lg.sessionID != state.SessionID {
		t.Errorf("the log line must be attributed to its session, got %q", lg.sessionID)
	}

	if len(rec.events) != 1 {
		t.Fatalf("expected exactly one lifecycle event, got %d: %+v", len(rec.events), rec.events)
	}
	ev := rec.events[0]
	if ev.Kind != lifecycle.KindHoldExpired {
		t.Errorf("Kind = %q, want %q", ev.Kind, lifecycle.KindHoldExpired)
	}
	if ev.SessionID != state.SessionID || ev.Adapter != state.Adapter {
		t.Errorf("the event must identify its session and adapter, got %q/%q", ev.SessionID, ev.Adapter)
	}
	if ev.SignalKind != string(session.SignalPermissionPrompt) {
		t.Errorf("SignalKind = %q, want %q", ev.SignalKind, session.SignalPermissionPrompt)
	}
	if ev.SignalTier != session.TierHook.String() {
		t.Errorf("SignalTier = %q, want %q — the tier is what makes the expiry consequential",
			ev.SignalTier, session.TierHook.String())
	}
	if ev.HeldForMS != beyondAnyHookCeiling.Milliseconds() {
		t.Errorf("HeldForMS = %d, want %d", ev.HeldForMS, beyondAnyHookCeiling.Milliseconds())
	}
	if ev.CeilingMS <= 0 || ev.CeilingMS >= ev.HeldForMS {
		t.Errorf("CeilingMS = %d must be positive and below HeldForMS %d, or the trace cannot show it was overdue",
			ev.CeilingMS, ev.HeldForMS)
	}
	if !ev.Timestamp.Equal(at) {
		t.Errorf("Timestamp = %v, want the pass clock %v — an expiry stamped with wall time would not replay",
			ev.Timestamp, at)
	}
}

// TestSessionDetector_NormalHoldReleaseIsNotReported is the noise-floor half.
// A permission denial is the expected end-of-life path and happens constantly;
// if it emitted a log line and a lifecycle event, the rare expiry that
// actually indicates a lost release would be buried, which defeats the purpose
// of recording it at all.
func TestSessionDetector_NormalHoldReleaseIsNotReported(t *testing.T) {
	lg := &capturingLogger{}
	rec := &ceilingRecorder{}
	d := newSessionDetector()
	d.log = lg
	d.recorder = rec

	state := &session.SessionState{
		SessionID: "s",
		Adapter:   "claudecode",
		Metrics:   &session.SessionMetrics{LastWasToolDenial: true},
	}

	d.signals.Hold(state.SessionID, session.SignalPermissionPrompt, session.SignalPayload{}, holdT0)

	at := holdT0.Add(time.Minute)
	d.reportHoldExpiries(state, d.signals.Overlay(state.SessionID, state.Metrics, at), at)

	if d.signals.Held(state.SessionID, session.SignalPermissionPrompt) {
		t.Fatal("precondition: the denial must have retired the hold, or this test proves nothing")
	}
	if lg.calls != 0 {
		t.Errorf("a normal staleness release must log nothing, got %q", lg.message)
	}
	if len(rec.events) != 0 {
		t.Errorf("a normal staleness release must record nothing, got %+v", rec.events)
	}
}
