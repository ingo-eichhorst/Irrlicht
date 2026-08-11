package contracttesting

import (
	"fmt"
	"strings"
	"testing"

	"irrlicht/core/domain/permission"
)

// This file is the deliberate mutation for the key-isolation arm added in
// issue #1475, committed rather than described. A contract assertion passes by
// construction against a correct adapter, so its whole value is that it can
// fail — and #1466 is the standing proof that this one could not: claudecode's
// hook receiver read transcripts with "transcripts" denied for the whole of
// its life while AssertPermissionGated was wired at that receiver and green.
//
// Every case below drives a call site whose gating is known-wrong through the
// arms and asserts what each one reports, so the evidence is re-run on every
// build instead of living in a merged PR body.

const (
	selfTestHooks       = "hooks"
	selfTestTranscripts = "transcripts"
)

// recordingReporter captures what an arm reports instead of failing the
// enclosing test.
type recordingReporter struct{ errs []string }

func (r *recordingReporter) Helper() {}

func (r *recordingReporter) Errorf(format string, args ...any) {
	r.errs = append(r.errs, fmt.Sprintf(format, args...))
}

func (r *recordingReporter) failed() bool { return len(r.errs) > 0 }

func (r *recordingReporter) report() string { return strings.Join(r.errs, "; ") }

// fakeReceiver stands in for a hook receiver: it dispatches only once the
// consent gate answers granted for every key in gatedOn. gatedOn is the
// mutation knob — naming the wrong key, or none, reproduces the two shapes
// this contract has to tell apart from a correct receiver.
type fakeReceiver struct {
	gate       consentSetter
	gatedOn    []string
	dispatched bool
}

// consentSetter is what a fakeReceiver needs of a gate: drive a key, and
// answer a consent check. *ConsentGate satisfies it; so does keyBlindGate,
// which is the point of the interface.
type consentSetter interface {
	SetState(key string, state permission.State)
	Granted(agentName, key string) bool
}

func (f *fakeReceiver) exercise() {
	f.dispatched = false
	for _, k := range f.gatedOn {
		if !f.gate.Granted("test-agent", k) {
			return
		}
	}
	f.dispatched = true
}

func (f *fakeReceiver) gateFor(key string, others ...string) PermissionGate {
	return PermissionGate{
		Key:       key,
		OtherKeys: others,
		SetState:  f.gate.SetState,
		Exercise:  f.exercise,
		Observe:   func() bool { return f.dispatched },
	}
}

// keyBlindGate is the fake every adapter supplied before #1475: its answer
// does not depend on the key it is handed.
type keyBlindGate struct{ state permission.State }

func (g *keyBlindGate) SetState(_ string, state permission.State) { g.state = state }

func (g *keyBlindGate) Granted(_, _ string) bool { return g.state == permission.StateGranted }

// TestAssertPermissionGated_PassesAgainstACorrectlyGatedReceiver is the
// vacuity guard. Without it, an arm that reported a failure unconditionally
// would satisfy every mutation case below and look like excellent coverage.
// The receiver here honours both permissions, which is the real shape of a
// hook receiver: "hooks" authorises writing our entries, "transcripts"
// authorises reading the file those entries point at.
func TestAssertPermissionGated_PassesAgainstACorrectlyGatedReceiver(t *testing.T) {
	for _, tc := range []struct{ key, other string }{
		{selfTestTranscripts, selfTestHooks},
		{selfTestHooks, selfTestTranscripts},
	} {
		t.Run(tc.key, func(t *testing.T) {
			r := &fakeReceiver{
				gate:    NewConsentGate(),
				gatedOn: []string{selfTestHooks, selfTestTranscripts},
			}
			AssertPermissionGated(t, r.gateFor(tc.key, tc.other))
		})
	}
}

// TestKeyIsolationArm_FailsAReceiverGatedOnTheWrongPermission is #1466 itself,
// reduced to a fixture: a receiver that checks only "hooks" while the effect
// it produces is a transcript read. Denying "transcripts" with "hooks" held
// open is the one state that separates it from a correct receiver, and the
// arm must report it.
func TestKeyIsolationArm_FailsAReceiverGatedOnTheWrongPermission(t *testing.T) {
	r := &fakeReceiver{gate: NewConsentGate(), gatedOn: []string{selfTestHooks}}

	rec := &recordingReporter{}
	assertGatedOnTheNamedKey(rec, r.gateFor(selfTestTranscripts, selfTestHooks))

	if !rec.failed() {
		t.Fatal("key-isolation arm passed a receiver gated on \"hooks\" while the key under test " +
			"was \"transcripts\" — this is exactly the #1466 defect the arm exists to catch")
	}
	if !strings.Contains(rec.report(), selfTestTranscripts) {
		t.Errorf("failure message does not name the key under test: %s", rec.report())
	}
}

