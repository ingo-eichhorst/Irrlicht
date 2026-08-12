package contracttesting

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"irrlicht/core/adapters/inbound/agents/hookjson"
)

// This file is the committed mutation evidence for the #1488 driver, in the
// same shape permission_gate_test.go carries it for #1475's arm.
//
// What is new here is not an obligation — the four arms are still
// AssertPermissionGated's — it is the DERIVATION: which arms run at all, and
// where their key set comes from. Three things can silently stop working, and
// each would look exactly like health:
//
//   - the derivation could pair a key with the wrong "others";
//   - the driver could read a set that is not the receiver's;
//   - a receiver declaring one permission could run an isolation arm with
//     nothing held open, which is #1475's original blind spot restored.
//
// Each is driven below through a reason-returning helper rather than through a
// *testing.T, which is what makes them gradeable at all — the choice
// malformedGateReason already made in this package.

// gatedFakeReceiver builds a real hookjson receiver that declares keys and
// dispatches only when every key in gatedOn is granted. It goes through
// hookjson.NewHandler so the Consent the driver reads is the one the request
// path uses, exactly as an adapter's constructor does.
func gatedFakeReceiver(gate *ConsentGate, declared, gatedOn []string) (hookjson.HookHandler, *bool) {
	dispatched := new(bool)
	consent := hookjson.RequireConsent(gate, "test-agent", declared[0], declared[1:]...)
	h := hookjson.NewHandler(nil, consent,
		func(c hookjson.Consent, _ *hookjson.PathConfiner, w http.ResponseWriter, _ *http.Request) {
			for _, k := range gatedOn {
				if !c.Granted(k) {
					w.WriteHeader(http.StatusOK)
					return
				}
			}
			*dispatched = true
			w.WriteHeader(http.StatusOK)
		})
	return h, dispatched
}

// receiverGateOver wires gatedFakeReceiver into the driver. The receiver never
// reaches DecodeConfined (its serve func dispatches directly), so a nil
// confiner is fine and the fixture stays about consent alone.
func receiverGateOver(declared, gatedOn, foreign []string) HookReceiverGate {
	return HookReceiverGate{
		Foreign: foreign,
		Build: func(_ *testing.T, gate *ConsentGate) GatedHookReceiver {
			h, dispatched := gatedFakeReceiver(gate, declared, gatedOn)
			return GatedHookReceiver{
				Handler: h,
				Exercise: func() {
					*dispatched = false
					req := httptest.NewRequest(http.MethodPost, "/api/v1/hooks/test", strings.NewReader(`{}`))
					h.ServeHTTP(httptest.NewRecorder(), req)
				},
				Observe: func() bool { return *dispatched },
			}
		},
	}
}

// TestAssertHookReceiverPermissionGated_PassesACorrectReceiver is the vacuity
// guard. Without it, a driver that derived no arms at all — or one whose arms
// reported unconditionally — would satisfy every case below and read as
// coverage.
func TestAssertHookReceiverPermissionGated_PassesACorrectReceiver(t *testing.T) {
	keys := []string{selfTestHooks, selfTestTranscripts}
	AssertHookReceiverPermissionGated(t, receiverGateOver(keys, keys, nil))
}

// TestAssertHookReceiverPermissionGated_DerivesOneArmPerDeclaredKey is the
// property the whole ticket rests on: the number of arms follows the
// RECEIVER's declaration, not the wiring's. A receiver that grows a third
// permission is graded on it without anyone editing its test.
//
// Build is called once to read the declaration plus once per arm, so four
// calls for three keys — and a driver that silently derived nothing would show
// one.
func TestAssertHookReceiverPermissionGated_DerivesOneArmPerDeclaredKey(t *testing.T) {
	keys := []string{"a", "b", "c"}
	base := receiverGateOver(keys, keys, nil)

	builds := 0
	counted := HookReceiverGate{Build: func(tb *testing.T, gate *ConsentGate) GatedHookReceiver {
		builds++
		return base.Build(tb, gate)
	}}

	AssertHookReceiverPermissionGated(t, counted)

	if want := len(keys) + 1; builds != want {
		t.Fatalf("Build called %d time(s), want %d (one probe + one per declared permission) — "+
			"an arm count that does not follow the declaration is the act of memory #1488 removes", builds, want)
	}
}

// TestHookGateArms_DerivesTheOthers pins the pairing directly. Three keys,
// because with two "the others" and "the other one" are the same answer, so a
// two-key case cannot tell a correct derivation from one returning the last
// key — the argument TestAssertPermissionGatedOnEachKey_DerivesTheOthers makes.
func TestHookGateArms_DerivesTheOthers(t *testing.T) {
	arms, reason := hookGateArms([]string{"a", "b", "c"}, nil)
	if reason != "" {
		t.Fatalf("a three-key declaration was refused: %s", reason)
	}

	want := map[string]string{"a": "b,c", "b": "a,c", "c": "a,b"}
	if len(arms) != len(want) {
		t.Fatalf("derived %d arm(s), want %d — every declared permission must get a turn under test", len(arms), len(want))
	}
	for _, arm := range arms {
		if got := strings.Join(arm.Others, ","); got != want[arm.Key] {
			t.Errorf("key %q held open %q, want %q", arm.Key, got, want[arm.Key])
		}
	}
}

// TestHookGateArms_AppendsForeignToEveryArm covers the single-permission
// receiver — claudecode's statusline endpoint, the one case where the derived
// set alone leaves nothing to hold open.
func TestHookGateArms_AppendsForeignToEveryArm(t *testing.T) {
	arms, reason := hookGateArms([]string{"statusline"}, []string{"hooks", "transcripts"})
	if reason != "" {
		t.Fatalf("a one-key declaration with Foreign was refused: %s", reason)
	}
	if len(arms) != 1 {
		t.Fatalf("derived %d arm(s), want 1", len(arms))
	}
	if got := strings.Join(arms[0].Others, ","); got != "hooks,transcripts" {
		t.Fatalf("held open %q, want %q — without them the isolation arm proves only that the "+
			"receiver is gated on something", got, "hooks,transcripts")
	}
}

