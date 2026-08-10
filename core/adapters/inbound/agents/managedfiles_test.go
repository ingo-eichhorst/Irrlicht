package agents

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"irrlicht/core/domain/agent"
	"irrlicht/core/domain/permission"
)

// TestEveryHooksPermissionIsCoveredByHookConfigs is the coverage check #1357
// asks for: an adapter that declares a hooks permission must also declare WHERE
// those hooks go, so both of the places that have to enumerate agent config
// files — `irrlichd --uninstall-hooks` and the onboarding recorder's snapshot —
// see it without anyone remembering to add it twice.
//
// Contract test, not a defect test: it passes by construction against a correct
// adapter set, and its whole value is that it CAN fail. It was seen red by
// dropping codex's Writes declaration (see the PR body).
//
// The key match is a substring rather than an equality on "hooks" so an adapter
// that spells its permission key differently is still held to the contract.
// Note HookConfigs itself narrows on the exact agent.HooksPermissionKey, so an
// adapter that spelled its key "hooks-v2" would be caught by the coverage
// arithmetic below rather than quietly excluded.
func TestEveryHooksPermissionIsCoveredByHookConfigs(t *testing.T) {
	configs, err := HookConfigs(All())
	if err != nil {
		t.Fatalf("HookConfigs(): %v", err)
	}

	covered := make(map[string]bool, len(configs))
	for _, c := range configs {
		id := c.Adapter + "/" + c.Key
		if covered[id] {
			t.Errorf("%s is listed twice by HookConfigs()", id)
		}
		covered[id] = true
		if !filepath.IsAbs(c.Path) {
			t.Errorf("%s: hook config path %q is not absolute — the recorder backs up and restores exactly this path", id, c.Path)
		}
		if c.Uninstall == nil {
			t.Errorf("%s: HookConfigs() returned a nil Uninstall", id)
		}
	}

	declared := 0
	for _, a := range All() {
		for _, p := range a.Permissions {
			if !strings.Contains(strings.ToLower(p.Key), "hook") {
				continue
			}
			declared++
			id := a.Identity.Name + "/" + p.Key
			if p.Writes == nil {
				t.Errorf("%s declares a hooks permission but no agent.ManagedUserFile: "+
					"`irrlichd --uninstall-hooks` would leave its entries in the user's real "+
					"config permanently, and a recording would repoint that file at a dead "+
					"recorder port and never hand it back (#1357)", id)
				continue
			}
			if !covered[id] {
				t.Errorf("%s declares an agent.ManagedUserFile that HookConfigs() does not list", id)
			}
		}
	}

	if declared == 0 {
		t.Fatal("no adapter declares a hooks permission — this check would pass vacuously")
	}
	if len(configs) != declared {
		t.Errorf("HookConfigs() returned %d entries for %d declared hooks permissions", len(configs), declared)
	}
}

// writerAdapter builds a one-permission adapter carrying the given
// ManagedUserFile under the given key.
func writerAdapter(name, key string, w *agent.ManagedUserFile) agent.Agent {
	return agent.Agent{
		Identity:    agent.Identity{Name: name},
		Permissions: []agent.Permission{{Key: key, Kind: permission.KindModify, Writes: w}},
	}
}

// TestManagedUserFilesRejectsUnusableDeclarations covers the branches that
// become log.Fatalf in `irrlichd --uninstall-hooks` and a hard error in
// `--print-managed-files`. A half-declared ManagedUserFile must be an error
// rather than a silent skip: skipping would reproduce exactly the failure the
// projection exists to remove — a file-writing permission absent from both
// lists — only harder to notice, because nothing would say so.
func TestManagedUserFilesRejectsUnusableDeclarations(t *testing.T) {
	ok := func() (string, error) { return "/tmp/x/settings.json", nil }
	uninstall := func() (bool, error) { return false, nil }

	cases := []struct {
		name  string
		write *agent.ManagedUserFile
		want  string
	}{
		{"nil Path", &agent.ManagedUserFile{Uninstall: uninstall}, "Path"},
		{"nil Uninstall", &agent.ManagedUserFile{Path: ok}, "Uninstall"},
		{"resolve fails", &agent.ManagedUserFile{
			Path:      func() (string, error) { return "", errors.New("no home dir") },
			Uninstall: uninstall,
		}, "no home dir"},
		{"relative path", &agent.ManagedUserFile{
			Path:      func() (string, error) { return "settings.json", nil },
			Uninstall: uninstall,
		}, "not absolute"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			catalog := []agent.Agent{writerAdapter("alpha", agent.HooksPermissionKey, tc.write)}
			got, err := ManagedUserFiles(catalog)
			if err == nil {
				t.Fatalf("ManagedUserFiles() = %v, want an error naming %q", got, tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not name %q", err, tc.want)
			}
			if !strings.Contains(err.Error(), "alpha") {
				t.Errorf("error %q does not name the offending adapter", err)
			}
			// HookConfigs runs the same validation rather than filtering
			// first: a broken declaration must not become invisible to
			// --uninstall-hooks just because the filter would have dropped it.
			if _, err := HookConfigs(catalog); err == nil {
				t.Errorf("HookConfigs() accepted a declaration ManagedUserFiles() rejects")
			}
		})
	}
}

