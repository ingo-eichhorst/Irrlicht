// hookport_test.go wires the Claude Code hook installer into the shared issue
// #1178 contract (the assertions live in contracttesting since #1216, so a
// third hook-installing adapter wires one call instead of porting this file),
// and keeps what is genuinely Claude-Code-only: the pre-#1161 curl→http
// migration, and the statusline feed, which no other adapter has.
package claudecode

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"irrlicht/core/internal/contracttesting"
	"irrlicht/core/pkg/daemonaddr"
)

func TestHookEndpointFollowsBindAddr(t *testing.T) {
	contracttesting.AssertHookEndpointFollowsBindAddr(t, contracttesting.HookInstaller{
		// withTempHome pins the bind address to the default as a side effect;
		// the contract sets its own value afterwards (see SettingsPath's doc).
		SettingsPath: func(t *testing.T) string {
			return filepath.Join(withTempHome(t), ".claude", "settings.json")
		},
		Sentinel: hookSentinel,
		Events:   installedHookEvents,
		Entry:    ourHookEntry,
		// Claude Code delivers natively over http (#1161), so the endpoint is
		// the entry's own url field rather than embedded in a shell command.
		EndpointOf: func(hook map[string]interface{}) string {
			url, _ := hook["url"].(string)
			return url
		},
		EnsureInstalled: EnsureHooksInstalled,
		Uninstall:       UninstallHooks,
	})
}

// seedSettings writes settings to the temp home's ~/.claude/settings.json.
func seedSettings(t *testing.T, home string, settings map[string]interface{}) string {
	t.Helper()
	path := filepath.Join(home, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(settings)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestEnsureHooksInstalled_UpgradesLegacyCurlOnAltPort pins that the pre-#1161
// curl form still migrates when the daemon is on a non-default port — the
// sentinel has to match a command carrying a *different* port than the one we
// are about to install. Claude-Code-only: Codex never had a delivery-shape
// migration, so this is not part of the shared contract.
func TestEnsureHooksInstalled_UpgradesLegacyCurlOnAltPort(t *testing.T) {
	home := withTempHomeOnPort(t, contracttesting.AltBindAddr)
	path := seedSettings(t, home, map[string]interface{}{
		"hooks": map[string]interface{}{
			HookPostToolUse: []interface{}{makeHookGroup(legacyMatcher, legacyCurlHookCommand)},
		},
	})

	if _, err := EnsureHooksInstalled(); err != nil {
		t.Fatal(err)
	}
	hooksMap, ok := readJSON(t, path)["hooks"].(map[string]interface{})
	if !ok {
		t.Fatalf("missing hooks map in %s", path)
	}
	groups, ok := hooksMap[HookPostToolUse].([]interface{})
	if !ok || len(groups) != 1 {
		t.Fatalf("expected exactly 1 %s matcher group (in-place upgrade, no append), got %v", HookPostToolUse, hooksMap[HookPostToolUse])
	}
	// assertHTTPEntry compares against hookEndpointURL(), which under the
	// alternate bind address is the :7838 form — so this pins the port too.
	assertHTTPEntry(t, groups[0].(map[string]interface{}))
}

func TestInstalledStatuslineCommand_FollowsBindAddr(t *testing.T) {
	t.Setenv(daemonaddr.EnvBindAddr, contracttesting.AltBindAddr)
	if got := installedStatuslineCommand(); !strings.Contains(got, "http://localhost:7838/api/v1/hooks/claudecode/statusline") {
		t.Errorf("statusline command = %q, want the resolved :7838 endpoint", got)
	}
}

// TestChainStatuslineCommand_RewritesDefaultPortStandalone: a bare :7837
// install is ours (port-independent sentinel), so it must be rewritten to the
// resolved port rather than mistaken for a third-party command and wrapped.
func TestChainStatuslineCommand_RewritesDefaultPortStandalone(t *testing.T) {
	t.Setenv(daemonaddr.EnvBindAddr, "")
	stale := installedStatuslineCommand() // what a :7837 daemon installed

	t.Setenv(daemonaddr.EnvBindAddr, contracttesting.AltBindAddr)
	got := chainStatuslineCommand(stale)
	if got != installedStatuslineCommand() {
		t.Errorf("expected rewrite to the resolved port, got %q", got)
	}
}

// TestChainStatuslineCommand_RepointsWrapPreservingUserCommand is the
// regression this issue's v3-unchain rework guards: a wrap written on one port
// must unwind structurally on another, so the user's own statusline command
// survives the repoint instead of being clobbered by the standalone install.
func TestChainStatuslineCommand_RepointsWrapPreservingUserCommand(t *testing.T) {
	user := "/usr/local/bin/my-statusline --foo"

	t.Setenv(daemonaddr.EnvBindAddr, "")
	wrappedOnDefault := chainStatuslineCommand(user)
	if !strings.Contains(wrappedOnDefault, ":7837/") {
		t.Fatalf("fixture should carry the default port, got %q", wrappedOnDefault)
	}

	t.Setenv(daemonaddr.EnvBindAddr, contracttesting.AltBindAddr)
	got := chainStatuslineCommand(wrappedOnDefault)
	if !strings.Contains(got, user) {
		t.Errorf("user command lost on repoint: %q", got)
	}
	if !strings.Contains(got, ":7838/") {
		t.Errorf("expected the resolved :7838 port after repoint, got %q", got)
	}
	if strings.Contains(got, ":7837/") {
		t.Errorf("stale :7837 endpoint survived the repoint: %q", got)
	}
	if u := unchainStatuslineCommand(got); u != user {
		t.Errorf("repointed wrap does not round-trip: got %q want %q", u, user)
	}
}

// TestChainStatuslineCommand_LeavesForeignTeePipelineWhole guards the other
// edge of the structural unchain: a third-party command that happens to look
// like a wrap but carries none of our sentinel must be wrapped whole, not
// mistaken for one of ours and truncated to its first half.
func TestChainStatuslineCommand_LeavesForeignTeePipelineWhole(t *testing.T) {
	t.Setenv(daemonaddr.EnvBindAddr, "")
	foreign := `tee >(/usr/local/bin/log) | curl -s https://example.invalid/statusline`

	got := chainStatuslineCommand(foreign)
	if !strings.Contains(got, foreign) {
		t.Errorf("foreign tee|curl pipeline was not preserved whole: %q", got)
	}
	if u := unchainStatuslineCommand(got); u != foreign {
		t.Errorf("wrapped foreign command does not round-trip: got %q want %q", u, foreign)
	}
}

// TestUninstallStatusline_RestoresUserCommandFromDefaultPortWrap pins the
// same structural unchain on the uninstall side.
func TestUninstallStatusline_RestoresUserCommandFromDefaultPortWrap(t *testing.T) {
	user := "/usr/local/bin/my-statusline --foo"

	home := withTempHome(t)
	wrappedOnDefault := chainStatuslineCommand(user)
	path := seedSettings(t, home, map[string]interface{}{
		"statusLine": map[string]interface{}{"type": "command", "command": wrappedOnDefault},
	})

	t.Setenv(daemonaddr.EnvBindAddr, contracttesting.AltBindAddr)
	modified, err := UninstallStatusline()
	if err != nil {
		t.Fatal(err)
	}
	if !modified {
		t.Fatal("expected modified=true uninstalling a :7837 wrap from a :7838 daemon")
	}
	if got := readStatuslineCommand(readJSON(t, path)); got != user {
		t.Errorf("statusLine.command = %q, want the user's original %q", got, user)
	}
}
