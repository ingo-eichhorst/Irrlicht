package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"irrlicht/core/adapters/inbound/agents/claudecode"
)

// reverifyIntervalForTest is the hook re-verification cadence the child daemon
// runs at. Short enough that "advance past one interval" is a wait a test can
// afford, long enough that the loop is not hot-spinning on the config file for
// the seconds the test lives.
const reverifyIntervalForTest = 200 * time.Millisecond

// reinstallWatchWindow is how long the test watches for the entries to come
// back. Many multiples of the interval, because the failure it is looking for
// is "the loop repaired them" and one missed tick would read as a pass.
const reinstallWatchWindow = 3 * time.Second

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
	waitFor(t, 15*time.Second, "hook entries to be installed at boot", func() bool {
		return hookEntriesPresent(settings)
	})

	// The defect's trigger: the flag, by hand, in a second process, against the
	// live daemon.
	runUninstallHooks(t, bin, homeDir, stateDir)

	if hookEntriesPresent(settings) {
		t.Fatalf("--uninstall-hooks left the entries in %s", settings)
	}

	// Advance past several re-verification intervals. THIS is the assertion the
	// issue is about: they must stay gone.
	deadline := time.Now().Add(reinstallWatchWindow)
	for time.Now().Before(deadline) {
		if hookEntriesPresent(settings) {
			t.Fatalf("the running daemon re-installed the hook entries within %s of --uninstall-hooks — "+
				"its in-memory consent is stale (#1425). %s now contains %q",
				reinstallWatchWindow, settings, readFileForMessage(settings))
		}
		time.Sleep(reverifyIntervalForTest / 4)
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
	cmd.Env = append(os.Environ(),
		"HOME="+homeDir,
		"IRRLICHT_HOME="+stateDir,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("--uninstall-hooks with no daemon running exited %v, want success\n%s", err, out)
	}

	// The store must still record the opt-out (#570): an undeliverable signal
	// changes nothing about what gets persisted.
	if got := storedPermissionState(t, stateDir, claudecode.AdapterName, claudecode.PermissionKeyHooks); got != "denied" {
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
	cmd.Env = append(os.Environ(),
		"HOME="+homeDir,
		"IRRLICHT_HOME="+stateDir,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("--uninstall-hooks exited %v\n%s", err, out)
	}
	t.Logf("--uninstall-hooks output:\n%s", out)
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

// seedGrantedHooksConsent writes a permissions.json granting Claude Code's
// hooks permission, so the daemon under test boots into the only state in
// which this defect can occur: consent granted, entries installed, a
// re-verification loop watching them.
func seedGrantedHooksConsent(t *testing.T, stateDir string) {
	t.Helper()
	body := fmt.Sprintf(`{"version":1,"agents":{%q:{%q:"granted"}}}`+"\n",
		claudecode.AdapterName, claudecode.PermissionKeyHooks)
	path := filepath.Join(stateDir, "permissions.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("seed %s: %v", path, err)
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
	resp, err := http.Get("http://" + addr + "/api/v1/permissions")
	if err != nil {
		t.Fatalf("GET /api/v1/permissions: %v", err)
	}
	defer resp.Body.Close()
	var snap struct {
		Agents []permissionsSnapshotAgent `json:"agents"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&snap); err != nil {
		t.Fatalf("decode permissions snapshot: %v", err)
	}
	for _, a := range snap.Agents {
		if a.Name != agentName {
			continue
		}
		for _, p := range a.Permissions {
			if p.Key == key {
				return p.State
			}
		}
	}
	return "<absent>"
}

// storedPermissionState reads one permission's state straight out of the
// on-disk store.
func storedPermissionState(t *testing.T, stateDir, agentName, key string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(stateDir, "permissions.json"))
	if err != nil {
		t.Fatalf("read permissions.json: %v", err)
	}
	var file struct {
		Agents map[string]map[string]string `json:"agents"`
	}
	if err := json.Unmarshal(b, &file); err != nil {
		t.Fatalf("permissions.json is not valid JSON: %v\n%s", err, b)
	}
	return file.Agents[agentName][key]
}

// waitFor polls cond until it holds or the timeout expires, failing with what
// it was waiting for.
func waitFor(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("timed out after %s waiting for %s", timeout, strings.TrimSpace(what))
}
