// hookpath_test.go wires this adapter's hook receiver into the shared issue
// #1361 contract: a caller-supplied transcript path is confined to the
// adapter's declared roots, symlinks resolved first, and anything outside
// is refused, logged, counted — and still answered 2xx.
package vibe

import (
	"testing"

	"irrlicht/core/internal/contracttesting"
)

func TestHookPathConfined(t *testing.T) {
	contracttesting.AssertHookPathConfined(t, contracttesting.HookReceiver{
		Root: vibeSessionRoot,
		New: func(t *testing.T) contracttesting.HookReceiverUnderTest {
			t.Helper()
			target := &mockTarget{}
			return contracttesting.HookReceiverUnderTest{
				Handler:  NewHookHandler(target, nil, mockLogger{}),
				Observed: func() bool { return target.totalCalls() > 0 },
				ObservedPath: func() string {
					stops := target.stops()
					if len(stops) == 0 {
						return ""
					}
					return stops[len(stops)-1].transcriptPath
				},
			}
		},
		WriteTranscript: writeContractTranscript,
		TranscriptExt:   transcriptExt,
		PayloadFor: func(transcriptPath string) string {
			return contractPayload(transcriptPath, hookEventPostAgentTurn)
		},
		EndpointPath: HookEndpointPath,
	})
}
