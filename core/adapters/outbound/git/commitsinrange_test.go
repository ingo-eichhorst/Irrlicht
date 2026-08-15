package git

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"irrlicht/core/domain/dora"
)

// This file covers #1564: CommitsInRange stopped asking git for a body it will
// not read, which took its output from ~1.9 KB per commit to ~80 bytes and
// moved the gitMaxOutput wall from ~36k commits in one range to ~835k.
//
// The risk that change carries is NOT a crash. It is a silently different
// RESULT SET — a range walk that drops or duplicates a commit corrupts
// LeadTime's median exactly the way `--since` would have, and #1553 rejected
// that option on those grounds. So the load-bearing test here is an ORACLE:
// the single `--pretty=%B` call this replaced, run directly against the same
// fixture, with the adapter required to agree with it commit for commit and
// verdict for verdict.
//
// MUTATION EVIDENCE is committed rather than only described. The three
// comparisons — diffCommitSets, diffRevertVerdicts, diffBodyContract — RETURN
// findings instead of calling t.Errorf, and
// TestTheComparisonsCatchEveryWayTheSplitCanComeUpShort runs all three against
// a result broken in exactly ONE way, requiring the right one to name what
// broke and the others to stay SILENT. Those silences are half the evidence:
// without them, seven mutations against three checks are equally satisfied by
// three checks that report on everything. A comparison that quietly stopped
// discriminating is indistinguishable from health, and a paragraph in a merged
// PR body is re-run by nothing.
//
// The mutations applied to the PRODUCTION code (which a test in this package
// cannot make) are in the PR body, and each is reproduced here as a row:
// `bodiesNeverFetched` is the production `fillBodies` deleted,
// `bodiesFetchedByTrailer` is its filter changed from the subject to the
// trailer, and `subsetOfCommits` is the range walk returning fewer commits than
// it read.
//
// One row earns its keep in a way the others do not, and it is why there are
// three comparisons rather than two: `bodiesFetchedByTrailer` leaves both the
// commit set and the DetectReverts verdict UNCHANGED. Only the body contract
// sees it.

// singleCallOracle is the command CommitsInRange made until #1564, verbatim.
// It is the yardstick every set assertion below measures against: a set that
// agrees with it is by definition the set that shipped before the split.
func singleCallOracle(t *testing.T, dir, rangeSpec string) []dora.CommitInfo {
	t.Helper()
	cmd := exec.Command("git", "log", commitBodyFormat, rangeSpec)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("oracle `git log %s`: %v", rangeSpec, err)
	}
	return parseCommitRecords(out)
}

// awkwardCommit is one fixture commit: a name, so the corpus reads as a list of
// SHAPES rather than of strings, and the message written. No row carries an
// expected verdict — the oracle decides those, which is the whole point of
// having one.
type awkwardCommit struct {
	name string
	msg  string
}

// awkwardCommits is the fixture's shape, and every row is here because it can
// tell a correct split from a broken one. The plain commits are the bulk a
// dropped-commit bug hides in; the revert rows are what a subject filter can
// miss; and the last few are the places `%s` and the first line of `%B`
// legitimately DIFFER, which is where a superset argument either holds or does
// not.
var awkwardCommits = []awkwardCommit{
	{"plain", "feat: add a thing\n\nwith a body paragraph."},
	{"plain-no-body", "chore: tidy"},
	{"revert-standard", "Revert \"feat: add a thing\"\n\nThis reverts commit 1111111111111111111111111111111111111111.\n"},
	{"revert-lowercase", "revert: undo the thing\n\nThis reverts commit 2222222222222222222222222222222222222222.\n"},
	{"revert-shouty", "REVERT the other thing\n\nThis reverts commit 3333333333333333333333333333333333333333.\n"},
	// Revert-shaped subject, NO trailer. DetectReverts counts this as
	// `unresolved` rather than as a candidate, and it only gets there if the
	// body was fetched — a split that fetched bodies by grepping for the
	// TRAILER instead of the subject would still classify it correctly by
	// accident, so it needs the row below to be caught.
	{"revert-no-trailer", "revert: a non-standard revert\n\nno trailer at all here."},
	// Trailer, NOT a revert subject. DetectReverts must IGNORE it. A split
	// that widened its filter to the trailer would fetch this body for
	// nothing; a split that classified on the trailer would count it wrongly.
	{"trailer-without-revert-subject", "feat: mentions a revert\n\nThis reverts commit 4444444444444444444444444444444444444444.\n"},
	// A body LINE starting with "revert". `git log --grep=^revert` is
	// line-anchored, so the issue's proposed second call would have matched
	// this one; DetectReverts must not.
	{"revert-word-in-body", "feat: something else\n\nrevert was considered and rejected."},
	{"revert-mid-subject", "fix: do not revert this\n\nThis reverts commit 5555555555555555555555555555555555555555.\n"},
	// Multi-line first paragraph: git folds it into ONE line for %s, so %s
	// and the first line of %B differ here.
	{"folded-subject", "Revert \"a subject that\nspans two lines\"\n\nThis reverts commit 6666666666666666666666666666666666666666.\n"},
	{"folded-subject-plain", "feat: a subject that\nspans two lines\n\nbody."},
	// Control bytes are what the record/field separators are, so a commit
	// carrying \x02 in its body proves the parse is not confused by content.
	{"body-with-field-separator", "chore: separators\n\nliteral \x02 in the body."},
	{"unicode", "feat: ünïcödé sübjéct ✓\n\nbody."},
	{"long-body", "feat: long\n\n" + strings.Repeat("filler line for a long body\n", 40)},
}

