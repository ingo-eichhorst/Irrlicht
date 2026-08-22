// hook_path_confinement_selftest_test.go is the committed mutation evidence for
// AssertHookPathConfined (#1361, #1389, #1390), turning what #1380 and #1446
// recorded in merged PR bodies into a lock that runs on every build (#1479,
// #1497).
//
// The mutations are transcribed, not invented. #1446 carries an 8x5 matrix — the
// best-documented in the repo — and its verdict pattern is what each case below
// reproduces:
//
//	M1 confiner rejects everything            RED on 1 only
//	M2 confiner accepts everything            RED on 2,3,4,5
//	M3 the ".." guard dropped                 RED on 2,3,4
//	M4 purely lexical containment             RED on 4,5 only   <- the ordering obligation
//	M6 the dangling-link guard disabled       RED on 5 only
//	   a no-op `set` closure (#1465)          RED on 6 only
//
// # What each case can and cannot claim
//
// Read receiver_fixture_test.go's header first: obligations 1 and 2 are driven
// by a receiver that is wrong in a way an ADAPTER can be wrong, running the
// whole production path. Obligations 3, 4 and 5 grade a verdict
// hookjson.PathConfiner reaches, so their recorded mutations edited confine.go
// and this file cannot; the fixture reproduces each recorded verdict pattern by
// second-guessing the confiner instead. The claim is therefore about the ARM —
// it reports when the receiver reaches the wrong verdict on that input — and
// not about confine.go, which has its own tests.
//
// # The isolation cases are half the evidence
//
// Every mutation here also names the obligations it must leave SILENT. Without
// that, six mutations against six obligations would be satisfied by six arms
// that all report on everything — which is #1453's failure wearing a bigger
// hat. The silence cases are what make "the obligation that owns this wrongness
// is the one that reported it" a checked claim rather than a hopeful one, and
// they are exactly where the recorded matrix's per-row PASS cells live.
package contracttesting

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// confinementArmBuilders mirrors AssertHookPathConfined's own t.Run bodies, in
// its order and with its inputs.
//
// It restates that entry point rather than sharing it, which is the same trade
// every <family>_selftest_test.go in this package makes. The vacuity guard is
// what keeps the restatement honest: a construction that drifted into something
// an arm legitimately rejects turns TestConfinementArms_PassACorrectReceiver
// red, not green.
func confinementArmBuilders() []armBuilder {
	return []armBuilder{
		{"in_tree_path_accepted", func(t *testing.T, brk receiverBreak) func(armT) {
			r := fakePathReceiver(brk)
			inTree := r.WriteTranscript(t, mkSubdir(t, r.Root(t), "in-tree"))
			rut := r.New(t)
			return func(at armT) { assertInTreeAccepted(at, r, rut, inTree) }
		}},
		{"out_of_tree_path_rejected", func(t *testing.T, brk receiverBreak) func(armT) {
			r := fakePathReceiver(brk)
			r.Root(t)
			outside := r.WriteTranscript(t, t.TempDir())
			rut := r.New(t)
			return func(at armT) { assertRefused(at, r, rut, outside, whatOutOfTree) }
		}},
		{"parent_traversal_rejected", func(t *testing.T, brk receiverBreak) func(armT) {
			r := fakePathReceiver(brk)
			root := r.Root(t)
			outside := r.WriteTranscript(t, t.TempDir())
			traversal := root + strings.Repeat("/..", 32) + outside
			rut := r.New(t)
			return func(at armT) { assertRefused(at, r, rut, traversal, whatTraversal) }
		}},
		{"symlink_escape_rejected", func(t *testing.T, brk receiverBreak) func(armT) {
			r := fakePathReceiver(brk)
			root := r.Root(t)
			outside := r.WriteTranscript(t, t.TempDir())
			link := filepath.Join(mkSubdir(t, root, "linked"), filepath.Base(outside))
			if err := os.Symlink(outside, link); err != nil {
				t.Fatalf(symlinkFailure, link, outside, err)
			}
			rut := r.New(t)
			return func(at armT) { assertRefused(at, r, rut, link, whatSymlinkEscape) }
		}},
		{"dangling_symlink_rejected", func(t *testing.T, brk receiverBreak) func(armT) {
			r := fakePathReceiver(brk)
			root := r.Root(t)
			// Deliberately NOT created: the whole point is that it does not
			// exist at confinement time.
			target := filepath.Join(t.TempDir(), "planted"+r.TranscriptExt)
			link := filepath.Join(mkSubdir(t, root, "dangling"), filepath.Base(target))
			if err := os.Symlink(target, link); err != nil {
				t.Fatalf(symlinkFailure, link, target, err)
			}
			rut := r.New(t)
			return func(at armT) { assertRefused(at, r, rut, link, whatDangling) }
		}},
		{"confined_spelling_is_what_dispatches", func(t *testing.T, brk receiverBreak) func(armT) {
			r := fakePathReceiver(brk)
			spelled := r.WriteTranscript(t, mkSubdir(t, r.Root(t), "spelling"))
			rut := r.New(t)
			noisy := noisySpellingOf(spelled)
			return func(at armT) { assertConfinedSpellingDispatches(at, r, rut, noisy) }
		}},
	}
}

