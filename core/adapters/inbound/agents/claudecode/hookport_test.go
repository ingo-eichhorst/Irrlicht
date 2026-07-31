// hookport_test.go covers issue #1178: the endpoint the Claude Code hook and
// statusline installers write is resolved from the daemon's bind address, and
// entries left behind by a daemon on a different port are still recognized as
// ours and rewritten in place rather than orphaned or double-wrapped.
package claudecode

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const altBindAddr = "127.0.0.1:7838"

// legacyPortHookURL is the pre-#1178 hard-coded endpoint, used to seed
// fixtures that simulate an install made by a daemon on the default port.
const legacyPortHookURL = "http://localhost:7837/api/v1/hooks/claudecode"

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

// onlyHookURL returns the url of the single inner hook in the event's single
// matcher group, failing if the shape is anything else — which is the point:
// an upgrade must not append a second group.
func onlyHookURL(t *testing.T, path, event string) string {
	t.Helper()
	settings := readJSON(t, path)
	hooksMap, ok := settings["hooks"].(map[string]interface{})
	if !ok {
		t.Fatalf("missing hooks map in %s", path)
	}
	arr, ok := hooksMap[event].([]interface{})
	if !ok {
		t.Fatalf("missing %s array", event)
	}
	if len(arr) != 1 {
		t.Fatalf("expected exactly 1 %s matcher group (in-place upgrade, no append), got %d", event, len(arr))
	}
	group := arr[0].(map[string]interface{})
	inner, ok := group["hooks"].([]interface{})
	if !ok || len(inner) != 1 {
		t.Fatalf("expected exactly 1 inner hook, got %v", group["hooks"])
	}
	url, _ := inner[0].(map[string]interface{})["url"].(string)
	return url
}

func TestHookEndpointURL_FollowsBindAddr(t *testing.T) {
	t.Setenv("IRRLICHT_BIND_ADDR", "")
	if got, want := hookEndpointURL(), legacyPortHookURL; got != want {
		t.Errorf("default: hookEndpointURL() = %q, want %q", got, want)
	}
	t.Setenv("IRRLICHT_BIND_ADDR", altBindAddr)
	if got, want := hookEndpointURL(), "http://localhost:7838/api/v1/hooks/claudecode"; got != want {
		t.Errorf("alt port: hookEndpointURL() = %q, want %q", got, want)
	}
}

// TestEnsureHooksInstalled_UsesResolvedPort is the core of #1178: a daemon on
// :7838 must install hooks that reach *it*, not whatever owns :7837.
func TestEnsureHooksInstalled_UsesResolvedPort(t *testing.T) {
	home := withTempHomeOnPort(t, altBindAddr)

	if _, err := EnsureHooksInstalled(); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(home, ".claude", "settings.json")
	settings := readJSON(t, path)
	hooksMap := settings["hooks"].(map[string]interface{})
	for _, event := range installedHookEvents {
		arr, ok := hooksMap[event].([]interface{})
		if !ok || len(arr) != 1 {
			t.Fatalf("event %s: expected 1 matcher group, got %v", event, hooksMap[event])
		}
		assertHTTPEntry(t, arr[0].(map[string]interface{}))
		if url := onlyHookURL(t, path, event); !strings.Contains(url, ":7838/") {
			t.Errorf("event %s: url = %q, want the resolved :7838 port", event, url)
		}
	}

	// Idempotent on the same port.
	modified, err := EnsureHooksInstalled()
	if err != nil {
		t.Fatal(err)
	}
	if modified {
		t.Error("second install on the same port: got modified=true, want false")
	}
}

// TestEnsureHooksInstalled_UpgradesDefaultPortInstallInPlace is the
// back-compat requirement: an existing :7837 entry must still be recognized as
// ours (the sentinel is port-independent) and rewritten in place to the
// resolved port — not left orphaned with a duplicate group appended.
func TestEnsureHooksInstalled_UpgradesDefaultPortInstallInPlace(t *testing.T) {
	home := withTempHomeOnPort(t, altBindAddr)
	path := seedSettings(t, home, map[string]interface{}{
		"hooks": map[string]interface{}{
			HookPostToolUse: []interface{}{
				map[string]interface{}{
					"matcher": hookMatcher,
					"hooks": []interface{}{
						map[string]interface{}{
							"type":    "http",
							"url":     legacyPortHookURL,
							"timeout": hookTimeoutSeconds,
						},
					},
				},
			},
		},
	})

	modified, err := EnsureHooksInstalled()
	if err != nil {
		t.Fatal(err)
	}
	if !modified {
		t.Fatal("expected modified=true when repointing a :7837 install at the resolved port")
	}
	if got, want := onlyHookURL(t, path, HookPostToolUse), hookEndpointURL(); got != want {
		t.Errorf("url = %q, want %q", got, want)
	}
}

