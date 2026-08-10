// hookdisclosure_test.go wires the Copilot hooks permission into the shared
// issue #1356 contract: the consent copy names every event the installer
// writes, states the right entry count, and names no event it does not
// install. This is a LOCK — the copy is derived from installedHookEvents via
// hookjson.EntriesTouched/EventList rather than hand-written, so it passes by
// construction; the contract is what keeps it that way when the event set
// changes.
package copilot

import (
	"testing"

	"irrlicht/core/internal/contracttesting"
)

func TestHooksPermission_DisclosureMatchesInstalled(t *testing.T) {
	contracttesting.AssertHookDisclosureMatchesInstalled(t, contracttesting.HookDisclosure{
		Agent:         Agent(),
		PermissionKey: PermissionKeyHooks,
		Installed:     installedHookEvents,
		// "GitHub" is CamelCase by the contract's token rule but is a vendor
		// name, not a hook event — it reaches the copy through displayName in
		// hookjson.RequiresVersion.
		NonEventTerms: []string{"GitHub"},
	})
}
