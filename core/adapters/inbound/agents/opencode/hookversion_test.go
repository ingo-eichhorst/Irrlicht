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
	"strings"
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

// TestHooksGate_CeilingRefusesOpenCode2 is #2105's red-first defect test. On
// main the gate was Min-only, so the 2.1.4 the sandbox's V2 binary reports
// passed it, and the daemon wrote the 1.x plugin file that opencode 2.x rejects
// on every location load ("Plugin must export a default definition with an id
// and an effect or setup function", measured in #2104). 2.1.4 is the version
// that shipped as npm `@opencode/cli` latest when this was written.
func TestHooksGate_CeilingRefusesOpenCode2(t *testing.T) {
	gate := hooksVersionGate(t)
	allowed, why := gate.Permits("2.1.4")
	if allowed {
		t.Fatal("the hooks gate permits opencode 2.1.4, so the daemon writes a 1.x plugin " +
			"file that opencode 2.x refuses to load on every start")
	}
	for _, want := range []string{"2.1.4", "2.0.0", "2.x"} {
		if !strings.Contains(why, want) {
			t.Errorf("refusal %q does not name %q; the user reads it as the reason "+
				"nothing was installed", why, want)
		}
	}
}

// TestHooksGate_PermitsOpenCode1 is a LOCK, not a defect test: it passed before
// #2105 and must keep passing. The newest 1.x a brew user has (1.19.4 when this
// was written) still installs the 1.x plugin — the ceiling sits at the major
// boundary, not below it.
func TestHooksGate_PermitsOpenCode1(t *testing.T) {
	gate := hooksVersionGate(t)
	for _, v := range []string{minOpenCodeVersion, "1.19.4", "1.999.999"} {
		if allowed, why := gate.Permits(v); !allowed {
			t.Errorf("the hooks gate refuses opencode %s (%s); every 1.x at or above the "+
				"floor must still install", v, why)
		}
	}
}
