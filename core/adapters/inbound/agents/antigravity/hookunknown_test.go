// hookunknown_test.go wires this adapter's hook receiver into the shared issue
// #1364 contract: an event name the receiver does not recognize is counted per
// (adapter, name), reported once per distinct name, and still answered 2xx.
//
// It reaches something specific to antigravity. This adapter's payload names
// no event — the beacon forwards antigravity's own stdin verbatim, and
// antigravity identifies an event by which config key the handler was
// registered under — so eventOf reads an ABSENT name as the sole installed
// event. That default is what makes an explicitly named event the only way an
// unrecognized one can arrive, and the two ways it can are both real: an
// upstream event rename, and a user pointing a handler under a different
// config key at irrlicht's beacon by hand. #1364's counter is what makes
// either visible instead of silent.
package antigravity

import (
	"testing"

	"irrlicht/core/internal/contracttesting"
	"irrlicht/core/ports/outbound"
)

func TestUnknownHookEventObserved(t *testing.T) {
	contracttesting.AssertUnknownHookEventObserved(t, contracttesting.UnknownEventReceiver{
		Adapter:         AdapterName,
		Root:            antigravityBrainRoot,
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
		KnownEvent:   HookEventStop,
		EndpointPath: HookEndpointPath,
	})
}
