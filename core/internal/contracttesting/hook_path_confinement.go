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
)

// HookReceiverUnderTest is one freshly built hook receiver plus the two things
// the contract must be able to see from outside it: whether a payload was
// dispatched, and what the receiver refused.
type HookReceiverUnderTest struct {
	// Handler is the adapter's hook HTTP handler.
	Handler http.Handler

	// Observed reports whether the receiver dispatched anything downstream —
	// i.e. whether the supplied transcript_path escaped the receiver. This is
	// the assertion that actually matters; the status code is how the refusal
	// is reported, but a 400 alongside a dispatch would still be a breach.
	Observed func() bool

	// Rejections is the receiver's confinement rejection count, so "rejected
	// and counted" is checked rather than assumed. A receiver that returns 400
	// without counting fails here.
	Rejections func() uint64
}

// HookReceiver wires one adapter's hook receiver into AssertHookPathConfined.
// Every field is required.
type HookReceiver struct {
	// Root relocates the adapter's declared transcript root to a
	// test-controlled directory — by t.Setenv of whatever env var moves it
	// ($HOME, $CODEX_HOME) — creates it, and returns its absolute path. Called
	// FIRST in every sub-test, before New.
	Root func(t *testing.T) string

	// New builds a fresh receiver, after Root has run. Fresh per sub-test so
	// the rejection count starts at zero and Observed cannot carry over.
	New func(t *testing.T) HookReceiverUnderTest

	// NewProduction builds the receiver through the adapter's DEFAULT exported
	// constructor — the one the daemon actually calls — rather than any
	// test-only variant New may use to reach the rejection counter.
	//
	// Without it the contract only ever inspects a handler the test assembled,
	// so an adapter could satisfy every obligation with a correctly wired test
	// handler while its real constructor confines nothing: the #1361 hole,
	// shipped past a green contract. Rejections may be nil here; the
	// obligation this field serves is about reachability, not counting.
	NewProduction func(t *testing.T) HookReceiverUnderTest

	// WriteTranscript writes a transcript the adapter would dispatch on into
	// dir, and returns its absolute path. The contract uses it for the in-tree
	// fixture AND for the out-of-tree decoy, so a refusal can never be confused
	// with "the file was unreadable" — the decoy is exactly as well-formed as
	// the fixture, and the only thing standing between it and a dispatch is
	// confinement.
	WriteTranscript func(t *testing.T, dir string) string

	// PayloadFor renders the adapter's hook JSON body carrying transcriptPath,
	// on an event the adapter dispatches.
	PayloadFor func(transcriptPath string) string

	// EndpointPath is the adapter's hook route, used to build the request.
	EndpointPath string
}

// AssertHookPathConfined runs the issue #1361 contract against r. Four
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
//  6. the adapter's PRODUCTION constructor confines too, not merely whatever
//     handler the test assembled.
//
// Obligations 2–5 additionally require the refusal to be loud (a 4xx, never a
// silent 200) and counted.
func AssertHookPathConfined(t *testing.T, r HookReceiver) {
	t.Helper()
	t.Run("in_tree_path_accepted", func(t *testing.T) { assertInTreeAccepted(t, r) })
	t.Run("out_of_tree_path_rejected", func(t *testing.T) { assertOutOfTreeRejected(t, r) })
	t.Run("parent_traversal_rejected", func(t *testing.T) { assertTraversalRejected(t, r) })
	t.Run("symlink_escape_rejected", func(t *testing.T) { assertSymlinkEscapeRejected(t, r) })
	t.Run("dangling_symlink_rejected", func(t *testing.T) { assertDanglingSymlinkRejected(t, r) })
	t.Run("production_constructor_confines", func(t *testing.T) { assertProductionConfines(t, r) })
}

// assertInTreeAccepted is obligation 1. It runs first because every assertion
// below it is a negative one, and a receiver that has stopped working at all
// satisfies negative assertions perfectly.
func assertInTreeAccepted(t *testing.T, r HookReceiver) {
	t.Helper()
	root := r.Root(t)
	inTree := r.WriteTranscript(t, mkSubdir(t, root, "in-tree"))
	rut := r.New(t)

	rec := postHookPath(t, r, rut, inTree)
	if rec.Code != http.StatusOK {
		t.Fatalf("in-tree transcript %s: status = %d, want 200 — confinement is refusing the adapter's own tree", inTree, rec.Code)
	}
	if !rut.Observed() {
		t.Fatal("in-tree transcript was not dispatched — the rejection assertions below would pass vacuously")
	}
	if n := rut.Rejections(); n != 0 {
		t.Errorf("in-tree transcript counted %d rejection(s), want 0", n)
	}
}

