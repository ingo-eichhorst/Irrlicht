package desktopdriver

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTreeSnapshotDetectsConcurrentConfigMutation(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "claude_desktop_config.json")
	if err := os.WriteFile(file, []byte(`{"mcpServers":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	snapshot, err := CaptureTreeSnapshot([]string{file})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(`{"mcpServers":{"external":{}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	err = VerifyTreeSnapshot(snapshot)
	if err == nil || !strings.Contains(err.Error(), "configuration changed") || !strings.Contains(err.Error(), file) {
		t.Fatalf("VerifyTreeSnapshot() error = %v", err)
	}
}

func TestTreeSnapshotDetectsCreationUnderAbsentRoot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "plugins")
	snapshot, err := CaptureTreeSnapshot([]string{root})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	err = VerifyTreeSnapshot(snapshot)
	if err == nil || !strings.Contains(err.Error(), "configuration changed") {
		t.Fatalf("VerifyTreeSnapshot() error = %v", err)
	}
}

func TestTreeSnapshotDoesNotFollowSymlinks(t *testing.T) {
	root := t.TempDir()
	external := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(external, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "plugin")
	if err := os.Symlink(external, link); err != nil {
		t.Fatal(err)
	}
	snapshot, err := CaptureTreeSnapshot([]string{root})
	if err != nil {
		t.Fatal(err)
	}
	if got := snapshot[link]; got.Kind != "symlink" || got.Digest != "" {
		t.Fatalf("symlink snapshot = %+v", got)
	}
}

func TestTreeSnapshotSymlinkDoesNotHideLaterSiblingMutation(t *testing.T) {
	root := t.TempDir()
	external := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(external, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, filepath.Join(root, "a-link")); err != nil {
		t.Fatal(err)
	}
	later := filepath.Join(root, "z-settings.json")
	if err := os.WriteFile(later, []byte("before"), 0o600); err != nil {
		t.Fatal(err)
	}
	snapshot, err := CaptureTreeSnapshot([]string{root})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := snapshot[later]; !ok {
		t.Fatal("the symlink mutation hid its later sibling from the baseline")
	}
	if err := os.WriteFile(later, []byte("after"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := VerifyTreeSnapshot(snapshot); err == nil || !strings.Contains(err.Error(), later) {
		t.Fatalf("VerifyTreeSnapshot() error = %v; want later sibling mutation", err)
	}
}
