package agents

import (
	"fmt"
	"testing"

	"irrlicht/core/adapters/inbound/agents/antigravity"
	"irrlicht/core/adapters/inbound/agents/claudecode"
	"irrlicht/core/adapters/inbound/agents/codex"
	"irrlicht/core/adapters/inbound/agents/copilot"
	"irrlicht/core/adapters/inbound/agents/geminicli"
	"irrlicht/core/adapters/inbound/agents/hermes"
	"irrlicht/core/adapters/inbound/agents/kirocli"
	"irrlicht/core/adapters/inbound/agents/opencode"
	"irrlicht/core/adapters/inbound/agents/pi"
	"irrlicht/core/adapters/inbound/agents/vibe"
	"irrlicht/core/pkg/hookbeacon"
)

// TestBeaconEndpointPathMatchesTheInstalledReceivers pins the one convention
// the beacon relies on and cannot see from inside core/pkg: that a hook
// receiver is registered at /api/v1/hooks/<segment>, where <segment> is the
// argument an adapter would pass to `irrlichd hook-post`.
//
// hookbeacon composes that route from a prefix rather than each adapter
// exporting a second constant, so this test is what keeps the two from
// drifting. Without it, a receiver registered somewhere else would leave the
// beacon POSTing into a 404 — which, because the beacon exits 0 by design, is
// a failure with no symptom at all on the agent's side.
//
// Note the segment is NOT AdapterName: claudecode.AdapterName is "claude-code"
// while its route segment is "claudecode". That is why beaconRoutes' keys
// below are written out literally rather than derived from the adapter
// constants.
//
// # #1759: a hand-typed map can't fail for a row nobody added
//
// beaconRoutes used to be an inline map only this test read, and a test that
// only ranges over a map's own rows can only ever be wrong about a row that
// IS in the map — it has no way to notice a row that was never added.
// mistral-vibe's row went missing exactly that way, from #1718 (which shipped
// the receiver) through #1721 (which added pi's row and found the gap):
// registerHookRoutes (core/cmd/irrlichd/startup.go) registered mistral-vibe's
// receiver the whole time, and this test passed the whole time, one adapter
// short of what it claimed to cover.
//
// TestBeaconRouteMapAccountsForEveryHooksAdapter closes that gap: it ties
// len(beaconRoutes) to agents.HookConfigs(agents.All()) — the registry every
// hooks-installing adapter is already required to appear in, since declaring
// agent.HooksPermissionKey is what gets it consent-gating and
// `--uninstall-hooks` at all (see managedfiles.go). A row that is never added
// now makes the two counts disagree, which is a failing assertion instead of
// a smaller loop that silently passes. beaconRouteCompletenessReason carries
// that guard's logic and is self-tested against a synthetic dropped row in
// TestBeaconRouteCompletenessReason — the committed mutation evidence a new
// guard owes in place of a "before the fix" state that cannot exist for it
// (AGENTS.md's Testing section).
//
// Scope, stated honestly: this still pins each receiver's route by a literal
// string, so a row present with the WRONG value is caught only by the
// per-segment loop below, and the completeness guard cannot invent the
// correct <segment, path> pair for a row that is missing — only insist one be
// added. What it removes is the failure #1759 found: a missing row reading as
// a clean sweep.
func TestBeaconEndpointPathMatchesTheInstalledReceivers(t *testing.T) {
	for segment, want := range beaconRoutes {
		if got := hookbeacon.EndpointPath(segment); got != want {
			t.Errorf("hookbeacon.EndpointPath(%q) = %q, but the receiver is registered at %q — the beacon would post into a route nothing serves", segment, got, want)
		}
	}
}

