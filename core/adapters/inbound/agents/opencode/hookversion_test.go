// hookversion_test.go wires the opencode hooks permission into the shared issue
// #1365 contract.
//
// The gate is a variant declaration (#2108): one artifact for opencode 1.x and
// one for 2.x, at the same path, selected by the installed version. Observed
// reads that version off the newest session in opencode's own store
// (storeversion.go), behind the database permission; Probe
// (`opencode --version`) is the fallback.
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
	})
}

// hooksWrites returns the declared managed file, read off the real
// registration the daemon consumes rather than a copy.
func hooksWrites(t *testing.T) *agent.ManagedUserFile {
	t.Helper()
	for _, p := range Agent().Permissions {
		if p.Key == PermissionKeyHooks {
			if p.Writes == nil {
				t.Fatal("hooks permission declares no managed file")
			}
			return p.Writes
		}
	}
	t.Fatal("no hooks permission")
	return nil
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

// TestHooksGate_SelectsTheTwoXArtifactForOpenCode2 is #2108's red-first
// declaration test. #2105 made the gate refuse 2.1.4 — the version the
// sandbox's V2 binary reports, npm `@opencode/cli` latest when this was written
// — so a user who upgraded got nothing. The gate now selects the 2.x artifact
// there, and its Apply is the 2.x installer's.
func TestHooksGate_SelectsTheTwoXArtifactForOpenCode2(t *testing.T) {
	writes := hooksWrites(t)
	variant, bound, why := writes.SelectVariant("2.1.4")
	if variant == nil {
		t.Fatalf("opencode 2.1.4 selects no hook artifact (bound %d: %q) — a user who upgraded "+
			"gets no waiting detection and no authoritative turn end", bound, why)
	}
	if variant.Min != minOpenCodeV2Version || len(variant.Events) != len(installedHookEventsV2) {
		t.Errorf("opencode 2.1.4 selects the artifact for %s up to %s with %d events, want the 2.x "+
			"one (%s up to %s, %d events)", variant.Min, variant.Max, len(variant.Events),
			minOpenCodeV2Version, maxOpenCodeV2Version, len(installedHookEventsV2))
	}
}

// TestHooksGate_RefusesBetweenAndAboveTheArtifacts pins the two refusals the
// variant declaration keeps from #2105 (a LOCK for 2.0.0 and 3.0.0, which the
// 1.x-only gate already refused): a 2.x older than the 2.x plugin's measured
// floor, and the next major, whose plugin format nobody has seen. Both refuse
// as AT OR ABOVE A CEILING, which is what makes PermissionService remove an
// earlier install's file there, as #2105 did for every 2.x.
func TestHooksGate_RefusesBetweenAndAboveTheArtifacts(t *testing.T) {
	writes := hooksWrites(t)
	for v, names := range map[string][]string{
		"2.0.0": {"2.0.0", minOpenCodeV2Version},
		"2.1.3": {"2.1.3", maxOpenCodeVersion, minOpenCodeV2Version},
		"3.0.0": {"3.0.0", "3.x"},
	} {
		variant, bound, why := writes.SelectVariant(v)
		if variant != nil || bound != agent.AtOrAboveCeiling {
			t.Errorf("opencode %s selected %v with bound %d (%q), want no artifact, refused at a "+
				"ceiling", v, variant != nil, bound, why)
			continue
		}
		for _, want := range names {
			if !strings.Contains(why, want) {
				t.Errorf("the refusal for %s (%q) does not name %q", v, why, want)
			}
		}
	}
}

// TestHooksGate_PermitsOpenCode1 is a LOCK, not a defect test: it passed before
// #2105 and #2108 and must keep passing. The newest 1.x a brew user has (1.19.4
// when this was written) still installs the 1.x plugin — the 1.x artifact's
// ceiling sits at the major boundary, not below it.
func TestHooksGate_PermitsOpenCode1(t *testing.T) {
	writes := hooksWrites(t)
	for _, v := range []string{minOpenCodeVersion, "1.19.4", "1.999.999"} {
		variant, _, why := writes.SelectVariant(v)
		if variant == nil || variant.Min != minOpenCodeVersion {
			t.Errorf("opencode %s does not select the 1.x artifact (%s); every 1.x at or above "+
				"the floor must still install it", v, why)
		}
	}
}

// TestHooksGate_ObservedReadsBehindTheDatabasePermission pins the declaration
// half of #2108's consent rule: Observed reads opencode's session database,
// which only the database permission discloses, so the gate names it and
// PermissionService skips Observed while it is not granted (the service half is
// TestVariantGate_ObservedIsNotConsultedWithoutItsPermission).
func TestHooksGate_ObservedReadsBehindTheDatabasePermission(t *testing.T) {
	gate := hooksVersionGate(t)
	if gate.Observed == nil {
		t.Fatal("the hooks gate declares no Observed version source")
	}
	if gate.ObservedRequires != PermissionKeyDatabase {
		t.Errorf("ObservedRequires = %q, want %q — Observed reads the session database",
			gate.ObservedRequires, PermissionKeyDatabase)
	}
}
