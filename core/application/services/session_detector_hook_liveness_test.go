package services

import (
	"strings"
	"sync"
	"testing"
	"time"

	"irrlicht/core/domain/lifecycle"
	"irrlicht/core/domain/session"
)

// livenessLogger captures every line at both levels. The package's
// capturingLogger discards LogError, and the watchdog's verdict is deliberately
// an error-level line — so reusing it would assert the demotion is reported by
// reading a field that is never written.
type livenessLogger struct {
	mu    sync.Mutex
	info  []string
	error []string
}

func (l *livenessLogger) LogInfo(_, _, message string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.info = append(l.info, message)
}

func (l *livenessLogger) LogError(_, _, message string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.error = append(l.error, message)
}

func (l *livenessLogger) LogProcessingTime(string, string, int64, int, string) {}
func (l *livenessLogger) Close() error                                         { return nil }

func (l *livenessLogger) joined() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Join(append(append([]string{}, l.info...), l.error...), "\n")
}

// livenessDetector wires the smallest detector that can run checkHookLiveness,
// plus the watchdog harness driving it. Same shape the #1360 ceiling tests use.
type livenessDetector struct {
	d   *SessionDetector
	h   *livenessHarness
	lg  *livenessLogger
	rec *ceilingRecorder
}

func newLivenessDetector(threshold int) *livenessDetector {
	lg := &livenessLogger{}
	rec := &ceilingRecorder{}
	d := newSessionDetector()
	d.log = lg
	d.recorder = rec
	h := newLivenessHarness(threshold)
	d.SetHookLivenessWatchdog(h.w)
	return &livenessDetector{d: d, h: h, lg: lg, rec: rec}
}

// pass drives one processActivity-equivalent pass through checkHookLiveness and
// returns the number of hook-tier holds it released — the value processActivity
// folds into `rebased`. One definition of "a pass", so a change to the metrics
// shape a turn is recognised by cannot leave some tests asserting against a
// shape the detector no longer produces.
func (ld *livenessDetector) pass(done bool, at time.Time) int {
	m := &session.SessionMetrics{LastEventType: "turn_done"}
	if !done {
		m.HasOpenToolCall = true
	}
	return ld.d.checkHookLiveness(
		&session.SessionState{SessionID: "s", Adapter: livenessAdapter, Metrics: m}, at)
}

func (ld *livenessDetector) turn(at time.Time) { ld.pass(false, at); ld.pass(true, at) }

func (ld *livenessDetector) kinds(k lifecycle.Kind) []lifecycle.Event {
	var out []lifecycle.Event
	for _, ev := range ld.rec.events {
		if ev.Kind == k {
			out = append(out, ev)
		}
	}
	return out
}

var livenessT0 = time.Date(2026, 8, 9, 9, 0, 0, 0, time.UTC)

// TestSessionDetector_SilentHookChannelReleasesItsPinnedHolds is THE defect
// test for #1368, stated at the altitude the failure is actually felt.
//
// The compound failure: a PermissionRequest hook lands and pins the session at
// waiting with a TierHook hold, then the channel dies. The release POST that
// would retire the hold travels over the same dead channel, so it never
// arrives; the hold outranks every lower tier, so nothing may correct it; and
// #1360's ceiling will not drop it for twelve hours. The session sits at the
// wrong state for a working day.
//
// A watchdog that only logged and only wrote a diagnostics row would leave that
// entirely intact — which is why the answer to "must the demotion also release
// the holds" is yes: the holds ARE the tier the demotion claims to be lowering.
func TestSessionDetector_SilentHookChannelReleasesItsPinnedHolds(t *testing.T) {
	ld := newLivenessDetector(3)
	ld.d.signals.Hold("s", session.SignalPermissionPrompt, session.SignalPayload{}, livenessT0)

	at := livenessT0.Add(2 * time.Hour)
	for i := 0; i < 3; i++ {
		ld.turn(at)
	}

	if ld.d.signals.Held("s", session.SignalPermissionPrompt) {
		t.Fatal("the hold placed by a channel now proven dead is still pinning the session — the demotion demoted nothing")
	}

	// The altitude claim: with the hold gone, the classifier's inputs are no
	// longer being rewritten by the dead channel.
	m := &session.SessionMetrics{LastEventType: "turn_done"}
	ld.d.signals.Overlay("s", m, at)
	if m.PermissionPending {
		t.Error("Overlay still applies the released hold — the session would stay pinned at waiting")
	}

	released := ld.kinds(lifecycle.KindHookHoldReleased)
	if len(released) != 1 {
		t.Fatalf("expected exactly one hook_hold_released event, got %d: %+v", len(released), ld.rec.events)
	}
	ev := released[0]
	if ev.SignalKind != string(session.SignalPermissionPrompt) {
		t.Errorf("SignalKind = %q, want %q", ev.SignalKind, session.SignalPermissionPrompt)
	}
	if ev.SignalTier != session.TierHook.String() {
		t.Errorf("SignalTier = %q, want %q — the tier is what made the hold consequential", ev.SignalTier, session.TierHook.String())
	}
	if ev.HeldForMS != (2 * time.Hour).Milliseconds() {
		t.Errorf("HeldForMS = %d, want %d", ev.HeldForMS, (2 * time.Hour).Milliseconds())
	}
	if ev.CeilingMS != 0 {
		t.Errorf("CeilingMS = %d, want 0 — a ceiling expiry (#1360) and a dead-channel release (#1368) are different diagnoses and must not be told apart by guesswork", ev.CeilingMS)
	}
	if !ev.Timestamp.Equal(at) {
		t.Errorf("Timestamp = %v, want the pass clock %v — a wall-time stamp would not replay", ev.Timestamp, at)
	}
	if !strings.Contains(ld.lg.joined(), "stopped delivering") {
		t.Errorf("the release must be explained in the log, got:\n%s", ld.lg.joined())
	}
}