// assertOutOfTreeRejected is obligation 2: a plain absolute path elsewhere.
func assertOutOfTreeRejected(t *testing.T, r HookReceiver) {
	t.Helper()
	r.Root(t)
	outside := r.WriteTranscript(t, t.TempDir())
	assertRefused(t, r, outside, "a well-formed transcript outside every declared root")
}

// assertTraversalRejected is obligation 3: lexically inside, really outside.
func assertTraversalRejected(t *testing.T, r HookReceiver) {
	t.Helper()
	root := r.Root(t)
	outside := r.WriteTranscript(t, t.TempDir())
	// Concatenated, not filepath.Join'd: Join cleans, and an already-cleaned
	// path is not the input under test. Enough ".." to bottom out at "/",
	// where further ones are absorbed.
	traversal := root + strings.Repeat("/..", 32) + outside
	assertRefused(t, r, traversal, "a path rooted in the declared tree that climbs out of it")
}

// assertSymlinkEscapeRejected is obligation 4 — the ordering obligation. The
// submitted path is lexically inside the declared root and passes any
// containment check applied to the raw string; only resolving it first reveals
// that it leaves the tree.
func assertSymlinkEscapeRejected(t *testing.T, r HookReceiver) {
	t.Helper()
	root := r.Root(t)
	outside := r.WriteTranscript(t, t.TempDir())
	link := filepath.Join(mkSubdir(t, root, "linked"), filepath.Base(outside))
	if err := os.Symlink(outside, link); err != nil {
		t.Fatalf("symlink %s -> %s: %v", link, outside, err)
	}
	assertRefused(t, r, link, "a symlink inside the declared root pointing out of it (symlinks must be resolved BEFORE the containment check)")
}

// assertDanglingSymlinkRejected is obligation 5. The link is inside the root
// and its target does not exist yet, so a guard that treats "cannot resolve" as
// "not written yet" accepts it — and the attacker then creates the target.
func assertDanglingSymlinkRejected(t *testing.T, r HookReceiver) {
	t.Helper()
	root := r.Root(t)
	// A path in a directory the receiver has no claim on. Deliberately NOT
	// created: the whole point is that it does not exist at confinement time.
	target := filepath.Join(t.TempDir(), "planted"+filepath.Ext(r.WriteTranscript(t, t.TempDir())))
	link := filepath.Join(mkSubdir(t, root, "dangling"), filepath.Base(target))
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("symlink %s -> %s: %v", link, target, err)
	}
	assertRefused(t, r, link, "a dangling symlink inside the declared root (an unresolvable leaf must not be assumed to be an unflushed write)")
}

// assertProductionConfines is obligation 6: the confinement is reachable
// through the constructor the daemon calls, not only the one the test wires.
func assertProductionConfines(t *testing.T, r HookReceiver) {
	t.Helper()
	r.Root(t)
	outside := r.WriteTranscript(t, t.TempDir())
	rut := r.NewProduction(t)

	rec := postHookPath(t, r, rut, outside)
	if rut.Observed() {
		t.Errorf("the adapter's production constructor dispatched an out-of-tree transcript: %s", outside)
	}
	if rec.Code < 400 || rec.Code > 499 {
		t.Errorf("production constructor: status = %d, want a 4xx — confinement is not wired into the handler the daemon builds", rec.Code)
	}
}

// --- helpers ---

// assertRefused drives one escape attempt against a fresh receiver and checks
// all three properties a refusal owes: nothing dispatched, a loud status, and a
// counted reason.
func assertRefused(t *testing.T, r HookReceiver, path, what string) {
	t.Helper()
	rut := r.New(t)

	rec := postHookPath(t, r, rut, path)
	if rut.Observed() {
		t.Errorf("%s was dispatched downstream: %s", what, path)
	}
	if rec.Code < 400 || rec.Code > 499 {
		t.Errorf("%s: status = %d, want a 4xx — a refused path must not be answered with a success", what, rec.Code)
	}
	if n := rut.Rejections(); n != 1 {
		t.Errorf("%s: counted %d rejection(s), want 1 — a refusal has to be countable, not just returned", what, n)
	}
}

// postHookPath POSTs the adapter's hook body for path and returns the response.
func postHookPath(t *testing.T, r HookReceiver, rut HookReceiverUnderTest, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, r.EndpointPath, strings.NewReader(r.PayloadFor(path)))
	rec := httptest.NewRecorder()
	rut.Handler.ServeHTTP(rec, req)
	return rec
}

// mkSubdir creates and returns root/name. Transcripts sit some levels below the
// root in every real layout, so the fixtures do too.
func mkSubdir(t *testing.T, root, name string) string {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("create %s: %v", dir, err)
	}
	return dir
}
