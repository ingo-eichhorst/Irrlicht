// hook_endpoint.go holds the issue #1178 contract every hook-installing agent
// adapter must satisfy: the endpoint it writes into the user's agent config
// follows the daemon's own bind address. Like AssertPermissionGated it exists
// because the obligation is a runtime choice an adapter has to opt into — a
// static check cannot see whether an installer baked ":7837" in — and because
// the alternative is a third adapter copying a test file, or (issue #1216, more
// likely) not copying it and silently shipping a hardcoded port.
package contracttesting

import (
	"fmt"
	"strings"
	"testing"

	"irrlicht/core/adapters/inbound/agents/hookjson"
	"irrlicht/core/pkg/daemonaddr"
)

// AltBindAddr is the non-default bind address AssertHookEndpointFollowsBindAddr
// installs against — the port a `separate`-mode dev daemon and the onboarding
// factory's coexist recorder use. The contract only needs it to differ from the
// default; the specific value keeps failure messages recognizable.
const AltBindAddr = "127.0.0.1:7838"

// HookInstaller wires one adapter's hook installer into
// AssertHookEndpointFollowsBindAddr. Every field is required. Between them they
// cover the only three things that genuinely differ per adapter: where the
// config file lives and which env var relocates it (SettingsPath), the
// install/uninstall entry points, and how an installed entry's endpoint is read
// back (EndpointOf) — Claude Code writes a native `type: http` entry whose
// endpoint is its `url`, Codex a `type: command` curl string that embeds it.
type HookInstaller struct {
	// SettingsPath points the installer at a temp config home — by t.Setenv of
	// whatever env var relocates it ($HOME, $CODEX_HOME) — and returns the
	// absolute path of the settings file the installer will write.
	//
	// The contract calls it FIRST in every sub-test and sets
	// daemonaddr.EnvBindAddr itself afterwards, so an implementation that pins
	// the bind address as a side effect (claudecode's withTempHome does) is
	// safe: the contract's own value always wins.
	SettingsPath func(t *testing.T) string

	// Sentinel is the port-independent substring marking our entries — the
	// same value the adapter passes to hookjson.Config. Used to confirm
	// uninstall left nothing of ours behind.
	Sentinel string

	// Events is the adapter's installed hook event list. Every obligation is
	// checked against every event, so an installer that resolves the port for
	// some events and not others is caught.
	Events []string

	// Entry builds the adapter's canonical inner hook object at the CURRENTLY
	// resolved bind address — the same builder the adapter hands to
	// hookjson.Config. Used to learn what endpoint the adapter would install on
	// a given bind address without going near the filesystem.
	Entry func() map[string]interface{}

	// EndpointOf extracts the delivery string carrying the endpoint from an
	// inner hook object: the `url` of an http entry, the whole `command` of a
	// curl entry. Compared for equality between entries, and searched for the
	// port, so returning the whole command is correct.
	EndpointOf func(hook map[string]interface{}) string

	// EnsureInstalled and Uninstall are the adapter's exported entry points.
	EnsureInstalled func() (bool, error)
	Uninstall       func() (bool, error)
}

// AssertHookEndpointFollowsBindAddr runs the issue #1178 contract against h.
// Three obligations, each of which a new hook-installing adapter can fail
// independently:
//
//  1. an install writes the RESOLVED port, not the default one;
//  2. an entry left by a daemon on a DIFFERENT port is still recognized as ours
//     (the sentinel is port-independent) and rewritten IN PLACE, with no
//     duplicate matcher group appended;
//  3. uninstall is NOT port-scoped, or `irrlichd --uninstall-hooks` run from a
//     daemon on one port leaves another port's entries behind.
//
// It assumes the installed endpoint is a URL with a path, since it looks for
// the ":<port>/" fragment — true of every hook endpoint the daemon serves.
func AssertHookEndpointFollowsBindAddr(t *testing.T, h HookInstaller) {
	t.Helper()

	// Obligation 1, at the source: the endpoint the adapter would install is a
	// function of the bind address at all. An installer that hardcodes the port
	// fails here first, and most legibly.
	t.Run("endpoint_follows_bind_addr", func(t *testing.T) {
		// Relocate the config home even though this sub-test writes nothing:
		// an Entry() that reads anything home-relative must never be evaluated
		// against the developer's real agent config.
		h.SettingsPath(t)

		onDefault := deliveryOn(t, h, "")
		onAlt := deliveryOn(t, h, AltBindAddr)
		if onDefault == onAlt {
			t.Fatalf("delivery is identical on the default and %s bind addresses (%q) — the endpoint does not follow %s",
				AltBindAddr, onDefault, daemonaddr.EnvBindAddr)
		}
		assertResolvedPort(t, "delivery on "+AltBindAddr, onAlt)
	})

	// Obligation 1, end to end: what actually lands in the settings file.
	t.Run("install_writes_resolved_port", func(t *testing.T) {
		path := h.SettingsPath(t)
		want := deliveryOn(t, h, AltBindAddr)

		if _, err := h.EnsureInstalled(); err != nil {
			t.Fatalf("EnsureInstalled: %v", err)
		}
		assertEveryEventDelivers(t, h, path, want)

		modified, err := h.EnsureInstalled()
		if err != nil {
			t.Fatalf("second EnsureInstalled: %v", err)
		}
		if modified {
			t.Error("second install on the same bind address: got modified=true, want false")
		}
	})

	// Obligation 2.
	t.Run("default_port_install_upgraded_in_place", func(t *testing.T) {
		path := h.SettingsPath(t)
		seedDefaultPortInstall(t, h, path)
		want := deliveryOn(t, h, AltBindAddr)

		modified, err := h.EnsureInstalled()
		if err != nil {
			t.Fatalf("EnsureInstalled: %v", err)
		}
		if !modified {
			t.Fatal("expected modified=true when repointing a default-port install at the resolved port")
		}
		// assertEveryEventDelivers fails on a second matcher group, which is
		// the "upgraded in place, not duplicated" half of this obligation.
		assertEveryEventDelivers(t, h, path, want)
	})

	// Obligation 3.
	t.Run("uninstall_is_not_port_scoped", func(t *testing.T) {
		path := h.SettingsPath(t)
		seedDefaultPortInstall(t, h, path)
		t.Setenv(daemonaddr.EnvBindAddr, AltBindAddr)

		modified, err := h.Uninstall()
		if err != nil {
			t.Fatalf("Uninstall: %v", err)
		}
		if !modified {
			t.Fatalf("expected modified=true removing a default-port install from a daemon on %s", AltBindAddr)
		}
		hooksMap := readHooksMap(t, path)
		for _, event := range h.Events {
			if hookjson.HasOurHook(hooksMap, event, h.Sentinel) {
				t.Errorf("event %s: a default-port entry survived uninstall", event)
			}
		}
	})
}

