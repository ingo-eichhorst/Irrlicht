// hookversion_test.go wires the Mistral Vibe hooks permission into the
// shared issue #1365 contract.
//
// Like geminicli, this adapter declares no Observed version source: nothing
// in a Vibe transcript's meta.json sidecar carries a CLI version field this
// adapter's parser reads, so the gate falls straight through to Probe
// (`vibe --version`), which cliversion runs itself.
package vibe

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

// TestHooksGate_BelowFloorSaysWhy pins that a Vibe older than the declared
// floor is refused WITH a reason naming both versions.
func TestHooksGate_BelowFloorSaysWhy(t *testing.T) {
	gate := hooksVersionGate(t)

	allowed, why := gate.Permits("2.18.0")
	if allowed {
		t.Fatal("gate permits installing into Vibe 2.18.0, below the declared floor")
	}
	if !strings.Contains(why, "2.18.0") || !strings.Contains(why, minVibeVersion) {
		t.Errorf("refusal %q names neither the installed version nor the required one; "+
			"the reason has to tell the user what to upgrade", why)
	}
}

// TestHooksGate_AtOrAboveFloorInstalls is a LOCK: it pins that the gate has
// not become a blanket refusal, which from a log looks identical to one
// that works.
func TestHooksGate_AtOrAboveFloorInstalls(t *testing.T) {
	gate := hooksVersionGate(t)
	for _, v := range []string{minVibeVersion, "2.20.0", "3.0.0"} {
		if allowed, why := gate.Permits(v); !allowed {
			t.Errorf("gate refuses Vibe %s, at or above the %s floor: %s", v, minVibeVersion, why)
		}
	}
}

// TestHooksGate_UnknownVersionFailsOpen pins the documented direction
// (core/pkg/cliversion): an unparseable or unknown version must not block
// an install. The daemon runs under launchd with a minimal PATH and
// routinely cannot see the user's CLI at all, so "unknown" must read as
// "not proven old", not as "assume the worst".
func TestHooksGate_UnknownVersionFailsOpen(t *testing.T) {
	gate := hooksVersionGate(t)
	if allowed, why := gate.Permits(""); !allowed {
		t.Errorf("gate refuses on an unknown version: %s", why)
	}
}

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
