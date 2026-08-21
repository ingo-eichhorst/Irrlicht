// hookdisclosure_test.go wires the kiro-cli hooks permission into the shared
// issue #1356 contract: the consent copy names every event the installer
// writes, states the right entry count, and names no event it does not
// install.
package kirocli

import (
	"testing"

	"irrlicht/core/internal/contracttesting"
)

func TestHooksPermission_DisclosureMatchesInstalled(t *testing.T) {
	contracttesting.AssertHookDisclosureMatchesInstalled(t, contracttesting.HookDisclosure{
		Agent:         Agent(),
		PermissionKey: PermissionKeyHooks,
		Installed:     installedHookEvents,
	})
}
