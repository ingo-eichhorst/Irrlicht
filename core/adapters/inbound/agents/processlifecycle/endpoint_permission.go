// endpoint_permission.go declares the consent surface for issue #2002's
// endpoint-route observation. It is a separate daemon-wide entry rather than
// a key inside LauncherPermissionDeclaration or an adapter's own observe
// permission: declaration.go's Permission doc states that at most one
// observe-kind permission per adapter is ever consulted, so a key added
// inside claudecode's existing observe permission would never gate anything,
// and folding it into the launcher entry would widen "terminal focus"
// consent to cover billing-adjacent observation while its wizard text keeps
// promising click-to-focus. The launcher entry is already a non-adapter,
// daemon-wide capability, so this needs no new mechanism — only a sibling
// entry beside it (#2002 §9 question 1, option (a)).
package processlifecycle

import (
	"irrlicht/core/domain/agent"
	"irrlicht/core/domain/permission"
)

// EndpointName identifies the endpoint-route pseudo-entry in the permission
// store and wizard.
const EndpointName = "endpoint"

// PermissionKeyEndpointEnv gates the endpoint-route env capture.
const PermissionKeyEndpointEnv = "env"

// EndpointPermissionDeclaration returns the consent declaration for
// endpoint-route observation. Like LauncherPermissionDeclaration it isn't a
// coding-agent adapter (no Source/Process axes) — it's a daemon-wide
// capability gated through the same wizard, and it reads its OWN key set
// (endpointEnvKeys), disjoint from launcherEnvKeys: granting one never
// exposes the other's values (#2002 §1.1).
func EndpointPermissionDeclaration() agent.Agent {
	return agent.Agent{
		Identity: agent.Identity{Name: EndpointName, DisplayName: "Provider endpoint"},
		Permissions: []agent.Permission{{
			Key:             PermissionKeyEndpointEnv,
			Kind:            permission.KindObserve,
			Title:           "Observe provider endpoint",
			FeatureUnlocked: "Shows which API endpoint a session's requests are actually routed through, alongside its usage",
			Touches:         "Reads a base-URL override variable from detected agent processes (ANTHROPIC_BASE_URL)",
			Detail: "When a session is linked to its process, irrlicht reads only " +
				"ANTHROPIC_BASE_URL from that process's environment — never the full " +
				"environment, and never a credential or API-key variable. Before the " +
				"value is stored or shown, irrlicht removes any username, password, " +
				"query string, and fragment from it, keeping only the scheme, host, " +
				"port, and path. A loopback or private-network address (e.g. " +
				"127.0.0.1) is shown as local, which proves only that the address is " +
				"on the local network — it does not prove the request stays local or " +
				"costs nothing, since a local gateway can still forward it to a paid " +
				"service elsewhere. This is separate from Terminal focus: turning it " +
				"off stops endpoint capture without affecting click-to-focus, and " +
				"turning off Terminal focus does not grant or affect this.",
		}},
	}
}
