// hookinstaller_realconfig_test.go grades this adapter's hooks install
// against a settings.json CLAUDE CODE ITSELF WROTE, committed verbatim under
// testdata/ (issue #1756, the registry-wide sibling of #1753/#1755's
// mistral-vibe fixture).
//
// Every other test in this package builds its own settings.json by hand.
// This one is the maintainer's own real ~/.claude/settings.json, captured
// whole — scanned for anything credential-shaped before committing (none
// found: the only hits were env-var NAMES inside autoMode.environment,
// explicitly labelled "(names only, not values)" in the file itself) — and it
// carries a construct no hand-written fixture ever would: several events
// already hold MULTIPLE hook groups that all point at our own endpoint. A
// single EnsureHooksInstalled call never produces more than one group per
// event, so this file is evidence of real accumulated history (repeated
// installs across daemon rebuilds) rather than something a test constructed.
package claudecode

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// realConfigFixture is the committed copy of the maintainer's own
// ~/.claude/settings.json, produced by Claude Code 2.1.240.
const realConfigFixture = "testdata/real-config-2.1.240.json"

// TestRealClaudeCodeConfigFixture_StillCarriesRealComplexity is the vacuity
// guard for every test in this file: a fixture quietly hand-minimised down to
// "just enough JSON to parse" would leave the round-trip tests below green
// while covering nothing more than the package's own hand-written fixtures
// already do.
func TestRealClaudeCodeConfigFixture_StillCarriesRealComplexity(t *testing.T) {
	src, err := os.ReadFile(realConfigFixture)
	if err != nil {
		t.Fatalf("reading the committed fixture: %v", err)
	}
	if len(src) < 2000 {
		t.Errorf("fixture is %d bytes, want >= 2000 — it has been minimised, which is exactly "+
			"what would hide a defect the same way a hand-written fixture already does", len(src))
	}

	var doc map[string]interface{}
	if err := json.Unmarshal(src, &doc); err != nil {
		t.Fatalf("fixture does not parse as JSON: %v", err)
	}

	// Real, non-hook complexity a hand-written fixture never has a reason to
	// include: the maintainer's actual model/UI/policy preferences, all of
	// which EnsureHooksInstalled/UninstallHooks must leave untouched.
	for _, key := range []string{"model", "statusLine", "autoMode", "alwaysThinkingEnabled", "effortLevel", "theme"} {
		if _, ok := doc[key]; !ok {
			t.Errorf("fixture is missing top-level key %q — it no longer carries the real, "+
				"non-hook complexity this test exists to exercise the merge against", key)
		}
	}

	hooksMap, ok := doc["hooks"].(map[string]interface{})
	if !ok {
		t.Fatal("fixture has no top-level \"hooks\" object")
	}

	// The construct that matters: at least one event already carries MORE
	// THAN ONE hook group before any test touches the file. A single
	// EnsureHooksInstalled call from empty never produces more than one group
	// per event (see TestEnsureHooksInstalled_CreatesFileIfAbsent in
	// hookinstaller_test.go) — so more than one is real accumulated history
	// a hand-written fixture would never spontaneously contain, and it is
	// exactly the shape an uninstall that only removed the FIRST matching
	// group per event would fail to fully clean up.
	maxGroups := 0
	for _, arr := range hooksMap {
		groups, ok := arr.([]interface{})
		if !ok {
			continue
		}
		if len(groups) > maxGroups {
			maxGroups = len(groups)
		}
	}
	if maxGroups < 2 {
		t.Errorf("no event in the fixture carries more than %d hook group(s) — the fixture has "+
			"been minimised to a single-group shape, which is exactly what a hand-written test "+
			"already covers", maxGroups)
	}
}

// seedRealConfig copies the committed fixture into a temp HOME's
// ~/.claude/settings.json and returns the temp home and the settings path.
func seedRealConfig(t *testing.T) (home, path string) {
	t.Helper()
	src, err := os.ReadFile(realConfigFixture)
	if err != nil {
		t.Fatalf("reading the committed fixture: %v", err)
	}
	home = withTempHome(t)
	path = filepath.Join(home, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, src, 0o600); err != nil {
		t.Fatalf("seeding settings.json: %v", err)
	}
	return home, path
}

