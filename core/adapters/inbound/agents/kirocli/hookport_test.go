// hookport_test.go covers the same issue #1178 obligations
// contracttesting.AssertHookEndpointFollowsBindAddr grades for every other
// hook-installing adapter, under this adapter's DeliveryAddressFree route
// (#1453): the installed entry carries no address at all, because it names
// the `irrlichd hook-post kiro-cli` beacon, which resolves the daemon's
// address itself at fire time (issue #1716's audit: kiro-cli's own delivery
// is exec/command with the payload on stdin, exactly the beacon's input
// shape).
//
// # Why this file does NOT call AssertHookEndpointFollowsBindAddr
//
// It cannot, structurally, and the reason is worth recording rather than
// silently working around: contracttesting's onlyEntry
// (core/internal/contracttesting/hook_endpoint.go) reads a settings-file
// event as hookjson's own nested matcher-group shape —
// `hooksMap[event] = [ { "matcher": ..., "hooks": [ {...} ] } ]` — and
// unconditionally indexes into `group["hooks"]`. kiro-cli's hook schema
// rejects exactly that nested shape outright (falls back to a different
// agent while reporting install success — the defect this issue's audit
// found and this adapter exists to not reproduce), so its entries are FLAT:
// `hooksMap[event] = [ {"command": ...} ]`, no matcher-group wrapper at all.
// onlyEntry's `group["hooks"].([]interface{})` step fails against that shape
// unconditionally (measured: `expected exactly 1 inner hook, got <nil>`),
// which is not a wiring mistake on this adapter's part — every entry shape
// this contract has ever graded, including gemini-cli's beacon-delivered one,
// is the nested shape; kiro-cli is the first genuinely flat one.
//
// Per this issue's constraints, core/internal/contracttesting is not modified
// here (a concurrent change to a different corner of it is landing
// separately, and hookjson's own splicer is deliberately left alone for the
// same reason — see hookinstaller.go's package comment). So this file grades
// the same four obligations by hand instead of leaving them uncovered:
// TestHookEntry_IsABeaconCommand and TestDeliveryCarriesNoAddress are
// obligation 1 (an address-free delivery is invariant across bind addresses
// and carries nothing address-shaped); TestInstallVerifyUninstall_RoundTrip
// (hookinstaller_test.go) is obligation 2 (install writes the canonical
// entries and a second install is idempotent);
// TestForeignBinaryInstall_UpgradedInPlace (hookinstaller_test.go) is
// obligation 3 (an entry naming a stale binary path is rewritten in place,
// not duplicated); TestUninstallIsNotBinaryScoped below is obligation 4.
//
// What a future extension to contracttesting would need, concretely, to
// close this gap for real: onlyEntry (or a variant AssertHookEndpointFollowsBindAddr
// could select via HookInstaller) needs an EntriesOf(hooksMap, event)
// []map[string]interface{} seam an adapter supplies, so a flat-shape
// installer can return `hooksMap[event]` directly cast, and a
// nested-shape one can keep unwrapping `group["hooks"]` — the two other
// obligations (assertDeliveryIsOurs, the duplicate-group check) already
// operate on the returned entries generically and would not need to change.
package kirocli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"irrlicht/core/internal/contracttesting"
	"irrlicht/core/pkg/daemonaddr"
	"irrlicht/core/pkg/hookbeacon"
)

// TestHookEntry_IsABeaconCommand pins the exact object kiro-cli reads, which
// the shared contract deliberately does not look at — it only cares that the
// delivery inside carries no address. The type is a flat {"command": ...}
// object, never claudecode's/codex's nested matcher-group shape (see
// hookinstaller.go's package comment) and never a "url" key (kiro-cli has no
// native http hook type).
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

