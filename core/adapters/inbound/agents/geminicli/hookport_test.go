// hookport_test.go wires the Gemini CLI hook installer into the shared issue
// #1178 contract, under its DeliveryAddressFree route (#1453): the installed
// entry carries no address at all, because it names the
// `irrlichd hook-post gemini-cli` beacon, which resolves the daemon's address
// itself at fire time. This adapter is the first production adopter of the
// route hook_endpoint_addressfree_test.go's reference wiring exists to
// exercise — see this file for what "adopts it" concretely means.
package geminicli

import (
	"testing"

	"irrlicht/core/adapters/inbound/agents/hookjson"
	"irrlicht/core/internal/contracttesting"
	"irrlicht/core/pkg/hookbeacon"
)

// TestHookEntry_IsABeaconCommand pins the exact object Gemini CLI reads,
// which the shared contract deliberately does not look at — it only cares
// that the delivery inside carries no address. The type is "command", never
// "http" (Gemini CLI has no such hook type — see hookinstaller.go's package
// doc) and never a bare curl line (which would fail closed on tool calls
// whenever the daemon is down).
func TestHookEntry_IsABeaconCommand(t *testing.T) {
	command, err := hookbeacon.InstalledCommand(AdapterName)
	if err != nil {
		t.Fatalf("resolving the beacon command: %v", err)
	}
	entry := beaconEntry(command)
	if got, want := entry["type"], "command"; got != want {
		t.Errorf("type = %v, want %v", got, want)
	}
	got, _ := entry["command"].(string)
	if got != command {
		t.Errorf("command = %q, want %q", got, command)
	}
	if _, ok := entry["url"]; ok {
		t.Error("entry carries a url key; beacon delivery names no address")
	}
}

func TestHookEndpointFollowsBindAddr(t *testing.T) {
	// Checked up front so a resolution failure reads as itself rather than
	// as the empty-delivery wiring error assertDeliveryIsOurs would report.
	if _, err := hookbeacon.InstalledCommand(AdapterName); err != nil {
		t.Fatalf("resolving the beacon command: %v", err)
	}

	contracttesting.AssertHookEndpointFollowsBindAddr(t, contracttesting.HookInstaller{
		Delivery: contracttesting.DeliveryAddressFree,
		SettingsPath: func(t *testing.T) string {
			home := t.TempDir()
			t.Setenv("HOME", home)
			path, err := geminiSettingsPath()
			if err != nil {
				t.Fatalf("resolving the gemini settings path: %v", err)
			}
			return path
		},
		Sentinel: hookbeacon.Sentinel(AdapterName),
		Events:   installedHookEvents,
		Entry:    ourHookEntry,
		// Beacon delivery is a `type: command` entry; the delivery string is
		// the whole command, since there is no url field and nothing
		// address-shaped inside it.
		EndpointOf: func(hook map[string]interface{}) string {
			c, _ := hook["command"].(string)
			return c
		},
		EnsureInstalled: EnsureHooksInstalled,
		Uninstall:       UninstallHooks,
		// ForeignInstall seeds an entry naming a DIFFERENT (nonexistent)
		// irrlicht binary — the address-free counterpart of seeding a
		// stale-port install — so the contract can assert EnsureInstalled
		// rewrites it in place rather than duplicating it.
		ForeignInstall: func() (bool, error) {
			path, err := geminiSettingsPath()
			if err != nil {
				return false, err
			}
			command, err := hookbeacon.Command("/nonexistent-irrlicht-1717/bin/irrlichd", AdapterName)
			if err != nil {
				return false, err
			}
			return hookjson.EnsureInstalled(hookConfig(path, command))
		},
	})
}
