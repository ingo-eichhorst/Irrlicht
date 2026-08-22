// hook_path_confinement.go holds the issue #1361 contract every hook-RECEIVING
// agent adapter must satisfy: the transcript_path in an inbound hook body is
// confined to the adapter's own declared transcript roots before anything
// downstream opens it.
//
// It is the receiving-side sibling of AssertHookEndpointFollowsBindAddr, and
// exists for the same reason: the obligation is a runtime choice a handler has
// to make, invisible to a static check, and the alternative is each new adapter
// remembering it. Two adapters received hooks when this was written and only
// one confined — claudecode derived a session id from the basename and handed
// the caller's raw string to the detector, which opened it, on a local
// unauthenticated endpoint. That is the failure this contract makes impossible
// to ship quietly at receiver three through ten.
//
// The ordering obligation is the one worth stating twice: symlinks must be
// resolved BEFORE containment is checked. A guard that checks first accepts a
// link planted inside the tree that points anywhere on the filesystem, while
// passing every lexical traversal test — it looks correct and confines nothing.
package contracttesting

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"irrlicht/core/adapters/inbound/agents/hookjson"
)

// HookReceiverUnderTest is one freshly built hook receiver plus the two things
// the contract must be able to see from outside it: whether a payload was
// dispatched, and what the receiver refused.
type HookReceiverUnderTest struct {
	// Handler is the adapter's hook receiver, as its constructor returns it.
	//
	// Typed as the concrete hookjson.HookHandler rather than http.Handler so the
	// rejection count can be read off THIS value — Handler.Confiner — instead of
	// arriving as a second, separately-supplied func that could name a different
	// instance. That is what makes "rejected and counted" evidence about the
	// handler under test.
	Handler hookjson.HookHandler

	// Observed reports whether the receiver dispatched anything downstream —
	// i.e. whether the supplied transcript_path escaped the receiver. This is
	// the assertion that actually matters; the status code is how the refusal
	// is reported, but a 400 alongside a dispatch would still be a breach.
	Observed func() bool

	// ObservedPath is the transcript path the receiver most recently dispatched,
	// or "" if it dispatched nothing.
	//
	// Observed answers WHETHER something was forwarded; this answers WHICH
	// STRING. The gap between those two is a whole class of defect the contract
	// could not see: a receiver that confines correctly, computes the confined
	// path, and then forwards the caller's original spelling anyway. Every
	// obligation below was green against exactly that, because each one only
	// ever asked whether a dispatch happened (found in review of #1389, by
	// replacing a receiver's write-back with a no-op).
	ObservedPath func() string
}

// PathRoute declares HOW a receiver obtains the transcript path it dispatches,
// and therefore which obligations AssertHookPathConfined can grade it on.
//
// It is a DECLARATION, not an exemption, for the same reason
// HookInstaller.Delivery is one (#1453): the two routes make CONTRADICTORY
// assertions about the same two strings, so declaring the wrong one cannot pass
// quietly. PathFromBody requires the dispatched path to VARY with what the
// caller named — and requires an out-of-tree spelling to be refused outright —
// while PathDaemonDerived requires it to be INVARIANT across every spelling a
// caller can put on the wire. A from-body receiver declaring PathDaemonDerived
// dispatches nothing for the hostile bodies and fails; a daemon-derived
// receiver declaring PathFromBody dispatches for all of them and fails.
type PathRoute int

const (
	// PathFromBody is the zero value and every receiver in the tree before
	// #1719: the payload names a transcript file, that string is
	// caller-supplied on a local unauthenticated endpoint, and the receiver
	// confines it to the adapter's declared roots before anything downstream
	// opens it. Six obligations.
	PathFromBody PathRoute = iota

	// PathDaemonDerived is a receiver whose payload names NO filesystem path,
	// because the adapter has no file for a caller to name. #1719's opencode is
	// the first: its Source is an agent.ProcessOwnedStore — one SQLite database,
	// no file per session — so the "transcript path" the daemon keys a session
	// on is a string the daemon COMPOSES from its own store resolver, and the
	// hook body carries a session id and stops there.
	//
	// This is the confinement family's counterpart to
	// DeliveryAddressFree: rather than confining the path-traversal class one
	// more time, the route makes it INEXPRESSIBLE — there is no caller-supplied
	// path on this receiver's dispatch path at all. So, exactly as the
	// address-free delivery route does, the obligations assert that the
	// property was actually OBTAINED (nothing the caller writes reaches the
	// dispatch, and the receiver holds no confiner because it has nothing to
	// confine) rather than being skipped.
	PathDaemonDerived
)

