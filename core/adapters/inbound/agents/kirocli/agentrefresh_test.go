// agentrefresh_test.go covers issue #1736: the copied `irrlicht` agent config
// is rebuilt from kiro-cli's built-in default when kiro-cli is upgraded, and is
// left strictly alone when the user has edited it.
//
// Everything this change adds passes the moment it is written, so every guard
// here carries a deliberate mutation. Two forms, and the split is deliberate:
//
//   - refreshMutation below is the COMMITTED corpus, per
//     docs/testing-philosophy.md ("prefer committing that mutation to
//     describing it"). Each row is one wrong implementation of decideRefresh —
//     the whole trigger, as a pure function — pinned to the input it must get
//     wrong and to the verdict the real one returns. It re-runs forever, which
//     a paragraph in a merged PR body does not.
//   - The behavioural tests below it were each additionally seen red against a
//     mutation of the PRODUCTION file, one at a time; the verbatim output is in
//     the PR body. Those cover what a pure function cannot reach: the ORDER in
//     which EnsureHooksInstalled records the baseline, the restore path, and the
//     fingerprint's subtraction of the hooks key.
package kirocli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"irrlicht/core/pkg/atomicfile"
	"irrlicht/core/pkg/hookbeacon"
)

// --- a kiro-cli stand-in whose version and built-in default are steerable ---

// fakeKiro is a stand-in for the real binary whose answers a test can change
// between calls, which is what a version BUMP needs: hooks_test.go's
// fakeKiroCLIScript answers 2.6.0 and one fixed document forever.
//
// Steered through files rather than by rewriting the script, so a change takes
// effect for the next invocation without touching the kiroCLIPath seam again.
type fakeKiro struct {
	dir string
}

// installSteerableFakeKiroCLI substitutes the steerable stand-in for the real
// binary, through the same kiroCLIPath seam installFakeKiroCLI uses (see
// kiroCLIPath's own doc comment for why PATH-prepending does not reach it).
func installSteerableFakeKiroCLI(t *testing.T) *fakeKiro {
	t.Helper()
	dir := t.TempDir()
	f := &fakeKiro{dir: dir}
	f.setVersion("2.6.0")
	f.setCreateFails(false)

	script := fmt.Sprintf(`#!/bin/sh
set -e
STATE=%q
if [ "$1" = "--version" ]; then
  v=$(cat "$STATE/version")
  if [ "$v" = "__FAIL__" ]; then
    echo "kiro-cli: could not determine version" >&2
    exit 1
  fi
  if [ "$v" = "__GARBAGE__" ]; then
    echo "kiro-cli (unreleased build)"
    exit 0
  fi
  echo "kiro-cli $v"
  exit 0
fi
if [ "$1" = "agent" ] && [ "$2" = "create" ]; then
  if [ "$(cat "$STATE/create_fails")" = "yes" ]; then
    echo "error: Editor process did not exit with success" >&2
    exit 1
  fi
  name="$3"
  target="$KIRO_HOME/agents/$name.json"
  if [ -e "$target" ]; then
    echo "error: Agent with name $name already exists. Aborting" >&2
    exit 1
  fi
  mkdir -p "$KIRO_HOME/agents"
  v=$(cat "$STATE/version")
  cat > "$target" <<JSON
{"name":"$name","description":"Default agent","prompt":"built-in as of $v","tools":["*"],"resources":["file://AGENTS.md"],"hooks":{},"model":null}
JSON
  exit 0
fi
if [ "$1" = "agent" ] && [ "$2" = "set-default" ]; then
  name="$3"
  mkdir -p "$KIRO_HOME/settings"
  f="$KIRO_HOME/settings/cli.json"
  if [ -f "$f" ] && grep -q '"chat.defaultAgent"' "$f"; then
    sed -E 's/"chat\.defaultAgent"[[:space:]]*:[[:space:]]*"[^"]*"/"chat.defaultAgent":"'"$name"'"/' "$f" > "$f.tmp"
    mv "$f.tmp" "$f"
  elif [ -f "$f" ]; then
    sed -E 's/^\{/{"chat.defaultAgent":"'"$name"'",/' "$f" > "$f.tmp"
    mv "$f.tmp" "$f"
  else
    printf '{"chat.defaultAgent":"%%s"}' "$name" > "$f"
  fi
  exit 0
fi
echo "fake-kiro-cli: unhandled args: $*" >&2
exit 1
`, dir)

	path := filepath.Join(dir, "kiro-cli")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write steerable fake kiro-cli: %v", err)
	}
	original := kiroCLIPath
	kiroCLIPath = func() string { return path }
	t.Cleanup(func() { kiroCLIPath = original })
	return f
}

