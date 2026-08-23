// hookrealfixture_test.go is the registry-wide tripwire for issue #1756: an
// adapter that installs hooks into a user's config must declare a REAL
// fixture of that config — a document captured from the agent's own output,
// not one any test constructed — and exercise a guard against it, or say
// explicitly why it does not.
//
// # The gap this closes
//
// #1753 shipped because every hook-install test in this tree graded against a
// config the adapter's OWN test constructed: a document whose shape the test
// author already knew how to handle. mistral-vibe's real config.toml
// (vibe/testdata/real-config-2.19.1.toml, captured for #1755) carries twelve
// multi-line arrays no hand-written fixture ever had; hooktoml's splicer
// refused all of them, Apply failed, and consent kept reading granted. It was
// found by accident, during a live recording, not by any test — and nothing
// short of a real fixture would have caught it, because every contract family
// in hookcontracts_test.go is wired against fixtures the adapter itself
// supplies.
//
// TestEveryHookInstallDeclaresARealConfigFixture asks the same question
// TestEveryHookInstallDeclaresAVerifier (#1372) and
// TestEveryHookInstallDeclaresAVersionFloor (#1365) already ask for their own
// axis: does a NEW hooks-installing adapter get this scrutiny by default, or
// does it silently inherit the blind spot every adapter had before #1753
// forced one exception? Four of five hooks adapters — the count named in
// #1756 when it was filed — had nothing.
//
// # What "real" means, and what this test does NOT dictate
//
// #1755's answer (restated at agent.RealConfigFixture's own doc comment): a
// committed file captured from the agent's own output, secrets redacted, plus
// a guard asserting it still carries the construct that mattered. The guard's
// assertion is necessarily adapter-specific — vibe's counts multi-line TOML
// arrays; claudecode's and geminicli's assert non-hook real-world keys survive
// a round trip. This test does not dictate what a guard checks, only that one
// exists and reads the exact declared file: requiring more would mean this
// walk encoding its own opinion of "real enough", which is exactly the kind of
// blind spot #1756 is about one level up.
//
// # Known, explicit gaps — not a silent skip
//
// A hooks-installing adapter not covered here is either declared (Path +
// CLIVersion + an existing, non-trivial file + a guard test that reads it) or
// named in knownFixtureGaps() with a real, checked reason. There is no third
// way to satisfy this test: an adapter in neither bucket fails loudly, by
// design, so a new adapter is covered by default rather than by whoever
// remembers to add a row (the same failure #1740 was about, one level up).
package agents

import (
	"go/build"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"irrlicht/core/domain/agent"
)

// minRealFixtureBytes is the floor below which a declared fixture reads as a
// stub rather than a captured document. Deliberately small and deliberately
// NOT tied to any one adapter's format: it exists to catch an empty or
// near-empty placeholder, not to grade realism — that job belongs to each
// adapter's own guard test, which knows what its format's real complexity
// looks like (TestRealVibeConfigFixture_StillCarriesTheConstructThatBrokeIt's
// multi-line-array count, TestRealClaudeCodeConfigFixture's non-hook-key
// survival, …).
const minRealFixtureBytes = 200

