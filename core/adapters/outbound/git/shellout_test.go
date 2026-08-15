package git

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"irrlicht/core/pkg/shellout"
)

// This file covers the two halves of #1543, and they need different evidence.
//
// The COLLAPSE is a pre-existing defect: before the fix, "git could not be run"
// and "this directory is not a repo" both arrived as "". Its test was seen red
// against the unmodified adapter — the failure is in the PR body.
//
// The BOUND is new, so there is no "before the fix" to run it against. Its
// tests owe a deliberate mutation instead: removing the ceiling, or widening it
// past the fixture's stall, and confirming they go red. Both mutations were
// run; what each reported is recorded on the test it breaks.

// ---------------------------------------------------------------------------
// Fixtures
// ---------------------------------------------------------------------------

// stalledGit is a child that runs past the ceiling without ever answering.
//
// /bin/sleep rather than a script the test writes, for the reason
// processlifecycle's stalledChild records: the first exec of a newly created
// file is code-signature-evaluated on macOS (measured at 2.14s there), which
// would make a bound test slow for a reason unrelated to the bound.
func stalledGit(ctx context.Context, _ string, _ ...string) *exec.Cmd {
	return exec.CommandContext(ctx, "/bin/sleep", "30")
}

// orphanHoldingStdout exits promptly but leaves a background child holding the
// stdout pipe. This is what WaitDelay exists for, and the ceiling does NOT
// cover it: the deadline never fires because the process the context knows
// about has already exited.
//
// Measured on this machine (go1.25 darwin/arm64, 2s ceiling) — the numbers the
// mutation evidence on TestOrphanHoldingStdoutIsANonAnswer refers to:
//
//	with WaitDelay:     500ms, err = "exec: WaitDelay expired before I/O complete"
//	without WaitDelay:  30.01s, err = NIL
//
// The second row is why this fixture is here rather than only a hang test. A
// nil error is an ANSWER, so without WaitDelay the adapter does not merely
// block for the orphan's whole lifetime — it then reports whatever partial
// output it collected as a complete, successful read.
func orphanHoldingStdout(ctx context.Context, _ string, _ ...string) *exec.Cmd {
	return exec.CommandContext(ctx, "/bin/sh", "-c", "sleep 30 & echo hi")
}

// missingGit is a binary that does not exist: the child never starts, so there
// is no exit status at all. shellout.Answered reports it as a non-answer
// because the error is not an *exec.ExitError.
func missingGit(ctx context.Context, _ string, _ ...string) *exec.Cmd {
	return exec.CommandContext(ctx, "/nonexistent/irrlicht-no-such-git-1543")
}

// floodingGit writes past gitMaxOutput and exits 0 — an answer by exit status
// whose output cannot be held. It writes a bounded amount and exits rather
// than streaming forever (`yes`), so the test costs one pipe copy instead of
// the whole ceiling; what is under test is the cap, not the ceiling.
func floodingGit(ctx context.Context, _ string, _ ...string) *exec.Cmd {
	over := strconv.Itoa(gitMaxOutput + 1<<20)
	return exec.CommandContext(ctx, "/bin/sh", "-c", "head -c "+over+" /dev/zero")
}

// withCmd returns an adapter whose git is build, under the production
// ceilings. Every method routes through Adapter.run, so one seam covers all
// seven shellouts.
func withCmd(build gitCmd) *Adapter {
	return &Adapter{cmd: build, timeout: gitTimeout, historyTimeout: gitHistoryTimeout}
}

// withCeiling is withCmd under a SHORT ceiling, for the tests whose subject is
// the ceiling firing rather than its value. Waiting out the real 5s twice is
// the package's slowest and most load-sensitive cost, and neither test learns
// anything from the extra 4.9s. The real constants stay proven by
// TestProductionAdapterIsBounded and
// TestEachShelloutRunsUnderTheCeilingForItsCostProfile, which run the adapter
// New() builds.
//
// It sets BOTH ceilings, which matters since #1553 gave the adapter two: the
// callers whose subject is "the ceiling fired" (the flooding one drives
// RevertedCommits) want a short one whichever profile they happen to reach,
// and one left at the production 30s would make that test wait 30s to prove
// something about the cap. withCeilings below is for the one test that needs
// them to DIFFER.
func withCeiling(build gitCmd, d time.Duration) *Adapter {
	return &Adapter{cmd: build, timeout: d, historyTimeout: d}
}

// withCeilings is withCeiling with the two ceilings driven independently —
// the only shape that can observe a history walk outliving the enrichment
// ceiling, which is #1553's whole subject.
func withCeilings(build gitCmd, fixed, history time.Duration) *Adapter {
	return &Adapter{cmd: build, timeout: fixed, historyTimeout: history}
}

// withBinary is the adapter New() returns, running binary instead of git —
// the production builder and the production ceiling, with one substitution.
//
// It is built FROM New() rather than from a struct literal on purpose: what
// TestProductionAdapterIsBounded has to prove is a property of the adapter the
// daemon builds, so the injected binary must be the only thing that differs
// from it. A literal would restate New()'s body and could then drift from it,
// which is the shape #1390 removed from the path-confinement contract.
func withBinary(binary string) *Adapter {
	a := New()
	a.path = binary
	return a
}

