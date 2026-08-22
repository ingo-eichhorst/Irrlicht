// hookversion_test.go wires the opencode hooks permission into the shared issue
// #1365 contract.
//
// Like geminicli, vibe and pi, this adapter declares no Observed version
// source: opencode's store carries no CLI version column this adapter's reader
// touches, so the gate falls straight through to Probe (`opencode --version`),
// which cliversion runs itself.
//
// What is deliberately NOT here — BelowFloorSaysWhy, AtOrAboveFloorInstalls and
// UnknownVersionFailsOpen — is issue #1762: six adapters carry hand-written
// copies of three obligations the shared contract strictly dominates. Adding a
// seventh copy would be adding to the thing #1762 exists to remove. pi's
// hookversion_test.go carries the measurement of why the contract is stronger.
package opencode

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

// TestHooksGate_ProbeIsOpenCodeVersion pins the argv this adapter asks
// cliversion to run. It is the one thing the shared contract does NOT cover:
// assertVersionSourceDeclared only requires Observed or Probe to be non-empty,
// never that the argv is the right one. `opencode --version` prints a bare
// "1.14.50" (live-run against the installed CLI during this issue's audit).
func TestHooksGate_ProbeIsOpenCodeVersion(t *testing.T) {
	gate := hooksVersionGate(t)
	want := []string{"opencode", "--version"}
	if len(gate.Probe) != len(want) {
		t.Fatalf("Probe = %v, want %v", gate.Probe, want)
	}
	for i := range want {
		if gate.Probe[i] != want[i] {
			t.Fatalf("Probe = %v, want %v", gate.Probe, want)
		}
	}
}