// confinementFamily is the obligation table this file's cases drive through
// receiverFamily's shared drive/silence pair.
var confinementFamily = receiverFamily{
	entryPoint: "AssertHookPathConfined",
	builders:   confinementArmBuilders,
}

// TestConfinementArms_PassACorrectReceiver is the family's vacuity guard.
// Without it, an arm that reported unconditionally would satisfy every mutation
// below and read as excellent coverage.
//
// The receiver is built the production way — hookjson.NewHandler over a real
// PathConfiner, decoding through hookjson.DecodeConfined — so a silence here is
// a silence against production code rather than against a stand-in.
func TestConfinementArms_PassACorrectReceiver(t *testing.T) {
	confinementFamily.mustPassACorrectReceiver(t, receiverBreak{}, "a correct hook receiver")
}

// TestConfinementArms_PassAHandRolledButFaithfulReceiver is the baseline the
// three verdict-override cases below are measured against.
//
// Those three cannot decode through DecodeConfined, because DecodeConfined
// offers no way to reach a wrong verdict. Without this test each of them would
// differ from a correct receiver in TWO ways — the verdict and the decode — and
// could not say which one the arm reported. With it, the decode is proved
// neutral and the verdict is the only variable left.
func TestConfinementArms_PassAHandRolledButFaithfulReceiver(t *testing.T) {
	confinementFamily.mustPassACorrectReceiver(t, receiverBreak{handRolledDecode: true},
		"a receiver that hand-rolls the decode but keeps the confiner's verdict")
}

// TestConfinementObligation1_InTreeAccepted is #1446's M1: a confiner that
// refuses everything. Here it arrives the way an adapter would actually reach
// it — by declaring no transcript root at all, which ConfinerForSource's doc
// comment warns about by name ("refuses everything with RejectNoRoots rather
// than waving paths through").
//
// It is the vacuity obligation, and the matrix says so: M1 is RED on 1 and
// green on every other row, because a receiver that refuses everything satisfies
// four negative assertions perfectly.
func TestConfinementObligation1_InTreeAccepted(t *testing.T) {
	brk := receiverBreak{roots: func() []string { return nil }}

	rec := confinementFamily.drive(t, brk, "in_tree_path_accepted")
	mustReport(t, rec, "in-tree transcript was not dispatched",
		"a receiver that declares no transcript root, so confinement refuses its own tree")

	confinementFamily.mustBeSilentOn(t, brk, "a receiver that refuses everything",
		"out_of_tree_path_rejected", "parent_traversal_rejected",
		"symlink_escape_rejected", "dangling_symlink_rejected")
}

// TestConfinementObligation2_OutOfTreeRejected is #1446's M2 — a confiner that
// accepts everything — reached the way an adapter reaches it: by declaring a
// root so broad it swallows the decoy. The whole production path runs,
// DecodeConfined included; only the declaration is wrong.
func TestConfinementObligation2_OutOfTreeRejected(t *testing.T) {
	brk := receiverBreak{roots: func() []string { return []string{os.TempDir()} }}

	rec := confinementFamily.drive(t, brk, "out_of_tree_path_rejected")
	mustReport(t, rec, whatOutOfTree+" was dispatched downstream",
		"a receiver whose declared root is broad enough to contain the out-of-tree decoy")

	// Obligation 1 stays silent, which is what separates this from M1: an
	// over-broad root still accepts the adapter's own tree.
	confinementFamily.mustBeSilentOn(t, brk, "a receiver with an over-broad declared root", "in_tree_path_accepted")
}

