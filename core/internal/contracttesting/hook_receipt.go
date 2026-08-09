// hook_receipt.go holds the issue #1368 contract every hook-RECEIVING agent
// adapter must satisfy: a consent-passed request is counted as a receipt for
// that adapter, whatever the receiver then decides to do with it.
//
// It is the fourth receiving-side contract, and unlike the other three the
// failure it guards against is a FALSE ACCUSATION rather than a silence.
//
// The hook-liveness watchdog convicts a channel that produces no receipts
// across N completed turns and demotes it to the transcript tier, releasing the
// holds it had placed. That is a real behavioural change to state detection —
// so a receiver that simply forgets to call hookjson.ObserveHookReceipt does not
// merely go unreported: it is reported as DEAD while working perfectly, and the
// product then stops trusting a channel that was fine. The watchdog cannot
// possibly detect that; a missing call and a missing hook look identical to it,
// which is precisely why the obligation has to be pinned from outside.
//
// The two obligations that are easiest to get subtly wrong are both about
// counting TOO LITTLE, and both would produce that false accusation on a live
// channel:
//
//   - an unrecognized event name (#1364) still proves the channel delivered.
//     A receiver that counts only names it handles reports a channel as dead
//     the moment upstream renames an event — turning a "we don't understand
//     these" problem into a "these aren't arriving" verdict, which sends the
//     reader to a different file and a different fix.
//   - a path-confinement rejection (#1361) still proves the channel delivered.
//     A receiver that counts only what it dispatches reports a misconfigured
//     transcript root as a dead channel.
//
// And one about counting too much: a request refused by the consent gate must
// NOT count. Observing that a POST arrived is an observation, and every
// observation an adapter makes is gated (AGENTS.md). It is also the honest
// answer — while consent is withheld the daemon has no basis for an opinion
// about whether the channel works.
package contracttesting

import (
	"fmt"
	"net/http"
	"testing"

	"irrlicht/core/adapters/inbound/agents/hookjson"
	"irrlicht/core/ports/outbound"
)

// HookReceiptReceiver wires one adapter's hook receiver into
// AssertHookReceiptObserved. Every field is required.
type HookReceiptReceiver struct {
	// Adapter is the name the receipt must be counted under — the same string
	// the receiver passes to hookjson.ObserveHookReceipt, and the same one the
	// permission service and the watchdog key on. Asserted, not assumed: a
	// receipt filed under the wrong name leaves the right channel reading as
	// dead while a channel nobody is watching looks busy.
	Adapter string

	// Root relocates the adapter's declared transcript root to a
	// test-controlled directory — by t.Setenv of whatever env var moves it —
	// creates it, and returns its absolute path. Called FIRST in every
	// sub-test, before New.
	Root func(t *testing.T) string

	// WriteTranscript writes a transcript the adapter will resolve a session id
	// from into dir, and returns its absolute path.
	WriteTranscript func(t *testing.T, dir string) string

	// New builds a fresh receiver whose consent gate GRANTS, logging into log.
	New func(t *testing.T, log outbound.Logger) http.Handler

	// NewDenied builds the same receiver with a consent gate that REFUSES.
	// Separate from New rather than a parameter on it because the denial case
	// is the one obligation whose expected count is zero, and a wiring that
	// silently ignored a "denied" flag would pass it by accident.
	NewDenied func(t *testing.T, log outbound.Logger) http.Handler

	// PayloadFor renders the adapter's hook JSON body carrying transcriptPath
	// and event.
	PayloadFor func(transcriptPath, event string) string

	// KnownEvent is an event name this adapter recognizes and dispatches on.
	KnownEvent string

	// EndpointPath is the adapter's hook route.
	EndpointPath string
}

