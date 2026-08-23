// hookinstaller_realconfig_test.go grades this adapter's hooks install
// against a settings.json GEMINI CLI ITSELF WROTE, committed verbatim under
// testdata/ (issue #1756, the registry-wide sibling of #1753/#1755's
// mistral-vibe fixture).
//
// TestUninstallPreservesUnrelatedSettingsKeys in hookinstaller_test.go already
// covers the shared-file property with a two-key hand-written seed. This file
// exercises the identical property against the maintainer's own real
// ~/.gemini/settings.json — real security/ide preferences alongside the
// "hooks" key, captured whole and scanned for anything credential-shaped
// before committing (none found: security.auth only names the auth TYPE,
// "oauth-personal", never a token).
package geminicli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// realConfigFixture is the committed copy of the maintainer's own
// ~/.gemini/settings.json, produced by Gemini CLI 0.56.0.
const realConfigFixture = "testdata/real-config-0.56.0.json"

// TestRealGeminiConfigFixture_StillCarriesRealComplexity is the vacuity guard
// for every test in this file: a fixture quietly hand-minimised down to "just
// enough JSON to parse" would leave the round-trip tests below green while
// covering nothing more than TestUninstallPreservesUnrelatedSettingsKeys's own
// two-key seed already does.
func TestRealGeminiConfigFixture_StillCarriesRealComplexity(t *testing.T) {
	src, err := os.ReadFile(realConfigFixture)
	if err != nil {
		t.Fatalf("reading the committed fixture: %v", err)
	}
	if len(src) < 400 {
		t.Errorf("fixture is %d bytes, want >= 400 — it has been minimised, which is exactly "+
			"what a hand-written fixture already covers", len(src))
	}

	var doc map[string]interface{}
	if err := json.Unmarshal(src, &doc); err != nil {
		t.Fatalf("fixture does not parse as JSON: %v", err)
	}

	// Real, non-hook complexity: Gemini CLI's own security/ide preferences,
	// nested two levels deep, which EnsureHooksInstalled/UninstallHooks must
	// leave untouched.
	sec, ok := doc["security"].(map[string]interface{})
	if !ok {
		t.Fatal("fixture is missing top-level \"security\" key")
	}
	auth, ok := sec["auth"].(map[string]interface{})
	if !ok || auth["selectedType"] == "" || auth["selectedType"] == nil {
		t.Error("fixture's security.auth.selectedType is missing — the fixture no longer " +
			"carries the nested real preference this test exercises the merge against")
	}
	if _, ok := doc["ide"].(map[string]interface{}); !ok {
		t.Error("fixture is missing top-level \"ide\" key")
	}

	// hooks already present, since this is the maintainer's real, already-
	// granted config — the round-trip tests below re-derive the "before our
	// install" state via UninstallHooks itself rather than a synthetic one.
	if _, ok := doc["hooks"].(map[string]interface{}); !ok {
		t.Fatal("fixture has no top-level \"hooks\" object")
	}
}

// seedRealConfig copies the committed fixture into a temp HOME's
// ~/.gemini/settings.json and returns the settings path.
func seedRealConfig(t *testing.T) string {
	t.Helper()
	src, err := os.ReadFile(realConfigFixture)
	if err != nil {
		t.Fatalf("reading the committed fixture: %v", err)
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	path, err := geminiSettingsPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, src, 0o600); err != nil {
		t.Fatalf("seeding settings.json: %v", err)
	}
	return path
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

// TestUninstallHooks_FromAConfigGeminiCLIItselfWrote uninstalls against the
// real fixture and asserts our entries are gone while security/ide survive
// untouched.
func TestUninstallHooks_FromAConfigGeminiCLIItselfWrote(t *testing.T) {
	path := seedRealConfig(t)
	before := readJSONDoc(t, path)

	modified, err := UninstallHooks()
	if err != nil {
		t.Fatalf("UninstallHooks into a config Gemini CLI itself wrote: %v", err)
	}
	if !modified {
		t.Fatal("UninstallHooks reported no modification against a config with real installed hooks")
	}

	after := readJSONDoc(t, path)
	if hooksMap, ok := after["hooks"].(map[string]interface{}); ok && len(hooksMap) != 0 {
		t.Errorf("hooks entries survive uninstall: %v", hooksMap)
	}
	assertNonHookKeysUnchanged(t, before, after)
}

// TestEnsureHooksInstalled_ReinstallsIntoAConfigGeminiCLIItselfWrote runs the
// full round trip against the real fixture: uninstall, then reinstall, and
// confirm the result is intact and every non-hook key is still exactly what
// Gemini CLI itself wrote.
func TestEnsureHooksInstalled_ReinstallsIntoAConfigGeminiCLIItselfWrote(t *testing.T) {
	path := seedRealConfig(t)
	before := readJSONDoc(t, path)

	if _, err := UninstallHooks(); err != nil {
		t.Fatalf("UninstallHooks: %v", err)
	}
	modified, err := EnsureHooksInstalled()
	if err != nil {
		t.Fatalf("EnsureHooksInstalled into a config Gemini CLI itself wrote: %v", err)
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

	after := readJSONDoc(t, path)
	assertNonHookKeysUnchanged(t, before, after)
}

func readJSONDoc(t *testing.T, path string) map[string]interface{} {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var m map[string]interface{}
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("unmarshal %s: %v", path, err)
	}
	return m
}