func (f *fakeKiro) setVersion(v string) {
	if err := os.WriteFile(filepath.Join(f.dir, "version"), []byte(v), 0o600); err != nil {
		panic(err)
	}
}

func (f *fakeKiro) setCreateFails(fails bool) {
	value := "no"
	if fails {
		value = "yes"
	}
	if err := os.WriteFile(filepath.Join(f.dir, "create_fails"), []byte(value), 0o600); err != nil {
		panic(err)
	}
}

// steerableKiroHome is kiroInstallerHome's counterpart for the tests that need
// to change kiro-cli's answers mid-test.
func steerableKiroHome(t *testing.T) (string, *fakeKiro) {
	t.Helper()
	home := t.TempDir()
	t.Setenv(kiroHomeEnvVar, home)
	return home, installSteerableFakeKiroCLI(t)
}

func readAgentConfig(t *testing.T) map[string]interface{} {
	t.Helper()
	path, err := irrlichtAgentConfigPath()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read agent config: %v", err)
	}
	var doc map[string]interface{}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("agent config is not valid JSON: %v", err)
	}
	return doc
}

func mustAgentConfigPath(t *testing.T) string {
	t.Helper()
	path, err := irrlichtAgentConfigPath()
	if err != nil {
		t.Fatal(err)
	}
	return path
}

// --- the committed mutation corpus for decideRefresh ---

// refreshMutation is one deliberately wrong decideRefresh, the input it must
// get wrong, and what the real one answers there.
//
// Each row is a mutation someone could plausibly write, not a random
// perturbation: dropping a clause, reordering two, or reading the wrong field.
// A row whose Broken agrees with the real function on its own input would prove
// nothing, so the harness asserts they DISAGREE.
type refreshMutation struct {
	Name   string
	Why    string
	Broken func(current string, rec agentSnapshotState, fingerprint string) refreshVerdict

	Current     string
	Recorded    agentSnapshotState
	Fingerprint string
	Want        refreshVerdict
}

const (
	ourFingerprint  = "aaaa-what-we-wrote"
	editFingerprint = "bbbb-someone-else-wrote-this"
)