// knownFixtureGaps names every hooks-installing adapter this test does NOT
// require a declared RealFixture from, each with the reason — checked, not
// assumed, per AGENTS.md's "dismissals carry evidence" rule.
//
// Every reason below was verified directly on the machine this issue (#1756)
// was implemented on, not inferred from the adapter's design alone:
//
//   - codex: ~/.codex/hooks.json is a file irrlicht created and is the ONLY
//     writer of (hookinstaller.go's own package doc: a dedicated file "so a
//     malformed write can never corrupt the user's main Codex config"). Read
//     on this machine, its entire content was our own previously-installed
//     entries — capturing it as a "real" fixture would be circular: it would
//     prove only that our installer's own output still parses, which every
//     hand-written test in hookinstaller_test.go already covers. There is no
//     non-irrlicht-authored specimen of this file to capture, even in
//     principle, unless a real Codex user hand-adds their own hook to it.
//   - copilot: $COPILOT_HOME/hooks/irrlicht.json had never been created on
//     this machine at all (os.Stat: no such file or directory). Same
//     dedicated-file design as codex (package doc: "irrlicht owns one
//     dedicated file"), so the same reasoning applies with an even weaker
//     case — there was not even our own output to capture.
//   - antigravity: HooksPath's own doc calls ~/.gemini/config/hooks.json "the
//     shared, user-owned document antigravity loads hooks from" — the SAME
//     risk class as vibe/claudecode/geminicli, genuinely worth a real fixture.
//     But the file did not exist on this machine (hooks never installed here)
//     — no specimen was available this session. Left as the best candidate
//     for a fast, cheap follow-up once a real install exists somewhere.
//   - hermes: ConfigPath's own doc calls ~/.hermes/config.yaml "hermes' own
//     config.yaml" — again the same shared-file risk class, and a real,
//     substantial specimen DOES exist on this machine (real model config
//     alongside an already-installed hooks: block). Left uncovered because it
//     is outside #1756's named scope (the issue enumerated five adapters
//     total — vibe plus claudecode/codex/copilot/geminicli — and hermes'
//     hooks install landed after #1756 was filed) and because turning the
//     on-disk file into a clean "before our install" fixture needs the
//     adapter's own UninstallHooks run once against a captured copy, which is
//     real, separate work this pass did not budget for. The best candidate
//     for a fast follow-up: the data already sits on this machine.
//   - kiro-cli, opencode, pi: also landed after #1756 was filed. opencode's
//     PluginPath and pi's ExtensionPath both name a dedicated, irrlicht-
//     authored file in their own doc comments (a .js plugin/extension nothing
//     else writes) — the same low-risk category as codex/copilot. kiro-cli's
//     Path is likewise dedicated, but its Also entries include a real
//     kiroSettingsPath this pass did not verify one way or the other — left
//     unverified rather than asserted.
func knownFixtureGaps() map[string]string {
	return map[string]string{
		"codex": "hooks.json is a dedicated file only irrlicht ever writes; the on-disk " +
			"specimen on the machine this was checked on held only irrlicht's own prior " +
			"install, so capturing it would be circular (see this function's doc comment)",
		"copilot": "hooks/irrlicht.json is a dedicated file only irrlicht ever writes, and it " +
			"had never been created on the machine this was checked on — no specimen at all, " +
			"real or synthetic",
		"antigravity": "hooks.json is the shared document antigravity itself loads named hooks " +
			"from (genuinely the same risk class as vibe/claudecode/geminicli), but it did not " +
			"exist on the machine this was checked on — no specimen was available this session",
		"hermes": "config.yaml is hermes' own shared config (genuinely the same risk class as " +
			"vibe/claudecode/geminicli) and a real specimen exists on the machine this was " +
			"checked on, but hermes' hooks install landed after #1756 was filed and turning the " +
			"on-disk file into a clean pre-install fixture is separate, unbudgeted work — the " +
			"best candidate for a fast follow-up",
		"kiro-cli": "landed after #1756 was filed; its own irrlichtAgentConfigPath is a " +
			"dedicated file, but its Also entries reach a real kiroSettingsPath this pass did " +
			"not verify one way or the other",
		"opencode": "landed after #1756 was filed; PluginPath names a dedicated, irrlicht-" +
			"authored plugin file (~/.config/opencode/plugin/irrlicht.js) nothing else writes",
		"pi": "landed after #1756 was filed; ExtensionPath names a dedicated, irrlicht-authored " +
			"extension file (~/.pi/agent/extensions/irrlicht.js) nothing else writes",
	}
}

// adapterPackageDir resolves the on-disk directory for an adapter's Go
// package, so a declared RealFixture.Path (relative to that package) and a
// guard test (in a _test.go file in that same directory) can both be checked
// against real files rather than trusted at face value.
func adapterPackageDir(t *testing.T, importPath string) string {
	t.Helper()
	pkg, err := build.Import(importPath, ".", build.FindOnly)
	if err != nil {
		t.Fatalf("resolving package directory for %q: %v", importPath, err)
	}
	return pkg.Dir
}

// testPackageReferencesFixture reports whether some _test.go file in dir
// contains the fixture's relative path as a quoted string literal — i.e.
// whether some test in the adapter's own package actually reads the declared
// file, rather than the fixture sitting under testdata/ unused.
//
// A plain substring scan over source text, not an AST/reachability walk like
// hookcontracts_test.go's scanContractWirings: this asks a narrower question
// ("is the file referenced by name at all") than "wired and reachable", and a
// false positive from a match inside a comment is a fail-open risk this test
// accepts explicitly rather than hides — see the package doc's "does NOT
// dictate what a guard checks".
func testPackageReferencesFixture(t *testing.T, dir, relPath string) bool {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading %s: %v", dir, err)
	}
	needle := `"` + relPath + `"`
	found := false
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		src, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatalf("reading %s: %v", e.Name(), err)
		}
		if strings.Contains(string(src), needle) {
			found = true
			break
		}
	}
	return found
}