// TestSessionDetector_SilentVerdictIsLoggedLoudlyAndRecorded is scope item 2's
// "log loudly" half. Error level, because outbound.Logger has no level between
// Info and Error and Info is where this class of failure was already hiding —
// the argument #1364's IgnoreUnknownEvent makes for the same choice.
func TestSessionDetector_SilentVerdictIsLoggedLoudlyAndRecorded(t *testing.T) {
	ld := newLivenessDetector(2)
	at := livenessT0
	ld.turn(at)
	ld.turn(at)

	if len(ld.lg.error) != 1 {
		t.Fatalf("expected exactly one error-level line for the verdict, got %d: %v", len(ld.lg.error), ld.lg.error)
	}
	line := ld.lg.error[0]
	for _, want := range []string{livenessAdapter, "zero hook requests", "granted", "install reported success", "transcript"} {
		if !strings.Contains(line, want) {
			t.Errorf("the verdict line must mention %q so a reader can tell this failure from its two neighbours; got:\n%s", want, line)
		}
	}

	silent := ld.kinds(lifecycle.KindHookChannelSilent)
	if len(silent) != 1 {
		t.Fatalf("expected one hook_channel_silent event, got %d: %+v", len(silent), ld.rec.events)
	}
	ev := silent[0]
	if ev.Adapter != livenessAdapter {
		t.Errorf("Adapter = %q, want %q", ev.Adapter, livenessAdapter)
	}
	if ev.SignalTier != session.TierTranscript.String() {
		t.Errorf("SignalTier = %q, want %q — the event should say what the adapter was demoted TO", ev.SignalTier, session.TierTranscript.String())
	}
	if ev.SilentTurns != 2 || ev.TurnThreshold != 2 {
		t.Errorf("SilentTurns/TurnThreshold = %d/%d, want 2/2", ev.SilentTurns, ev.TurnThreshold)
	}
}

// TestSessionDetector_LiveHookChannelKeepsItsHolds is a LOCK: it pins behaviour
// that must not change, and it passes by construction on main because nothing
// released holds there at all. Its job is to fail if the release ever escapes
// the silent-channel condition and starts dropping holds on a healthy channel —
// which would break every hook-driven state transition in the product.
func TestSessionDetector_LiveHookChannelKeepsItsHolds(t *testing.T) {
	ld := newLivenessDetector(3)
	ld.d.signals.Hold("s", session.SignalPermissionPrompt, session.SignalPayload{}, livenessT0)

	at := livenessT0.Add(time.Minute)
	for i := 0; i < 10; i++ {
		ld.h.hookArrives()
		ld.turn(at)
	}

	if !ld.d.signals.Held("s", session.SignalPermissionPrompt) {
		t.Fatal("a live channel's hold was released — hook-driven waiting detection would stop working")
	}
	if len(ld.rec.events) != 0 {
		t.Errorf("a healthy channel must record nothing, got %+v", ld.rec.events)
	}
}