// HookReceiver wires one adapter's hook receiver into AssertHookPathConfined.
// Every field is required.
type HookReceiver struct {
	// Route declares how this receiver obtains the path it dispatches. The zero
	// value, PathFromBody, is the caller-supplied-path route every receiver
	// before #1719 takes; see PathRoute for why declaring the wrong one fails.
	Route PathRoute

	// Root relocates the adapter's declared transcript root to a
	// test-controlled directory — by t.Setenv of whatever env var moves it
	// ($HOME, $CODEX_HOME) — creates it, and returns its absolute path. Called
	// FIRST in every sub-test, before New.
	Root func(t *testing.T) string

	// New builds a fresh receiver, after Root has run, THROUGH THE ADAPTER'S
	// PRODUCTION CONSTRUCTOR — the one the daemon calls. Fresh per sub-test so
	// the rejection count starts at zero and Observed cannot carry over.
	//
	// There is nothing else to call: since #1390 each receiver has exactly one
	// exported constructor, so every obligation here binds the production path
	// rather than a handler the test assembled.
	New func(t *testing.T) HookReceiverUnderTest

	// WriteTranscript writes a transcript the adapter would dispatch on into
	// dir, and returns its absolute path. The contract uses it for the in-tree
	// fixture AND for the out-of-tree decoy, so a refusal can never be confused
	// with "the file was unreadable" — the decoy is exactly as well-formed as
	// the fixture, and the only thing standing between it and a dispatch is
	// confinement.
	WriteTranscript func(t *testing.T, dir string) string

	// TranscriptExt is the transcript file extension (e.g. ".jsonl"). Used to
	// name a decoy that must NOT exist, which is why it is declared rather
	// than read back off a throwaway WriteTranscript call.
	TranscriptExt string

	// PayloadFor renders the adapter's hook JSON body carrying transcriptPath,
	// on an event the adapter dispatches.
	PayloadFor func(transcriptPath string) string

	// EndpointPath is the adapter's hook route, used to build the request.
	EndpointPath string

	// DerivedPath returns the exact string a PathDaemonDerived receiver must
	// dispatch for the payload PayloadFor renders — the daemon's own composed
	// path, resolved AFTER Root has run so it lands inside the test's scratch
	// directories.
	//
	// Required for PathDaemonDerived and rejected for PathFromBody: a from-body
	// receiver has no single answer to give here (its dispatched path is a
	// function of what the caller sent), so a wiring that supplied one would be
	// declaring the wrong route.
	DerivedPath func(t *testing.T) string

	// ForeignPathPayload renders a body this receiver WILL dispatch on, with a
	// caller-supplied path spliced into it under whatever key a hostile caller
	// would try — i.e. the body a PathDaemonDerived receiver must be indifferent
	// to. Required for PathDaemonDerived and rejected for PathFromBody.
	//
	// The wiring renders it rather than the contract, because "the key a caller
	// would try" is adapter-shaped: it is the field name the adapter's own
	// payload struct would have carried had it carried one. The contract does
	// check that this actually produced a DIFFERENT body from PayloadFor's — a
	// wiring that ignored its argument would otherwise make the invariance
	// obligation compare a string to itself, which is the "a mechanism that
	// cannot run must fail loudly" rule in its local form.
	ForeignPathPayload func(path string) string
}

