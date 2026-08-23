// hookport_test.go wires this adapter into the same issue #1178 contract
// contracttesting.AssertHookEndpointFollowsBindAddr grades for every other
// hook-installing adapter — DeliveryAddressFree (#1453): the installed entry
// carries no address at all, because it names the `irrlichd hook-post
// kiro-cli` beacon, which resolves the daemon's address itself at fire time
// (issue #1716's audit: kiro-cli's own delivery is exec/command with the
// payload on stdin, exactly the beacon's input shape).
//
// Until #1734 this file graded the same four obligations BY HAND, because
// contracttesting's onlyEntry hardcoded hookjson's own nested matcher-group
// shape and kiro-cli's hook schema rejects that shape outright (falls back
// to a different agent while reporting install success — the defect this
// issue's audit found and this adapter exists to not reproduce), so its
// entries are FLAT: `hooksMap[event] = [ {"command": ...} ]`, no
// matcher-group wrapper at all. #1734 widened the shared contract with an
// EntriesOf seam an adapter can supply — kiro-cli is EXACTLY the flat-shape
// case FlatHookEntries was built for — so this adapter now wires eight of
// eight contract families like every other adapter, the same as every
// sibling installer.
package kirocli

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"irrlicht/core/internal/contracttesting"
	"irrlicht/core/pkg/atomicfile"
	"irrlicht/core/pkg/hookbeacon"
)

// TestHookEntry_IsABeaconCommand pins the exact object kiro-cli reads, which
// the shared contract deliberately does not look at — it only cares that the
// delivery inside carries no address. The type is a flat {"command": ...}
// object, never claudecode's/codex's nested matcher-group shape (see
// hookinstaller.go's package comment) and never a "url" key (kiro-cli has no
// native http hook type). This is adapter-specific literal-shape coverage
// the shared contract has no opinion on, so it stays beside the contract
// wiring rather than being subsumed by it.
func TestHookEntry_IsABeaconCommand(t *testing.T) {
	command, err := hookbeacon.InstalledCommand(AdapterName)
	if err != nil {
		t.Fatalf("resolving the beacon command: %v", err)
	}
	entry := ourHookEntry()
	if got, want := entry["command"], command; got != want {
		t.Errorf("command = %v, want %v", got, want)
	}
	if _, ok := entry["url"]; ok {
		t.Error("entry carries a url key; beacon delivery names no address")
	}
	if _, ok := entry["matcher"]; ok {
		t.Error("entry carries a matcher key; postToolUse and stop are installed with no per-tool filter")
	}
	if len(entry) != 1 {
		t.Errorf("entry has %d keys, want exactly 1 (command): %v", len(entry), entry)
	}
}

// kiroContractForeignBinary is the irrlichd a differently-situated daemon
// would have named, matching TestForeignBinaryInstall_UpgradedInPlace's own
// literal (hookinstaller_test.go) — absolute and nonexistent, the drift the
// beacon newly admits (#1373).
const kiroContractForeignBinary = "/nonexistent-irrlicht-1716/bin/irrlichd"

// kiroHookEntryEndpoint is the contract's EndpointOf: a flat entry's whole
// delivery string is its "command" value — there is no url field and no
// address-shaped fragment inside it (see TestHookEntry_IsABeaconCommand).
func kiroHookEntryEndpoint(hook map[string]interface{}) string {
	c, _ := hook["command"].(string)
	return c
}

// seedForeignKiroInstall leaves behind what a differently-situated daemon
// would have installed: the irrlicht agent config, present but naming a
// stale, nonexistent irrlichd binary. It seeds ONLY the flat hook entries —
// deliberately not through EnsureHooksInstalled, which would also flip
// chat.defaultAgent and so would not be "what a foreign daemon installed" but
// "what this adapter's own full install does" — mirroring
// TestForeignBinaryInstall_UpgradedInPlace's own seed exactly.
func seedForeignKiroInstall() (bool, error) {
	configPath, err := irrlichtAgentConfigPath()
	if err != nil {
		return false, err
	}
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		return false, err
	}
	if _, statErr := os.Stat(configPath); errors.Is(statErr, os.ErrNotExist) {
		if err := os.WriteFile(configPath, []byte(`{"name":"irrlicht","hooks":{}}`), 0o600); err != nil {
			return false, err
		}
	} else if statErr != nil {
		return false, statErr
	}
	staleCommand, err := hookbeacon.Command(kiroContractForeignBinary, AdapterName)
	if err != nil {
		return false, err
	}
	return ensureFlatHooksInstalled(flatHookConfig{
		Path:        configPath,
		Sentinel:    hookbeacon.Sentinel(AdapterName),
		Events:      installedHookEvents,
		Entry:       func() map[string]interface{} { return flatBeaconEntry(staleCommand) },
		IsCanonical: hookEntryIsCanonical,
		WriteFile:   atomicfile.WriteFile,
	})
}

// hookEndpointInstaller is this adapter's whole #1178 wiring, as one value —
// the same role every sibling adapter's own hookport_test.go installer
// value plays. EntriesOf: contracttesting.FlatHookEntries is the one field
// that differs from a nested-shape adapter's wiring — see this file's
// header for why.
func hookEndpointInstaller() contracttesting.HookInstaller {
	return contracttesting.HookInstaller{
		Delivery: contracttesting.DeliveryAddressFree,
		SettingsPath: func(t *testing.T) string {
			kiroInstallerHome(t)
			path, err := irrlichtAgentConfigPath()
			if err != nil {
				t.Fatalf("resolving the irrlicht agent config path: %v", err)
			}
			return path
		},
		Sentinel:        hookbeacon.Sentinel(AdapterName),
		Events:          installedHookEvents,
		Entry:           ourHookEntry,
		EndpointOf:      kiroHookEntryEndpoint,
		EnsureInstalled: EnsureHooksInstalled,
		Uninstall:       UninstallHooks,
		ForeignInstall:  seedForeignKiroInstall,
		EntriesOf:       contracttesting.FlatHookEntries,
	}
}

// TestHookEndpointContract is the #1178 contract itself, run end to end
// through the same entry point every sibling adapter calls.
func TestHookEndpointContract(t *testing.T) {
	contracttesting.AssertHookEndpointFollowsBindAddr(t, hookEndpointInstaller())
}
