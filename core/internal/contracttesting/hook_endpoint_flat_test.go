// hook_endpoint_flat_test.go exercises AssertHookEndpointFollowsBindAddr's
// EntriesOf seam (issue #1716) against a reference FLAT-shape installer, the
// same role hook_endpoint_addressfree_test.go's referenceBeaconInstaller
// plays for the DeliveryAddressFree route: proof the seam works, built and
// driven BEFORE any real adapter declares it, so this obligation is not
// "exemption with no test behind it" for the months between the seam
// landing and kiro-cli's own PR adopting it.
//
// It is scaffolding, and says so exactly as that file does: once kiro-cli
// wires EntriesOf: FlatHookEntries for real (kirocli/hookport_test.go), that
// adapter's wiring becomes the honest coverage and this file should be
// reduced to whatever it still uniquely proves, or deleted.
//
// The flat merge logic below is deliberately NOT hookjson.EnsureInstalled —
// that function only ever writes the nested matcher-group shape, which is
// exactly what a flat-shape agent's hook schema rejects (#1716's own audit:
// kiro-cli falls back to a different agent while reporting install success
// against a nested entry). So this file carries its own minimal flat merge,
// reusing hookjson.ReadSettings/WriteSettings UNMODIFIED for the actual
// byte-level splice — the risky part, already property-tested in hookjson —
// rather than writing a second structural-diff writer. This is the same
// shape kirocli/hookinstaller.go uses in the adapter that motivated this
// seam.
package contracttesting

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"irrlicht/core/adapters/inbound/agents/agentpaths"
	"irrlicht/core/adapters/inbound/agents/hookjson"
	"irrlicht/core/pkg/hookbeacon"
)

// referenceFlatAdapter is the route segment the beacon posts to for this
// fixture's flat-shape reference installer.
const referenceFlatAdapter = "reference-flat"

// referenceFlatHomeEnv relocates the fixture's config home. Deliberately a
// dedicated env var, distinct from referenceBeaconHomeEnv, so the two
// reference installers' fixtures cannot collide if ever driven in the same
// process.
const referenceFlatHomeEnv = "IRRLICHT_TEST_REFERENCE_FLAT_HOME"

// referenceFlatForeignBinary mirrors referenceForeignBinary: an absolute,
// nonexistent irrlichd — the drift the beacon newly admits.
const referenceFlatForeignBinary = "/nonexistent-irrlicht-1716/bin/irrlichd"

var referenceFlatEvents = []string{"postToolUse", "stop"}

// referenceFlatHome mirrors referenceBeaconHome's reasoning exactly,
// including the deliberately empty default: an unset override must fail
// loudly rather than resolve into a developer's real $HOME.
func referenceFlatHome() (string, error) {
	return agentpaths.AbsRoot(agentpaths.FromEnv(referenceFlatAdapter, referenceFlatHomeEnv, ""))
}

func referenceFlatConfigPath() (string, error) {
	home, err := referenceFlatHome()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "hooks.json"), nil
}

func referenceFlatEntry(command string) map[string]interface{} {
	return map[string]interface{}{"command": command}
}

func referenceFlatEndpointOf(hook map[string]interface{}) string {
	c, _ := hook["command"].(string)
	return c
}

func referenceFlatEntryNow() map[string]interface{} {
	command, _ := hookbeacon.InstalledCommand(referenceFlatAdapter)
	return referenceFlatEntry(command)
}

func referenceFlatIsCanonical(hook map[string]interface{}) bool {
	c, _ := hook["command"].(string)
	return hookbeacon.IsCanonical(c, referenceFlatAdapter)
}

func referenceFlatWriteFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