// buildAwkwardRepo commits every row of awkwardCommits, tags v0.1.0 at the
// first and v0.2.0 at the last, and returns the repo dir.
func buildAwkwardRepo(t *testing.T) string {
	t.Helper()
	dir := gitInitForTest(t)
	for i, c := range awkwardCommits {
		if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte(strconv.Itoa(i)), 0o644); err != nil {
			t.Fatalf("write f.txt: %v", err)
		}
		runGitForTest(t, dir, "add", "f.txt")
		// --cleanup=verbatim so git does not strip the shapes the rows exist
		// to carry.
		runGitForTest(t, dir, "commit", "--cleanup=verbatim", "-m", c.msg)
		if i == 0 {
			runGitForTest(t, dir, "tag", "v0.1.0")
		}
	}
	runGitForTest(t, dir, "tag", "v0.2.0")
	return dir
}

// TestCommitsInRangeReturnsTheSameSetAsTheSingleCallForm is #1564's central
// obligation. Hash and AuthorEpoch must match the pre-split command exactly, in
// order, with no commit dropped and none repeated — and dora.DetectReverts must
// reach the SAME verdict from the adapter's commits as from the oracle's,
// including the `unresolved` count, which is the half a set comparison alone
// cannot see.
func TestCommitsInRangeReturnsTheSameSetAsTheSingleCallForm(t *testing.T) {
	t.Parallel()
	dir := buildAwkwardRepo(t)
	a := New()

	for _, rangeSpec := range []struct{ name, from, to, spec string }{
		{"whole history (the oldest-tag case #1564 is about)", "", "v0.2.0", "v0.2.0"},
		{"between two tags", "v0.1.0", "v0.2.0", "v0.1.0..v0.2.0"},
	} {
		t.Run(rangeSpec.name, func(t *testing.T) {
			want := singleCallOracle(t, dir, rangeSpec.spec)
			got, answered := a.CommitsInRange(noBudget(), dir, rangeSpec.from, rangeSpec.to)
			if !answered {
				t.Fatal("git did not answer")
			}
			// Vacuity guard: a fixture that produced no commits, or only one
			// kind of commit, would satisfy every assertion below without
			// discriminating anything.
			if len(want) < len(awkwardCommits)-1 {
				t.Fatalf("the oracle read %d commits from a fixture of %d; this range is not "+
					"exercising the corpus", len(want), len(awkwardCommits))
			}
			// Vacuity guard on the corpus itself: a fixture with no revert
			// candidates AND no unresolved cannot tell a body-fetch bug from a
			// no-op, because both checks below would be comparing zero to zero.
			cands, unresolved := detectRevertsOver(want)
			if len(cands) == 0 || unresolved == 0 {
				t.Fatalf("the fixture produced %d revert candidate(s) and %d unresolved; both "+
					"must be non-zero", len(cands), unresolved)
			}
			for _, finding := range diffCommitSets(got, want) {
				t.Error(finding)
			}
			for _, finding := range diffRevertVerdicts(got, want) {
				t.Error(finding)
			}
		})
	}
}

// detectRevertsOver runs the domain's detector over one range's commits, under
// a single synthetic tag and a window wide enough to hold everything — the
// shape ComputeDoraMetrics assembles, reduced to the one range under test.
func detectRevertsOver(commits []dora.CommitInfo) ([]dora.RevertCandidate, int) {
	tags := []dora.TagInfo{{Name: "v0.2.0", Epoch: 1}}
	return dora.DetectReverts(tags, map[string][]dora.CommitInfo{"v0.2.0": commits}, 0, 1<<62)
}