// AssertHookPathConfined runs the issue #1361 contract against r. Six
// obligations, each independently failable:
//
//  1. a transcript inside the declared root is still ACCEPTED — the vacuity
//     guard, without which a receiver that refuses everything passes 2–4 for
//     entirely the wrong reason;
//  2. a well-formed transcript outside every declared root is refused;
//  3. a path whose lexical prefix IS the declared root but which climbs out of
//     it with ".." is refused;
//  4. a symlink INSIDE the declared root pointing at a file outside it is
//     refused — the ordering obligation, and the only one a
//     containment-before-resolution guard fails;
//  5. a DANGLING symlink inside the declared root is refused. Resolution
//     reports "does not exist" for a broken link exactly as it does for a file
//     that has not been written yet, so a receiver that waves through an
//     unresolvable leaf (a reasonable allowance — the hook fires around the
//     write) hands the attacker an escape needing no race at all: plant the
//     broken link, get the path accepted, then create the target;
//  6. for a path that IS accepted, the string dispatched downstream is the
//     CONFINED spelling and not the caller's — see
//     assertConfinedSpellingDispatches. Added by #1389, because 1-5 are all
//     about the accept/refuse DECISION and every one of them is satisfied by a
//     receiver that decides correctly and then forwards the caller's string
//     anyway.
//
// Obligations 2–5 additionally require the refusal to be VISIBLE — counted by
// the confiner, so an operator can see it — and to answer 2xx.
//
// Note the numbering: an EARLIER sixth obligation, "the adapter's PRODUCTION
// constructor confines too", was retired in #1390 and is unrelated to the one
// above. It went because the count is now read off Handler.Confiner, so a
// handler that confines and the counter proving it can no longer be two objects
// that disagree, and obligations 2-5 fail directly when the production
// constructor stops confining. The full argument, including what that
// obligation never covered either, is in docs/testing-contracts.md under
// "Hook path confinement" (AGENTS.md/#1742 links to it rather than restating
// it). The two are near-opposites in spirit: the retired one policed
// WIRING and was replaced by a type guarantee, while #1389's polices the
// VALUE that travels and could not be replaced by one — nothing in the type
// system distinguishes a confined string from an unconfined one.
//
// The 2xx is asserted, not merely tolerated. A refused path is already fully
// contained by not being forwarded, so the status code buys no security; what
// it does buy is an untested interaction on the user's critical path. Claude
// Code documents that a non-2xx from a `type: http` hook "can't block actions",
// but not whether it surfaces an error; gemini-cli's pre-tool hooks and
// Copilot's preToolUse are known to fail CLOSED on an error result. A receiver
// added for one of those adapters must not answer 4xx, so the contract pins the
// rule rather than leaving each adapter to rediscover it (issues #1361, #1364).
func AssertHookPathConfined(t *testing.T, r HookReceiver) {
	t.Helper()

	// The route check and the PathDaemonDerived obligations are INLINE rather
	// than two helpers, because a helper taking *testing.T first is exactly what
	// seam_walk_test.go's rule 1 exists to stop: it could not be driven by a
	// negative self-test, so its own failure would be un-gradeable. The arms it
	// runs (assertDerivedDispatch, assertNoConfinerHeld) take reporter and ARE
	// driven, in hook_path_confinement_selftest_test.go.
	switch r.Route {
	case PathDaemonDerived:
		if r.DerivedPath == nil {
			t.Fatal("Route is PathDaemonDerived but DerivedPath is nil — this route's whole claim is that the daemon composes the path, so there is nothing to compare the dispatch against")
		}
		if r.ForeignPathPayload == nil {
			t.Fatal("Route is PathDaemonDerived but ForeignPathPayload is nil — without a body carrying a caller-supplied path, the invariance obligation would compare a string to itself")
		}
		// The PathDaemonDerived route. Three obligations, each independently
		// failable:
		//
		//  1. a well-formed payload IS still dispatched — the vacuity guard, without
		//     which a receiver that answers nothing at all satisfies 2 and 3 perfectly;
		//  2. the string dispatched is the daemon's own composed path, and the receiver
		//     holds no confiner — the two halves of "the property was obtained";
		//  3. the dispatched string does not move when the body names a path, for each
		//     of the four escapes the from-body route has to refuse.
		//
		// Obligation 3 is the one that makes this a route rather than an exemption: it
		// contradicts obligations 2-5 of the other route directly. There, each of those
		// four bodies must dispatch NOTHING; here, each must dispatch the SAME thing a
		// clean body does.
		t.Run("well_formed_payload_dispatches", func(t *testing.T) {
			r.Root(t)
			assertDerivedDispatch(realT(t), r, r.New(t), r.PayloadFor(""), r.DerivedPath(t), "a well-formed payload")
		})
		t.Run("dispatched_path_is_the_daemons_own", func(t *testing.T) {
			r.Root(t)
			rut := r.New(t)
			assertDerivedDispatch(realT(t), r, rut, r.PayloadFor(""), r.DerivedPath(t), "a well-formed payload")
			assertNoConfinerHeld(realT(t), rut)
		})
		for _, spelling := range hostilePathSpellings {
			t.Run("dispatch_is_indifferent_to_"+spelling.name, func(t *testing.T) {
				root := r.Root(t)
				want := r.DerivedPath(t)
				body := hostileBodyFor(t, r, spelling.path(t, root, t.TempDir()))
				assertDerivedDispatch(realT(t), r, r.New(t), body, want, "a body naming "+spelling.name)
			})
		}
		return
	case PathFromBody:
		// Deliberately a hard failure rather than a skip. The two routes'
		// required fields are disjoint, so a wiring that supplied a
		// PathDaemonDerived field on this route has either declared the wrong
		// route or copied a neighbour's wiring wholesale — and in both cases the
		// obligations that then run grade something other than what the author
		// believes. That is the shape #1453 records for the delivery routes and
		// the same answer applies here.
		if r.DerivedPath != nil || r.ForeignPathPayload != nil {
			t.Fatal("Route is PathFromBody (the zero value) but a PathDaemonDerived-only field is set — either the route declaration is wrong, or a neighbouring wiring was copied wholesale")
		}
	default:
		t.Fatalf("unknown PathRoute %d", r.Route)
	}

	// Every wiring closure — Root, WriteTranscript, New — is called HERE, on
	// the sub-test's real *testing.T, rather than inside an arm. That is the
	// split AssertHookEndpointFollowsBindAddr already draws, and it is what
	// lets the arms report through the seam: relocating a root, writing a
	// transcript, planting a symlink and building a receiver are fixture
	// machinery that reports its own failures, and a fixture that cannot be
	// BUILT must fail the run loudly rather than be recorded as the obligation
	// under test firing (#1479). The arms below take resolved values and do
	// nothing but grade them, so a negative self-test can drive one against a
	// deliberately wrong receiver and read back what it said (#1497).
	t.Run("in_tree_path_accepted", func(t *testing.T) {
		root := r.Root(t)
		inTree := r.WriteTranscript(t, mkSubdir(t, root, "in-tree"))
		rut := r.New(t)
		assertInTreeAccepted(realT(t), r, rut, inTree)
	})
	t.Run("out_of_tree_path_rejected", func(t *testing.T) {
		r.Root(t)
		outside := r.WriteTranscript(t, t.TempDir())
		assertRefused(realT(t), r, r.New(t), outside, whatOutOfTree)
	})
	t.Run("parent_traversal_rejected", func(t *testing.T) {
		root := r.Root(t)
		outside := r.WriteTranscript(t, t.TempDir())
		// Concatenated, not filepath.Join'd: Join cleans, and an already-cleaned
		// path is not the input under test. Enough ".." to bottom out at "/",
		// where further ones are absorbed.
		traversal := root + strings.Repeat("/..", 32) + outside
		assertRefused(realT(t), r, r.New(t), traversal, whatTraversal)
	})
	t.Run("symlink_escape_rejected", func(t *testing.T) {
		root := r.Root(t)
		outside := r.WriteTranscript(t, t.TempDir())
		link := filepath.Join(mkSubdir(t, root, "linked"), filepath.Base(outside))
		if err := os.Symlink(outside, link); err != nil {
			t.Fatalf(symlinkFailure, link, outside, err)
		}
		assertRefused(realT(t), r, r.New(t), link, whatSymlinkEscape)
	})
	t.Run("dangling_symlink_rejected", func(t *testing.T) {
		root := r.Root(t)
		// A path in a directory the receiver has no claim on. Deliberately NOT
		// created: the whole point is that it does not exist at confinement time.
		target := filepath.Join(t.TempDir(), "planted"+r.TranscriptExt)
		link := filepath.Join(mkSubdir(t, root, "dangling"), filepath.Base(target))
		if err := os.Symlink(target, link); err != nil {
			t.Fatalf(symlinkFailure, link, target, err)
		}
		assertRefused(realT(t), r, r.New(t), link, whatDangling)
	})
	t.Run("confined_spelling_is_what_dispatches", func(t *testing.T) {
		root := r.Root(t)
		inTree := r.WriteTranscript(t, mkSubdir(t, root, "spelling"))
		assertConfinedSpellingDispatches(realT(t), r, r.New(t), noisySpellingOf(inTree))
	})
}

