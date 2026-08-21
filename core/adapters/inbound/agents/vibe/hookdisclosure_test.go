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
	contracttesting.AssertHookDisclosureMatchesInstalled(t, contracttesting.HookDisclosure{
		Agent:         Agent(),
		PermissionKey: PermissionKeyHooks,
		Installed:     installedHookEvents,
		// No NonEventTerms: "Vibe" and "Mistral" are each single-hump words,
		// and every other identifier the copy names either matches an
		// installed event (post_agent_turn) or is not event-shaped at all
		// (enable_experimental_hooks, hook-post, mistral-vibe).
	})
}
