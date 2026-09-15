package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"irrlicht/tools/onboarding-factory/internal/shard"
)

// recipesRepo writes a minimal catalog with one scenario ("resume-like",
// id "9.1", canonical folder "9-1_resume-like") and a meta.min_versions
// entry for every adapter in agents. Synthetic on purpose — AGENTS.md's
// testing philosophy forbids pinning a test to real replaydata values,
// which drift as the corpus is re-recorded.
func recipesRepo(t *testing.T, agents []string) string {
	t.Helper()
	root := t.TempDir()
	mv := make([]string, len(agents))
	for i, a := range agents {
		mv[i] = `"` + a + `": "1.0.0"`
	}
	write(t, filepath.Join(root, "replaydata", "agents", "scenarios.json"), `{
  "meta": {"min_versions": {`+strings.Join(mv, ", ")+`}},
  "scenarios": [
    {"id": "9.1", "name": "resume-like", "description": "d", "process": "p", "acceptance_criteria": "a"}
  ]
}`)
	return root
}

// recipeCell writes one (adapter, "resume-like") cell in its canonical
// folder. recipeJSON is the verbatim details.recipe object literal (e.g.
// `{"applicable":true,"script":[...]}`); pass "" to write a cell with NO
// recipe field at all (the recipeStatusNoRecipe case).
func recipeCell(t *testing.T, root, adapter, recipeJSON string) {
	t.Helper()
	dir := filepath.Join(root, "replaydata", "agents", adapter, "scenarios", "9-1_resume-like")
	details := "{}"
	if recipeJSON != "" {
		details = `{"recipe": ` + recipeJSON + `}`
	}
	write(t, filepath.Join(dir, "metadata.json"), `{"scenario_id": "resume-like", "details": `+details+`}`)
}

// resumeScript builds a script with a "resume" landmark step, optionally
// preceded/followed by a sleep. before/after are full JSON step objects
// (e.g. `{"type":"sleep","seconds":5}`) or "" to omit that side.
func resumeScript(before, after string) string {
	steps := []string{`{"type":"send","text":"x"}`, `{"type":"wait_turn"}`}
	if before != "" {
		steps = append(steps, before)
	}
	steps = append(steps, `{"type":"resume"}`)
	if after != "" {
		steps = append(steps, after)
	}
	steps = append(steps, `{"type":"send","text":"y"}`, `{"type":"wait_turn"}`)
	return `{"applicable": true, "timeout_seconds": 120, "settings": {}, "script": [` + strings.Join(steps, ",") + `]}`
}

// --- "cannot look" vs "nothing found" must not read the same (the task's
// own acceptance bar) ---

func TestScenarioRecipesUnknownScenarioFailsLoud(t *testing.T) {
	root := recipesRepo(t, []string{"a"})
	code, out, errb := runOf("scenario", "recipes", "--name", "no-such-scenario", "--repo-root", root)
	if code != exitFail {
		t.Fatalf("exit=%d want %d (exitFail); stdout=%q stderr=%q", code, exitFail, out, errb)
	}
	if out != "" {
		t.Fatalf("an unknown scenario must print nothing to stdout, got %q", out)
	}
	if !strings.Contains(errb, "is not a scenario") {
		t.Fatalf("stderr=%q; want it to say the id/name isn't a scenario", errb)
	}
}

func TestScenarioRecipesNoRecipesYetIsNotAnError(t *testing.T) {
	root := recipesRepo(t, []string{"a", "b"})
	recipeCell(t, root, "a", "")                      // no recipe field at all
	recipeCell(t, root, "b", `{"applicable": false}`) // blocked, never driven

	code, out, errb := runOf("scenario", "recipes", "--name", "resume-like", "--repo-root", root)
	if code != exitOK {
		t.Fatalf("exit=%d want %d (exitOK — a real scenario with nothing committed yet is not a failure); stdout=%q stderr=%q", code, exitOK, out, errb)
	}
	if errb != "" {
		t.Fatalf("stderr should be empty for a legitimate empty finding, got %q", errb)
	}
	if !strings.Contains(out, "no adapter has a committed, driven recipe") {
		t.Fatalf("stdout=%q; want the distinct 'nothing found yet' message", out)
	}
}

