package main

import (
	"bytes"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"irrlicht/core/adapters/inbound/agents/claudecode"
	"irrlicht/core/adapters/outbound/filesystem"
	"irrlicht/core/domain/permission"
)

// reverifyIntervalForTest is the hook re-verification cadence the child daemon
// runs at. Short enough that "advance past one interval" is a wait a test can
// afford, long enough that the loop is not hot-spinning on the config file for
// the seconds the test lives.
const reverifyIntervalForTest = time.Second

// reinstallWatchWindow is how long the test watches for the entries to come
// back — three re-verification intervals, because the failure it looks for is
// "the loop repaired them" and one missed tick would read as a pass. The
// interval cannot go below config's floor (that timer writes to the user's
// agent config files), so this is the shortest honest window available.
const reinstallWatchWindow = 3 * reverifyIntervalForTest

// TestUninstallHooksAgainstLiveDaemon is #1425's end-to-end regression test:
// `irrlichd --uninstall-hooks`, run by hand in a SECOND process while a daemon
// is live, must stick.
//
// Before the fix it did not. The CLI removed the entries and wrote "denied"
// into permissions.json, but the running daemon had read that store exactly
// once — at startup, in NewPermissionService — so its in-memory consent still
// said "granted". #1372's re-verification loop then found the entries missing,
// asked the stale gate, and repaired them within one interval. The user ran a
// command whose entire purpose is "remove these" and they came back.
//
// Deliberately end-to-end over two real processes rather than against a fake:
// the whole defect is that two processes disagree, which is precisely what a
// single-process test cannot express. Hermetic and parallel-safe on the same
// terms as TestDaemonStartupSmoke — HOME and IRRLICHT_HOME are fresh temp dirs,
// so it never touches the real ~/.claude or the production daemon — and the
// re-verify cadence is shortened via IRRLICHT_HOOK_REVERIFY_INTERVAL so the
// "advance past one interval" step is seconds rather than five minutes.
func TestUninstallHooksAgainstLiveDaemon(t *testing.T) {
	bin := buildIrrlichd(t)

	homeDir := t.TempDir()
	stateDir := shortTempDir(t)
	seedGrantedHooksConsent(t, stateDir)

	d := bootSmokeDaemonIn(t, bin, homeDir, stateDir,
		"IRRLICHT_HOOK_REVERIFY_INTERVAL="+reverifyIntervalForTest.String(),
		"PATH="+fakeClaudeCLIDir(t)+string(os.PathListSeparator)+os.Getenv("PATH"),
	)

	settings := filepath.Join(homeDir, ".claude", "settings.json")

	// Precondition, and the vacuity guard: a granted permission means the boot
	// install actually wrote the entries. Without this the assertions below
	// would pass against a daemon that never installed anything.
	if !pollUntil(t, 15*time.Second, 25*time.Millisecond, func() bool { return hookEntriesPresent(settings) }) {
		t.Fatalf("the daemon never installed the hook entries into %s at boot", settings)
	}

	// The defect's trigger: the flag, by hand, in a second process, against the
	// live daemon.
	runUninstallHooks(t, bin, homeDir, stateDir)

	if hookEntriesPresent(settings) {
		t.Fatalf("--uninstall-hooks left the entries in %s", settings)
	}

	// Advance past several re-verification intervals. THIS is the assertion the
	// issue is about: they must stay gone.
	if pollUntil(t, reinstallWatchWindow, reverifyIntervalForTest/4, func() bool { return hookEntriesPresent(settings) }) {
		t.Fatalf("the running daemon re-installed the hook entries within %s of --uninstall-hooks — "+
			"its in-memory consent is stale (#1425). %s now contains %q",
			reinstallWatchWindow, settings, readFileForMessage(settings))
	}

	// The mechanism, asserted directly: the LIVE daemon now reports the hooks
	// permission as denied. Separate from the behavioural assertion above on
	// purpose — if only this one fails, the signal did not land; if only the
	// one above fails, the signal landed but the loop repaired anyway.
	if got := livePermissionState(t, d.addr, claudecode.AdapterName, claudecode.PermissionKeyHooks); got != "denied" {
		t.Errorf("running daemon reports %s/%s = %q after --uninstall-hooks, want \"denied\" — "+
			"the daemon never reloaded consent from the store",
			claudecode.AdapterName, claudecode.PermissionKeyHooks, got)
	}

	d.shutdown(t)
}

