package processlifecycle

import (
	"go/parser"
	"go/token"
	"strings"
	"testing"

	"go/ast"
)

// This is the corpus behind shellout_guard_test.go's rule: one spelling per
// case, pinned to the verdict the detector must return.
//
// It exists because the mutation evidence for a guard is otherwise re-run by
// nothing. The five mutations that established this rule discriminates —
// reverting processTTY, kittyWindowIDForPID and WriterOf to their collapsed
// spellings, and appending the queued tmux shellout written the way all six
// previous instances were written — each turned the live rule red exactly
// once, and every one of those is a row below. A guard whose evidence lives
// only in a merged PR body is a guard nobody can check again.
//
// It runs the PRODUCTION detector (scanShelloutClassification), not a copy, so
// a change to the rule is measured against these cases rather than against a
// second implementation that can drift.

// shelloutShape is one construction spelling and the verdict it pins.
type shelloutShape struct {
	name     string
	src      string // a complete file in package shapes
	needle   string // a substring that MUST appear in src
	want     int    // findings the detector must report
	wantRuns int    // run sites it must SEE (0 findings from 0 run sites proves nothing)
	why      string
}

func shelloutShapes() []shelloutShape {
	return []shelloutShape{
		// ---- caught: the family's collapse, in its spellings ----
		{
			name: "collapse_after_output", want: 1, wantRuns: 1,
			needle: `if err != nil {`,
			why:    "the sentence every issue in this family is: #1485, #1492, #1513, #1524, #1533, #1537",
			src: `package shapes
func probe(pid int) string {
	out, err := exec.CommandContext(ctx, psPath, "-p", pid).Output()
	if err != nil {
		return ""
	}
	return string(out)
}`,
		},
		{
			name: "blank_assigned_error", want: 1, wantRuns: 1,
			needle: `out, _ :=`,
			why:    "background_probe.go spells it this way today; the error is gone before anything could classify it",
			src: `package shapes
func probe() string {
	out, _ := exec.CommandContext(ctx, lsofPath, "-t").Output()
	return string(out)
}`,
		},
		{
			name: "run_as_bare_statement", want: 1, wantRuns: 1,
			needle: `.Run()` + "\n",
			why:    "no binding at all: there is no error variable to classify",
			src: `package shapes
func probe() {
	exec.CommandContext(ctx, kittenPath, "@", "close").Run()
}`,
		},
		{
			name: "if_init_collapse", want: 1, wantRuns: 1,
			needle: `if err :=`,
			why:    "the same collapse with the assignment inside the if statement, which a naive AssignStmt walk misses",
			src: `package shapes
func probe() int {
	if err := exec.CommandContext(ctx, pgrepPath, "-x", "claude").Run(); err != nil {
		return 0
	}
	return 1
}`,
		},
		{
			name: "second_shellout_collapses", want: 1, wantRuns: 2,
			needle: `secondOut, err :=`,
			why: "the #1538 shape itself: a function that already classifies one shellout is exactly where the next one gets added, " +
				"and a function-wide 'does err appear near a classifier' check would call this clean",
			src: `package shapes
func probe() (string, bool) {
	out, err := exec.CommandContext(ctx, psPath, "-p", "1").Output()
	if !probeAnswered(err) {
		return "", false
	}
	secondOut, err := exec.CommandContext(ctx, lsofPath, "-t").Output()
	if err != nil {
		return string(out), true
	}
	return string(secondOut), true
}`,
		},
		{
			name: "classified_before_the_run", want: 1, wantRuns: 1,
			needle: `probeAnswered(err)`,
			why:    "a classifier call that PRECEDES the run cannot be about its error; the position test is what makes this a finding",
			src: `package shapes
func probe(err error) string {
	if !probeAnswered(err) {
		return ""
	}
	out, err := exec.CommandContext(ctx, psPath, "-p", "1").Output()
	if err != nil {
		return ""
	}
	return string(out)
}`,
		},

		// ---- clean: the two legitimate answers ----
		{
			name: "classified_empty_variadic", want: 0, wantRuns: 1,
			needle: `probeAnswered(err)`,
			why:    "plutil's rule: any normal exit is an answer (#1524)",
			src: `package shapes
func probe() (string, error) {
	out, err := build(ctx, plist).Output()
	if probeAnswered(err) {
		return "", nil
	}
	return string(out), nil
}`,
		},
		{
			name: "classified_with_allowlist", want: 0, wantRuns: 1,
			needle: `probeAnswered(err, pgrepNoMatch)`,
			why:    "pgrep's and lsof's rule: only exit 1 is an answer",
			src: `package shapes
func probe() ([]int, error) {
	out, err := build(ctx, flag, pattern).Output()
	if probeAnswered(err, pgrepNoMatch) {
		return nil, nil
	}
	_ = out
	return nil, err
}`,
		},
		{
			name: "classified_via_named_wrapper", want: 0, wantRuns: 1,
			needle: `lsofProbeRan(err)`,
			why:    "a tool-specific wrapper counts; it IS the shared predicate with its allowlist bound",
			src: `package shapes
func probe() ([]int, bool) {
	out, err := exec.CommandContext(ctx, lsofPath, logPath).Output()
	if !lsofProbeRan(err) {
		return nil, false
	}
	return parse(out), true
}`,
		},
		{
			name: "propagated_wrapped", want: 0, wantRuns: 1,
			needle: `fmt.Errorf(`,
			why:    "readProcInfo and CWDOf do this: the two facts stay distinguishable at the caller instead of merging here",
			src: `package shapes
func probe(pid int) (string, error) {
	out, err := exec.CommandContext(ctx, psPath, "-p", pid).Output()
	if err != nil {
		return "", fmt.Errorf("ps pid %d: %w", pid, err)
	}
	return string(out), nil
}`,
		},
		{
			name: "propagated_bare", want: 0, wantRuns: 1,
			needle: `return nil, err`,
			why:    "returning the error unwrapped is propagation too",
			src: `package shapes
func probe() ([]byte, error) {
	out, err := exec.CommandContext(ctx, lsofPath).Output()
	if err != nil {
		return nil, err
	}
	return out, nil
}`,
		},
		{
			name: "if_init_propagated", want: 0, wantRuns: 1,
			needle: `return err`,
			why:    "the if-init spelling is not itself the defect — what it does with the error is",
			src: `package shapes
func probe() error {
	if err := exec.CommandContext(ctx, pgrepPath).Run(); err != nil {
		return err
	}
	return nil
}`,
		},
		{
			name: "builder_that_never_runs", want: 0, wantRuns: 0,
			needle: `*exec.Cmd`,
			why: "plutilBundleIDCmd's shape. Building a command is not running one, and the injected-builder idiom every " +
				"seam in this package uses depends on this staying clean",
			src: `package shapes
func plutilBundleIDCmd(ctx context.Context, plist string) *exec.Cmd {
	return exec.CommandContext(ctx, plutilPath, "-extract", "CFBundleIdentifier", "raw", "-o", "-", plist)
}`,
		},
		{
			name: "no_shellout_but_names_the_classifier", want: 0, wantRuns: 0,
			needle: `probeAnswered(err)`,
			why: "the run-site counter must count RUNS, not mentions of the predicate — otherwise the vacuity guard in the " +
				"live rule could be satisfied by a file that starts no process at all",
			src: `package shapes
func classify(err error) bool {
	return probeAnswered(err)
}`,
		},

		// ---- the measured blind spot, pinned as a known fact ----
		{
			name: "classifier_called_and_verdict_discarded", want: 0, wantRuns: 1,
			needle: `_ = lsofProbeRan(err)`,
			why: "MEASURED LIMITATION, not an oversight: this rule asks whether the error REACHES a classifier, not what is done " +
				"with the verdict. It was found by a mutation that kept the lsofProbeRan call while collapsing the return, and the " +
				"live rule stayed green. Discarding a verdict is #1533's and #1537's actual shape and is pinned one layer up, by the " +
				"fold tests in tty_darwin_test.go and kittywindow_darwin_test.go. Seeing it statically needs the verdict's dataflow, " +
				"which this syntactic scan does not have",
			src: `package shapes
func probe() (int, error) {
	out, err := exec.CommandContext(ctx, lsofPath, path).Output()
	_ = lsofProbeRan(err)
	if err != nil {
		return 0, nil
	}
	return parse(out), nil
}`,
		},
	}
}

