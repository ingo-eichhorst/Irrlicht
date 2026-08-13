// receiver_fixture_test.go is the one hook receiver the three receiving-side
// families' negative self-tests are driven against, and the knobs that make it
// wrong in exactly one way at a time.
//
// # Why one fixture rather than three
//
// AssertHookPathConfined, AssertUnknownHookEventObserved and
// AssertHookReceiptObserved all grade a hook RECEIVER, and a receiver is not a
// small object: it needs declared transcript roots, a consent gate, a live
// hookjson.PathConfiner whose rejection counters the contract reads back off
// the handler, and the process-global unknown-event and receipt counters. Built
// three times it is three chances to drift; built once it is the shape every
// adapter's constructor actually produces (issue #1497).
//
// # It is built the production way
//
// newFakeHookReceiver goes through hookjson.NewHandler with a real
// PathConfiner and a real Consent from hookjson.RequireConsent, and its default
// request path is hookjson.DecodeConfined — the same chokepoint every shipped
// receiver decodes through since #1389/#1488. That matters for the vacuity
// guards above all: an arm proved silent against this receiver is proved silent
// against production code, not against a hand-written stand-in.
//
// Since #1390 a receiver has exactly ONE exported constructor returning a
// hookjson.HookHandler, so "this handler confines" and "the counter proving it"
// cannot be two objects that disagree. This fixture preserves that: there is no
// second, test-shaped constructor, and HookReceiverUnderTest.Handler is the
// value NewHandler returned.
//
// # What a mutation may and may not touch
//
// A negative self-test grades an ARM. It may therefore build a wrong RECEIVER;
// it may not reach into hookjson. That line is not cosmetic — it is the honest
// limit of what this file can prove, and it lands differently per family:
//
//   - Obligations 1 and 2 of the confinement family are reached by declaring
//     the wrong ROOTS (none at all; one so broad it swallows the decoy). Those
//     receivers run the entire production path, DecodeConfined included, and
//     are ordinary adapter defects — ConfinerForSource's doc comment warns
//     about the first by name.
//   - Obligations 3, 4 and 5 grade a VERDICT that hookjson.PathConfiner
//     reaches. #1380 and #1446 recorded their mutations as edits to confine.go
//     itself, which a self-test in this package cannot make. So the fixture
//     reproduces each recorded verdict pattern instead: it consults the real
//     confiner — which is also the only way to reach the rejection counter the
//     arms read — and then second-guesses its answer in exactly the direction
//     that mutation produced. The claim the self-test then supports is stated
//     precisely: the arm reports when the receiver reaches the WRONG VERDICT on
//     that input. That is the obligation. It is not a claim about confine.go,
//     which has its own tests.
//   - Obligation 6 needs no override at all. It grades which STRING travels
//     after a correct verdict, and forwarding the caller's own spelling is a
//     one-line receiver defect — the exact one #1465 found green.
//
// One limit of the hand-rolled path is worth stating rather than leaving to be
// discovered. It is a SECOND implementation of the chokepoint, and the baseline
// test proves it faithful only with respect to what the six arms look at:
// DecodeConfined's 1 MiB MaxBytesReader bound and its get/set postcondition are
// exercised by no arm here, so a hand-rolled decode that dropped them would
// still read as faithful. Both are covered where they live, in hookjson's own
// tests (review of PR #1512).
package contracttesting

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"irrlicht/core/adapters/inbound/agents/hookjson"
	"irrlicht/core/domain/permission"
	"irrlicht/core/ports/outbound"
)

const (
	// fakeAdapterName is the name the process-global unknown-event and receipt
	// counters are keyed by. Every contract reads them as a DELTA, so one
	// constant shared by every self-test in the package is safe and keeps the
	// "counted under the right adapter" assertions meaningful.
	fakeAdapterName = "contracttesting-selftest"

	fakeEndpointPath  = "/api/v1/hooks/selftest"
	fakeTranscriptExt = ".jsonl"
	fakeKnownEvent    = "Stop"
	fakeLogComponent  = "selftest-hook-receiver"
)

// fakeHookPayload is the hook envelope this receiver speaks. Two fields,
// because two fields are what all three families exercise: the caller-supplied
// path that must be confined, and the event name that may not be recognized.
type fakeHookPayload struct {
	TranscriptPath string `json:"transcript_path"`
	HookEventName  string `json:"hook_event_name"`
}

