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
			if p.Writes == nil || p.Writes.Version == nil {
				t.Fatal("hooks permission declares no version gate")
			}
			return p.Writes.Version
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

// What is deliberately NOT here: TestHooksGate_AtOrAboveFloorInstalls, a
// hand-picked list of at-or-above versions run through
// writeCopilotSessionWithVersion + Observed(), asserted a LOCK against the
// gate becoming a blanket refusal. The same shape was removed from five other
// adapters by #1721/#1758 and #1762.
//
// It added nothing AssertHookVersionGate doesn't already cover more
// thoroughly: Permits(gate.Min) allowed, PLUS a vacuity guard a floor with
// nothing below it fails outright, which this lock had no equivalent of.
// newestObservedCLIVersion/sessionStartVersion have no format-dependent
// branching — a bare JSON field read (hookinstaller.go) — so re-exercising it
// against four different version strings proved nothing beyond what
// TestHooksGate_BelowFloorSaysWhy below already proves once.
//
// Proven by mutation, not by inspection: weakening notificationGuaranteeSince
// (== minCLIVersion) to 0.0.0 reddens the contract —
//
//	--- FAIL: .../floor_refuses_an_older_cli
//	    hook_version.go:62: declared floor 0.0.0 has no version below it, so this
//	    obligation asserts nothing — a floor of 0.0.0 permits every CLI and is not a gate
//
// — while TestHooksGate_AtOrAboveFloorInstalls stayed GREEN under the same
// mutation, which is exactly the coverage gap deleting it closes.
// TestHooksGate_BelowFloorSaysWhy and TestHooksGate_NoTranscriptFallsThroughToProbe
// stay: each is the ONLY test exercising the real transcript-reading path (a
// written copilotVersion, and the zero-transcripts fallback to ""), which the
// shared contract never reaches at all.

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
