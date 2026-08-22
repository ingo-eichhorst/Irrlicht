// managedwrites_shapes_test.go is the committed mutation evidence for
// managedwrites_test.go's two arms.
//
// Per docs/testing-philosophy.md, a guard a change ADDS owes a deliberate
// mutation seen red, and the mutation is worth more committed than described:
// "a paragraph in a merged PR body is re-run by nothing and an assertion that
// silently stops discriminating looks exactly like health."
//
// Two things are graded here that the live catalog cannot express.
//
// The GRADING (gradeWrites/treeDiff) is driven with snapshots no correct
// adapter produces — a modified pre-existing file, a REMOVED file, an
// allowance that absorbs nothing, a path that merely shares a string prefix
// with an allowance root. Each row carries both verdicts on purpose: a
// detector that reports everything and one that reports correctly are
// indistinguishable without the cases that must stay SILENT.
//
// The WALK (packageLevelPathResolvers) is driven over source strings, one row
// per spelling, pinned to the verdict it must return. The want:false rows are
// where the value is (#1450): three of them are false positives a text-based
// rule would produce, and two are declared LIMITS of an ast.FuncDecl walk —
// pinned so the next person learns them from a test rather than from an
// incident.
package main

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// The grading
// ---------------------------------------------------------------------------

// TestGradeWritesReportsEveryKnownShape drives gradeWrites over one case per
// shape, each wrong in exactly ONE way, and pins both what it must report and
// what it must leave silent.
func TestGradeWritesReportsEveryKnownShape(t *testing.T) {
	const allowRoot = "irrlicht-home"
	allowed := []allowance{{Root: allowRoot, Reason: "the daemon's own state"}}

	cases := []struct {
		name           string
		observed       []string
		declared       []string
		wantUndeclared []string
		wantMissing    []string
		wantAllowed    int
	}{{
		name:     "the correct case — every observed write is declared, nothing declared is unwritten",
		observed: []string{"home/.vibe/config.toml", "home/.vibe/hooks.toml"},
		declared: []string{"home/.vibe/hooks.toml", "home/.vibe/config.toml"},
	}, {
		// #1739's shape: a second file the same Apply writes, undeclared.
		name:           "an undeclared write is reported",
		observed:       []string{"home/.kiro/agents/irrlicht.json", "home/.kiro/.irrlicht-prior-default.json"},
		declared:       []string{"home/.kiro/agents/irrlicht.json"},
		wantUndeclared: []string{"home/.kiro/.irrlicht-prior-default.json"},
	}, {
		// The shape a "which files APPEARED" check misses entirely: the file
		// was already there and Apply rewrote it. treeDiff is what makes this
		// reachable; gradeWrites only has to not special-case it.
		name:           "a modified pre-existing file is reported like a created one",
		observed:       []string{"home/.zshrc"},
		declared:       []string{"home/.claude/settings.json"},
		wantUndeclared: []string{"home/.zshrc"},
		wantMissing:    []string{"home/.claude/settings.json"},
	}, {
		name:        "a declared file nothing wrote is reported — the vacuity direction",
		observed:    []string{},
		declared:    []string{"home/.config/kitty/kitty.conf"},
		wantMissing: []string{"home/.config/kitty/kitty.conf"},
	}, {
		// The failure a misconfigured sandbox produces. Without the Missing
		// direction this row is a clean sweep, which is the whole reason
		// gradeWrites reports both ways.
		name:     "nothing observed and nothing declared is NOT reported by this function",
		observed: []string{},
		declared: []string{},
	}, {
		name:        "a file inside an allowance is absorbed, not reported",
		observed:    []string{"irrlicht-home/sessions.db", "home/.codex/hooks.json"},
		declared:    []string{"home/.codex/hooks.json"},
		wantAllowed: 1,
	}, {
		name:        "the allowance root itself is absorbed",
		observed:    []string{"irrlicht-home"},
		declared:    []string{},
		wantAllowed: 1,
	}, {
		// Segment-aware containment. A string-prefix test would absorb this,
		// and it is a different file entirely.
		name:           "a sibling that merely shares the allowance root's prefix is reported",
		observed:       []string{"irrlicht-home-backup/leak.json"},
		declared:       []string{},
		wantUndeclared: []string{"irrlicht-home-backup/leak.json"},
	}, {
		// A permission that DELETES a user file has written to it in every
		// sense this declaration is about. treeDiff surfaces the removal; this
		// row pins that gradeWrites treats it like any other change.
		name:           "a removed undeclared file is reported",
		observed:       []string{"home/.claude/settings.json.bak"},
		declared:       []string{},
		wantUndeclared: []string{"home/.claude/settings.json.bak"},
	}, {
		name:           "several undeclared writes are all reported, sorted",
		observed:       []string{"home/b.json", "home/a.json"},
		declared:       []string{},
		wantUndeclared: []string{"home/a.json", "home/b.json"},
	}}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := gradeWrites(tc.observed, tc.declared, allowed)
			if got, want := strings.Join(v.Undeclared, ","), strings.Join(tc.wantUndeclared, ","); got != want {
				t.Errorf("Undeclared = [%s], want [%s]", got, want)
			}
			if got, want := strings.Join(v.Missing, ","), strings.Join(tc.wantMissing, ","); got != want {
				t.Errorf("Missing = [%s], want [%s]", got, want)
			}
			if got := v.Allowed[allowRoot]; got != tc.wantAllowed {
				t.Errorf("allowance %q absorbed %d paths, want %d", allowRoot, got, tc.wantAllowed)
			}
		})
	}
}

