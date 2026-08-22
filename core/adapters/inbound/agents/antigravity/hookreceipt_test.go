// hookreceipt_test.go wires this adapter's hook receiver into the shared issue
// #1368 contract: a consent-passed request counts as a receipt whatever the
// receiver then does with it, and a consent-denied one does not.
//
// The watchdog this feeds is load-bearing for antigravity in a way it is not
// for most adapters: Stop is measured NOT to fire on at least one real
// termination path (a turn that ended on a denied tool call fired PostToolUse
// three times and Stop zero — see hooks.go), so a channel that is alive but
// quiet for a stretch is an ordinary state here. A receiver that forgot to
// count receipts would be reported dead on top of that, and the two would be
// indistinguishable.
package antigravity

import (
	"net/http"
	"testing"

	"irrlicht/core/internal/contracttesting"
	"irrlicht/core/ports/outbound"
)

func TestHookReceiptObserved(t *testing.T) {
	contracttesting.AssertHookReceiptObserved(t, contracttesting.HookReceiptReceiver{
		Adapter:         AdapterName,
		Root:            antigravityBrainRoot,
		WriteTranscript: writeContractTranscript,
		New: func(t *testing.T, log outbound.Logger) http.Handler {
			t.Helper()
			return NewHookHandler(&mockTarget{},
				keyedGate{PermissionKeyHooks: true, PermissionKeyTranscripts: true}, log)
		},
		NewDenied: func(t *testing.T, log outbound.Logger) http.Handler {
			t.Helper()
			return NewHookHandler(&mockTarget{}, keyedGate{}, log)
		},
		PayloadFor:   contractPayload,
		KnownEvent:   HookEventStop,
		EndpointPath: HookEndpointPath,
	})
}