// nonHookKeys returns doc with the "hooks" key removed, for comparing
// everything ELSE in the document across an uninstall/reinstall round trip.
func nonHookKeys(doc map[string]interface{}) map[string]interface{} {
	out := make(map[string]interface{}, len(doc))
	for k, v := range doc {
		if k == "hooks" {
			continue
		}
		out[k] = v
	}
	return out
}

// assertNonHookKeysUnchanged fails unless every non-"hooks" key in before
// survives into after with an identical value (compared as decoded JSON, so
// key/array ordering differences from the atomic rewrite don't cause a false
// failure — only a VALUE difference would).
func assertNonHookKeysUnchanged(t *testing.T, before, after map[string]interface{}) {
	t.Helper()
	wantOther := nonHookKeys(before)
	gotOther := nonHookKeys(after)
	if len(wantOther) != len(gotOther) {
		t.Fatalf("non-hook key count changed: had %d, now %d", len(wantOther), len(gotOther))
	}
	for k, wantV := range wantOther {
		gotV, ok := gotOther[k]
		if !ok {
			t.Errorf("dropped unrelated key %q", k)
			continue
		}
		wantJSON, _ := json.Marshal(wantV)
		gotJSON, _ := json.Marshal(gotV)
		if string(wantJSON) != string(gotJSON) {
			t.Errorf("changed unrelated key %q:\n  before: %s\n  after:  %s", k, wantJSON, gotJSON)
		}
	}
}

// TestUninstallHooks_FromAConfigClaudeCodeItselfWrote uninstalls against the
// real fixture and asserts every one of our entries is gone — including from
// EVERY duplicate group the real file carries, not just the first — while
// every other real key (model, statusLine, autoMode, …) survives untouched.
func TestUninstallHooks_FromAConfigClaudeCodeItselfWrote(t *testing.T) {
	_, path := seedRealConfig(t)
	before := readJSON(t, path)

	modified, err := UninstallHooks()
	if err != nil {
		t.Fatalf("UninstallHooks into a config Claude Code itself wrote: %v", err)
	}
	if !modified {
		t.Fatal("UninstallHooks reported no modification against a config with real installed hooks")
	}

	after := readJSON(t, path)

	// Every group in every managed event must be gone — not just the first
	// of several duplicates.
	if hooksMap, ok := after["hooks"].(map[string]interface{}); ok {
		for _, event := range installedHookEvents {
			if arr, ok := hooksMap[event]; ok {
				t.Errorf("%s still has hook entries after uninstall: %v", event, arr)
			}
		}
	}

	// Every other real key survives byte-for-byte.
	assertNonHookKeysUnchanged(t, before, after)
}

// TestEnsureHooksInstalled_ReinstallsIntoAConfigClaudeCodeItselfWrote runs the
// full round trip against the real fixture: uninstall, then reinstall, and
// confirm the result is intact and every non-hook key is still exactly what
// Claude Code itself wrote.
func TestEnsureHooksInstalled_ReinstallsIntoAConfigClaudeCodeItselfWrote(t *testing.T) {
	_, path := seedRealConfig(t)
	before := readJSON(t, path)

	if _, err := UninstallHooks(); err != nil {
		t.Fatalf("UninstallHooks: %v", err)
	}
	modified, err := EnsureHooksInstalled()
	if err != nil {
		t.Fatalf("EnsureHooksInstalled into a config Claude Code itself wrote: %v", err)
	}
	if !modified {
		t.Fatal("EnsureHooksInstalled reported no modification on a fresh reinstall")
	}

	status, err := VerifyHooksInstalled()
	if err != nil {
		t.Fatalf("VerifyHooksInstalled: %v", err)
	}
	if !status.Intact() {
		t.Fatalf("VerifyHooksInstalled reports %+v after reinstall, want intact", status)
	}

	after := readJSON(t, path)
	assertNonHookKeysUnchanged(t, before, after)
}
