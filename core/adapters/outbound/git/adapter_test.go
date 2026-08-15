package git

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// noBudget is the context this package's tests drive the adapter with where the
// caller has no aggregate deadline — the great majority of them, since what
// they grade is the adapter's OWN per-call ceiling, which run() derives from
// whatever it is handed (#1563). It is the test-side twin of
// services.noGitBudget, named for the same reason: an absence spelled out reads
// differently from one inferred. The budget-aware tests in shellout_test.go
// supply their own cancellable context instead.
func noBudget() context.Context { return context.Background() }

func TestNearestExistingDir(t *testing.T) {
	t.Run("existing dir returns itself", func(t *testing.T) {
		dir := t.TempDir()
		got := nearestExistingDir(dir)
		if got != dir {
			t.Errorf("got %q, want %q", got, dir)
		}
	})

	t.Run("deleted child resolves to parent", func(t *testing.T) {
		dir := t.TempDir()
		child := filepath.Join(dir, "deleted-child")
		got := nearestExistingDir(child)
		if got != dir {
			t.Errorf("got %q, want %q", got, dir)
		}
	})

	t.Run("deeply nested non-existent resolves to ancestor", func(t *testing.T) {
		dir := t.TempDir()
		deep := filepath.Join(dir, "a", "b", "c", "d")
		got := nearestExistingDir(deep)
		if got != dir {
			t.Errorf("got %q, want %q", got, dir)
		}
	})
}

func TestGetGitRoot_DeletedSubdir(t *testing.T) {
	dir := realPath(t, t.TempDir())
	if err := exec.Command("git", "init", dir).Run(); err != nil {
		t.Fatalf("git init: %v", err)
	}

	a := New()

	// Existing dir works as before.
	got, answered := a.GetGitRoot(noBudget(), dir)
	if !answered {
		t.Fatal("existing dir: git did not answer")
	}
	if got != dir {
		t.Errorf("existing dir: got %q, want %q", got, dir)
	}

	// Deleted subdir resolves to the same repo root.
	deleted := filepath.Join(dir, "nonexistent", "child")
	got, answered = a.GetGitRoot(noBudget(), deleted)
	if !answered {
		t.Fatal("deleted subdir: git did not answer")
	}
	if got != dir {
		t.Errorf("deleted subdir: got %q, want %q", got, dir)
	}
}

func TestGetGitRoot_NotARepo(t *testing.T) {
	dir := t.TempDir()
	a := New()
	got, answered := a.GetGitRoot(noBudget(), dir)
	if !answered {
		t.Fatal("a non-repo dir is an ANSWER — git ran and reported exit 128 (#1543)")
	}
	if got != "" {
		t.Errorf("non-repo dir: got %q, want empty", got)
	}
}

// realPath resolves symlinks (e.g. macOS /var → /private/var) so test
// comparisons match the absolute paths returned by git.
func realPath(t *testing.T, dir string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("EvalSymlinks(%q): %v", dir, err)
	}
	return resolved
}

