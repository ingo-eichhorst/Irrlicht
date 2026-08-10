package main

import (
	"bufio"
	"bytes"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// This file is the end-to-end half of issue #1449: a grant-all daemon started
// by hand — the ordinary way to test anything consent-gated — must not rewrite
// the user's real agent configs.
//
// It boots a REAL irrlichd child in grant-all mode against a temp HOME and a
// temp IRRLICHT_HOME, exactly the shape a developer or the ir:test-mac
// "separate" mode produces, and asserts that every file the binary itself
// declares as managed comes back byte-identical.
//
// The file set is taken from `--print-managed-files` run against the same temp
// HOME rather than from a list written here, for the reason #1383 gives for the
// recorder doing the same: a hand-kept second list silently stops covering the
// next adapter that declares a managed file, and the failure it would then miss
// is invisible (the permission still reads "granted").
//
// Never point this at the real ~/.claude, ~/.codex or ~/.config/kitty:
// sanitizedChildEnv overrides HOME for the child, and every path asserted on
// below is derived from that temp HOME. assertUnderTempHome enforces it rather
// than trusting it.

// managedFileState is one declared managed file's contents before the daemon
// ran. exists distinguishes "absent" from "empty", which are different
// outcomes: creating a config the user never had is as much damage as
// rewriting one they did.
type managedFileState struct {
	path   string
	exists bool
	data   []byte
}

// printManagedFilesFor asks the built binary which shared user files it would
// write, with HOME pointed at the test's temp home so every path it names is
// inside it.
func printManagedFilesFor(t *testing.T, bin, homeDir, stateDir string) []string {
	t.Helper()
	cmd := exec.Command(bin, "--print-managed-files")
	cmd.Env = sanitizedChildEnv(homeDir, stateDir)
	// CombinedOutput, not Output: a non-zero exit otherwise reports a bare
	// "exit status 1" with the daemon's own explanation discarded.
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("--print-managed-files exited %v\n%s", err, out)
	}
	var paths []string
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		if p := strings.TrimSpace(sc.Text()); p != "" {
			paths = append(paths, p)
		}
	}
	if len(paths) == 0 {
		t.Fatal("--print-managed-files named no files — the test would assert nothing")
	}
	return paths
}

// assertUnderTempHome fails the test if any declared managed path escapes the
// temp home. This is the guard that keeps a bug in the test — or an adapter
// resolving a path from something other than HOME — from pointing the
// assertions, and the daemon, at the developer's real config.
func assertUnderTempHome(t *testing.T, paths []string, homeDir string) {
	t.Helper()
	for _, p := range paths {
		if escapesRoot(homeDir, p) {
			t.Fatalf("managed file %q resolves outside the test's temp HOME %q — refusing to run, "+
				"this test must never touch the real ~/.claude, ~/.codex or ~/.config/kitty", p, homeDir)
		}
	}
}

// escapesRoot reports whether path lies outside root. An unrelatable pair
// counts as escaping: this decides whether the test is allowed to proceed, so
// "cannot tell" must mean "stop".
func escapesRoot(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return true
	}
	return rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func readManagedFile(path string) managedFileState {
	st := managedFileState{path: path}
	if b, err := os.ReadFile(path); err == nil {
		st.exists, st.data = true, b
	}
	return st
}

func snapshotManagedFiles(paths []string) []managedFileState {
	states := make([]managedFileState, 0, len(paths))
	for _, p := range paths {
		states = append(states, readManagedFile(p))
	}
	return states
}