// --- the knobs ---

// confineOverride names how the receiver second-guesses the shared confiner.
// The zero value overrides nothing, which is why it is also the correct value:
// a fixture whose CORRECT setting has to be spelled out is a fixture that can
// be wrong by omission.
type confineOverride int

const (
	// confineFaithful takes hookjson.PathConfiner's verdict unchanged, through
	// hookjson.DecodeConfined — the production path, end to end.
	confineFaithful confineOverride = iota

	// confineRawPrefix accepts anything whose CALLER-SUPPLIED string starts
	// with the declared root: containment by lexical prefix, uncleaned and
	// unresolved. Reproduces #1380's mutation 3 ("the .. guard dropped"), which
	// the #1446 matrix records as red on obligations 2, 3 and 4.
	confineRawPrefix

	// confineCleanedPrefix cleans the path and then checks the prefix, but
	// never resolves symlinks — the CONTAINMENT-BEFORE-RESOLUTION inversion
	// #1361 was filed about, and #1380's mutation 4. A guard in this order
	// passes every lexical traversal test and confines nothing. The #1446
	// matrix records it as red on obligations 4 and 5 alone, which is what
	// makes it the ordering obligation's own mutation.
	confineCleanedPrefix

	// confineAcceptUnresolvable waves through a leaf that will not resolve,
	// treating "cannot resolve" as "not written yet" — a reasonable-looking
	// allowance, since the hook fires around the write. It is #1380's mutation
	// 6, red on obligation 5 and nothing else.
	confineAcceptUnresolvable
)

// receiptPlacement names where in the request the receiver counts its #1368
// receipt. The zero value is the correct placement.
type receiptPlacement int

const (
	// receiptAfterChannelConsent is immediately after the "hooks" gate and
	// before everything else — what every shipped receiver does.
	receiptAfterChannelConsent receiptPlacement = iota

	// receiptNever is the receiver that forgets entirely. #1413's recorded
	// mutation, which reddened obligations 1, 2 and 3 jointly.
	receiptNever

	// receiptBeforeChannelConsent counts a request the user never consented to
	// the daemon observing. #1413's obligation-4 mutation.
	receiptBeforeChannelConsent

	// receiptAfterConfinement counts only what survived path confinement — the
	// receiver that "counts what it dispatches". Not recorded anywhere; it is
	// the split #1497 asks for, isolating obligation 3.
	receiptAfterConfinement

	// receiptOnlyWhenRecognized counts only event names it handles. Also not
	// recorded; it is the other half of that split, and the shape hook_receipt.go's
	// own header names ("a receiver that counts only names it handles").
	receiptOnlyWhenRecognized
)

// receiverBreak is the deliberate wrongness, one field per mutation. Its zero
// value is a CORRECT receiver, so a self-test names exactly what it broke and
// nothing else — which is what lets mustBeSilent and mustReport disagree for a
// reason the reader can see.
type receiverBreak struct {
	// --- path confinement (#1361, #1389, #1390) ---

	// roots replaces the declared transcript roots. nil means "the root the
	// wiring relocated to", i.e. correct. It has hookjson.NewPathConfiner's own
	// signature, so a mutation supplies exactly what production supplies.
	roots func() []string

	confine confineOverride

	// forwardCallersSpelling dispatches the string the caller sent instead of
	// the confined one — #1465's no-op `set`, which obligations 1-5 are all
	// green against.
	forwardCallersSpelling bool

	// handRolledDecode takes this file's own decode while keeping the
	// confiner's verdict unchanged. It is not a mutation: it is the BASELINE
	// for the verdict overrides, which cannot use DecodeConfined because
	// DecodeConfined offers no way to be wrong. Without it, a self-test for
	// obligation 3/4/5 would differ from a correct receiver in TWO ways — the
	// verdict AND the decode — and could not say which one the arm reported.
	handRolledDecode bool

	// privateConfiner reaches every verdict correctly, but through a SECOND
	// PathConfiner built alongside the published one. #1446's M7a: the field
	// says one instance, the request path uses another, so every refusal lands
	// on a counter nobody reads. It is the only way a receiver can be right
	// about the decision and wrong about the evidence, which is the half of
	// obligations 2-5 the escape mutations leave untouched.
	privateConfiner bool

	// refusesPathWith4xx answers a confinement refusal with a status code. The
	// rule it breaks is shared — assertHookStatus2xx grades it for three
	// families — and it is not decoration: gemini-cli's pre-tool hooks and
	// Copilot's preToolUse fail CLOSED on an error result, so a receiver added
	// for one of those must not answer 4xx (RejectPath's doc records what was
	// and was not observed).
	refusesPathWith4xx bool

	// --- unrecognized events (#1364) ---

	// countsEveryEvent counts a RECOGNIZED event as unrecognized as well.
	countsEveryEvent bool

	// ignoresKnownEvent recognizes nothing: every event, including the one this
	// adapter handles, goes down the unrecognized branch and dispatches
	// nowhere. It is the unknown-event family's analogue of #1446's M1, and the
	// reason obligation 1 exists — a receiver that has stopped working at all
	// satisfies obligations 2 through 4 perfectly.
	ignoresKnownEvent bool

	// dispatchesUnknown guesses a handler for an unfamiliar name.
	dispatchesUnknown bool

	// refusesUnknownWith4xx answers an unfamiliar name with a status code
	// instead of a log line — the fail-closed hazard RejectPath's doc records.
	refusesUnknownWith4xx bool

	// collapsesEventNames counts every unrecognized name under one key, which
	// is the single scalar that cannot tell a rename from a stray POST.
	collapsesEventNames bool

	// logsEveryUnknown reports each arrival rather than each distinct name —
	// literally what both receivers did before #1364.
	logsEveryUnknown bool

	// --- receipts (#1368) ---

	receipt receiptPlacement
}

