// hook_receipt_selftest_test.go is the committed mutation evidence for
// AssertHookReceiptObserved (#1368), and it is the family where the recorded
// evidence had to be SPLIT rather than transcribed or derived (#1479, #1497).
//
// PR #1413's mutations live in a PR comment rather than its body, and they are
// two:
//
//	obligations 1-3 : both receivers stop calling hookjson.ObserveHookReceipt
//	obligation 4    : the count moved ABOVE the consent gate
//
// The first is JOINT, and joint is not good enough here. Obligations 2 and 3
// exist to pin WHERE the call sits — before the unrecognized-event switch, and
// before the confinement refusal — and removing the call entirely proves
// neither placement: a receiver that counts only what it dispatches, or only
// what it understands, passes obligation 1 and reddens nothing that names it.
// So this file adds the two placements #1413 never recorded, and each isolates
// the obligation that owns it.
//
// # Why a false green here is worse than elsewhere
//
// This is the only receiving-side contract whose failure is a false ACCUSATION
// rather than a silence. The liveness watchdog demotes a channel that produces
// no receipts across N completed turns and releases the holds it placed, so a
// receiver that forgets to count is reported DEAD while working perfectly — and
// the watchdog cannot tell that apart from a real outage. An arm that stopped
// discriminating would let that ship.
package contracttesting

import "testing"

// receiptArmBuilders mirrors AssertHookReceiptObserved's own t.Run bodies. A
// builder rather than a pre-built arm, for the reason armBuilder's doc gives:
// a drive constructs only the obligation it is about.
func receiptArmBuilders() []armBuilder {
	return []armBuilder{
		{"recognized_event_counts_one", func(t *testing.T, brk receiverBreak) func(armT) {
			r := fakeReceiptReceiver(brk)
			transcript := receiptTranscript(t, r, "in-tree")
			h := r.New(t, &recordingLogger{})
			return func(at armT) { assertReceiptCounted(at, r, h, transcript, r.KnownEvent) }
		}},
		{"unrecognized_event_counts_too", func(t *testing.T, brk receiverBreak) func(armT) {
			r := fakeReceiptReceiver(brk)
			transcript := receiptTranscript(t, r, "in-tree")
			h := r.New(t, &recordingLogger{})
			return func(at armT) { assertReceiptCounted(at, r, h, transcript, receiptUnknownEvent) }
		}},
		{"confinement_rejected_request_counts_too", func(t *testing.T, brk receiverBreak) func(armT) {
			r := fakeReceiptReceiver(brk)
			// Relocate the root first so the receiver is confined to it, then
			// write the transcript somewhere else entirely — the request is
			// well-formed and the channel plainly delivered it; only the path
			// is refused.
			r.Root(t)
			outside := r.WriteTranscript(t, mkSubdir(t, t.TempDir(), "out-of-tree"))
			h := r.New(t, &recordingLogger{})
			return func(at armT) { assertReceiptCountedOutOfTree(at, r, h, outside) }
		}},
		{"consent_denied_request_is_not_counted", func(t *testing.T, brk receiverBreak) func(armT) {
			r := fakeReceiptReceiver(brk)
			transcript := receiptTranscript(t, r, "in-tree")
			h := r.NewDenied(t, &recordingLogger{})
			return func(at armT) { assertDeniedNotCounted(at, r, h, transcript) }
		}},
	}
}

// receiptFamily is the obligation table this file's cases drive through
// receiverFamily's shared drive/silence pair.
var receiptFamily = receiverFamily{
	entryPoint: "AssertHookReceiptObserved",
	builders:   receiptArmBuilders,
}

// TestReceiptArms_PassACorrectReceiver is the family's vacuity guard. The
// receiver counts its receipt exactly where every shipped one does: after the
// channel consent gate and before everything else.
func TestReceiptArms_PassACorrectReceiver(t *testing.T) {
	receiptFamily.mustPassACorrectReceiver(t, receiverBreak{}, "a correct hook receiver")
}