// TestTreeDiffSeesEveryKindOfChange pins what a snapshot pair can and cannot
// tell apart. The last row is a DECLARED LIMIT rather than a defect: a write
// that restores size, mtime and content exactly is not a change any diff of
// this shape can see, and saying so here is cheaper than someone re-deriving it
// from a green test.
func TestTreeDiffSeesEveryKindOfChange(t *testing.T) {
	base := func() map[string]fileState {
		return map[string]fileState{
			"home/.codex/hooks.json": {Size: 10, ModNano: 100, Sum: "aaa"},
			"home/.zshrc":            {Size: 20, ModNano: 200, Sum: "bbb"},
		}
	}

	cases := []struct {
		name string
		// mutate turns the "after" snapshot into the case.
		mutate func(map[string]fileState)
		want   []string
	}{{
		name:   "nothing changed",
		mutate: func(map[string]fileState) {},
		want:   nil,
	}, {
		name: "a created file",
		mutate: func(m map[string]fileState) {
			m["home/.vibe/hooks.toml"] = fileState{Size: 1, ModNano: 300, Sum: "ccc"}
		},
		want: []string{"home/.vibe/hooks.toml"},
	}, {
		name:   "content rewritten at the same size",
		mutate: func(m map[string]fileState) { s := m["home/.zshrc"]; s.Sum = "zzz"; m["home/.zshrc"] = s },
		want:   []string{"home/.zshrc"},
	}, {
		name:   "identical content rewritten — caught by mtime alone",
		mutate: func(m map[string]fileState) { s := m["home/.zshrc"]; s.ModNano = 999; m["home/.zshrc"] = s },
		want:   []string{"home/.zshrc"},
	}, {
		name:   "a removed file",
		mutate: func(m map[string]fileState) { delete(m, "home/.codex/hooks.json") },
		want:   []string{"home/.codex/hooks.json"},
	}, {
		name: "everything at once, sorted",
		mutate: func(m map[string]fileState) {
			delete(m, "home/.zshrc")
			m["home/.a"] = fileState{Size: 1}
		},
		want: []string{"home/.a", "home/.zshrc"},
	}, {
		// DECLARED LIMIT. Kept as a want:none row because it is the one thing
		// this mechanism cannot observe, and a reader who assumes otherwise
		// would trust a green further than it goes.
		name: "a write that restores size, mtime AND content is invisible — the declared limit",
		mutate: func(m map[string]fileState) {
			m["home/.zshrc"] = fileState{Size: 20, ModNano: 200, Sum: "bbb"}
		},
		want: nil,
	}}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			after := base()
			tc.mutate(after)
			got := treeDiff(base(), after)
			if strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Errorf("treeDiff = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestPathUnderDirRefusesEveryUncontainedShape pins the fail-closed
// precondition's containment rule. Every want:false row here is a path the
// precondition must REFUSE to run a closure for, and the traversal rows are why
// the rule is filepath.Rel rather than a string prefix.
func TestPathUnderDirRefusesEveryUncontainedShape(t *testing.T) {
	const root = "/tmp/scratch/001"
	cases := []struct {
		path string
		want bool
	}{
		{"/tmp/scratch/001", true},
		{"/tmp/scratch/001/home/.claude/settings.json", true},
		{"/tmp/scratch/001/./home/x", true},
		{"/tmp/scratch/0011/home/x", false},  // sibling sharing the prefix
		{"/tmp/scratch/001/../002/x", false}, // traversal back out
		{"/tmp/scratch", false},              // the parent
		{"/Users/ingo/.claude/settings.json", false},
		{"relative/path", false}, // not absolute: cannot be proven contained
		{"", false},
	}
	for _, tc := range cases {
		if got := pathUnderDir(root, tc.path); got != tc.want {
			t.Errorf("pathUnderDir(%q, %q) = %v, want %v", root, tc.path, got, tc.want)
		}
	}
	// A relative ROOT can never contain anything: the precondition would
	// otherwise wave a path through against a root it could not resolve.
	if pathUnderDir("scratch", "/tmp/scratch/x") {
		t.Error("pathUnderDir accepted a relative root — that direction fails open")
	}
}

// ---------------------------------------------------------------------------
// The walk
// ---------------------------------------------------------------------------

// TestPackageLevelPathResolversPinsEverySpelling is the static arm's corpus:
// one source file per spelling, pinned to whether the walk must report it.
//
// The want:false rows carry the value. Three are false positives a text-based
// rule ("grep for func.*(string, error)") would produce; two are declared
// limits of walking ast.FuncDecl over syntactic types.
func TestPackageLevelPathResolversPinsEverySpelling(t *testing.T) {
	cases := []struct {
		name string
		// file is the name inside the synthetic package. Empty means
		// "a.go" — only the _test.go row needs to say otherwise, and repeating
		// it on every other row is noise a reader has to check anyway.
		file string
		src  string
		want []string
	}{{
		name: "the ordinary spelling",
		src:  "package p\nfunc settingsPath() (string, error) { return \"\", nil }\n",
		want: []string{"settingsPath"},
	}, {
		name: "named results",
		src:  "package p\nfunc settingsPath() (path string, err error) { return }\n",
		want: []string{"settingsPath"},
	}, {
		name: "two names on one result field",
		src:  "package p\nfunc twoStrings() (a, b string) { return }\n",
		want: nil, // (string, string), not (string, error)
	}, {
		name: "exported is still a resolver",
		src:  "package p\nfunc SettingsPath() (string, error) { return \"\", nil }\n",
		want: []string{"SettingsPath"},
	}, {
		// FALSE POSITIVE a text rule produces: it takes a parameter, so it is
		// not something a ManagedUserFile field can hold.
		name: "takes a parameter",
		src:  "package p\nfunc pathUnder(root string) (string, error) { return \"\", nil }\n",
		want: nil,
	}, {
		// FALSE POSITIVE: the result types are not (string, error).
		name: "returns the wrong pair",
		src:  "package p\nfunc info() (string, bool) { return \"\", false }\n",
		want: nil,
	}, {
		// FALSE POSITIVE: a METHOD cannot be a ManagedUserFile resolver, and
		// its receiver is what tells it apart from one.
		name: "a method with the same signature",
		src:  "package p\ntype T struct{}\nfunc (t T) settingsPath() (string, error) { return \"\", nil }\n",
		want: nil,
	}, {
		// DECLARED LIMIT, the same one core/architecture_hookbody_test.go and
		// contracttesting/seam_walk_corpus_test.go pin for their own walks: a
		// func literal held in a package-level var hangs off a ValueSpec, not a
		// FuncDecl. Note the direction — this one reads as "no undeclared
		// resolver", so it is a hole rather than a loud refusal, which is
		// exactly why it is written down.
		name: "a func literal in a package-level var",
		src:  "package p\nvar settingsPath = func() (string, error) { return \"\", nil }\n",
		want: nil,
	}, {
		// DECLARED LIMIT: the walk compares result types syntactically, so a
		// local alias of error is not recognised.
		name: "an aliased error type",
		src:  "package p\ntype myErr = error\nfunc settingsPath() (string, myErr) { return \"\", nil }\n",
		want: nil,
	}, {
		// _test.go files are excluded: a resolver a test declares is not one an
		// Apply closure can write through.
		name: "declared in a test file",
		file: "a_test.go",
		src:  "package p\nfunc settingsPath() (string, error) { return \"\", nil }\n",
		want: nil,
	}, {
		name: "several in one file, sorted",
		src: "package p\n" +
			"func zPath() (string, error) { return \"\", nil }\n" +
			"func aPath() (string, error) { return \"\", nil }\n",
		want: []string{"aPath", "zPath"},
	}}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			name := tc.file
			if name == "" {
				name = "a.go"
			}
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, name), []byte(tc.src), 0o600); err != nil {
				t.Fatal(err)
			}
			// The row's own vacuity guard: the case must actually contain the
			// construct it plants, or a corpus that quietly stopped carrying
			// its own test cases would read as a pass (the rule
			// core/architecture_hookbody_shapes_test.go states).
			assertParses(t, dir, name)

			got, err := packageLevelPathResolvers(dir)
			if err != nil {
				t.Fatalf("packageLevelPathResolvers: %v", err)
			}
			sort.Strings(got)
			if strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Errorf("resolvers = %v, want %v", got, tc.want)
			}
		})
	}
}

