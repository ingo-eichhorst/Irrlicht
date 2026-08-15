package services_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"irrlicht/core/adapters/outbound/git"
	"irrlicht/core/application/services"
	"irrlicht/core/domain/dora"
	"irrlicht/core/domain/session"
)

// doraFakeSessions is the minimal historySessionLister-shaped fake
// ComputeDoraMetrics needs: one session per project, pointing at a real
// temp git repo, so the whole adapter+domain+service pipeline is exercised
// end to end (mirrors the technique used to validate the original shell
// prototype against real git history).
type doraFakeSessions struct {
	states []*session.SessionState
}

func (f *doraFakeSessions) ListAll() ([]*session.SessionState, error) {
	return f.states, nil
}

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func doraTestRepo(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolve temp dir: %v", err)
	}
	runGit(t, dir, "init")
	runGit(t, dir, "config", "user.email", "test@example.com")
	runGit(t, dir, "config", "user.name", "Test")
	runGit(t, dir, "config", "commit.gpgsign", "false")
	return dir
}

func doraCommit(t *testing.T, dir, name, content, message string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	runGit(t, dir, "add", name)
	runGit(t, dir, "commit", "-m", message)
	return runGit(t, dir, "rev-parse", "HEAD")
}

// doraCommitAt backdates the commit's author/committer date to date (an
// RFC3339 string) — a lightweight tag's %(creatordate) reflects the
// tagged commit's date, so this is how tests space releases realistically
// apart (days, not the same wall-clock second git actually ran in), to
// isolate the hotfix-window signal from the revert signal being tested.
func doraCommitAt(t *testing.T, dir, name, content, message, date string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	runGit(t, dir, "add", name)
	cmd := exec.Command("git", "commit", "-m", message)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_DATE="+date, "GIT_COMMITTER_DATE="+date)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git commit: %v\n%s", err, out)
	}
	return runGit(t, dir, "rev-parse", "HEAD")
}

func TestComputeDoraMetrics_ProjectRequired(t *testing.T) {
	if _, err := services.ComputeDoraMetrics(context.Background(), git.New(), &doraFakeSessions{}, "", 0, 1); err == nil {
		t.Fatal("expected an error for an empty project")
	}
}

