// hook_receiver_gate.go is the issue #1488 half of the permission-gate family:
// the driver that reads a hook receiver's permissions off the RECEIVER instead
// of taking them from the wiring.
//
// AssertPermissionGatedOnEachKey (#1475) already removes one hand-written
// table — the wiring names its key set once and the "other" key of each pair is
// derived. What it cannot remove is the key set itself. An author who wires
// only "hooks" gets a green contract, a green suite, and a receiver that reads
// transcripts with "transcripts" denied; that is issue #1466, and copilot's
// receiver reached #1475 with no wiring at all.
//
// Since #1488 a receiver DECLARES its permissions to hookjson.NewHandler and
// the handler publishes them, so the set is a property of the object the daemon
// builds. This driver takes it from there. Two consequences, and the second is
// the point:
//
//   - A receiver that later honours a third permission grows a third contract
//     arm by existing, not by someone remembering — the same "covered by
//     existing rather than by remembering" shape TestEveryHookInstallDeclares-
//     AVersionFloor has for the registry.
//   - A wiring can no longer name a set the receiver does not use, because it
//     names no set at all. The two-objects-that-could-disagree failure #1390
//     removed for the confiner is removed here for consent.
//
// It also owns the ConsentGate rather than accepting one. PermissionGate.
// SetState carries a warning that a key-blind fake is exactly what hid #1466;
// a driver that constructs the granter itself makes that unsupplyable.
package contracttesting

import (
	"fmt"
	"slices"
	"sync"
	"testing"

	"irrlicht/core/adapters/inbound/agents/hookjson"
)

// GatedHookReceiver is one freshly built receiver: the handler whose declared
// permissions the driver reads, plus the way to drive and observe it.
//
// Handler is the concrete hookjson.HookHandler rather than an http.Handler for
// the same reason HookReceiverUnderTest.Handler is: the property under test is
// carried by a field on it (Consent here, Confiner there), and an interface
// would force the wiring to declare that property separately — which is the
// declaration this driver exists to delete.
type GatedHookReceiver struct {
	// Handler is the receiver under test, built through the adapter's own
	// production constructor.
	Handler hookjson.HookHandler

	// Exercise posts a request the receiver would act on when every permission
	// is granted.
	Exercise func()

	// Observe reports whether the gated effect is currently present.
	Observe func() bool
}

// HookReceiverGate wires one hook receiver into
// AssertHookReceiverPermissionGated.
type HookReceiverGate struct {
	// Build returns a FRESH receiver bound to gate. It is called once per
	// declared permission plus once to read the declaration, and the fixtures
	// must not be shared between calls, or state left behind by one run decides
	// the next.
	//
	// It is handed the testing.TB of the arm it is building for, and that is
	// what makes "fresh" structural rather than incidental. A wiring closing
	// over the enclosing *testing.T instead would hang every arm's t.TempDir()
	// and t.Setenv() off the PARENT — copilot's relocation of COPILOT_HOME is
	// exactly that shape — so the fixtures would be correct only because each
	// arm happens to be built immediately before it runs, and their cleanup
	// would not land until the whole family finished. A t.Fatalf from inside
	// Exercise would likewise be attributed to the parent rather than to the
	// arm that failed.
	//
	// It takes *testing.T rather than this package's reporter seam on purpose.
	// Build is wiring supplied by the adapter, not an obligation that decides a
	// verdict, and what it needs is fixture machinery — t.TempDir(), t.Setenv()
	// — which fixtureT deliberately does not carry and a recorder must not
	// fake. It is the same split armT draws, landing on the fixture side.
	Build func(t *testing.T, gate *ConsentGate) GatedHookReceiver

	// Foreign names permissions the ADAPTER declares that this receiver must
	// not be gated on. It is needed only for a receiver declaring a single
	// permission, where the derived set leaves nothing to hold open and the
	// key-isolation arm cannot run (issue #1475) — claudecode's statusline
	// endpoint is the one such receiver today, and the two keys it names are
	// exactly the ones a copy-paste from hooks.go would have reached for.
	//
	// For a receiver declaring two or more it is empty: the other declared
	// permissions already supply the isolation state, and listing more would
	// re-introduce the hand-written table this driver removes.
	Foreign []string
}