// The four refusal inputs, named once. Obligations 2-5 share ONE arm
// (assertRefused) and differ only in the path they post, so `what` is the only
// thing that tells their failures apart — which makes it the fragment a
// negative self-test has to match on. Naming them here rather than at the call
// site is #1498's own lesson applied: that PR graded an arm on an interpolated
// value a NEIGHBOURING obligation's message also contained, so an arm reporting
// the wrong prose for the right condition was graded green. A fragment retyped
// in a self-test is the same defect with an extra step.
const (
	whatOutOfTree     = "a well-formed transcript outside every declared root"
	whatTraversal     = "a path rooted in the declared tree that climbs out of it"
	whatSymlinkEscape = "a symlink inside the declared root pointing out of it (symlinks must be resolved BEFORE the containment check)"
	whatDangling      = "a dangling symlink inside the declared root (an unresolvable leaf must not be assumed to be an unflushed write)"
)

// noisySpellingOf returns path with a redundant "./" before its last component
// — the same file, spelled the way a caller would if it wanted to find out
// whether its own string is what travels.
//
// Concatenated rather than filepath.Join'd because Join cleans, and a
// pre-cleaned path cannot tell "the confined string was used" from "the
// caller's was echoed".
func noisySpellingOf(path string) string {
	dir, base := filepath.Split(path)
	return dir + "./" + base
}