// diffCommitSets compares WHICH commits came back and in what order, reporting
// a dropped commit, a repeated one and a wrong author time as different
// findings, because they come from different bugs and only the first is
// visible in a length comparison.
//
// It returns findings rather than calling t.Errorf so that
// TestTheComparisonsCatchEveryWayTheSplitCanComeUpShort can require it to
// SPEAK about a result known to be wrong. A check that silently stopped
// discriminating reads exactly like a passing test.
func diffCommitSets(got, want []dora.CommitInfo) []string {
	var findings []string
	if len(got) != len(want) {
		findings = append(findings, fmt.Sprintf("got %d commits, want %d — a range walk that "+
			"drops or duplicates commits corrupts LeadTime's median while still publishing "+
			"Available:true", len(got), len(want)))
	}
	seen := map[string]int{}
	for _, c := range got {
		seen[c.Hash]++
	}
	for _, c := range got {
		if n := seen[c.Hash]; n > 1 {
			findings = append(findings, fmt.Sprintf("commit %s returned %d times; DORA would "+
				"weight it %dx in the median", short(c.Hash), n, n))
			seen[c.Hash] = 1
		}
	}
	for i := range want {
		if i >= len(got) {
			findings = append(findings, fmt.Sprintf("commit %d (%s) is missing from the "+
				"split's result", i, short(want[i].Hash)))
			continue
		}
		if got[i].Hash != want[i].Hash {
			findings = append(findings, fmt.Sprintf("commit %d: hash %s, want %s (order differs "+
				"or a commit was dropped)", i, short(got[i].Hash), short(want[i].Hash)))
			continue
		}
		if got[i].AuthorEpoch != want[i].AuthorEpoch {
			findings = append(findings, fmt.Sprintf("commit %s: AuthorEpoch %d, want %d — "+
				"LeadTime is the median over exactly this field",
				short(want[i].Hash), got[i].AuthorEpoch, want[i].AuthorEpoch))
		}
	}
	return findings
}

// diffRevertVerdicts is the half a set comparison cannot see: the same commits,
// carrying bodies complete enough (or not) for DetectReverts to reach the same
// verdict. `unresolved` is compared as carefully as the candidates are —
// a revert whose body never arrived does not vanish, it MOVES from one count to
// the other, and Change Failure Rate then reads better than reality.
func diffRevertVerdicts(got, want []dora.CommitInfo) []string {
	gotCands, gotUnresolved := detectRevertsOver(got)
	wantCands, wantUnresolved := detectRevertsOver(want)

	var findings []string
	if len(gotCands) != len(wantCands) || gotUnresolved != wantUnresolved {
		findings = append(findings, fmt.Sprintf("DetectReverts over the split read %d "+
			"candidate(s)/%d unresolved, over the single-call form %d/%d. A revert that arrives "+
			"carrying only its subject is not a blank chart — it silently becomes an `unresolved` "+
			"count instead of a Change Failure Rate sample, which makes the number read BETTER "+
			"than reality\n got: %+v\nwant: %+v",
			len(gotCands), gotUnresolved, len(wantCands), wantUnresolved, gotCands, wantCands))
	}
	for i := range wantCands {
		if i < len(gotCands) && gotCands[i] != wantCands[i] {
			findings = append(findings, fmt.Sprintf("revert candidate %d: got %+v, want %+v",
				i, gotCands[i], wantCands[i]))
		}
	}
	return findings
}

func short(hash string) string {
	if len(hash) > 8 {
		return hash[:8]
	}
	return hash
}