func refreshMutations() []refreshMutation {
	return []refreshMutation{
		{
			Name: "no_user_edit_guard",
			Why: "the whole skip clause is gone, so an upgrade regenerates over a config " +
				"the user has edited — #1736's first open question, and the one failure " +
				"here that destroys work rather than merely omitting some",
			Broken: func(current string, rec agentSnapshotState, _ string) refreshVerdict {
				switch {
				case current == "":
					return refreshUnknownVersion
				case rec.CLIVersion == "":
					return refreshAdopted
				case rec.CLIVersion == current:
					return refreshUpToDate
				}
				return refreshDue
			},
			Current:     "2.7.0",
			Recorded:    agentSnapshotState{CLIVersion: "2.6.0", ConfigFingerprint: ourFingerprint},
			Fingerprint: editFingerprint,
			Want:        refreshUserEdited,
		},
		{
			Name: "empty_recorded_fingerprint_is_a_match",
			Why: "`rec.ConfigFingerprint == \"\"` dropped from the guard, so a baseline " +
				"that never recorded a fingerprint compares equal to nothing and " +
				"regenerates anyway — the shape a half-written sidecar produces",
			Broken: func(current string, rec agentSnapshotState, fingerprint string) refreshVerdict {
				switch {
				case current == "":
					return refreshUnknownVersion
				case rec.CLIVersion == "":
					return refreshAdopted
				case rec.CLIVersion == current:
					return refreshUpToDate
				case rec.ConfigFingerprint != fingerprint:
					return refreshUserEdited
				}
				return refreshDue
			},
			Current:     "2.7.0",
			Recorded:    agentSnapshotState{CLIVersion: "2.6.0", ConfigFingerprint: ""},
			Fingerprint: "",
			Want:        refreshUserEdited,
		},
		{
			Name: "unknown_version_falls_open",
			Why: "an unreadable version treated as 'assume it changed'. Fail-open is the " +
				"right direction for #1365's version FLOOR and the wrong one here: the " +
				"reading and the regeneration are the same binary, so a refresh that " +
				"cannot read the version is one that cannot be performed",
			Broken: func(current string, rec agentSnapshotState, fingerprint string) refreshVerdict {
				switch {
				case rec.CLIVersion == "":
					return refreshAdopted
				case rec.CLIVersion == current:
					return refreshUpToDate
				case rec.ConfigFingerprint == "" || rec.ConfigFingerprint != fingerprint:
					return refreshUserEdited
				}
				return refreshDue
			},
			Current:     "",
			Recorded:    agentSnapshotState{CLIVersion: "2.6.0", ConfigFingerprint: ourFingerprint},
			Fingerprint: ourFingerprint,
			Want:        refreshUnknownVersion,
		},
		{
			Name: "unknown_version_reads_as_up_to_date",
			Why: "the unknown clause moved BELOW the equality one, so an unreadable " +
				"version against an empty baseline answers up_to_date — inability to " +
				"look producing the same output as absence of a finding, which is the " +
				"failure docs/testing-philosophy.md names as the most expensive one",
			Broken: func(current string, rec agentSnapshotState, fingerprint string) refreshVerdict {
				switch {
				case rec.CLIVersion == current:
					return refreshUpToDate
				case current == "":
					return refreshUnknownVersion
				case rec.CLIVersion == "":
					return refreshAdopted
				case rec.ConfigFingerprint == "" || rec.ConfigFingerprint != fingerprint:
					return refreshUserEdited
				}
				return refreshDue
			},
			Current:     "",
			Recorded:    agentSnapshotState{},
			Fingerprint: ourFingerprint,
			Want:        refreshUnknownVersion,
		},
		{
			Name: "no_baseline_regenerates",
			Why: "an install with nothing on record regenerates instead of adopting. " +
				"Every install that predates #1736 is in that state, and its config may " +
				"carry edits made before any baseline existed",
			Broken: func(current string, rec agentSnapshotState, fingerprint string) refreshVerdict {
				switch {
				case current == "":
					return refreshUnknownVersion
				case rec.CLIVersion == current:
					return refreshUpToDate
				case rec.ConfigFingerprint != "" && rec.ConfigFingerprint != fingerprint:
					return refreshUserEdited
				}
				return refreshDue
			},
			Current:     "2.7.0",
			Recorded:    agentSnapshotState{},
			Fingerprint: ourFingerprint,
			Want:        refreshAdopted,
		},
		{
			Name: "never_due",
			Why: "the trigger never fires — the state #1736 was filed about, reproduced " +
				"as a mutation so the positive case is not the only thing holding it",
			Broken: func(current string, rec agentSnapshotState, _ string) refreshVerdict {
				if current == "" {
					return refreshUnknownVersion
				}
				if rec.CLIVersion == "" {
					return refreshAdopted
				}
				return refreshUpToDate
			},
			Current:     "2.7.0",
			Recorded:    agentSnapshotState{CLIVersion: "2.6.0", ConfigFingerprint: ourFingerprint},
			Fingerprint: ourFingerprint,
			Want:        refreshDue,
		},
	}
}