// TestScenarioRecipesUnknownVsEmptyAreDistinguishable directly proves the
// task's requirement: "these two must not print the same thing".
func TestScenarioRecipesUnknownVsEmptyAreDistinguishable(t *testing.T) {
	root := recipesRepo(t, []string{"a"})
	recipeCell(t, root, "a", "")

	knownCode, knownOut, knownErr := runOf("scenario", "recipes", "--name", "resume-like", "--repo-root", root)
	unknownCode, unknownOut, unknownErr := runOf("scenario", "recipes", "--name", "does-not-exist", "--repo-root", root)

	if knownCode == unknownCode {
		t.Fatalf("exit codes must differ: known-but-empty=%d unknown=%d", knownCode, unknownCode)
	}
	if knownOut == unknownOut && knownErr == unknownErr {
		t.Fatalf("output must differ between the two cases; both produced stdout=%q stderr=%q", knownOut, knownErr)
	}
	if knownOut == "" {
		t.Fatalf("a real scenario with no recipes yet must still print a report")
	}
	if unknownErr == "" {
		t.Fatalf("an unknown scenario must print an error")
	}
}

// --- QA finding A (#1969): checkCatalogReadable must check what
// shard.LoadAll actually needs to succeed, not a looser shape. shard.LoadAll
// unmarshals scenarios[] into typed []shard.Shard — a SINGLE type-mismatched
// field anywhere in the array (e.g. a numeric "id" where a string is
// expected) makes that whole json.Unmarshal call return a non-nil error, so
// shard's internal loadCatalog discards the ENTIRE catalog and LoadAll
// returns nil for EVERY id, not just the malformed entry. A probe that
// tolerates any per-element shape (e.g. unmarshaling scenarios as
// []json.RawMessage) would pass on exactly that file and then misreport the
// wholesale parse failure as "that id isn't in the catalog" once
// resolveScenario's shard.LoadAll call comes up empty.

// scenariosWithTypeMismatch writes a scenarios.json whose SECOND entry has a
// numeric "id" where shard.Shard expects a string — syntactically valid
// JSON, but a type shard.LoadAll's typed unmarshal rejects wholesale. The
// FIRST entry is otherwise well-formed, so a probe that only checks "is this
// syntactically valid JSON" would wrongly call the catalog readable.
func scenariosWithTypeMismatch(t *testing.T, root string) {
	t.Helper()
	write(t, filepath.Join(root, "replaydata", "agents", "scenarios.json"), `{
  "meta": {"min_versions": {"a": "1.0.0"}},
  "scenarios": [
    {"id": "9.1", "name": "resume-like", "description": "d", "process": "p", "acceptance_criteria": "a"},
    {"id": 123, "name": "other-scenario", "description": "d", "process": "p", "acceptance_criteria": "a"}
  ]
}`)
}

func TestCheckCatalogReadableRejectsATypeMismatchLoadAllAlsoRejects(t *testing.T) {
	root := t.TempDir()
	scenariosWithTypeMismatch(t, root)

	// Ground truth: shard.LoadAll (what resolveScenario actually calls)
	// really does choke on this file.
	if got := len(shard.LoadAll(root)); got != 0 {
		t.Fatalf("test fixture is not reproducing the bug: shard.LoadAll returned %d scenarios, want 0 (wholesale parse failure)", got)
	}

	if err := checkCatalogReadable(root); err == nil {
		t.Fatalf("checkCatalogReadable(%q) = nil; want an error — shard.LoadAll cannot parse this file at all", root)
	}
}

func TestScenarioRecipesReportsCatalogParseFailureNotUnknownScenario(t *testing.T) {
	root := t.TempDir()
	scenariosWithTypeMismatch(t, root)

	// "resume-like" is a perfectly well-formed entry in the SAME file — the
	// only thing wrong is a sibling entry's "id" field.
	code, out, errb := runOf("scenario", "recipes", "--name", "resume-like", "--repo-root", root)
	if code != exitFail {
		t.Fatalf("exit=%d want %d; stdout=%q stderr=%q", code, exitFail, out, errb)
	}
	if out != "" {
		t.Fatalf("a catalog parse failure must not also print a report; got stdout=%q", out)
	}
	if strings.Contains(errb, "is not a scenario") {
		t.Fatalf("must NOT misreport a catalog-wide parse failure as this id being unknown; stderr=%q", errb)
	}
	if !strings.Contains(errb, "catalog") {
		t.Fatalf("stderr=%q; want it to name the catalog as the problem", errb)
	}
}

