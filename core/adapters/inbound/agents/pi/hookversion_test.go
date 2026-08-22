// hookversion_test.go wires the pi hooks permission into the shared issue
// #1365 contract.
//
// Like geminicli and vibe, this adapter declares no Observed version source:
// nothing in a pi transcript carries a CLI version field this adapter's
// parser reads, so the gate falls straight through to Probe
// (`pi --version`), which cliversion runs itself.
package pi

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

// TestHooksGate_BelowFloorSaysWhy pins that a pi older than the declared
// floor is refused WITH a reason naming both versions.
func TestHooksGate_BelowFloorSaysWhy(t *testing.T) {
	gate := hooksVersionGate(t)

	allowed, why := gate.Permits("0.82.1")
	if allowed {
		t.Fatal("gate permits installing into pi 0.82.1, below the declared floor")
	}
	if !strings.Contains(why, "0.82.1") || !strings.Contains(why, minPiVersion) {
		t.Errorf("refusal %q names neither the installed version nor the required one; "+
			"the reason has to tell the user what to upgrade", why)
	}
}

// TestHooksGate_AtOrAboveFloorInstalls is a LOCK: it pins that the gate has
// not become a blanket refusal, which from a log looks identical to one that
// works.
func TestHooksGate_AtOrAboveFloorInstalls(t *testing.T) {
	gate := hooksVersionGate(t)
	for _, v := range []string{minPiVersion, "0.84.2", "1.0.0"} {
		if allowed, why := gate.Permits(v); !allowed {
			t.Errorf("gate refuses pi %s, at or above the %s floor: %s", v, minPiVersion, why)
		}
	}
}

// TestHooksGate_UnknownVersionFailsOpen pins the documented direction
// (core/pkg/cliversion): an unparseable or unknown version must not block an
// install. The daemon runs under launchd with a minimal PATH and routinely
// cannot see the user's CLI at all — pi in particular is usually an nvm-
// managed shim outside that PATH — so "unknown" must read as "not proven
// old", not as "assume the worst".
func TestHooksGate_UnknownVersionFailsOpen(t *testing.T) {
	gate := hooksVersionGate(t)
	if allowed, why := gate.Permits(""); !allowed {
		t.Errorf("gate refuses on an unknown version: %s", why)
	}
}

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