// testCeiling is short enough to keep the suite fast and long enough that a
// loaded machine still starts the child before it fires.
const testCeiling = 300 * time.Millisecond

// ---------------------------------------------------------------------------
// The collapse (#1543 defect 1)
// ---------------------------------------------------------------------------

// TestNonAnswerIsDistinguishableFromAGenuineMiss is the defect test, in the
// form it takes now that the adapter can express the distinction. Its pre-fix
// form asserted the only thing a single-return API allowed — that the two
// situations must not produce the same value — and failed:
//
//	GetBranch: a git that could not be RUN ("") is indistinguishable from a
//	directory that genuinely is not a repo ("") — #1543
//
// A real repo is used for the non-answer arm on purpose: the branch IS
// resolvable, so an empty result cannot be excused as "there was nothing".
func TestNonAnswerIsDistinguishableFromAGenuineMiss(t *testing.T) {
	realRepo := gitInitForTest(t)
	commitFileForTest(t, realRepo, "a.txt", "A")
	notARepo := t.TempDir()

	// A genuine miss: git runs fine and reports that this is not a repo.
	branch, answered := New().GetBranch(notARepo)
	if !answered {
		t.Fatal("a directory that is not a repo is an ANSWER — git exits 128 and has said so")
	}
	if branch != "" {
		t.Errorf("non-repo: got branch %q, want empty", branch)
	}

	// A non-answer: git cannot be run at all.
	branch, answered = withCmd(missingGit).GetBranch(realRepo)
	if answered {
		t.Error("a git that never started was reported as an answer; \"\" would then " +
			"mean \"this repo has no branch\", which is #1543")
	}
	if branch != "" {
		t.Errorf("non-answer: got branch %q, want empty", branch)
	}
}

// TestEveryShelloutReportsANonAnswer walks the seven shellouts plus
// GetProjectName, which starts no child of its own but forwards GetGitRoot's
// verdict. The vacuity guard is the
// second half of each row: the same call against a real repo must ANSWER, or a
// method that reported false unconditionally would pass the first half.
func TestEveryShelloutReportsANonAnswer(t *testing.T) {
	repo := gitInitForTest(t)
	sha := commitFileForTest(t, repo, "a.txt", "A")
	runGitForTest(t, repo, "tag", "v0.1.0")

	broken, working := withCmd(missingGit), New()

	cases := []struct {
		name    string
		unread  func(*Adapter) bool // reports the adapter's answered bit
		wantVal string              // description of what the zero value would have meant
	}{
		{"GetBranch", func(a *Adapter) bool { _, ok := a.GetBranch(repo); return ok },
			"detached HEAD"},
		{"GetHeadCommit", func(a *Adapter) bool { _, ok := a.GetHeadCommit(repo); return ok },
			"not a git repo (persisted as YieldUnknown)"},
		{"RevertedCommits", func(a *Adapter) bool { _, ok := a.RevertedCommits(repo); return ok },
			"this repo has no reverts"},
		{"ListReleaseTags", func(a *Adapter) bool { _, ok := a.ListReleaseTags(repo); return ok },
			"no releases found for this project"},
		{"CommitsInRange", func(a *Adapter) bool { _, ok := a.CommitsInRange(repo, "", "v0.1.0"); return ok },
			"this release shipped no commits"},
		{"TagContaining", func(a *Adapter) bool { _, ok := a.TagContaining(repo, sha); return ok },
			"this commit was never released (which LOWERS change failure rate)"},
		{"GetGitRoot", func(a *Adapter) bool { _, ok := a.GetGitRoot(repo); return ok },
			"project not found or not a git repository"},
		{"GetProjectName", func(a *Adapter) bool { _, ok := a.GetProjectName(repo); return ok },
			"the directory basename, cached for the daemon's lifetime"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.unread(broken) {
				t.Errorf("a git that never started was reported as an answer; its zero value "+
					"would then mean %q", tc.wantVal)
			}
			// Vacuity guard: without this, a method hard-coded to report
			// "never answered" passes the arm above.
			if !tc.unread(working) {
				t.Error("a real git against a real repo was reported as a non-answer")
			}
		})
	}
}