// --- the core comparison: an omitted sleep must be visible, and "before"
// vs "after" must not be conflated (see landmarkSleepPair's doc comment —
// this is what running the helper against the real replaydata corpus for
// #1969 actually found: most siblings sleep BEFORE resume, not after). ---

// --- QA finding B (#1969): "<-- MISSING" must not fire just because a
// sibling's sleep sits on the OTHER side of the landmark. The corpus's
// dominant real shape (`exit_clean -> sleep -> resume -> send`, no sleep
// directly after resume) is not a bug — six committed adapters agree on it —
// so it must render unflagged. Only a landmark with NO settle sleep on
// EITHER side, with a step actually following it to race, is worth a flag.

// #1969 QA caught a real regression in a first cut of this fix: an
// "either side counts" rule for EVERY landmark made a reconstruction of
// muse's actual pre-fix recipe render unflagged, because its 12s sleep sits
// before resume. Measured live: `of scenario recipes` on that reconstruction
// printed "resume after: muse=NONE" with NO "<-- MISSING" suffix — the exact
// defect #1969 exists to catch, silently passed. The corrected rule is
// TYPE-SPECIFIC (requiresAfterSleep): resume is flagged on an absent AFTER
// sleep alone, because that is the specific window the #1960 daemon
// mechanism needs covered (re-admission to next observable send) and a
// sleep sitting BEFORE resume — however large — has already elapsed by the
// time that window opens. exit_clean/sigkill still accept either side,
// because nothing observable ever races them directly in this corpus.

func TestScenarioRecipesResumeRequiresAnAfterSleepSpecifically(t *testing.T) {
	root := recipesRepo(t, []string{"before-only", "after-only"})
	// before-only: exactly the six real siblings' shape for `resume` —
	// settles with a sleep BEFORE it, sends immediately after. This IS
	// flagged: the sleep does not cover the post-resume race window.
	recipeCell(t, root, "before-only", resumeScript(`{"type":"sleep","seconds":5}`, ""))
	// after-only: settles with a sleep AFTER resume — covers the window,
	// not flagged.
	recipeCell(t, root, "after-only", resumeScript("", `{"type":"sleep","seconds":6}`))

	code, out, errb := runOf("scenario", "recipes", "--name", "resume-like", "--repo-root", root)
	if code != exitOK {
		t.Fatalf("exit=%d want %d; stderr=%q", code, exitOK, errb)
	}

	if !strings.Contains(out, "before-only=NONE <-- MISSING") {
		t.Fatalf("before-only has a sleep BEFORE resume but none after — must still be flagged (before doesn't cover the post-resume window); got:\n%s", out)
	}
	if !strings.Contains(out, "after-only=6s") {
		t.Fatalf("after-only's post-resume sleep must appear on the after row, unflagged; got:\n%s", out)
	}
	if strings.Contains(out, "after-only=6s <-- MISSING") {
		t.Fatalf("after-only must not be flagged — it covers the window; got:\n%s", out)
	}
	if !strings.Contains(out, "before-only=5s") {
		t.Fatalf("before-only's pre-resume sleep must still be shown on the before row (factual, even though it doesn't clear the flag); got:\n%s", out)
	}
}

func TestScenarioRecipesExitCleanAcceptsASleepOnEitherSide(t *testing.T) {
	root := recipesRepo(t, []string{"solo"})
	// A sleep BEFORE exit_clean, none after (exit_clean is immediately
	// followed by resume, not a sleep) — unlike resume, this must NOT be
	// flagged: nothing observable races exit_clean directly.
	recipeCell(t, root, "solo", `{"applicable": true, "timeout_seconds": 120, "settings": {}, "script": [`+
		`{"type":"send","text":"x"},{"type":"wait_turn"},{"type":"sleep","seconds":5},`+
		`{"type":"exit_clean"},{"type":"resume"},{"type":"sleep","seconds":6},`+
		`{"type":"send","text":"y"},{"type":"wait_turn"}]}`)

	_, out, errb := runOf("scenario", "recipes", "--name", "resume-like", "--repo-root", root)
	if errb != "" {
		t.Fatalf("unexpected stderr: %q", errb)
	}
	if strings.Contains(out, "exit_clean") && strings.Contains(out, "MISSING") {
		// Only fail if the MISSING marker is actually on an exit_clean line.
		for _, line := range strings.Split(out, "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "exit_clean") && strings.Contains(line, "MISSING") {
				t.Fatalf("exit_clean has a sleep BEFORE it — must not be flagged; got line:\n%s", line)
			}
		}
	}
	if !strings.Contains(out, "before:  solo=5s") {
		t.Fatalf("exit_clean's before-sleep must render; got:\n%s", out)
	}
}

