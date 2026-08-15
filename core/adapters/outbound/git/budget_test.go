package git

import (
	"context"
	"os/exec"
	"testing"
	"time"
)

// This file is the adapter's half of #1563. The operation-level half — one
// deadline over a whole sequence, and loops that check it themselves — is
// core/application/services/git_budget_test.go; what belongs HERE is the two
// properties the caller's budget only has because this adapter gives them to
// it: that a caller's deadline reaches the child at all, and that a call made
// under a spent one is a NON-answer rather than an empty one.
//
// The second is the load-bearing one and it is the whole reason an aggregate
// budget is cheap here. #1543 built this package around "git answered" versus
// "git was never asked"; a budget that abandoned a call and reported it as
// "git ran and found nothing" would put the collapse back, this time with
// DORA publishing a median over the ranges that fit inside the budget.

// TestReadUnderASpentBudgetIsANonAnswer drives every method that starts a child
// under a caller budget that is already gone.
//
// It uses the PRODUCTION adapter and a real repository, so what it grades is
// the whole path — run's context derivation, os/exec's refusal to start, and
// shellout.Answered's classification of an error that is not an *exec.ExitError
// at all. A stub could not: the distinction it asserts is precisely the one no
// faked return value can pin.
func TestReadUnderASpentBudgetIsANonAnswer(t *testing.T) {
	dir := gitInitForTest(t)
	sha := commitFileForTest(t, dir, "a.txt", "A")
	a := New()

	spent, cancel := context.WithCancel(context.Background())
	cancel()

	reads := []struct {
		name string
		call func(ctx context.Context) bool
	}{
		{"GetBranch", func(ctx context.Context) bool { _, ok := a.GetBranch(ctx, dir); return ok }},
		{"GetHeadCommit", func(ctx context.Context) bool { _, ok := a.GetHeadCommit(ctx, dir); return ok }},
		{"GetGitRoot", func(ctx context.Context) bool { _, ok := a.GetGitRoot(ctx, dir); return ok }},
		{"GetProjectName", func(ctx context.Context) bool { _, ok := a.GetProjectName(ctx, dir); return ok }},
		{"RevertedCommits", func(ctx context.Context) bool { _, ok := a.RevertedCommits(ctx, dir); return ok }},
		{"ListReleaseTags", func(ctx context.Context) bool { _, ok := a.ListReleaseTags(ctx, dir); return ok }},
		{"CommitsInRange", func(ctx context.Context) bool { _, ok := a.CommitsInRange(ctx, dir, "", "HEAD"); return ok }},
		{"TagContaining", func(ctx context.Context) bool { _, ok := a.TagContaining(ctx, dir, sha); return ok }},
	}

	for _, r := range reads {
		t.Run(r.name, func(t *testing.T) {
			if r.call(spent) {
				t.Errorf("%s reported that git ANSWERED under a spent aggregate budget. It was never asked — "+
					"os/exec fails before Start — and reporting an empty answer instead of a non-answer is the "+
					"#1543 collapse arriving through the #1563 budget: the caller then reads 'no releases', "+
					"'no reverts' or 'not a repo' as a fact about a repository nothing looked at", r.name)
			}
			// Vacuity guard, in the same sub-test so it cannot drift from the
			// claim: this repository DOES answer when the budget is live. Without
			// it the assertion above passes for a read that never works.
			if !r.call(noBudget()) {
				t.Errorf("%s did not answer for a real repository under a live budget — the assertion above "+
					"proves nothing if this read never answers at all", r.name)
			}
		})
	}
}

// TestRunTakesTheShorterOfTheCallersBudgetAndItsOwnCeiling pins what the ctx
// parameter did NOT change, which is the half a reviewer is most likely to
// assume rather than check.
//
// #1543's guarantee is per-CALL and it survives: a caller with no aggregate at
// all still gets gitTimeout/gitHistoryTimeout, because run derives its own
// deadline from whatever it is handed. #1563 adds the other direction — a
// caller whose budget is shorter wins, which is what makes an aggregate over a
// sequence a real bound rather than a suggestion. Both directions are asserted,
// because a run() that ignored the caller and a run() that ignored its own
// ceiling each satisfy exactly one of them.
func TestRunTakesTheShorterOfTheCallersBudgetAndItsOwnCeiling(t *testing.T) {
	var seen time.Time
	var bounded bool
	record := func(ctx context.Context, dir string, args ...string) *exec.Cmd {
		seen, bounded = ctx.Deadline()
		return exec.CommandContext(ctx, gitPath, args...)
	}

	// A caller budget far SHORTER than the ceiling: the caller wins.
	short, cancelShort := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancelShort()
	before := time.Now()
	withCeiling(record, 5*time.Second).GetBranch(short, "/tmp")
	if !bounded {
		t.Fatal("the child ran under a context with no deadline at all (#1543)")
	}
	if got := seen.Sub(before); got > time.Second {
		t.Errorf("under a 20ms caller budget the child's deadline was %v away, want <= the caller's: run derives "+
			"its ceiling from context.Background() again, so an aggregate over a sequence bounds nothing (#1563)", got)
	}

	// No caller budget at all: the adapter's OWN ceiling still applies.
	before = time.Now()
	withCeiling(record, 250*time.Millisecond).GetBranch(noBudget(), "/tmp")
	if !bounded {
		t.Fatal("with no caller budget the child ran unbounded: #1543's per-call guarantee is gone")
	}
	if got := seen.Sub(before); got > time.Second {
		t.Errorf("with no caller budget the child's deadline was %v away, want ~250ms (this adapter's own "+
			"ceiling): passing the caller's context through unchanged would leave every noGitBudget() call "+
			"unbounded (#1543)", got)
	}
}
