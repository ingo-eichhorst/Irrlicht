// hook_endpoint_addressfree_test.go exercises AssertHookEndpointFollowsBindAddr's
// DeliveryAddressFree mode against a reference beacon installer, and is the
// wiring the first beacon-delivered adapter copies (#1453).
//
// It lives here rather than in an adapter because no adapter installs the beacon
// yet: #1373 built the mechanism and adoption is a separate change per adapter,
// so a mode with no call site would be a branch nothing ever runs — which is
// precisely the "exemption with no test behind it" the contract-family
// convention exists to prevent. It is the same split tools/lib's linter suites
// draw: assertions are pinned against a purpose-built fixture, so they do not
// move when a real adapter is edited.
//
// The installer below is deliberately the SHAPE an adapter should have rather
// than the smallest thing that passes. Note in particular that
// referenceBeaconConfig returns a hookjson.Config and not a (Config, error):
// the binary path is resolved ONCE, at install time, so the config builder and
// its four callers stay infallible. Resolving it per-config instead is what
// measured +12 lines of error branching when copilot evaluated beacon delivery.
package contracttesting

import (
	"os"
	"path/filepath"
	"testing"

	"irrlicht/core/adapters/inbound/agents/hookjson"
	"irrlicht/core/pkg/hookbeacon"
)

// referenceBeaconAdapter is the route segment the beacon posts to. A real
// adapter exports this as one constant shared with registerHookRoutes.
const referenceBeaconAdapter = "reference-beacon"

// referenceBeaconHomeEnv relocates the reference installer's config file, the
// way CODEX_HOME and COPILOT_HOME relocate the real ones.
const referenceBeaconHomeEnv = "IRRLICHT_TEST_REFERENCE_BEACON_HOME"

// referenceForeignBinary is the irrlichd a differently-situated daemon would
// have named. It is absolute (Command refuses a relative path) and does not
// exist, which is the drift the beacon newly admits: an absolute path that has
// stopped existing fails exactly as quietly as a curl that was never on PATH.
const referenceForeignBinary = "/nonexistent-irrlicht-1453/bin/irrlichd"

var referenceBeaconEvents = []string{"BeforeTool", "Stop"}

// referenceBeaconCommand is resolved once, at package init, standing in for an
// adapter resolving it once at install time. The error is kept beside it rather
// than swallowed so the contract test can fail on it explicitly instead of
// silently grading an empty command.
var referenceBeaconCommand, referenceBeaconCommandErr = hookbeacon.InstalledCommand(referenceBeaconAdapter)

func referenceBeaconSettingsPath() string {
	return filepath.Join(os.Getenv(referenceBeaconHomeEnv), "hooks.json")
}

// referenceBeaconEntry is the inner hook object. The timeout is milliseconds,
// which is gemini-cli's unit and the one upstream's own `hooks migrate` gets
// wrong by 1000x.
func referenceBeaconEntry(command string) map[string]interface{} {
	return map[string]interface{}{
		"type":    "command",
		"command": command,
		"timeout": 2000,
	}
}

// referenceBeaconConfig returns a Config, not a (Config, error) — see the file
// comment. command is empty only on the uninstall path, which never needs it.
func referenceBeaconConfig(command string) hookjson.Config {
	return hookjson.Config{
		Path:       referenceBeaconSettingsPath(),
		Sentinel:   hookbeacon.Sentinel(referenceBeaconAdapter),
		Events:     referenceBeaconEvents,
		MatcherFor: func(string) string { return "" },
		Entry:      func() map[string]interface{} { return referenceBeaconEntry(command) },
		IsCanonical: func(hook map[string]interface{}) bool {
			c, _ := hook["command"].(string)
			return hookbeacon.IsCanonical(c, referenceBeaconAdapter)
		},
		WriteFile: func(path string, data []byte) error {
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				return err
			}
			return os.WriteFile(path, data, 0o600)
		},
	}
}

func referenceBeaconEnsureInstalled() (bool, error) {
	return hookjson.EnsureInstalled(referenceBeaconConfig(referenceBeaconCommand))
}

// referenceBeaconUninstall passes no command on purpose: our entries are
// identified by Sentinel, which names neither the binary path nor an address,
// so removal must not depend on resolving a binary that `site/install.sh
// --uninstall` has already deleted.
func referenceBeaconUninstall() (bool, error) {
	return hookjson.Uninstall(referenceBeaconConfig(""))
}

// referenceBeaconForeignInstall is the address-free counterpart of seeding a
// default-port install: the same installer, situated on a different irrlichd.
func referenceBeaconForeignInstall() (bool, error) {
	command, err := hookbeacon.Command(referenceForeignBinary, referenceBeaconAdapter)
	if err != nil {
		return false, err
	}
	return hookjson.EnsureInstalled(referenceBeaconConfig(command))
}

func TestAddressFreeDeliveryContract(t *testing.T) {
	if referenceBeaconCommandErr != nil {
		t.Fatalf("resolving the beacon command: %v", referenceBeaconCommandErr)
	}
	AssertHookEndpointFollowsBindAddr(t, HookInstaller{
		Delivery: DeliveryAddressFree,
		SettingsPath: func(t *testing.T) string {
			home := t.TempDir()
			t.Setenv(referenceBeaconHomeEnv, home)
			return referenceBeaconSettingsPath()
		},
		Sentinel: hookbeacon.Sentinel(referenceBeaconAdapter),
		Events:   referenceBeaconEvents,
		Entry:    func() map[string]interface{} { return referenceBeaconEntry(referenceBeaconCommand) },
		// Beacon delivery is a `type: command` entry, so the delivery string is
		// the whole command — there is no url field and nothing address-shaped
		// inside it.
		EndpointOf: func(hook map[string]interface{}) string {
			c, _ := hook["command"].(string)
			return c
		},
		EnsureInstalled: referenceBeaconEnsureInstalled,
		Uninstall:       referenceBeaconUninstall,
		ForeignInstall:  referenceBeaconForeignInstall,
	})
}