// referenceFlatEnsureInstalled merges a flat entry for every event into the
// settings file's "hooks" key, upgrading a stale sentinel-bearing entry in
// place rather than duplicating it — the flat-shape analogue of
// hookjson.EnsureInstalled's own reconciliation, over the SAME
// ReadSettings/WriteSettings pair (the tested byte-level splice), differing
// only in how one event's array is shaped.
func referenceFlatEnsureInstalled() (bool, error) {
	path, err := referenceFlatConfigPath()
	if err != nil {
		return false, err
	}
	command, err := hookbeacon.InstalledCommand(referenceFlatAdapter)
	if err != nil {
		return false, err
	}
	settings, err := hookjson.ReadSettings(path)
	if err != nil {
		return false, err
	}
	hooksMap, ok := settings["hooks"].(map[string]interface{})
	if !ok {
		hooksMap = map[string]interface{}{}
		settings["hooks"] = hooksMap
	}
	sentinel := hookbeacon.Sentinel(referenceFlatAdapter)
	modified := false
	for _, event := range referenceFlatEvents {
		arr, _ := hooksMap[event].([]interface{})
		newArr, changed := reconcileFlatEventForTest(arr, sentinel, func() map[string]interface{} {
			return referenceFlatEntry(command)
		}, referenceFlatIsCanonical)
		if changed {
			hooksMap[event] = newArr
			modified = true
		}
	}
	if !modified {
		return false, nil
	}
	return true, hookjson.WriteSettings(path, settings, referenceFlatWriteFile)
}

// referenceFlatUninstall removes every sentinel-bearing flat entry, whatever
// binary it names — not scoped to the currently resolved one, matching
// referenceBeaconUninstall's own reasoning.
func referenceFlatUninstall() (bool, error) {
	path, err := referenceFlatConfigPath()
	if err != nil {
		return false, err
	}
	settings, err := hookjson.ReadSettings(path)
	if err != nil {
		return false, err
	}
	hooksMap, ok := settings["hooks"].(map[string]interface{})
	if !ok {
		return false, nil
	}
	sentinel := hookbeacon.Sentinel(referenceFlatAdapter)
	modified := false
	for _, event := range referenceFlatEvents {
		arr, ok := hooksMap[event].([]interface{})
		if !ok {
			continue
		}
		var kept []interface{}
		removed := false
		for _, e := range arr {
			entry, ok := e.(map[string]interface{})
			if ok {
				if c, _ := entry["command"].(string); strings.Contains(c, sentinel) {
					removed = true
					continue
				}
			}
			kept = append(kept, e)
		}
		if removed {
			modified = true
			if len(kept) == 0 {
				delete(hooksMap, event)
			} else {
				hooksMap[event] = kept
			}
		}
	}
	if !modified {
		return false, nil
	}
	return true, hookjson.WriteSettings(path, settings, referenceFlatWriteFile)
}

// referenceFlatForeignInstall seeds an entry naming a different, nonexistent
// irrlichd binary — the flat-shape counterpart of referenceBeaconForeignInstall.
func referenceFlatForeignInstall() (bool, error) {
	path, err := referenceFlatConfigPath()
	if err != nil {
		return false, err
	}
	command, err := hookbeacon.Command(referenceFlatForeignBinary, referenceFlatAdapter)
	if err != nil {
		return false, err
	}
	settings, err := hookjson.ReadSettings(path)
	if err != nil {
		return false, err
	}
	hooksMap, ok := settings["hooks"].(map[string]interface{})
	if !ok {
		hooksMap = map[string]interface{}{}
		settings["hooks"] = hooksMap
	}
	sentinel := hookbeacon.Sentinel(referenceFlatAdapter)
	modified := false
	for _, event := range referenceFlatEvents {
		arr, _ := hooksMap[event].([]interface{})
		newArr, changed := reconcileFlatEventForTest(arr, sentinel, func() map[string]interface{} {
			return referenceFlatEntry(command)
		}, referenceFlatIsCanonical)
		if changed {
			hooksMap[event] = newArr
			modified = true
		}
	}
	if !modified {
		return false, nil
	}
	return true, hookjson.WriteSettings(path, settings, referenceFlatWriteFile)
}

// reconcileFlatEventForTest mirrors kirocli/hookinstaller.go's
// reconcileFlatEvent: rewrite a sentinel-bearing entry that is not canonical,
// append a fresh one only when none exists. Kept local to this test file
// rather than exported from hook_endpoint.go, because it is fixture
// machinery for the reference installer, not part of the contract's own
// grading surface (that surface is EntriesOf/onlyEntry/hasOurEntry).
func reconcileFlatEventForTest(arr []interface{}, sentinel string, entry func() map[string]interface{}, isCanonical func(map[string]interface{}) bool) ([]interface{}, bool) {
	found := false
	changed := false
	out := make([]interface{}, 0, len(arr)+1)
	for _, e := range arr {
		m, ok := e.(map[string]interface{})
		if !ok {
			out = append(out, e)
			continue
		}
		if c, _ := m["command"].(string); strings.Contains(c, sentinel) {
			found = true
			if !isCanonical(m) {
				m = entry()
				changed = true
			}
		}
		out = append(out, m)
	}
	if !found {
		out = append(out, entry())
		changed = true
	}
	return out, changed
}