// assertConfinedSpellingDispatches is obligation 6: for a path that IS accepted,
// the string handed downstream is the confined one, not the caller's.
//
// Obligations 1-5 are all about the accept/refuse decision, and every one of
// them is satisfied by a receiver that decides correctly and then forwards the
// caller's own string. That receiver is not hypothetical: confinement produces
// a NEW string, so using it is a separate act from computing it, and the
// #1389 chokepoint expresses that act as a caller-supplied write-back — a
// no-op version of which passed this entire contract.
//
// It matters beyond tidiness for the same reason claudecode's statusline
// receiver confines at all: the transcript path is the tailer's map key, so two
// spellings of one file are two sessions. And where the accepted path reached
// the root through a symlink, the caller's spelling names the link while the
// confined one names the target.
func assertConfinedSpellingDispatches(t reporter, r HookReceiver, rut HookReceiverUnderTest, noisy string) {
	t.Helper()
	rec := postHookPath(t, r, rut, noisy)
	if rec.Code != http.StatusOK {
		t.Fatalf("in-tree transcript spelled %s: status = %d, want 200 — a redundant \"./\" is still inside the root",
			noisy, rec.Code)
	}
	got := rut.ObservedPath()
	if got == "" {
		t.Fatal("nothing was dispatched, so this obligation would pass vacuously")
	}
	// Asserted as "cleaned", not as equal to the fixture path: the confiner
	// rebuilds an accepted path on the adapter's DECLARED root, which on macOS
	// can be the /var spelling of the /private/var directory the test created.
	// Comparing strings would fail there for a reason that has nothing to do
	// with the obligation. Whether the noisy segment survived is the actual
	// question.
	if got == noisy || strings.Contains(got, string(filepath.Separator)+"."+string(filepath.Separator)) {
		t.Errorf("dispatched %q, which is the caller's own spelling — the receiver confined the path and then "+
			"forwarded the unconfined string anyway. Confinement must not only decide; its result must be what travels",
			got)
	}
}

