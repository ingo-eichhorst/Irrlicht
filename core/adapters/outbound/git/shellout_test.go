package git

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
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

// withCmd returns an adapter whose git is build. Every method routes through
// Adapter.run, so one seam covers all seven shellouts.
func withCmd(build gitCmd) *Adapter { return &Adapter{cmd: build} }

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

// TestEveryShelloutReportsANonAnswer walks all seven. The vacuity guard is the
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
// The elapsed bound is gitTimeout+gitWaitDelay+slack rather than exactly
// gitTimeout: the child is SIGKILLed at the deadline and exec then waits up to
// WaitDelay for the output pipe.
func TestRunCeilingActuallyFires(t *testing.T) {
	t.Parallel()

	start := time.Now()
	_, answered := withCmd(stalledGit).GetBranch("/tmp")
	elapsed := time.Since(start)

	if answered {
		t.Error("a child killed by the ceiling was reported as an answer — the empty branch " +
			"would then mean \"detached HEAD\"")
	}
	if elapsed < gitTimeout/2 {
		t.Errorf("returned after %v, well under the %v ceiling: the child cannot have been "+
			"the 30s sleep this test arranges, so the ceiling is not what stopped it", elapsed, gitTimeout)
	}
	if limit := gitTimeout + gitWaitDelay + 2*time.Second; elapsed > limit {
		t.Errorf("took %v against a %v ceiling (+%v WaitDelay); the bound did not fire",
			elapsed, gitTimeout, gitWaitDelay)
	}
}