// TestNotARepoIsAnAnswer is the other direction, and it is the arm that keeps
// the fix from being "report a non-answer whenever anything is empty". Every
// exit status git produces on its own is an answer: measured on git 2.50.1
// (Apple Git-155, darwin/arm64), not-a-repo is 128, an unborn branch's
// rev-parse is 128, a range naming an unknown ref is 128, and `tag --contains`
// with an unresolvable object name is 129.
func TestNotARepoIsAnAnswer(t *testing.T) {
	notARepo := t.TempDir()
	unborn := realPath(t, t.TempDir())
	runGitForTest(t, unborn, "init")
	repo := gitInitForTest(t)
	commitFileForTest(t, repo, "a.txt", "A")

	a := New()
	cases := []struct {
		name string
		ok   func() bool
	}{
		{"not a repo/GetBranch", func() bool { _, ok := a.GetBranch(notARepo); return ok }},
		{"not a repo/GetHeadCommit", func() bool { _, ok := a.GetHeadCommit(notARepo); return ok }},
		{"not a repo/RevertedCommits", func() bool { _, ok := a.RevertedCommits(notARepo); return ok }},
		{"not a repo/ListReleaseTags", func() bool { _, ok := a.ListReleaseTags(notARepo); return ok }},
		{"not a repo/TagContaining", func() bool { _, ok := a.TagContaining(notARepo, "deadbeef"); return ok }},
		{"not a repo/GetGitRoot", func() bool { _, ok := a.GetGitRoot(notARepo); return ok }},
		// The seventh method. It was the one row missing from this table
		// (#1551 QA), which mattered because CommitsInRange is the method
		// whose non-answer biases a DORA median rather than blanking a field.
		{"not a repo/CommitsInRange", func() bool { _, ok := a.CommitsInRange(notARepo, "", "HEAD"); return ok }},
		{"unborn branch/GetHeadCommit", func() bool { _, ok := a.GetHeadCommit(unborn); return ok }},
		{"unborn branch/RevertedCommits", func() bool { _, ok := a.RevertedCommits(unborn); return ok }},
		{"unresolvable object/TagContaining", func() bool { _, ok := a.TagContaining(repo, "deadbeef"); return ok }},
		{"unknown ref range/CommitsInRange", func() bool { _, ok := a.CommitsInRange(repo, "v9.9.9", "v9.9.8"); return ok }},
	}
	for _, tc := range cases {
		if !tc.ok() {
			t.Errorf("%s: reported as a non-answer, but git RAN and said so — reporting it "+
				"as unread would blank a chart that should read \"not a git repository\"", tc.name)
		}
	}
}

// TestNothingAskedIsNotANonAnswer pins the early-return guards. An empty dir
// starts no child, so nothing failed — the reading processTTYVia gives a
// non-positive pid.
func TestNothingAskedIsNotANonAnswer(t *testing.T) {
	a := withCmd(func(context.Context, string, ...string) *exec.Cmd {
		t.Fatal("no child process should be started when there is nothing to ask about")
		return nil
	})
	checks := []struct {
		name string
		ok   bool
	}{
		{"GetBranch", second(a.GetBranch(""))},
		{"GetHeadCommit", second(a.GetHeadCommit(""))},
		{"GetGitRoot", second(a.GetGitRoot(""))},
		{"GetProjectName", second(a.GetProjectName(""))},
		{"TagContaining/no hash", second(a.TagContaining("/tmp", ""))},
	}
	for _, c := range checks {
		if !c.ok {
			t.Errorf("%s: an unasked question was reported as a non-answer", c.name)
		}
	}
	if _, ok := a.RevertedCommits(""); !ok {
		t.Error("RevertedCommits: an unasked question was reported as a non-answer")
	}
	if _, ok := a.ListReleaseTags(""); !ok {
		t.Error("ListReleaseTags: an unasked question was reported as a non-answer")
	}
	if _, ok := a.CommitsInRange("", "", ""); !ok {
		t.Error("CommitsInRange: an unasked question was reported as a non-answer")
	}
}

func second(_ string, ok bool) bool { return ok }

// ---------------------------------------------------------------------------
// The bound (#1543 defect 2)
// ---------------------------------------------------------------------------

// TestRunCeilingActuallyFires is the bound's own test, and it is the one that
// owes a mutation rather than a red-first run — before this change there was no
// ceiling to test.
//
// MUTATION EVIDENCE (both run, both red):
//
//   - Replacing context.WithTimeout(…, gitTimeout) in Adapter.run with
//     context.Background(): this test does not fail, it HANGS — the run never
//     returns, and `go test` kills the package at its own 10-minute timeout
//     with `panic: test timed out after 10m0s`. That is the unbounded adapter,
//     reproduced.
//   - Widening gitTimeout to 60s: fails on the elapsed assertion, reporting
//     ~30s against a half-ceiling of 30s.
//
// The elapsed bound is gitTimeout+shellout.WaitDelay+slack rather than exactly
// gitTimeout: the child is SIGKILLed at the deadline and exec then waits up to
// WaitDelay for the output pipe.
func TestRunCeilingActuallyFires(t *testing.T) {
	t.Parallel()

	start := time.Now()
	_, answered := withCeiling(stalledGit, testCeiling).GetBranch("/tmp")
	elapsed := time.Since(start)

	if answered {
		t.Error("a child killed by the ceiling was reported as an answer — the empty branch " +
			"would then mean \"detached HEAD\"")
	}
	if elapsed < testCeiling/2 {
		t.Errorf("returned after %v, well under the %v ceiling: the child cannot have been "+
			"the 30s sleep this test arranges, so the ceiling is not what stopped it", elapsed, testCeiling)
	}
	if limit := testCeiling + shellout.WaitDelay + 5*time.Second; elapsed > limit {
		t.Errorf("took %v against a %v ceiling (+%v WaitDelay); the bound did not fire",
			elapsed, testCeiling, shellout.WaitDelay)
	}
}

