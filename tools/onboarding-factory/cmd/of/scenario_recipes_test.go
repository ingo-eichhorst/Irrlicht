package main

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
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

// --- the core comparison: an omitted sleep must be visible, and "before"
// vs "after" must not be conflated (see landmarkSleepPair's doc comment —
// this is what running the helper against the real replaydata corpus for
// #1969 actually found: most siblings sleep BEFORE resume, not after). ---

func TestScenarioRecipesFlagsSleepMissingAfterLandmarkButNotBefore(t *testing.T) {
	root := recipesRepo(t, []string{"before-style", "after-style"})
	// before-style: settles with a sleep BEFORE resume, sends immediately after.
	recipeCell(t, root, "before-style", resumeScript(`{"type":"sleep","seconds":5}`, ""))
	// after-style: resumes immediately, settles with a sleep AFTER resume.
	recipeCell(t, root, "after-style", resumeScript("", `{"type":"sleep","seconds":6}`))

	code, out, errb := runOf("scenario", "recipes", "--name", "resume-like", "--repo-root", root)
	if code != exitOK {
		t.Fatalf("exit=%d want %d; stderr=%q", code, exitOK, errb)
	}

	if !strings.Contains(out, "before-style=NONE <-- MISSING") {
		t.Fatalf("before-style has no sleep AFTER resume — must be flagged MISSING on the after row; got:\n%s", out)
	}
	if !strings.Contains(out, "after-style=6s") {
		t.Fatalf("after-style DOES have a 6s sleep after resume — must show it, unflagged; got:\n%s", out)
	}
	if strings.Contains(out, "after-style=NONE <-- MISSING") {
		t.Fatalf("after-style must NOT be flagged on the after row — it has a post-resume sleep; got:\n%s", out)
	}
	// The point of tracking "before" separately: before-style's OWN settle
	// sleep must still be visible, so a reader doesn't read its "after:
	// NONE" as proof it has no settle time at all.
	if !strings.Contains(out, "before-style=5s") {
		t.Fatalf("before-style's pre-resume sleep must appear on the before row; got:\n%s", out)
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