// TestTheComparisonsCatchEveryWayTheSplitCanComeUpShort is this change's
// committed mutation evidence. Each row breaks a CORRECT result in exactly one
// way and names the comparison that must report it, a fragment of the finding
// it must produce, and — the half that carries the discrimination — the
// comparison that must stay SILENT.
//
// Three rows reproduce a mutation actually applied to the production code while
// this was written (see the file header); the rest cover the shapes a future
// edit could introduce.
func TestTheComparisonsCatchEveryWayTheSplitCanComeUpShort(t *testing.T) {
	t.Parallel()
	dir := buildAwkwardRepo(t)
	correct, answered := New().CommitsInRange(noBudget(), dir, "", "v0.2.0")
	if !answered {
		t.Fatal("git did not answer")
	}

	// The vacuity guard, and it is what makes every row below mean something:
	// a comparison that reported unconditionally would satisfy all seven.
	if f := diffCommitSets(correct, correct); len(f) != 0 {
		t.Fatalf("the set comparison reported %v against a correct result", f)
	}
	if f := diffRevertVerdicts(correct, correct); len(f) != 0 {
		t.Fatalf("the verdict comparison reported %v against a correct result", f)
	}
	if f := diffBodyContract(correct, correct); len(f) != 0 {
		t.Fatalf("the body-contract comparison reported %v against a correct result", f)
	}

	// subjectOnly is every commit reduced to its subject line — what
	// CommitsInRange would return with fillBodies deleted.
	subjectOnly := func(in []dora.CommitInfo) []dora.CommitInfo {
		out := clone(in)
		for i := range out {
			out[i].Body, _, _ = strings.Cut(out[i].Body, "\n")
		}
		return out
	}

	cases := []struct {
		name      string
		break_    func([]dora.CommitInfo) []dora.CommitInfo
		bySet     string // fragment the set comparison must report ("" = must be silent)
		byVerdict string // fragment the verdict comparison must report ("" = must be silent)
		byBody    string // fragment the body-contract comparison must report ("" = silent)
	}{
		{
			// THE mutation this change owes: a two-call form that returns a
			// subset. #1553 rejected `--since` because a dropped commit moves
			// LeadTime's median while still publishing Available:true.
			name:   "subsetOfCommits: the range walk returns one commit fewer",
			break_: func(in []dora.CommitInfo) []dora.CommitInfo { return clone(in[1:]) },
			bySet:  "corrupts LeadTime's median",
		},
		{
			name:   "dropsTheLastCommit: a truncated read",
			break_: func(in []dora.CommitInfo) []dora.CommitInfo { return clone(in[:len(in)-1]) },
			bySet:  "is missing from the split's result",
		},
		{
			name: "duplicatesOneCommit: a merge that appends instead of replacing",
			break_: func(in []dora.CommitInfo) []dora.CommitInfo {
				return append(clone(in), in[0])
			},
			bySet: "returned 2 times",
		},
		{
			name: "reordersTwo: the body fetch's order overwriting the walk's",
			break_: func(in []dora.CommitInfo) []dora.CommitInfo {
				out := clone(in)
				out[0], out[1] = out[1], out[0]
				return out
			},
			bySet: "order differs",
		},
		{
			name: "losesAnAuthorEpoch: %at dropped from the subject format",
			break_: func(in []dora.CommitInfo) []dora.CommitInfo {
				out := clone(in)
				out[0].AuthorEpoch = 0
				return out
			},
			bySet: "LeadTime is the median over exactly this field",
		},
		{
			// fillBodies deleted: every commit carries only its subject, so
			// every revert loses its trailer. The set is UNCHANGED, which is
			// exactly why the verdict comparison exists.
			name:      "bodiesNeverFetched: fillBodies deleted",
			break_:    subjectOnly,
			byVerdict: "unresolved",
			byBody:    "not fetched in full",
		},
		{
			// Bodies fetched for the commits carrying the TRAILER rather than
			// for the commits with a revert SUBJECT. This is the row that earns
			// the third comparison, and it is the only one here that grades a
			// shape THIS implementation cannot express — it filters on subjects
			// it already holds, and no subject contains the trailer. What it
			// grades is the issue's own proposed option 1, a second
			// `--grep`-filtered walk, whose natural pattern is the trailer.
			//
			// It changes nothing DetectReverts can see: a revert-shaped commit
			// with no trailer is `unresolved` whether its body was fetched or
			// not, so the set and the verdict are both unchanged and the sole
			// breakage is of what CommitInfo.Body promises. A future consumer of
			// Body is who that costs, which is exactly the failure a
			// verdict-only check licenses — and the reason the filter here asks
			// about the subject.
			name: "bodiesFetchedByTrailer: the filter asks the wrong question",
			break_: func(in []dora.CommitInfo) []dora.CommitInfo {
				out := clone(in)
				for i := range out {
					if dora.HasRevertSubject(out[i].Body) && !strings.Contains(out[i].Body, "This reverts commit") {
						out[i].Body, _, _ = strings.Cut(out[i].Body, "\n")
					}
				}
				return out
			},
			byBody: "not fetched in full",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			broken := tc.break_(correct)
			// The harness's own guard, in the shape #1390 earned: a mutation
			// that changed nothing makes every comparison below report about
			// something other than the row it names.
			if len(broken) == len(correct) && sameBodies(broken, correct) {
				t.Fatal("this row did not actually change anything, so whatever the comparisons " +
					"report below is not about the mutation it names")
			}
			assertFinding(t, "set comparison", diffCommitSets(broken, correct), tc.bySet)
			assertFinding(t, "verdict comparison", diffRevertVerdicts(broken, correct), tc.byVerdict)
			assertFinding(t, "body-contract comparison", diffBodyContract(broken, correct), tc.byBody)
		})
	}
}