// TestOrphanHoldingStdoutIsANonAnswer is WaitDelay's own arm, and it covers a
// failure the ceiling cannot: the child exits promptly, so the deadline never
// fires, but a grandchild that inherited the stdout pipe holds it open and
// exec keeps waiting for an EOF that does not come. git spawns such children
// routinely — a credential helper, an fsmonitor daemon, a pager forced on by
// global config.
//
// MUTATION EVIDENCE: deleting `cmd.WaitDelay = shellout.WaitDelay` from Adapter.run
// makes this fail on BOTH assertions, and the first is the one worth having.
// Measured directly (go1.25 darwin/arm64): without WaitDelay the call returns
// after 30.01s with err == NIL — so the adapter does not just block, it then
// reports the orphan's partial output as a complete successful read. With
// WaitDelay it returns in 500ms with "exec: WaitDelay expired before I/O
// complete", which is not an *exec.ExitError and so is correctly a non-answer.
func TestOrphanHoldingStdoutIsANonAnswer(t *testing.T) {
	t.Parallel()

	start := time.Now()
	_, answered := withCeiling(orphanHoldingStdout, testCeiling).GetHeadCommit("/tmp")
	elapsed := time.Since(start)

	if answered {
		t.Error("a read that never saw EOF was reported as an answer; whatever partial " +
			"output it collected would be published as the whole of it")
	}
	if limit := shellout.WaitDelay + 2*time.Second; elapsed > limit {
		t.Errorf("took %v; WaitDelay (%v) is supposed to bound exactly this, and the "+
			"ceiling cannot — the process the context knows about exited immediately",
			elapsed, shellout.WaitDelay)
	}
}

// TestMissingBinaryFailsImmediately pins the most common real-world
// non-answer, and pins that it costs nothing: the daemon runs under launchd
// with a minimal PATH, and pathutil.MustResolve falls back to the bare name
// when git is not in a trusted directory. That must fail before anything is
// spawned rather than waiting out gitTimeout.
func TestMissingBinaryFailsImmediately(t *testing.T) {
	t.Parallel()

	start := time.Now()
	_, answered := withCeiling(missingGit, testCeiling).GetGitRoot("/tmp")
	elapsed := time.Since(start)

	if answered {
		t.Error("a binary that does not exist was reported as an answer")
	}
	if elapsed > testCeiling/2 {
		t.Errorf("took %v for a missing binary; the PATH lookup should fail before anything "+
			"is spawned, well inside the %v budget", elapsed, testCeiling)
	}
}

// TestFloodingChildIsBoundedAndReportsANonAnswer covers gitMaxOutput, and the
// polarity is the point. cliprobe TRUNCATES on overflow, because a truncated
// version banner still contains the version. Here truncation would silently
// drop commits and releases, and DORA would publish the surviving subset as
// Available:true — a biased median is harder to notice than a blank chart.
//
// MUTATION EVIDENCE (both run, both red):
//
//   - Dropping the `if buf.overflowed { return nil, false }` arm from
//     Adapter.run: fails with "a truncated read was reported as an answer".
//   - Removing the cap itself (cappedBuffer replaced by a plain bytes.Buffer):
//     fails the same assertion, because the whole 65 MiB is then accepted as
//     an answer. That is the mutation that matters for memory — the fixture
//     writes a bounded amount so the test stays cheap, but production has no
//     such courtesy and a 1M-commit `git log` is measured below at ~810 MB.
func TestFloodingChildIsBoundedAndReportsANonAnswer(t *testing.T) {
	t.Parallel()

	start := time.Now()
	_, answered := withCeiling(floodingGit, testCeiling).RevertedCommits("/tmp")
	elapsed := time.Since(start)

	if answered {
		t.Error("a truncated read was reported as an answer; the surviving commits would " +
			"then be published as if they were all of them")
	}
	if limit := testCeiling + shellout.WaitDelay + 5*time.Second; elapsed > limit {
		t.Errorf("took %v against a %v ceiling; the flood was not bounded", elapsed, testCeiling)
	}
}

// TestUnderTheCapIsStillAnAnswer is the cap's vacuity guard: without it, a run
// that reported every read as truncated would satisfy the arm above. It also
// pins the boundary in the direction that blinds the adapter — a read of
// exactly the cap is not an overflow of it, because nothing was dropped.
func TestUnderTheCapIsStillAnAnswer(t *testing.T) {
	t.Parallel()

	build := func(n int) gitCmd {
		return func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
			return exec.CommandContext(ctx, "/bin/sh", "-c", "head -c "+strconv.Itoa(n)+" /dev/zero")
		}
	}
	// gitMaxOutput itself is the row that matters, and it is the one this
	// test adds over shellout.CappedBuffer's unit tests: it pins that run()
	// wires the REAL constant, not just that the buffer's arithmetic is right.
	// A gitMaxOutput-1 row would cost a second 64 MiB pipe copy to assert what
	// TestCappedBufferStopsAtItsLimit already pins at Limit: 8.
	for _, n := range []int{1 << 10, gitMaxOutput} {
		out, answered := withCmd(build(n)).run(fixedCost, "/tmp", "irrelevant")
		if !answered {
			t.Errorf("%d bytes, under the %d-byte cap, was reported as a non-answer", n, gitMaxOutput)
		}
		if len(out) != n {
			t.Errorf("%d bytes written, %d kept", n, len(out))
		}
	}
}