// --- the receiver ---

type fakeHookReceiver struct {
	root    string
	brk     receiverBreak
	log     outbound.Logger
	handler hookjson.HookHandler

	dispatched     bool
	dispatchedPath string
}

// newFakeHookReceiver builds one receiver rooted at root. There is exactly one
// constructor, and it returns the handler together with the confiner and the
// consent it actually uses — #1390's and #1488's property, preserved rather
// than re-litigated.
func newFakeHookReceiver(root string, brk receiverBreak, gate hookjson.ConsentGranter, log outbound.Logger) *fakeHookReceiver {
	f := &fakeHookReceiver{root: root, brk: brk, log: log}
	roots := func() []string { return []string{root} }
	if brk.roots != nil {
		roots = brk.roots
	}
	f.handler = hookjson.NewHandler(
		hookjson.NewPathConfiner(roots, fakeTranscriptExt),
		hookjson.RequireConsent(gate, fakeAdapterName, selfTestHooks, selfTestTranscripts),
		f.serve)
	return f
}

// serve is the request sequence every shipped receiver writes, in the order
// consent.go documents as load-bearing: the channel key BEFORE the receipt so a
// consent-denied request counts none, and the transcript key AFTER it so a
// hooks-granted / transcripts-denied install is not reported dead.
func (f *fakeHookReceiver) serve(consent hookjson.Consent, c *hookjson.PathConfiner, w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if f.brk.receipt == receiptBeforeChannelConsent {
		hookjson.ObserveHookReceipt(fakeAdapterName)
	}
	if !consent.Granted(selfTestHooks) {
		w.WriteHeader(http.StatusOK)
		return
	}
	if f.brk.receipt == receiptAfterChannelConsent {
		hookjson.ObserveHookReceipt(fakeAdapterName)
	}
	if !consent.Granted(selfTestTranscripts) {
		w.WriteHeader(http.StatusOK)
		return
	}

	payload, ok := f.decode(consent, c, w, r)
	if !ok {
		return
	}
	if f.brk.receipt == receiptAfterConfinement {
		hookjson.ObserveHookReceipt(fakeAdapterName)
	}
	f.dispatch(w, payload)
}

