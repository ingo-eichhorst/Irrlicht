package main

import (
	"bytes"
	"path/filepath"
	"slices"
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
// EnsureHooksInstalled's single Apply closure writes THREE shared, user-owned
// files — the agent config (Writes.Path), chat.defaultAgent inside
// settings/cli.json (hookinstaller.go's ensureDefaultAgent), and the
// prior-default sidecar (the sibling test below) — and only the first is
// Writes.Path. This test grades the settings/cli.json one; each file gets its
// own test so a declaration that goes missing names WHICH file it was.
// Undeclared, settings/cli.json is invisible to both
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
//
// The #1449 refusal itself IS exercised, separately: TestSharedConfigGate_
// KiroCLIAlsoDeclarationReachesTheGuard (application/services/
// permission_shared_config_gate_test.go) extracts this SAME real Also
// resolver and plugs it into a synthetic permission whose Path is
// deliberately decoupled from kiro-cli's own — the only way to observe
// sharedConfigRefusal's verdict move at all, given the co-location this
// comment describes. Both tests were seen red together against the identical
// mutation (Also commented out here).
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
	wantSuffix := filepath.Join("settings", "cli.json")
	also := resolvedAlsoUnder(t, perm, kiroHome, wantSuffix)

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
	if !slices.Contains(kirocliCfg.Also, also) {
		t.Errorf("projected ManagedUserFile.Also = %v, want it to contain %q — the recorder's "+
			"snapshot/restore sweep resolves this slice directly, so an entry missing here is a "+
			"file it will never back up before a grant-all daemon can rewrite it", kirocliCfg.Also, also)
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

// TestKiroCLIAlso_PriorDefaultSidecarIsAManagedFile covers the THIRD file
// kiro-cli's single hooks Apply closure writes into the user's real agent
// home, and the only one #1718's audit did not declare:
// $KIRO_HOME/.irrlicht-prior-default.json, written by
// recordPriorDefaultAgentOnce (hookinstaller.go) on the way through
// ensureDefaultAgent.
//
// It matters for one consumer and one failure, both concrete. The onboarding
// recording rig protects exactly what `--print-managed-files` names
// (tools/onboarding-factory/scripts/lib/managed-file-snapshot.sh asks the
// daemon and keeps no literal list), so an undeclared file is one the
// snapshot never backs up and the EXIT-trap restore never hands back — a
// grant-all recording daemon creates it in the operator's real ~/.kiro and
// leaves it there. The residue is not inert: recordPriorDefaultAgentOnce
// deliberately never overwrites an existing record, so the stale sidecar a
// recording left behind is what a LATER real grant reads instead of the
// user's actual chat.defaultAgent — and a subsequent uninstall then restores
// the recording's snapshot of that setting rather than the user's own choice.
//
// Same shape as the settings/cli.json case above and the same fix (one Also
// entry), separated into its own test so the two files' declarations can fail
// independently: a single assertion over the whole slice cannot say WHICH
// file stopped being protected.
func TestKiroCLIAlso_PriorDefaultSidecarIsAManagedFile(t *testing.T) {
	kiroHome := filepath.Join(t.TempDir(), "kiro-home")
	t.Setenv("KIRO_HOME", kiroHome)

	perm := kirocliHooksPermission(t)
	if perm.Writes == nil {
		t.Fatal("kirocli hooks permission declares no Writes")
	}

	want := filepath.Join(kiroHome, ".irrlicht-prior-default.json")
	found := false
	for i, resolve := range perm.Writes.Also {
		got, err := resolve()
		if err != nil {
			t.Fatalf("resolving Writes.Also[%d]: %v", i, err)
		}
		if got == want {
			found = true
		}
	}
	if !found {
		t.Errorf("Writes.Also declares no entry resolving to %q — recordPriorDefaultAgentOnce "+
			"writes it on every install, so the recording rig cannot back it up or hand it back", want)
	}

	var buf bytes.Buffer
	if err := printManagedFiles(&buf); err != nil {
		t.Fatalf("printManagedFiles: %v", err)
	}
	if !strings.Contains(buf.String(), want) {
		t.Errorf("--print-managed-files does not list %q — the recording rig protects only what "+
			"this command names", want)
	}
}

// resolvedAlsoUnder returns the one Writes.Also entry that resolves under root
// and ends in suffix.
//
// Selected by what it RESOLVES TO, not by index: kiro-cli's hooks permission
// declares more than one Also entry, and an index would make a test's subject
// depend on declaration order — a silent retarget at a different file the day
// somebody reorders the slice.
func resolvedAlsoUnder(t *testing.T, perm agent.Permission, root, suffix string) string {
	t.Helper()
	for i, resolve := range perm.Writes.Also {
		got, err := resolve()
		if err != nil {
			t.Fatalf("resolving Writes.Also[%d]: %v", i, err)
		}
		if strings.HasPrefix(got, root) && strings.HasSuffix(got, suffix) {
			return got
		}
	}
	t.Fatalf("Writes.Also declares no entry under %q ending in %q", root, suffix)
	return ""
}