// TestFailingGitDoesNotPublishItsStdoutAsData is findings 1 and 2 of PR #1551's
// review, and the regression it pins was INTRODUCED by #1543's first draft
// rather than being pre-existing: .Output()'s `if err != nil { return "" }`
// discarded stdout on any failure, and routing everything through
// shellout.Answered's empty-variadic form started forwarding it, because a
// non-zero NORMAL exit is an answer.
//
// It is an answer. Its stdout is not the answer's content.
//
// Measured (git 2.50.1 / Apple Git-155, darwin/arm64): on an unborn branch
// `git rev-parse HEAD` exits 128 and writes the literal string "HEAD" to
// STDOUT, not just to stderr. The first draft returned ("HEAD", true), which
// CaptureYieldOnReady persisted as a commit SHA with YieldState productive.
//
// RED-FIRST: run against the first draft this reports
//
//	GetHeadCommit(unborn) = "HEAD" — a failing git's stdout was published as a commit SHA
func TestFailingGitDoesNotPublishItsStdoutAsData(t *testing.T) {
	unborn := realPath(t, t.TempDir())
	runGitForTest(t, unborn, "init")

	a := New()

	head, answered := a.GetHeadCommit(unborn)
	if !answered {
		t.Fatal("an unborn branch is an ANSWER — git ran and exited 128")
	}
	if head != "" {
		t.Errorf("GetHeadCommit(unborn) = %q — a failing git's stdout was published as a "+
			"commit SHA; #373 says an unborn branch is YieldUnknown, and a session indexed "+
			"under the fake SHA %q is one the yield sweeper can never correlate", head, head)
	}

	branch, answered := a.GetBranch(unborn)
	if !answered {
		t.Fatal("an unborn branch is an ANSWER for GetBranch too")
	}
	if branch != "" {
		t.Errorf("GetBranch(unborn) = %q, want empty", branch)
	}
}

// TestPartialOutputBeforeAFatalIsNotACompleteAnswer is the other half of the
// same root cause, and the one with DORA-shaped blast radius: a `git log` that
// streams commits and then hits a fatal exits 128 having already written a
// prefix of the history. Publishing that prefix is a silently truncated commit
// list presented as Available:true — exactly what gitMaxOutput's doc argues
// must never happen, reached through a different door.
//
// The fixture writes two records and then exits non-zero, which is the shape
// without needing to corrupt a real object store.
//
// RED-FIRST: against the first draft, both arms report the truncated data.
func TestPartialOutputBeforeAFatalIsNotACompleteAnswer(t *testing.T) {
	t.Parallel()

	partial := func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
		// Two well-formed \x01-delimited records, then a non-zero exit.
		return exec.CommandContext(ctx, "/bin/sh", "-c",
			`printf '\001aaa\002100\002first\001bbb\002200\002second'; exit 128`)
	}
	a := withCmd(partial)

	commits, answered := a.CommitsInRange("/tmp", "", "HEAD")
	if !answered {
		t.Fatal("a git that exited 128 ANSWERED; only a killed or unstarted child has not")
	}
	if len(commits) != 0 {
		t.Errorf("CommitsInRange returned %d commit(s) from a git that died mid-stream; DORA "+
			"would publish a median lead time over a silently truncated history as Available:true",
			len(commits))
	}

	shas, answered := a.RevertedCommits("/tmp")
	if !answered {
		t.Fatal("same for RevertedCommits")
	}
	if len(shas) != 0 {
		t.Errorf("RevertedCommits returned %d SHA(s) from a partial read", len(shas))
	}
}

// TestASuccessfulGitStillReturnsItsOutput is the vacuity guard for the two
// tests above. Discarding stdout whenever err != nil is only correct if
// err == nil still delivers it — without this, an adapter that returned nil
// unconditionally would pass both.
func TestASuccessfulGitStillReturnsItsOutput(t *testing.T) {
	repo := gitInitForTest(t)
	sha := commitFileForTest(t, repo, "a.txt", "A")

	if got, answered := New().GetHeadCommit(repo); got != sha || !answered {
		t.Errorf("GetHeadCommit = (%q, %v), want (%q, true)", got, answered, sha)
	}
}

// TestProductionAdapterIsBounded is the wiring arm. Every test above builds an
// Adapter with an injected command, so none of them proves the adapter New()
// returns has a ceiling — which is the shape #1390 removed from the path
// confinement contract for the same reason. It drives the PRODUCTION builder
// (execGitCmd) under the PRODUCTION ceiling at a real stalling binary,
// substituted through the adapter's own path field.
//
// Until #1554 that substitution was an assignment to the package var gitPath,
// under t.Parallel(). It was clean only because every other parallel test in
// the package built its adapter through the injected gitCmd seam, which never
// reads gitPath — so the invariant was "no parallel test may call New()", and
// nothing enforced it. The first one that did would have run
// `/bin/sh -c 'exec sleep 30'` as git for the ~5s this test holds the stub in
// place, and reported it as a timing flake rather than a fixture collision.
// The seam removes the hazard rather than documenting it, so t.Parallel()
// stays and no test in this package needs to know what any other is doing.
func TestProductionAdapterIsBounded(t *testing.T) {
	t.Parallel()

	// A script rather than /bin/sleep directly: the adapter's argv is git's
	// ("rev-parse", "HEAD"), which sleep rejects as a duration and exits on
	// immediately. The script ignores its arguments and stalls, which is what
	// this needs to observe.
	stub := filepath.Join(t.TempDir(), "git")
	if err := os.WriteFile(stub, []byte("#!/bin/sh\nexec sleep 30\n"), 0o755); err != nil {
		t.Fatalf("write stub: %v", err)
	}
	// Pay macOS's first-exec code-signature evaluation (measured at 2.14s in
	// #1524) OUTSIDE the measurement, so it cannot be mistaken for the
	// ceiling, and so it cannot push a legitimate run past the assertion.
	warm := exec.Command(stub)
	_ = warm.Start()
	if warm.Process != nil {
		_ = warm.Process.Kill()
		_ = warm.Wait()
	}

	start := time.Now()
	_, answered := withBinary(stub).GetHeadCommit(t.TempDir())
	elapsed := time.Since(start)

	if answered {
		t.Error("the production adapter reported an answer from a child it had to kill")
	}
	if elapsed < gitTimeout/2 {
		t.Errorf("returned after %v, far under the %v ceiling — the stub cannot have run, "+
			"so this proves nothing about the production path", elapsed, gitTimeout)
	}
	if limit := gitTimeout + shellout.WaitDelay + 3*time.Second; elapsed > limit {
		t.Errorf("the production adapter took %v; New() is not running under gitTimeout", elapsed)
	}
}