// TestConfinementObligation3_ParentTraversalRejected is #1446's M3, the ".."
// guard dropped: containment by lexical prefix on the CALLER'S string.
//
// The matrix records M3 as RED on 2, 3 and 4 — so the isolation that matters
// here is against obligation 2, which stays green because a path in another
// temp directory does not start with the declared root either way. That is what
// makes the traversal obligation a separate obligation rather than a second
// spelling of the out-of-tree one.
func TestConfinementObligation3_ParentTraversalRejected(t *testing.T) {
	brk := receiverBreak{confine: confineRawPrefix}

	rec := confinementFamily.drive(t, brk, "parent_traversal_rejected")
	mustReport(t, rec, whatTraversal+" was dispatched downstream",
		"a receiver that checks containment by lexical prefix on the caller's uncleaned string")

	confinementFamily.mustBeSilentOn(t, brk, "a receiver whose containment check is a raw lexical prefix",
		"in_tree_path_accepted", "out_of_tree_path_rejected")
}

// TestConfinementObligation4_SymlinkEscapeRejected is #1446's M4 and #1361's
// headline warning: containment checked BEFORE symlinks are resolved. A guard
// in that order passes the in-tree, out-of-tree AND traversal obligations and
// confines nothing.
//
// The matrix records M4 as RED on 4 and 5 only. Obligation 3 staying green is
// the decisive cell — it is what proves the ordering obligation is not covered
// by the traversal one, which was #1361's entire point.
func TestConfinementObligation4_SymlinkEscapeRejected(t *testing.T) {
	brk := receiverBreak{confine: confineCleanedPrefix}

	rec := confinementFamily.drive(t, brk, "symlink_escape_rejected")
	mustReport(t, rec, whatSymlinkEscape+" was dispatched downstream",
		"a receiver that cleans the path and checks containment without ever resolving symlinks")

	// The matrix records M4 as red on 4 AND 5 — #1380 called that "strictly
	// stronger" than its first result — so obligation 5 is ASSERTED here rather
	// than described. A matrix cell claimed in prose and checked by nothing is
	// the shape this file exists to replace (review of PR #1512).
	t.Run("also_red_on_dangling_symlink_rejected", func(t *testing.T) {
		mustReport(t, confinementFamily.drive(t, brk, "dangling_symlink_rejected"),
			whatDangling+" was dispatched downstream",
			"a receiver that checks containment before resolution — a dangling link inside the "+
				"root survives purely lexical containment too")
	})

	confinementFamily.mustBeSilentOn(t, brk, "a receiver that checks containment before resolution",
		"in_tree_path_accepted", "out_of_tree_path_rejected", "parent_traversal_rejected")
}

// TestConfinementObligation5_DanglingSymlinkRejected is #1446's M6: the
// dangling-link guard disabled, red on obligation 5 and nothing else.
//
// The receiver here is correct in every other respect — it resolves before it
// contains, and it refuses a resolvable escape — and wrong only in treating
// "cannot resolve" as "not written yet". That allowance is reasonable-looking,
// which is why it needs an obligation: resolution reports the same error for a
// broken link as for a file that has not landed, so an attacker plants the
// broken link, has the path accepted, and then creates the target.
func TestConfinementObligation5_DanglingSymlinkRejected(t *testing.T) {
	brk := receiverBreak{confine: confineAcceptUnresolvable}

	rec := confinementFamily.drive(t, brk, "dangling_symlink_rejected")
	mustReport(t, rec, whatDangling+" was dispatched downstream",
		"a receiver that waves through a leaf it cannot resolve, on the assumption the write has not landed yet")

	confinementFamily.mustBeSilentOn(t, brk, "a receiver that accepts an unresolvable leaf",
		"in_tree_path_accepted", "out_of_tree_path_rejected",
		"parent_traversal_rejected", "symlink_escape_rejected")
}

