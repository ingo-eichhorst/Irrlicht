package claudecode

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"irrlicht/core/domain/agent"
	"irrlicht/core/internal/contracttesting"
)

// TestHooksPermission_DeclaresVersionGate is the issue #1365 defect test for
// this adapter. Claude Code installed seven hook entries into the user's
// ~/.claude/settings.json at ANY version — no floor, no check, nothing to
// declare — which is what the issue is about. Against that code this fails at
// the "declares no minimum CLI version" fatal.
func TestHooksPermission_DeclaresVersionGate(t *testing.T) {
	contracttesting.AssertHookVersionGate(t, contracttesting.HookVersionGate{
		Agent:         Agent(),
		PermissionKey: PermissionKeyHooks,
		Installed:     installedHookEvents,
		Since:         hookEventSince,
	})
}

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

// What is deliberately NOT here: RefusesTooOldClaudeCode and
// AllowsCurrentClaudeCode, formerly a hand-picked "2.1.121 (Claude Code)"
// below-floor refusal and a hand-picked list of at-or-above values, both
// driven straight through gate.Permits with no Observed/transcript
// involvement — the same shape #1762 found and removed in five sibling
// adapters (issue #1721 / PR #1758 did the first, for pi).
// AssertHookVersionGate already runs both obligations more thoroughly:
//
//   - assertFloorRefusesOlder refuses THREE field-wise predecessors of the
//     real floor (major/minor/patch decremented independently — the patch
//     predecessor of 2.1.122 is the exact 2.1.121 the deleted test picked by
//     hand), each refusal checked to name both versions, PLUS a vacuity guard
//     a floor with nothing below it fails outright, which the deleted lock had
//     no equivalent of.
//   - The "(Claude Code)" banner-suffix format the deleted tests exercised
//     is independently pinned at the shared cliversion package
//     (cliversion_test.go: "2.1.226 (Claude Code)"), so nothing about this
//     adapter's parsing was uniquely covered here.
//
// Proven by mutation, not by inspection: weakening minCLIVersion to 0.0.0
// reddens the contract —
//
//	--- FAIL: .../floor_refuses_an_older_cli
//	    hook_version.go:62: declared floor 0.0.0 has no version below it, so this
//	    obligation asserts nothing — a floor of 0.0.0 permits every CLI and is not a gate
//
// — while AllowsCurrentClaudeCode (the vacuity-blind lock) stayed GREEN under
// the same mutation, which is exactly the coverage gap deleting it closes.
//
// TestHooksGate_DeclaresBothVersionSources stays: the contract only requires
// Observed OR Probe, never both, and this adapter's floor is decorative
// without Observed (see its own doc comment). The four
// TestNewestObservedCLIVersion_* tests below it are untouched — they pin the
// transcript-parsing AssertHookVersionGate never reaches at all.

// TestHooksGate_DeclaresBothVersionSources is the regression guard for the
// review finding that nearly shipped: with only a Probe declared, this gate is
// decorative in the deployment that matters. The production daemon is launched
// by Irrlicht.app and inherits a LaunchServices PATH of
// /usr/bin:/bin:/usr/sbin:/sbin, while the official installer puts `claude` in
// ~/.local/bin — so the probe misses, the version reads as unknown, unknown
// fails open, and every install takes the fail-open branch. A user on 2.1.90
// would still get all seven entries written into a settings.json that version
// can be broken by.
//
// Observed closes that: the transcripts are already on disk and already carry
// the version stamp. The probe stays as the fallback and as the authority when
// the passive reading looks too old.
func TestHooksGate_DeclaresBothVersionSources(t *testing.T) {
	gate := hooksVersionGate(t)
	if gate.Observed == nil {
		t.Error("no Observed source — under the production daemon's PATH the probe " +
			"cannot find `claude`, so the gate would only ever fail open")
	}
	if len(gate.Probe) == 0 {
		t.Fatal("no probe declared")
	}
	if gate.Probe[0] != "claude" {
		t.Errorf("probe runs %q, want the claude binary", gate.Probe[0])
	}
}

