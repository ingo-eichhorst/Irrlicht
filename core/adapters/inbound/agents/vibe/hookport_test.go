// hookport_test.go wires the Mistral Vibe hook installer into the shared
// issue #1178 contract, under its DeliveryAddressFree route (#1453): the
// installed entry carries no address at all, because it names the
// `irrlichd hook-post mistral-vibe` beacon, which resolves the daemon's
// address itself at fire time.
//
// The read-back obligations (2-4) go through HookInstaller.ReadEntries /
// EndpointOfRaw (#1734, widened specifically for this adapter's own audit of
// #1716's map-typed seam): hooktoml never produces a decoded generic
// structure — it works entirely as byte-range edits over the original file
// bytes — so vibeReadEntries and vibeEndpointOfRaw are thin wrappers over
// hooktoml.HookBlocks and hooktoml.FieldValue rather than anything JSON-
// shaped. Entry/EndpointOf (obligation 1, in-memory only) stay
// map[string]interface{} regardless — nothing downstream of them touches the
// filesystem, so a synthetic one-key map answers that question honestly even
// though what actually lands on disk is raw TOML text.
package vibe

import (
	"testing"

	"irrlicht/core/adapters/inbound/agents/hooktoml"
	"irrlicht/core/internal/contracttesting"
	"irrlicht/core/pkg/hookbeacon"
)

// TestHookEntry_IsABeaconCommand pins the exact object rendered for Vibe's
// [[hooks]] command field, which the shared contract deliberately does not
// look at directly — it only cares that the delivery inside carries no
// address. The command is never a bare curl line (which would fail closed on
// the agent's next turn whenever the daemon is not running).
func TestHookEntry_IsABeaconCommand(t *testing.T) {
	command, err := hookbeacon.InstalledCommand(AdapterName)
	if err != nil {
		t.Fatalf("resolving the beacon command: %v", err)
	}
	block := vibeHookEntryBlock(command)
	got, ok := hooktoml.FieldValue(block, "command")
	if !ok || got != command {
		t.Errorf("command field = %q, ok=%v, want %q, true", got, ok, command)
	}
	if _, ok := hooktoml.FieldValue(block, "url"); ok {
		t.Error("entry carries a url field; beacon delivery names no address")
	}
}

// vibeEntryNow/vibeEndpointOfMap are the contract's in-memory Entry/
// EndpointOf pair for obligation 1 — a synthetic one-key map, since
// obligation 1 never reads the filesystem (deliveriesOnBothAddrs calls
// EndpointOf(Entry()) directly). Deliberately NOT vibeHookEntryBlock's real
// TOML rendering: that would work too, but the map is the shape
// HookInstaller.EndpointOfRaw's own doc comment describes as the intended
// answer here ("an adapter can honestly answer that question with a map
// even when what it actually persists to disk is raw TOML text").
func vibeEntryNow() map[string]interface{} {
	command, _ := hookbeacon.InstalledCommand(AdapterName)
	return map[string]interface{}{"command": command}
}

func vibeEndpointOfMap(hook map[string]interface{}) string {
	c, _ := hook["command"].(string)
	return c
}

// vibeReadEntries is the contract's ReadEntries: every [[hooks]] block in
// hooks.toml, raw. event is ignored — hooktoml has no per-event
// disambiguation (vibe installs exactly one event, so "the entry" is
// unambiguous without filtering), the decision
// HookInstaller.ReadEntries's own doc comment and the reference TOML
// installer's TestReferenceTOMLReadEntries_IgnoresEventArgument both record
// as a deliberately supported shape, not a gap this wiring is working
// around.
func vibeReadEntries(path, _ string) ([][]byte, error) {
	return hooktoml.HookBlocks(path)
}

// vibeEndpointOfRaw extracts the command field's decoded value from one raw
// [[hooks]] block — the same field IsCanonical already compares, applied
// here to a single named field instead of the whole block.
func vibeEndpointOfRaw(entry []byte) string {
	v, _ := hooktoml.FieldValue(entry, "command")
	return v
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
			t.Setenv(vibeHomeEnvVar, "")
			path, err := hooksTomlPath()
			if err != nil {
				t.Fatalf("resolving hooks.toml path: %v", err)
			}
			return path
		},
		Sentinel:      hookbeacon.Sentinel(AdapterName),
		Events:        installedHookEvents,
		Entry:         vibeEntryNow,
		EndpointOf:    vibeEndpointOfMap,
		ReadEntries:   vibeReadEntries,
		EndpointOfRaw: vibeEndpointOfRaw,
		// EnsureInstalled/Uninstall are the REAL adapter entry points, not a
		// hooks.toml-only stand-in: they also touch config.toml's
		// enable_experimental_hooks flag, which the contract never inspects
		// (it only reads hooks.toml back through ReadEntries) but which is
		// exactly the production code path a fake substitute would not
		// exercise.
		EnsureInstalled: EnsureHooksInstalled,
		Uninstall:       UninstallHooks,
		// ForeignInstall seeds an entry naming a DIFFERENT (nonexistent)
		// irrlicht binary — the address-free counterpart of seeding a
		// stale-port install — so the contract can assert EnsureInstalled
		// rewrites it in place rather than duplicating it. Mirrors
		// geminicli's identical ForeignInstall shape.
		ForeignInstall: func() (bool, error) {
			path, err := hooksTomlPath()
			if err != nil {
				return false, err
			}
			command, err := hookbeacon.Command("/nonexistent-irrlicht-1718/bin/irrlichd", AdapterName)
			if err != nil {
				return false, err
			}
			return hooktoml.EnsureInstalled(vibeHookConfig(path, command))
		},
	})
}