// beaconRoutes is every beacon-reachable receiver's segment mapped to the
// literal HTTP path registerHookRoutes (core/cmd/irrlichd/startup.go)
// registers it at. Whoever adds the next receiver adds its row here — see
// TestBeaconRouteMapAccountsForEveryHooksAdapter for what now enforces that
// they did.
var beaconRoutes = map[string]string{
	"antigravity": antigravity.HookEndpointPath,
	"claudecode":  claudecode.HookEndpointPath,
	"codex":       codex.HookEndpointPath,
	"copilot":     copilot.HookEndpointPath,
	"gemini-cli":  geminicli.HookEndpointPath,
	"hermes":      hermes.HookEndpointPath,
	"kiro-cli":    kirocli.HookEndpointPath,
	// mistral-vibe was beacon-delivered from the day it shipped (#1718) but
	// was never added here; found while adding pi's row (#1721), and the
	// reason issue #1759 exists.
	"mistral-vibe": vibe.HookEndpointPath,
	"opencode":     opencode.HookEndpointPath,
	"pi":           pi.HookEndpointPath,
}

// TestBeaconRouteMapAccountsForEveryHooksAdapter is #1759's completeness
// guard: it fails whenever beaconRoutes' size disagrees with the number of
// agents.All() adapters that declare agent.HooksPermissionKey, rather than
// only checking the rows someone remembered to type in. See the file comment
// on TestBeaconEndpointPathMatchesTheInstalledReceivers for why that is the
// gap #1759 found and how this closes it.
func TestBeaconRouteMapAccountsForEveryHooksAdapter(t *testing.T) {
	cfgs, err := HookConfigs(All())
	if err != nil {
		t.Fatalf("HookConfigs(All()): %v", err)
	}
	if reason := beaconRouteCompletenessReason(len(beaconRoutes), len(cfgs)); reason != "" {
		t.Fatal(reason)
	}
}

// beaconRouteCompletenessReason reports why mapSize (beaconRoutes' entry
// count) does not account for registryHookAdapters (the number of adapters
// agents.HookConfigs(agents.All()) reports), or "" when it does.
//
// It refuses on an empty derivation rather than reading that as a clean sweep
// — a registry walk that finds nothing is indistinguishable, by count alone,
// from a map that legitimately has nothing to account for, and AGENTS.md's
// Testing section (and #1759 itself) asks the two to never read the same.
func beaconRouteCompletenessReason(mapSize, registryHookAdapters int) string {
	if registryHookAdapters == 0 {
		return "agents.HookConfigs(agents.All()) reports zero hook-installing adapters — a registry walk that finds nothing must fail loudly rather than pass as if there were nothing to check"
	}
	if mapSize != registryHookAdapters {
		return fmt.Sprintf("beaconRoutes has %d entries but agents.HookConfigs(agents.All()) reports %d adapters declaring agent.HooksPermissionKey — a hook adapter was added (or removed) without updating beaconRoutes' rows to match", mapSize, registryHookAdapters)
	}
	return ""
}

// TestBeaconRouteCompletenessReason is the committed mutation evidence for
// beaconRouteCompletenessReason. A new guard has no pre-fix "before" state to
// run red (AGENTS.md's Testing section), so this mutates what it protects
// instead — a map one row short of the registry, exactly mistral-vibe's shape
// in #1759 — and confirms the guard reddens there, and that a 0-vs-0 count
// does not vacuously read as complete.
func TestBeaconRouteCompletenessReason(t *testing.T) {
	if reason := beaconRouteCompletenessReason(len(beaconRoutes)-1, len(beaconRoutes)); reason == "" {
		t.Fatal("beaconRouteCompletenessReason(len-1, len) = \"\", want a non-empty reason — a map one row short of the registry must not read as complete")
	}
	if reason := beaconRouteCompletenessReason(len(beaconRoutes), len(beaconRoutes)); reason != "" {
		t.Fatalf("beaconRouteCompletenessReason(len, len) = %q, want \"\" — a map matching the registry's count must not be flagged", reason)
	}
	if reason := beaconRouteCompletenessReason(0, 0); reason == "" {
		t.Fatal("beaconRouteCompletenessReason(0, 0) = \"\", want a non-empty reason — an empty registry derivation must fail loudly, not read as a vacuous match")
	}
}