// TestUninstallHooksWithNoDaemonRunning is a LOCK, not a defect test: it pins
// behaviour that must NOT change. `--uninstall-hooks` worked with no daemon
// running before #1425 and has to keep working — a consent-changed signal that
// cannot be delivered is not an error. It passes on main by construction.
func TestUninstallHooksWithNoDaemonRunning(t *testing.T) {
	bin := buildIrrlichd(t)

	homeDir := t.TempDir()
	stateDir := t.TempDir()
	seedGrantedHooksConsent(t, stateDir)

	cmd := exec.Command(bin, "--uninstall-hooks")
	cmd.Env = sanitizedChildEnv(homeDir, stateDir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("--uninstall-hooks with no daemon running exited %v, want success\n%s", err, out)
	}
	if !strings.Contains(string(out), "No running daemon to notify") {
		t.Errorf("output did not report the missing daemon:\n%s", out)
	}

	// The store must still record the opt-out (#570): an undeliverable signal
	// changes nothing about what gets persisted.
	if got := storedPermissionState(t, stateDir, claudecode.AdapterName, claudecode.PermissionKeyHooks); got != permission.StateDenied {
		t.Errorf("permissions.json records %s/%s = %q with no daemon running, want \"denied\"",
			claudecode.AdapterName, claudecode.PermissionKeyHooks, got)
	}
}

// runUninstallHooks runs the flag in a child process sharing the live daemon's
// HOME and IRRLICHT_HOME — how a user running it by hand reaches the same
// daemon, and how the CLI resolves the daemon's published address.
func runUninstallHooks(t *testing.T, bin, homeDir, stateDir string) {
	t.Helper()
	cmd := exec.Command(bin, "--uninstall-hooks")
	cmd.Env = sanitizedChildEnv(homeDir, stateDir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("--uninstall-hooks exited %v\n%s", err, out)
	}
	t.Logf("--uninstall-hooks output:\n%s", out)
}

// sanitizedChildEnv builds the environment for a child irrlichd, with every
// variable that could point it at a daemon OTHER than this test's explicitly
// neutralised.
//
// Inheriting os.Environ() wholesale is not hermetic, and both leaks were real.
// IRRLICHT_BIND_ADDR is first in daemonaddr's client ladder and wins over the
// addr file, so a developer with it set (ir:test-mac's separate mode exports
// 7838) sent the CLI's nudge to the wrong daemon and the live test failed for
// a reason unrelated to the code under test. IRRLICHT_PERMISSION_MODE=grant-all
// makes the daemon ignore the store entirely. The rest are cleared on the same
// principle rather than because a failure was observed: the child must be a
// function of what this test sets, not of the machine it runs on.
//
// Clearing rather than filtering, because os/exec keeps the LAST occurrence of
// a duplicate key — the same mechanism the HOME override already relies on.
func sanitizedChildEnv(homeDir, stateDir string) []string {
	return append(os.Environ(),
		"HOME="+homeDir,
		"IRRLICHT_HOME="+stateDir,
		"IRRLICHT_BIND_ADDR=",
		"IRRLICHT_PERMISSION_MODE=",
		// #1449: a developer with this exported would let a child daemon write
		// the real ~/.claude even under a temp HOME, if HOME ever leaked.
		"IRRLICHT_ALLOW_SHARED_CONFIG_WRITES=",
		// The per-agent home overrides ManagedUserFile.Path honors. Overriding
		// HOME is NOT enough to keep a child's managed files inside a temp home:
		// each of these relocates one of them on its own, so an ambient
		// XDG_CONFIG_HOME (a standard freedesktop variable) or CODEX_HOME points
		// the child at the developer's real config. Measured: with either set,
		// --print-managed-files under a temp HOME still names a path outside it.
		"CODEX_HOME=",
		"COPILOT_HOME=",
		"KIRO_HOME=",
		"XDG_CONFIG_HOME=",
		// Does not move claudeSettingsPath today; cleared for symmetry so the
		// next adapter that honors it does not reopen this quietly.
		"CLAUDE_CONFIG_DIR=",
		"IRRLICHT_DEMO_MODE=",
		"IRRLICHT_RECORD=",
		"IRRLICHT_HOOK_REVERIFY_INTERVAL=",
		"IRRLICHT_HOOK_SILENT_TURNS=",
	)
}