// TestEveryHookInstallDeclaresARealConfigFixture is the registry-wide
// tripwire for issue #1756. For every hooks-installing adapter it requires
// EITHER:
//
//   - a declared agent.RealConfigFixture (Path + CLIVersion, both non-empty),
//     whose Path resolves to an existing, non-trivial file relative to the
//     adapter's own package directory, referenced by name in at least one of
//     that package's own _test.go files (a guard reads it) — OR
//   - an entry in knownFixtureGaps() naming why not.
//
// An adapter satisfying neither fails loudly: the point of this test,
// mirroring #1372/#1365's own registry tripwires, is that a NEW adapter is
// covered by default rather than by someone remembering to add a row.
func TestEveryHookInstallDeclaresARealConfigFixture(t *testing.T) {
	installs := hookInstallingAdapters(t)
	gaps := knownFixtureGaps()

	seenAdapters := map[string]bool{}
	for _, in := range installs {
		seenAdapters[in.Adapter] = true

		var p *agent.Permission
		for _, a := range All() {
			if a.Identity.Name != in.Adapter {
				continue
			}
			for i := range a.Permissions {
				if a.Permissions[i].Key == in.Key {
					p = &a.Permissions[i]
				}
			}
		}
		if p == nil {
			t.Fatalf("%s/%s: hookInstallingAdapters found this permission but All() no longer "+
				"has it — the registry changed under this test", in.Adapter, in.Key)
		}

		reason, gapped := gaps[in.Adapter]
		fixture := p.Writes.RealFixture

		if gapped {
			if reason == "" {
				t.Errorf("%s: knownFixtureGaps() names this adapter with an EMPTY reason — a "+
					"gap with no stated reason is a silent skip wearing a visible one", in.Adapter)
			}
			if fixture != nil {
				t.Errorf("%s: is in knownFixtureGaps() (%q) but ALSO declares a RealFixture "+
					"(%s) — the gap entry is stale now that a fixture exists; remove it from "+
					"knownFixtureGaps()", in.Adapter, reason, fixture.Path)
			}
			continue
		}

		if fixture == nil {
			t.Errorf("%s/%s installs hooks but declares no RealFixture (#1756), and is not in "+
				"this test's knownFixtureGaps() either — capture a real config this adapter's "+
				"own output produced (or the shared file it manages), secrets redacted, commit "+
				"it under testdata/, and declare it on Writes.RealFixture; or add a reasoned "+
				"entry to knownFixtureGaps() if a real capture is genuinely not feasible right "+
				"now (see #1755/vibe for the pattern)", in.Adapter, in.Key)
			continue
		}
		if fixture.Path == "" {
			t.Errorf("%s/%s declares a RealFixture with an empty Path", in.Adapter, in.Key)
			continue
		}
		if fixture.CLIVersion == "" {
			t.Errorf("%s/%s declares a RealFixture at %s with no CLIVersion — a fixture with no "+
				"version stamp is evidence nobody can date (the same staleness problem "+
				"docs/replay-testing.md names for replay goldens)", in.Adapter, in.Key, fixture.Path)
		}

		dir := adapterPackageDir(t, in.Pkg)
		full := filepath.Join(dir, fixture.Path)
		info, err := os.Stat(full)
		if err != nil {
			t.Errorf("%s/%s declares RealFixture.Path=%s but it does not exist at %s: %v",
				in.Adapter, in.Key, fixture.Path, full, err)
			continue
		}
		if info.Size() < minRealFixtureBytes {
			t.Errorf("%s/%s's real fixture %s is %d bytes, want >= %d — too small to be a "+
				"captured real-world document rather than a stub",
				in.Adapter, in.Key, fixture.Path, info.Size(), minRealFixtureBytes)
		}

		if !testPackageReferencesFixture(t, dir, fixture.Path) {
			t.Errorf("%s/%s declares RealFixture.Path=%s but no _test.go file in %s references "+
				"it by name — a fixture nothing reads is exactly the vacuity #1753 shipped "+
				"through: add a guard test that reads this file and exercises install/uninstall "+
				"against it (see vibe's hookinstaller_realconfig_test.go for the pattern)",
				in.Adapter, in.Key, fixture.Path, dir)
		}
	}

	// A gap entry for an adapter that is no longer a hooks-installing adapter
	// at all (renamed, removed, or its hooks permission dropped) is stale and
	// silently over-broad — it would keep exempting a name nothing asks about
	// anymore.
	for name := range gaps {
		if !seenAdapters[name] {
			t.Errorf("knownFixtureGaps() names %q, but no hooks-installing adapter by that name "+
				"exists in the registry — this gap entry is stale (renamed, removed, or its "+
				"hooks permission was dropped) and should be deleted", name)
		}
	}
}