// TestScenarioRecipesFlagsTheReconstructedMusePreFixRecipe is a PERMANENT
// regression fixture: muse's actual committed script MINUS the 6s sleep
// that fixed #1960 (replaydata/agents/muse/scenarios/1-4_session-resume's
// own scope_note documents the before/after shapes). This is the ONE case
// #1969 exists to catch, and nothing else in this suite pins the literal
// historical defect.
func TestScenarioRecipesFlagsTheReconstructedMusePreFixRecipe(t *testing.T) {
	root := recipesRepo(t, []string{"muse"})
	recipeCell(t, root, "muse", `{"applicable": true, "timeout_seconds": 240, "settings": {}, "script": [`+
		`{"type":"send","text":"Reply with exactly the word: ok"},`+
		`{"type":"wait_turn"},`+
		`{"type":"sleep","seconds":2},`+
		`{"type":"exit_clean"},`+
		`{"type":"sleep","seconds":12},`+
		`{"type":"resume"},`+
		`{"type":"send","text":"Reply with exactly the word: again"},`+
		`{"type":"wait_turn"},`+
		`{"type":"sleep","seconds":4}]}`)

	code, out, errb := runOf("scenario", "recipes", "--name", "resume-like", "--repo-root", root)
	if code != exitOK {
		t.Fatalf("exit=%d want %d; stderr=%q", code, exitOK, errb)
	}
	if !strings.Contains(out, "resume         after:   muse=NONE <-- MISSING") {
		t.Fatalf("pre-fix muse's recipe (12s BEFORE resume, nothing after) must flag resume's after row; got:\n%s", out)
	}
	// The 12s sleep is real and load-bearing for a DIFFERENT purpose (it
	// clears the deletedSessions cooldown before relaunch) — it must still
	// render, just not clear the flag.
	if !strings.Contains(out, "resume         before:  muse=12s") {
		t.Fatalf("the pre-resume 12s sleep must still be shown; got:\n%s", out)
	}
}

func TestScenarioRecipesFlagsWhenNeitherSideHasASleep(t *testing.T) {
	root := recipesRepo(t, []string{"unsafe"})
	// No sleep on either side of resume, and a send follows it — the actual
	// shape #1960 shipped with.
	recipeCell(t, root, "unsafe", resumeScript("", ""))

	code, out, errb := runOf("scenario", "recipes", "--name", "resume-like", "--repo-root", root)
	if code != exitOK {
		t.Fatalf("exit=%d want %d; stderr=%q", code, exitOK, errb)
	}
	if !strings.Contains(out, "unsafe=NONE <-- MISSING") {
		t.Fatalf("unsafe has no settle sleep on either side of resume, and a send follows — must be flagged; got:\n%s", out)
	}
}

func TestScenarioRecipesDoesNotFlagATerminalLandmarkWithNoFollowingStep(t *testing.T) {
	root := recipesRepo(t, []string{"terminal"})
	// A script that ENDS at resume: no sleep on either side, but nothing
	// follows it either, so there is nothing to race.
	recipeCell(t, root, "terminal", `{"applicable": true, "timeout_seconds": 120, "settings": {}, "script": [`+
		`{"type":"send","text":"x"},{"type":"wait_turn"},{"type":"resume"}]}`)

	_, out, errb := runOf("scenario", "recipes", "--name", "resume-like", "--repo-root", root)
	if errb != "" {
		t.Fatalf("unexpected stderr: %q", errb)
	}
	if strings.Contains(out, "<-- MISSING") {
		t.Fatalf("resume is the LAST step — nothing follows it to race, so it must not be flagged; got:\n%s", out)
	}
}