// TestNarrowedProjectionsSurviveABrokenDeclarationOnAnotherKey is the other
// half of the test above, and the two together are the whole rule: a narrowed
// projection validates exactly what it collects — no less (above) and no more
// (here).
//
// It exists because the first cut of #1437's shared narrowing resolved the
// whole catalog and filtered afterwards, which made `--uninstall-task-eta`
// log.Fatalf and remove NOTHING from the user's own CLAUDE.md when an unrelated
// adapter's path failed to resolve. Reproduced with built binaries against a
// relative XDG_CONFIG_HOME: kitty's remote-control path came back
// "relconf/kitty/kitty.conf", not absolute, and the command that exists to get
// irrlicht out of the user's prose file refused, citing kitty.
//
// A defect test: red before the fix, with `configsForKey` implemented as
// ManagedUserFiles-then-filter.
func TestNarrowedProjectionsSurviveABrokenDeclarationOnAnotherKey(t *testing.T) {
	broken := &agent.ManagedUserFile{
		Path:      func() (string, error) { return "relative/kitty.conf", nil },
		Uninstall: func() (bool, error) { return false, nil },
	}
	good := &agent.ManagedUserFile{
		Path:      func() (string, error) { return "/tmp/x/CLAUDE.md", nil },
		Uninstall: func() (bool, error) { return false, nil },
	}
	catalog := []agent.Agent{
		writerAdapter("kitty", "remote-control", broken),
		writerAdapter("alpha", agent.InstructionsPermissionKey, good),
	}

	got, err := InstructionConfigs(catalog)
	if err != nil {
		t.Fatalf("InstructionConfigs() failed on a broken declaration it does not collect: %v — "+
			"--uninstall-task-eta would abort without removing anything from the user's CLAUDE.md", err)
	}
	if len(got) != 1 || got[0].Adapter != "alpha" {
		t.Fatalf("InstructionConfigs() = %v, want exactly alpha/instructions", got)
	}

	// The full projection must still refuse it: --print-managed-files and the
	// recorder need completeness, and a short list there means the recorder
	// writes over the user's real files unprotected.
	if _, err := ManagedUserFiles(catalog); err == nil {
		t.Error("ManagedUserFiles() accepted a broken declaration — the recorder would read a short list as success")
	}
}

// TestManagedUserFilesIgnoresPermissionsThatDeclareNoFile is a lock: only a
// permission that actually declares a file contributes one. A transcripts
// permission (read-only) and the shared control permission (modify-kind, but
// writes nothing to disk) must not be swept in — the recorder would back up a
// file nobody writes, and uninstall would report on it.
func TestManagedUserFilesIgnoresPermissionsThatDeclareNoFile(t *testing.T) {
	a := agent.Agent{
		Identity: agent.Identity{Name: "alpha"},
		Permissions: []agent.Permission{
			{Key: "transcripts", Kind: permission.KindObserve},
			agent.ControlPermission(),
		},
	}
	got, err := ManagedUserFiles([]agent.Agent{a})
	if err != nil {
		t.Fatalf("ManagedUserFiles(): %v", err)
	}
	if len(got) != 0 {
		t.Errorf("ManagedUserFiles() = %v, want none", got)
	}
}

// TestHookConfigsIsTheHooksSliceOfManagedUserFiles pins the narrowing
// `--uninstall-hooks` depends on (#1383). Widening the declaration must not
// widen what that command touches: it removes hook entries and records the
// matching consent as denied, so running an instructions or kitty uninstaller
// from it would revoke a capability the user never asked it to change.
//
// Driven by synthetic declarations so it stays true of an adapter set that has
// not been written yet.
func TestHookConfigsIsTheHooksSliceOfManagedUserFiles(t *testing.T) {
	path := func(p string) func() (string, error) {
		return func() (string, error) { return p, nil }
	}
	uninstall := func() (bool, error) { return false, nil }
	catalog := []agent.Agent{
		writerAdapter("alpha", agent.HooksPermissionKey, &agent.ManagedUserFile{Path: path("/tmp/alpha/settings.json"), Uninstall: uninstall}),
		writerAdapter("alpha-memory", "instructions", &agent.ManagedUserFile{Path: path("/tmp/alpha/MEMORY.md"), Uninstall: uninstall}),
		writerAdapter("host", "remote-control", &agent.ManagedUserFile{Path: path("/tmp/host/host.conf"), Uninstall: uninstall}),
	}

	all, err := ManagedUserFiles(catalog)
	if err != nil {
		t.Fatalf("ManagedUserFiles(): %v", err)
	}
	if len(all) != 3 {
		t.Errorf("ManagedUserFiles() returned %d entries, want 3 — the recorder must protect every managed file (#1383)", len(all))
	}

	hooks, err := HookConfigs(catalog)
	if err != nil {
		t.Fatalf("HookConfigs(): %v", err)
	}
	if len(hooks) != 1 || hooks[0].Adapter != "alpha" {
		t.Fatalf("HookConfigs() = %v, want only alpha/hooks — --uninstall-hooks would otherwise "+
			"uninstall the instruction blocks and the host config patch nobody asked it to touch", hooks)
	}
}