// assertParses is the corpus's own guard: a row whose source no longer compiles
// as Go would report "no resolvers" for a reason unrelated to the spelling
// under test.
func assertParses(t *testing.T, dir, file string) {
	t.Helper()
	fset := token.NewFileSet()
	path := filepath.Join(dir, file)
	f, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatalf("the corpus row's own source does not parse (%s): %v", file, err)
	}
	if f.Name == nil || f.Name.Name == "" {
		t.Fatalf("the corpus row's own source declares no package")
	}
	// And it must contain at least one declaration, so an empty row cannot
	// masquerade as a want:nil case.
	if len(f.Decls) == 0 {
		t.Fatalf("the corpus row's own source declares nothing")
	}
}

// TestPackageLevelPathResolversRefusesAnUnreadableDirectory is the walk's
// "fail loudly when it cannot run" arm: a directory it cannot parse must be an
// error, never an empty result. Both read as "no undeclared resolver" to a
// caller that ignores the error, which is why the caller t.Fatals on it.
func TestPackageLevelPathResolversRefusesAnUnreadableDirectory(t *testing.T) {
	if _, err := packageLevelPathResolvers(filepath.Join(t.TempDir(), "no-such-dir")); err == nil {
		t.Error("parsing a nonexistent directory returned no error — an unreadable package " +
			"would report the same empty result as a clean one")
	}

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "broken.go"), []byte("package p\nfunc ("), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := packageLevelPathResolvers(dir); err == nil {
		t.Error("parsing an unparseable file returned no error — a validator that cannot read " +
			"its input must check MORE, never less")
	}
}

