// hookport_test.go wires this adapter's installer into the shared issue #1178
// contract, under its DeliveryAddressFree route (#1453): the installed entry
// carries no address at all, because it names the `irrlichd hook-post
// antigravity` beacon, which resolves the daemon's address itself at fire
// time.
//
// For antigravity the route is not a preference, it is the only expressible
// one: hooks.md's "Current Limitations" says only `type: "command"` is
// supported, and there is no `url` field anywhere in the handler schema. So a
// URL-carrying install is not something this adapter chose against — it is
// something antigravity cannot read.
//
// The read-back obligations (2-4) go through HookInstaller.ReadEntries /
// EndpointOfRaw (#1734) rather than the JSON matcher-group walk, because this
// document is not the `{"hooks": {<event>: [...]}}` shape that walk knows: it
// is keyed by top-level hook NAME, and irrlicht's entries live under one of
// them. Entry/EndpointOf (obligation 1, in-memory only) stay
// map[string]interface{} — nothing downstream of them touches the filesystem.
package antigravity

import (
	"encoding/json"
	"os"
	"testing"

	"irrlicht/core/adapters/inbound/agents/hookjson"
	"irrlicht/core/internal/contracttesting"
	"irrlicht/core/pkg/hookbeacon"
)

// antigravityEntryNow is the contract's in-memory Entry: the handler object
// this daemon would install right now, built by the same function the
// installer uses.
func antigravityEntryNow() map[string]interface{} {
	beacon, _ := hookbeacon.InstalledCommand(AdapterName)
	return hookEntry(beacon)
}

// antigravityEndpointOfMap reads the delivery string out of a handler object.
// The whole `command` is correct here — the contract's own doc says so for a
// command entry, and it is what carries (or, on this route, does not carry) an
// address.
func antigravityEndpointOfMap(handler map[string]interface{}) string {
	command, _ := handler["command"].(string)
	return command
}

// antigravityReadEntries returns the raw bytes of every handler under our
// named hook for one event — including handlers that are not ours, since the
// contract checks for the sentinel itself and a filtered view would make its
// "nothing of ours survived uninstall" arm pass vacuously.
func antigravityReadEntries(path, event string) ([][]byte, error) {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return nil, nil
	}
	doc, err := hookjson.ReadSettings(path)
	if err != nil {
		return nil, err
	}
	ours, ok := doc[hookName].(map[string]interface{})
	if !ok {
		return nil, nil
	}
	handlers, ok := ours[event].([]interface{})
	if !ok {
		return nil, nil
	}
	var out [][]byte
	for _, h := range handlers {
		raw, err := json.Marshal(h)
		if err != nil {
			return nil, err
		}
		out = append(out, raw)
	}
	return out, nil
}

// antigravityEndpointOfRaw extracts the delivery string from one raw handler.
func antigravityEndpointOfRaw(entry []byte) string {
	var handler map[string]interface{}
	if err := json.Unmarshal(entry, &handler); err != nil {
		return ""
	}
	return antigravityEndpointOfMap(handler)
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
			t.Helper()
			t.Setenv("HOME", t.TempDir())
			path, err := HooksPath()
			if err != nil {
				t.Fatalf("resolving the hooks path: %v", err)
			}
			return path
		},
		Sentinel:        hookbeacon.Sentinel(AdapterName),
		Events:          installedHookEvents,
		Entry:           antigravityEntryNow,
		EndpointOf:      antigravityEndpointOfMap,
		ReadEntries:     antigravityReadEntries,
		EndpointOfRaw:   antigravityEndpointOfRaw,
		EnsureInstalled: EnsureHooksInstalled,
		Uninstall:       UninstallHooks,
		// ForeignInstall seeds an entry naming a DIFFERENT (nonexistent)
		// irrlicht binary — the address-free counterpart of seeding a
		// stale-port install — so the contract can assert EnsureHooksInstalled
		// rewrites it in place rather than leaving two of them under our name.
		ForeignInstall: func() (bool, error) {
			beacon, err := hookbeacon.Command(foreignBinaryPath, AdapterName)
			if err != nil {
				return false, err
			}
			return ensureInstalledWithCommand(beacon)
		},
	})
}