func TestComputeDoraMetrics_ProjectNotFound(t *testing.T) {
	result, err := services.ComputeDoraMetrics(context.Background(), git.New(), &doraFakeSessions{}, "no-such-project", 0, 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Available {
		t.Fatalf("expected Available=false, got %+v", result)
	}
}

func TestComputeDoraMetrics_NoReleasesYet(t *testing.T) {
	dir := doraTestRepo(t)
	doraCommit(t, dir, "a.txt", "A", "initial")

	sessions := &doraFakeSessions{states: []*session.SessionState{
		{SessionID: "s1", ProjectName: "proj", CWD: dir},
	}}
	result, err := services.ComputeDoraMetrics(context.Background(), git.New(), sessions, "proj", 0, 1<<62)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Available {
		t.Fatalf("expected Available=false with no tags yet, got %+v", result)
	}
}

// runGitAt runs a git subcommand (typically one that creates a commit,
// e.g. revert) with backdated author/committer dates, so its resulting
// commit's date is under test control rather than wall-clock "now".
func runGitAt(t *testing.T, dir, date string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_DATE="+date, "GIT_COMMITTER_DATE="+date)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestComputeDoraMetrics_EndToEnd(t *testing.T) {
	dir := doraTestRepo(t)

	// Releases spaced a month apart — well outside the 24h hotfix window —
	// so only the revert signal is expected to fire, isolating it from the
	// hotfix-window signal for this assertion.
	doraCommitAt(t, dir, "a.txt", "A", "initial", "2020-01-01T00:00:00")
	runGit(t, dir, "tag", "v0.1.0")

	buggy := doraCommitAt(t, dir, "b.txt", "B", "feat: risky change", "2020-02-01T00:00:00")
	runGit(t, dir, "tag", "v0.2.0")

	runGitAt(t, dir, "2020-03-01T00:00:00", "revert", "--no-edit", buggy)
	runGit(t, dir, "tag", "v0.3.0")

	doraCommitAt(t, dir, "c.txt", "C", "chore: unrelated", "2020-04-01T00:00:00")
	runGit(t, dir, "tag", "v0.4.0")

	sessions := &doraFakeSessions{states: []*session.SessionState{
		{SessionID: "s1", ProjectName: "proj", CWD: dir},
	}}

	result, err := services.ComputeDoraMetrics(context.Background(), git.New(), sessions, "proj", 0, 1<<62)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Available {
		t.Fatalf("expected Available=true, got %+v", result)
	}
	if !result.DeploymentFrequency.Available {
		t.Fatalf("expected DeploymentFrequency available, got %+v", result.DeploymentFrequency)
	}
	if !result.LeadTime.Available {
		t.Fatalf("expected LeadTime available, got %+v", result.LeadTime)
	}
	if !result.ChangeFailureRate.Available {
		t.Fatalf("expected ChangeFailureRate available, got %+v", result.ChangeFailureRate)
	}
	// v0.3.0 (ships the revert of v0.2.0's commit) must be the only flagged
	// release: 1 of 4 = 25%.
	if result.ChangeFailureRate.Value != 25 {
		t.Fatalf("ChangeFailureRate.Value = %v, want 25 (1 of 4 releases)", result.ChangeFailureRate.Value)
	}
	if !result.MTTR.Available {
		t.Fatalf("expected MTTR available, got %+v", result.MTTR)
	}
	// v0.2.0 (2020-02-01) -> v0.3.0 (2020-03-01) is a 29-day restore.
	wantHours := float64(29 * 24)
	if result.MTTR.Value != wantHours {
		t.Fatalf("MTTR.Value = %v, want %v (29 days)", result.MTTR.Value, wantHours)
	}
}

// doraRecordingProbe is a doraGitProbe that serves a fixed tag list and
// records every commit-range it is asked for. It is a fake rather than a real
// repo because what is under test is which git calls are MADE, and a real repo
// answers the same either way.
type doraRecordingProbe struct {
	root    string
	tags    []dora.TagInfo
	commits map[string][]dora.CommitInfo
	asked   []string // "<fromRef>..<toRef>" per CommitsInRange call, in order
}

func (p *doraRecordingProbe) GetGitRoot(context.Context, string) (string, bool) { return p.root, true }

func (p *doraRecordingProbe) ListReleaseTags(context.Context, string) ([]dora.TagInfo, bool) {
	return p.tags, true
}

func (p *doraRecordingProbe) CommitsInRange(_ context.Context, _, fromRef, toRef string) ([]dora.CommitInfo, bool) {
	p.asked = append(p.asked, fromRef+".."+toRef)
	return p.commits[toRef], true
}

func (p *doraRecordingProbe) TagContaining(context.Context, string, string) (string, bool) {
	return "", true
}

// TestComputeDoraMetrics_SkipsCommitRangesNoMetricReads is #1553's work bound.
// ComputeDoraMetrics used to walk the commits of EVERY release tag in the
// repository on every request, however narrow the window — and the oldest
// tag's range starts at the repo root, so on a project that adopted tags late
// that one call is a full-history walk paid for a chart spanning a week.
// Nothing read those commits: LeadTime and DetectReverts index commitsByTag
// through the domain's window filter (locked by
// TestNoMetricReadsAnOutOfWindowTagsCommits).
//
// MUTATION EVIDENCE: deleting the `if !dora.InWindow(...) { continue }` guard
// from ComputeDoraMetrics makes this fail, reporting the four ranges it walked
// against the two it needed.
func TestComputeDoraMetrics_SkipsCommitRangesNoMetricReads(t *testing.T) {
	const day = int64(86400)
	probe := &doraRecordingProbe{
		root: "/repo",
		tags: []dora.TagInfo{
			{Name: "v1.0", Epoch: 10 * day}, // before the window
			{Name: "v2.0", Epoch: 20 * day}, // before the window
			{Name: "v3.0", Epoch: 40 * day}, // in
			{Name: "v4.0", Epoch: 45 * day}, // in
			{Name: "v5.0", Epoch: 90 * day}, // after
		},
		commits: map[string][]dora.CommitInfo{
			"v3.0": {{Hash: "ccc", AuthorEpoch: 39 * day, Body: "work\n"}},
			"v4.0": {{Hash: "ddd", AuthorEpoch: 44 * day, Body: "more work\n"}},
		},
	}
	sessions := &doraFakeSessions{states: []*session.SessionState{
		{SessionID: "s1", ProjectName: "proj", CWD: "/repo/sub"},
	}}

	result, err := services.ComputeDoraMetrics(context.Background(), probe, sessions, "proj", 30*day, 50*day)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// The vacuity guard, and it is the important half: a service that fetched
	// NOTHING would satisfy the call-list assertion below while publishing
	// metrics over an empty sample.
	if !result.Available {
		t.Fatalf("expected Available=true, got %+v", result)
	}
	if result.LeadTime.SampleSize != 2 {
		t.Errorf("LeadTime.SampleSize = %d, want 2 — the in-window releases' commits were not read",
			result.LeadTime.SampleSize)
	}

	// The predecessor ref of an in-window tag is still used as the range's
	// START even when that predecessor is itself out of window: the range is
	// what shipped in v3.0, and that is bounded by v2.0 whether or not v2.0's
	// own commits are read.
	want := []string{"v2.0..v3.0", "v3.0..v4.0"}
	if !slices.Equal(probe.asked, want) {
		t.Errorf("walked %v, want %v — a commit range was fetched for a tag no metric reads "+
			"(the oldest one is a full-history walk), or an in-window range was skipped",
			probe.asked, want)
	}
}