// TestDecideRefresh_CatchesEveryCommittedMutation drives each committed
// mutation against the input it must get wrong.
//
// Two assertions per row, and the second is the one that makes the corpus
// evidence rather than decoration: the real function must return Want, AND the
// broken one must not. A row where they agree is a mutation this corpus does
// not actually discriminate.
func TestDecideRefresh_CatchesEveryCommittedMutation(t *testing.T) {
	mutations := refreshMutations()
	if len(mutations) == 0 {
		t.Fatal("no committed mutations — this corpus would grade nothing")
	}
	for _, m := range mutations {
		t.Run(m.Name, func(t *testing.T) {
			got := decideRefresh(m.Current, m.Recorded, m.Fingerprint)
			if got != m.Want {
				t.Fatalf("decideRefresh(%q, %+v, %q) = %q, want %q",
					m.Current, m.Recorded, m.Fingerprint, got, m.Want)
			}
			broken := m.Broken(m.Current, m.Recorded, m.Fingerprint)
			if broken == m.Want {
				t.Errorf("the %q mutation returns %q too, so this row discriminates nothing. "+
					"Either the mutation no longer breaks what it claims (%s), or the input "+
					"no longer reaches it", m.Name, broken, m.Why)
			}
		})
	}
}

// TestDecideRefresh_EveryVerdictIsReachable is the vacuity guard for the corpus
// above: a decideRefresh that returned one constant would satisfy several rows
// at once and read as coverage.
func TestDecideRefresh_EveryVerdictIsReachable(t *testing.T) {
	cases := map[refreshVerdict]struct {
		current     string
		rec         agentSnapshotState
		fingerprint string
	}{
		refreshUnknownVersion: {"", agentSnapshotState{CLIVersion: "2.6.0"}, ourFingerprint},
		refreshAdopted:        {"2.6.0", agentSnapshotState{}, ourFingerprint},
		refreshUpToDate:       {"2.6.0", agentSnapshotState{CLIVersion: "2.6.0", ConfigFingerprint: ourFingerprint}, ourFingerprint},
		refreshUserEdited:     {"2.7.0", agentSnapshotState{CLIVersion: "2.6.0", ConfigFingerprint: ourFingerprint}, editFingerprint},
		refreshDue:            {"2.7.0", agentSnapshotState{CLIVersion: "2.6.0", ConfigFingerprint: ourFingerprint}, ourFingerprint},
	}
	for want, in := range cases {
		if got := decideRefresh(in.current, in.rec, in.fingerprint); got != want {
			t.Errorf("decideRefresh(%q, %+v, %q) = %q, want %q", in.current, in.rec, in.fingerprint, got, want)
		}
	}
}

// TestRefreshVerdicts_UnknownVersionIsNotUpToDate is the "a mechanism that
// cannot run must fail loudly" obligation, in the only form this package can
// carry: the two states leave DIFFERENT bytes in the sidecar, so a reader can
// tell "checked, nothing to do" from "could not check".
//
// Asserted on the recorded document rather than on the constants, because the
// constants agreeing proves nothing about what is written down.
func TestRefreshVerdicts_UnknownVersionIsNotUpToDate(t *testing.T) {
	if refreshUnknownVersion == refreshUpToDate {
		t.Fatal("the two verdicts are the same word")
	}

	_, fake := steerableKiroHome(t)
	if _, err := EnsureHooksInstalled(); err != nil {
		t.Fatalf("install: %v", err)
	}
	if _, err := EnsureHooksInstalled(); err != nil {
		t.Fatalf("second install: %v", err)
	}
	healthy := readAgentSnapshotState()
	if healthy.LastVerdict != string(refreshUpToDate) {
		t.Fatalf("last verdict after an ordinary pass = %q, want %q", healthy.LastVerdict, refreshUpToDate)
	}

	fake.setVersion("__FAIL__")
	if _, err := EnsureHooksInstalled(); err != nil {
		t.Fatalf("install with an unreadable version: %v", err)
	}
	blind := readAgentSnapshotState()
	if blind.LastVerdict == healthy.LastVerdict {
		t.Errorf("an unreadable kiro-cli version records the same verdict %q as a healthy "+
			"pass — inability to look and absence of a finding must not produce the same "+
			"output", blind.LastVerdict)
	}
	if blind.LastVerdict != string(refreshUnknownVersion) {
		t.Errorf("last verdict = %q, want %q", blind.LastVerdict, refreshUnknownVersion)
	}
	if blind.CLIVersion != healthy.CLIVersion {
		t.Errorf("a failed version read overwrote the recorded baseline (%q -> %q); that "+
			"silently disarms the trigger", healthy.CLIVersion, blind.CLIVersion)
	}
}

