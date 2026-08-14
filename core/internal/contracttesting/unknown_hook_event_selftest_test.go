// unknown_hook_event_selftest_test.go is the committed mutation evidence for
// AssertUnknownHookEventObserved (#1364), and it is the family where half of it
// had to be RE-DERIVED rather than transcribed (#1479, #1497).
//
// PR #1403's evidence is a PRE-FIX RUN, not a mutation table: the contract and
// both adapter wirings were written first, with the two `default:` branches
// untouched, so obligations 3 and 4 reddened together against main's
// log-at-Info-and-200 behaviour. That is real red-first evidence and it is
// transcribed below. Obligations 1 and 2 have none — the PR names them
// explicitly as LOCKS ("passed pre-fix ... their green says nothing about the
// defect") — so their mutations are derived here for the first time, from what
// the arms' own doc comments say they are for.
//
// Deriving them was worth doing rather than skipping. Obligation 1 turns out to
// carry two independent claims that the single recorded phrase "the vacuity
// guard" hides: that a recognized event still DISPATCHES, and that it is
// counted as unrecognized by NOBODY. A receiver can fail either without the
// other, so each gets its own mutation below.
// # A note on why obligation 4 is absent from every silence list
//
// It is the only obligation that must spend a GLOBALLY-FRESH event name (it
// grades first-sighting behaviour), and hookjson retains at most 64 distinct
// (adapter, name) pairs for the life of the process, never resetting. Naming it
// in four silence lists cost four extra drives — eight permanent slots — per
// run, which took this package from passing `go test -count=5` to failing
// `-count=3` (review of PR #1512). Its own case and the vacuity guard still
// drive it; what is given up is the weakest evidence in the file, since none of
// the four mutations touches the reporting path obligation 4 grades.
//
// The residual limit is real and bounded rather than hidden:
// assertNameTableHadRoom turns a saturated table into an explicit "this is a
// limit of the TEST BINARY, not a defect in the receiver" failure instead of
// the false accusation ("counted 0") it used to produce.
package contracttesting

import "testing"

// unknownArmBuilders is one builder per obligation, mirroring
// AssertUnknownHookEventObserved's own t.Run bodies — the same restatement,
// kept honest by the same vacuity guard, that confinementArmBuilders makes.
//
// Builders rather than pre-built arms because a drive must construct ONLY the
// obligation it is about. An earlier draft built all four and discarded three,
// which quadrupled the globally-fresh event names this file spends against
// hookjson's 64-pair, never-reset table and took the package from passing
// -count=5 to failing -count=3 (review of PR #1512). Only obligation 4 needs a
// fresh name at all; 2 and 3 measure deltas and use reusableEventName.
func unknownArmBuilders() []armBuilder {
	return []armBuilder{
		{"known_event_still_dispatched", func(t *testing.T, brk receiverBreak) func(armT) {
			r := fakeUnknownEventReceiver(brk)
			log := &recordingLogger{}
			transcript := unknownEventTranscript(t, r)
			rut := r.New(t, log)
			return func(at armT) { assertKnownEventUncounted(at, r, rut, log, transcript) }
		}},
		{"unknown_event_answered_2xx_not_dispatched", func(t *testing.T, brk receiverBreak) func(armT) {
			r := fakeUnknownEventReceiver(brk)
			transcript := unknownEventTranscript(t, r)
			rut := r.New(t, &recordingLogger{})
			event := reusableEventName(r.Adapter)
			return func(at armT) { assertUnknownIgnored(at, r, rut, transcript, event) }
		}},
		{"counted_per_adapter_and_name", func(t *testing.T, brk receiverBreak) func(armT) {
			r := fakeUnknownEventReceiver(brk)
			transcript := unknownEventTranscript(t, r)
			rut := r.New(t, &recordingLogger{})
			renamed, stray := reusableEventName(r.Adapter)+"Renamed", reusableEventName(r.Adapter)+"Stray"
			return func(at armT) { assertUnknownCounted(at, r, rut, transcript, renamed, stray) }
		}},
		{"reported_once_per_distinct_name", func(t *testing.T, brk receiverBreak) func(armT) {
			r := fakeUnknownEventReceiver(brk)
			log := &recordingLogger{}
			transcript := unknownEventTranscript(t, r)
			rut := r.New(t, log)
			flooding, second := unknownEventName(t)+"Flood", unknownEventName(t)+"Second"
			return func(at armT) { assertUnknownReportedOnce(at, r, rut, log, transcript, flooding, second) }
		}},
	}
}

