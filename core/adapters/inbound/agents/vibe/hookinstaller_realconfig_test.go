// hookinstaller_realconfig_test.go grades this adapter's hooks install
// against a config.toml MISTRAL VIBE ITSELF WROTE, committed verbatim under
// testdata/ (one redaction, see below), rather than against a document one of
// these tests constructed.
//
// That distinction is the whole ticket (#1753). Every other test in this
// package builds its own config.toml, and every one of them was green while
// the shipped install (#1718/#1733) could not write into a realistic user
// config at all: vibe's own writer emits multi-line arrays, hooktoml's
// splicer refused any document containing one, so Apply returned ErrUnsafe,
// enable_experimental_hooks was never set, vibe never read hooks.toml, and no
// hook ever fired — while the permission continued to read `granted`. A
// hand-minimised fixture is exactly what hid it, so the fixture here is not
// minimised and TestRealVibeConfigFixture_StillCarriesTheConstructThatBrokeIt
// fails loudly if someone ever minimises it.
package vibe

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"irrlicht/core/adapters/inbound/agents/hooktoml"
)

// realConfigFixture is the committed copy of the maintainer's own
// $VIBE_HOME/config.toml as written by mistral-vibe 2.19.1. It is byte-for-
// byte upstream output except for ONE redaction — [experiments].client_key,
// a telemetry SDK key, replaced with a placeholder. The whole file was
// scanned for secret-shaped values before committing
// (api_key_env_var names an env var, not a key; [[providers]] carries no
// credential).
const realConfigFixture = "testdata/real-config-2.19.1.toml"

// multiLineArrayOpen matches an assignment whose value opens a multi-line
// array: `key = [` with nothing but whitespace (or a comment) after the
// bracket. It is deliberately the SAME shape #1753's own measurement used, so
// the count this file asserts and the count the issue quotes are produced by
// the same rule rather than by two people's eyeballs.
var multiLineArrayOpen = regexp.MustCompile(`(?m)^[ \t]*[A-Za-z0-9_.-]+[ \t]*=[ \t]*\[[ \t]*(#.*)?$`)

// countMultiLineArrayOpens reports how many multi-line arrays src opens in
// total, and how many of those are at the TOP level — before the first
// [table] or [[array]] header. The split matters: hooktoml has two
// independent scanners, and the top-level one (topLevelKeyLine, the path
// EnsureBoolTrue takes) never even reaches a header, so a fixture whose only
// multi-line arrays sat inside tables would exercise one of the two refusals
// and read as covering both.
//
// Extracted as a function, rather than inlined into the guard below, so
// countMultiLineArrayOpensCorpus can grade it against documents whose answers
// are known by construction — the mutation evidence for a guard that
// otherwise passes the moment it is written.
func countMultiLineArrayOpens(src []byte) (total, topLevel int) {
	firstHeader := len(src)
	for _, line := range bytes.SplitAfter(src, []byte("\n")) {
		trimmed := bytes.TrimSpace(line)
		if len(trimmed) > 0 && trimmed[0] == '[' {
			firstHeader = bytes.Index(src, line)
			break
		}
	}
	for _, loc := range multiLineArrayOpen.FindAllIndex(src, -1) {
		total++
		if loc[0] < firstHeader {
			topLevel++
		}
	}
	return total, topLevel
}

