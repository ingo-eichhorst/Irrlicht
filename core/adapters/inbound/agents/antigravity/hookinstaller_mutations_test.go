// hookinstaller_mutations_test.go is the committed mutation evidence for this
// adapter's merge — the fixture docs/testing-philosophy.md asks for in place of
// a paragraph in a PR body that nothing re-runs.
//
// Everything this change adds has no "before the fix" to run red, so the rule
// is to mutate the thing the check protects and confirm the check reports it.
// Doing that by hand once proves the assertions discriminated on the day they
// were written; committing it proves they still do. The failure this guards
// against is specific and has happened in this repo before: an assertion that
// silently stops discriminating looks exactly like health.
//
// Two corpora live here, for the two things a reader would otherwise have to
// take on trust.
//
//   - mergeMutations drives hookinstaller_property_test.go's two preservation
//     assertions against install results that are wrong in exactly ONE way, and
//     requires each to REPORT. Each row also names the assertions it must leave
//     SILENT, which is half the evidence: without it, four mutations against
//     two assertions are equally satisfied by two assertions that report on
//     everything.
//
//   - resultMutations drives the hazard-1 predicate — "the installed handler's
//     stdout is exactly `{}`" — against candidate command renderings including
//     the ones an author might reach for, and requires the rejections. Unlike
//     the merge corpus it EXECUTES each candidate through a real `sh -c`,
//     because the claim being graded is about bytes on a file descriptor, not
//     about a string.
package antigravity

import (
	"fmt"
	"os/exec"
	"strings"
	"testing"
)

// recordingReporter is the reporter the merge corpus drives the assertions
// through. It records rather than fails, so a mutation the assertions catch is
// a PASS here.
type recordingReporter struct{ reports []string }

func (r *recordingReporter) Helper() {}

func (r *recordingReporter) Errorf(format string, args ...interface{}) {
	r.reports = append(r.reports, fmt.Sprintf(format, args...))
}

func (r *recordingReporter) reported() bool { return len(r.reports) > 0 }

// --- merge corpus ---

// mergeMutation is one broken install result, plus which of the two
// preservation assertions must report it and which must stay quiet.
type mergeMutation struct {
	name string
	// breaks the "after" document a correct install would have produced.
	break_ func(before, after map[string]interface{})
	// wantForeignNames is whether assertForeignNamesPreserved must report.
	wantForeignNames bool
	// wantOurHook is whether assertOurHookIsCorrect must report.
	wantOurHook bool
	// why records what real defect this row stands in for.
	why string
}

