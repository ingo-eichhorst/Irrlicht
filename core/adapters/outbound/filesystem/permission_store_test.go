package filesystem

import (
	"os"
	"path/filepath"
	"testing"

	"irrlicht/core/domain/permission"
)

func TestPermissionStoreMissingFileIsEmptySet(t *testing.T) {
	s := NewPermissionStore(t.TempDir())
	set, err := s.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(set) != 0 {
		t.Fatalf("expected empty set, got %v", set)
	}
	// Empty set means everything pending — the consent-first migration.
	if got := set.Get("claude-code", "hooks"); got != permission.StatePending {
		t.Fatalf("Get = %q, want pending", got)
	}
}

func TestPermissionStoreRoundTrip(t *testing.T) {
	dir := t.TempDir()
	s := NewPermissionStore(dir)

	set := permission.Set{}
	set.Put("claude-code", "hooks", permission.StateGranted)
	set.Put("claude-code", "statusline", permission.StateDenied)
	set.Put("codex", "transcripts", permission.StateGranted)
	if err := s.Save(set); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// No stray temp file left behind by the atomic write.
	if leftovers, _ := filepath.Glob(filepath.Join(dir, "permissions.json.tmp*")); len(leftovers) > 0 {
		t.Fatalf("temp files left behind: %v", leftovers)
	}

	loaded, err := NewPermissionStore(dir).Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := loaded.Get("claude-code", "hooks"); got != permission.StateGranted {
		t.Fatalf("hooks = %q, want granted", got)
	}
	if got := loaded.Get("claude-code", "statusline"); got != permission.StateDenied {
		t.Fatalf("statusline = %q, want denied", got)
	}
	if got := loaded.Get("codex", "transcripts"); got != permission.StateGranted {
		t.Fatalf("codex = %q, want granted", got)
	}
	// Unanswered pair still pending after round-trip.
	if got := loaded.Get("pi", "transcripts"); got != permission.StatePending {
		t.Fatalf("pi = %q, want pending", got)
	}
}

func TestPermissionStoreCorruptFileErrors(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "permissions.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewPermissionStore(dir).Load(); err == nil {
		t.Fatal("expected error on corrupt file")
	}
}
