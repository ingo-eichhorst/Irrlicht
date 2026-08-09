package codex

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

// writeCodexSessionWithVersion lays down a $CODEX_HOME/sessions tree holding a
// single transcript whose session_meta header declares cliVersion, which is
// what the adapter uses as its install-time proxy for "the Codex the user is
// running now".
func writeCodexSessionWithVersion(t *testing.T, cliVersion string) string {
	t.Helper()
	home := t.TempDir()
	sessions := filepath.Join(home, "sessions")
	if err := os.MkdirAll(sessions, 0o755); err != nil {
		t.Fatalf("mkdir sessions: %v", err)
	}
	line := `{"type":"session_meta","payload":{"id":"s1","cli_version":"` + cliVersion + `"}}` + "\n"
	if err := os.WriteFile(filepath.Join(sessions, "s1.jsonl"), []byte(line), 0o644); err != nil {
		t.Fatalf("write session: %v", err)
	}
	t.Setenv(codexHomeEnvVar, home)
	return home
}

// hooksVersionGate returns the declared gate for the hooks permission, read
// off the real registration the daemon consumes.
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

// TestHooksGate_BelowFloorSaysWhy is the issue #1365 defect test for the "say
// why" half of scope item 3, resolved through the adapter's real version
// source. A Codex older than the hooks feature was already skipped before
// #1365 — but silently: applyCodexHooks returned nil, indistinguishable from a
// successful install, so the user saw the permission go green with a channel
// that can never fire and nothing anywhere said why.
//
// The reason is now carried by the refusal itself, which PermissionService
// records as the permission's EffectError, so #1362's consent-effect surfacing
// renders it in both wizards.
func TestHooksGate_BelowFloorSaysWhy(t *testing.T) {
	writeCodexSessionWithVersion(t, "0.100.0")
	gate := hooksVersionGate(t)
	observed := gate.Observed

	if observed == nil {
		t.Fatal("codex declares no Observed version source; it reads cli_version from the " +
			"session header it already tails, and losing that means probing the binary")
	}
	if got := observed(); got != "0.100.0" {
		t.Fatalf("Observed() = %q, want the cli_version from the newest session header", got)
	}

	allowed, why := gate.Permits(observed())

	if allowed {
		t.Fatal("gate permits installing into Codex 0.100.0, below the declared floor")
	}
	if !strings.Contains(why, "0.100.0") || !strings.Contains(why, minCLIVersion) {
		t.Errorf("refusal %q does not name both the installed version and the required "+
			"one; the reason has to be actionable enough for a user to know what to "+
			"upgrade", why)
	}
}

// TestHooksGate_AtOrAboveFloorInstalls is a LOCK: it pins that the gate does
// not become a blanket refusal. It passes by construction and is here because
// a floor that rejects everything looks identical, from the log, to a floor
// that works.
func TestHooksGate_AtOrAboveFloorInstalls(t *testing.T) {
	for _, v := range []string{minCLIVersion, "0.146.1", "rust-v0.120.3"} {
		writeCodexSessionWithVersion(t, v)
		gate := hooksVersionGate(t)
		if allowed, why := gate.Permits(gate.Observed()); !allowed {
			t.Errorf("gate refuses Codex %s, at or above the %s floor: %s", v, minCLIVersion, why)
		}
	}
}

// TestHooksGate_NoTranscriptFallsThroughToProbe pins that a machine with no
// Codex sessions yet does not resolve to "version 0" — Observed returns "",
// which must mean unknown and reach the probe, not a refusal.
func TestHooksGate_NoTranscriptFallsThroughToProbe(t *testing.T) {
	t.Setenv(codexHomeEnvVar, t.TempDir())
	gate := hooksVersionGate(t)

	if got := gate.Observed(); got != "" {
		t.Fatalf("Observed() = %q with no sessions on disk, want empty", got)
	}
	if allowed, why := gate.Permits(""); !allowed {
		t.Errorf("gate refuses on an unknown version: %s — an unread version is not an "+
			"old one, and refusing here disables hooks on a fresh install", why)
	}
}
