// hookdisclosure_test.go wires the Codex hooks permission into the shared
// issue #1356 contract. Codex's copy was already accurate when the contract
// landed, so this is a lock rather than a defect test — it is here because the
// contract's value is being wired by every hook-installing adapter, not only
// the one that was caught drifting.
package codex

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