// assertInTreeAccepted is obligation 1. It runs first because every assertion
// below it is a negative one, and a receiver that has stopped working at all
// satisfies negative assertions perfectly.
func assertInTreeAccepted(t reporter, r HookReceiver, rut HookReceiverUnderTest, inTree string) {
	t.Helper()
	rec := postHookPath(t, r, rut, inTree)
	if rec.Code != http.StatusOK {
		t.Fatalf("in-tree transcript %s: status = %d, want 200 — confinement is refusing the adapter's own tree", inTree, rec.Code)
	}
	if !rut.Observed() {
		t.Fatal("in-tree transcript was not dispatched — the rejection assertions below would pass vacuously")
	}
	if n := rut.Handler.Confiner.RejectionCount(); n != 0 {
		t.Errorf("in-tree transcript counted %d rejection(s), want 0", n)
	}
}

// --- helpers ---

// assertRefused is obligations 2-5. It drives one escape attempt against a
// fresh receiver and checks all three properties a refusal owes: nothing
// dispatched, a loud-in-the-daemon-but-quiet-on-the-wire status, and a counted
// reason. Which obligation is speaking is carried entirely by `what`.
func assertRefused(t reporter, r HookReceiver, rut HookReceiverUnderTest, path, what string) {
	t.Helper()
	rec := postHookPath(t, r, rut, path)
	if rut.Observed() {
		t.Errorf("%s was dispatched downstream: %s", what, path)
	}
	assertHookStatus2xx(t, rec, what)
	if n := rut.Handler.Confiner.RejectionCount(); n != 1 {
		t.Errorf("%s: counted %d rejection(s), want 1 — a refusal has to be countable, not just returned", what, n)
	}
}

// postHookPath POSTs the adapter's hook body for path and returns the response.
func postHookPath(t reporter, r HookReceiver, rut HookReceiverUnderTest, path string) *httptest.ResponseRecorder {
	t.Helper()
	return postHookBody(t, rut.Handler, r.EndpointPath, r.PayloadFor(path))
}

// --- PathDaemonDerived route ---

// plantedTranscriptName is the leaf every hostile spelling below points at. A
// constant because five call sites name it and a sixth that misspelled it would
// still plant a file, just not the one the case describes.
const plantedTranscriptName = "planted.jsonl"

// symlinkFailure is the fixture-failure message this file reports when a
// symlink cannot be created. Named once so the three planting sites cannot
// drift into three different spellings of the same environmental failure.
const symlinkFailure = "symlink %s -> %s: %v"

// hostilePathSpellings are the four bodies obligation 3 posts at a
// PathDaemonDerived receiver. They are the SAME four escapes obligations 2-5
// grade a from-body receiver on, which is the point: on this route each one
// must be a no-op rather than a refusal, and running them proves the receiver
// is indifferent to the exact inputs the other route has to defend against.
var hostilePathSpellings = []struct {
	name string
	// path builds the caller-supplied path, given the relocated root and a
	// scratch directory outside it.
	path func(t *testing.T, root, outside string) string
}{
	{"out_of_tree", func(_ *testing.T, _, outside string) string {
		return filepath.Join(outside, plantedTranscriptName)
	}},
	{"parent_traversal", func(_ *testing.T, root, outside string) string {
		return root + strings.Repeat("/..", 32) + filepath.Join(outside, plantedTranscriptName)
	}},
	{"symlink_escape", func(t *testing.T, root, outside string) string {
		target := filepath.Join(outside, plantedTranscriptName)
		if err := os.WriteFile(target, []byte("{}\n"), 0o600); err != nil {
			t.Fatalf("write %s: %v", target, err)
		}
		link := filepath.Join(mkSubdir(t, root, "linked"), plantedTranscriptName)
		if err := os.Symlink(target, link); err != nil {
			t.Fatalf(symlinkFailure, link, target, err)
		}
		return link
	}},
	{"dangling_symlink", func(t *testing.T, root, outside string) string {
		target := filepath.Join(outside, "never-created.jsonl")
		link := filepath.Join(mkSubdir(t, root, "dangling"), plantedTranscriptName)
		if err := os.Symlink(target, link); err != nil {
			t.Fatalf(symlinkFailure, link, target, err)
		}
		return link
	}},
}

