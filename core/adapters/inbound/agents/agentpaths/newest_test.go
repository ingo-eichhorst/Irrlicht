package agentpaths

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestAbsRootResolvesRelativeDefaults(t *testing.T) {
	if got, err := AbsRoot("/already/absolute"); err != nil || got != "/already/absolute" {
		t.Errorf("AbsRoot(absolute) = %q, %v; want the path unchanged", got, err)
	}

	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory")
	}
	// The trap this exists to close: a $HOME-relative default (what FromEnv
	// returns when the override is unset) must not be walked as-is, or the
	// walk runs against the daemon's CWD and reports "nothing here".
	got, err := AbsRoot(".claude/projects")
	if err != nil {
		t.Fatalf("AbsRoot: %v", err)
	}
	if want := filepath.Join(home, ".claude/projects"); got != want {
		t.Errorf("AbsRoot(relative) = %q, want %q", got, want)
	}
}

func TestNewestFileWithSuffix(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	write := func(path string, age time.Duration) {
		t.Helper()
		if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
		when := time.Now().Add(-age)
		if err := os.Chtimes(path, when, when); err != nil {
			t.Fatalf("chtimes %s: %v", path, err)
		}
	}
	write(filepath.Join(root, "old.jsonl"), 2*time.Hour)
	write(filepath.Join(nested, "new.jsonl"), time.Minute)
	// A newer file of another type must not win — the suffix is the filter.
	write(filepath.Join(nested, "newest.txt"), 0)

	got := NewestFileWithSuffix(root, ".jsonl")
	if want := filepath.Join(nested, "new.jsonl"); got != want {
		t.Errorf("NewestFileWithSuffix = %q, want %q", got, want)
	}
}

func TestNewestFileWithSuffix_NoMatchIsEmpty(t *testing.T) {
	if got := NewestFileWithSuffix(t.TempDir(), ".jsonl"); got != "" {
		t.Errorf("got %q for an empty tree, want empty", got)
	}
	// A root that does not exist is the fresh-install case, and must read as
	// "nothing to report" rather than panicking or erroring out.
	if got := NewestFileWithSuffix(filepath.Join(t.TempDir(), "absent"), ".jsonl"); got != "" {
		t.Errorf("got %q for a missing root, want empty", got)
	}
}