// unknownEventFamily is the obligation table this file's cases drive through
// receiverFamily's shared drive/silence pair.
var unknownEventFamily = receiverFamily{
	entryPoint: "AssertUnknownHookEventObserved",
	builders:   unknownArmBuilders,
}

// TestUnknownEventArms_PassACorrectReceiver is the family's vacuity guard,
// against a receiver built the production way — hookjson.NewHandler, decoding
// through hookjson.DecodeConfined, counting through hookjson.IgnoreUnknownEvent.
func TestUnknownEventArms_PassACorrectReceiver(t *testing.T) {
	unknownEventFamily.mustPassACorrectReceiver(t, receiverBreak{}, "a correct hook receiver")
}

// TestUnknownEventObligation1_RecognizedEventStillDispatches is the first of
// obligation 1's two derived mutations: a receiver that recognizes nothing.
//
// This is the shape the arm's Fatal exists for, and #1403 recorded no mutation
// for it because on main it was true by construction. It is the same argument
// #1446's M1 makes for the confinement family: obligations 2-4 are all
// assertions about what an unrecognized event must NOT do, and a receiver that
// treats every event as unrecognized satisfies all three while being broken.
func TestUnknownEventObligation1_RecognizedEventStillDispatches(t *testing.T) {
	brk := receiverBreak{ignoresKnownEvent: true}

	mustReport(t, unknownEventFamily.drive(t, brk, "known_event_still_dispatched"),
		"was not dispatched — every obligation below it would pass vacuously",
		"a receiver that recognizes no event at all, so every name goes down the unrecognized branch")

	unknownEventFamily.mustBeSilentOn(t, brk, "a receiver that recognizes nothing",
		"unknown_event_answered_2xx_not_dispatched", "counted_per_adapter_and_name")
}

// TestUnknownEventObligation1_RecognizedEventIsCountedByNobody is obligation
// 1's second derived mutation, and the one the arm's doc comment names: a
// receiver that counts EVERYTHING.
//
// It matters because such a receiver is indistinguishable from a correct one on
// obligations 2, 3 and 4 — all three of which are about unrecognized names —
// while reporting an upstream rename that never happened, on every tool call,
// forever. The diagnostic value of the counter is exactly its silence on
// healthy traffic.
func TestUnknownEventObligation1_RecognizedEventIsCountedByNobody(t *testing.T) {
	brk := receiverBreak{countsEveryEvent: true}

	mustReport(t, unknownEventFamily.drive(t, brk, "known_event_still_dispatched"),
		"to the unrecognized-event counters, want 0",
		"a receiver that counts a recognized event as unrecognized as well")

	unknownEventFamily.mustBeSilentOn(t, brk, "a receiver that counts every event",
		"unknown_event_answered_2xx_not_dispatched", "counted_per_adapter_and_name")
}