// AssertHookReceiverPermissionGated runs the permission-gate contract once per
// permission the receiver DECLARES, deriving that set from the handler.
//
// It fails rather than skipping on every shape it cannot grade — an empty
// declaration, a Build that returns differently-declared receivers, a derived
// set with nothing to hold open. A family that quietly ran fewer arms would
// read as coverage, which is the shape this package exists to remove.
func AssertHookReceiverPermissionGated(t *testing.T, g HookReceiverGate) {
	t.Helper()
	if g.Build == nil {
		t.Fatal("HookReceiverGate needs Build")
	}

	probe := g.Build(t, NewConsentGate())
	if reason := malformedReceiverReason(probe); reason != "" {
		t.Fatal(reason)
	}
	declared := probe.Handler.Consent.Keys()

	arms, reason := hookGateArms(declared, g.Foreign)
	if reason != "" {
		t.Fatal(reason)
	}

	for _, arm := range arms {
		t.Run(arm.Key, func(t *testing.T) {
			gate := NewConsentGate()
			r := g.Build(t, gate)
			if why := sameDeclarationReason(declared, r.Handler.Consent.Keys()); why != "" {
				t.Fatal(why)
			}

			AssertPermissionGated(t, PermissionGate{
				Key:       arm.Key,
				OtherKeys: arm.Others,
				SetState:  gate.SetState,
				Exercise:  r.Exercise,
				Observe:   r.Observe,
			})
		})
	}
}

// hookGateArm is one derived run: the declared permission under test, and the
// permissions held open while it is denied.
type hookGateArm struct {
	Key    string
	Others []string
}

// hookGateArms derives one arm per declared permission, holding the rest of the
// declared set plus foreign open in each.
//
// It returns a reason instead of failing so the shapes it rejects are
// themselves testable — the same choice malformedGateReason makes, and for the
// same reason: a guard nobody can drive is the kind of unverifiable check this
// package exists to remove.
func hookGateArms(declared, foreign []string) ([]hookGateArm, string) {
	for _, f := range foreign {
		if slices.Contains(declared, f) {
			return nil, fmt.Sprintf("Foreign names %q, which this receiver DECLARES — Foreign is for "+
				"permissions the receiver must NOT be gated on, and the declared ones are already "+
				"held open by the derivation. Listing one here re-introduces the hand-written table "+
				"this driver removes, and on that key's own arm it would grant and deny the same "+
				"permission.", f)
		}
	}

	// Loop-invariant, so it is decided once: every arm holds the same
	// len(declared)-1+len(foreign) permissions open, and only a lone
	// declaration with no Foreign leaves that empty.
	if len(declared) == 1 && len(foreign) == 0 {
		return nil, fmt.Sprintf("the receiver declares only %q and the wiring names no Foreign "+
			"permissions — the key-isolation arm cannot run, so %q would only be shown to be "+
			"gated on something rather than on %q (issue #1475). Name at least one permission "+
			"this receiver must NOT be gated on.", declared[0], declared[0], declared[0])
	}

	arms := make([]hookGateArm, 0, len(declared))
	for i, key := range declared {
		arms = append(arms, hookGateArm{Key: key, Others: slices.Concat(declared[:i], declared[i+1:], foreign)})
	}
	return arms, ""
}

