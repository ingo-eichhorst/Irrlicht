package museaccountapi

import (
	"irrlicht/core/domain/agent"
	"irrlicht/core/domain/permission"
)

// Name is the pseudo-adapter identifier this permission is declared on —
// distinct from "muse" (the coding-agent adapter's own Identity.Name,
// core/adapters/inbound/agents/muse), per
// agent.AccountAPIPermissionKey's own doc comment: "a provider's
// account-quota permission is declared on its OWN pseudo-adapter Agent
// value ... never as a second Permission entry on the provider's own
// agent.Agent, where it would silently never be consulted."
const Name = "muse-account-api"

// PermissionDeclaration returns the consent declaration for Muse's
// account-quota permission — the gastown/kitty shape
// (core/cmd/irrlichd/consentcatalog.go's own doc comment): a daemon-wide
// entry with no Source/Process axes, KindObserve, non-nil Apply/Remove built
// by the daemon's own wiring (core/cmd/irrlichd) with whatever it has that
// this package cannot construct itself (an *services.AccountPoller instance
// and, eventually, a session repository).
func PermissionDeclaration(apply, remove func() error) agent.Agent {
	return agent.Agent{
		Identity: agent.Identity{Name: Name, DisplayName: "Muse account quota"},
		Permissions: []agent.Permission{{
			Key:             agent.AccountAPIPermissionKey,
			Kind:            permission.KindObserve,
			Title:           "Read Muse account quota",
			FeatureUnlocked: "Muse subscription usage in the session quota chip",
			Touches: "Reads the Muse OAuth credential (~/.config/muse/auth.json, or the " +
				"ai.meta.dev.credentials/meta macOS Keychain item when the login uses " +
				"Keychain storage) and sends it, once per poll, to " +
				"POST https://api.meta.ai/muse-code/key — the same endpoint the Muse CLI " +
				"itself calls at startup and for its own /usage panel.",
			Detail: "Covers both the credential read and the one outbound destination " +
				"together as one consent row, per issue #2003's own reviewed shape " +
				"(core/domain/agent.AccountAPIPermissionKey). Toggling off stops every " +
				"further credential read and every further request to " +
				"api.meta.ai immediately, and discards any cached quota reading.",
			Apply:  apply,
			Remove: remove,
		}},
	}
}