// TestShelloutGuardCatchesEveryKnownShape runs the production detector over the
// corpus.
func TestShelloutGuardCatchesEveryKnownShape(t *testing.T) {
	seen := map[string]bool{}
	totalRuns, totalFindings := 0, 0

	for _, shape := range shelloutShapes() {
		if seen[shape.name] {
			t.Fatalf("duplicate corpus case %q: one would silently shadow the other", shape.name)
		}
		seen[shape.name] = true

		// The anti-rot guard: a case whose planted construct has been edited
		// away would read as a pass for every want:0 row.
		if shape.needle == "" {
			t.Fatalf("%s: every case must assert the construct it plants is present", shape.name)
		}
		if !strings.Contains(shape.src, shape.needle) {
			t.Fatalf("%s: source does not contain %q — the case is not testing what it says it tests", shape.name, shape.needle)
		}

		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, shape.name+".go", shape.src, 0)
		if err != nil {
			t.Fatalf("%s: corpus case does not parse — fix the case, not the scan: %v", shape.name, err)
		}

		findings, runs := scanShelloutClassification(fset, map[string]*ast.File{shape.name + ".go": file})
		totalRuns += runs
		totalFindings += len(findings)

		if len(findings) != shape.want {
			t.Errorf("%s: got %d findings, want %d — %s\n%v\n--- source ---\n%s",
				shape.name, len(findings), shape.want, shape.why, findings, shape.src)
		}
		if runs != shape.wantRuns {
			t.Errorf("%s: detector saw %d run sites, want %d — %s", shape.name, runs, shape.wantRuns, shape.why)
		}
	}

	// Two vacuity guards, in opposite directions. Without the first, a
	// detector that reports nothing satisfies every want:0 row; without the
	// second, one that reports everything satisfies every want:1 row.
	if totalRuns == 0 {
		t.Fatal("the corpus reported no run sites at all — the scan is inert, so every want:0 case above proves nothing")
	}
	if totalFindings == 0 {
		t.Fatal("the corpus reported no findings at all — the scan cannot fail, so every want:1 case above proves nothing")
	}
}