// assertFinding requires the named comparison to report `fragment`, or — when
// fragment is empty — to report nothing at all. A bare "it failed" is refused:
// "the comparison spoke" and "THIS finding fired" are different claims, and
// only the second is evidence the check reaches what the row names.
func assertFinding(t *testing.T, who string, findings []string, fragment string) {
	t.Helper()
	joined := strings.Join(findings, "\n")
	if fragment == "" {
		if len(findings) != 0 {
			t.Errorf("the %s was expected to stay silent about this mutation and reported:\n%s\n"+
				"Two checks that both report on everything cannot tell these mutations apart",
				who, joined)
		}
		return
	}
	if !strings.Contains(joined, fragment) {
		t.Errorf("the %s did not report %q. It said:\n%s", who, fragment, joined)
	}
}

func clone(in []dora.CommitInfo) []dora.CommitInfo {
	return append([]dora.CommitInfo(nil), in...)
}

func sameBodies(a, b []dora.CommitInfo) bool {
	for i := range a {
		if i >= len(b) || a[i] != b[i] {
			return false
		}
	}
	return true
}

// diffBodyContract is the third comparison, and the mutation corpus is what
// shows it is not a duplicate of the other two: an adapter that fetched bodies
// by grepping for the TRAILER instead of the subject returns the same commit
// set AND the same DetectReverts verdict — the two checks above are both
// silent — while leaving a revert-shaped commit carrying only its subject, in
// breach of what dora.CommitInfo.Body promises. That is the shape a future
// consumer of Body would be hurt by, and this is the only thing that sees it.
//
// The three clauses are exactly what CommitInfo.Body guarantees, no more:
// nothing here asserts that a non-revert commit's Body is its %B, because
// #1564's whole subject is that it is not.
func diffBodyContract(got []dora.CommitInfo, want []dora.CommitInfo) []string {
	full := make(map[string]string, len(want))
	for _, c := range want {
		full[c.Hash] = c.Body
	}
	var findings []string
	for _, c := range got {
		whole, ok := full[c.Hash]
		if !ok {
			continue // a foreign commit is the set comparison's finding, not this one's
		}
		// 1. The verdict HasRevertSubject reaches is the same one it would
		//    reach on the complete message. This is what makes the subject a
		//    safe stand-in for the body in the filter, and it is asserted
		//    rather than the subject STRING, because git's %s folds a
		//    multi-line first paragraph and so is not byte-identical to %B's
		//    first line — only prefix-identical, which is all the filter reads.
		if dora.HasRevertSubject(c.Body) != dora.HasRevertSubject(whole) {
			findings = append(findings, fmt.Sprintf("commit %s: HasRevertSubject is %v over the "+
				"body returned and %v over the complete message — the filter and the detector "+
				"would disagree about the same commit", short(c.Hash),
				dora.HasRevertSubject(c.Body), dora.HasRevertSubject(whole)))
			continue
		}
		// 2. Nothing arrives empty when there was something to carry.
		if c.Body == "" && whole != "" {
			findings = append(findings, fmt.Sprintf("commit %s came back with an empty body",
				short(c.Hash)))
			continue
		}
		// 3. A revert-shaped commit carries its COMPLETE message, or its
		//    trailer is unreadable and it silently stops being a Change
		//    Failure Rate sample.
		if dora.HasRevertSubject(c.Body) && trimRecordSeparator(c.Body) != trimRecordSeparator(whole) {
			findings = append(findings, fmt.Sprintf("commit %s is revert-shaped but its body was "+
				"not fetched in full:\n got %q\nwant %q", short(c.Hash), c.Body, whole))
		}
	}
	return findings
}

// trimRecordSeparator removes the trailing newlines `--pretty=format:` leaves
// on a body.
//
// `format:` SEPARATES records with a newline (where `tformat:` would terminate
// them), and the parser attributes that newline to the body it follows — so how
// many trailing newlines a body carries depends on its POSITION in git's
// output, and the two calls emit their records in different orders. That is a
// pre-existing property of this parse, not something #1564 introduced, and it is
// read by nothing: DetectReverts cuts the subject at the first newline and
// matches the trailer with a multi-line-anchored pattern. It is trimmed HERE
// rather than fixed in the parser because fixing it would change the bodies the
// single-call form returned too, which is a separate change from this one.
func trimRecordSeparator(body string) string { return strings.TrimRight(body, "\n") }