// TestHookGateArms_RefusesASoloDeclarationWithNoForeign is #1475's blind spot,
// which the derivation could otherwise restore by accident: with one declared
// permission and no Foreign there is nothing to hold open, and the isolation
// arm would pass having proved nothing.
func TestHookGateArms_RefusesASoloDeclarationWithNoForeign(t *testing.T) {
	arms, reason := hookGateArms([]string{"statusline"}, nil)
	if reason == "" {
		t.Fatal("a receiver declaring ONE permission with no Foreign was accepted — the " +
			"key-isolation arm then runs with nothing granted beside the denied key, which is " +
			"exactly the state that cannot tell a correctly gated receiver from an ungated one")
	}
	if arms != nil {
		t.Error("arms were returned alongside a refusal")
	}
	if !strings.Contains(reason, "statusline") {
		t.Errorf("the refusal does not name the key it is about: %s", reason)
	}
}

// TestHookGateArms_RefusesAForeignKeyTheReceiverDeclares covers the other
// direction of Foreign's contract. Its doc says a multi-key receiver leaves it
// empty because the declared keys already supply the isolation state — but
// nothing stopped a wiring listing one anyway, which is the hand-written table
// this driver exists to delete, growing back. It is also incoherent on the
// named key's own arm, where the same permission would be denied and granted.
func TestHookGateArms_RefusesAForeignKeyTheReceiverDeclares(t *testing.T) {
	arms, reason := hookGateArms([]string{"hooks", "transcripts"}, []string{"transcripts"})
	if reason == "" {
		t.Fatal("Foreign named a permission the receiver DECLARES and was accepted — on that " +
			"key's own arm the driver would grant and deny the same permission, and the " +
			"hand-written key table this driver removes would be back")
	}
	if arms != nil {
		t.Error("arms were returned alongside a refusal")
	}
	if !strings.Contains(reason, "transcripts") {
		t.Errorf("the refusal does not name the offending key: %s", reason)
	}

	// The vacuity guard: a genuinely foreign key is still accepted, or the
	// check above would refuse every statusline-shaped wiring too.
	if _, reason := hookGateArms([]string{"statusline"}, []string{"hooks"}); reason != "" {
		t.Fatalf("a genuinely foreign key was refused: %s", reason)
	}
}

// TestMalformedReceiverReason_RefusesADeclarationlessReceiver covers the
// handler a wiring assembled without going through hookjson.RequireConsent.
// Zero arms and every arm passing look identical from outside.
func TestMalformedReceiverReason_RefusesADeclarationlessReceiver(t *testing.T) {
	empty := GatedHookReceiver{
		Handler:  hookjson.HookHandler{},
		Exercise: func() {},
		Observe:  func() bool { return false },
	}
	if malformedReceiverReason(empty) == "" {
		t.Fatal("a receiver declaring NO permission was accepted — the driver would run zero " +
			"arms and report success, which reads exactly like running them all")
	}

	ok := GatedHookReceiver{
		Handler:  mustDeclare("hooks", "transcripts"),
		Exercise: func() {},
		Observe:  func() bool { return false },
	}
	if reason := malformedReceiverReason(ok); reason != "" {
		t.Fatalf("a well-formed receiver was refused (%s) — the vacuity guard for the case above", reason)
	}
}

// TestSameDeclarationReason_CatchesABuildThatDrifts covers a Build returning a
// differently-declared receiver on a later call, where the arms would grade a
// set the receiver under test does not use.
func TestSameDeclarationReason_CatchesABuildThatDrifts(t *testing.T) {
	if reason := sameDeclarationReason([]string{"hooks", "transcripts"}, []string{"hooks", "transcripts"}); reason != "" {
		t.Fatalf("an unchanged declaration was reported as drift: %s — the vacuity guard here", reason)
	}
	if sameDeclarationReason([]string{"hooks", "transcripts"}, []string{"hooks"}) == "" {
		t.Error("a receiver that dropped a permission between builds was accepted")
	}
	if sameDeclarationReason([]string{"hooks", "transcripts"}, []string{"transcripts", "hooks"}) == "" {
		t.Error("a reordered declaration was accepted — the arms are derived positionally, so " +
			"order is part of what must not drift")
	}
}

// TestAssertDeclaredPermissions_IsTheLockItClaimsToBe grades the lock helper
// itself: it has to fail for a set that differs, or the four per-adapter locks
// replay nothing.
func TestAssertDeclaredPermissions_IsTheLockItClaimsToBe(t *testing.T) {
	h := mustDeclare("hooks", "transcripts")

	if got := h.Consent.Keys(); strings.Join(got, ",") != "hooks,transcripts" {
		t.Fatalf("fixture declares %v", got)
	}
	rec := observe(t, func(at armT) { declaredPermissionsMismatch(at, h, "hooks") })
	mustReport(t, rec, "receiver declares",
		"a lock naming FEWER permissions than the receiver declares — the shape that would let a "+
			"newly honoured permission slip past the lock unnoticed")
}

func mustDeclare(keys ...string) hookjson.HookHandler {
	return hookjson.NewHandler(nil,
		hookjson.RequireConsent(nil, "test-agent", keys[0], keys[1:]...),
		func(hookjson.Consent, *hookjson.PathConfiner, http.ResponseWriter, *http.Request) {})
}
