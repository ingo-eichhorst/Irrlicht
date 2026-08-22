// hookunknown_test.go wires this adapter's hook receiver into the shared issue
// #1364 contract: an event name the receiver does not recognize is counted per
// (adapter, name), reported once per distinct name, and still answered 2xx.
//
// It is not a hypothetical for this adapter, and for two independent reasons.
// opencode's plugin API forwards its whole 30-plus-member bus through a single
// `event` hook, so an upstream rename is a live-traffic event name this switch
// does not know rather than a missing subscription. And, like pi, irrlicht's
// own installed plugin can be NEWER than the daemon reading it — the file on
// disk survives a daemon downgrade — so a future plugin that forwards a fourth
// event delivers a name this switch has never heard of.
package opencode

import (
	"testing"

	"irrlicht/core/internal/contracttesting"
	"irrlicht/core/ports/outbound"
)

func TestUnknownHookEventObserved(t *testing.T) {
	contracttesting.AssertUnknownHookEventObserved(t, contracttesting.UnknownEventReceiver{
		Adapter:         AdapterName,
		Root:            opencodeStoreRoot,
		WriteTranscript: writeContractTranscript,
		New: func(t *testing.T, log outbound.Logger) contracttesting.UnknownEventReceiverUnderTest {
			t.Helper()
			target := &mockTarget{}
			return contracttesting.UnknownEventReceiverUnderTest{
				Handler:  NewHookHandler(target, nil, log),
				Observed: func() bool { return target.totalCalls() > 0 },
			}
		},
		PayloadFor:   func(_, event string) string { return contractPayload(event) },
		KnownEvent:   HookEventSessionIdle,
		EndpointPath: HookEndpointPath,
	})
}