// TestGrantAllLeavesAgentConfigsAlone is the #1449 defect test.
//
// Seen red before the fix existed: the daemon rewrote
// <tempHOME>/.claude/settings.json (the sentinel) and created
// <tempHOME>/.claude/CLAUDE.md, <tempHOME>/.codex/hooks.json and
// <tempHOME>/.config/kitty/kitty.conf.
func TestGrantAllLeavesAgentConfigsAlone(t *testing.T) {
	bin := buildIrrlichd(t)
	// shortTempDir, not t.TempDir(): IRRLICHT_HOME holds the unix socket and a
	// long test name pushes sun_path past its 104-byte limit — see its comment.
	homeDir := shortTempDir(t)
	stateDir := shortTempDir(t)

	// A sentinel in the one file the incident actually damaged, so at least one
	// asserted path is in the "existed already" arm rather than all of them
	// being "must not be created". Valid JSON on purpose: an installer that
	// bailed on a malformed file would make this test pass for the wrong reason.
	sentinelPath := filepath.Join(homeDir, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(sentinelPath), 0o755); err != nil {
		t.Fatalf("seed sentinel dir: %v", err)
	}
	sentinel := []byte("{\n  \"sentinel\": \"issue-1449-do-not-touch\"\n}\n")
	if err := os.WriteFile(sentinelPath, sentinel, 0o644); err != nil {
		t.Fatalf("seed sentinel: %v", err)
	}

	paths := printManagedFilesFor(t, bin, homeDir, stateDir)
	assertUnderTempHome(t, paths, homeDir)
	if !containsPath(paths, sentinelPath) {
		t.Fatalf("--print-managed-files did not name %s; it named %v — the sentinel would prove nothing", sentinelPath, paths)
	}
	before := snapshotManagedFiles(paths)

	d := bootSmokeDaemonIn(t, bin, homeDir, stateDir, "IRRLICHT_PERMISSION_MODE=grant-all")
	snap := fetchPermissionsSnapshot(t, http.DefaultClient, "http://"+d.addr+"/api/v1/permissions")
	if snap.Mode != "grant-all" {
		t.Fatalf("daemon reports permission mode %q, want grant-all — the test did not exercise the defect", snap.Mode)
	}
	// kitty/remote-control, not claude-code/hooks: the kitty patch is the LAST
	// managed-file effect in consent-catalog order (consentCatalog appends
	// gastown, launcher and kitty after the adapter registry), so waiting for it
	// means every managed file this test asserts on has had its effect run.
	// Waiting on the FIRST one would leave the later assertions racing an effect
	// that had not happened yet — measured as sub-millisecond in practice
	// against a 20ms poll, but "too fast to lose" is not the same as ordered.
	waitForConsentEffect(t, homeDir, "kitty/remote-control", 15*time.Second)
	d.shutdown(t)

	for _, was := range before {
		assertManagedFileUnchanged(t, was)
	}
}

// assertManagedFileUnchanged compares one managed file against how it looked
// before the daemon ran. Creating a config the user never had is reported
// separately from rewriting one they did: they are different damage, and the
// incident produced both.
func assertManagedFileUnchanged(t *testing.T, was managedFileState) {
	t.Helper()
	now := readManagedFile(was.path)
	switch {
	case !was.exists && now.exists:
		t.Errorf("grant-all daemon CREATED the shared user file %s, which the user did not have:\n%s", was.path, now.data)
	case was.exists && !now.exists:
		t.Errorf("grant-all daemon DELETED the shared user file %s", was.path)
	case was.exists && !bytes.Equal(was.data, now.data):
		t.Errorf("grant-all daemon rewrote the shared user file %s\nbefore:\n%s\nafter:\n%s", was.path, was.data, now.data)
	}
}

// waitForConsentEffect blocks until the daemon has actually run the named
// permission's consent effect, by watching for the line runClosureEffect logs
// either way — "<agent>/<key>: applied" on success, "failed to apply
// <agent>/<key>: …" on a refusal.
//
// A completion signal is required, not optional. The daemon publishes its addr
// file BEFORE PermissionService.Start runs effects, so a test that boots,
// fetches a snapshot and shuts down races startup and can SIGTERM the daemon
// before the install it is trying to catch — which is exactly how this test
// first came back green against the unfixed binary. Watching the effect log is
// the one marker that exists in both the pre-fix and post-fix worlds.
//
// It fails the test rather than returning quietly on timeout: a completion
// signal that can be missed reads as a pass, which is the failure mode this
// whole file is about.
func waitForConsentEffect(t *testing.T, homeDir, agentSlashKey string, timeout time.Duration) {
	t.Helper()
	logPath := filepath.Join(homeDir, "Library", "Application Support", "Irrlicht", "logs", "events.log")
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(logPath); err == nil && strings.Contains(string(b), agentSlashKey) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("daemon never logged a consent effect for %s within %s (%s) — "+
		"the test cannot tell 'left the file alone' from 'was killed before writing it'",
		agentSlashKey, timeout, logPath)
}

func containsPath(paths []string, want string) bool {
	for _, p := range paths {
		if p == want {
			return true
		}
	}
	return false
}