// TestRealVibeConfigFixture_StillCarriesTheConstructThatBrokeIt is the
// vacuity guard for every test in this file: a fixture quietly collapsed (the
// exact workaround PR #1752 applied to its scratch SEED, and documented there
// as a workaround) would leave the tests below green while covering nothing.
//
// The numbers are the ones measured on the real config when #1753 was filed —
// 12 multi-line arrays, at least one of them top-level — and the assertion is
// ">=", not "==": a later re-capture of the maintainer's config may carry
// more, and only FEWER means the fixture stopped carrying the defect's input.
func TestRealVibeConfigFixture_StillCarriesTheConstructThatBrokeIt(t *testing.T) {
	src, err := os.ReadFile(realConfigFixture)
	if err != nil {
		t.Fatalf("reading the committed fixture: %v", err)
	}
	total, topLevel := countMultiLineArrayOpens(src)
	if total < 12 {
		t.Errorf("fixture opens %d multi-line arrays, want >= 12 — it has been minimised, "+
			"which is exactly what hid #1753", total)
	}
	if topLevel < 1 {
		t.Errorf("fixture opens %d TOP-LEVEL multi-line arrays (before the first header), want >= 1 — "+
			"without one, EnsureBoolTrue's own scanner is never reached", topLevel)
	}
	if bytes.Contains(src, []byte("sdk-OE8")) {
		t.Error("the fixture still carries the unredacted telemetry client_key")
	}
}

// countMultiLineArrayOpensCorpus is the committed mutation evidence for the
// guard above: one document per shape, pinned to the answer the counter must
// return. Without it a counter that returned a large constant, or that never
// found a header and called everything top-level, would satisfy the guard and
// read as excellent coverage.
func TestCountMultiLineArrayOpens_Corpus(t *testing.T) {
	cases := []struct {
		name            string
		doc             string
		total, topLevel int
	}{
		{"empty", "", 0, 0},
		{"single-line array only", "a = []\nb = [1, 2]\n", 0, 0},
		{"one top-level, no headers at all", "a = [\n  1,\n]\n", 1, 1},
		{"one top-level before a header", "a = [\n  1,\n]\n\n[t]\nb = 2\n", 1, 1},
		{"one inside a table only", "[t]\na = [\n  1,\n]\n", 1, 0},
		{"one each side of the first header", "a = [\n  1,\n]\n[t]\nb = [\n  2,\n]\n", 2, 1},
		{"opener carrying a trailing comment", "a = [  # why\n  1,\n]\n", 1, 1},
		// The collapsed shape PR #1752's scratch seed used: the same content
		// on one line. A guard blind to this is a guard that would not notice
		// the fixture being minimised.
		{"collapsed to one line", "a = [ 1 ]\n[t]\nb = [ 2 ]\n", 0, 0},
	}
	for _, tc := range cases {
		total, topLevel := countMultiLineArrayOpens([]byte(tc.doc))
		if total != tc.total || topLevel != tc.topLevel {
			t.Errorf("%s: got total=%d topLevel=%d, want total=%d topLevel=%d",
				tc.name, total, topLevel, tc.total, tc.topLevel)
		}
	}
}

// seedRealConfig points VIBE_HOME at a fresh temp dir holding a copy of the
// committed real config, and returns that dir. HOME is redirected too so
// nothing can fall back to the developer's own ~/.vibe.
func seedRealConfig(t *testing.T) (home, configPath string) {
	t.Helper()
	src, err := os.ReadFile(realConfigFixture)
	if err != nil {
		t.Fatalf("reading the committed fixture: %v", err)
	}
	home = t.TempDir()
	t.Setenv("HOME", t.TempDir())
	t.Setenv(vibeHomeEnvVar, home)
	configPath = filepath.Join(home, configFilename)
	if err := os.WriteFile(configPath, src, 0o600); err != nil {
		t.Fatalf("seeding config.toml: %v", err)
	}
	return home, configPath
}