// AssertHookReceiptObserved runs the issue #1368 contract against r. Four
// obligations, each independently failable:
//
//  1. a recognized event counts exactly one receipt for this adapter — the
//     positive case, and the one that fails outright on a receiver that never
//     calls ObserveHookReceipt at all;
//  2. an unrecognized event counts too. Alive-but-misunderstood is #1364's
//     diagnosis and must not be reported as this one;
//  3. a path-confinement rejection counts too. Alive-but-misrouted is #1361's
//     diagnosis and must not be reported as this one either;
//  4. a consent-denied request counts nothing, and is still answered 2xx.
//
// The counters are process-global and monotonic, so every obligation measures a
// DELTA around its own request rather than an absolute — two adapters, or two
// invocations, share a test binary without interfering.
func AssertHookReceiptObserved(t *testing.T, r HookReceiptReceiver) {
	t.Helper()
	t.Run("recognized_event_counts_one", func(t *testing.T) { assertReceiptCounted(t, r, r.KnownEvent) })
	t.Run("unrecognized_event_counts_too", func(t *testing.T) { assertReceiptCounted(t, r, "IrrReceiptUnknownEvent") })
	t.Run("confinement_rejected_request_counts_too", func(t *testing.T) { assertReceiptCountedOutOfTree(t, r) })
	t.Run("consent_denied_request_is_not_counted", func(t *testing.T) { assertDeniedNotCounted(t, r) })
}

// assertReceiptCounted is obligations 1 and 2: a request the receiver was
// allowed to see adds exactly one receipt, whether or not it understood it.
func assertReceiptCounted(t *testing.T, r HookReceiptReceiver, event string) {
	t.Helper()
	transcript := receiptTranscript(t, r, "in-tree")
	handler := r.New(t, &recordingLogger{})

	before := hookjson.HookReceipts()[r.Adapter]
	rec := postHookBody(t, handler, r.EndpointPath, r.PayloadFor(transcript, event))

	assertHookStatus2xx(t, rec, fmt.Sprintf("event %q", event))
	if got := hookjson.HookReceipts()[r.Adapter] - before; got != 1 {
		t.Errorf("event %q added %d receipts for adapter %q, want exactly 1 — a receiver that does not count its own traffic is reported as a DEAD channel by the liveness watchdog and demoted while working perfectly",
			event, got, r.Adapter)
	}
}

// assertReceiptCountedOutOfTree is obligation 3.
func assertReceiptCountedOutOfTree(t *testing.T, r HookReceiptReceiver) {
	t.Helper()
	// Relocate the root first so the receiver is confined to it, then write the
	// transcript somewhere else entirely — the request is well-formed and the
	// channel plainly delivered it; only the path is refused.
	r.Root(t)
	outside := r.WriteTranscript(t, mkSubdir(t, t.TempDir(), "out-of-tree"))
	handler := r.New(t, &recordingLogger{})

	before := hookjson.HookReceipts()[r.Adapter]
	rec := postHookBody(t, handler, r.EndpointPath, r.PayloadFor(outside, r.KnownEvent))

	assertHookStatus2xx(t, rec, "a confinement-rejected request")
	if got := hookjson.HookReceipts()[r.Adapter] - before; got != 1 {
		t.Errorf("a path-refused request added %d receipts for adapter %q, want 1 — a misconfigured transcript root (#1361) must not be reported as a channel that stopped delivering (#1368)",
			got, r.Adapter)
	}
}

// assertDeniedNotCounted is obligation 4.
func assertDeniedNotCounted(t *testing.T, r HookReceiptReceiver) {
	t.Helper()
	transcript := receiptTranscript(t, r, "in-tree")
	handler := r.NewDenied(t, &recordingLogger{})

	before := hookjson.HookReceipts()[r.Adapter]
	rec := postHookBody(t, handler, r.EndpointPath, r.PayloadFor(transcript, r.KnownEvent))

	assertHookStatus2xx(t, rec, "a consent-denied request")
	if got := hookjson.HookReceipts()[r.Adapter] - before; got != 0 {
		t.Errorf("a consent-denied request added %d receipts for adapter %q, want 0 — noting that a POST arrived is an observation, and every observation an adapter makes is gated",
			got, r.Adapter)
	}
}

// receiptTranscript relocates the adapter's root and writes a transcript inside
// it, so confinement accepts the path and the only thing under test is the
// counting.
func receiptTranscript(t *testing.T, r HookReceiptReceiver, name string) string {
	t.Helper()
	return r.WriteTranscript(t, mkSubdir(t, r.Root(t), name))
}
