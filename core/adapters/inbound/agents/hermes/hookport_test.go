// hookport_test.go wires the hermes shell-hook installer into the shared issue
// #1178 contract, under its DeliveryAddressFree route (#1453): the installed
// entry carries no address at all, because it names the `irrlichd hook-post
// hermes` beacon, which resolves the daemon's address itself at fire time.
//
// hermes is the sixth adapter on that route, after geminicli, kirocli, vibe, pi
// and opencode.
//
// The read-back obligations (2-4) go through HookInstaller.ReadEntries /
// EndpointOfRaw (#1734), for the same reason mistral-vibe's do: this adapter's
// config is not JSON, so there is no decoded map[string]interface{} anywhere on
// the write path for EntriesOf to traverse. readInstalledCommands below is a
// line scan over the marker-delimited region the installer owns — which is what
// makes the read genuinely per-event rather than "the whole file, three times".
package hermes

import (
	"bufio"
	"os"
	"strings"
	"testing"

	"irrlicht/core/adapters/inbound/agents/hookyaml"
	"irrlicht/core/internal/contracttesting"
	"irrlicht/core/pkg/hookbeacon"
)

// foreignBinaryPath is the irrlichd a DIFFERENTLY-situated daemon would have
// named. Deliberately a path that does not exist: an absolute path that has
// stopped existing is exactly the drift beacon delivery newly admits (#1373),
// and it needs no second executable to arrange.
const foreignBinaryPath = "/nonexistent-irrlicht-1722/bin/irrlichd"

// hermesEntryNow / hermesEndpointOfMap are the contract's in-memory
// Entry/EndpointOf pair for obligation 1 — a synthetic one-key map, since
// obligation 1 never reads the filesystem.
func hermesEntryNow() map[string]interface{} {
	beacon, _ := hookbeacon.InstalledCommand(AdapterName)
	return map[string]interface{}{"command": hookCommand(beacon)}
}

func hermesEndpointOfMap(hook map[string]interface{}) string {
	c, _ := hook["command"].(string)
	return c
}

// hermesReadEntries is the contract's ReadEntries: the `command:` value the
// installed region declares for one event, as raw bytes.
//
// It reads only INSIDE the marker-delimited region, so a user's own hook on the
// same event — which the installer refuses to create in the first place — could
// never be mistaken for ours.
func hermesReadEntries(path, event string) ([][]byte, error) {
	f, err := os.Open(path) // #nosec G304 -- the path the test itself resolved
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()

	var out [][]byte
	inRegion, inEvent := false, false
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		switch {
		case line == hookyaml.BeginMarker(hookRegionOwner):
			inRegion = true
		case line == hookyaml.EndMarker(hookRegionOwner):
			inRegion = false
		case !inRegion:
		case strings.HasSuffix(line, ":") && !strings.HasPrefix(line, "-"):
			inEvent = strings.TrimSuffix(line, ":") == event
		case inEvent && strings.HasPrefix(line, "- command:"):
			out = append(out, []byte(strings.TrimSpace(strings.TrimPrefix(line, "- command:"))))
		}
	}
	return out, sc.Err()
}

// hermesEndpointOfRaw unquotes one raw entry back into the command the agent
// will run — the same string ensureInstalledWithCommand rendered in.
func hermesEndpointOfRaw(entry []byte) string {
	s := string(entry)
	if len(s) < 2 || s[0] != '"' || s[len(s)-1] != '"' {
		return s
	}
	// The installer writes a YAML double-quoted scalar whose only escapes are
	// \\ and \" (hookyaml.Quote), so undoing those two is exact rather than an
	// approximation of a general YAML unescape.
	s = s[1 : len(s)-1]
	s = strings.ReplaceAll(s, `\"`, `"`)
	return strings.ReplaceAll(s, `\\`, `\`)
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
			hermesHome(t)
			path, err := ConfigPath()
			if err != nil {
				t.Fatalf("resolving the config path: %v", err)
			}
			return path
		},
		Sentinel:        hookbeacon.Sentinel(AdapterName),
		Events:          installedHookEvents,
		Entry:           hermesEntryNow,
		EndpointOf:      hermesEndpointOfMap,
		ReadEntries:     hermesReadEntries,
		EndpointOfRaw:   hermesEndpointOfRaw,
		EnsureInstalled: EnsureHooksInstalled,
		Uninstall:       UninstallHooks,
		// ForeignInstall seeds a region naming a DIFFERENT (nonexistent)
		// irrlicht binary — the address-free counterpart of seeding a
		// stale-port install — so the contract can assert EnsureHooksInstalled
		// rewrites it in place rather than leaving two of them.
		ForeignInstall: func() (bool, error) {
			beacon, err := hookbeacon.Command(foreignBinaryPath, AdapterName)
			if err != nil {
				return false, err
			}
			return ensureInstalledWithCommand(beacon)
		},
	})
}
