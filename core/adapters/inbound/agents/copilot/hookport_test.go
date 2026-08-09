// hookport_test.go wires the Copilot hook installer into the shared issue
// #1178 contract: the endpoint it writes follows the daemon's bind address, an
// entry left by a daemon on a different port is rewritten in place rather than
// duplicated, and uninstall is not port-scoped.
package copilot

import (
	"path/filepath"
	"testing"

	"irrlicht/core/internal/contracttesting"
	"irrlicht/core/pkg/daemonaddr"
)

// TestHookEntry_DefaultPortForm pins the exact object Copilot reads, which the
// shared contract deliberately does not look at — it only cares that the
// endpoint inside follows the bind address.
//
// Two things here are load-bearing and were both verified live against CLI
// 1.0.78. The type is "http", Copilot's native delivery, so nothing spawns a
// shell and curl need not be on PATH. And there is no timeout key: Copilot
// accepts the entry without one, and both installed events are dispatched
// fire-and-forget on its side, so there is nothing for a timeout to bound.
func TestHookEntry_DefaultPortForm(t *testing.T) {
	t.Setenv(daemonaddr.EnvBindAddr, "")
	entry := ourHookEntry()
	if got, want := entry["type"], "http"; got != want {
		t.Errorf("type = %v, want %v", got, want)
	}
	if got, want := entry["url"], "http://localhost:7837/api/v1/hooks/copilot"; got != want {
		t.Errorf("url = %v, want %v", got, want)
	}
	if _, ok := entry["timeout"]; ok {
		t.Error("entry carries a timeout key; Copilot dispatches these events fire-and-forget")
	}
	if _, ok := entry["timeoutSec"]; ok {
		t.Error("entry carries a timeoutSec key; Copilot dispatches these events fire-and-forget")
	}
}

func TestHookEndpointFollowsBindAddr(t *testing.T) {
	contracttesting.AssertHookEndpointFollowsBindAddr(t, contracttesting.HookInstaller{
		SettingsPath: func(t *testing.T) string {
			home := t.TempDir()
			t.Setenv(copilotHomeEnvVar, home)
			return filepath.Join(home, "hooks", "irrlicht.json")
		},
		Sentinel: hookSentinel,
		Events:   installedHookEvents,
		Entry:    ourHookEntry,
		// Copilot delivers natively over http, so the endpoint is the url
		// field rather than a substring of a shell command.
		EndpointOf: func(hook map[string]interface{}) string {
			url, _ := hook["url"].(string)
			return url
		},
		EnsureInstalled: EnsureHooksInstalled,
		Uninstall:       UninstallHooks,
	})
}