// TestCommitsInRangeBodyContract pins what dora.CommitInfo.Body now promises,
// which is the one thing #1564 deliberately WEAKENED.
func TestCommitsInRangeBodyContract(t *testing.T) {
	t.Parallel()
	dir := buildAwkwardRepo(t)

	got, answered := New().CommitsInRange(noBudget(), dir, "", "v0.2.0")
	if !answered {
		t.Fatal("git did not answer")
	}
	want := singleCallOracle(t, dir, "v0.2.0")

	// Vacuity guard: clause 3 is the only one that can fail for a body-fetch
	// bug, and it never runs on a corpus holding no revert.
	revertShaped := 0
	for _, c := range want {
		if dora.HasRevertSubject(c.Body) {
			revertShaped++
		}
	}
	if revertShaped == 0 {
		t.Fatal("no commit in the fixture is revert-shaped, so the contract's third clause was " +
			"never exercised")
	}
	for _, finding := range diffBodyContract(got, want) {
		t.Error(finding)
	}
}

// TestCommitsInRangeAsksForNoBodiesWhenNothingIsRevertShaped is the reason the
// change is worth making at all: on the ordinary repository the body fetch does
// not happen, so the range costs one process and ~80 bytes per commit. Without
// this, an implementation that always made the second call would pass every
// other test here while spending #1563's aggregate budget twice over.
func TestCommitsInRangeAsksForNoBodiesWhenNothingIsRevertShaped(t *testing.T) {
	t.Parallel()
	dir := gitInitForTest(t)
	commitFileForTest(t, dir, "a.txt", "A")
	commitFileForTest(t, dir, "b.txt", "B")
	runGitForTest(t, dir, "tag", "v0.1.0")

	a, calls := recordingAdapter()
	if _, answered := a.CommitsInRange(noBudget(), dir, "", "v0.1.0"); !answered {
		t.Fatal("git did not answer")
	}

	argvs := calls.argv()
	if len(argvs) != 1 {
		t.Fatalf("a range holding no revert-shaped commit cost %d git processes, want 1: %v",
			len(argvs), argvs)
	}
	if !slicesContains(argvs[0], commitSubjectFormat) {
		t.Errorf("the range walk asked for %v, not %s — asking for %%B over the whole range is "+
			"#1564 itself: ~1.9 KB per commit, and gitMaxOutput at ~36k commits",
			argvs[0], commitSubjectFormat)
	}
	if slicesContains(argvs[0], commitBodyFormat) {
		t.Errorf("the range walk asked for the full body format %s", commitBodyFormat)
	}
}

// TestBodyFetchPutsItsRevisionsOnStdinNotArgv pins the decision recorded on
// runWithInput. At 41 bytes per hash, a range holding more than ~25k
// revert-shaped commits would exceed ARG_MAX, and execve fails before Start —
// a non-answer for a range that reads fine today. On stdin there is no such
// bound, which is what makes #1564 strictly better than what it replaces rather
// than better in the common case.
func TestBodyFetchPutsItsRevisionsOnStdinNotArgv(t *testing.T) {
	t.Parallel()
	dir := buildAwkwardRepo(t)

	// What the adapter WOULD have returned, to name the commits whose bodies
	// the fetch owes — read through the production path, since the capturing
	// fixture below replaces the very call under inspection.
	full, answered := New().CommitsInRange(noBudget(), dir, "", "v0.2.0")
	if !answered {
		t.Fatal("git did not answer")
	}

	a, calls := recordingAdapter()
	if _, answered := a.CommitsInRange(noBudget(), dir, "", "v0.2.0"); !answered {
		t.Fatal("git did not answer")
	}
	argvs := calls.argv()
	if len(argvs) != 2 {
		t.Fatalf("a range holding revert-shaped commits cost %d git processes, want 2: %v",
			len(argvs), argvs)
	}
	if !slicesContains(argvs[1], "--stdin") || !slicesContains(argvs[1], "--no-walk") {
		t.Errorf("the body fetch ran %v, want `log --no-walk --stdin`", argvs[1])
	}
	for _, c := range full {
		for _, arg := range argvs[1] {
			if arg == c.Hash {
				t.Fatalf("hash %s was passed on ARGV; a revert-heavy range then dies at ARG_MAX "+
					"with a non-answer, for a range the single-call form could read", c.Hash[:8])
			}
		}
	}

	// The other half: absent from argv is only good news if the revisions
	// reached git at all. This reads what the child actually received.
	capturing, stdinOf := capturingSecondCall(t)
	capturing.CommitsInRange(noBudget(), dir, "", "v0.2.0")
	fed := stdinOf()
	if fed == "" {
		t.Fatal("the body fetch was given no revisions on stdin either, so nothing was asked for")
	}
	wanted := 0
	for _, c := range full {
		if !dora.HasRevertSubject(c.Body) {
			continue
		}
		wanted++
		if !strings.Contains(fed, c.Hash) {
			t.Errorf("revert-shaped commit %s was not among the revisions fed to the body fetch",
				c.Hash[:8])
		}
	}
	if wanted == 0 {
		t.Fatal("no commit in the fixture was revert-shaped, so the loop above asserted nothing")
	}
}

