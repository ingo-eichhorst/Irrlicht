package agents

import (
	"testing"

	"irrlicht/core/adapters/inbound/agents/claudecode"
	"irrlicht/core/adapters/inbound/agents/codex"
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
// while its route segment is "claudecode". That mismatch is exactly the kind of
// thing this test exists to catch when a third receiver is added, so the map keys
// below are written out literally rather than derived from the adapter constants.
func TestBeaconEndpointPathMatchesTheInstalledReceivers(t *testing.T) {
	for segment, want := range map[string]string{
		"claudecode": claudecode.HookEndpointPath,
		"codex":      codex.HookEndpointPath,
	} {
		if got := hookbeacon.EndpointPath(segment); got != want {
			t.Errorf("hookbeacon.EndpointPath(%q) = %q, but the receiver is registered at %q — the beacon would post into a route nothing serves", segment, got, want)
		}
	}
}
