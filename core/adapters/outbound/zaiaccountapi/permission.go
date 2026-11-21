package zaiaccountapi

import (
	"irrlicht/core/domain/agent"
	"irrlicht/core/domain/permission"
)

// Name is the pseudo-adapter this permission is declared on. No coding-agent
// adapter is named for Z.ai, and agent.AccountAPIPermissionKey's doc requires
// a provider's account-quota permission on its own pseudo-adapter.
const Name = "zai-account-api"

// PermissionDeclaration returns the consent declaration for the Z.ai quota:
// a daemon-wide entry in the gastown/kitty/muse-account-api shape, separate
// from the endpoint-observation permission that finds the route. The
// endpoint permission reads only ANTHROPIC_BASE_URL; this one covers the
// credential beside it and the request it is sent with.
func PermissionDeclaration(apply, remove func() error) agent.Agent {
	return agent.Agent{
		Identity: agent.Identity{Name: Name, DisplayName: "Z.ai coding plan quota"},
		Permissions: []agent.Permission{{
			Key:             agent.AccountAPIPermissionKey,
			Kind:            permission.KindObserve,
			Title:           "Read Z.ai coding plan quota",
			FeatureUnlocked: "GLM Coding Plan usage in the quota chip of a session routed to api.z.ai",
			Touches: "Reads the Z.ai key (env.ANTHROPIC_AUTH_TOKEN, else env.ANTHROPIC_API_KEY) from the " +
				"Claude Code settings file that routed a session to api.z.ai, and sends it, once per " +
				"poll, to GET https://api.z.ai/api/monitor/usage/quota/limit",
			Detail: "Only a session whose observed route names the host api.z.ai is polled, and its key " +
				"is read only from the settings file that set that route — managed-settings.json or a " +
				"managed-settings.d drop-in, .claude/settings.local.json, .claude/settings.json, or " +
				"~/.claude/settings.json — and only while that same file still names api.z.ai. A key " +
				"that exists only in the process environment is not read. The key is never stored, " +
				"logged or shown; sessions using the same key share one poll, matched by a salted " +
				"fingerprint that exists only in memory. Toggling off stops every further key read " +
				"and every further request to api.z.ai immediately, and discards cached readings.",
			Apply:  apply,
			Remove: remove,
		}},
	}
}
