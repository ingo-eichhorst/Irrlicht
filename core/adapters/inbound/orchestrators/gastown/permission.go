// permission.go declares Gas Town's consent surface (issue #570). The
// orchestrator isn't a coding-agent adapter — it has no Source/Process
// axes — but it reads the user's ~/gt state and execs the gt binary, so
// its monitoring is consent-gated exactly like the agent adapters'.
package gastown

import (
	"irrlicht/core/domain/agent"
	"irrlicht/core/domain/permission"
)

// Name is the orchestrator identifier used on events, in the permission
// store, and by the wizard.
const Name = "gastown"

// PermissionKeyState gates all Gas Town monitoring.
const PermissionKeyState = "state"

// RootDetected reports whether a Gas Town root exists (GT_ROOT env var or
// ~/gt) — a stat-only probe with no file reads, safe before consent. The
// permission service uses it as the wizard's detection signal; the full
// adapter, whose construction reads state files, is only built on grant.
func RootDetected() bool {
	root := resolveRoot()
	return root != "" && isGasTownRoot(root)
}

// PermissionDeclaration returns the consent declaration for Gas Town.
// start/stop are wired by the daemon: they construct the adapter (whose
// construction reads ~/gt) and start/stop its watcher.
func PermissionDeclaration(start, stop func() error) agent.Agent {
	return agent.Agent{
		Identity: agent.Identity{Name: Name, DisplayName: "Gas Town"},
		Permissions: []agent.Permission{{
			Key:             PermissionKeyState,
			Kind:            permission.KindObserve,
			Title:           "Read Gas Town state",
			FeatureUnlocked: "Orchestrator view: rigs, polecats, and convoy status",
			Touches:         "Reads ~/gt (daemon/state.json, rigs.json) and runs `gt … list` subcommands",
			Detail: "Watches Gas Town's daemon/state.json and rigs.json under " +
				"GT_ROOT (default ~/gt) and periodically runs read-only " +
				"`gt rig/polecat/dog/boot list` commands to populate the " +
				"orchestrator view. Toggling off stops all reading and gt " +
				"invocations immediately.",
			Apply:  start,
			Remove: stop,
		}},
	}
}