// TestRefreshVerdicts_GarbageVersionIsAlsoUnknown pins that "printed something
// we cannot parse" lands in the same fail-closed bucket as "did not run" — the
// direction core/pkg/cliversion takes for the floor too, arrived at here for a
// different reason: a version we cannot parse cannot be compared, so no upgrade
// can be established from it.
func TestRefreshVerdicts_GarbageVersionIsAlsoUnknown(t *testing.T) {
	_, fake := steerableKiroHome(t)
	if _, err := EnsureHooksInstalled(); err != nil {
		t.Fatalf("install: %v", err)
	}
	fake.setVersion("__GARBAGE__")
	if _, err := EnsureHooksInstalled(); err != nil {
		t.Fatalf("install with an unparseable version: %v", err)
	}
	if got := readAgentSnapshotState().LastVerdict; got != string(refreshUnknownVersion) {
		t.Errorf("last verdict = %q, want %q", got, refreshUnknownVersion)
	}
}

// --- the behaviour ---

// TestRefresh_FiresOnAVersionBump is #1736 itself: after a kiro-cli upgrade the
// copied agent carries the NEW built-in default's content, and still carries our
// hook entries.
func TestRefresh_FiresOnAVersionBump(t *testing.T) {
	_, fake := steerableKiroHome(t)
	if _, err := EnsureHooksInstalled(); err != nil {
		t.Fatalf("install: %v", err)
	}
	if got := readAgentConfig(t)["prompt"]; got != "built-in as of 2.6.0" {
		t.Fatalf("prompt after install = %v, want the 2.6.0 built-in", got)
	}

	fake.setVersion("2.7.0")
	modified, err := EnsureHooksInstalled()
	if err != nil {
		t.Fatalf("install after upgrade: %v", err)
	}
	if !modified {
		t.Error("a refresh that rebuilt the agent config reported modified=false")
	}

	doc := readAgentConfig(t)
	if got := doc["prompt"]; got != "built-in as of 2.7.0" {
		t.Errorf("prompt after the upgrade = %v, want the 2.7.0 built-in — the copy is "+
			"still the grant-time snapshot (#1736)", got)
	}
	status, err := VerifyHooksInstalled()
	if err != nil || !status.Intact() {
		t.Errorf("hook entries after a refresh: intact=%v err=%v damage=%s",
			status.Intact(), err, status.Damage())
	}
	if got := readAgentSnapshotState().CLIVersion; got != "2.7.0" {
		t.Errorf("recorded baseline version = %q after the refresh, want 2.7.0 — an "+
			"unrecorded refresh regenerates on every daemon start forever", got)
	}
}

// TestRefresh_SkipsAUserEditedConfig is #1736's other half: an edit to the copy
// survives an upgrade.
func TestRefresh_SkipsAUserEditedConfig(t *testing.T) {
	_, fake := steerableKiroHome(t)
	if _, err := EnsureHooksInstalled(); err != nil {
		t.Fatalf("install: %v", err)
	}

	doc := readAgentConfig(t)
	doc["prompt"] = "my own prompt, hands off"
	edited, _ := json.MarshalIndent(doc, "", "  ")
	if err := os.WriteFile(mustAgentConfigPath(t), edited, 0o600); err != nil {
		t.Fatal(err)
	}

	fake.setVersion("2.7.0")
	if _, err := EnsureHooksInstalled(); err != nil {
		t.Fatalf("install after upgrade: %v", err)
	}

	if got := readAgentConfig(t)["prompt"]; got != "my own prompt, hands off" {
		t.Errorf("prompt after the upgrade = %v; the refresh regenerated over a config the "+
			"user had edited and destroyed their work", got)
	}
	if got := readAgentSnapshotState().LastVerdict; got != string(refreshUserEdited) {
		t.Errorf("last verdict = %q, want %q — a skip nobody can see is indistinguishable "+
			"from a refresh that never triggered", got, refreshUserEdited)
	}

	// A SECOND upgrade must skip too. Without this arm a skip that quietly
	// re-baselined over the user's bytes would pass everything above and then
	// destroy the edit at the next upgrade — one release later, with nothing
	// connecting the two events.
	fake.setVersion("2.8.0")
	if _, err := EnsureHooksInstalled(); err != nil {
		t.Fatalf("install after a second upgrade: %v", err)
	}
	if got := readAgentConfig(t)["prompt"]; got != "my own prompt, hands off" {
		t.Errorf("prompt after a SECOND upgrade = %v; the first skip adopted the user's own "+
			"bytes as irrlicht's baseline, so the next upgrade overwrote them", got)
	}
}