// TestEnsureHooksInstalled_IntoAConfigVibeItselfWrote is #1753's defect test.
// Before the fix it fails on the very first assertion, with hooktoml's
// ErrUnsafe: the install cannot write the gate at all.
func TestEnsureHooksInstalled_IntoAConfigVibeItselfWrote(t *testing.T) {
	home, configPath := seedRealConfig(t)
	before, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("reading the seeded config: %v", err)
	}

	modified, err := EnsureHooksInstalled()
	if err != nil {
		t.Fatalf("EnsureHooksInstalled into a config vibe itself wrote: %v", err)
	}
	if !modified {
		t.Fatal("EnsureHooksInstalled reported no modification")
	}

	// The gate is on — the fact whose absence means no hook ever fires.
	on, found, err := hooktoml.TopLevelBool(configPath, experimentalHooksFlag)
	if err != nil {
		t.Fatalf("TopLevelBool after install: %v", err)
	}
	if !found || !on {
		t.Fatalf("%s: found=%v value=%v, want true/true", experimentalHooksFlag, found, on)
	}

	// The entry is live, by the adapter's own definition of intact.
	status, err := VerifyHooksInstalled()
	if err != nil {
		t.Fatalf("VerifyHooksInstalled: %v", err)
	}
	if !status.Intact() {
		t.Fatalf("VerifyHooksInstalled reports %+v, want intact", status)
	}
	if _, err := os.Stat(filepath.Join(home, hooksFilename)); err != nil {
		t.Fatalf("hooks.toml: %v", err)
	}

	// Every byte of the user's own config survives except the one line the
	// install adds. This is the property a splicer exists for, and the reason
	// hooktoml refuses rather than re-encoding: a read-modify-marshal through
	// a generic TOML encoder would reformat all twelve arrays and drop every
	// comment.
	after, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("reading the rewritten config: %v", err)
	}
	assertOnlyAddedTheFlagLine(t, before, after)
}

// assertOnlyAddedTheFlagLine checks that after differs from before by exactly
// one added line, and that the added line is the flag assignment.
func assertOnlyAddedTheFlagLine(t *testing.T, before, after []byte) {
	t.Helper()
	wantLine := experimentalHooksFlag + " = true"

	beforeLines := strings.Split(string(before), "\n")
	afterLines := strings.Split(string(after), "\n")
	if len(afterLines) != len(beforeLines)+1 {
		t.Fatalf("config gained %d lines, want exactly 1", len(afterLines)-len(beforeLines))
	}

	var added []string
	i, j := 0, 0
	for i < len(beforeLines) && j < len(afterLines) {
		if beforeLines[i] == afterLines[j] {
			i, j = i+1, j+1
			continue
		}
		added = append(added, afterLines[j])
		j++
	}
	added = append(added, afterLines[j:]...)
	if len(added) != 1 || strings.TrimSpace(added[0]) != wantLine {
		t.Fatalf("config changed by %d line(s) %q, want exactly one added line %q", len(added), added, wantLine)
	}
}

// TestUninstallHooks_FromAConfigVibeItselfWrote is the other half of the round
// trip: the flag added to a real config is cleared again, and the user's own
// twelve arrays are still byte-identical to what vibe wrote.
func TestUninstallHooks_FromAConfigVibeItselfWrote(t *testing.T) {
	_, configPath := seedRealConfig(t)
	before, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("reading the seeded config: %v", err)
	}

	if _, err := EnsureHooksInstalled(); err != nil {
		t.Fatalf("EnsureHooksInstalled: %v", err)
	}
	if _, err := UninstallHooks(); err != nil {
		t.Fatalf("UninstallHooks: %v", err)
	}

	on, _, err := hooktoml.TopLevelBool(configPath, experimentalHooksFlag)
	if err != nil {
		t.Fatalf("TopLevelBool after uninstall: %v", err)
	}
	if on {
		t.Errorf("%s is still true after UninstallHooks", experimentalHooksFlag)
	}

	after, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("reading the config after uninstall: %v", err)
	}
	// The flag line survives as `= false` — ClearBoolIfPresent rewrites the
	// line it added rather than deleting it, which is the documented
	// behaviour. Everything else must be untouched.
	stripped := strings.Replace(string(after), experimentalHooksFlag+" = false\n", "", 1)
	if stripped != string(before) {
		t.Errorf("uninstall did not restore the user's own bytes:\n--- want ---\n%s\n--- got ---\n%s", before, stripped)
	}
}
