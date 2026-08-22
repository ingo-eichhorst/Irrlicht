// hookversion_test.go wires this adapter's hooks permission into the shared
// issue #1365 contract.
//
// Like geminicli, vibe and pi, this adapter declares no Observed version
// source: nothing an antigravity transcript or its sibling conversation store
// carries is a CLI version this adapter's parser reads, so the gate falls
// straight through to Probe (`agy --version`), which cliversion runs itself.
//
// What is deliberately NOT here: hand-written BelowFloorSaysWhy /
// AtOrAboveFloorInstalls / UnknownVersionFailsOpen tests. The shared contract
// asserts all three, and more thoroughly than a hand-written copy does — see
// pi/hookversion_test.go's own note, which measured that before deleting the
// copies it had just written.
package antigravity

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

// TestHooksGate_ProbeIsAgyVersion pins the argv this adapter asks cliversion
// to run — the one thing the contract does not cover, since
// assertVersionSourceDeclared only requires Observed or Probe to be non-empty
// and never that the argv is the right one.
//
// `agy --version` is the CLI this adapter already matches processes by
// (ProcessName), which is why the probe is derived from that constant rather
// than spelled a second time: a rename that moved one and not the other would
// leave the version gate probing a binary the daemon does not believe exists.
func TestHooksGate_ProbeIsAgyVersion(t *testing.T) {
	gate := hooksVersionGate(t)
	want := []string{"agy", "--version"}
	if len(gate.Probe) != len(want) {
		t.Fatalf("Probe = %v, want %v", gate.Probe, want)
	}
	for i := range want {
		if gate.Probe[i] != want[i] {
			t.Fatalf("Probe = %v, want %v", gate.Probe, want)
		}
	}
}

// TestHooksGate_FloorIsTheOnlyVerifiedVersion is a LOCK, and it is the honest
// statement of what the floor rests on.
//
// 1.1.18 is not the version the Stop event was introduced at — no upstream
// changelog dates it — it is the only version at which ANY of this install was
// verified: that ~/.gemini/config/hooks.json is the file that loads (the
// binary's own changelog records that location having MOVED), that the
// per-event flat/grouped structure is what it is, that the payload is
// camelCase protojson, and that Stop is invoked at all. #1723's audit read
// 1.1.2 and got three load-bearing facts wrong about it, which is the evidence
// that lowering this floor needs a measurement and not an argument.
func TestHooksGate_FloorIsTheOnlyVerifiedVersion(t *testing.T) {
	if minAgyVersion != "1.1.18" {
		t.Errorf("minAgyVersion = %q. 1.1.18 is the only agy version anything in this adapter's "+
			"hook install was verified against; moving the floor needs a new measurement, not an "+
			"inference from a changelog", minAgyVersion)
	}
	if got := hookEventSince[HookEventStop]; got != minAgyVersion {
		t.Errorf("hookEventSince[%q] = %q, want %q — the event's Since is the version it was "+
			"OBSERVED firing at, which is the same one the floor pins", HookEventStop, got, minAgyVersion)
	}
}
