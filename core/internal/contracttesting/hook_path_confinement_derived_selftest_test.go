// hook_path_confinement_derived_selftest_test.go is the committed mutation
// evidence for AssertHookPathConfined's PathDaemonDerived route (#1719), the
// same lock hook_path_confinement_selftest_test.go is for the PathFromBody one.
//
// The route is new, so unlike its sibling there is no recorded matrix to
// transcribe. The three mutations below are therefore derived from what the
// route CLAIMS, one per claim, and each names the obligations it must leave
// SILENT — which is half the evidence: without it, three mutations against
// three obligations are equally satisfied by three arms that all report on
// everything.
//
// The middle one is the route's whole reason for existing. A receiver that
// dispatches the daemon's own path for an ordinary body, and the CALLER'S path
// as soon as one is present, is indistinguishable from a correct receiver on
// every other assertion in this package — it answers 2xx, it dispatches, it
// counts its receipts, its consent gating is intact. Only
// dispatch_is_indifferent_to_* separates them.
package contracttesting

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"testing"

	"irrlicht/core/adapters/inbound/agents/hookjson"
	"irrlicht/core/ports/outbound"
)

// sealedBreak names the ways a PathDaemonDerived receiver can be wrong. Its
// ZERO VALUE is a correct receiver, so a case names what it broke and nothing
// else — the same shape receiverBreak draws for the from-body route.
type sealedBreak struct {
	// silent makes the receiver decode and then dispatch nothing.
	silent bool

	// echoesTheBody makes the receiver dispatch a transcript_path the caller
	// supplied, when the body carries one, instead of the daemon's own path.
	// This is the defect the whole route exists to catch.
	echoesTheBody bool

	// holdsConfiner publishes a PathConfiner on the handler. A receiver with a
	// confiner has something to confine, which is the other route.
	holdsConfiner bool
}

// sealedEvent is the one event the fixture receiver understands.
const sealedEvent = "irr.sealed.event"

// sealedSessionID is the id the fixture's payloads carry, and the input its
// daemon-side path composition is a function of.
const sealedSessionID = "ses_selftest"

// sealedPayload is the fixture's payload type. It carries transcript_path
// deliberately — a correct receiver on this route IGNORES it, and a fixture
// that could not decode it could not express the echoesTheBody mutation at all.
type sealedPayload struct {
	HookEventName  string `json:"hook_event_name"`
	SessionID      string `json:"session_id"`
	TranscriptPath string `json:"transcript_path"`
}

// sealedDerivedPath is the "daemon composes it" half: a function of the session
// id and a root the daemon owns, never of anything on the wire.
func sealedDerivedPath(root string) string {
	return filepath.Join(root, "store.db-wal") + "?session=" + sealedSessionID
}

// silentLogger is the fixture's logger. The arms under test grade dispatch and
// the handler's fields; nothing here reads log output.
type silentLogger struct{}

func (silentLogger) LogInfo(string, string, string)                       {}
func (silentLogger) LogError(string, string, string)                      {}
func (silentLogger) LogProcessingTime(string, string, int64, int, string) {}
func (silentLogger) Close() error                                         { return nil }

var _ outbound.Logger = silentLogger{}

