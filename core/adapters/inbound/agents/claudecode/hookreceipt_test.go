package claudecode

import (
	"net/http"
	"testing"

	"irrlicht/core/internal/contracttesting"
	"irrlicht/core/ports/outbound"
)

// TestHookReceiptObserved wires this adapter's hook receiver into the shared
// issue #1368 contract: a consent-passed request is counted as a receipt for
// this adapter whatever the receiver then does with it, and a consent-denied
// one is not.
//
// The counter is what tells the liveness watchdog this channel is alive, so a
// receiver that stops calling it is not merely unmonitored — it is actively
// reported as dead and demoted to the transcript tier while working.
func TestHookReceiptObserved(t *testing.T) {
	contracttesting.AssertHookReceiptObserved(t, contracttesting.HookReceiptReceiver{
		Adapter:         AdapterName,
		Root:            claudeProjectsRoot,
		WriteTranscript: writeContractTranscript,
		New: func(t *testing.T, log outbound.Logger) http.Handler {
			t.Helper()
			return NewHookHandler(&mockTarget{}, nil, fakeGate(true), log)
		},
		NewDenied: func(t *testing.T, log outbound.Logger) http.Handler {
			t.Helper()
			return NewHookHandler(&mockTarget{}, nil, fakeGate(false), log)
		},
		PayloadFor:   contractPayload,
		KnownEvent:   HookPermissionRequest,
		EndpointPath: HookEndpointPath,
	})
}
