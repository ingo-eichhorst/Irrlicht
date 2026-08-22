// hookunknown_test.go wires this adapter's hook receiver into the shared
// issue #1364 contract: an event name the receiver does not recognize is
// counted per (adapter, name), reported once per distinct name, and still
// answered 2xx.
//
// It is not a hypothetical for this adapter. Every OTHER receiver's unknown
// event would come from an upstream rename; pi's can also come from
// irrlicht's own installed extension being NEWER than the daemon reading
// it — the file on disk survives a daemon downgrade, so a future extension
// that subscribes to a second event would deliver a name this switch does
// not know. #1364's counter is what makes that visible instead of silent.
package pi

import (
	"testing"

	"irrlicht/core/internal/contracttesting"
	"irrlicht/core/ports/outbound"
)

func TestUnknownHookEventObserved(t *testing.T) {
	contracttesting.AssertUnknownHookEventObserved(t, contracttesting.UnknownEventReceiver{
		Adapter:         AdapterName,
		Root:            piSessionRoot,
		WriteTranscript: writeContractTranscript,
		New: func(t *testing.T, log outbound.Logger) contracttesting.UnknownEventReceiverUnderTest {
			t.Helper()
			target := &mockTarget{}
			return contracttesting.UnknownEventReceiverUnderTest{
				Handler:  NewHookHandler(target, nil, log),
				Observed: func() bool { return target.totalCalls() > 0 },
			}
		},
		PayloadFor:   contractPayload,
		KnownEvent:   HookEventAgentSettled,
		EndpointPath: HookEndpointPath,
	})
}