// decode reads the body and confines the path it names.
//
// confineFaithful is the whole of the production path — DecodeConfined, which
// owns the body bound, the consent backstop and the get/set postcondition.
// Every other setting has to hand-roll it, because DecodeConfined offers no way
// to reach a wrong verdict; confineHandRolledFaithful exists so that difference
// is never the only difference between a correct fixture and a mutated one.
func (f *fakeHookReceiver) decode(consent hookjson.Consent, c *hookjson.PathConfiner, w http.ResponseWriter, r *http.Request) (fakeHookPayload, bool) {
	var p fakeHookPayload
	if f.brk.usesProductionDecode() {
		ok := hookjson.DecodeConfined(w, r, f.log, fakeLogComponent, consent, c, &p,
			func(p *fakeHookPayload) string { return p.TranscriptPath },
			func(p *fakeHookPayload, confined string) { p.TranscriptPath = confined })
		return p, ok
	}

	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		http.Error(w, "bad request: invalid JSON", http.StatusBadRequest)
		return p, false
	}
	raw := p.TranscriptPath

	// The real confiner is consulted first in EVERY setting. Two reasons, and
	// the second is not optional: its verdict is what the overrides below
	// second-guess, and its counters are the only evidence a refusal happened
	// at all — RejectPath logs and answers 2xx but counts nothing, so a fixture
	// that skipped Confine would leave RejectionCount at 0 and make every arm
	// report "counted 0 rejection(s)" for a reason that has nothing to do with
	// the mutation under test.
	deciding := c
	if f.brk.privateConfiner {
		deciding = hookjson.NewPathConfiner(func() []string { return []string{f.root} }, fakeTranscriptExt)
	}
	confined, reason := deciding.Confine(raw)
	confined, reason = f.override(raw, confined, reason)
	if reason != hookjson.RejectNone {
		if f.brk.refusesPathWith4xx {
			// INSTEAD of RejectPath, not after it: RejectPath writes the 200
			// itself, and a second WriteHeader is a no-op, so a fixture that
			// tried to "add" a 4xx would not be wrong at all. mustReport caught
			// exactly that while this file was being written, which is the
			// difference between a negative self-test and a hopeful one.
			f.log.LogError(fakeLogComponent, "", "rejected hook transcript_path "+strconv.Quote(raw))
			http.Error(w, "refused: transcript_path outside the declared roots", http.StatusBadRequest)
			return p, false
		}
		hookjson.RejectPath(w, f.log, fakeLogComponent, raw, reason)
		return p, false
	}
	p.TranscriptPath = confined
	if f.brk.forwardCallersSpelling {
		p.TranscriptPath = raw
	}
	return p, true
}

// override applies the one deliberate verdict error. It only ever turns a
// REFUSAL into an acceptance: every recorded mutation in #1380 and #1446 that
// this file reproduces is an escape, and a fixture that could also refuse what
// the confiner accepted would be able to fail obligation 1 for reasons no
// obligation owns.
func (f *fakeHookReceiver) override(raw, confined string, reason hookjson.RejectReason) (string, hookjson.RejectReason) {
	if reason == hookjson.RejectNone {
		return confined, reason
	}
	switch f.brk.confine {
	case confineRawPrefix:
		if strings.HasPrefix(raw, f.root) {
			return filepath.Clean(raw), hookjson.RejectNone
		}
	case confineCleanedPrefix:
		if cleaned := filepath.Clean(raw); strings.HasPrefix(cleaned, f.root+string(filepath.Separator)) {
			return cleaned, hookjson.RejectNone
		}
	case confineAcceptUnresolvable:
		if reason == hookjson.RejectUnresolvable {
			return filepath.Clean(raw), hookjson.RejectNone
		}
	}
	return confined, reason
}

// usesProductionDecode reports whether this receiver can decode through
// hookjson.DecodeConfined — the production path, end to end.
//
// It is the ONE expression of the fork, and it is derived rather than declared.
// The first draft made "hand-rolled" a value of the confine enum, which
// conflated two independent axes (WHICH DECODE PATH, and WHICH VERDICT ERROR)
// and forced obligation 6's mutation to set two fields — quietly breaking this
// struct's own stated rule that a self-test names exactly one (review of
// PR #1512). Every knob below needs something DecodeConfined does not offer, so
// each one implies the hand-rolled path on its own.
func (b receiverBreak) usesProductionDecode() bool {
	return !b.handRolledDecode && b.confine == confineFaithful &&
		!b.forwardCallersSpelling && !b.privateConfiner && !b.refusesPathWith4xx
}

