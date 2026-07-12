package services_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"irrlicht/core/adapters/outbound/git"
	"irrlicht/core/application/services"
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
	if _, err := services.ComputeDoraMetrics(git.New(), &doraFakeSessions{}, "", 0, 1); err == nil {
		t.Fatal("expected an error for an empty project")
	}
}

func TestComputeDoraMetrics_ProjectNotFound(t *testing.T) {
	result, err := services.ComputeDoraMetrics(git.New(), &doraFakeSessions{}, "no-such-project", 0, 1)
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
	result, err := services.ComputeDoraMetrics(git.New(), sessions, "proj", 0, 1<<62)
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

	result, err := services.ComputeDoraMetrics(git.New(), sessions, "proj", 0, 1<<62)
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
