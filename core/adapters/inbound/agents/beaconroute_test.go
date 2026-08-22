package agents

import (
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
// Scope, stated honestly: this pins the receivers that exist (ten as of
// #1723's antigravity row; #1721's pi row also added the mistral-vibe row
// #1718 never wrote — exactly the omission the paragraph below predicts, and
// the whole of issue #1759). It cannot fail for a NEW receiver registered at a
// different prefix, because registerHookRoutes (core/cmd/irrlichd/startup.go)
// is hand-wired with no registry to enumerate and this map is hand-written.
// Whoever adds the next receiver adds its row here; until then the
// convention is pinned, not enforced.
func TestBeaconEndpointPathMatchesTheInstalledReceivers(t *testing.T) {
	for segment, want := range map[string]string{
		"antigravity": antigravity.HookEndpointPath,
		"claudecode":  claudecode.HookEndpointPath,
		"codex":       codex.HookEndpointPath,
		"copilot":     copilot.HookEndpointPath,
		"gemini-cli":  geminicli.HookEndpointPath,
		"hermes":      hermes.HookEndpointPath,
		"kiro-cli":    kirocli.HookEndpointPath,
		// mistral-vibe was beacon-delivered from the day it shipped (#1718)
		// but was never added here; found while adding pi's row (#1721).
		"mistral-vibe": vibe.HookEndpointPath,
		"opencode":     opencode.HookEndpointPath,
		"pi":           pi.HookEndpointPath,
	} {
		if got := hookbeacon.EndpointPath(segment); got != want {
			t.Errorf("hookbeacon.EndpointPath(%q) = %q, but the receiver is registered at %q — the beacon would post into a route nothing serves", segment, got, want)
		}
	}
}