func TestScenarioRecipesExcludesNonProcessLifecycleStepsFromTheComparison(t *testing.T) {
	root := recipesRepo(t, []string{"solo"})
	recipeCell(t, root, "solo", `{"applicable": true, "timeout_seconds": 120, "settings": {}, "script": [`+
		`{"type":"start_session"},{"type":"seed_instruction","path":"x","text":"y"},`+
		`{"type":"send","text":"x"},{"type":"wait_turn"}]}`)

	_, out, errb := runOf("scenario", "recipes", "--name", "resume-like", "--repo-root", root)
	if errb != "" {
		t.Fatalf("unexpected stderr: %q", errb)
	}
	if strings.Contains(out, "start_session") && strings.Contains(out, "sleep adjoining") {
		t.Fatalf("start_session mints a NEW session id (never re-admitted) — it must not appear in the landmark comparison; got:\n%s", out)
	}
	if strings.Contains(out, "seed_instruction") && strings.Contains(out, "sleep adjoining") {
		t.Fatalf("seed_instruction never ends the process — it must not appear in the landmark comparison; got:\n%s", out)
	}
	// No landmark type present at all -> the whole section is omitted.
	if strings.Contains(out, "sleep adjoining each landmark") {
		t.Fatalf("neither remaining step type is a process-lifecycle landmark — the comparison section should not print at all; got:\n%s", out)
	}
}

func TestScenarioRecipesStepLineRendersFullSequence(t *testing.T) {
	root := recipesRepo(t, []string{"solo"})
	recipeCell(t, root, "solo", resumeScript(`{"type":"sleep","seconds":5}`, `{"type":"sleep","seconds":6}`))

	_, out, errb := runOf("scenario", "recipes", "--name", "resume-like", "--repo-root", root)
	want := "send -> wait_turn -> SLEEP 5s -> resume -> SLEEP 6s -> send -> wait_turn"
	if !strings.Contains(out, want) {
		t.Fatalf("stdout=%q (stderr=%q); want it to contain the step sequence %q", out, errb, want)
	}
}

// --- fail loud: a cell this command specifically needed but could not
// read/parse must not be silently reported as "no cell". ---

func TestScenarioRecipesFailsLoudOnMalformedCanonicalCell(t *testing.T) {
	root := recipesRepo(t, []string{"broken", "fine"})
	recipeCell(t, root, "fine", resumeScript("", `{"type":"sleep","seconds":4}`))
	dir := filepath.Join(root, "replaydata", "agents", "broken", "scenarios", "9-1_resume-like")
	write(t, filepath.Join(dir, "metadata.json"), `{ this is not valid json`)

	code, out, errb := runOf("scenario", "recipes", "--name", "resume-like", "--repo-root", root)
	if code == exitOK {
		t.Fatalf("a malformed cell must not exit 0; stdout=%q", out)
	}
	if out != "" {
		t.Fatalf("a read error must not ALSO print a report — a truncated/empty-looking table would read as 'no differences'; got stdout=%q", out)
	}
	if !strings.Contains(errb, "could not be read") {
		t.Fatalf("stderr=%q; want a clear read-error message naming the unreadable file", errb)
	}
	if strings.Contains(errb, "no committed cell") {
		t.Fatalf("a malformed file must not be described in the same words as a genuinely absent cell; stderr=%q", errb)
	}
}

// --- status classification: every non-comparable state gets its own,
// distinct label rather than collapsing into a single "no recipe". ---

func TestScenarioRecipesClassifiesEveryStatus(t *testing.T) {
	root := recipesRepo(t, []string{"has-script", "no-recipe", "blocked", "no-script", "no-cell"})
	recipeCell(t, root, "has-script", resumeScript("", `{"type":"sleep","seconds":4}`))
	recipeCell(t, root, "no-recipe", "")
	recipeCell(t, root, "blocked", `{"applicable": false}`)
	recipeCell(t, root, "no-script", `{"applicable": true, "timeout_seconds": 120, "settings": {}, "script": []}`)
	// "no-cell": deliberately no metadata.json written at all.

	_, out, errb := runOf("scenario", "recipes", "--name", "resume-like", "--repo-root", root)
	if errb != "" {
		t.Fatalf("unexpected stderr: %q", errb)
	}
	if !strings.Contains(out, "1/5 onboarded adapters carry a committed, driven recipe") {
		t.Fatalf("stdout=%q; want the has_script count to be exactly 1/5", out)
	}
	for adapter, note := range map[string]string{
		"no-recipe": "cell exists, no recipe authored yet (details.recipe absent)",
		"blocked":   "blocked -- recipe.applicable=false, never driven",
		"no-script": "recipe present but script is empty",
		"no-cell":   "no committed cell for this scenario",
	} {
		want := fmt.Sprintf("  %-12s %s\n", adapter, note) // matches printOtherAdapterLines's own format string
		if !strings.Contains(out, want) {
			t.Fatalf("stdout missing line %q; got:\n%s", want, out)
		}
	}
}

