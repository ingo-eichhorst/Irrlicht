package codex

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
// Codex's receiver has two gates, and the receipt is counted against the first.
// That is deliberate: "transcripts" authorizes READING the transcript, while
// noting that a POST arrived needs only the "hooks" consent — and a
// hooks-granted / transcripts-denied install has a channel that plainly works
// and must not be convicted of silence.
func TestHookReceiptObserved(t *testing.T) {
	contracttesting.AssertHookReceiptObserved(t, contracttesting.HookReceiptReceiver{
		Adapter:         AdapterName,
		Root:            codexSessionsRoot,
		WriteTranscript: writeContractTranscript,
		New: func(t *testing.T, log outbound.Logger) http.Handler {
			t.Helper()
			return NewHookHandlerWithConfiner(&mockTarget{},
				keyedGate{PermissionKeyHooks: true, PermissionKeyTranscripts: true}, log, TranscriptConfiner())
		},
		NewDenied: func(t *testing.T, log outbound.Logger) http.Handler {
			t.Helper()
			return NewHookHandlerWithConfiner(&mockTarget{}, keyedGate{}, log, TranscriptConfiner())
		},
		PayloadFor:   contractPayload,
		KnownEvent:   HookPermissionRequest,
		EndpointPath: HookEndpointPath,
	})
}