func TestGetProjectName_DeletedWorktree(t *testing.T) {
	// Create a temp dir structure that mimics a repo with a deleted worktree.
	parent := t.TempDir()
	repoDir := filepath.Join(parent, "myproject")
	if err := os.Mkdir(repoDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := exec.Command("git", "init", repoDir).Run(); err != nil {
		t.Fatalf("git init: %v", err)
	}

	a := New()
	deleted := filepath.Join(repoDir, ".claude", "worktrees", "62")
	got, answered := a.GetProjectName(noBudget(), deleted)
	if !answered {
		t.Fatal("git did not answer")
	}
	if got != "myproject" {
		t.Errorf("got %q, want %q", got, "myproject")
	}
}

// gitInitForTest creates a fresh repo with an identity + signing disabled so
// commits/reverts don't prompt. Returns the symlink-resolved repo dir.
func gitInitForTest(t *testing.T) string {
	t.Helper()
	dir := realPath(t, t.TempDir())
	runGitForTest(t, dir, "init")
	runGitForTest(t, dir, "config", "user.email", "test@example.com")
	runGitForTest(t, dir, "config", "user.name", "Test")
	runGitForTest(t, dir, "config", "commit.gpgsign", "false")
	return dir
}

func runGitForTest(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func commitFileForTest(t *testing.T, dir, name, content string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	runGitForTest(t, dir, "add", name)
	runGitForTest(t, dir, "commit", "-m", "add "+name)
	return runGitForTest(t, dir, "rev-parse", "HEAD")
}

func TestGetHeadCommit(t *testing.T) {
	a := New()
	if got, answered := a.GetHeadCommit(noBudget(), t.TempDir()); got != "" || !answered {
		t.Errorf("non-repo dir: got (%q, %v), want (\"\", true)", got, answered)
	}
	dir := gitInitForTest(t)
	sha := commitFileForTest(t, dir, "a.txt", "hello")
	if got, answered := a.GetHeadCommit(noBudget(), dir); got != sha || !answered {
		t.Errorf("got (%q, %v), want (%q, true)", got, answered, sha)
	}
}

func TestRevertedCommits(t *testing.T) {
	a := New()
	dir := gitInitForTest(t)
	shaA := commitFileForTest(t, dir, "a.txt", "A")
	commitFileForTest(t, dir, "b.txt", "B")

	if got, answered := a.RevertedCommits(noBudget(), dir); len(got) != 0 || !answered {
		t.Fatalf("no reverts yet: got (%v, %v)", got, answered)
	}

	runGitForTest(t, dir, "revert", "--no-edit", shaA)
	got, answered := a.RevertedCommits(noBudget(), dir)
	if !answered {
		t.Fatal("git did not answer")
	}
	found := false
	for _, s := range got {
		if s == shaA {
			found = true
		}
	}
	if !found {
		t.Errorf("expected revert of %s in %v", shaA, got)
	}

	if r, answered := a.RevertedCommits(noBudget(), t.TempDir()); r != nil || !answered {
		t.Errorf("non-repo dir: want (nil, true), got (%v, %v)", r, answered)
	}
}

func TestGetCWDFromTranscript_WrappedCodex(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wrapped-codex.jsonl")
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create transcript: %v", err)
	}
	defer f.Close()

	enc := json.NewEncoder(f)
	lines := []map[string]interface{}{
		{
			"type": "session_meta",
			"payload": map[string]interface{}{
				"cwd": "/Users/test/original",
			},
		},
		{
			"type": "turn_context",
			"payload": map[string]interface{}{
				"cwd": "/Users/test/worktree",
			},
		},
		{
			"type": "response_item",
			"payload": map[string]interface{}{
				"type":      "function_call",
				"name":      "shell_command",
				"arguments": `{"command":["pwd"],"workdir":"/Users/test/override"}`,
			},
		},
	}
	for _, line := range lines {
		if err := enc.Encode(line); err != nil {
			t.Fatalf("encode transcript line: %v", err)
		}
	}

	a := New()
	got := a.GetCWDFromTranscript(path)
	if got != "/Users/test/override" {
		t.Errorf("got %q, want %q", got, "/Users/test/override")
	}
}

func TestListReleaseTags(t *testing.T) {
	a := New()

	if got, answered := a.ListReleaseTags(noBudget(), t.TempDir()); got != nil || !answered {
		t.Errorf("non-repo dir: want (nil, true), got (%v, %v)", got, answered)
	}

	dir := gitInitForTest(t)
	commitFileForTest(t, dir, "a.txt", "A")
	if got, answered := a.ListReleaseTags(noBudget(), dir); got != nil || !answered {
		t.Fatalf("no tags yet: want (nil, true), got (%v, %v)", got, answered)
	}

	runGitForTest(t, dir, "tag", "v0.1.0")
	runGitForTest(t, dir, "tag", "not-a-release") // filtered out
	commitFileForTest(t, dir, "b.txt", "B")
	runGitForTest(t, dir, "tag", "v0.2.0")

	got, answered := a.ListReleaseTags(noBudget(), dir)
	if !answered {
		t.Fatal("git did not answer")
	}
	if len(got) != 2 {
		t.Fatalf("got %d tags, want 2 (non-release tag must be filtered): %+v", len(got), got)
	}
	if got[0].Name != "v0.1.0" || got[1].Name != "v0.2.0" {
		t.Fatalf("got %+v, want v0.1.0 then v0.2.0 (creation order)", got)
	}
	if got[0].Epoch > got[1].Epoch {
		t.Fatalf("got %+v, want ascending epoch order", got)
	}
}

func TestCommitsInRange(t *testing.T) {
	a := New()

	if got, answered := a.CommitsInRange(noBudget(), t.TempDir(), "", "HEAD"); got != nil || !answered {
		t.Errorf("non-repo dir: want (nil, true), got (%v, %v)", got, answered)
	}

	dir := gitInitForTest(t)
	shaA := commitFileForTest(t, dir, "a.txt", "A")
	runGitForTest(t, dir, "tag", "v0.1.0")

	// Multi-line body, so the record/field separator parsing is exercised
	// against real newline-bearing commit messages, not just single-liners.
	if err := os.WriteFile(filepath.Join(dir, "b.txt"), []byte("B"), 0o644); err != nil {
		t.Fatalf("write b.txt: %v", err)
	}
	runGitForTest(t, dir, "add", "b.txt")
	runGitForTest(t, dir, "commit", "-m", "add b.txt\n\nFixes #123.")
	shaB := runGitForTest(t, dir, "rev-parse", "HEAD")
	runGitForTest(t, dir, "tag", "v0.2.0")

	oldest, answered := a.CommitsInRange(noBudget(), dir, "", "v0.1.0")
	if !answered {
		t.Fatal("git did not answer for the oldest tag")
	}
	if len(oldest) != 1 || oldest[0].Hash != shaA {
		t.Fatalf("oldest tag (no predecessor): got %+v, want just %s", oldest, shaA)
	}

	between, answered := a.CommitsInRange(noBudget(), dir, "v0.1.0", "v0.2.0")
	if !answered {
		t.Fatal("git did not answer for the range")
	}
	if len(between) != 1 || between[0].Hash != shaB {
		t.Fatalf("v0.1.0..v0.2.0: got %+v, want just %s", between, shaB)
	}
	if !strings.Contains(between[0].Body, "Fixes #123.") {
		t.Fatalf("multi-line body not preserved: %q", between[0].Body)
	}

	if got, answered := a.CommitsInRange(noBudget(), dir, "v0.2.0", "v0.2.0"); got != nil || !answered {
		t.Fatalf("empty range: want (nil, true), got (%v, %v)", got, answered)
	}
}

func TestTagContaining(t *testing.T) {
	a := New()

	if got, answered := a.TagContaining(noBudget(), t.TempDir(), "deadbeef"); got != "" || !answered {
		t.Errorf("non-repo dir: want (\"\", true), got (%q, %v)", got, answered)
	}

	dir := gitInitForTest(t)
	shaA := commitFileForTest(t, dir, "a.txt", "A")
	runGitForTest(t, dir, "tag", "v0.1.0")
	commitFileForTest(t, dir, "b.txt", "B")
	runGitForTest(t, dir, "tag", "v0.2.0")

	if got, answered := a.TagContaining(noBudget(), dir, shaA); got != "v0.1.0" || !answered {
		t.Errorf("got (%q, %v), want (v0.1.0, true) — earliest tag containing the first commit", got, answered)
	}
	// An object name git cannot resolve exits 129 (measured, git 2.50.1) —
	// still an ANSWER: git ran and told us no release contains it.
	if got, answered := a.TagContaining(noBudget(), dir, "0000000000000000000000000000000000000000"); got != "" || !answered {
		t.Errorf("unknown hash: want (\"\", true), got (%q, %v)", got, answered)
	}
}
