// hookversion_test.go wires the Mistral Vibe hooks permission into the
// shared issue #1365 contract.
//
// Like geminicli, this adapter declares no Observed version source: nothing
// in a Vibe transcript's meta.json sidecar carries a CLI version field this
// adapter's parser reads, so the gate falls straight through to Probe
// (`vibe --version`), which cliversion runs itself.
package vibe

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
		Since:         map[string]string{hookEventPostAgentTurn: minVibeVersion},
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
// and UnknownVersionFailsOpen — each a hand-picked restatement of an
// obligation AssertHookVersionGate already runs, strictly more weakly, the
// same pattern issue #1721 / PR #1758 found and removed from pi, and #1762
// removes here and from claudecode, codex, copilot, geminicli and kirocli:
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
// Proven by mutation, not by inspection: weakening minVibeVersion to 0.0.0
// reddens the contract —
//
//	--- FAIL: .../floor_refuses_an_older_cli
//	    hook_version.go:62: declared floor 0.0.0 has no version below it, so this
//	    obligation asserts nothing — a floor of 0.0.0 permits every CLI and is not a gate
//
// — while AtOrAboveFloorInstalls (the vacuity-blind lock) stayed GREEN under
// the same mutation, which is exactly the coverage gap deleting it closes.
//
// TestHooksGate_ProbeIsVibeVersion below stays, because it is the one thing
// the contract does NOT cover: assertVersionSourceDeclared only requires
// Observed or Probe to be non-empty, never that the argv is the right one.

// TestHooksGate_ProbeIsVibeVersion pins the argv this adapter asks
// cliversion to run — `vibe --version` prints "vibe X.Y.Z" (live-fired
// against the installed CLI during this issue's audit; the leading "vibe "
// word does not confuse cliversion's parser, which searches the whole
// string for a \d+\.\d+\.\d+ pattern rather than requiring an exact match).
func TestHooksGate_ProbeIsVibeVersion(t *testing.T) {
	gate := hooksVersionGate(t)
	want := []string{"vibe", "--version"}
	if len(gate.Probe) != len(want) {
		t.Fatalf("Probe = %v, want %v", gate.Probe, want)
	}
	for i := range want {
		if gate.Probe[i] != want[i] {
			t.Fatalf("Probe = %v, want %v", gate.Probe, want)
		}
	}
}