// writeVersionTranscript lays down a transcript under a temp CLAUDE_CONFIG_DIR and
// returns the config root.
func writeVersionTranscript(t *testing.T, name, line string) string {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "projects", "-Users-someone-repo")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(line), 0o600); err != nil {
		t.Fatalf("write transcript: %v", err)
	}
	t.Setenv("CLAUDE_CONFIG_DIR", root)
	return root
}

// TestNewestObservedCLIVersion_ReadsTheVersionStamp covers the passive source
// that keeps this gate from being decorative under launchd's PATH.
func TestNewestObservedCLIVersion_ReadsTheVersionStamp(t *testing.T) {
	writeVersionTranscript(t, "s1.jsonl",
		`{"type":"user","version":"2.1.90","message":{"role":"user"}}`+"\n")

	if got := newestObservedCLIVersion(); got != "2.1.90" {
		t.Errorf("newestObservedCLIVersion() = %q, want 2.1.90", got)
	}
}

// TestNewestObservedCLIVersion_SkipsLinesWithoutAVersion pins that the scan
// does not stop at the first line: Claude Code's transcripts routinely open
// with a summary record carrying no version stamp.
func TestNewestObservedCLIVersion_SkipsLinesWithoutAVersion(t *testing.T) {
	writeVersionTranscript(t, "s1.jsonl",
		`{"type":"summary","summary":"a thing"}`+"\n"+
			`{"type":"user","version":"2.1.130","message":{"role":"user"}}`+"\n")

	if got := newestObservedCLIVersion(); got != "2.1.130" {
		t.Errorf("newestObservedCLIVersion() = %q, want 2.1.130", got)
	}
}

// TestNewestObservedCLIVersion_UnreadableIsEmpty pins that every "cannot tell"
// path resolves to unknown rather than to something that compares. Unknown
// reaches the probe and ultimately fails open; a zero value would refuse.
func TestNewestObservedCLIVersion_UnreadableIsEmpty(t *testing.T) {
	t.Run("no transcripts at all", func(t *testing.T) {
		t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
		if got := newestObservedCLIVersion(); got != "" {
			t.Errorf("got %q, want empty", got)
		}
	})
	t.Run("transcript carries no version", func(t *testing.T) {
		writeVersionTranscript(t, "s1.jsonl", `{"type":"summary","summary":"no version here"}`+"\n")
		if got := newestObservedCLIVersion(); got != "" {
			t.Errorf("got %q, want empty", got)
		}
	})
	t.Run("transcript is not json", func(t *testing.T) {
		writeVersionTranscript(t, "s1.jsonl", "this is not jsonl at all\n")
		if got := newestObservedCLIVersion(); got != "" {
			t.Errorf("got %q, want empty", got)
		}
	})
}

// TestNewestObservedCLIVersion_PicksTheNewestFile pins that the answer tracks
// the most recently written session, which is the closest proxy on disk for
// "the Claude Code the user is running now".
func TestNewestObservedCLIVersion_PicksTheNewestFile(t *testing.T) {
	root := writeVersionTranscript(t, "old.jsonl", `{"type":"user","version":"2.1.90"}`+"\n")
	dir := filepath.Join(root, "projects", "-Users-someone-repo")
	newer := filepath.Join(dir, "new.jsonl")
	if err := os.WriteFile(newer, []byte(`{"type":"user","version":"2.1.200"}`+"\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	older := filepath.Join(dir, "old.jsonl")
	if err := os.Chtimes(older, time.Now().Add(-time.Hour), time.Now().Add(-time.Hour)); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	if got := newestObservedCLIVersion(); got != "2.1.200" {
		t.Errorf("newestObservedCLIVersion() = %q, want 2.1.200 (the newest file)", got)
	}
}