// mergeMutations is the corpus. Each row is a defect a merge over a
// user-owned document can actually have; three of the five were run as live
// source mutations against the installer while #1723 was being written and are
// reproduced here so they keep running.
var mergeMutations = []mergeMutation{
	{
		name: "deletes another named hook",
		break_: func(_, after map[string]interface{}) {
			for name := range after {
				if name != hookName {
					delete(after, name)
					return
				}
			}
		},
		wantForeignNames: true,
		wantOurHook:      false,
		why: "the sync-by-omission clobber #1372 documents for gemini-cli's settings writer, " +
			"pointed at a file antigravity's own /hooks command shares with irrlicht",
	},
	{
		name: "rewrites another named hook's handler",
		break_: func(_, after map[string]interface{}) {
			for name, value := range after {
				if name == hookName {
					continue
				}
				spec, ok := value.(map[string]interface{})
				if !ok {
					continue
				}
				for key, v := range spec {
					if arr, isArray := v.([]interface{}); isArray && len(arr) > 0 {
						spec[key] = []interface{}{map[string]interface{}{"command": "./ours-now.sh"}}
						return
					}
				}
			}
		},
		wantForeignNames: true,
		wantOurHook:      false,
		why:              "a merge that normalizes the whole document instead of only its own subtree",
	},
	{
		name: "drops a foreign handler from our own event array",
		break_: func(_, after map[string]interface{}) {
			ours, ok := after[hookName].(map[string]interface{})
			if !ok {
				return
			}
			handlers, ok := ours[HookEventStop].([]interface{})
			if !ok {
				return
			}
			var kept []interface{}
			for _, h := range handlers {
				entry, isEntry := h.(map[string]interface{})
				if isEntry && isOurs(entry) {
					kept = append(kept, h)
				}
			}
			ours[HookEventStop] = kept
		},
		wantForeignNames: false,
		wantOurHook:      true,
		why: "M1, run live against reconcileHandlers: treating our own event array as ours to " +
			"own, when upstream merges multiple named hooks per event and coexistence is normal",
	},
	{
		name: "deletes a foreign key from under our own name",
		break_: func(_, after map[string]interface{}) {
			ours, ok := after[hookName].(map[string]interface{})
			if !ok {
				return
			}
			for key := range ours {
				if key != HookEventStop {
					delete(ours, key)
					return
				}
			}
		},
		wantForeignNames: false,
		wantOurHook:      true,
		why: "re-enabling a hook the user disabled through /hooks — `enabled: false` is a key " +
			"under our name that irrlicht must preserve rather than reassert on every daemon start",
	},
	{
		name: "installs a second copy of our own handler",
		break_: func(_, after map[string]interface{}) {
			ours, ok := after[hookName].(map[string]interface{})
			if !ok {
				return
			}
			handlers, ok := ours[HookEventStop].([]interface{})
			if !ok || len(handlers) == 0 {
				return
			}
			for _, h := range handlers {
				entry, isEntry := h.(map[string]interface{})
				if isEntry && isOurs(entry) {
					ours[HookEventStop] = append(handlers, entry)
					return
				}
			}
		},
		wantForeignNames: false,
		wantOurHook:      true,
		why:              "an append-if-absent that stopped recognizing its own entry: two beacon spawns per turn, forever",
	},
}

// TestMergeAssertionsCatchEveryCommittedMutation is the corpus driver.
func TestMergeAssertionsCatchEveryCommittedMutation(t *testing.T) {
	if len(mergeMutations) == 0 {
		t.Fatal("the corpus is empty — a mutation table with no rows grades nothing and passes")
	}

	// One seeded document rich enough to express every row: another named
	// hook, a foreign key under our name, and a foreign handler in our own
	// event array.
	seed := `{
  "lint-checker": { "PostToolUse": [ { "matcher": "*", "hooks": [ { "command": "./lint.sh" } ] } ] },
  "` + hookName + `": {
    "enabled": false,
    "Stop": [ { "type": "command", "command": "./theirs.sh" } ]
  }
}
`

	// The vacuity guard: a CORRECT install must leave both assertions silent,
	// or every row below would pass for a reason unrelated to its mutation.
	t.Run("correct_install_is_silent", func(t *testing.T) {
		before, after := installFromSeed(t, seed)
		names, hook := runMergeAssertions(before, after)
		if names.reported() {
			t.Errorf("assertForeignNamesPreserved reported against a CORRECT install: %v", names.reports)
		}
		if hook.reported() {
			t.Errorf("assertOurHookIsCorrect reported against a CORRECT install: %v", hook.reports)
		}
	})

	for _, m := range mergeMutations {
		t.Run(m.name, func(t *testing.T) {
			before, after := installFromSeed(t, seed)
			m.break_(before, after)

			names, hook := runMergeAssertions(before, after)

			if got := names.reported(); got != m.wantForeignNames {
				t.Errorf("assertForeignNamesPreserved reported=%v, want %v.\nmutation stands in for: %s\nreports: %v",
					got, m.wantForeignNames, m.why, names.reports)
			}
			if got := hook.reported(); got != m.wantOurHook {
				t.Errorf("assertOurHookIsCorrect reported=%v, want %v.\nmutation stands in for: %s\nreports: %v",
					got, m.wantOurHook, m.why, hook.reports)
			}
			if !m.wantForeignNames && !m.wantOurHook {
				t.Fatalf("row %q expects no assertion to report — it would pass for a broken merge "+
					"and for a working one alike", m.name)
			}
		})
	}
}