// TestSessionDetector_RecoveredChannelStopsReleasingHolds closes the loop: once
// hooks resume, a newly placed hold must survive. A released hold is not a
// tombstone.
func TestSessionDetector_RecoveredChannelStopsReleasingHolds(t *testing.T) {
	ld := newLivenessDetector(2)
	at := livenessT0
	ld.turn(at)
	ld.turn(at)
	if !ld.h.w.Silent(livenessAdapter) {
		t.Fatal("precondition: expected a silent channel")
	}

	ld.h.hookArrives()
	ld.pass(true, at)
	ld.d.signals.Hold("s", session.SignalPermissionPrompt, session.SignalPayload{}, at)
	ld.pass(false, at)

	if !ld.d.signals.Held("s", session.SignalPermissionPrompt) {
		t.Fatal("a hold placed after recovery was released — the demotion behaved as a latch")
	}
	if len(ld.kinds(lifecycle.KindHookChannelRecovered)) != 1 {
		t.Errorf("expected one hook_channel_recovered event, got %+v", ld.rec.events)
	}
}

// TestSessionDetector_HookLivenessDisabledDoesNothing: the kill switch, and the
// nil-watchdog path the detector's other collaborators all honour.
func TestSessionDetector_HookLivenessDisabledDoesNothing(t *testing.T) {
	for name, threshold := range map[string]int{"kill_switch": 0, "enabled": 3} {
		t.Run(name, func(t *testing.T) {
			ld := newLivenessDetector(threshold)
			if name == "enabled" {
				ld.d.SetHookLivenessWatchdog(nil)
			}
			ld.d.signals.Hold("s", session.SignalPermissionPrompt, session.SignalPayload{}, livenessT0)
			for i := 0; i < 20; i++ {
				ld.turn(livenessT0)
			}
			if !ld.d.signals.Held("s", session.SignalPermissionPrompt) {
				t.Error("a disabled watchdog must not touch holds")
			}
			if len(ld.rec.events) != 0 || len(ld.lg.error) != 0 {
				t.Errorf("a disabled watchdog must be silent, got %+v / %v", ld.rec.events, ld.lg.error)
			}
		})
	}
}

// TestSessionDetector_CheckHookLivenessReportsWhatItReleased is the review
// finding that the un-pinning was being handed straight back to #1366's grace
// timer.
//
// checkHookLiveness deletes the hold BEFORE Overlay runs, so a watchdog release
// can never show up in the `expiries` slice that processActivity folds into
// `rebased`. Without a separate signal the pass is not authoritative,
// admitTransition defers to the dwell, and the correction the watchdog exists
// to deliver in minutes is debounced — while admitTransition's own doc argues
// the identical ceiling-expiry case must bypass the dwell precisely because the
// hold is gone and nothing re-asserts it.
//
// The count returned here is that signal; processActivity ORs it into `rebased`
// alongside len(expiries).
func TestSessionDetector_CheckHookLivenessReportsWhatItReleased(t *testing.T) {
	// Threshold 1: with the first observation of a session now seeded rather
	// than counted, the pass below is the seed and the one after it is the
	// first real turn boundary.
	ld := newLivenessDetector(1)
	ld.d.signals.Hold("s", session.SignalPermissionPrompt, session.SignalPayload{}, livenessT0)
	ld.d.signals.Hold("s", session.SignalIdlePrompt, session.SignalPayload{}, livenessT0)

	at := livenessT0.Add(time.Minute)
	if got := ld.pass(false, at); got != 0 {
		t.Fatalf("a pass with no verdict reported %d releases, want 0 — a false positive here makes every pass authoritative and disables the dwell", got)
	}

	// The first real turn boundary crosses the threshold; both hook-tier holds go.
	released := ld.pass(true, at)
	if released != 2 {
		t.Fatalf("reported %d releases, want 2 — processActivity reads this to decide the pass is authoritative, so a zero here silently re-debounces the un-pinning", released)
	}

	// And once everything is released, later sweeps report nothing, so a
	// permanently-dead channel does not permanently disable the dwell.
	if got := ld.pass(false, at); got != 0 {
		t.Fatalf("a sweep that released nothing reported %d — a silent channel would make every subsequent pass authoritative", got)
	}
}

// TestSessionDetector_HookLivenessToleratesAMissingHoldStore is the review's
// nil-guard finding: forgetSessionScopedState guards d.signals with an explicit
// "not nil-safe, struct-literal test detectors reach this without one", and
// this path must not take the opposite position.
func TestSessionDetector_HookLivenessToleratesAMissingHoldStore(t *testing.T) {
	ld := newLivenessDetector(1)
	ld.d.signals = nil

	// Two passes, so the channel is actually convicted and the sweep — the only
	// line that touches d.signals — is really reached. One pass returns before
	// it and would prove nothing.
	ld.pass(false, livenessT0)
	if got := ld.pass(true, livenessT0); got != 0 {
		t.Fatalf("got %d releases from a detector with no hold store", got)
	}
}