// TestReceiptObligation1_RecognizedEventCountsOne is #1413's recorded mutation,
// narrowed to the one obligation it is evidence for: a receiver that never
// calls hookjson.ObserveHookReceipt at all.
//
// The recorded failure, verbatim from that PR's comment:
//
//	event "PermissionRequest" added 0 receipts for adapter "claude-code", want
//	exactly 1 — a receiver that does not count its own traffic is reported as a
//	DEAD channel by the liveness watchdog and demoted while working perfectly
//
// Obligation 4 stays silent, and that is not a technicality: a receiver that
// counts NOTHING satisfies "a consent-denied request counts nothing" perfectly.
// It is the same shape as #1446's M1 — the degenerate receiver passes every
// obligation phrased as an absence.
func TestReceiptObligation1_RecognizedEventCountsOne(t *testing.T) {
	brk := receiverBreak{receipt: receiptNever}

	mustReport(t, receiptFamily.drive(t, brk, "recognized_event_counts_one"),
		"added 0 receipts for adapter",
		"a receiver that never calls hookjson.ObserveHookReceipt")

	receiptFamily.mustBeSilentOn(t, brk, "a receiver that counts no receipts at all",
		"consent_denied_request_is_not_counted")
}

// TestReceiptObligation2_UnrecognizedEventCountsToo is the first of the two
// splits #1413 never recorded: a receiver that counts only the event names it
// HANDLES.
//
// That is the shape hook_receipt.go's own header names, and it is the one the
// joint mutation cannot reach — such a receiver passes obligation 1 with full
// marks. Its diagnosis is the damaging part: an upstream event rename would be
// reported as a channel that stopped delivering, sending the reader to #1368's
// file and #1368's fix when the actual problem is #1364's.
//
// The isolation is asymmetric on purpose, and the asymmetry is the finding.
// This receiver also reddens obligation 3, because a request refused at
// confinement never reaches the switch either — so it is obligation 3's
// mutation, below, that separates the two, not this one.
func TestReceiptObligation2_UnrecognizedEventCountsToo(t *testing.T) {
	brk := receiverBreak{receipt: receiptOnlyWhenRecognized}

	mustReport(t, receiptFamily.drive(t, brk, "unrecognized_event_counts_too"),
		"event \""+receiptUnknownEvent+"\" added 0 receipts for adapter",
		"a receiver that counts a receipt only for event names it recognizes")

	receiptFamily.mustBeSilentOn(t, brk, "a receiver that counts only recognized events",
		"recognized_event_counts_one", "consent_denied_request_is_not_counted")
}

// TestReceiptObligation3_ConfinementRejectedRequestCountsToo is the second
// split: a receiver that counts only what survives path confinement.
//
// It isolates obligation 3 exactly — obligations 1, 2 and 4 all stay silent —
// which is what makes it, and not obligation 2's mutation, the evidence that
// obligations 2 and 3 are two obligations rather than one written twice. Its
// diagnosis is a misconfigured transcript root (#1361) reported as a dead
// channel (#1368): again the right symptom filed under the wrong issue.
func TestReceiptObligation3_ConfinementRejectedRequestCountsToo(t *testing.T) {
	brk := receiverBreak{receipt: receiptAfterConfinement}

	mustReport(t, receiptFamily.drive(t, brk, "confinement_rejected_request_counts_too"),
		"a path-refused request added 0 receipts for adapter",
		"a receiver that counts a receipt only once the path has survived confinement")

	receiptFamily.mustBeSilentOn(t, brk, "a receiver that counts only what it dispatches",
		"recognized_event_counts_one", "unrecognized_event_counts_too",
		"consent_denied_request_is_not_counted")
}

// TestReceiptObligation4_ConsentDeniedRequestIsNotCounted is #1413's second
// recorded mutation: the count moved ABOVE the consent gate. Its failure,
// verbatim from that PR's comment:
//
//	a consent-denied request added 1 receipts for adapter "claude-code", want 0
//	— noting that a POST arrived is an observation, and every observation an
//	adapter makes is gated
//
// The ordering it pins is the one consent.go documents as load-bearing in two
// directions at once: the channel key BEFORE the receipt so a denied request
// counts none, and the transcript key AFTER it so a hooks-granted /
// transcripts-denied install is not falsely reported dead. This mutation moves
// the receipt past the first of those, and nothing but obligation 4 notices.
func TestReceiptObligation4_ConsentDeniedRequestIsNotCounted(t *testing.T) {
	brk := receiverBreak{receipt: receiptBeforeChannelConsent}

	mustReport(t, receiptFamily.drive(t, brk, "consent_denied_request_is_not_counted"),
		"a consent-denied request added 1 receipts for adapter",
		"a receiver that counts the receipt before the consent gate it is meant to sit behind")

	receiptFamily.mustBeSilentOn(t, brk, "a receiver that counts before consent",
		"recognized_event_counts_one", "unrecognized_event_counts_too",
		"confinement_rejected_request_counts_too")
}
