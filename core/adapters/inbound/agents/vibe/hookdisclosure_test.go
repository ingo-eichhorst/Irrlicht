// hookdisclosure_test.go wires the Mistral Vibe hooks permission into the
// shared issue #1356 contract: the consent copy names every event the
// installer writes, states the right entry count, and names no event it
// does not install.
package vibe

import (
	"testing"

	"irrlicht/core/internal/contracttesting"
)

func TestHooksPermission_DisclosureMatchesInstalled(t *testing.T) {
	// No Installed: with variants declared (#2171), the contract grades the
	// union of every variant's Events and each variant's own entry count.
	contracttesting.AssertHookDisclosureMatchesInstalled(t, contracttesting.HookDisclosure{
		Agent:         Agent(),
		PermissionKey: PermissionKeyHooks,
		// No NonEventTerms: "Vibe" and "Mistral" are each single-hump words,
		// and every other identifier the copy names either matches an
		// installed event (post_agent_turn, post_agent) or is not event-shaped
		// at all (enable_experimental_hooks, hook-post, mistral-vibe,
		// UV_TOOL_DIR).
	})
}