// dispatch is the event switch, and the second place a receipt can be counted
// wrongly.
func (f *fakeHookReceiver) dispatch(w http.ResponseWriter, p fakeHookPayload) {
	if p.HookEventName == fakeKnownEvent && !f.brk.ignoresKnownEvent {
		if f.brk.countsEveryEvent {
			hookjson.IgnoreUnknownEvent(f.log, fakeLogComponent, fakeAdapterName, "", p.HookEventName)
		}
		if f.brk.receipt == receiptOnlyWhenRecognized {
			hookjson.ObserveHookReceipt(fakeAdapterName)
		}
		f.dispatched = true
		f.dispatchedPath = p.TranscriptPath
		w.WriteHeader(http.StatusOK)
		return
	}

	if f.brk.dispatchesUnknown {
		f.dispatched = true
		f.dispatchedPath = p.TranscriptPath
	}
	if f.brk.logsEveryUnknown {
		f.log.LogInfo(fakeLogComponent, "", "ignored unrecognized hook event "+strconv.Quote(p.HookEventName))
	}
	name := p.HookEventName
	if f.brk.collapsesEventNames {
		name = "unknown"
	}
	hookjson.IgnoreUnknownEvent(f.log, fakeLogComponent, fakeAdapterName, "", name)
	if f.brk.refusesUnknownWith4xx {
		http.Error(w, "unrecognized hook event", http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// --- wiring the fixture into each family ---

// grantingGate is a ConsentGate with both declared permissions granted, which
// is the state every obligation except the receipt family's fourth runs under.
func grantingGate() *ConsentGate {
	g := NewConsentGate()
	g.SetState(selfTestHooks, permission.StateGranted)
	g.SetState(selfTestTranscripts, permission.StateGranted)
	return g
}

// writeFakeTranscript writes a well-formed transcript into dir. The out-of-tree
// decoy is written by this same function, so a refusal can never be confused
// with "the file was unreadable".
func writeFakeTranscript(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "11111111-2222-3333-4444-555555555555"+fakeTranscriptExt)
	if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
		t.Fatalf("write transcript %s: %v", path, err)
	}
	return path
}

// fakePayloadFor renders the hook body this receiver speaks.
//
// Marshalled from fakeHookPayload rather than formatted by hand, so the encoder
// and the decoder cannot disagree: a renamed json tag would otherwise leave the
// fixture posting the old key, the receiver decoding an empty transcript_path,
// and every arm failing for a reason unrelated to the mutation under test —
// the "failed, but NOT on the obligation" mode this package exists to remove.
func fakePayloadFor(transcriptPath, event string) string {
	body, err := json.Marshal(fakeHookPayload{TranscriptPath: transcriptPath, HookEventName: event})
	if err != nil {
		// Two strings into a two-field struct cannot fail to marshal. Panicking
		// keeps this callable from the wiring closures, which hold no reporter.
		panic("marshal fake hook payload: " + err.Error())
	}
	return string(body)
}

// fakePathReceiver wires the fixture into AssertHookPathConfined. root is
// captured so New builds a confiner over the directory Root just relocated to —
// the ordering the HookReceiver contract requires.
func fakePathReceiver(brk receiverBreak) HookReceiver {
	var built *fakeHookReceiver
	var root string
	return HookReceiver{
		Root: func(t *testing.T) string { root = t.TempDir(); return root },
		New: func(t *testing.T) HookReceiverUnderTest {
			t.Helper()
			built = newFakeHookReceiver(root, brk, grantingGate(), &recordingLogger{})
			return HookReceiverUnderTest{
				Handler:      built.handler,
				Observed:     func() bool { return built.dispatched },
				ObservedPath: func() string { return built.dispatchedPath },
			}
		},
		WriteTranscript: writeFakeTranscript,
		TranscriptExt:   fakeTranscriptExt,
		PayloadFor:      func(p string) string { return fakePayloadFor(p, fakeKnownEvent) },
		EndpointPath:    fakeEndpointPath,
	}
}

// fakeUnknownEventReceiver wires the fixture into
// AssertUnknownHookEventObserved.
func fakeUnknownEventReceiver(brk receiverBreak) UnknownEventReceiver {
	var built *fakeHookReceiver
	var root string
	return UnknownEventReceiver{
		Adapter: fakeAdapterName,
		Root:    func(t *testing.T) string { root = t.TempDir(); return root },
		New: func(t *testing.T, log outbound.Logger) UnknownEventReceiverUnderTest {
			t.Helper()
			built = newFakeHookReceiver(root, brk, grantingGate(), log)
			return UnknownEventReceiverUnderTest{
				Handler:  built.handler,
				Observed: func() bool { return built.dispatched },
			}
		},
		WriteTranscript: writeFakeTranscript,
		PayloadFor:      fakePayloadFor,
		KnownEvent:      fakeKnownEvent,
		EndpointPath:    fakeEndpointPath,
	}
}

// fakeReceiptReceiver wires the fixture into AssertHookReceiptObserved. New and
// NewDenied differ only in the consent gate, which is the point of the contract
// keeping them separate: a wiring that ignored the distinction would pass
// obligation 4 by accident.
func fakeReceiptReceiver(brk receiverBreak) HookReceiptReceiver {
	var root string
	build := func(t *testing.T, log outbound.Logger, gate hookjson.ConsentGranter) http.Handler {
		t.Helper()
		return newFakeHookReceiver(root, brk, gate, log).handler
	}
	return HookReceiptReceiver{
		Adapter: fakeAdapterName,
		Root:    func(t *testing.T) string { root = t.TempDir(); return root },
		New: func(t *testing.T, log outbound.Logger) http.Handler {
			return build(t, log, grantingGate())
		},
		NewDenied: func(t *testing.T, log outbound.Logger) http.Handler {
			g := NewConsentGate()
			g.SetState(selfTestHooks, permission.StateDenied)
			g.SetState(selfTestTranscripts, permission.StateDenied)
			return build(t, log, g)
		},
		WriteTranscript: writeFakeTranscript,
		PayloadFor:      fakePayloadFor,
		KnownEvent:      fakeKnownEvent,
		EndpointPath:    fakeEndpointPath,
	}
}

// --- driving one obligation of a receiver-shaped family ---

// armBuilder is one obligation's fixture-plus-arm, built on demand.
//
// A BUILDER rather than a pre-built arm because a drive must construct only the
// obligation it is about — building every obligation's fixture per drive and
// discarding all but one multiplied the process-global name slots the
// unrecognized-event family spends (review of PR #1512). Building outside
// observe is equally deliberate: observe runs the arm on its own goroutine
// (Fatal is runtime.Goexit), and t.Fatalf from a non-test goroutine is not
// something a fixture failure should ever depend on.
type armBuilder struct {
	name  string
	build func(*testing.T, receiverBreak) func(armT)
}

// receiverFamily is one contract family's obligation table, plus the entry
// point's name for the drift message.
//
// The three families' drive-and-silence helpers were byte-identical apart from
// the table and that one string, so they are one pair here (review of
// PR #1512). It lives beside receiverBreak rather than in selftest_test.go on
// purpose: the harness serves seven families and four of them have no
// receiverBreak, so teaching it about this one would be shared infrastructure
// learning a special case.
type receiverFamily struct {
	entryPoint string
	builders   func() []armBuilder
}

// drive builds and runs ONE named obligation against a receiver broken by brk,
// looked up BY NAME from the same table the vacuity guard walks — so a case
// cannot silently grade a different obligation than the one it claims to.
func (f receiverFamily) drive(t *testing.T, brk receiverBreak, obligation string) *recordingT {
	t.Helper()
	for _, b := range f.builders() {
		if b.name == obligation {
			return observe(t, b.build(t, brk))
		}
	}
	t.Fatalf("no obligation named %q — the mutation table and %s have drifted apart", obligation, f.entryPoint)
	return nil
}

// mustBeSilentOn drives every obligation in others against brk and asserts none
// of them reports. This is the half of the evidence that says WHICH obligation
// owns a wrongness, rather than merely that some obligation noticed it.
func (f receiverFamily) mustBeSilentOn(t *testing.T, brk receiverBreak, what string, others ...string) {
	t.Helper()
	for _, name := range others {
		t.Run("silent_on_"+name, func(t *testing.T) {
			mustBeSilent(t, f.drive(t, brk, name), what)
		})
	}
}

// mustPassACorrectReceiver runs every obligation against brk and asserts all of
// them stay silent — each family's vacuity guard. Without one, an arm that
// reported unconditionally would satisfy every mutation and read as excellent
// coverage.
func (f receiverFamily) mustPassACorrectReceiver(t *testing.T, brk receiverBreak, what string) {
	t.Helper()
	for _, b := range f.builders() {
		t.Run(b.name, func(t *testing.T) {
			mustBeSilent(t, observe(t, b.build(t, brk)), what)
		})
	}
}