// --- JSON output: a lock (green by construction) pinning the wire shape,
// not a behavior this command needed to be defect-tested for. ---

func TestScenarioRecipesJSONRoundTrips(t *testing.T) {
	root := recipesRepo(t, []string{"solo"})
	recipeCell(t, root, "solo", resumeScript("", `{"type":"sleep","seconds":6}`))

	code, out, errb := runOf("scenario", "recipes", "--name", "resume-like", "--json", "--repo-root", root)
	if code != exitOK {
		t.Fatalf("exit=%d; stderr=%q", code, errb)
	}
	var got scenarioRecipeReport
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, out)
	}
	if got.ScenarioID != "9.1" || got.ScenarioName != "resume-like" {
		t.Fatalf("got scenario id/name %q/%q, want 9.1/resume-like", got.ScenarioID, got.ScenarioName)
	}
	var solo *adapterRecipeView
	for i := range got.Adapters {
		if got.Adapters[i].Adapter == "solo" {
			solo = &got.Adapters[i]
		}
	}
	if solo == nil {
		t.Fatalf("adapter %q missing from JSON output", "solo")
	}
	// send, wait_turn, resume, sleep, send, wait_turn.
	if solo.Status != recipeStatusHasScript || len(solo.Steps) != 6 {
		t.Fatalf("solo = %+v; want status has_script with 6 steps", solo)
	}
}

// --- recorded flag: exercises the validate.NewestRecordingDir wiring. A
// lock — pins that a recording directory flips Recorded, not new logic. ---

func TestScenarioRecipesMarksRecordedCells(t *testing.T) {
	root := recipesRepo(t, []string{"solo"})
	recipeCell(t, root, "solo", resumeScript("", `{"type":"sleep","seconds":4}`))
	recording(t, root, "solo", "9-1_resume-like", "r1", false)

	_, out, errb := runOf("scenario", "recipes", "--name", "resume-like", "--repo-root", root)
	if errb != "" {
		t.Fatalf("unexpected stderr: %q", errb)
	}
	if !strings.Contains(out, "solo         [recorded    ]") {
		t.Fatalf("stdout=%q; want solo marked [recorded    ]", out)
	}
}

// --- QA finding B (#1969): a ratchet over the REAL corpus, so the
// false-positive rate cannot silently regress as new scenarios land. Follows
// the same shape as internal/validate's
// TestNoCellCarriesAnUnevaluableInvariant (unevaluableInvariantRatchet):
// fail loud if the scan itself can't run, fail if the count goes UP (a
// regression), fail if it goes DOWN too (force the constant to be lowered so
// a real improvement gets locked in rather than silently re-drifting).

