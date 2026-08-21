// hookunknown_test.go wires this adapter's hook receiver into the shared
// issue #1364 contract: an event name the receiver does not recognize is
// counted per (adapter, name), reported once per distinct name, and still
// answered 2xx. For kiro-cli that includes preToolUse, agentSpawn and
// userPromptSubmit — all real kiro-cli events this adapter deliberately does
// not install (see hooks.go's package comment).
package kirocli

import (
	"testing"

	"irrlicht/core/internal/contracttesting"
	"irrlicht/core/ports/outbound"
)

func TestUnknownHookEventObserved(t *testing.T) {
	contracttesting.AssertUnknownHookEventObserved(t, contracttesting.UnknownEventReceiver{
		Adapter:         AdapterName,
		Root:            kiroSessionRoot,
		WriteTranscript: writeContractTranscript,
		New: func(t *testing.T, log outbound.Logger) contracttesting.UnknownEventReceiverUnderTest {
			t.Helper()
			target := &mockTarget{}
			return contracttesting.UnknownEventReceiverUnderTest{
				Handler:  NewHookHandler(target, nil, log),
				Observed: func() bool { return target.totalCalls() > 0 },
			}
		},
		PayloadFor: func(transcriptPath, event string) string {
			return contractPayload(sessionIDForPath(transcriptPath), event)
		},
		KnownEvent:   HookStop,
		EndpointPath: HookEndpointPath,
	})
}
