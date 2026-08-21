package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"irrlicht/core/adapters/inbound/agents"
	"irrlicht/core/adapters/inbound/agents/kirocli"
	"irrlicht/core/domain/agent"
)

// kirocliHooksPermission returns the real declared "hooks" permission off
// kirocli's own registration. This lives here, not in the kirocli package's
// own test suite, because kirocli cannot import agents (agents imports every
// adapter, kirocli included — that would be an import cycle); cmd/irrlichd is
// the outer layer that can see both without one.
func kirocliHooksPermission(t *testing.T) agent.Permission {
	t.Helper()
	for _, p := range kirocli.Agent().Permissions {
		if p.Key == kirocli.PermissionKeyHooks {
			return p
		}
	}
	t.Fatal("kirocli.Agent() declares no hooks permission")
	return agent.Permission{}
}

// TestKiroCLIAlso_SettingsFileIsAManagedFile is the mutation-evidence test for
// issue #1718's Also field applied to kiro-cli's hooks install (issue #1716):
// EnsureHooksInstalled's single Apply closure writes TWO shared, user-owned
// files — the agent config (Writes.Path) and chat.defaultAgent inside
// settings/cli.json (hookinstaller.go's ensureDefaultAgent) — and only the
// first is Writes.Path. Undeclared, the second is invisible to both
// consumers that need to see it: the #1449 grant-all refusal
// (PermissionService.sharedConfigRefusal) and the recording rig's
// snapshot/restore sweep (agents.ManagedUserFiles, --print-managed-files).
//
// Seen red by commenting out the Also field in kirocli/agent.go: Also drops
// to length 0, the projected ManagedUserFile.Also becomes empty, and
// --print-managed-files stops naming settings/cli.json — see this issue's #1716
// comment thread for the pasted failure.
//
// Structural note, recorded here rather than left to be rediscovered: for
// kiro-cli specifically, Path (agents/irrlicht.json) and every Also entry
// (settings/cli.json) resolve under the SAME root ($KIRO_HOME —
// hookinstaller.go's kiroHome()), so the two can never disagree on being
// inside or outside the daemon's isolated home.
// PermissionService.sharedConfigRefusal checks Path FIRST and returns
// immediately on a refusal (permission_shared_config_gate.go), so for this
// adapter's co-located shape the #1449 REFUSAL VERDICT is identical whether
// or not Also is declared — Path's own check already catches every unsafe
// case, and the Also loop is never even reached once Path itself has failed.
// What declaring Also changes, OBSERVABLY, is exclusively the file SET
// agents.ManagedUserFiles / --print-managed-files reports, which is what
// this test actually grades — a completeness/defence-in-depth property for
// this adapter's shape, not a verdict-changing one. Stated here rather than
// left implied, because claiming this test exercises the #1449 refusal
// itself would overclaim what a co-located pair of files can demonstrate.
func TestKiroCLIAlso_SettingsFileIsAManagedFile(t *testing.T) {
	// Hermetic: the resolved path must be a function of what THIS test sets,
	// never of the machine it runs on (see AGENTS.md's sanitizedChildEnv
	// comment for the same rule applied to the daemon-process tests in this
	// package) — so KIRO_HOME is pinned to a throwaway directory rather than
	// left to resolve against the real $HOME.
	kiroHome := filepath.Join(t.TempDir(), "kiro-home")
	t.Setenv("KIRO_HOME", kiroHome)

	perm := kirocliHooksPermission(t)
	if perm.Writes == nil {
		t.Fatal("kirocli hooks permission declares no Writes")
	}
	if len(perm.Writes.Also) != 1 {
		t.Fatalf("Writes.Also has %d entries, want exactly 1 (settings/cli.json)", len(perm.Writes.Also))
	}
	also, err := perm.Writes.Also[0]()
	if err != nil {
		t.Fatalf("resolving Writes.Also[0]: %v", err)
	}
	wantSuffix := filepath.Join("settings", "cli.json")
	if !strings.HasPrefix(also, kiroHome) || !strings.HasSuffix(also, wantSuffix) {
		t.Errorf("Writes.Also[0] = %q, want it under %q ending in %q", also, kiroHome, wantSuffix)
	}

	configs, err := agents.ManagedUserFiles(declaredConsentCatalog())
	if err != nil {
		t.Fatalf("agents.ManagedUserFiles: %v", err)
	}
	var kirocliCfg *agents.ManagedUserFile
	for i := range configs {
		if configs[i].Adapter == kirocli.AdapterName && configs[i].Key == kirocli.PermissionKeyHooks {
			kirocliCfg = &configs[i]
		}
	}
	if kirocliCfg == nil {
		t.Fatal("agents.ManagedUserFiles projects no kiro-cli/hooks entry at all")
	}
	if len(kirocliCfg.Also) != 1 || kirocliCfg.Also[0] != also {
		t.Errorf("projected ManagedUserFile.Also = %v, want [%q] — the recorder's snapshot/restore "+
			"sweep resolves this slice directly, so an entry missing here is a file it will never "+
			"back up before a grant-all daemon can rewrite it", kirocliCfg.Also, also)
	}

	var buf bytes.Buffer
	if err := printManagedFiles(&buf); err != nil {
		t.Fatalf("printManagedFiles: %v", err)
	}
	if !strings.Contains(buf.String(), also) {
		t.Errorf("--print-managed-files does not list %q — the recording rig protects only what "+
			"this command names", also)
	}
}