// malformedReceiverReason reports why the probe receiver cannot be graded, or
// "" when it can.
//
// A receiver that declares nothing cannot be built through
// hookjson.RequireConsent — its first key is positional — so an empty set here
// means a wiring that assembled a handler some other way, and it must not pass
// silently: a driver that ran zero arms is indistinguishable from one that ran
// them all.
func malformedReceiverReason(r GatedHookReceiver) string {
	if r.Exercise == nil || r.Observe == nil {
		return "GatedHookReceiver needs Exercise and Observe"
	}
	if len(r.Handler.Consent.Keys()) == 0 {
		return "the receiver under test declares no permission — nothing would be graded, and a " +
			"contract that runs zero arms reads exactly like one that runs them all (issue #1488)"
	}
	return ""
}

// sameDeclarationReason reports why a receiver Build returned declares
// something other than the set the arms were derived from, or "" when they
// agree.
//
// Build is called once to read the declaration and once per arm. A Build that
// returned a differently-configured receiver on later calls would have the
// contract grading a set the receiver under test does not use — the failure
// #1390 removed by publishing the confiner, arriving here through the wiring
// instead of through the constructor.
func sameDeclarationReason(want, got []string) string {
	if slices.Equal(want, got) {
		return ""
	}
	return fmt.Sprintf("Build returned a receiver declaring %v, but the arms were derived from %v — "+
		"the contract would grade a set this receiver does not use", got, want)
}

// AssertDeclaredPermissions is the per-adapter LOCK companion: it pins the
// exact key set a receiver declares, so the list each wiring used to write out
// by hand survives as an assertion rather than being deleted along with the
// wiring.
//
// Per #1450 a rewritten guard replays its predecessor's cases or it is not
// known to be a superset. AssertHookReceiverPermissionGated derives what those
// wirings declared, which makes it a superset only if the derived set is the
// same set — and that is exactly what a derivation cannot assert about itself.
func AssertDeclaredPermissions(t *testing.T, h hookjson.HookHandler, want ...string) {
	t.Helper()
	declaredPermissionsMismatch(t, h, want...)
}

// declaredPermissionsMismatch is the arm behind AssertDeclaredPermissions. It
// takes reporter rather than *testing.T so a negative self-test can drive it —
// a lock that could not itself be shown to fail is a lock nobody has checked.
func declaredPermissionsMismatch(t reporter, h hookjson.HookHandler, want ...string) {
	t.Helper()
	if got := h.Consent.Keys(); !slices.Equal(got, want) {
		t.Fatalf("receiver declares %v, want %v — this list is the one the pre-#1488 contract "+
			"wiring named by hand, kept as a lock (#1450)", got, want)
	}
}

// RecordingLogger is an outbound.Logger that keeps what was logged at ERROR.
//
// It exists for one assertion, made by every hook receiver's hand-written
// consent test since #1488: that an ordinary consent-denied request is refused
// SILENTLY. That matters twice over. It is a user-facing property — a hook
// fires per tool call, and a statusline hook per prompt render, so a user who
// declined a permission must not collect an error line at that rate. And it is
// the last thing those tests still discriminate: DecodeConfined's backstop
// drops the same payload on the receiver's own declaration, so every
// dispatch-shaped assertion survives deleting a receiver's gate, while the
// silence does not — the backstop logs, because reaching it means a check was
// skipped or consent was revoked mid-request.
//
// It lives here rather than in each adapter for the reason ConsentGate does:
// three packages had grown the same ten lines, and "what a refusal may log" is
// a decision about the contract rather than about an adapter.
type RecordingLogger struct {
	mu     sync.Mutex
	errors []string
}

// LogError records. The other methods are deliberately inert: only the error
// channel carries the obligation above.
func (l *RecordingLogger) LogError(_, _, msg string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.errors = append(l.errors, msg)
}

func (l *RecordingLogger) LogInfo(string, string, string) {}

func (l *RecordingLogger) LogProcessingTime(string, string, int64, int, string) {}

func (l *RecordingLogger) Close() error { return nil }

// Errors returns what was logged at ERROR, newest last.
func (l *RecordingLogger) Errors() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.errors)
}
