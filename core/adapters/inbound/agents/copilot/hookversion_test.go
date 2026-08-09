// hookversion_test.go wires the Copilot hooks permission into the shared issue
// #1365 contract, and pins the adapter's own passive version source.
package copilot

import (
	"os"
	"path/filepath"
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

// writeCopilotSessionWithVersion lays down a $COPILOT_HOME/session-state tree
// holding one transcript whose session.start header declares copilotVersion —
// the adapter's install-time proxy for "the Copilot the user is running now".
func writeCopilotSessionWithVersion(t *testing.T, cliVersion string) {
	t.Helper()
	home := t.TempDir()
	dir := filepath.Join(home, "session-state", "s1")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir session: %v", err)
	}
	line := `{"type":"session.start","data":{"copilotVersion":"` + cliVersion + `"}}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, transcriptFilename), []byte(line), 0o644); err != nil {
		t.Fatalf("write transcript: %v", err)
	}
	t.Setenv(copilotHomeEnvVar, home)
}

// hooksVersionGate returns the declared gate, read off the real registration
// the daemon consumes rather than a copy.
func hooksVersionGate(t *testing.T) *agent.VersionGate {
	t.Helper()
	for _, p := range Agent().Permissions {
		if p.Key == PermissionKeyHooks {
			if p.Hooks == nil || p.Hooks.Version == nil {
				t.Fatal("hooks permission declares no version gate")
			}
			return p.Hooks.Version
		}
	}
	t.Fatal("no hooks permission")
	return nil
}

// TestHooksGate_BelowFloorSaysWhy pins that a Copilot older than the
// notification guarantee is refused WITH a reason naming both versions. Below
// 1.0.26 the notification event either does not exist (pre-1.0.18) or fires
// when no prompt was shown (1.0.18–1.0.25), which would manufacture false
// waiting states — so this floor is about correctness, not just presence.
func TestHooksGate_BelowFloorSaysWhy(t *testing.T) {
	writeCopilotSessionWithVersion(t, "1.0.20")
	gate := hooksVersionGate(t)

	if gate.Observed == nil {
		t.Fatal("copilot declares no Observed version source; it reads copilotVersion from " +
			"the session.start header it already tails")
	}
	if got := gate.Observed(); got != "1.0.20" {
		t.Fatalf("Observed() = %q, want the copilotVersion from the newest transcript", got)
	}

	allowed, why := gate.Permits(gate.Observed())
	if allowed {
		t.Fatal("gate permits installing into Copilot 1.0.20, below the declared floor")
	}
	if !strings.Contains(why, "1.0.20") || !strings.Contains(why, minCLIVersion) {
		t.Errorf("refusal %q names neither the installed version nor the required one; "+
			"the reason has to tell the user what to upgrade", why)
	}
}

// TestHooksGate_AtOrAboveFloorInstalls is a LOCK: it pins that the gate has not
// become a blanket refusal, which from a log looks identical to one that works.
func TestHooksGate_AtOrAboveFloorInstalls(t *testing.T) {
	for _, v := range []string{minCLIVersion, "1.0.78", "1.1.0", "2.0.0"} {
		writeCopilotSessionWithVersion(t, v)
		gate := hooksVersionGate(t)
		if allowed, why := gate.Permits(gate.Observed()); !allowed {
			t.Errorf("gate refuses Copilot %s, at or above the %s floor: %s", v, minCLIVersion, why)
		}
	}
}

// TestHooksGate_NoTranscriptFallsThroughToProbe pins that a machine with no
// Copilot sessions yet resolves to "unknown", not to "version 0" — an unread
// version is not an old one, and refusing here would disable hooks on a fresh
// install.
func TestHooksGate_NoTranscriptFallsThroughToProbe(t *testing.T) {
	t.Setenv(copilotHomeEnvVar, t.TempDir())
	gate := hooksVersionGate(t)

	if got := gate.Observed(); got != "" {
		t.Fatalf("Observed() = %q with no transcripts on disk, want empty", got)
	}
	if allowed, why := gate.Permits(""); !allowed {
		t.Errorf("gate refuses on an unknown version: %s", why)
	}
}
