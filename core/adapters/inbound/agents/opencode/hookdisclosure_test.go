// hookdisclosure_test.go wires the opencode hooks permission into the shared
// issue #1356 contract: the consent copy names every event the installer
// subscribes to, states the right entry count, and names no event it does not
// install. The permission declares one artifact per opencode major (#2108), so
// the contract reads each artifact's events off the declaration and holds the
// copy to both counts and every artifact's version bounds.
package opencode

import (
	"testing"

	"irrlicht/core/internal/contracttesting"
)

func TestHooksPermission_DisclosureMatchesInstalled(t *testing.T) {
	contracttesting.AssertHookDisclosureMatchesInstalled(t, contracttesting.HookDisclosure{
		Agent:         Agent(),
		PermissionKey: PermissionKeyHooks,
		// "JavaScript" is a two-hump CamelCase word, so the over-promise arm
		// reads it as an event-shaped token. It is exempted rather than avoided
		// because naming the artifact's LANGUAGE is the single most load-bearing
		// sentence in this permission's copy: what is installed is executable
		// code rather than a config entry, and the #570 contract is that the
		// user reads what is actually done.
		NonEventTerms: []string{"JavaScript"},
	})
}