// --- helpers ---

// deliveryOn pins the daemon bind address to bindAddr for the rest of the
// sub-test and returns the delivery string the adapter would install there.
func deliveryOn(t *testing.T, h HookInstaller, bindAddr string) string {
	t.Helper()
	t.Setenv(daemonaddr.EnvBindAddr, bindAddr)
	return h.EndpointOf(h.Entry())
}

// assertEveryEventDelivers checks each event's installed entry against want and
// against the resolved port. Both matter: equality catches an installer writing
// a stale shape, the port check catches the case want itself is wrong because
// the endpoint stopped following the bind address.
func assertEveryEventDelivers(t *testing.T, h HookInstaller, path, want string) {
	t.Helper()
	hooksMap := readHooksMap(t, path)
	for _, event := range h.Events {
		got := h.EndpointOf(onlyEntry(t, hooksMap, event))
		if got != want {
			t.Errorf("event %s: installed delivery = %q, want %q", event, got, want)
		}
		assertResolvedPort(t, "event "+event, got)
	}
}

// assertResolvedPort fails unless delivery carries the alternate port and no
// longer carries the default one.
func assertResolvedPort(t *testing.T, what, delivery string) {
	t.Helper()
	if want := portMarker(AltBindAddr); !strings.Contains(delivery, want) {
		t.Errorf("%s: %q does not carry the resolved port %q", what, delivery, want)
	}
	if stale := portMarker(""); strings.Contains(delivery, stale) {
		t.Errorf("%s: %q still carries the default port %q", what, delivery, stale)
	}
}

// portMarker renders the ":<port>/" fragment of bindAddr's endpoint. An empty
// bindAddr yields the default port, which is what daemonaddr falls back to.
func portMarker(bindAddr string) string {
	return fmt.Sprintf(":%d/", daemonaddr.PortOf(bindAddr))
}

// seedDefaultPortInstall leaves behind exactly what a daemon on the DEFAULT
// port would have: the adapter's own installer, run under a default-port
// environment. Synthesizing the matcher groups by hand instead would be a
// second implementation of what hookjson.EnsureInstalled already does, and one
// that could drift from the shape the adapter actually writes.
func seedDefaultPortInstall(t *testing.T, h HookInstaller, path string) {
	t.Helper()
	t.Setenv(daemonaddr.EnvBindAddr, "")
	if _, err := h.EnsureInstalled(); err != nil {
		t.Fatalf("seeding a default-port install: %v", err)
	}

	// Sentinel is the one field nothing else cross-checks, and a wrong value
	// makes the uninstall-survival assertion pass vacuously — HasOurHook would
	// simply find nothing, for the wrong reason. Confirming the adapter's own
	// install is matched by the declared sentinel turns that silent hole into a
	// wiring error.
	seeded := readHooksMap(t, path)
	for _, event := range h.Events {
		if !hookjson.HasOurHook(seeded, event, h.Sentinel) {
			t.Fatalf("event %s: the adapter's own default-port install is not matched by Sentinel %q — the contract is wired wrong", event, h.Sentinel)
		}
	}
}

// onlyEntry returns the single inner hook of the event's single matcher group,
// failing on any other shape — which is the assertion, not incidental: an
// upgrade that appends a second group instead of rewriting in place shows up
// here as a group count of 2.
func onlyEntry(t *testing.T, hooksMap map[string]interface{}, event string) map[string]interface{} {
	t.Helper()
	groups, ok := hooksMap[event].([]interface{})
	if !ok {
		t.Fatalf("event %s: missing from the settings file or not an array", event)
	}
	if len(groups) != 1 {
		t.Fatalf("event %s: expected exactly 1 matcher group (in-place upgrade, no duplicate appended), got %d", event, len(groups))
	}
	group, ok := groups[0].(map[string]interface{})
	if !ok {
		t.Fatalf("event %s: matcher group is not an object: %v", event, groups[0])
	}
	entries, ok := group["hooks"].([]interface{})
	if !ok || len(entries) != 1 {
		t.Fatalf("event %s: expected exactly 1 inner hook, got %v", event, group["hooks"])
	}
	entry, ok := entries[0].(map[string]interface{})
	if !ok {
		t.Fatalf("event %s: inner hook is not an object: %v", event, entries[0])
	}
	return entry
}

// readHooksMap reads the settings file through the same codec the installers
// use and returns its top-level "hooks" object (nil when absent).
func readHooksMap(t *testing.T, path string) map[string]interface{} {
	t.Helper()
	settings, err := hookjson.ReadSettings(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	hooksMap, _ := settings["hooks"].(map[string]interface{})
	return hooksMap
}