// TestUninstall_RemovesTheRefreshBaseline pins that the sidecar does not outlive
// the install it describes. It is the #1739 residue shape:
// recordPriorDefaultAgentOnce's sidecar had to be declared and cleaned up for
// exactly this reason, and a baseline left behind claims a config irrlicht no
// longer wrote as one it did.
func TestUninstall_RemovesTheRefreshBaseline(t *testing.T) {
	steerableKiroHome(t)
	if _, err := EnsureHooksInstalled(); err != nil {
		t.Fatalf("install: %v", err)
	}
	path, err := agentSnapshotStatePath()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("install did not record a baseline at %s: %v", path, err)
	}

	if _, err := UninstallHooks(); err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("%s survives uninstall (stat err = %v) — a stale baseline is what a LATER "+
			"grant reads instead of the state it should adopt", path, err)
	}
}

// TestRefresh_DriftedOwnEntryIsNotAUserEdit is the load-bearing half of #1736's
// "does not also fire on our own hook entries" requirement, driven end-to-end
// rather than asserted about the fingerprint alone.
//
// It reproduces the one shape in which OUR OWN entries genuinely differ from
// what the baseline was taken over: #1373's beacon drift, an entry of ours
// naming a binary path that is no longer the running one, co-occurring with a
// kiro-cli upgrade. The decision is made before ensureFlatHooksInstalled
// repairs it, so at that instant the hooks key on disk is not the one we last
// wrote. A whole-file baseline would read that as a user edit and skip — and
// then keep skipping, because the drift repeats for every user whose daemon
// binary ever moved.
//
// Also asserts the entries were repaired in the same pass, so a refresh cannot
// buy its green by leaving the drift in place.
func TestRefresh_DriftedOwnEntryIsNotAUserEdit(t *testing.T) {
	_, fake := steerableKiroHome(t)
	if _, err := EnsureHooksInstalled(); err != nil {
		t.Fatalf("install: %v", err)
	}

	configPath := mustAgentConfigPath(t)
	staleCommand, err := hookbeacon.Command("/nonexistent-irrlicht-1736/bin/irrlichd", AdapterName)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ensureFlatHooksInstalled(flatHookConfig{
		Path:        configPath,
		Sentinel:    hookbeacon.Sentinel(AdapterName),
		Events:      installedHookEvents,
		Entry:       func() map[string]interface{} { return flatBeaconEntry(staleCommand) },
		IsCanonical: func(map[string]interface{}) bool { return false },
		WriteFile:   atomicfile.WriteFile,
	}); err != nil {
		t.Fatalf("seed a drifted entry: %v", err)
	}

	fake.setVersion("2.7.0")
	if _, err := EnsureHooksInstalled(); err != nil {
		t.Fatalf("install after upgrade: %v", err)
	}

	if got := readAgentConfig(t)["prompt"]; got != "built-in as of 2.7.0" {
		t.Errorf("prompt after the upgrade = %v, want the 2.7.0 built-in. The refresh read "+
			"irrlicht's OWN drifted hook entry as a user edit and skipped (verdict %q) — "+
			"the trigger is then dead for every user whose daemon binary ever moved",
			got, readAgentSnapshotState().LastVerdict)
	}
	status, err := VerifyHooksInstalled()
	if err != nil || !status.Intact() {
		t.Errorf("entries after the refresh: intact=%v err=%v damage=%s — the drift that "+
			"made this test interesting was carried across instead of repaired",
			status.Intact(), err, status.Damage())
	}
}