// fakeSealedReceiver builds the wiring and the receiver a case drives.
//
// The receiver is built the production way — hookjson.NewHandler over
// hookjson.RequireConsent, decoding through hookjson.DecodeSealed — so a
// silence here is a silence against production code rather than against a
// stand-in. The three mutations are all downstream of the decode, which is why
// this family needs no hand-rolled-but-faithful baseline the way the from-body
// one does: every case reaches its payload through the same call.
func fakeSealedReceiver(t *testing.T, brk sealedBreak) (HookReceiver, HookReceiverUnderTest) {
	t.Helper()
	root := t.TempDir()
	want := sealedDerivedPath(root)

	var dispatched string
	var confiner *hookjson.PathConfiner
	if brk.holdsConfiner {
		confiner = hookjson.NewPathConfiner(func() []string { return []string{root} }, "")
	}

	handler := hookjson.NewHandler(confiner,
		hookjson.RequireConsent(nil, "selftest", "hooks"),
		func(consent hookjson.Consent, _ *hookjson.PathConfiner, w http.ResponseWriter, r *http.Request) {
			var p sealedPayload
			if !hookjson.DecodeSealed(w, r, silentLogger{}, "selftest", consent, &p) {
				return
			}
			if p.HookEventName == sealedEvent && !brk.silent {
				if brk.echoesTheBody && p.TranscriptPath != "" {
					dispatched = p.TranscriptPath
				} else {
					dispatched = want
				}
			}
			w.WriteHeader(http.StatusOK)
		})

	// Assigned and then returned, rather than returned as two composite
	// literals in one statement: gofmt 1.25 and 1.27 indent that construct
	// differently, so the second spelling makes the file's formatting depend on
	// which toolchain last touched it.
	wiring := HookReceiver{
		Route: PathDaemonDerived,
		Root:  func(*testing.T) string { return root },
		PayloadFor: func(string) string {
			return sealedBody(sealedPayload{HookEventName: sealedEvent, SessionID: sealedSessionID})
		},
		ForeignPathPayload: func(path string) string {
			return sealedBody(sealedPayload{HookEventName: sealedEvent, SessionID: sealedSessionID, TranscriptPath: path})
		},
		DerivedPath:  func(*testing.T) string { return want },
		EndpointPath: "/api/v1/hooks/selftest",
	}
	rut := HookReceiverUnderTest{
		Handler:      handler,
		Observed:     func() bool { return dispatched != "" },
		ObservedPath: func() string { return dispatched },
	}
	return wiring, rut
}

func sealedBody(p sealedPayload) string {
	b, err := json.Marshal(p)
	if err != nil {
		panic(err)
	}
	return string(b)
}

// sealedArmBuilder is this family's obligation table entry. It is local rather
// than reusing armBuilder because that type is typed on receiverBreak, which is
// the from-body fixture's break set and cannot express any of the three above.
type sealedArmBuilder struct {
	name  string
	build func(*testing.T, sealedBreak) func(armT)
}

// sealedArmBuilders mirrors the PathDaemonDerived branch of
// AssertHookPathConfined, in its order and with its inputs.
//
// It restates that branch rather than sharing it, which is the trade every
// <family>_selftest_test.go in this package makes. The vacuity guard is what
// keeps the restatement honest: a construction that drifted into something an
// arm legitimately rejects turns TestDerivedRouteArms_PassACorrectReceiver red,
// not green.
//
// Only ONE of the four hostile spellings is reproduced here, and deliberately.
// The entry point runs all four; what differs between them is the FIXTURE PATH
// (an out-of-tree file, a traversal, two symlinks), and on this route the
// receiver never looks at that string at all, so four cases would grade one
// behaviour four times. The out-of-tree spelling is the one kept because it is
// the only one that needs no symlink support to plant.
func sealedArmBuilders() []sealedArmBuilder {
	return []sealedArmBuilder{
		{"well_formed_payload_dispatches", func(t *testing.T, brk sealedBreak) func(armT) {
			r, rut := fakeSealedReceiver(t, brk)
			body, want := r.PayloadFor(""), r.DerivedPath(t)
			return func(at armT) { assertDerivedDispatch(at, r, rut, body, want, "a well-formed payload") }
		}},
		{"dispatch_is_indifferent_to_out_of_tree", func(t *testing.T, brk sealedBreak) func(armT) {
			r, rut := fakeSealedReceiver(t, brk)
			hostile := filepath.Join(t.TempDir(), "planted.jsonl")
			body, want := r.ForeignPathPayload(hostile), r.DerivedPath(t)
			return func(at armT) { assertDerivedDispatch(at, r, rut, body, want, "a body naming out_of_tree") }
		}},
		{"no_confiner_held", func(t *testing.T, brk sealedBreak) func(armT) {
			_, rut := fakeSealedReceiver(t, brk)
			return func(at armT) { assertNoConfinerHeld(at, rut) }
		}},
	}
}

