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
// permission's effects do not produce this one's — and where that other
// permission has no closures to run at all (an observe-kind sibling, or a
// declaration exporting a single permission) it degenerates one step further
// still, to a repeat of the revoked arm. Those wirings say so at the call
// site. The arm is kept uniform anyway, with no opt-out, because a flag that
// let an install-type wiring skip it is a flag a live-gate wiring could also
// reach for, and that is the one place it must not be skippable.
//
// What this still does not do is make the obligation unforgettable: a call site
// must be wired once per permission it must honour, and nothing fails if an
// author wires only one of them. Issue #1488 removed that last act of memory
// for hook receivers — the consent keys are folded into
// hookjson.DecodeConfined, which core/architecture_hookbody_test.go already
// forces every hook receiver through, and published on the handler so
// AssertHookReceiverPermissionGated derives the set instead of being told it.
// Every other kind of call site still names its own keys here.
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

// AssertPermissionGatedOnEachKey runs the contract once per key in keys, each
// time holding the REST of the set open — the shape every receiver gated on
// more than one permission needs, since a receiver must be shown to honour
// each of them separately.
//
// It exists because the alternative is what three adapters had started doing
// independently: a hand-written table pairing each key with "the other one".
// Such a table can silently list only one direction, and listing only the
// direction that already works is indistinguishable from covering both. Here a
// wiring names its key set once and the pairing is derived.
//
// build receives the key under test and the others, and returns a gate with a
// FRESH call site — the fixtures must not be shared between keys, or a state
// left behind by one run decides the next.
func AssertPermissionGatedOnEachKey(t *testing.T, keys []string, build func(key string, others []string) PermissionGate) {
	t.Helper()
	if len(keys) < 2 {
		t.Fatalf("AssertPermissionGatedOnEachKey needs at least two keys, got %d — "+
			"with fewer there is nothing to hold open and the key-isolation arm cannot run", len(keys))
	}
	for i, key := range keys {
		others := append(append([]string{}, keys[:i]...), keys[i+1:]...)
		t.Run(key, func(t *testing.T) { AssertPermissionGated(t, build(key, others)) })
	}
}

// OnlyKey adapts a single permission's state-driver to PermissionGate.SetState
// by ignoring every other key. It is the install-type counterpart to
// ConsentGate: those wirings hold one permission's own Apply/Remove closures,
// so a key that is not theirs has nothing to drive.
//
// It is here rather than written out at each install wiring for the same
// reason ConsentGate is: three of them had each hand-rolled the filter, and
// "a foreign key is a no-op" is a decision about the contract, not about an
// adapter. Note what it means for the key-isolation arm — with nothing to
// drive for the other key, that arm repeats the revoked arm exactly. See
// AssertPermissionGated's doc for why it is kept uniform anyway.
func OnlyKey(key string, drive func(permission.State)) func(string, permission.State) {
	return func(k string, state permission.State) {
		if k != key {
			return
		}
		drive(state)
	}
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
	return malformedOtherKeysReason(g.Key, g.OtherKeys)
}

// malformedOtherKeysReason reports why the keys meant to be held open beside
// key cannot serve that purpose, or "" when they can. Split from the shape
// checks above because it answers a different question: those ask whether a
// gate was filled in at all, this asks whether the key set it names can
// actually produce the isolation arm's state.
//
// key is assumed non-empty — its caller has already rejected that.
func malformedOtherKeysReason(key string, others []string) string {
	if len(others) == 0 {
		return fmt.Sprintf("PermissionGate.OtherKeys is empty — the key-isolation arm cannot run, so %q "+
			"would only be shown to be gated on something rather than on %q (issue #1475). "+
			"Name at least one permission this call site must NOT be gated on.", key, key)
	}
	for _, k := range others {
		if k == "" {
			// key is non-empty, so the k == key test below can never catch
			// this. An unfilled constant or a struct typo would otherwise
			// grant a permission no call site can be checking, and the
			// isolation arm would pass having proved nothing — the exact
			// inert-but-green shape this guard exists for.
			return "PermissionGate.OtherKeys contains an empty key — name a real permission " +
				"this call site must NOT be gated on"
		}
		if k == key {
			return fmt.Sprintf("PermissionGate.OtherKeys repeats the key under test (%q) — "+
				"the key-isolation arm would grant and deny the same permission", key)
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

func assertPendingNoEffect(t reporter, g PermissionGate) {
	t.Helper()
	setEveryKey(g, permission.StatePending)
	g.Exercise()
	if g.Observe() {
		t.Errorf("effect observed while permission %q is pending — call site is not consent-gated", g.Key)
	}
}

func assertGrantedEffectObservable(t reporter, g PermissionGate) {
	t.Helper()
	setEveryKey(g, permission.StateGranted)
	g.Exercise()
	if !g.Observe() {
		t.Errorf("effect not observed after granting permission %q", g.Key)
	}
}

func assertRevokedEffectUndone(t reporter, g PermissionGate) {
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
func assertGatedOnTheNamedKey(t reporter, g PermissionGate) {
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
// It replaces the three key-blind mutable fakes the live-gate wirings each
// grew for this contract (claudecode's and codex's mutableGate, the services
// layer's mutableConsent), and it lives here rather than in an adapter because
// a contract whose wiring supplies the fake is a contract whose wiring can
// supply a key-blind one. Adapters keep their own static keyedGate map
// literals for one-off tests that pin a fixed two-permission combination —
// those were never the problem. SetState has exactly PermissionGate.SetState's
// signature, so the usual wiring is one line: SetState: gate.SetState.
//
// It is backed by permission.Set rather than a bare map so the "never answered
// reads as pending" rule is INHERITED from the domain instead of restated
// here. A fake that hand-maintains its own copy of that default would keep
// answering the old way if the domain ever changed it, and the contract would
// go on asserting against a consent model production no longer uses.
type ConsentGate struct {
	mu  sync.Mutex
	set permission.Set
}

// consentGateAgent is the single agent name a ConsentGate records under. The
// fake stands in for one adapter's consent at a time, so the agent axis of
// permission.Set is deliberately collapsed — see Granted.
const consentGateAgent = "contracttesting"

// NewConsentGate returns a ConsentGate with every key pending.
func NewConsentGate() *ConsentGate {
	return &ConsentGate{set: permission.Set{}}
}

// SetState drives one permission key to state.
func (g *ConsentGate) SetState(key string, state permission.State) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.set.Put(consentGateAgent, key, state)
}

// Granted implements the receiver-side consent check. The agent name a call
// site passes is ignored: a ConsentGate stands in for one adapter at a time,
// so every question it is asked is about that adapter.
func (g *ConsentGate) Granted(_ string, key string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.set.Get(consentGateAgent, key) == permission.StateGranted
}