// TestAgentConfigFingerprint_IgnoresTheHooksKey states the same property
// directly, at the level it is implemented: no change confined to "hooks" moves
// the fingerprint, and a change outside it always does.
//
// The second half is the vacuity guard — a fingerprint that ignored everything
// would satisfy the first half perfectly.
func TestAgentConfigFingerprint_IgnoresTheHooksKey(t *testing.T) {
	steerableKiroHome(t)
	if _, err := EnsureHooksInstalled(); err != nil {
		t.Fatalf("install: %v", err)
	}
	configPath := mustAgentConfigPath(t)

	base, err := agentConfigFingerprint(configPath)
	if err != nil {
		t.Fatal(err)
	}

	rewrite := func(mutate func(doc map[string]interface{})) {
		doc := readAgentConfig(t)
		mutate(doc)
		data, _ := json.MarshalIndent(doc, "", "  ")
		if err := os.WriteFile(configPath, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	for _, tc := range []struct {
		name   string
		mutate func(doc map[string]interface{})
	}{
		{"a foreign hook entry is added", func(doc map[string]interface{}) {
			doc["hooks"].(map[string]interface{})["agentSpawn"] =
				[]interface{}{map[string]interface{}{"command": "echo mine"}}
		}},
		{"every hook entry is deleted", func(doc map[string]interface{}) {
			doc["hooks"] = map[string]interface{}{}
		}},
		{"the hooks key is removed entirely", func(doc map[string]interface{}) {
			delete(doc, "hooks")
		}},
	} {
		rewrite(tc.mutate)
		got, err := agentConfigFingerprint(configPath)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if got != base {
			t.Errorf("%s moved the fingerprint (%s -> %s); a change confined to hooks must "+
				"not read as a user edit", tc.name, base[:8], got[:8])
		}
	}

	rewrite(func(doc map[string]interface{}) { doc["prompt"] = "edited by a human" })
	got, err := agentConfigFingerprint(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if got == base {
		t.Error("editing prompt did not move the fingerprint — it ignores the content it " +
			"exists to watch, so nothing would ever be detected as a user edit")
	}
}

// TestRefresh_PreservesForeignHookEntries pins the cost of the subtraction
// above: because a foreign hook entry does not suppress the refresh, the
// regeneration has to carry it across, or #1736 would silently delete hook
// entries irrlicht did not install — the rule uninstallFlatHooks already
// follows.
func TestRefresh_PreservesForeignHookEntries(t *testing.T) {
	_, fake := steerableKiroHome(t)
	if _, err := EnsureHooksInstalled(); err != nil {
		t.Fatalf("install: %v", err)
	}

	doc := readAgentConfig(t)
	doc["hooks"].(map[string]interface{})["agentSpawn"] =
		[]interface{}{map[string]interface{}{"command": "echo mine"}}
	data, _ := json.MarshalIndent(doc, "", "  ")
	if err := os.WriteFile(mustAgentConfigPath(t), data, 0o600); err != nil {
		t.Fatal(err)
	}

	fake.setVersion("2.7.0")
	if _, err := EnsureHooksInstalled(); err != nil {
		t.Fatalf("install after upgrade: %v", err)
	}

	after := readAgentConfig(t)
	if got := after["prompt"]; got != "built-in as of 2.7.0" {
		t.Fatalf("the refresh did not fire (prompt = %v); the rest of this test would be "+
			"asserting nothing", got)
	}
	raw, _ := json.Marshal(after)
	if !strings.Contains(string(raw), "echo mine") {
		t.Error("the regeneration dropped a hook entry irrlicht did not install")
	}
	status, err := VerifyHooksInstalled()
	if err != nil || !status.Intact() {
		t.Errorf("our own entries after the refresh: intact=%v err=%v damage=%s",
			status.Intact(), err, status.Damage())
	}
}

// TestRefresh_FailedRegenerationRestoresTheConfig covers the window the rebuild
// opens: `kiro-cli agent create` refuses when the target exists, so the old file
// has to be removed first. A failure there without a restore leaves
// chat.defaultAgent pointing at a file that no longer exists — strictly worse
// than the drift this feature is fixing.
func TestRefresh_FailedRegenerationRestoresTheConfig(t *testing.T) {
	_, fake := steerableKiroHome(t)
	if _, err := EnsureHooksInstalled(); err != nil {
		t.Fatalf("install: %v", err)
	}
	configPath := mustAgentConfigPath(t)
	before, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}

	fake.setVersion("2.7.0")
	fake.setCreateFails(true)
	_, err = EnsureHooksInstalled()
	if err == nil {
		t.Error("a failed regeneration was reported as success; #1362's effect_error is the " +
			"only place a user would ever see it")
	}

	after, readErr := os.ReadFile(configPath)
	if readErr != nil {
		t.Fatalf("the agent config is gone after a failed regeneration: %v", readErr)
	}
	if string(after) != string(before) {
		t.Errorf("the agent config was not restored byte-for-byte after a failed "+
			"regeneration:\nbefore: %s\nafter:  %s", before, after)
	}
	if got := readAgentSnapshotState().LastVerdict; got != string(refreshFailed) {
		t.Errorf("last verdict = %q, want %q", got, refreshFailed)
	}

	// And it recovers: with the CLI working again, the next pass refreshes.
	fake.setCreateFails(false)
	if _, err := EnsureHooksInstalled(); err != nil {
		t.Fatalf("install after the CLI recovered: %v", err)
	}
	if got := readAgentConfig(t)["prompt"]; got != "built-in as of 2.7.0" {
		t.Errorf("prompt after recovery = %v, want the 2.7.0 built-in — a failed refresh "+
			"latched the trigger off", got)
	}
}

// TestRefresh_UnreadableVersionDoesNotRegenerate is the fail-closed direction,
// end to end: with `kiro-cli --version` failing, the copy is left exactly as it
// was rather than rebuilt from a binary we have just failed to run.
func TestRefresh_UnreadableVersionDoesNotRegenerate(t *testing.T) {
	_, fake := steerableKiroHome(t)
	if _, err := EnsureHooksInstalled(); err != nil {
		t.Fatalf("install: %v", err)
	}
	configPath := mustAgentConfigPath(t)
	before, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}

	fake.setVersion("__FAIL__")
	modified, err := EnsureHooksInstalled()
	if err != nil {
		t.Fatalf("install with an unreadable version: %v", err)
	}
	if modified {
		t.Error("an install that could not read the version reported modified=true")
	}

	after, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("the agent config is gone: %v", err)
	}
	if string(after) != string(before) {
		t.Error("the agent config changed while the version could not be read — the refresh " +
			"failed OPEN, which is the direction that can leave a user with no agent at all")
	}
}

