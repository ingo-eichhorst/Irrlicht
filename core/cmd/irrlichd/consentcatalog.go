package main

import (
	"irrlicht/core/adapters/inbound/agents"
	"irrlicht/core/adapters/inbound/agents/processlifecycle"
	gastownadapter "irrlicht/core/adapters/inbound/orchestrators/gastown"
	"irrlicht/core/domain/agent"
)

// consentCatalog returns every declaration the permission wizard offers: the
// agent adapters in agents.All() plus three daemon-wide entries with no
// Source/Process axes — the Gas Town orchestrator (reads ~/gt), launcher-identity
// capture (reads whitelisted env vars from agent processes for click-to-focus),
// and the kitty remote-control config patch (writes kitty.conf for tab-precise
// click-to-focus, #425).
//
// It exists so the composition lives in ONE place (#1383). This list is what
// IRRLICHT_PERMISSION_MODE=grant-all grants, so it is also the set whose Apply
// closures a recording daemon runs against the user's real $HOME — which makes
// it the correct input to agents.ManagedUserFiles. Building it separately in
// setupPermissionService and in the flag paths is how the kitty config patch
// came to be offered by the wizard while being invisible to both `--uninstall-hooks`
// and the recorder's protected file set.
//
// Package agents cannot compose this itself: processlifecycle imports agents,
// so agents importing processlifecycle back would be an import cycle. This is
// the lowest package that already sees all four.
//
// startGastown/stopGastown are Gas Town's grant/revoke effects, which only the
// daemon's runtime wiring can build. Callers that read declarations rather than
// exercise them pass no-ops — see declaredConsentCatalog.
func consentCatalog(all []agent.Agent, startGastown, stopGastown func() error) []agent.Agent {
	return append(append([]agent.Agent{}, all...),
		gastownadapter.PermissionDeclaration(startGastown, stopGastown),
		processlifecycle.LauncherPermissionDeclaration(),
		processlifecycle.KittyPermissionDeclaration())
}

// declaredConsentCatalog is consentCatalog for the flag paths, which read the
// DECLARATIONS — paths, keys, uninstallers — and never exercise Gas Town's
// start/stop effects. Those closures need the daemon's session repository and
// orchestrator monitor, neither of which `--print-managed-files` or
// `--uninstall-hooks` builds; passing no-ops keeps the catalog they project
// identical to the one the wizard offers without standing a daemon up.
func declaredConsentCatalog() []agent.Agent {
	noop := func() error { return nil }
	return consentCatalog(agents.All(), noop, noop)
}