// TestBodyFetchRunsUnderTheHistoryCeiling closes the gap
// TestEachShelloutRunsUnderTheCeilingForItsCostProfile cannot reach: that table
// drives CommitsInRange against a child which exits at once and writes nothing,
// so no commit is revert-shaped, the body fetch never happens, and the ceiling
// it runs under is measured by nobody. #1564's second call is a `git log` whose
// cost is bounded by the range rather than by a constant, so the short
// enrichment ceiling would make a legitimately revert-heavy range a non-answer.
func TestBodyFetchRunsUnderTheHistoryCeiling(t *testing.T) {
	t.Parallel()
	dir := buildAwkwardRepo(t)

	a, calls := recordingAdapter()
	if _, answered := a.CommitsInRange(noBudget(), dir, "", "v0.2.0"); !answered {
		t.Fatal("git did not answer")
	}
	budgets := calls.budgets()
	if len(budgets) != 2 {
		t.Fatalf("measured %d child budgets, want 2; the body fetch did not run, so this test "+
			"asserted nothing about it", len(budgets))
	}
	// The same vacuity guard the sibling table carries: with one value the
	// assertion below cannot tell the two profiles apart.
	if gitHistoryTimeout <= gitTimeout {
		t.Fatalf("gitHistoryTimeout (%v) is not longer than gitTimeout (%v)",
			gitHistoryTimeout, gitTimeout)
	}
	const tolerance = 2 * time.Second
	if diff := gitHistoryTimeout - budgets[1]; diff < 0 || diff > tolerance {
		t.Errorf("the body fetch ran under a %v budget, want ~%v (the enrichment ceiling is %v). "+
			"On the short one, a range holding many revert-shaped commits becomes a non-answer "+
			"and the DORA panel blanks", budgets[1], gitHistoryTimeout, gitTimeout)
	}
}

// TestBodyFetchFailsClosedWhenACommitComesBackMissing is the guard that makes
// "I asked for N and got N-1" a NON-answer instead of a partial one. It is the
// one failure in this method that does not blank anything on its own: a
// revert-shaped commit left carrying only its subject finds no trailer, becomes
// an `unresolved` count, and Change Failure Rate reads BETTER than reality.
func TestBodyFetchFailsClosedWhenACommitComesBackMissing(t *testing.T) {
	t.Parallel()
	dir := buildAwkwardRepo(t)

	// A git whose body fetch answers with nothing at all: exit 0, empty
	// stdout, which is what an upstream `--no-walk` that stopped resolving the
	// revisions on stdin would look like.
	var n int
	var mu sync.Mutex
	a := New()
	real := a.execGitCmd
	a.cmd = func(ctx context.Context, d string, args ...string) *exec.Cmd {
		mu.Lock()
		n++
		second := n == 2
		mu.Unlock()
		if second {
			return exec.CommandContext(ctx, "/bin/sh", "-c", "exit 0")
		}
		return real(ctx, d, args...)
	}

	if got, answered := a.CommitsInRange(noBudget(), dir, "", "v0.2.0"); answered {
		t.Errorf("a body fetch that returned none of the commits it asked for was reported as an "+
			"ANSWER, publishing %d commit(s) whose reverts are now invisible; Change Failure Rate "+
			"then reads better than reality, the one direction this adapter calls worse than a "+
			"blank chart", len(got))
	}
}