// installFromSeed writes seed into an isolated hooks.json, installs, and
// returns the before/after documents the property assertions grade.
func installFromSeed(t *testing.T, seed string) (before, after map[string]interface{}) {
	t.Helper()
	path := antigravityConfigHome(t)
	writeDoc(t, path, seed)
	before = readDoc(t, path)
	if _, err := EnsureHooksInstalled(); err != nil {
		t.Fatalf("install: %v", err)
	}
	after = readDoc(t, path)
	return before, after
}

func runMergeAssertions(before, after map[string]interface{}) (names, hook *recordingReporter) {
	names, hook = &recordingReporter{}, &recordingReporter{}
	assertForeignNamesPreserved(names, before, after)
	assertOurHookIsCorrect(hook, before, after)
	return names, hook
}

// --- inert-result corpus ---

// resultMutation is one candidate rendering of the installed command's tail,
// plus whether the hazard-1 predicate must reject it.
type resultMutation struct {
	name   string
	suffix string
	// wantInert is whether the rendered command's stdout must be accepted as
	// the inert result.
	wantInert bool
	why       string
}

// resultMutations is the corpus for hazard 1: the installed Stop handler's
// stdout is a control channel into the user's agent under hooks.md §5, so the
// obligation is that it can only ever be `{}`.
//
// The want:false rows carry the value, and two of them are the renderings an
// author would plausibly reach for rather than exotic ones.
var resultMutations = []resultMutation{
	{
		name: "the shipped suffix", suffix: inertResultSuffix, wantInert: true,
		why: "the vacuity guard — a predicate that rejected everything would satisfy every row below",
	},
	{
		name: "nothing at all", suffix: "", wantInert: false,
		why: "the beacon already redirects its own stdout to /dev/null, so a bare beacon prints " +
			"NOTHING. Empty stdout is not `{}`: whether antigravity treats an unparseable result as " +
			"benign is not measured, and #1723's probe measured `{}` rather than emptiness",
	},
	{
		name: "a decision that blocks the stop", suffix: `; printf '{"decision":"continue"}'`, wantInert: false,
		why: "M4, run live: hooks.md §5's own example, which re-enters the agent loop — the one " +
			"thing a monitoring channel must never be able to do",
	},
	{
		name: "a decision that allows the stop", suffix: `; printf '{"decision":"stop"}'`, wantInert: false,
		why: "still a decision. §5 says any value other than continue allows the stop, so this one " +
			"is harmless TODAY — and it is rejected anyway, because a channel that emits a decision " +
			"field at all is one upstream can redefine underneath us",
	},
	{
		name: "a human-readable line", suffix: `; echo irrlicht ok`, wantInert: false,
		why: "the diagnostic an author adds while debugging and forgets to remove; it is not JSON " +
			"and antigravity parses this stream",
	},
	{
		name: "an inert object with a trailing newline", suffix: `; printf '{}\n'`, wantInert: false,
		why: "harmless in substance and rejected on FORM: the predicate pins the exact bytes the " +
			"probe observed, so a change to what is emitted is a change a reader has to make deliberately",
	},
}

// TestInstalledResultCorpus executes each candidate and grades its stdout.
func TestInstalledResultCorpus(t *testing.T) {
	if len(resultMutations) == 0 {
		t.Fatal("the corpus is empty")
	}
	beacon := "true" // stands in for the beacon: exits 0, prints nothing on stdout

	inert := 0
	for _, m := range resultMutations {
		t.Run(m.name, func(t *testing.T) {
			// #nosec G204 -- every command is built from a literal in this file.
			cmd := exec.Command("sh", "-c", beacon+m.suffix)
			cmd.Stdin = strings.NewReader("{}")
			var stderr strings.Builder
			cmd.Stderr = &stderr
			stdout, err := cmd.Output()
			if err != nil {
				t.Fatalf("candidate exited non-zero: %v (stderr %q)", err, stderr.String())
			}

			got := string(stdout) == "{}"
			if got != m.wantInert {
				t.Errorf("stdout %q graded inert=%v, want %v.\nrow stands for: %s",
					stdout, got, m.wantInert, m.why)
			}
		})
		if m.wantInert {
			inert++
		}
	}
	if inert != 1 {
		t.Errorf("%d rows are marked inert; exactly one — the shipped suffix — should be. More than "+
			"one means the corpus has stopped pinning a single rendering", inert)
	}
}