// shortTempDir is t.TempDir() with a short name, for a directory the daemon
// will put a unix socket in.
//
// t.TempDir() embeds the test's own name, and a long one pushes
// <dir>/irrlichd.sock past the 104-byte sun_path limit — the daemon then fails
// to listen with "bind: invalid argument" and never publishes its addr file,
// which surfaces as a 15s timeout waiting for a daemon that in fact died at
// startup. The name here is deliberately not derived from the test's.
func shortTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "ir")
	if err != nil {
		t.Fatalf("make short temp dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// waitForAddrFileTight polls for a non-empty addr file at addrPath with a
// 100µs cadence — deliberately tighter than waitForAddr's 20ms poll — so a
// caller racing a signal against the instant the file appears (#1808) loses
// as little of the window as possible to its own detection latency. Fails
// the test loudly on deadline rather than returning quietly: a fixture that
// cannot observe what it waits for must not read as a pass.
func waitForAddrFileTight(t *testing.T, addrPath string, deadline time.Time) {
	t.Helper()
	for {
		if b, err := os.ReadFile(addrPath); err == nil && strings.TrimSpace(string(b)) != "" {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("daemon never wrote addr file %s before the deadline", addrPath)
		}
		time.Sleep(100 * time.Microsecond)
	}
}

// seedGrantedHooksConsent writes a permissions.json granting Claude Code's
// hooks permission, so the daemon under test boots into the only state in
// which this defect can occur: consent granted, entries installed, a
// re-verification loop watching them.
func seedGrantedHooksConsent(t *testing.T, stateDir string) {
	t.Helper()
	seedGrantedConsent(t, stateDir, claudecode.PermissionKeyHooks)
}

// seedGrantedConsent writes a permissions.json granting one Claude Code
// permission, for a daemon that is about to boot against stateDir.
//
// Through the daemon's own store, not a hand-written JSON literal: the on-disk
// shape (the filename, the version field, the nesting) belongs to
// filesystem.PermissionStore and is documented as bumpable. A literal here would
// keep parsing as valid-but-empty after such a bump, and the caller would then
// fail at its vacuity guard complaining about the thing it seeded rather than
// about the schema.
//
// One helper rather than one per key: Save replaces the whole set, so the three
// live-daemon tests that need this were three byte-identical copies differing
// only in a constant.
func seedGrantedConsent(t *testing.T, stateDir, key string) {
	t.Helper()
	set := permission.Set{}
	set.Put(claudecode.AdapterName, key, permission.StateGranted)
	if err := filesystem.NewPermissionStore(stateDir).Save(set); err != nil {
		t.Fatalf("seed permissions store in %s: %v", stateDir, err)
	}
}

// fakeClaudeCLIDir returns a directory holding a stub `claude` that reports a
// version above the hooks install's declared floor.
//
// The install is version-gated (#1365) and the gate fails OPEN on a CLI it
// cannot see — so on a machine with no claude the install would proceed, and on
// one with an older claude it would refuse. That is a real difference in what
// the test exercises between a laptop and a CI runner, and it decides whether
// the boot install happens at all. Pinning the probe's answer removes the
// machine from the outcome.
func fakeClaudeCLIDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	script := "#!/bin/sh\necho '9999.0.0 (Claude Code)'\n"
	path := filepath.Join(dir, "claude")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write stub claude: %v", err)
	}
	return dir
}

// hookEntriesPresent reports whether ~/.claude/settings.json currently carries
// irrlicht's hook entries, matched on the endpoint path the installer itself
// uses as its sentinel so the two cannot drift.
func hookEntriesPresent(settingsPath string) bool {
	b, err := os.ReadFile(settingsPath)
	if err != nil {
		return false
	}
	return bytes.Contains(b, []byte(claudecode.HookEndpointPath))
}

// readFileForMessage returns a file's contents for a failure message, bounded
// so a large settings.json cannot bury the assertion that printed it.
func readFileForMessage(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return "<unreadable: " + err.Error() + ">"
	}
	if len(b) > 2000 {
		return string(b[:2000]) + "…"
	}
	return string(b)
}

// livePermissionState asks the RUNNING daemon what it currently believes about
// one permission — the in-memory set, not the file on disk.
func livePermissionState(t *testing.T, addr, agentName, key string) string {
	t.Helper()
	snap := fetchPermissionsSnapshot(t, http.DefaultClient, "http://"+addr+"/api/v1/permissions")
	for _, p := range snapshotAgent(snap, agentName).Permissions {
		if p.Key == key {
			return p.State
		}
	}
	return "<absent>"
}

// snapshotAgent picks one agent out of a permissions snapshot, or a zero value
// when it is absent — so callers loop once instead of nesting.
func snapshotAgent(snap permissionsSnapshot, agentName string) permissionsSnapshotAgent {
	for _, a := range snap.Agents {
		if a.Name == agentName {
			return a
		}
	}
	return permissionsSnapshotAgent{}
}

// storedPermissionState reads one permission's state straight out of the
// on-disk store.
func storedPermissionState(t *testing.T, stateDir, agentName, key string) permission.State {
	t.Helper()
	set, err := filesystem.NewPermissionStore(stateDir).Load()
	if err != nil {
		t.Fatalf("load permissions store from %s: %v", stateDir, err)
	}
	return set.Get(agentName, key)
}