// TestOrphanHoldingStdoutIsANonAnswer is WaitDelay's own arm, and it covers a
// failure the ceiling cannot: the child exits promptly, so the deadline never
// fires, but a grandchild that inherited the stdout pipe holds it open and
// exec keeps waiting for an EOF that does not come. git spawns such children
// routinely — a credential helper, an fsmonitor daemon, a pager forced on by
// global config.
//
// MUTATION EVIDENCE: deleting `cmd.WaitDelay = gitWaitDelay` from Adapter.run
// makes this fail on BOTH assertions, and the first is the one worth having.
// Measured directly (go1.25 darwin/arm64): without WaitDelay the call returns
// after 30.01s with err == NIL — so the adapter does not just block, it then
// reports the orphan's partial output as a complete successful read. With
// WaitDelay it returns in 500ms with "exec: WaitDelay expired before I/O
// complete", which is not an *exec.ExitError and so is correctly a non-answer.
func TestOrphanHoldingStdoutIsANonAnswer(t *testing.T) {
	t.Parallel()

	start := time.Now()
	_, answered := withCmd(orphanHoldingStdout).GetHeadCommit("/tmp")
	elapsed := time.Since(start)

	if answered {
		t.Error("a read that never saw EOF was reported as an answer; whatever partial " +
			"output it collected would be published as the whole of it")
	}
	if limit := gitWaitDelay + 2*time.Second; elapsed > limit {
		t.Errorf("took %v; WaitDelay (%v) is supposed to bound exactly this, and the "+
			"ceiling cannot — the process the context knows about exited immediately",
			elapsed, gitWaitDelay)
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
	_, answered := withCmd(missingGit).GetGitRoot("/tmp")
	elapsed := time.Since(start)

	if answered {
		t.Error("a binary that does not exist was reported as an answer")
	}
	if elapsed > gitTimeout/2 {
		t.Errorf("took %v for a missing binary; the PATH lookup should fail before anything "+
			"is spawned, well inside the %v budget", elapsed, gitTimeout)
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
	_, answered := withCmd(floodingGit).RevertedCommits("/tmp")
	elapsed := time.Since(start)

	if answered {
		t.Error("a truncated read was reported as an answer; the surviving commits would " +
			"then be published as if they were all of them")
	}
	if limit := gitTimeout + gitWaitDelay + 2*time.Second; elapsed > limit {
		t.Errorf("took %v against a %v ceiling; the flood was not bounded", elapsed, gitTimeout)
	}
}

// TestUnderTheCapIsStillAnAnswer is the cap's vacuity guard: without it, a run
// that reported every read as truncated would satisfy the arm above. It also
// pins that the boundary is not off by one in the direction that blinds the
// adapter — a read of exactly the cap is not an overflow of it.
func TestUnderTheCapIsStillAnAnswer(t *testing.T) {
	t.Parallel()

	build := func(n int) gitCmd {
		return func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
			return exec.CommandContext(ctx, "/bin/sh", "-c", "head -c "+strconv.Itoa(n)+" /dev/zero")
		}
	}
	for _, n := range []int{1 << 10, gitMaxOutput - 1} {
		out, answered := withCmd(build(n)).run("/tmp", "irrelevant")
		if !answered {
			t.Errorf("%d bytes, under the %d-byte cap, was reported as a non-answer", n, gitMaxOutput)
		}
		if len(out) != n {
			t.Errorf("%d bytes written, %d kept", n, len(out))
		}
	}
}

// TestCappedBufferStopsAtItsLimit is the unit half of the arm above, so a
// change to gitMaxOutput's enforcement is caught without spawning `yes`.
func TestCappedBufferStopsAtItsLimit(t *testing.T) {
	var w cappedBuffer
	w.limit = 8

	if n, err := w.Write([]byte("abc")); n != 3 || err != nil {
		t.Fatalf("Write = (%d, %v), want (3, nil)", n, err)
	}
	if w.overflowed {
		t.Error("3 bytes into a limit of 8 reported an overflow")
	}
	// Reports the full length even past the limit: returning short would make
	// exec's copier treat it as a write error and fail the command for the
	// wrong reason.
	if n, err := w.Write([]byte("defghijkl")); n != 9 || err != nil {
		t.Fatalf("Write past the limit = (%d, %v), want (9, nil)", n, err)
	}
	if !w.overflowed {
		t.Error("writing past the limit did not record an overflow")
	}
	if len(w.buf) != 8 {
		t.Errorf("kept %d bytes, want the limit of 8", len(w.buf))
	}
	if string(w.buf) != "abcdefgh" {
		t.Errorf("kept %q, want the FIRST 8 bytes", w.buf)
	}
}

// TestProductionAdapterIsBounded is the wiring arm. Every test above builds an
// Adapter with an injected command, so none of them proves the adapter New()
// returns has a ceiling — which is the shape #1390 removed from the path
// confinement contract for the same reason. gitPath is a package var, so the
// production builder can be pointed at a real stalling binary without an
// injected gitCmd.
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

	saved := gitPath
	gitPath = stub
	t.Cleanup(func() { gitPath = saved })

	start := time.Now()
	_, answered := New().GetHeadCommit(t.TempDir())
	elapsed := time.Since(start)

	if answered {
		t.Error("the production adapter reported an answer from a child it had to kill")
	}
	if elapsed < gitTimeout/2 {
		t.Errorf("returned after %v, far under the %v ceiling — the stub cannot have run, "+
			"so this proves nothing about the production path", elapsed, gitTimeout)
	}
	if limit := gitTimeout + gitWaitDelay + 3*time.Second; elapsed > limit {
		t.Errorf("the production adapter took %v; New() is not running under gitTimeout", elapsed)
	}
}

// TestGitPathIsResolvedNotInherited pins go:S4036 — the adapter must not run
// whatever "git" the inherited PATH happens to resolve to.
func TestGitPathIsResolvedNotInherited(t *testing.T) {
	if gitPath == "git" {
		t.Skip("git is not under a trusted directory on this machine; pathutil fell back " +
			"to a PATH lookup, which is MustResolve's documented degradation")
	}
	if !filepath.IsAbs(gitPath) {
		t.Errorf("gitPath = %q, want an absolute path under a trusted directory", gitPath)
	}
	if strings.Contains(gitPath, "..") {
		t.Errorf("gitPath = %q contains a traversal", gitPath)
	}
}