// TestEnsureHooksInstalled_UpgradesLegacyCurlOnAltPort pins that the pre-#1161
// curl form still migrates when the daemon is on a non-default port — the
// sentinel has to match a command carrying a *different* port than the one we
// are about to install.
func TestEnsureHooksInstalled_UpgradesLegacyCurlOnAltPort(t *testing.T) {
	home := withTempHomeOnPort(t, altBindAddr)
	path := seedSettings(t, home, map[string]interface{}{
		"hooks": map[string]interface{}{
			HookPostToolUse: []interface{}{makeHookGroup(legacyMatcher, legacyCurlHookCommand)},
		},
	})

	if _, err := EnsureHooksInstalled(); err != nil {
		t.Fatal(err)
	}
	if got, want := onlyHookURL(t, path, HookPostToolUse), hookEndpointURL(); got != want {
		t.Errorf("url = %q, want %q", got, want)
	}
}

// TestUninstallHooks_RemovesDefaultPortInstall pins that uninstall is not
// port-scoped: a daemon on :7838 must still clean up entries written by one on
// :7837, or `irrlichd --uninstall-hooks` would leave junk behind.
func TestUninstallHooks_RemovesDefaultPortInstall(t *testing.T) {
	home := withTempHomeOnPort(t, altBindAddr)
	path := seedSettings(t, home, map[string]interface{}{
		"hooks": map[string]interface{}{
			HookPostToolUse: []interface{}{
				map[string]interface{}{
					"matcher": hookMatcher,
					"hooks": []interface{}{
						map[string]interface{}{"type": "http", "url": legacyPortHookURL},
					},
				},
			},
		},
	})

	modified, err := UninstallHooks()
	if err != nil {
		t.Fatal(err)
	}
	if !modified {
		t.Fatal("expected modified=true removing a :7837 install from a :7838 daemon")
	}
	settings := readJSON(t, path)
	hooksMap, _ := settings["hooks"].(map[string]interface{})
	if _, still := hooksMap[HookPostToolUse]; still {
		t.Errorf("%s entry survived uninstall: %v", HookPostToolUse, hooksMap[HookPostToolUse])
	}
}

func TestInstalledStatuslineCommand_FollowsBindAddr(t *testing.T) {
	t.Setenv("IRRLICHT_BIND_ADDR", altBindAddr)
	if got := installedStatuslineCommand(); !strings.Contains(got, "http://localhost:7838/api/v1/hooks/claudecode/statusline") {
		t.Errorf("statusline command = %q, want the resolved :7838 endpoint", got)
	}
}

// TestChainStatuslineCommand_RewritesDefaultPortStandalone: a bare :7837
// install is ours (port-independent sentinel), so it must be rewritten to the
// resolved port rather than mistaken for a third-party command and wrapped.
func TestChainStatuslineCommand_RewritesDefaultPortStandalone(t *testing.T) {
	t.Setenv("IRRLICHT_BIND_ADDR", altBindAddr)
	stale := "curl -fsS --max-time 1 -X POST --data-binary @- " +
		"http://localhost:7837/api/v1/hooks/claudecode/statusline >/dev/null 2>&1 || true"

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

	t.Setenv("IRRLICHT_BIND_ADDR", "")
	wrappedOnDefault := chainStatuslineCommand(user)
	if !strings.Contains(wrappedOnDefault, ":7837/") {
		t.Fatalf("fixture should carry the default port, got %q", wrappedOnDefault)
	}

	t.Setenv("IRRLICHT_BIND_ADDR", altBindAddr)
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

// TestUninstallStatusline_RestoresUserCommandFromDefaultPortWrap pins the
// same structural unchain on the uninstall side.
func TestUninstallStatusline_RestoresUserCommandFromDefaultPortWrap(t *testing.T) {
	user := "/usr/local/bin/my-statusline --foo"

	home := withTempHome(t)
	wrappedOnDefault := chainStatuslineCommand(user)
	path := seedSettings(t, home, map[string]interface{}{
		"statusLine": map[string]interface{}{"type": "command", "command": wrappedOnDefault},
	})

	t.Setenv("IRRLICHT_BIND_ADDR", altBindAddr)
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
