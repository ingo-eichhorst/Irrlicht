package git

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

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
	got := a.GetGitRoot(dir)
	if got != dir {
		t.Errorf("existing dir: got %q, want %q", got, dir)
	}

	// Deleted subdir resolves to the same repo root.
	deleted := filepath.Join(dir, "nonexistent", "child")
	got = a.GetGitRoot(deleted)
	if got != dir {
		t.Errorf("deleted subdir: got %q, want %q", got, dir)
	}
}

func TestGetGitRoot_NotARepo(t *testing.T) {
	dir := t.TempDir()
	a := New()
	got := a.GetGitRoot(dir)
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
	got := a.GetProjectName(deleted)
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
	if got := a.GetHeadCommit(t.TempDir()); got != "" {
		t.Errorf("non-repo dir: got %q, want empty", got)
	}
	dir := gitInitForTest(t)
	sha := commitFileForTest(t, dir, "a.txt", "hello")
	if got := a.GetHeadCommit(dir); got != sha {
		t.Errorf("got %q, want %q", got, sha)
	}
}

func TestRevertedCommits(t *testing.T) {
	a := New()
	dir := gitInitForTest(t)
	shaA := commitFileForTest(t, dir, "a.txt", "A")
	commitFileForTest(t, dir, "b.txt", "B")

	if got := a.RevertedCommits(dir); len(got) != 0 {
		t.Fatalf("no reverts yet: got %v", got)
	}

	runGitForTest(t, dir, "revert", "--no-edit", shaA)
	got := a.RevertedCommits(dir)
	found := false
	for _, s := range got {
		if s == shaA {
			found = true
		}
	}
	if !found {
		t.Errorf("expected revert of %s in %v", shaA, got)
	}

	if r := a.RevertedCommits(t.TempDir()); r != nil {
		t.Errorf("non-repo dir: want nil, got %v", r)
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