// referenceFlatInstaller is the whole flat-shape wiring, as one value —
// mirroring referenceBeaconInstaller's own role exactly, one field
// different: EntriesOf: FlatHookEntries.
func referenceFlatInstaller() HookInstaller {
	return HookInstaller{
		Delivery: DeliveryAddressFree,
		SettingsPath: func(t *testing.T) string {
			t.Setenv(referenceFlatHomeEnv, t.TempDir())
			path, err := referenceFlatConfigPath()
			if err != nil {
				t.Fatalf("resolving the reference flat hooks path: %v", err)
			}
			return path
		},
		Sentinel:        hookbeacon.Sentinel(referenceFlatAdapter),
		Events:          referenceFlatEvents,
		Entry:           referenceFlatEntryNow,
		EndpointOf:      referenceFlatEndpointOf,
		EnsureInstalled: referenceFlatEnsureInstalled,
		Uninstall:       referenceFlatUninstall,
		ForeignInstall:  referenceFlatForeignInstall,
		EntriesOf:       FlatHookEntries,
	}
}

// TestFlatEntryShapeContract is the vacuity guard: the full #1178 contract,
// run against a CORRECT flat-shape installer, end to end, via
// AssertHookEndpointFollowsBindAddr itself — the same entry point a real
// adapter calls — rather than the arm-by-arm self-tests below.
func TestFlatEntryShapeContract(t *testing.T) {
	if _, err := hookbeacon.InstalledCommand(referenceFlatAdapter); err != nil {
		t.Fatalf("resolving the beacon command: %v", err)
	}
	AssertHookEndpointFollowsBindAddr(t, referenceFlatInstaller())
}

// TestFlatHookEntries_NestedShapeIsNotFlat pins FlatHookEntries against the
// shape it is NOT: a nested matcher-group array, where each element is a
// GROUP object ({"matcher":..., "hooks":[...]}) rather than an inner hook
// entry itself. FlatHookEntries must still return it as a "entry" (matching
// what the value on disk literally contains, structurally) — the shape
// mismatch is exactly why an adapter reading kiro-cli's rejected nested
// install would see the wrong thing, which is the defect #1716 catches, not
// one FlatHookEntries introduces.
func TestFlatHookEntries_NestedShapeIsNotFlat(t *testing.T) {
	hooksMap := map[string]interface{}{
		"stop": []interface{}{
			map[string]interface{}{"matcher": "", "hooks": []interface{}{
				map[string]interface{}{"command": "irrlichd hook-post kiro-cli"},
			}},
		},
	}
	entries, err := FlatHookEntries(hooksMap, "stop")
	if err != nil {
		t.Fatalf("FlatHookEntries: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("got %d entries, want 1 (the outer group object itself)", len(entries))
	}
	if _, hasCommand := entries[0]["command"]; hasCommand {
		t.Error("FlatHookEntries read a nested group as if it carried \"command\" directly — " +
			"it must return the group object UNINTERPRETED, not reach into it")
	}
	if _, hasHooks := entries[0]["hooks"]; !hasHooks {
		t.Error("FlatHookEntries lost the group's own \"hooks\" key — it must return the raw " +
			"element, not a nested-shape-aware transformation of it")
	}
}

// TestNestedHookEntries_FlatShapeIsNotNested is the mirror: NestedHookEntries
// fed a flat array (no "hooks" sub-key) must refuse rather than silently
// return zero entries, which would make onlyEntry's "expected exactly 1"
// check pass VACUOUSLY for a nested-shape adapter that regressed to writing
// flat entries by mistake — reporting "0 entries" would read as "event
// missing" (a different, wrong diagnosis) rather than "shape mismatch".
func TestNestedHookEntries_FlatShapeIsNotNested(t *testing.T) {
	hooksMap := map[string]interface{}{
		"stop": []interface{}{
			map[string]interface{}{"command": "irrlichd hook-post kiro-cli"},
		},
	}
	if _, err := NestedHookEntries(hooksMap, "stop"); err == nil {
		t.Error("NestedHookEntries accepted a flat array element with no \"hooks\" key and no error")
	}
}
