// hookdisclosure_test.go wires the Claude Code hooks permission into the
// shared issue #1356 contract: the consent copy the user reads before granting
// names every event installedHookEvents installs, and no event it doesn't.
// This adapter is where the contract was found failing — the copy declared six
// entries against a seven-event install (#1173's Notification hook).
package claudecode

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