// corpusLandmarkFlagRatchet is how many landmark occurrences across every
// committed, driven recipe in replaydata/agents get flagged "<-- MISSING" by
// `of scenario recipes`.
//
// Produced by this test — run it and read the failure, do not count by hand:
//
//	go test ./tools/onboarding-factory/cmd/of/ \
//	    -run TestScenarioRecipesCorpusFlagRateStaysLow -count=1 -v
//
// This number has moved twice, for two different reasons — both measured by
// this same test, never asserted by hand:
//
//  1. A first cut treating every non-send/wait_turn/sleep step as a landmark
//     (isTimingLandmark) and flagging on an absent sleep on EITHER side
//     measured 109 of 280 occurrences (39%) — including 100% of keys, slash,
//     start_session, seed_instruction and restart, none of which relate to
//     the #1960 daemon cooldown this command exists to surface. Narrowing to
//     {resume, exit_clean, sigkill} brought this to 0 of 79.
//  2. That 0 was WRONG in a different way: it also silenced the one true
//     defect #1969 exists to catch. A reconstruction of muse's actual
//     pre-fix recipe (`exit_clean -> SLEEP 12s -> resume -> send`, no sleep
//     after resume) rendered unflagged, because the "either side" rule let
//     the 12s sleep BEFORE resume stand in for one after it — but that 12s
//     only clears the deletedSessions cooldown before relaunch; it does not
//     cover the separate window between resume completing and the next
//     send, which is what actually failed. requiresAfterSleep makes resume
//     flag on an absent AFTER sleep specifically (exit_clean/sigkill still
//     accept either side, since nothing observable races them directly in
//     this corpus), which both flags that reconstruction
//     (TestScenarioRecipesFlagsTheReconstructedMusePreFixRecipe) and settles
//     the corpus scan at 6 of 79 — precisely the six adapters whose
//     committed 1-4_session-resume recipe places its settle sleep before
//     resume rather than after (a real, previously-invisible open question,
//     not noise: nothing here claims those six are unsafe, only that they
//     depart from the four siblings' proven shape and deserve a
//     confirm-or-write-it-down against #1969's own SKILL.md instruction).
const corpusLandmarkFlagRatchet = 6

func TestScenarioRecipesCorpusFlagRateStaysLow(t *testing.T) {
	root := filepath.Join("..", "..", "..", "..", "replaydata", "agents")
	matches, err := filepath.Glob(filepath.Join(root, "*", "scenarios", "*", "metadata.json"))
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	if len(matches) == 0 {
		t.Fatalf("no metadata.json files found under %s — the corpus scan cannot run, which is not the same as finding nothing", root)
	}

	type occurrence struct{ cell, landmarkType string }
	var flagged []occurrence
	total := 0

	for _, path := range matches {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		var cellDoc struct {
			Details struct {
				Recipe json.RawMessage `json:"recipe"`
			} `json:"details"`
		}
		if json.Unmarshal(raw, &cellDoc) != nil || len(cellDoc.Details.Recipe) == 0 {
			continue
		}
		// Ask the evaluator itself (recipeDoc / landmarkSleeps) rather than
		// re-implementing the parse here — a second copy would drift from
		// the one the command actually runs.
		var doc recipeDoc
		if json.Unmarshal(cellDoc.Details.Recipe, &doc) != nil {
			continue
		}
		if doc.Applicable != nil && !*doc.Applicable {
			continue
		}
		if len(doc.Script) == 0 {
			continue
		}
		steps := make([]recipeStepView, len(doc.Script))
		for i, s := range doc.Script {
			steps[i] = recipeStepView{Index: i + 1, Type: s.Type, Seconds: s.Seconds}
		}
		rel := strings.TrimPrefix(filepath.ToSlash(path), filepath.ToSlash(root)+"/")
		cell := strings.TrimSuffix(rel, "/metadata.json")
		for landmarkType, pairs := range landmarkSleeps(steps) {
			for _, p := range pairs {
				total++
				if p.flagged() {
					flagged = append(flagged, occurrence{cell, landmarkType})
				}
			}
		}
	}

	if total == 0 {
		t.Fatalf("parsed %d metadata.json files but found no landmark occurrences at all — the scan cannot run", len(matches))
	}

	if len(flagged) > corpusLandmarkFlagRatchet {
		sort.Slice(flagged, func(i, j int) bool {
			if flagged[i].cell != flagged[j].cell {
				return flagged[i].cell < flagged[j].cell
			}
			return flagged[i].landmarkType < flagged[j].landmarkType
		})
		var b strings.Builder
		fmt.Fprintf(&b, "%d of %d landmark occurrences flagged MISSING (ratchet: %d).\n\n", len(flagged), total, corpusLandmarkFlagRatchet)
		for _, o := range flagged {
			fmt.Fprintf(&b, "  %s: %s\n", o.cell, o.landmarkType)
		}
		t.Error(b.String())
	}
	if len(flagged) < corpusLandmarkFlagRatchet {
		t.Errorf("only %d of %d landmark occurrences flagged, below the ratchet of %d — lower corpusLandmarkFlagRatchet to %d so the gain is locked in",
			len(flagged), total, corpusLandmarkFlagRatchet, len(flagged))
	}
}
