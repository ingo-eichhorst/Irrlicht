// hookpath_test.go wires this adapter's hook receiver into the shared issue
// #1361 contract, under its PathDaemonDerived route (#1719).
//
// The route's usual premise — opencode's and hermes' — is that the adapter has
// no file per session for a caller to name. Antigravity's is different and
// worth stating rather than glossing, because it is a third reason the same
// route is the right one:
//
// Antigravity's hook payload DOES carry a `transcriptPath`, and it names
// `transcript_full.jsonl` — the unfiltered view, which this adapter
// deliberately refuses to own (adapter.go's sessionIDFromPath accepts only
// `transcript.jsonl`, so a conversation mints one session rather than two). So
// there is no path the caller could name that this adapter could key a session
// on. What it uses instead is `conversationId`, which IS the session id (it is
// the brain directory name), and the path is composed by the daemon from its
// own declared roots.
//
// The obligation the route runs is therefore the mirror image of confinement:
// nothing the caller writes may reach the dispatch. Its three obligations
// contradict the from-body route's directly — there the four hostile bodies
// must dispatch NOTHING, here each must dispatch the SAME string a clean body
// does — which is what makes this a declaration rather than an exemption.
//
// What this route does NOT grade, and what carries it instead: the traversal
// class does not vanish here, it MOVES, from the path to the conversation id
// that is joined into one. TestHookReceiver_RejectsAnUnsafeConversationID
// (hooks_test.go) is that half, with TestHookReceiver_AcceptsAConversationID-
// ThatIsNotAUUID as its vacuity guard.
package antigravity

import (
	"testing"

	"irrlicht/core/internal/contracttesting"
)

func TestHookPathConfined(t *testing.T) {
	contracttesting.AssertHookPathConfined(t, contracttesting.HookReceiver{
		Route: contracttesting.PathDaemonDerived,
		Root:  antigravityBrainRoot,
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
		// is the whole point: PayloadFor renders what antigravity itself sends
		// (minus the fields this receiver does not decode), and
		// ForeignPathPayload splices back in the `transcriptPath` key a hostile
		// caller would try. The contract refuses to run if the two render the
		// same body.
		PayloadFor:         func(string) string { return contractPayload("", HookEventStop) },
		ForeignPathPayload: foreignPathPayload,
		DerivedPath: func(t *testing.T) string {
			t.Helper()
			return transcriptPathFor(contractConversationID)
		},
		EndpointPath: HookEndpointPath,
	})
}
