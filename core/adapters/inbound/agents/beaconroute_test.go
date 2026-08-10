package agents

import (
	"testing"

	"irrlicht/core/adapters/inbound/agents/claudecode"
	"irrlicht/core/adapters/inbound/agents/codex"
	"irrlicht/core/adapters/inbound/agents/copilot"
	"irrlicht/core/pkg/hookbeacon"
)

// TestBeaconEndpointPathMatchesTheInstalledReceivers pins the one convention the
// beacon relies on and cannot see from inside core/pkg: that a hook receiver is
// registered at /api/v1/hooks/<segment>, where <segment> is the argument an
// adapter would pass to `irrlichd hook-post`.
//
// hookbeacon composes that route from a prefix rather than each adapter
// exporting a second constant, so this test is what keeps the two from drifting.
// Without it, a receiver registered somewhere else would leave the beacon POSTing
// into a 404 — which, because the beacon exits 0 by design, is a failure with no
// symptom at all on the agent's side.
//
// Note the segment is NOT AdapterName: claudecode.AdapterName is "claude-code"
// while its route segment is "claudecode". That is why the map keys below are
// written out literally rather than derived from the adapter constants.
//
// Scope, stated honestly: this pins the two receivers that exist. It cannot fail
// for a THIRD receiver registered at a different prefix, because
// registerHookRoutes (core/cmd/irrlichd/startup.go) is hand-wired with no
// registry to enumerate and this map is hand-written. Whoever adds the third
// receiver adds its row here; until then the convention is pinned, not
// enforced.
func TestBeaconEndpointPathMatchesTheInstalledReceivers(t *testing.T) {
	for segment, want := range map[string]string{
		"claudecode": claudecode.HookEndpointPath,
		"codex":      codex.HookEndpointPath,
		"copilot":    copilot.HookEndpointPath,
	} {
		if got := hookbeacon.EndpointPath(segment); got != want {
			t.Errorf("hookbeacon.EndpointPath(%q) = %q, but the receiver is registered at %q — the beacon would post into a route nothing serves", segment, got, want)
		}
	}
}
