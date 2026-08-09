package claudecode

import (
	"encoding/json"
	"testing"

	"irrlicht/core/internal/contracttesting"
	"irrlicht/core/ports/outbound"
)

// TestUnknownHookEventObserved wires this adapter's hook receiver into the
// shared issue #1364 contract: an event name the receiver does not recognize is
// counted per (adapter, name), reported once per distinct name, and still
// answered 2xx.
func TestUnknownHookEventObserved(t *testing.T) {
	contracttesting.AssertUnknownHookEventObserved(t, contracttesting.UnknownEventReceiver{
		Adapter:         AdapterName,
		Root:            claudeProjectsRoot,
		WriteTranscript: writeContractTranscript,
		New: func(t *testing.T, log outbound.Logger) contracttesting.UnknownEventReceiverUnderTest {
			t.Helper()
			target := &mockTarget{}
			return contracttesting.UnknownEventReceiverUnderTest{
				Handler:  NewHookHandlerWithConfiner(target, nil, nil, log, TranscriptConfiner()),
				Observed: func() bool { return len(target.getCalls()) > 0 },
			}
		},
		PayloadFor: func(transcriptPath, event string) string {
			body, err := json.Marshal(hookPayload{
				TranscriptPath: transcriptPath,
				HookEventName:  event,
				ToolName:       "Bash",
			})
			if err != nil {
				panic(err)
			}
			return string(body)
		},
		KnownEvent:   HookPermissionRequest,
		EndpointPath: HookEndpointPath,
	})
}