// TestZeroValuedAdapterIsStillBounded covers ceiling()'s fallback, which
// nothing else reaches: every other test builds its Adapter through New() or
// one of the helpers above, all of which set timeout.
//
// It exists because the timeout field is a TEST seam, and a test seam whose
// zero value is "no ceiling" reintroduces #1543 the first time someone writes
// &Adapter{cmd: ...} — an entirely reasonable thing to write, and exactly the
// literal the helpers above are. The fallback makes unbounded unrepresentable
// rather than merely discouraged.
//
// MUTATION EVIDENCE: replacing ceiling()'s body with `return a.timeout` left
// the whole package GREEN before this test existed, which is why it is here —
// the fallback was uncovered, so nothing would have noticed it being deleted.
// With this test the same mutation fails, reporting `ceiling() = 0s ... want
// the 5s default`.
//
// Only the FIRST assertion fires under that mutation, and saying so is the
// point (#1551 QA, B5): a zero ceiling makes context.WithTimeout expire
// immediately, so the run returns in microseconds and the elapsed arms below
// are satisfied for the wrong reason. They are kept because they cover the
// OTHER direction — a negative or absurdly large fallback, which a zero-check
// alone would wave through — and the ceiling() assertion is what actually
// discriminates the deletion.
func TestZeroValuedAdapterIsStillBounded(t *testing.T) {
	t.Parallel()

	a := &Adapter{cmd: stalledGit} // no timeout set — the shape that must not be unbounded

	if got := a.ceiling(); got != gitTimeout {
		t.Errorf("ceiling() = %v on a zero-valued Adapter, want the %v default; a zero ceiling "+
			"means context.WithTimeout fires immediately and a negative one means never", got, gitTimeout)
	}
	// #1553's second ceiling needs the same fallback, and needs it MORE: every
	// helper in this file predates it and sets only `timeout`, so a missing
	// fallback would run every history walk under a zero ceiling — a
	// context.WithTimeout that has already expired, i.e. RevertedCommits and
	// CommitsInRange reporting a non-answer instantly, for a reason that has
	// nothing to do with git.
	if got := a.historyCeiling(); got != gitHistoryTimeout {
		t.Errorf("historyCeiling() = %v on a zero-valued Adapter, want the %v default", got, gitHistoryTimeout)
	}

	start := time.Now()
	_, answered := a.GetBranch("/tmp")
	elapsed := time.Since(start)

	if answered {
		t.Error("a child killed by the default ceiling was reported as an answer")
	}
	if limit := gitTimeout + shellout.WaitDelay + 5*time.Second; elapsed > limit {
		t.Errorf("took %v; a zero-valued Adapter is not bounded, which is #1543 reintroduced "+
			"through the test seam that was added to make it faster", elapsed)
	}
}

// TestEverySeamDefaultsToProduction covers the other two accessors the way
// TestZeroValuedAdapterIsStillBounded covers ceiling(): an Adapter with a
// field left at its zero value must run the PRODUCTION value for it, so no
// seam can leave the adapter in a state the daemon would never build.
//
// binary() is #1554's new field and needs it most. An empty path reaches
// exec.CommandContext as "", which fails to start on every call — so a
// zero-valued Adapter without the fallback resolves NOTHING, quietly, and
// every method reports a non-answer for a reason that has nothing to do with
// git.
//
// The second half is a distinct claim from the first: binary() returning the
// injected path and execGitCmd actually RUNNING it are two things, and an
// execGitCmd left reading the package var satisfies the first while ignoring
// the injection entirely — which is precisely the defect this issue's fix
// would be if it were only half applied.
//
// MUTATION EVIDENCE (#1554):
//   - `binary()` reduced to `return a.path` fails the first arm:
//     `binary() = "" on a zero-valued Adapter`.
//   - `execGitCmd` reading the package `gitPath` instead of `a.binary()` fails
//     the last arm: `the production builder ran "/usr/bin/git", want the
//     injected "/nonexistent/irrlicht-1554-stub"`.
//   - `builder()` returning a nil gitCmd fails the second arm rather than
//     panicking somewhere inside run().
func TestEverySeamDefaultsToProduction(t *testing.T) {
	t.Parallel()

	zero := &Adapter{} // every seam at its zero value

	if got := zero.binary(); got != gitPath {
		t.Errorf("binary() = %q on a zero-valued Adapter, want the resolved %q; an empty "+
			"path reaches exec as \"\", so every call becomes a non-answer", got, gitPath)
	}
	if zero.builder() == nil {
		t.Fatal("builder() = nil on a zero-valued Adapter; run() would panic on the call " +
			"rather than shelling out to the production git")
	}
	// ceilingFor is the whole of the profile→ceiling mapping, and it is one
	// expression, so pinning both directions costs two lines. Without the
	// second, a ceilingFor that returned the history ceiling for EVERYTHING
	// would satisfy the first — and that is the mutation that puts the
	// detector loop on a 30s stall.
	if got := zero.ceilingFor(historyCost); got != gitHistoryTimeout {
		t.Errorf("ceilingFor(historyCost) = %v, want %v", got, gitHistoryTimeout)
	}
	if got := zero.ceilingFor(fixedCost); got != gitTimeout {
		t.Errorf("ceilingFor(fixedCost) = %v, want %v", got, gitTimeout)
	}

	const stub = "/nonexistent/irrlicht-1554-stub"
	cmd := withBinary(stub).builder()(context.Background(), "/tmp", gitRevParseCmd, "HEAD")
	if cmd.Path != stub {
		t.Errorf("the production builder ran %q, want the injected %q; the injected path "+
			"is readable but not reaching the child", cmd.Path, stub)
	}
	if cmd.Dir != "/tmp" {
		t.Errorf("cmd.Dir = %q, want /tmp — the builder is not running git in the "+
			"directory it was asked about", cmd.Dir)
	}
	if want := []string{stub, gitRevParseCmd, "HEAD"}; !slices.Equal(cmd.Args, want) {
		t.Errorf("cmd.Args = %q, want %q", cmd.Args, want)
	}
}

