// hookdisclosure_test.go wires the Gemini CLI hooks permission into the
// shared issue #1356 contract: the consent copy names every event the
// installer writes, states the right entry count, and names no event it does
// not install. This is a LOCK — the copy is derived from installedHookEvents
// via hookjson.EntriesTouched/EventList rather than hand-written, so it
// passes by construction; the contract is what keeps it that way when the
// event set changes.
package geminicli

import (
	"testing"

	"irrlicht/core/internal/contracttesting"
)

func TestHooksPermission_DisclosureMatchesInstalled(t *testing.T) {
	contracttesting.AssertHookDisclosureMatchesInstalled(t, contracttesting.HookDisclosure{
		Agent:         Agent(),
		PermissionKey: PermissionKeyHooks,
		Installed:     installedHookEvents,
		// No NonEventTerms: unlike copilot's "GitHub" (which is genuinely
		// two-hump CamelCase — Git+Hub — and so matches the contract's
		// eventShapedToken shape), nothing in this adapter's consent copy is
		// a two-or-more-word CamelCase token that is not an actual installed
		// event name. "Gemini" and "CLI" are each a single hump.
	})
}
