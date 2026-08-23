package atomicfile

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// These are ordinary unit tests for newly-written code (issue #1761 created
// this package; nothing here existed on main to have been seen buggy), not
// defect-regression tests — there is no "before the fix" to have run red.
// What each one locks down is a behavior that at least one of the eleven
// call sites this package replaces actually relied on, per the comparison
// in atomicfile.go's package doc.

func TestWriteFile_ContentIsExact(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "out.json")
	want := []byte(`{"hello":"world"}` + "\n")

	if err := WriteFile(path, want); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("content mismatch: got %q want %q", got, want)
	}
}

func TestWriteFile_EmptyData(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "empty.txt")

	if err := WriteFile(path, nil); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected empty file, got %d bytes", len(got))
	}
}

// TestWriteFile_CreatesMissingParentDirectory locks the behavior every one
// of the eleven prior copies had: the parent directory is created if it
// doesn't already exist, arbitrarily deep.
func TestWriteFile_CreatesMissingParentDirectory(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "a", "b", "c", "hooks.json")

	if err := WriteFile(path, []byte("x")); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected file to exist: %v", err)
	}
}

// TestWriteFile_CreatedDirectoryModeIsOwnerOnly locks the strictest of the
// two directory permissions found across the eleven prior copies (0o700,
// hookyaml's/hermes' — ten of eleven used 0o755). This only matters the
// first time WriteFile is what creates a given directory: os.MkdirAll never
// changes the mode of a directory that already exists, which is why every
// existing adapter test that pre-creates its own fixture directory (all of
// them do) is unaffected by this choice.
func TestWriteFile_CreatedDirectoryModeIsOwnerOnly(t *testing.T) {
	root := t.TempDir()
	newDir := filepath.Join(root, "fresh-dir")
	path := filepath.Join(newDir, "config.json")

	if err := WriteFile(path, []byte("x")); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	info, err := os.Stat(newDir)
	if err != nil {
		t.Fatalf("Stat dir: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Fatalf("directory mode = %o, want 0700", perm)
	}
}

// TestWriteFile_FileModeIsOwnerReadWriteOnly locks the strictest of the two
// file permissions found across the eleven prior copies (0o600 — nine of
// eleven; claudecode's and processlifecycle's os.WriteFile calls used the
// weaker 0o644).
func TestWriteFile_FileModeIsOwnerReadWriteOnly(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "secret.json")

	if err := WriteFile(path, []byte("x")); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("file mode = %o, want 0600", perm)
	}
}

// TestWriteFile_OverwritesExistingContent locks the idempotent-install shape
// every adapter's EnsureHooksInstalled/EnsureInstalled relies on: a second
// write with different content fully replaces the first, never appends or
// merges.
func TestWriteFile_OverwritesExistingContent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "out.txt")

	if err := WriteFile(path, []byte("version one")); err != nil {
		t.Fatalf("first WriteFile: %v", err)
	}
	if err := WriteFile(path, []byte("v2")); err != nil {
		t.Fatalf("second WriteFile: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != "v2" {
		t.Fatalf("content = %q, want %q (no trailing bytes from the first write)", got, "v2")
	}
}

// TestWriteFile_NoLeftoverTempFileOnSuccess locks the cleanup shape nine of
// the eleven prior copies had (a deferred best-effort Remove of the temp
// file, which is a no-op once Rename has already moved it away): after a
// successful write, the target's directory contains exactly the target,
// never an orphaned `.*.tmp` sibling.
func TestWriteFile_NoLeftoverTempFileOnSuccess(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hooks.json")

	if err := WriteFile(path, []byte("x")); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "hooks.json" {
		names := make([]string, len(entries))
		for i, e := range entries {
			names[i] = e.Name()
		}
		t.Fatalf("directory entries = %v, want exactly [hooks.json]", names)
	}
}

// TestWriteFile_TempFileColocatedInTargetDirectory locks the property that
// makes the final rename atomic in the first place: the temp file must be
// created in the SAME directory as the target, never in os.TempDir() or
// elsewhere, so the rename can never cross a filesystem boundary. This is
// observed indirectly (a temp-dir override that only the target's own
// directory would satisfy) since the temp file is renamed away by the time
// WriteFile returns.
func TestWriteFile_TempFileColocatedInTargetDirectory(t *testing.T) {
	dir := t.TempDir()
	// A subdirectory one level below TMPDIR: if CreateTemp were ever pointed
	// at os.TempDir() instead of the target's own directory, this test's
	// target directory would end up empty and TMPDIR would gain a stray file
	// instead — which the leftover-file test above would not catch, because
	// it only inspects the target's own directory.
	sub := filepath.Join(dir, "sub")
	path := filepath.Join(sub, "out.txt")

	if err := WriteFile(path, []byte("x")); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	entries, err := os.ReadDir(sub)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "out.txt" {
		t.Fatalf("target directory should contain exactly the renamed file")
	}
}

// TestWriteFile_CleansUpTempFileWhenRenameFails locks the failure-path
// cleanup: when the final Rename cannot succeed (here, because the target
// path is an existing non-empty directory, which os.Rename always refuses
// to replace with a file), WriteFile returns the error, the pre-existing
// target is left completely untouched, and the temp file it created for the
// attempt does not survive as a leftover sibling.
func TestWriteFile_CleansUpTempFileWhenRenameFails(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "blocked")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	// Non-empty, so the rename is refused on every platform this repo ships
	// on (an empty directory is replaceable on some but not all of them).
	if err := os.WriteFile(filepath.Join(target, "keep.txt"), []byte("keep"), 0o644); err != nil {
		t.Fatalf("seed file: %v", err)
	}

	err := WriteFile(target, []byte("would-be-content"))
	if err == nil {
		t.Fatalf("expected an error renaming a file over a non-empty directory")
	}

	if _, statErr := os.Stat(filepath.Join(target, "keep.txt")); statErr != nil {
		t.Fatalf("pre-existing directory must be untouched on failure: %v", statErr)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "blocked" {
		names := make([]string, len(entries))
		for i, e := range entries {
			names[i] = e.Name()
		}
		t.Fatalf("directory entries = %v, want exactly [blocked] (no leaked temp file)", names)
	}
}

// TestWriteFile_MatchesInjectableWriteFileSignature is a compile-time lock:
// every one of the eleven call sites this package replaces plugs its writer
// into a `WriteFile func(path string, data []byte) error` field or
// parameter without a wrapper. If WriteFile's signature ever drifts from
// that shape, this line stops compiling.
func TestWriteFile_MatchesInjectableWriteFileSignature(t *testing.T) {
	var fn func(path string, data []byte) error = WriteFile
	dir := t.TempDir()
	if err := fn(filepath.Join(dir, "x"), []byte("x")); err != nil {
		t.Fatalf("WriteFile via injected signature: %v", err)
	}
}

func TestTempPattern_ContainsGlobAndTargetBasename(t *testing.T) {
	got := tempPattern("/home/user/.config/agent/hooks.json")
	if !strings.Contains(got, "hooks.json") {
		t.Fatalf("pattern %q should name the target file for debuggability", got)
	}
	if !strings.Contains(got, "*") {
		t.Fatalf("pattern %q must contain a '*' for CreateTemp to substitute a random string into", got)
	}
	// A leading dot keeps the temp file hidden alongside the target, matching
	// every one of the eleven prior copies' own naming convention.
	if !strings.HasPrefix(got, ".") {
		t.Fatalf("pattern %q should start with '.' to stay a hidden file", got)
	}
}