// TestDeliveryCarriesNoAddress is obligation 1 of the #1178 contract's
// address-free route, driven by hand for the reason this file's package
// comment gives: the entry THIS ADAPTER installs (ourHookEntry, not the
// shared hookbeacon.InstalledCommand directly — that helper is already
// covered by hookbeacon's own suite and calling it here would test nothing
// adapter-specific) is IDENTICAL on the default and an alternate bind
// address, and carries nothing address-shaped — no scheme, no loopback host,
// no port-shaped fragment, no hook route.
func TestDeliveryCarriesNoAddress(t *testing.T) {
	kiroInstallerHome(t)

	t.Setenv(daemonaddr.EnvBindAddr, "")
	onDefault, ok := ourHookEntry()["command"].(string)
	if !ok || onDefault == "" {
		t.Fatalf("ourHookEntry()[command] on the default bind addr = %v, want a non-empty string", onDefault)
	}
	t.Setenv(daemonaddr.EnvBindAddr, contracttesting.AltBindAddr)
	onAlt, ok := ourHookEntry()["command"].(string)
	if !ok || onAlt == "" {
		t.Fatalf("ourHookEntry()[command] on %s = %v, want a non-empty string", contracttesting.AltBindAddr, onAlt)
	}

	if onDefault != onAlt {
		t.Fatalf("installed entry differs between bind addresses (%q vs %q) — an address-free "+
			"install must not vary with %s", onDefault, onAlt, daemonaddr.EnvBindAddr)
	}
	assertNoAddressShapedFragment(t, onDefault)
}

// assertNoAddressShapedFragment is a narrower, adapter-local version of the
// shared contract's own addressShaped check — narrower because it does not
// need to defend against a hardcoded PORT specifically (a beacon command
// never renders one at all; hookbeacon.Command's own doc and tests cover
// that), only the shapes an accidental regression here could introduce: a
// scheme, a loopback host literal, or the daemon's own HTTP route prefix.
func assertNoAddressShapedFragment(t *testing.T, delivery string) {
	t.Helper()
	lower := strings.ToLower(delivery)
	for _, frag := range []string{"http://", "https://", "localhost", "127.0.0.1", "/api/v1/"} {
		if strings.Contains(lower, frag) {
			t.Errorf("delivery %q carries the address-shaped fragment %q — an address-free "+
				"delivery must resolve the daemon at fire time, not bake in an address", delivery, frag)
		}
	}
}

// TestUninstallIsNotBinaryScoped is obligation 4: a daemon cleans up entries
// installed under a DIFFERENT irrlichd binary path — `site/install.sh
// --uninstall` removes the binary without calling `--uninstall-hooks`, so the
// entry commonly outlives the path it names, and Uninstall must not depend on
// resolving that (now possibly gone) path at all.
func TestUninstallIsNotBinaryScoped(t *testing.T) {
	kiroInstallerHome(t)
	configPath, err := irrlichtAgentConfigPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte(`{"name":"irrlicht","hooks":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	staleCommand, err := hookbeacon.Command("/nonexistent-irrlicht-1716/bin/irrlichd", AdapterName)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ensureFlatHooksInstalled(flatHookConfig{
		Path:        configPath,
		Sentinel:    hookbeacon.Sentinel(AdapterName),
		Events:      installedHookEvents,
		MatcherFor:  matcherForEvent,
		Entry:       func() map[string]interface{} { return flatBeaconEntry(staleCommand) },
		IsCanonical: hookEntryIsCanonical,
		WriteFile:   atomicWriteFile,
	}); err != nil {
		t.Fatalf("seed a foreign-binary install: %v", err)
	}

	modified, err := UninstallHooks()
	if err != nil {
		t.Fatalf("UninstallHooks: %v", err)
	}
	if !modified {
		t.Fatal("expected modified=true removing a foreign-binary install")
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	var settings map[string]interface{}
	if err := json.Unmarshal(data, &settings); err != nil {
		t.Fatal(err)
	}
	hooks, _ := settings["hooks"].(map[string]interface{})
	for _, event := range installedHookEvents {
		if arr, ok := hooks[event]; ok && len(arr.([]interface{})) > 0 {
			t.Errorf("event %s: a foreign-binary entry survived uninstall: %v", event, arr)
		}
	}
}
