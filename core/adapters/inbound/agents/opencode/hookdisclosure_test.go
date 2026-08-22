// hookdisclosure_test.go wires the opencode hooks permission into the shared
// issue #1356 contract: the consent copy names every event the installer
// subscribes to, states the right entry count, and names no event it does not
// install.
package opencode

import (
	"testing"

	"irrlicht/core/internal/contracttesting"
)

func TestHooksPermission_DisclosureMatchesInstalled(t *testing.T) {
	contracttesting.AssertHookDisclosureMatchesInstalled(t, contracttesting.HookDisclosure{
		Agent:         Agent(),
		PermissionKey: PermissionKeyHooks,
		Installed:     installedHookEvents,
		// "JavaScript" is a two-hump CamelCase word, so the over-promise arm
		// reads it as an event-shaped token. It is exempted rather than avoided
		// because naming the artifact's LANGUAGE is the single most load-bearing
		// sentence in this permission's copy: what is installed is executable
		// code rather than a config entry, and the #570 contract is that the
		// user reads what is actually done.
		NonEventTerms: []string{"JavaScript"},
	})
}
