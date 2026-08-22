// hookpath_test.go wires this adapter's hook receiver into the shared issue
// #1361 contract, under its PathDaemonDerived route (#1719).
//
// Every other receiver in the tree takes the PathFromBody route: the payload
// names a transcript file and the receiver confines that caller-supplied string
// to the adapter's declared roots. This one cannot, and the difference is
// structural rather than a shortfall — opencode's Source is an
// agent.ProcessOwnedStore, so there is no file per session for a caller to
// name, and the path every consumer keys an opencode session on is composed by
// the daemon from its own store resolver.
//
// So the obligation the route runs is the mirror image: nothing the caller
// writes may reach the dispatch. Its three obligations contradict the from-body
// route's directly (there the four hostile bodies must dispatch NOTHING; here
// each must dispatch the SAME string a clean body does), which is what makes
// this a declaration rather than an exemption — declaring the wrong route
// fails, it does not pass quietly.
package opencode

import (
	"testing"

	"irrlicht/core/internal/contracttesting"
)

func TestHookPathConfined(t *testing.T) {
	contracttesting.AssertHookPathConfined(t, contracttesting.HookReceiver{
		Route: contracttesting.PathDaemonDerived,
		Root:  opencodeStoreRoot,
		New: func(t *testing.T) contracttesting.HookReceiverUnderTest {
			t.Helper()
			target := &mockTarget{}
			return contracttesting.HookReceiverUnderTest{
				Handler:      NewHookHandler(target, nil, mockLogger{}),
				Observed:     func() bool { return target.totalCalls() > 0 },
				ObservedPath: target.observedPath,
			}
		},
		// Both closures ignore the transcript path the contract offers, which
		// is the whole point: PayloadFor renders exactly what plugin.js writes,
		// and ForeignPathPayload splices in the key a hostile caller would try.
		// The contract refuses to run if those two render the same body.
		PayloadFor:         func(string) string { return contractPayload(HookEventSessionIdle) },
		ForeignPathPayload: foreignPathPayload,
		DerivedPath: func(t *testing.T) string {
			t.Helper()
			return transcriptPathFor(contractSessionID)
		},
		EndpointPath: HookEndpointPath,
	})
}
