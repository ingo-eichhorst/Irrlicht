package contracttesting

import (
	"fmt"
	"sync"
	"testing"

	"irrlicht/core/domain/permission"
)

// PermissionGate wires one declared permission's consent-gated call site
// into AssertPermissionGated.
//
// Key names the permission under test — the one this call site must honour.
// OtherKeys names permissions it must NOT be gated on; the contract holds
// them open while Key is denied, which is the only condition under which a
// mis-gated call site looks different from a correct one.
//
// SetState drives ONE key to one state through whatever mechanism the call
// site actually checks — a keyed fake ConsentGranter for a live per-request
// check (an HTTP handler, input forwarding), or the permission's real
// Apply/Remove closures for an install-type permission (a hook file, a
// config block). Exercise performs the action the permission is supposed to
// gate. Observe reports whether the gated effect is currently present.
//
// Use ConsentGate for the live-per-request flavour rather than hand-rolling a
// keyed fake: SetState is then satisfied by a method that cannot ignore the
// key it is handed.
type PermissionGate struct {
	// Key is the permission key whose consent this call site must honour.
	Key string

	// OtherKeys are permissions this call site must not be gated on —
	// typically the rest of what the same adapter declares. At least one is
	// required: with nothing to hold open, "denied" is indistinguishable
	// from "denied along with everything else", which is precisely the
	// blind spot this contract had (issue #1475).
	OtherKeys []string

	// SetState drives one key to one state. It MUST honour the key it is
	// passed — a fake that answers identically for every key is what let a
	// receiver read transcripts with "transcripts" denied for the whole of
	// its life (issue #1466), and the key-isolation arm below is written so
	// that such a fake fails rather than passes.
	SetState func(key string, state permission.State)

	// Exercise performs the action the permission gates.
	Exercise func()

	// Observe reports whether the gated effect is currently present.
	Observe func() bool
}

// gateReporter is the slice of *testing.T the individual arms use. Taking an
// interface rather than *testing.T is what lets this file's own tests drive an
// arm against a deliberately mis-gated call site and assert it goes red —
// a contract assertion passes by construction against a correct adapter, so
// its whole value is that it can fail, and nothing re-runs evidence that
// exists only in a merged PR body.
type gateReporter interface {
	Helper()
	Errorf(format string, args ...any)
}

// AssertPermissionGated runs the issue #797 contract against g: no
// observable effect while the permission is pending, the effect observable
// once granted, and the effect undone after a subsequent revoke. Each
// state transition is followed by an Exercise + Observe pair, so a call
// site that forgot to check consent is caught even when the underlying
// resource (a hook entry, a config block) was left behind by an earlier
// grant.
//
// A fourth arm (issue #1475) covers what the first three cannot: they move
// every key in lockstep, so they answer identically whether the call site
// reads g.Key or some other permission entirely. Holding g.Key denied while
// OtherKeys are granted is the one state that tells those apart.
//
// The arm is load-bearing for a LIVE per-request gate — a handler that reads
// a key from a ConsentGranter on every request, which is where #1466 lived.
// For an install-type permission it is weak by construction: the wiring holds
// that permission's own Apply/Remove closures, so "gated on the wrong key" is
// not representable there, and the arm degenerates to asserting that another
// permission's effects do not produce this one's. That is worth little, but it
// is not nothing, and a uniform contract with no opt-out is worth more than a
// flag a live-gate adapter could also reach for.
//
// What this still does not do is make the obligation unforgettable: a receiver
// must be wired once per permission it must honour, and nothing fails if an
// author wires only one of them. Issue #1488 is the chokepoint move that would
// remove that last act of memory — folding the consent key into
// hookjson.DecodeConfined, which core/architecture_hookbody_test.go already
// forces every hook receiver through.
func AssertPermissionGated(t *testing.T, g PermissionGate) {
	t.Helper()
	if reason := malformedGateReason(g); reason != "" {
		t.Fatal(reason)
	}

	t.Run("pending_no_effect", func(t *testing.T) { assertPendingNoEffect(t, g) })
	t.Run("granted_effect_observable", func(t *testing.T) { assertGrantedEffectObservable(t, g) })
	t.Run("revoked_effect_undone", func(t *testing.T) { assertRevokedEffectUndone(t, g) })
	t.Run("gated_on_the_named_key", func(t *testing.T) { assertGatedOnTheNamedKey(t, g) })
}

