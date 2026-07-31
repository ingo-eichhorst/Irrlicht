// hookport_test.go wires the Codex hook installer into the shared issue #1178
// contract: the endpoint it writes follows the daemon's bind address, an entry
// left by a daemon on a different port is rewritten in place rather than
// duplicated, and uninstall is not port-scoped. The assertions themselves live
// in contracttesting so a third hook-installing adapter wires one call instead
// of porting this file (issue #1216).
package codex

import (
	"path/filepath"
	"testing"

	"irrlicht/core/internal/contracttesting"
	"irrlicht/core/pkg/daemonaddr"
)

// TestHookDeliveryCommand_DefaultPortForm pins the exact curl invocation Codex
// execs, which the shared contract deliberately does not look at — it only
// cares that the endpoint inside follows the bind address. The flags are load
// bearing: -fsS keeps a failed POST quiet, --max-time bounds a wedged daemon,
// --data-binary @- streams the payload from stdin, and `|| true` stops a
// delivery failure from failing the user's turn.
func TestHookDeliveryCommand_DefaultPortForm(t *testing.T) {
	t.Setenv(daemonaddr.EnvBindAddr, "")
	const want = "curl -fsS --max-time 1 -X POST --data-binary @- " +
		"http://localhost:7837/api/v1/hooks/codex || true"
	if got := hookDeliveryCommand(); got != want {
		t.Errorf("hookDeliveryCommand() = %q, want %q", got, want)
	}
}

func TestHookEndpointFollowsBindAddr(t *testing.T) {
	contracttesting.AssertHookEndpointFollowsBindAddr(t, contracttesting.HookInstaller{
		SettingsPath: func(t *testing.T) string {
			home := t.TempDir()
			t.Setenv(codexHomeEnvVar, home)
			return filepath.Join(home, "hooks.json")
		},
		Sentinel:   hookSentinel,
		Events:     installedHookEvents,
		MatcherFor: matcherForEvent,
		Entry:      ourHookEntry,
		// Codex delivers via a curl `type: command` entry, so the endpoint is
		// embedded in the command string rather than carried in a url field.
		EndpointOf: func(hook map[string]interface{}) string {
			cmd, _ := hook["command"].(string)
			return cmd
		},
		EnsureInstalled: EnsureHooksInstalled,
		Uninstall:       UninstallHooks,
	})
}
