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

// ~/.claude.json cannot be held byte-exact across a run. It is shared with the
// Claude Code CLI, which rewrites caches, counters and per-project entries
// whenever any session on the machine does anything — and a recording machine
// has one running by definition. Live run 21 archived cleanly and still failed
// with `app-wide Desktop configuration changed: bytes, type, target, or mode
// differ at "/Users/ingo/.claude.json"`.
//
// What the driver CAN assert about that file is the thing worth asserting: it
// did not take anyone's project entry away, and the only entries it added name
// its own scratch workspace.
func TestUserConfigGuardAllowsForeignChurnButNotLosses(t *testing.T) {
	workspace := "/tmp/staging/2-1_basic-turn/cwd"
	before := []byte(`{"projects":{"/repo/a":{"x":1},"/repo/b":{"y":2}},"counter":1}`)

	// Another session churned a cache, updated its own project, and this run
	// added its workspace. All of that is somebody else's business, or ours.
	after := []byte(`{"projects":{"/repo/a":{"x":99},"/repo/b":{"y":2},` +
		`"/tmp/staging/2-1_basic-turn/cwd":{"z":3}},"counter":7}`)
	if err := verifyUserProjectEntries(before, after, workspace); err != nil {
		t.Fatalf("the guard refused ordinary Claude Code churn: %v", err)
	}

	// A project entry that disappeared is a real loss and must fail.
	lost := []byte(`{"projects":{"/repo/a":{"x":1}},"counter":1}`)
	err := verifyUserProjectEntries(before, lost, workspace)
	if err == nil || !strings.Contains(err.Error(), "/repo/b") {
		t.Fatalf("a lost project entry was accepted: %v", err)
	}

	// An added entry that is NOT this run's workspace is not ours to add.
	foreign := []byte(`{"projects":{"/repo/a":{"x":1},"/repo/b":{"y":2},"/repo/c":{"z":3}}}`)
	err = verifyUserProjectEntries(before, foreign, workspace)
	if err == nil || !strings.Contains(err.Error(), "/repo/c") {
		t.Fatalf("a foreign new project entry was accepted: %v", err)
	}

	// Unreadable input must fail, never pass quietly: a guard that cannot look
	// must not report the same thing as a guard that looked and found nothing.
	if err := verifyUserProjectEntries(before, []byte(`{not json`), workspace); err == nil {
		t.Fatal("the guard accepted a file it could not parse")
	}
	if err := verifyUserProjectEntries([]byte(`{not json`), after, workspace); err == nil {
		t.Fatal("the guard accepted a baseline it could not parse")
	}
}

// Claude Desktop's own config.json holds token caches, allowlist timestamps and
// window layout, all of which the app rewrites on its own schedule. Live run 22
// failed on it for the same reason run 21 failed on ~/.claude.json.
//
// The guard that survives churn and still catches a driver that wrecks the file
// is: no key may disappear.
func TestSharedConfigGuardCatchesLossesThroughChurn(t *testing.T) {
	before := []byte(`{"oauth:tokenCache":"abc","scale":0,"bootFrameLayout":{"w":1}}`)

	churn := []byte(`{"oauth:tokenCache":"abc","scale":0,"bootFrameLayout":{"w":2},"newKey":1}`)
	if err := verifyNoKeyLosses(before, churn, "Desktop configuration"); err != nil {
		t.Fatalf("the guard refused ordinary Desktop churn: %v", err)
	}

	truncated := []byte(`{"scale":0}`)
	err := verifyNoKeyLosses(before, truncated, "Desktop configuration")
	if err == nil || !strings.Contains(err.Error(), "oauth:tokenCache") {
		t.Fatalf("a lost key was accepted: %v", err)
	}

	if err := verifyNoKeyLosses(before, []byte(`{oops`), "Desktop configuration"); err == nil {
		t.Fatal("the guard accepted a file it could not parse")
	}
}