// malformedGateReason reports why the contract cannot actually exercise g, or
// "" when it can. It returns a reason instead of failing so that the shapes it
// rejects are themselves testable — a guard nobody can drive is the same kind
// of unverifiable check as the one this file exists to remove. The caller
// fails loudly on a non-empty reason rather than running a reduced set of arms
// that reads as a pass.
func malformedGateReason(g PermissionGate) string {
	if g.SetState == nil || g.Exercise == nil || g.Observe == nil {
		return "PermissionGate needs SetState, Exercise and Observe"
	}
	if g.Key == "" {
		return "PermissionGate.Key is empty — name the permission this call site must be gated on"
	}
	if len(g.OtherKeys) == 0 {
		return fmt.Sprintf("PermissionGate.OtherKeys is empty — the key-isolation arm cannot run, so %q "+
			"would only be shown to be gated on something rather than on %q (issue #1475). "+
			"Name at least one permission this call site must NOT be gated on.", g.Key, g.Key)
	}
	for _, k := range g.OtherKeys {
		if k == g.Key {
			return fmt.Sprintf("PermissionGate.OtherKeys repeats the key under test (%q) — "+
				"the key-isolation arm would grant and deny the same permission", g.Key)
		}
	}
	return ""
}

// setEveryKey drives the key under test and every other key to state. The
// first three arms move them in lockstep on purpose: a call site legitimately
// gated on several permissions (a hook receiver needs both "hooks" and
// "transcripts") must see all of them open before its effect can appear.
func setEveryKey(g PermissionGate, state permission.State) {
	g.SetState(g.Key, state)
	for _, k := range g.OtherKeys {
		g.SetState(k, state)
	}
}

func assertPendingNoEffect(t gateReporter, g PermissionGate) {
	t.Helper()
	setEveryKey(g, permission.StatePending)
	g.Exercise()
	if g.Observe() {
		t.Errorf("effect observed while permission %q is pending — call site is not consent-gated", g.Key)
	}
}

func assertGrantedEffectObservable(t gateReporter, g PermissionGate) {
	t.Helper()
	setEveryKey(g, permission.StateGranted)
	g.Exercise()
	if !g.Observe() {
		t.Errorf("effect not observed after granting permission %q", g.Key)
	}
}

func assertRevokedEffectUndone(t gateReporter, g PermissionGate) {
	t.Helper()
	setEveryKey(g, permission.StateDenied)
	g.Exercise()
	if g.Observe() {
		t.Errorf("effect still observed after revoking a previously granted permission %q", g.Key)
	}
}

// assertGatedOnTheNamedKey is the issue #1475 arm: the effect must be absent
// while g.Key is denied even though every other permission is granted.
//
// The write order is load-bearing and is the reason this arm also rejects a
// key-blind SetState. g.Key is denied FIRST and the other keys are opened
// AFTER, so a wiring that ignores the key it is handed ends up holding
// everything granted, dispatches, and fails here. Opening the other keys
// first would leave such a wiring sitting at "denied" — it would pass, and
// the contract would once again be satisfiable by exactly the fake that
// hid #1466.
func assertGatedOnTheNamedKey(t gateReporter, g PermissionGate) {
	t.Helper()
	g.SetState(g.Key, permission.StateDenied)
	for _, k := range g.OtherKeys {
		g.SetState(k, permission.StateGranted)
	}
	g.Exercise()
	if g.Observe() {
		t.Errorf("effect observed while %q is denied and %v granted — the call site is gated on "+
			"some OTHER permission (or on none), not on %q (issue #1475)", g.Key, g.OtherKeys, g.Key)
	}
}

// ConsentGate is a keyed, mutable ConsentGranter fake: it answers per
// permission key, so a test can hold one of an adapter's permissions denied
// while every other is granted. It satisfies hookjson.ConsentGranter (and the
// identical private interfaces the services layer declares) structurally.
//
// It lives here rather than in each adapter's test package because three of
// them had already re-invented the same map[string]bool after #1466, and
// because a contract whose wiring supplies the fake is a contract whose wiring
// can supply a key-blind one. SetState has exactly PermissionGate.SetState's
// signature, so the usual wiring is one line: SetState: gate.SetState.
//
// A key never set reads as pending, matching permission.Set.Get.
type ConsentGate struct {
	mu     sync.Mutex
	states map[string]permission.State
}

// NewConsentGate returns a ConsentGate with every key pending.
func NewConsentGate() *ConsentGate {
	return &ConsentGate{states: make(map[string]permission.State)}
}

// SetState drives one permission key to state.
func (g *ConsentGate) SetState(key string, state permission.State) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.states == nil {
		g.states = make(map[string]permission.State)
	}
	g.states[key] = state
}

// Granted implements the receiver-side consent check. The agent name is
// ignored: a ConsentGate stands in for one adapter's consent at a time.
func (g *ConsentGate) Granted(_ string, key string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.states[key] == permission.StateGranted
}