// TestRefreshTriggerAndRegenerationUseTheSameBinarySeam is what makes the
// fail-closed decision above defensible rather than merely stated: "we can read
// the version" and "we can regenerate" have to be the SAME capability, or a
// refresh could be triggered by a binary we are not going to run.
//
// Asserted by breaking the seam once and requiring BOTH to fail. A version read
// that went through core/pkg/cliprobe would survive this, because cliprobe
// resolves argv[0] through pathutil's trusted directories instead — measured to
// select a different kiro-cli on this machine (kiroCLIPath's doc comment).
func TestRefreshTriggerAndRegenerationUseTheSameBinarySeam(t *testing.T) {
	steerableKiroHome(t)

	original := kiroCLIPath
	kiroCLIPath = func() string { return filepath.Join(t.TempDir(), "no-such-kiro-cli") }
	t.Cleanup(func() { kiroCLIPath = original })

	ctx := context.Background()
	version, verErr := kiroCLIVersion(ctx)
	if verErr == nil {
		t.Errorf("kiroCLIVersion returned %q through a broken seam", version)
	}
	if err := kiroAgentCreateFromDefault(ctx, irrlichtAgentName, kiroBuiltinDefaultAgent); err == nil {
		t.Error("`agent create` succeeded through the same broken seam that failed the " +
			"version read — the trigger and the capability are not the same binary, so " +
			"agentrefresh.go's fail-closed argument does not hold")
	}
}