// TestConfinementObligation6_ConfinedSpellingDispatches is #1465's no-op `set`:
// a receiver that decides correctly and then forwards the caller's own string.
//
// The silences are the entire reason this obligation exists. #1465 found the
// whole contract green against exactly this receiver, because obligations 1-5
// ask WHETHER a receiver dispatched and never WHICH STRING — measured by
// replacing a receiver's write-back with a no-op and watching the suite stay
// green. Re-running that measurement here is what stops the fifth obligation
// from quietly growing to cover the sixth's ground and leaving nobody to notice
// when it stops.
func TestConfinementObligation6_ConfinedSpellingDispatches(t *testing.T) {
	brk := receiverBreak{forwardCallersSpelling: true}

	rec := confinementFamily.drive(t, brk, "confined_spelling_is_what_dispatches")
	mustReport(t, rec, "which is the caller's own spelling",
		"a receiver that confines correctly and then dispatches the caller's unconfined string")

	confinementFamily.mustBeSilentOn(t, brk, "a receiver that forwards the caller's spelling",
		"in_tree_path_accepted", "out_of_tree_path_rejected", "parent_traversal_rejected",
		"symlink_escape_rejected", "dangling_symlink_rejected")
}

// refusalObligations are the four inputs that share assertRefused, paired with
// the `what` each one prints. Named once and ranged over by both cases below,
// so a fifth refusal obligation cannot be added to one table and forgotten in
// the other.
var refusalObligations = []struct{ name, what string }{
	{"out_of_tree_path_rejected", whatOutOfTree},
	{"parent_traversal_rejected", whatTraversal},
	{"symlink_escape_rejected", whatSymlinkEscape},
	{"dangling_symlink_rejected", whatDangling},
}

// TestConfinementObligations2to5_RefusalIsCounted covers the half of those four
// obligations the escape mutations above leave untouched: a receiver can reach
// the right VERDICT and still leave no evidence it did.
//
// The mutation is #1446's M7a — the handler publishes one PathConfiner and its
// request path uses another — which the matrix records as red on obligations
// 2-5, and which has no analogue in #1380 because the pre-#1390 shape could not
// express it. RejectPath logs and answers 2xx but counts NOTHING; only
// Confine counts. So a receiver that confines through an instance the handler
// does not publish refuses correctly, silently, and forever.
//
// Obligation 1 stays silent: the in-tree path is accepted by both instances,
// and its assertion is that the count is ZERO — which a counter nobody
// increments satisfies perfectly. That is the cell worth reading twice.
func TestConfinementObligations2to5_RefusalIsCounted(t *testing.T) {
	brk := receiverBreak{privateConfiner: true}

	for _, ob := range refusalObligations {
		t.Run(ob.name, func(t *testing.T) {
			mustReport(t, confinementFamily.drive(t, brk, ob.name),
				ob.what+": counted 0 rejection(s), want 1",
				"a receiver whose published confiner is not the one its request path uses")
		})
	}

	confinementFamily.mustBeSilentOn(t, brk, "a receiver confining through a second, unpublished instance",
		"in_tree_path_accepted")
}

// TestConfinementObligations2to5_RefusalAnswers2xx is the other shared half: a
// refusal is reported by the log and the counter, never by a status code on the
// user's critical path.
//
// It is asserted rather than tolerated because the status buys no security —
// the path is already contained by not being forwarded — while a non-2xx is an
// untested interaction with the agent CLI. Claude Code documents that a non-2xx
// from a `type: http` hook "can't block actions" but not whether it surfaces an
// error, and gemini-cli's pre-tool hooks and Copilot's preToolUse are known to
// fail CLOSED on an error result (#1361, #1364). assertHookStatus2xx is the one
// place that rule lives, so this is also the negative test for the two other
// receiving-side families that depend on it.
func TestConfinementObligations2to5_RefusalAnswers2xx(t *testing.T) {
	brk := receiverBreak{refusesPathWith4xx: true}

	for _, ob := range refusalObligations {
		t.Run(ob.name, func(t *testing.T) {
			mustReport(t, confinementFamily.drive(t, brk, ob.name),
				ob.what+": status = 400, want 2xx",
				"a receiver that reports a confinement refusal through the status code")
		})
	}

	confinementFamily.mustBeSilentOn(t, brk, "a receiver that answers 4xx on a refusal", "in_tree_path_accepted")
}
