// hookport_test.go covers issue #1178: the endpoint the Codex hook installer
// writes is resolved from the daemon's bind address, and entries left behind
// by a daemon on a different port are still recognized as ours and rewritten
// in place rather than orphaned alongside a duplicate group.
package codex

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"irrlicht/core/pkg/daemonaddr"
)

const altBindAddr = "127.0.0.1:7838"

// legacyPortDeliveryCommand is the pre-#1178 hard-coded curl command, used to
// seed a fixture that simulates an install made by a daemon on :7837.
const legacyPortDeliveryCommand = "curl -fsS --max-time 1 -X POST --data-binary @- " +
	"http://localhost:7837/api/v1/hooks/codex || true"

// codexHomeOnPort points the installer at a temp $CODEX_HOME and pins the
// daemon bind address, so the resolved endpoint is the same whatever the
// developer's shell exports.
func codexHomeOnPort(t *testing.T, bindAddr string) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv(codexHomeEnvVar, home)
	t.Setenv(daemonaddr.EnvBindAddr, bindAddr)
	return home
}

// onlyHookCommand returns the command of the single inner hook in the event's
// single matcher group, failing on any other shape — an upgrade must not
// append a second group.
func onlyHookCommand(t *testing.T, home, event string) string {
	t.Helper()
	hooks := readHooks(t, home)
	arr, ok := hooks[event].([]interface{})
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
	cmd, _ := inner[0].(map[string]interface{})["command"].(string)
	return cmd
}

func seedCodexHooks(t *testing.T, home string, hooks map[string]interface{}) {
	t.Helper()
	path := filepath.Join(home, "hooks.json")
	data, err := json.Marshal(map[string]interface{}{"hooks": hooks})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestHookEndpointURL_FollowsBindAddr(t *testing.T) {
	t.Setenv(daemonaddr.EnvBindAddr, "")
	if got, want := hookEndpointURL(), "http://localhost:7837/api/v1/hooks/codex"; got != want {
		t.Errorf("default: hookEndpointURL() = %q, want %q", got, want)
	}
	if got, want := hookDeliveryCommand(), legacyPortDeliveryCommand; got != want {
		t.Errorf("default: hookDeliveryCommand() = %q, want %q", got, want)
	}

	t.Setenv(daemonaddr.EnvBindAddr, altBindAddr)
	if got, want := hookEndpointURL(), "http://localhost:7838/api/v1/hooks/codex"; got != want {
		t.Errorf("alt port: hookEndpointURL() = %q, want %q", got, want)
	}
}

// TestEnsureHooksInstalled_UsesResolvedPort is the core of #1178: a daemon on
// :7838 must install hooks that reach *it*, not whatever owns :7837. Without
// it, the onboarding factory's coexist recorder can never observe a Codex hook.
func TestEnsureHooksInstalled_UsesResolvedPort(t *testing.T) {
	home := codexHomeOnPort(t, altBindAddr)

	if _, err := EnsureHooksInstalled(); err != nil {
		t.Fatalf("EnsureHooksInstalled: %v", err)
	}
	for _, event := range installedHookEvents {
		cmd := onlyHookCommand(t, home, event)
		if !strings.Contains(cmd, ":7838/") {
			t.Errorf("event %s: command = %q, want the resolved :7838 port", event, cmd)
		}
	}

	modified, err := EnsureHooksInstalled()
	if err != nil {
		t.Fatalf("second EnsureHooksInstalled: %v", err)
	}
	if modified {
		t.Error("second install on the same port: got modified=true, want false")
	}
}

// TestEnsureHooksInstalled_UpgradesDefaultPortInstallInPlace is the
// back-compat requirement: an existing :7837 command must still be recognized
// as ours (the sentinel is port-independent) and rewritten in place.
func TestEnsureHooksInstalled_UpgradesDefaultPortInstallInPlace(t *testing.T) {
	home := codexHomeOnPort(t, altBindAddr)
	seedCodexHooks(t, home, map[string]interface{}{
		HookPostToolUse: []interface{}{
			map[string]interface{}{
				"matcher": hookMatcher,
				"hooks": []interface{}{
					map[string]interface{}{
						"type":    "command",
						"command": legacyPortDeliveryCommand,
						"timeout": hookTimeoutSeconds,
					},
				},
			},
		},
	})

	modified, err := EnsureHooksInstalled()
	if err != nil {
		t.Fatalf("EnsureHooksInstalled: %v", err)
	}
	if !modified {
		t.Fatal("expected modified=true when repointing a :7837 install at the resolved port")
	}
	if got, want := onlyHookCommand(t, home, HookPostToolUse), hookDeliveryCommand(); got != want {
		t.Errorf("command = %q, want %q", got, want)
	}
}

// TestUninstallHooks_RemovesDefaultPortInstall pins that uninstall is not
// port-scoped: a daemon on :7838 must still clean up entries written by one on
// :7837, or `irrlichd --uninstall-hooks` would leave junk behind.
func TestUninstallHooks_RemovesDefaultPortInstall(t *testing.T) {
	home := codexHomeOnPort(t, altBindAddr)
	seedCodexHooks(t, home, map[string]interface{}{
		HookStop: []interface{}{
			map[string]interface{}{
				"hooks": []interface{}{
					map[string]interface{}{"type": "command", "command": legacyPortDeliveryCommand},
				},
			},
		},
	})

	modified, err := UninstallHooks()
	if err != nil {
		t.Fatalf("UninstallHooks: %v", err)
	}
	if !modified {
		t.Fatal("expected modified=true removing a :7837 install from a :7838 daemon")
	}
	if eventHasSentinel(readHooks(t, home), HookStop) {
		t.Errorf("%s entry survived uninstall", HookStop)
	}
}