// TestGitPathIsResolvedNotInherited pins go:S4036 — the adapter must not run
// whatever "git" the inherited PATH happens to resolve to.
//
// It asserts on what New()'s adapter WILL RUN rather than on the package var
// alone. Since #1554 those are two reads and only the first is the property:
// an adapter whose path field were wired to something else would leave a
// var-only assertion green while running that something else. The var is still
// read for the skip and for the equality arm, which is what pins the wiring.
//
// t.Parallel() is safe here now and was not before, which is the point rather
// than a tidy-up: gitPath is written once, at package initialisation, and
// nothing assigns to it afterwards — TestNothingAssignsToTheResolvedGitPath
// (gitpath_scan_test.go) is what keeps that true, so this unsynchronised read
// cannot race a fixture.
func TestGitPathIsResolvedNotInherited(t *testing.T) {
	t.Parallel()

	if gitPath == "git" {
		t.Skip("git is not under a trusted directory on this machine; pathutil fell back " +
			"to a PATH lookup, which is MustResolve's documented degradation")
	}
	resolved := New().binary()
	if resolved != gitPath {
		t.Errorf("New() runs %q, want the resolved %q; the constructor is not wiring "+
			"the pathutil-resolved git into the adapter", resolved, gitPath)
	}
	if !filepath.IsAbs(resolved) {
		t.Errorf("New() runs %q, want an absolute path under a trusted directory", resolved)
	}
	if strings.Contains(resolved, "..") {
		t.Errorf("New() runs %q, which contains a traversal", resolved)
	}
}

// ---------------------------------------------------------------------------
// The two cost profiles (#1553)
// ---------------------------------------------------------------------------

// TestEachShelloutRunsUnderTheCeilingForItsCostProfile is the wiring arm for
// #1553, and it is the test the mutation "put the history walks back on the
// short ceiling" has to fail. It reads the DEADLINE the adapter puts on the
// context it hands the builder, rather than outliving it: waiting out
// gitHistoryTimeout would cost 30s per run, which -count=4 turns into two
// minutes for a fact a deadline states exactly.
//
// It drives the adapter New() builds, with only the builder substituted, so
// what it pins is the PRODUCTION mapping of both constants — not a pair of
// values a helper chose. TestProductionAdapterIsBounded remains the arm that
// proves a real child actually dies at a real ceiling; this one proves which
// ceiling each of the eight methods asks for.
//
// MUTATION EVIDENCE is recorded on TestTheTwoCeilingsAreIndependentAtRuntime
// below, which the same mutations also redden.
func TestEachShelloutRunsUnderTheCeilingForItsCostProfile(t *testing.T) {
	t.Parallel()

	// Vacuity guard, and it is not decorative: every row below asserts
	// "budget == the constant this profile names", so if the two constants
	// were equal the whole table would pass against an adapter that had never
	// heard of a second profile. The 6x gap is the only reason the assertions
	// discriminate.
	if gitHistoryTimeout <= gitTimeout {
		t.Fatalf("gitHistoryTimeout (%v) is not longer than gitTimeout (%v); with one value "+
			"this test cannot tell the two profiles apart and #1553 is not fixed",
			gitHistoryTimeout, gitTimeout)
	}

	dir := realPath(t, t.TempDir())

	var budget time.Duration
	var built bool
	a := New()
	a.cmd = func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
		if deadline, ok := ctx.Deadline(); ok {
			budget = time.Until(deadline)
		}
		built = true
		// A child that exits at once: what is under test is the deadline the
		// adapter set, not what the child does with it.
		return exec.CommandContext(ctx, "/bin/sh", "-c", "exit 0")
	}

	cases := []struct {
		name string
		want time.Duration
		call func()
	}{
		{"GetBranch", gitTimeout, func() { a.GetBranch(dir) }},
		{"GetHeadCommit", gitTimeout, func() { a.GetHeadCommit(dir) }},
		{"GetGitRoot", gitTimeout, func() { a.GetGitRoot(dir) }},
		{"GetProjectName", gitTimeout, func() { a.GetProjectName(dir) }},
		{"ListReleaseTags", gitTimeout, func() { a.ListReleaseTags(dir) }},
		{"TagContaining", gitTimeout, func() { a.TagContaining(dir, "deadbeef") }},
		{"RevertedCommits", gitHistoryTimeout, func() { a.RevertedCommits(dir) }},
		{"CommitsInRange", gitHistoryTimeout, func() { a.CommitsInRange(dir, "", "HEAD") }},
	}

	const tolerance = 2 * time.Second // the read happens microseconds after the deadline is set
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			budget, built = 0, false
			tc.call()
			// Fail loudly rather than silently: a method that started no child
			// records no budget, and a zero budget would otherwise be reported
			// as "ran under a 0s ceiling" — a finding about a call that never
			// happened.
			if !built {
				t.Fatal("no child was built, so nothing was measured; this row is not exercising " +
					"the shellout it names")
			}
			if budget <= 0 {
				t.Fatalf("the context carried no deadline; #1543's whole subject is that every " +
					"child here runs under one")
			}
			if diff := tc.want - budget; diff < 0 || diff > tolerance {
				t.Errorf("ran under a %v budget, want ~%v. The other profile's ceiling is %v — a "+
					"history walk on the short one is #1553 reintroduced (a big repository gets a "+
					"permanent non-answer), and an enrichment read on the long one stalls the "+
					"detector loop for %v",
					budget, tc.want, otherCeiling(tc.want), gitHistoryTimeout)
			}
		})
	}
}

