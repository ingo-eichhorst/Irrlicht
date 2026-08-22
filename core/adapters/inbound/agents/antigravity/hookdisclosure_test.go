// hookdisclosure_test.go wires this adapter's hooks permission into the shared
// issue #1356 contract: the consent copy names every event the installer
// registers, states the right entry count, and names no event it does not
// install.
package antigravity

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
