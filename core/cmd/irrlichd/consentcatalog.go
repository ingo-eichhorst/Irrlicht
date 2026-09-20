package main

import (
	"irrlicht/core/adapters/inbound/agents"
	"irrlicht/core/adapters/inbound/agents/processlifecycle"
	gastownadapter "irrlicht/core/adapters/inbound/orchestrators/gastown"
	"irrlicht/core/adapters/outbound/museaccountapi"
	"irrlicht/core/domain/agent"
)

// consentCatalog returns every declaration the permission wizard offers: the
// agent adapters in agents.All() plus five daemon-wide entries with no
// Source/Process axes — the Gas Town orchestrator (reads ~/gt), launcher-identity
// capture (reads whitelisted env vars from agent processes for click-to-focus),
// the kitty remote-control config patch (writes kitty.conf for tab-precise
// click-to-focus, #425), endpoint-route observation (reads a base-URL
// override env var for #1994's resolver, #2002), and Muse's account-quota
// permission (issue #2007 — the credential read plus the api.meta.ai egress,
// covered together per agent.AccountAPIPermissionKey's own doc comment).
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
// the lowest package that already sees all five.
//
// startGastown/stopGastown are Gas Town's grant/revoke effects, and
// startMuseAccountAPI/stopMuseAccountAPI are Muse's (museaccountapi_effects.go's
// museAccountAPIEffects) — both only the daemon's runtime wiring can build.
// Callers that read declarations rather than exercise them pass no-ops — see
// declaredConsentCatalog.
func consentCatalog(all []agent.Agent, startGastown, stopGastown, startMuseAccountAPI, stopMuseAccountAPI func() error) []agent.Agent {
	return append(append([]agent.Agent{}, all...),
		gastownadapter.PermissionDeclaration(startGastown, stopGastown),
		processlifecycle.LauncherPermissionDeclaration(),
		processlifecycle.KittyPermissionDeclaration(),
		processlifecycle.EndpointPermissionDeclaration(),
		museaccountapi.PermissionDeclaration(startMuseAccountAPI, stopMuseAccountAPI))
}

// declaredConsentCatalog is consentCatalog for the flag paths, which read the
// DECLARATIONS — paths, keys, uninstallers — and never exercise Gas Town's or
// Muse's start/stop effects. Those closures need daemon-only state (Gas
// Town: the session repository and orchestrator monitor; Muse: the
// daemon-wide AccountPoller), none of which `--print-managed-files` or
// `--uninstall-hooks` builds; passing no-ops keeps the catalog they project
// identical to the one the wizard offers without standing a daemon up.
func declaredConsentCatalog() []agent.Agent {
	noop := func() error { return nil }
	return consentCatalog(agents.All(), noop, noop, noop, noop)
}