// driveSealed builds and runs ONE named obligation against a receiver broken by
// brk, looked up BY NAME so a case cannot silently grade a different obligation
// than the one it claims to.
func driveSealed(t *testing.T, brk sealedBreak, obligation string) *recordingT {
	t.Helper()
	for _, b := range sealedArmBuilders() {
		if b.name == obligation {
			return observe(t, b.build(t, brk))
		}
	}
	t.Fatalf("no obligation named %q — the mutation table and AssertHookPathConfined's PathDaemonDerived branch have drifted apart", obligation)
	return nil
}

// sealedMustBeSilentOn drives every obligation in others against brk and
// asserts none of them reports.
func sealedMustBeSilentOn(t *testing.T, brk sealedBreak, what string, others ...string) {
	t.Helper()
	for _, name := range others {
		t.Run("silent_on_"+name, func(t *testing.T) {
			mustBeSilent(t, driveSealed(t, brk, name), what)
		})
	}
}

// TestDerivedRouteArms_PassACorrectReceiver is the family's vacuity guard.
// Without it, an arm that reported unconditionally would satisfy every mutation
// below and read as excellent coverage.
func TestDerivedRouteArms_PassACorrectReceiver(t *testing.T) {
	for _, b := range sealedArmBuilders() {
		t.Run(b.name, func(t *testing.T) {
			mustBeSilent(t, observe(t, b.build(t, sealedBreak{})), "a correct daemon-derived receiver")
		})
	}
}

// TestDerivedRouteObligation1_WellFormedPayloadDispatches is the vacuity
// obligation of the route itself: a receiver that answers 2xx and dispatches
// nothing satisfies the two negative-shaped obligations perfectly, exactly as
// #1446's M1 does on the from-body route.
func TestDerivedRouteObligation1_WellFormedPayloadDispatches(t *testing.T) {
	brk := sealedBreak{silent: true}

	rec := driveSealed(t, brk, "well_formed_payload_dispatches")
	mustReport(t, rec, "a well-formed payload dispatched nothing",
		"a receiver that decodes the payload and then dispatches nothing")

	sealedMustBeSilentOn(t, brk, "a receiver that dispatches nothing", "no_confiner_held")
}

// TestDerivedRouteObligation2_DispatchIsIndifferentToTheBody is the route's
// reason for existing.
//
// The mutation is one line — dispatch the caller's transcript_path when the
// body carries one — and it is invisible to every other assertion in this
// package: the receiver still answers 2xx, still dispatches, still counts its
// receipt, still honours its consent. On the from-body route the equivalent
// receiver is the CORRECT one, which is precisely why the two routes'
// obligations have to contradict each other.
//
// The silence on obligation 1 is what makes this a separate obligation rather
// than a second spelling of it: a clean body carries no path, so an echoing
// receiver dispatches the daemon's own string for it and passes.
func TestDerivedRouteObligation2_DispatchIsIndifferentToTheBody(t *testing.T) {
	brk := sealedBreak{echoesTheBody: true}

	rec := driveSealed(t, brk, "dispatch_is_indifferent_to_out_of_tree")
	mustReport(t, rec, "want the daemon's own composed path",
		"a receiver that dispatches the caller's transcript_path whenever the body carries one")

	sealedMustBeSilentOn(t, brk, "a receiver that echoes a caller-supplied path only when one is present",
		"well_formed_payload_dispatches", "no_confiner_held")
}

// TestDerivedRouteObligation3_NoConfinerHeld pins the structural half of "the
// property was obtained". A receiver holding a confiner has something to
// confine, which is the PathFromBody route — and a nil confiner is what makes
// hookjson.DecodeConfined fail CLOSED if a later edit switches such a receiver
// back to the confining decode without giving the adapter roots.
func TestDerivedRouteObligation3_NoConfinerHeld(t *testing.T) {
	brk := sealedBreak{holdsConfiner: true}

	rec := driveSealed(t, brk, "no_confiner_held")
	mustReport(t, rec, "publishes a PathConfiner",
		"a receiver that publishes a confiner while declaring PathDaemonDerived")

	sealedMustBeSilentOn(t, brk, "a receiver that holds an unused confiner",
		"well_formed_payload_dispatches", "dispatch_is_indifferent_to_out_of_tree")
}