// TestPackageDirOfResolverRefusesAnythingItCannotMap pins the reflection→
// directory mapping, including the shapes that must not silently resolve to
// some other package's source.
func TestPackageDirOfResolverRefusesAnythingItCannotMap(t *testing.T) {
	dir, pkg, err := packageDirOfResolver("irrlicht/core/adapters/inbound/agents/kirocli.priorDefaultStatePath")
	if err != nil {
		t.Fatalf("mapping a real resolver: %v", err)
	}
	if pkg != "kirocli" {
		t.Errorf("package name = %q, want kirocli", pkg)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "hookinstaller.go")); statErr != nil {
		t.Errorf("mapped %q, which does not hold kirocli's installer: %v", dir, statErr)
	}

	for _, bad := range []struct{ name, in string }{
		{"no package qualifier", "someFuncWithNoDot"},
		{"another module", "example.com/other/pkg.Resolver"},
		{"a package that does not exist", "irrlicht/core/adapters/inbound/agents/nosuchadapter.Resolver"},
	} {
		if _, _, err := packageDirOfResolver(bad.in); err == nil {
			t.Errorf("%s: packageDirOfResolver(%q) returned no error — it would grade the "+
				"wrong package, or none, in silence", bad.name, bad.in)
		}
	}
}

// TestEnvKeptDuringScrubKeepsOnlyWhatTheRunnerNeeds pins the allow-list. Every
// per-agent home override an adapter honours today is listed explicitly, so a
// change that started keeping one — which would point that adapter's install at
// the developer's real config while this test believed it was sandboxed — is a
// failure rather than a silent widening.
func TestEnvKeptDuringScrubKeepsOnlyWhatTheRunnerNeeds(t *testing.T) {
	for _, name := range []string{"PATH", "TMPDIR", "GOCACHE"} {
		if !envKeptDuringScrub(name) {
			t.Errorf("the scrub clears %s, which the test runner needs", name)
		}
	}
	// The overrides #1739's righome work and services.sharedConfigRefusal
	// enumerate, plus HOME itself, which newScratchHome sets explicitly after
	// the scrub rather than keeping.
	for _, name := range []string{
		"HOME", "CODEX_HOME", "COPILOT_HOME", "KIRO_HOME", "VIBE_HOME",
		"XDG_CONFIG_HOME", "CLAUDE_CONFIG_DIR", "IRRLICHT_HOME",
		"IRRLICHT_ALLOW_SHARED_CONFIG_WRITES", "PI_CODING_AGENT_SESSION_DIR",
	} {
		if envKeptDuringScrub(name) {
			t.Errorf("the scrub KEEPS %s — an adapter honouring it would resolve its install "+
				"against the developer's real environment while this test reported on an "+
				"empty scratch tree", name)
		}
	}
}
