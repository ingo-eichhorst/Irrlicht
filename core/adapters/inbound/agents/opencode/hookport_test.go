// hookport_test.go wires the opencode plugin installer into the shared issue
// #1178 contract, under its DeliveryAddressFree route (#1453): the installed
// artifact carries no address at all, because it names the `irrlichd hook-post
// opencode` beacon, which resolves the daemon's address itself at fire time.
//
// opencode is the fifth adapter on that route, after geminicli, kirocli, vibe
// and pi.
//
// The read-back obligations (2-4) go through HookInstaller.ReadEntries /
// EndpointOfRaw (#1734), for the same reason pi's do: this adapter's "entry" is
// not a document node of any kind. It is a JavaScript file, and the whole file
// is the entry. There is one, it carries all three of this adapter's event
// subscriptions, and the delivery it carries is the one beacon command line
// inside it — which openCodeEndpointOfRaw reads back with the same anchored
// regexp the installer uses to decide whether an installed copy is current.
// Entry/EndpointOf (obligation 1, in-memory only) stay
// map[string]interface{} regardless: nothing downstream of them touches the
// filesystem, so a synthetic one-key map answers that question honestly.
package opencode

import (
	"os"
	"testing"

	"irrlicht/core/internal/contracttesting"
	"irrlicht/core/pkg/hookbeacon"
)

// foreignBinaryPath is the irrlichd a DIFFERENTLY-situated daemon would have
// named. Deliberately a path that does not exist: an absolute path that has
// stopped existing is exactly the drift beacon delivery newly admits (#1373),
// and it needs no second executable to arrange.
const foreignBinaryPath = "/nonexistent-irrlicht-1719/bin/irrlichd"

// openCodeEntryNow / openCodeEndpointOfMap are the contract's in-memory
// Entry/EndpointOf pair for obligation 1 — a synthetic one-key map, since
// obligation 1 never reads the filesystem.
func openCodeEntryNow() map[string]interface{} {
	command, _ := hookbeacon.InstalledCommand(AdapterName)
	return map[string]interface{}{"command": command}
}

func openCodeEndpointOfMap(hook map[string]interface{}) string {
	c, _ := hook["command"].(string)
	return c
}

// openCodeReadEntries is the contract's ReadEntries: the installed plugin file,
// raw, as a single entry — or nothing at all when the file is absent or is not
// ours. event is ignored; this adapter writes one file carrying every
// subscription, so there is nothing to filter, which HookInstaller.ReadEntries's
// own doc comment records as a supported shape rather than a gap.
func openCodeReadEntries(path, _ string) ([][]byte, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return [][]byte{data}, nil
}

// openCodeEndpointOfRaw extracts the beacon command from one raw entry — the
// same value ensureInstalledWithCommand renders in and VerifyHooksInstalled
// compares, read back through the production helper rather than a second parser
// written for the test.
func openCodeEndpointOfRaw(entry []byte) string {
	command, _ := beaconCommandOf(entry)
	return command
}

func TestHookEndpointFollowsBindAddr(t *testing.T) {
	// Checked up front so a resolution failure reads as itself rather than as
	// the empty-delivery wiring error assertDeliveryIsOurs would report.
	if _, err := hookbeacon.InstalledCommand(AdapterName); err != nil {
		t.Fatalf("resolving the beacon command: %v", err)
	}

	contracttesting.AssertHookEndpointFollowsBindAddr(t, contracttesting.HookInstaller{
		Delivery: contracttesting.DeliveryAddressFree,
		SettingsPath: func(t *testing.T) string {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv(configHomeEnvVar, "")
			path, err := PluginPath()
			if err != nil {
				t.Fatalf("resolving the plugin path: %v", err)
			}
			return path
		},
		Sentinel:        hookbeacon.Sentinel(AdapterName),
		Events:          installedHookEvents,
		Entry:           openCodeEntryNow,
		EndpointOf:      openCodeEndpointOfMap,
		ReadEntries:     openCodeReadEntries,
		EndpointOfRaw:   openCodeEndpointOfRaw,
		EnsureInstalled: EnsureHooksInstalled,
		Uninstall:       UninstallHooks,
		// ForeignInstall seeds a plugin naming a DIFFERENT (nonexistent)
		// irrlicht binary — the address-free counterpart of seeding a
		// stale-port install — so the contract can assert EnsureHooksInstalled
		// rewrites it in place rather than leaving two of them.
		ForeignInstall: func() (bool, error) {
			command, err := hookbeacon.Command(foreignBinaryPath, AdapterName)
			if err != nil {
				return false, err
			}
			return ensureInstalledWithCommand(command)
		},
	})
}