// otherCeiling names the ceiling a row did NOT want, so the failure message
// above can say which mistake it is looking at rather than only which number
// disagreed.
func otherCeiling(want time.Duration) time.Duration {
	if want == gitHistoryTimeout {
		return gitTimeout
	}
	return gitHistoryTimeout
}

// TestTheTwoCeilingsAreIndependentAtRuntime is the behavioural half: the
// deadline test above reads what the adapter INTENDS, this one watches a real
// child outlive the shorter ceiling. Together they are the two claims #1553
// makes — that the mapping is right, and that the longer ceiling is a ceiling
// rather than a number nothing consults.
//
// The two values are driven independently through the seams (short=100ms,
// long=2s) rather than at their production values, because the property is the
// INDEPENDENCE and waiting out 30s to observe it buys nothing. The gap is wide
// enough that shellout.WaitDelay (500ms) cannot close it: a history walk that
// had run under the short ceiling returns in at most ~600ms, well under the 1s
// this asserts it must exceed.
//
// MUTATION EVIDENCE (both run, both red — see the PR body for the output):
//
//   - RevertedCommits and CommitsInRange changed back to run(fixedCost, …):
//     this test fails with "RevertedCommits returned after ~100ms against a 2s
//     history ceiling", and
//     TestEachShelloutRunsUnderTheCeilingForItsCostProfile fails on both of its
//     history rows with "ran under a 5s budget, want ~30s".
//   - ceilingFor() reduced to `return a.historyCeiling()` — i.e. one ceiling
//     again, the other direction: this test fails on the GetBranch arm, and the
//     deadline test fails on all six fixed-cost rows.
func TestTheTwoCeilingsAreIndependentAtRuntime(t *testing.T) {
	t.Parallel()

	const short = 100 * time.Millisecond
	const long = 2 * time.Second

	a := withCeilings(stalledGit, short, long)

	start := time.Now()
	_, answered := a.GetBranch("/tmp")
	fixedElapsed := time.Since(start)
	if answered {
		t.Error("GetBranch: a child killed by the ceiling was reported as an answer")
	}

	start = time.Now()
	_, answered = a.RevertedCommits("/tmp")
	historyElapsed := time.Since(start)
	if answered {
		t.Error("RevertedCommits: a child killed by the ceiling was reported as an answer")
	}

	if historyElapsed < long/2 {
		t.Errorf("RevertedCommits returned after %v against a %v history ceiling — it is running "+
			"under the %v enrichment ceiling instead, which is #1553: the history walk a large "+
			"repository needs is cut off at the short one and the yield sweep reports that root "+
			"unread forever", historyElapsed, long, short)
	}
	if fixedElapsed > long/2 {
		t.Errorf("GetBranch returned after %v against a %v enrichment ceiling — it is running "+
			"under the %v history ceiling instead, which puts a %v stall inside the detector loop",
			fixedElapsed, short, long, gitHistoryTimeout)
	}
	// Upper bounds, so a ceiling that fired for the wrong reason (or not at
	// all) is not read as success.
	if limit := long + shellout.WaitDelay + 5*time.Second; historyElapsed > limit {
		t.Errorf("RevertedCommits took %v against a %v ceiling; the bound did not fire",
			historyElapsed, long)
	}
	if limit := short + shellout.WaitDelay + 5*time.Second; fixedElapsed > limit {
		t.Errorf("GetBranch took %v against a %v ceiling; the bound did not fire", fixedElapsed, short)
	}
}