// TestSubjectSelectsASupersetOfTheBodySubjects is the argument the whole split
// rests on, checked against real git rather than reasoned about: %s is not
// byte-identical to the first line of %B — it skips leading blank lines and
// folds a multi-line first paragraph onto one line — and the claim is that
// neither difference can REMOVE a match.
//
// The direction matters. A superset costs a body nobody reads. A subset costs a
// Change Failure Rate sample, silently.
func TestSubjectSelectsASupersetOfTheBodySubjects(t *testing.T) {
	t.Parallel()
	dir := buildAwkwardRepo(t)

	subjects := singleCallOracleWith(t, dir, commitSubjectFormat, "v0.2.0")
	bodies := singleCallOracle(t, dir, "v0.2.0")
	if len(subjects) != len(bodies) || len(bodies) == 0 {
		t.Fatalf("the two formats read %d and %d commits; they must read the same non-empty set",
			len(subjects), len(bodies))
	}

	subjectMatches, bodyMatches := 0, 0
	for i := range bodies {
		bySubject := dora.HasRevertSubject(subjects[i].Body)
		byBody := dora.HasRevertSubject(bodies[i].Body)
		if bySubject {
			subjectMatches++
		}
		if byBody {
			bodyMatches++
		}
		if byBody && !bySubject {
			t.Errorf("commit %s: %%B's first line is revert-shaped but %%s is not (%q vs %q). "+
				"The adapter would not fetch its body, DetectReverts would find no trailer, and "+
				"the revert would silently become an `unresolved` count",
				bodies[i].Hash[:8], bodies[i].Body, subjects[i].Body)
		}
	}
	// Vacuity guard, both ways: a corpus where nothing is revert-shaped
	// satisfies the arm above, and so does one where everything is.
	if bodyMatches == 0 || bodyMatches == len(bodies) {
		t.Fatalf("%d of %d fixture commits are revert-shaped by %%B; the superset claim is not "+
			"being exercised", bodyMatches, len(bodies))
	}
	if subjectMatches < bodyMatches {
		t.Errorf("%%s selected %d commits and %%B's first line %d; the subject filter must never "+
			"select fewer", subjectMatches, bodyMatches)
	}
}

func singleCallOracleWith(t *testing.T, dir, format, rangeSpec string) []dora.CommitInfo {
	t.Helper()
	cmd := exec.Command("git", "log", format, rangeSpec)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("`git log %s %s`: %v", format, rangeSpec, err)
	}
	return parseCommitRecords(out)
}

// ---------------------------------------------------------------------------
// Recording seam
// ---------------------------------------------------------------------------

// callLog records the argv of each git child, so a test can assert HOW MANY
// processes a range cost and what each was asked for — the two facts #1564 is
// about that no return value carries.
type callLog struct {
	mu        sync.Mutex
	argvs     [][]string
	deadlines []time.Duration
}

func (c *callLog) argv() [][]string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([][]string(nil), c.argvs...)
}

func (c *callLog) budgets() []time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]time.Duration(nil), c.deadlines...)
}

// recordingAdapter is the production adapter with its builder wrapped: real git
// runs, and the argv is recorded on the way through. Wrapping rather than
// faking is deliberate — a fake git would make every assertion here a statement
// about the fake.
func recordingAdapter() (*Adapter, *callLog) {
	log := &callLog{}
	a := New()
	build := a.execGitCmd
	a.cmd = func(ctx context.Context, dir string, args ...string) *exec.Cmd {
		log.mu.Lock()
		log.argvs = append(log.argvs, append([]string(nil), args...))
		var budget time.Duration
		if deadline, ok := ctx.Deadline(); ok {
			budget = time.Until(deadline)
		}
		log.deadlines = append(log.deadlines, budget)
		log.mu.Unlock()
		return build(ctx, dir, args...)
	}
	return a, log
}

// capturingSecondCall is the production adapter with its SECOND child replaced
// by a shell that copies its stdin to a file. It is how a test reads what the
// adapter actually fed git, without a production seam existing only for the
// test: the adapter sets cmd.Stdin on whatever the builder returned, so a child
// that drains stdin observes exactly the bytes runWithInput supplied.
//
// The substituted child produces no commit records, so the call it stands in
// for reports a non-answer. That is fine and is why this is a separate fixture
// from recordingAdapter: what it is used to assert is the INPUT, and the
// fail-closed behaviour on an empty body fetch has its own test.
func capturingSecondCall(t *testing.T) (*Adapter, func() string) {
	t.Helper()
	sink := filepath.Join(t.TempDir(), "stdin.txt")
	var n int
	var mu sync.Mutex
	a := New()
	build := a.execGitCmd
	a.cmd = func(ctx context.Context, dir string, args ...string) *exec.Cmd {
		mu.Lock()
		n++
		second := n == 2
		mu.Unlock()
		if second {
			return exec.CommandContext(ctx, "/bin/sh", "-c", "cat > "+sink)
		}
		return build(ctx, dir, args...)
	}
	return a, func() string {
		b, err := os.ReadFile(sink)
		if err != nil {
			return ""
		}
		return string(b)
	}
}

func slicesContains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}
