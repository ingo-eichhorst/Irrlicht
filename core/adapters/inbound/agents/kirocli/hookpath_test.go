// hookpath_test.go wires this adapter's hook receiver into the shared issue
// #1361 contract: a caller-supplied path is confined to the adapter's
// declared roots, symlinks resolved first, and anything outside is refused,
// logged, counted — and still answered 2xx.
//
// Unlike every other wired adapter, kiro-cli never carries transcript_path on
// the wire at all (see hooks.go's payload doc) — every event's path is
// reconstructed from session_id against the adapter's own declared root. So
// the contract's escape attempts, each expressed as a target FILE PATH, are
// driven through sessionIDForPath (hooks_test.go) rather than embedded
// directly in the payload.
package kirocli

import (
	"testing"

	"irrlicht/core/internal/contracttesting"
)

func TestHookPathConfined(t *testing.T) {
	contracttesting.AssertHookPathConfined(t, contracttesting.HookReceiver{
		Root: kiroSessionRoot,
		New: func(t *testing.T) contracttesting.HookReceiverUnderTest {
			t.Helper()
			target := &mockTarget{}
			return contracttesting.HookReceiverUnderTest{
				Handler:  NewHookHandler(target, nil, mockLogger{}),
				Observed: func() bool { return target.totalCalls() > 0 },
				ObservedPath: func() string {
					if stops := target.stops(); len(stops) > 0 {
						return stops[len(stops)-1].transcriptPath
					}
					return ""
				},
			}
		},
		WriteTranscript: writeContractTranscript,
		TranscriptExt:   transcriptExt,
		PayloadFor: func(transcriptPath string) string {
			return contractPayload(sessionIDForPath(transcriptPath), HookStop)
		},
		EndpointPath: HookEndpointPath,
	})
}
