// hookreceipt_test.go wires this adapter's hook receiver into the shared issue
// #1368 contract: a consent-passed request counts as a receipt whatever the
// receiver then does with it, and a consent-denied one does not.
//
// One of the four obligations is WEAKER here than for a from-body receiver, and
// it is stated rather than left implied. Obligation 3 — "a path-confinement
// rejection still counts" — posts a well-formed body carrying an out-of-tree
// transcript path. This receiver reads no path field, so that body is
// indistinguishable from obligation 1's and the arm degenerates into a repeat
// of it. That is the same inertness AGENTS.md records for OtherKeys on an
// install-type permission gate: the arm is kept uniform, with no opt-out,
// because a flag this wiring could take is one a from-body wiring could take
// too. What obligation 3 protects against is unreachable here for the reason
// the whole PathDaemonDerived route exists — there is no confinement rejection
// to mis-count, because there is no caller-supplied path.
package hermes

import (
	"net/http"
	"testing"

	"irrlicht/core/internal/contracttesting"
	"irrlicht/core/ports/outbound"
)

func TestHookReceiptObserved(t *testing.T) {
	contracttesting.AssertHookReceiptObserved(t, contracttesting.HookReceiptReceiver{
		Adapter:         AdapterName,
		Root:            hermesHome,
		WriteTranscript: writeContractTranscript,
		New: func(t *testing.T, log outbound.Logger) http.Handler {
			t.Helper()
			return NewHookHandler(&mockTarget{},
				keyedGate{PermissionKeyHooks: true, PermissionKeyStore: true}, log)
		},
		NewDenied: func(t *testing.T, log outbound.Logger) http.Handler {
			t.Helper()
			return NewHookHandler(&mockTarget{}, keyedGate{}, log)
		},
		PayloadFor:   func(_, event string) string { return unknownEventPayload(event) },
		KnownEvent:   HookEventOnSessionEnd,
		EndpointPath: HookEndpointPath,
	})
}