// hostileBodyFor renders the body one hostile spelling posts, and refuses the
// run when the wiring's ForeignPathPayload did not actually splice the path in.
//
// Fixture machinery, not an obligation, which is why it reports on *testing.T
// and is named in seam_walk_test.go's deferredToTheSeam: a wiring that ignored
// its argument would make the invariance obligation compare the clean dispatch
// to itself, and "this obligation could not run" must be loud rather than
// recorded as the obligation firing (#1479).
func hostileBodyFor(t *testing.T, r HookReceiver, hostile string) string {
	t.Helper()
	body := r.ForeignPathPayload(hostile)
	if body == r.PayloadFor("") {
		t.Fatalf("ForeignPathPayload(%q) rendered the same body as PayloadFor — this obligation cannot run, and would otherwise report a pass having posted nothing hostile", hostile)
	}
	if !strings.Contains(body, jsonEscapedFragment(hostile)) {
		t.Fatalf("ForeignPathPayload(%q) rendered a body that does not contain that path — this obligation cannot run: %s", hostile, body)
	}
	return body
}

// assertDerivedDispatch is the shared grading step of all three obligations: a
// POST of body must be answered 2xx, must dispatch, and must dispatch exactly
// want.
func assertDerivedDispatch(t reporter, r HookReceiver, rut HookReceiverUnderTest, body, want, what string) {
	t.Helper()
	rec := postHookBody(t, rut.Handler, r.EndpointPath, body)
	assertHookStatus2xx(t, rec, what)
	if !rut.Observed() {
		t.Fatalf("%s dispatched nothing — on the PathDaemonDerived route every body the receiver understands must dispatch the daemon's own path, so this is either a broken receiver or a wiring that declared the wrong route", what)
	}
	if got := rut.ObservedPath(); got != want {
		t.Errorf("%s dispatched %q, want the daemon's own composed path %q — on this route nothing a caller writes may reach the dispatch, and a path that MOVED with the body is a caller-supplied path travelling under a different name", what, got, want)
	}
}

// assertNoConfinerHeld is the structural half of obligation 2. A
// PathDaemonDerived receiver reaches its payload through
// hookjson.DecodeSealed, which takes no confiner; a nil Confiner is what makes
// DecodeConfined fail closed for it, so a later edit that switched the receiver
// back to the confining decode without giving it roots drops payloads instead
// of dispatching unconfined ones.
func assertNoConfinerHeld(t reporter, rut HookReceiverUnderTest) {
	t.Helper()
	if rut.Handler.Confiner != nil {
		t.Errorf("the receiver publishes a PathConfiner (%d rejection(s) so far) while declaring PathDaemonDerived — a receiver that holds a confiner has something to confine, which is the PathFromBody route",
			rut.Handler.Confiner.RejectionCount())
	}
}

// jsonEscapedFragment renders path the way it appears inside a JSON string, so
// the guard above matches a body built with encoding/json (which escapes
// backslashes) as well as one built by hand.
func jsonEscapedFragment(path string) string {
	b, err := json.Marshal(path)
	if err != nil {
		return path
	}
	return strings.Trim(string(b), `"`)
}
