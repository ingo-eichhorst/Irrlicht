// hookunknown_test.go wires this adapter's hook receiver into the shared issue
// #1364 contract: an event name the receiver does not recognize is counted per
// (adapter, name), reported once per distinct name, and still answered 2xx.
//
// It is not a hypothetical for this adapter. hermes' VALID_HOOKS carries 25
// event names and this adapter subscribes to three of them, so a user (or a
// future irrlichd) adding a fourth to the same `hooks:` block delivers a name
// this switch has never heard of — the entries in config.yaml survive a daemon
// downgrade the same way pi's and opencode's installed files do. And hermes
// warns-and-skips an unknown event name in its own config rather than failing,
// so a rename upstream is a live-traffic condition rather than a crash.
package hermes

import (
	"testing"

	"irrlicht/core/internal/contracttesting"
	"irrlicht/core/ports/outbound"
)

func TestUnknownHookEventObserved(t *testing.T) {
	contracttesting.AssertUnknownHookEventObserved(t, contracttesting.UnknownEventReceiver{
		Adapter:         AdapterName,
		Root:            hermesHome,
		WriteTranscript: writeContractTranscript,
		New: func(t *testing.T, log outbound.Logger) contracttesting.UnknownEventReceiverUnderTest {
			t.Helper()
			target := &mockTarget{}
			return contracttesting.UnknownEventReceiverUnderTest{
				Handler:  NewHookHandler(target, nil, log),
				Observed: func() bool { return target.totalCalls() > 0 },
			}
		},
		PayloadFor:   func(_, event string) string { return unknownEventPayload(event) },
		KnownEvent:   HookEventOnSessionEnd,
		EndpointPath: HookEndpointPath,
	})
}

// unknownEventPayload renders a body for an arbitrary event name. It always
// puts the id in the top-level field, because the contract drives it with
// names that are not hermes events at all and so have no "which field does
// hermes fill" answer of their own — contractPayload's per-event split would
// silently send those through the approval spelling.
func unknownEventPayload(event string) string {
	return `{"hook_event_name":"` + event + `","session_id":"` + contractSessionID + `","extra":{}}`
}
