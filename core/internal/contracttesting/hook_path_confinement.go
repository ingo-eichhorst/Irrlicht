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

// HookReceiver wires one adapter's hook receiver into AssertHookPathConfined.
// Every field is required.
type HookReceiver struct {
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
			t.Fatalf("symlink %s -> %s: %v", link, outside, err)
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
			t.Fatalf("symlink %s -> %s: %v", link, target, err)
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