// TestUnknownEventObligation2_UnknownEventIsNotDispatched is obligation 2's
// derived mutation: the receiver guesses a handler for a name it does not know.
//
// #1403 recorded none, because before the fix both receivers already ignored
// the payload — the defect that issue was about was the SILENCE, not a wrong
// dispatch. The obligation is still the one that has to fail here: an
// unfamiliar name reaching a handler chosen by fallback is worse than one
// ignored, and nothing else in the family would notice.
func TestUnknownEventObligation2_UnknownEventIsNotDispatched(t *testing.T) {
	brk := receiverBreak{dispatchesUnknown: true}

	mustReport(t, unknownEventFamily.drive(t, brk, "unknown_event_answered_2xx_not_dispatched"),
		"was dispatched downstream — an unfamiliar name must not be guessed into a handler",
		"a receiver that guesses a handler for an event name it does not recognize")

	unknownEventFamily.mustBeSilentOn(t, brk, "a receiver that dispatches unrecognized events",
		"known_event_still_dispatched", "counted_per_adapter_and_name")
}

// TestUnknownEventObligation2_UnknownEventAnswers2xx is the other half of
// obligation 2's statement. The rule is shared — assertHookStatus2xx grades it
// for three families — and it is pinned here as well as in the confinement
// family because this is the branch where a receiver is most tempted to answer
// 4xx: it genuinely does not understand the request. gemini-cli's pre-tool
// hooks and Copilot's preToolUse fail CLOSED on an error result, so for those
// adapters a 400 on an unrecognized name would block the user's tool call.
func TestUnknownEventObligation2_UnknownEventAnswers2xx(t *testing.T) {
	brk := receiverBreak{refusesUnknownWith4xx: true}

	mustReport(t, unknownEventFamily.drive(t, brk, "unknown_event_answered_2xx_not_dispatched"),
		"status = 400, want 2xx",
		"a receiver that answers an unrecognized event with a status code")

	unknownEventFamily.mustBeSilentOn(t, brk, "a receiver that answers 4xx on an unrecognized event",
		"known_event_still_dispatched", "counted_per_adapter_and_name")
}

// TestUnknownEventObligation3_CountedPerAdapterAndName transcribes #1403's
// pre-fix run, reproduced here as a receiver that files every unrecognized name
// under one key.
//
// The recorded failure is the same in shape and in prose:
//
//	event "...Renamed" arrived 3 times, counted 0 for adapter "claude-code" —
//	an event that keeps arriving is the signature of an upstream rename and has
//	to be countable as such
//
// A single scalar cannot tell an upstream rename, which fires on every tool
// call forever, from one stray POST — and telling those apart is the whole
// diagnostic value.
func TestUnknownEventObligation3_CountedPerAdapterAndName(t *testing.T) {
	brk := receiverBreak{collapsesEventNames: true}

	mustReport(t, unknownEventFamily.drive(t, brk, "counted_per_adapter_and_name"),
		"arrived 3 times, counted 0 for adapter",
		"a receiver that counts every unrecognized name under one collapsed key")

	unknownEventFamily.mustBeSilentOn(t, brk, "a receiver that collapses every unrecognized name into one key",
		"known_event_still_dispatched", "unknown_event_answered_2xx_not_dispatched")
}

// TestUnknownEventObligation4_ReportedOncePerDistinctName transcribes the other
// half of #1403's pre-fix run — literally what both receivers did before the
// fix: an Info log per arrival.
//
// The recorded failure named five lines for five arrivals; this fixture keeps
// hookjson.IgnoreUnknownEvent's own once-per-name line as well, so the count is
// six. That difference is deliberate. It isolates the REPORTING axis: the
// counters stay correct, so obligation 3 remains green and the only thing wrong
// is that the evidence is buried under one line per tool call.
func TestUnknownEventObligation4_ReportedOncePerDistinctName(t *testing.T) {
	brk := receiverBreak{logsEveryUnknown: true}

	mustReport(t, unknownEventFamily.drive(t, brk, "reported_once_per_distinct_name"),
		"arrived 5 times and produced 6 log line(s), want exactly 1",
		"a receiver that reports every arrival of an unrecognized name rather than every distinct name")

	unknownEventFamily.mustBeSilentOn(t, brk, "a receiver that logs one line per unrecognized arrival",
		"known_event_still_dispatched", "unknown_event_answered_2xx_not_dispatched",
		"counted_per_adapter_and_name")
}
