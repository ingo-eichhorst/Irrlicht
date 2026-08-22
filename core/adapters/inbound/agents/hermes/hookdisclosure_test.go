// hookdisclosure_test.go wires the hermes hooks permission into the shared
// issue #1356 contract: the consent copy names every event the installer
// subscribes to, states the right entry count, and names no event it does not
// install.
package hermes

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
