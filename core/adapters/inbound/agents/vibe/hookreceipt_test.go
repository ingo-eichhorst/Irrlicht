// hookreceipt_test.go wires this adapter's hook receiver into the shared
// issue #1368 contract: a consent-passed request counts as a receipt
// whatever the receiver then does with it, and a consent-denied one does
// not.
package vibe

import (
	"net/http"
	"testing"

	"irrlicht/core/internal/contracttesting"
	"irrlicht/core/ports/outbound"
)

func TestHookReceiptObserved(t *testing.T) {
	contracttesting.AssertHookReceiptObserved(t, contracttesting.HookReceiptReceiver{
		Adapter:         AdapterName,
		Root:            vibeSessionRoot,
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
		KnownEvent:   hookEventPostAgentTurn,
		EndpointPath: HookEndpointPath,
	})
}