// TestKeyIsolationArm_FailsAnUngatedReceiver covers the other mis-gating: no
// consent check at all. The lockstep arms already catch this one; the arm must
// not have become narrower than they are on its way to catching the wrong-key
// case.
func TestKeyIsolationArm_FailsAnUngatedReceiver(t *testing.T) {
	r := &fakeReceiver{gate: NewConsentGate(), gatedOn: nil}

	rec := &recordingReporter{}
	assertGatedOnTheNamedKey(rec, r.gateFor(selfTestTranscripts, selfTestHooks))

	if !rec.failed() {
		t.Fatal("key-isolation arm passed a receiver with no consent check at all")
	}
}

// TestKeyIsolationArm_FailsAKeyBlindWiring pins the arm's write order, which
// is load-bearing and not obvious. The key under test is denied FIRST and the
// other keys opened AFTER, so a SetState that ignores the key it is handed
// ends up holding everything granted, dispatches, and fails here. Reverse the
// two loops and this test goes green: such a wiring would settle at "denied",
// pass, and the contract would once again be satisfiable by precisely the fake
// that hid #1466.
func TestKeyIsolationArm_FailsAKeyBlindWiring(t *testing.T) {
	// The receiver itself is gated correctly — only the wiring is blind.
	r := &fakeReceiver{
		gate:    &keyBlindGate{},
		gatedOn: []string{selfTestHooks, selfTestTranscripts},
	}

	rec := &recordingReporter{}
	assertGatedOnTheNamedKey(rec, r.gateFor(selfTestTranscripts, selfTestHooks))

	if !rec.failed() {
		t.Fatal("key-isolation arm passed a SetState that ignores the key it is handed — " +
			"the arm must deny the key under test before opening the others")
	}
}

// TestTheLockstepArmsCannotSeeAWrongKeyGate is the measured blind spot: the
// three original arms move every key together, so they answer identically for
// a receiver gated on "hooks" and one gated on "transcripts". It is why the
// fourth arm exists, and why #1466 survived a contract wired straight at the
// defective receiver.
//
// If a future change makes one of these arms catch a wrong-key gate on its
// own, RETIRE this test rather than patching it — its subject is a limitation,
// not a behaviour worth preserving.
func TestTheLockstepArmsCannotSeeAWrongKeyGate(t *testing.T) {
	misGated := func() PermissionGate {
		r := &fakeReceiver{gate: NewConsentGate(), gatedOn: []string{selfTestHooks}}
		return r.gateFor(selfTestTranscripts, selfTestHooks)
	}

	for name, arm := range map[string]func(gateReporter, PermissionGate){
		"pending_no_effect":         assertPendingNoEffect,
		"granted_effect_observable": assertGrantedEffectObservable,
		"revoked_effect_undone":     assertRevokedEffectUndone,
	} {
		t.Run(name, func(t *testing.T) {
			rec := &recordingReporter{}
			arm(rec, misGated())
			if rec.failed() {
				t.Fatalf("this arm now catches a wrong-key gate (%s) — retire this test, "+
					"it documents a limitation that no longer holds", rec.report())
			}
		})
	}
}

// TestMalformedGateReason covers the precondition guard. A wiring the contract
// cannot exercise must be refused out loud: silently running three of four
// arms is indistinguishable from a pass, which is the failure mode the whole
// change is about.
func TestMalformedGateReason(t *testing.T) {
	wellFormed := func() PermissionGate {
		r := &fakeReceiver{gate: NewConsentGate(), gatedOn: []string{selfTestHooks}}
		return r.gateFor(selfTestHooks, selfTestTranscripts)
	}

	if reason := malformedGateReason(wellFormed()); reason != "" {
		t.Fatalf("well-formed gate rejected: %s", reason)
	}

	for name, mutate := range map[string]func(*PermissionGate){
		"no_key":              func(g *PermissionGate) { g.Key = "" },
		"no_other_keys":       func(g *PermissionGate) { g.OtherKeys = nil },
		"other_key_is_theKey": func(g *PermissionGate) { g.OtherKeys = []string{g.Key} },
		"no_set_state":        func(g *PermissionGate) { g.SetState = nil },
		"no_exercise":         func(g *PermissionGate) { g.Exercise = nil },
		"no_observe":          func(g *PermissionGate) { g.Observe = nil },
	} {
		t.Run(name, func(t *testing.T) {
			g := wellFormed()
			mutate(&g)
			if reason := malformedGateReason(g); reason == "" {
				t.Error("malformed gate accepted — the contract would run a reduced set of arms silently")
			}
		})
	}
}

// TestConsentGate_AnswersPerKey pins the fake the contract now owns. Three
// adapters had re-invented this after #1466; the value of moving it here is
// entirely that a wiring can no longer supply a key-blind one by accident.
func TestConsentGate_AnswersPerKey(t *testing.T) {
	g := NewConsentGate()

	if g.Granted("any", selfTestHooks) {
		t.Error("an unanswered key must read as pending, not granted")
	}

	g.SetState(selfTestHooks, permission.StateGranted)
	g.SetState(selfTestTranscripts, permission.StateDenied)

	if !g.Granted("any", selfTestHooks) {
		t.Error("granted key reads as not granted")
	}
	if g.Granted("any", selfTestTranscripts) {
		t.Error("denied key reads as granted — the gate is answering key-blind")
	}
}
