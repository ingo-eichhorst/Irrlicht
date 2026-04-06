package git

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
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
