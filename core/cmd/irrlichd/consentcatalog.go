package main

import (
	"irrlicht/core/adapters/inbound/agents"
	"irrlicht/core/adapters/inbound/agents/claudecode"
	"irrlicht/core/adapters/inbound/agents/processlifecycle"
	gastownadapter "irrlicht/core/adapters/inbound/orchestrators/gastown"
	"irrlicht/core/adapters/inbound/orchestrators/workerlist"
	"irrlicht/core/adapters/outbound/museaccountapi"
	"irrlicht/core/adapters/outbound/zaiaccountapi"
	"irrlicht/core/domain/agent"
)

// consentCatalog returns every declaration the permission wizard offers: the
// agent adapters in agents.All() plus eight daemon-wide entries with no
// Source/Process axes — the Gas Town orchestrator (reads ~/gt), launcher-identity
// capture (reads whitelisted env vars from agent processes for click-to-focus),
// the kitty remote-control config patch (writes kitty.conf for tab-precise
// click-to-focus, #425), endpoint-route observation (reads a base-URL
// override env var for #1994's resolver, #2002), and Muse's account-quota
// permission (issue #2007 — the credential read plus the api.meta.ai egress,
// covered together per agent.AccountAPIPermissionKey's own doc comment), and
// Claude Code's account-identity read (issue #2095 — a pseudo-adapter, not a
// second permission on claude-code, whose revoke would stop transcript watching),
// and the worker-list orchestrator (issue #2148 — reads one status file),
// and Z.ai's coding-plan quota (issue #2010 — the key from the settings file
// that routed a session to api.z.ai, plus the api.z.ai egress).
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
// the lowest package that already sees all of them.
//
// fx carries the grant/revoke effects only the daemon's runtime wiring can
// build. Callers that read declarations rather than exercise them pass no-ops
// — see declaredConsentCatalog.
func consentCatalog(all []agent.Agent, fx consentEffects) []agent.Agent {
	return append(append([]agent.Agent{}, all...),
		gastownadapter.PermissionDeclaration(fx.StartGastown, fx.StopGastown),
		processlifecycle.LauncherPermissionDeclaration(),
		processlifecycle.KittyPermissionDeclaration(),
		processlifecycle.EndpointPermissionDeclaration(),
		museaccountapi.PermissionDeclaration(fx.StartMuseAccountAPI, fx.StopMuseAccountAPI),
		claudecode.AccountPermissionDeclaration(),
		workerlist.PermissionDeclaration(fx.StartWorkerlist, fx.StopWorkerlist),
		zaiaccountapi.PermissionDeclaration(fx.StartZaiAccountAPI, fx.StopZaiAccountAPI))
}

// consentEffects are the grant/revoke effects of the daemon-wide permissions
// whose effects need daemon-only state: Gas Town's and the worker list's
// (startup.go's gastownEffects/workerlistEffects — the session repository and
// the orchestrator monitor) and Muse's account-quota permission
// (museaccountapi_effects.go's museAccountAPIEffects — the daemon-wide
// AccountPoller), and Z.ai's (zaiaccountapi_effects.go — its poller).
type consentEffects struct {
	StartGastown, StopGastown               func() error
	StartMuseAccountAPI, StopMuseAccountAPI func() error
	StartWorkerlist, StopWorkerlist         func() error
	StartZaiAccountAPI, StopZaiAccountAPI   func() error
}

// declaredConsentCatalog is consentCatalog for the flag paths, which read the
// DECLARATIONS — paths, keys, uninstallers — and never exercise the
// consentEffects. Those closures need daemon-only state, none of which
// `--print-managed-files` or `--uninstall-hooks` builds; passing no-ops keeps
// the catalog they project identical to the one the wizard offers without
// standing a daemon up.
func declaredConsentCatalog() []agent.Agent {
	noop := func() error { return nil }
	return consentCatalog(agents.All(), consentEffects{
		StartGastown: noop, StopGastown: noop,
		StartMuseAccountAPI: noop, StopMuseAccountAPI: noop,
		StartWorkerlist: noop, StopWorkerlist: noop,
		StartZaiAccountAPI: noop, StopZaiAccountAPI: noop,
	})
}
