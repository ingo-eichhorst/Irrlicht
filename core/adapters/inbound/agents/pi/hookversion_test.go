// hookversion_test.go wires the pi hooks permission into the shared issue
// #1365 contract.
//
// Like geminicli and vibe, this adapter declares no Observed version source:
// nothing in a pi transcript carries a CLI version field this adapter's
// parser reads, so the gate falls straight through to Probe
// (`pi --version`), which cliversion runs itself.
package pi

import (
	"testing"

	"irrlicht/core/domain/agent"
	"irrlicht/core/internal/contracttesting"
)

func TestHooksPermission_DeclaresVersionGate(t *testing.T) {
	contracttesting.AssertHookVersionGate(t, contracttesting.HookVersionGate{
		Agent:         Agent(),
		PermissionKey: PermissionKeyHooks,
		Installed:     installedHookEvents,
		Since:         hookEventSince,
	})
}

// hooksVersionGate returns the declared gate, read off the real registration
// the daemon consumes rather than a copy.
func hooksVersionGate(t *testing.T) *agent.VersionGate {
	t.Helper()
	for _, p := range Agent().Permissions {
		if p.Key == PermissionKeyHooks {
			if p.Writes == nil || p.Writes.Version == nil {
				t.Fatal("hooks permission declares no version gate")
			}
			return p.Writes.Version
		}
	}
	t.Fatal("no hooks permission")
	return nil
}

// What is deliberately NOT here: BelowFloorSaysWhy, AtOrAboveFloorInstalls
// and UnknownVersionFailsOpen.
//
// Five sibling adapters (claudecode, codex, geminicli, kirocli, vibe) each
// carry hand-written versions of those three beside their
// AssertHookVersionGate call, and #1721 added a sixth copy before measuring
// what the shared contract already asserts. It asserts all three, and more
// thoroughly than the copies did:
//
//   - assertFloorRefusesOlder checks gate.Permits(gate.Min) is ALLOWED (the
//     at-or-above lock), then refuses THREE field-wise predecessors — major,
//     minor and patch decremented independently, because a single synthetic
//     "just below" value exercises only the borrow path — and requires each
//     refusal's reason to name both versions. The hand-written copy picked
//     ONE older version by hand.
//   - It also carries a vacuity guard the copies had no equivalent of: a
//     floor with nothing below it fails, rather than passing having asserted
//     only that the gate permits itself.
//   - assertUnknownFailsOpen drives THREE unreadable spellings ("",
//     "not a version", "2.1"). The hand-written copy drove one.
//
// So the copies were strictly weaker restatements of obligations already
// running one call above, which is the hand-maintained restatement this
// repo's own testing philosophy exists to remove. Deleting them here
// increases coverage rather than reducing it. The same three are redundant
// in all five siblings; removing them there is a follow-up rather than
// something a pi ticket should do to five other adapters' test files.
//
// TestHooksGate_ProbeIsPiVersion below stays, because it is the one thing
// the contract does NOT cover: assertVersionSourceDeclared only requires
// Observed or Probe to be non-empty, never that the argv is the right one.

// TestHooksGate_ProbeIsPiVersion pins the argv this adapter asks cliversion
// to run — `pi --version` prints a bare "0.83.0" (live-run against the
// installed CLI during this issue's audit).
func TestHooksGate_ProbeIsPiVersion(t *testing.T) {
	gate := hooksVersionGate(t)
	want := []string{"pi", "--version"}
	if len(gate.Probe) != len(want) {
		t.Fatalf("Probe = %v, want %v", gate.Probe, want)
	}
	for i := range want {
		if gate.Probe[i] != want[i] {
			t.Fatalf("Probe = %v, want %v", gate.Probe, want)
		}
	}
}
