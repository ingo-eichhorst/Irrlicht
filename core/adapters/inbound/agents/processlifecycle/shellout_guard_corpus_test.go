package processlifecycle

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
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
			name: "classified_via_the_qualified_shared_predicate", want: 0, wantRuns: 1,
			needle: `shellout.Answered(err)`,
			why:    "#1543 promoted the predicate to core/pkg/shellout; before shelloutPkgClassifiers existed this row reported a FALSE POSITIVE against correctly-classified code (measured against processTTYVia)",
			src: `package shapes
func probe() (string, bool) {
	out, err := exec.CommandContext(ctx, psPath, "-p", pid).Output()
	if !shellout.Answered(err) {
		return "", false
	}
	return string(out), true
}`,
		},
		{
			name: "classified_via_the_qualified_predicate_with_an_allowlist", want: 0, wantRuns: 1,
			needle: `shellout.Answered(err, lsofNothingToReport)`,
			why:    "the variadic form must be recognised too; matching is on the callee, not on the argument count",
			src: `package shapes
func probe() (int, bool) {
	out, err := exec.CommandContext(ctx, lsofPath, path).Output()
	if !shellout.Answered(err, lsofNothingToReport) {
		return 0, false
	}
	return parse(out), true
}`,
		},
		{
			name: "a_different_packages_Answered_does_not_vouch", want: 1, wantRuns: 1,
			needle: `notourpkg.Answered(err)`,
			why:    "matching is keyed on the package identifier: an unrelated Answered is not this repo's predicate, and accepting it would make the rule satisfiable by naming",
			src: `package shapes
func probe() (string, bool) {
	out, err := exec.CommandContext(ctx, psPath, "-p", pid).Output()
	if !notourpkg.Answered(err) {
		return "", false
	}
	return string(out), true
}`,
		},
		{
			name: "an_aliased_import_of_the_shared_predicate_is_NOT_recognised", want: 1, wantRuns: 1,
			needle: `sh.Answered(err)`,
			why:    "a DECLARED LIMIT, pinned so it is learned from a test rather than an incident: the scan has no type information, so `import sh \"irrlicht/core/pkg/shellout\"` reads as an unknown package. The failure is a false positive, which is the loud direction",
			src: `package shapes
func probe() (string, bool) {
	out, err := exec.CommandContext(ctx, psPath, "-p", pid).Output()
	if !sh.Answered(err) {
		return "", false
	}
	return string(out), true
}`,
		},
		{
			name: "a_method_named_Answered_on_a_receiver", want: 1, wantRuns: 1,
			needle: `probe.Answered(err)`,
			why:    "the SelectorExpr match must not be satisfied by any value with an Answered method; only the known package identifiers count",
			src: `package shapes
func run(probe classifier) (string, bool) {
	out, err := exec.CommandContext(ctx, psPath, "-p", pid).Output()
	if !probe.Answered(err) {
		return "", false
	}
	return string(out), true
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

		// ---- caught: found by review of PR #1545, each a hole in the rule's central promise ----
		{
			name: "rebound_err_launders_the_collapse", want: 1, wantRuns: 1,
			needle: `n, err := parseInt(out)`,
			why: "`err` is the most reused identifier in Go: a later `return n, err` returns a DIFFERENT error and says nothing " +
				"about the collapse above it. Without the upper window edge this reported findings=0 — the family's exact defect, read as clean",
			src: `package shapes
func probe() (int, error) {
	out, err := exec.CommandContext(ctx, lsofPath).Output()
	if err != nil {
		return 0, nil
	}
	n, err := parseInt(out)
	return n, err
}`,
		},
		{
			name: "package_level_var_iife", want: 1, wantRuns: 1,
			needle: `var cached = func() string {`,
			why: "kittenPath in this package is exactly this idiom — a package-level IIFE that probes for a CLI. file.Decls only " +
				"descends into FuncDecls, so before this the shellout was invisible AND runSites stayed 0, which meant the live " +
				"rule's vacuity floor could not notice either",
			src: `package shapes
var cached = func() string {
	out, err := exec.CommandContext(ctx, psPath, "-p", "1").Output()
	if err != nil {
		return ""
	}
	return string(out)
}()`,
		},
		{
			name: "closure_return_does_not_vouch_for_the_outer_collapse", want: 1, wantRuns: 1,
			needle: `return func() error {`,
			why: "a closure has its own returns; flattening a FuncDecl into one body would let an inner `return err` vouch for an " +
				"outer collapse, and vice versa",
			src: `package shapes
func probe() func() error {
	out, err := exec.CommandContext(ctx, lsofPath).Output()
	if err != nil {
		return nil
	}
	_ = out
	return func() error {
		return err
	}
}`,
		},

		// ---- clean: propagation spellings a name-based rule mis-reports ----
		{
			name: "returned_directly", want: 0, wantRuns: 1,
			needle: `return exec.CommandContext(ctx, lsofPath).Output()`,
			why: "the error goes straight to the caller with no variable in between. Reported as \"discarded outright\" before " +
				"review of #1545 — a build failure whose message actively misdescribed correct code",
			src: `package shapes
func probe() ([]byte, error) {
	return exec.CommandContext(ctx, lsofPath).Output()
}`,
		},
		{
			name: "var_declaration_binding", want: 0, wantRuns: 1,
			needle: `var out, err = `,
			why:    "`var out, err = cmd.Output()` binds exactly like `:=`; only AssignStmt was handled before",
			src: `package shapes
func probe() ([]byte, error) {
	var out, err = exec.CommandContext(ctx, lsofPath).Output()
	if err != nil {
		return nil, err
	}
	return out, nil
}`,
		},

		// ---- #1547: the same rule over the runProbe spelling -------------------
		//
		// Every row above stays exactly as it was — #1547 did not rewrite this
		// detector's method path, it ADDED a second run-site spelling — so the
		// predecessor's cases are carried as locks by continuing to run, not by
		// being transcribed. What these add is that a call to runProbe is graded
		// identically, which is the whole of the production package now: after
		// the routing rule there is one .Output() in the tree and it lives
		// inside runProbe.
		//
		// The pair that matters most is the last two. A runProbe call carries a
		// FuncLit ARGUMENT, which is a shape no row above has: the builder opens
		// a scope of its own, and a detector that either counted it as a second
		// run site or let its scope swallow the outer error binding would report
		// the eight production sites wrongly in opposite directions.
		{
			name: "runProbe_collapse", want: 1, wantRuns: 1,
			needle: `runProbe(ctx, build)`,
			why: "the family's collapse routed perfectly through the helper. This row is why runProbe returns (out, err) " +
				"rather than #1547's proposed (out, answered, err): folding the predicate in would NOT have made this " +
				"unrepresentable, so the classification rule had to survive and the helper had to compose with it",
			src: `package shapes
func probe(ctx context.Context, build shelloutCmd) string {
	out, err := runProbe(ctx, build)
	if err != nil {
		return ""
	}
	return string(out)
}`,
		},
		{
			name: "runProbe_blank_assigned_error", want: 1, wantRuns: 1,
			needle: `out, _ := runProbe(`,
			why:    "the error is gone before anything could classify it, whichever spelling started the child",
			src: `package shapes
func probe(ctx context.Context, build shelloutCmd) string {
	out, _ := runProbe(ctx, build)
	return string(out)
}`,
		},
		{
			name: "runProbe_as_bare_statement", want: 1, wantRuns: 1,
			needle: `	runProbe(ctx, build)` + "\n",
			why:    "no binding at all: there is no error variable to classify",
			src: `package shapes
func probe(ctx context.Context, build shelloutCmd) {
	runProbe(ctx, build)
}`,
		},
		{
			name: "runProbe_if_init_collapse", want: 1, wantRuns: 1,
			needle: `if _, err := runProbe(`,
			why:    "the if-init spelling over the helper, which a naive AssignStmt walk misses",
			src: `package shapes
func probe(ctx context.Context, build shelloutCmd) int {
	if _, err := runProbe(ctx, build); err != nil {
		return 0
	}
	return 1
}`,
		},
		{
			name: "runProbe_second_call_collapses", want: 1, wantRuns: 2,
			needle: `secondOut, err := runProbe(`,
			why: "the #1538 shape over the new spelling: a function that already classifies one shellout is exactly where " +
				"the next gets added, and both windows still have to hold",
			src: `package shapes
func probe(ctx context.Context, build shelloutCmd) (string, bool) {
	out, err := runProbe(ctx, build)
	if !probeAnswered(err) {
		return "", false
	}
	secondOut, err := runProbe(ctx, build)
	if err != nil {
		return string(out), true
	}
	return string(secondOut), true
}`,
		},
		{
			name: "runProbe_rebound_err_launders_the_collapse", want: 1, wantRuns: 1,
			needle: `n, err := parseInt(out)`,
			why:    "the upper window edge must apply to the helper spelling too: the trailing `return n, err` is a DIFFERENT error",
			src: `package shapes
func probe(ctx context.Context, build shelloutCmd) (int, error) {
	out, err := runProbe(ctx, build)
	if err != nil {
		return 0, nil
	}
	n, err := parseInt(out)
	return n, err
}`,
		},
		{
			name: "runProbe_classified", want: 0, wantRuns: 1,
			needle: `probeAnswered(err)`,
			why:    "processTTYVia's and kittyWindowIDForPIDVia's production shape after #1547",
			src: `package shapes
func probe(ctx context.Context, build shelloutCmd) (string, bool) {
	out, err := runProbe(ctx, build)
	if !probeAnswered(err) {
		return "", false
	}
	return string(out), true
}`,
		},
		{
			name: "runProbe_classified_via_named_wrapper", want: 0, wantRuns: 1,
			needle: `lsofProbeRan(err)`,
			why:    "writerOfVia's and herdrClientPIDs' production shape after #1547",
			src: `package shapes
func probe(ctx context.Context, build shelloutCmd) ([]int, bool) {
	out, err := runProbe(ctx, build)
	if !lsofProbeRan(err) {
		return nil, false
	}
	return parse(out), true
}`,
		},
		{
			name: "runProbe_propagated_wrapped", want: 0, wantRuns: 1,
			needle: `fmt.Errorf(`,
			why:    "readProcInfo's and CWDOf's production shape after #1547: the two facts stay distinguishable at the caller",
			src: `package shapes
func probe(ctx context.Context, build shelloutCmd, pid int) (string, error) {
	out, err := runProbe(ctx, build)
	if err != nil {
		return "", fmt.Errorf("ps pid %d: %w", pid, err)
	}
	return string(out), nil
}`,
		},
		{
			name: "runProbe_returned_directly", want: 0, wantRuns: 1,
			needle: `return runProbe(ctx, build)`,
			why:    "the error goes straight to the caller with no variable in between",
			src: `package shapes
func probe(ctx context.Context, build shelloutCmd) ([]byte, error) {
	return runProbe(ctx, build)
}`,
		},
		{
			name: "the_runProbe_helper_itself_is_clean", want: 0, wantRuns: 1,
			needle: `return build(ctx).Output()`,
			why: "the one .Output() the routing rule allows. It returns the error directly, so it is clean under this rule " +
				"too — and it must be counted as a run site, or the package's run-site floor would be one short of what it sees",
			src: `package shapes
func runProbe(ctx context.Context, build shelloutCmd) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, shelloutTimeout)
	defer cancel()
	return build(ctx).Output()
}`,
		},
		{
			name: "an_inline_builder_handed_to_runProbe_is_not_a_second_run", want: 0, wantRuns: 1,
			needle: `func(ctx context.Context) *exec.Cmd {`,
			why: "CWDOf, readProcInfo and herdrClientPIDs pass a FuncLit. It BUILDS a command and never runs one, so it adds " +
				"no run site — a detector that counted the literal would report every one of those three twice",
			src: `package shapes
func probe(pid int) (string, error) {
	out, err := runProbe(context.Background(), func(ctx context.Context) *exec.Cmd {
		return exec.CommandContext(ctx, psPath, "-p", strconv.Itoa(pid))
	})
	if err != nil {
		return "", fmt.Errorf("ps pid %d: %w", pid, err)
	}
	return string(out), nil
}`,
		},
		{
			name: "an_inline_builder_does_not_swallow_the_outer_collapse", want: 1, wantRuns: 1,
			needle: `return exec.CommandContext(ctx, psPath)`,
			why: "the other direction of the row above: the builder opens its own scope, and a walk that let that scope " +
				"stand in for the enclosing one would stop seeing the collapse three lines below it",
			src: `package shapes
func probe() string {
	out, err := runProbe(context.Background(), func(ctx context.Context) *exec.Cmd {
		return exec.CommandContext(ctx, psPath)
	})
	if err != nil {
		return ""
	}
	return string(out)
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

// routingShape is one spelling pinned to the verdict #1547's routing detector
// must return. It is a separate table from shelloutShape because the two rules
// count different things: the classification rule counts RUN SITES (a runProbe
// call is one), the routing rule counts runs INSIDE runProbe (a runProbe call
// is not one at all). Sharing one struct would mean one of the two numbers
// silently meaning nothing per row.
type routingShape struct {
	name       string
	src        string // a complete file in package shapes
	needle     string // a substring that MUST appear in src
	want       int    // findings the detector must report
	wantInside int    // run sites it must see INSIDE runProbe
	why        string
}

func runProbeRoutingShapes() []routingShape {
	return []routingShape{
		{
			name: "run_inside_runProbe_is_the_one_allowed_site", want: 0, wantInside: 1,
			needle: `return build(ctx).Output()`,
			why:    "the exemption itself — and it is what makes the rule's second arm non-vacuous",
			src: `package shapes
func runProbe(ctx context.Context, build shelloutCmd) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, shelloutTimeout)
	defer cancel()
	return build(ctx).Output()
}`,
		},
		{
			name: "run_outside_runProbe_is_a_finding", want: 1, wantInside: 0,
			needle: `.Output()`,
			why:    "the ninth shellout, written the way the eight were written before #1547: its own ceiling, copied",
			src: `package shapes
func probe(pid int) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), shelloutTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, psPath, "-p", pid).Output()
	if err != nil {
		return "", err
	}
	return string(out), nil
}`,
		},
		{
			name: "a_builder_that_runs_its_own_command_is_a_finding", want: 1, wantInside: 0,
			needle: `func(ctx context.Context) *exec.Cmd {`,
			why: "the smuggling route this package is uniquely exposed to: every site's builder IS a FuncLit, so a scope-confined " +
				"walk that stopped at closures would see nothing here. Measured as a want:1 row rather than argued for",
			src: `package shapes
func probe(ctx context.Context) ([]byte, error) {
	build := func(ctx context.Context) *exec.Cmd {
		cmd := exec.CommandContext(ctx, psPath)
		cmd.Run()
		return cmd
	}
	return runProbe(ctx, build)
}`,
		},
		{
			name: "a_runProbe_CALL_is_not_a_run", want: 0, wantInside: 0,
			needle: `runProbe(ctx, build)`,
			why: "the row that stops the rule reporting all eight production sites. A call to the helper is a call, not a child " +
				"— and the classification corpus above pins that the OTHER rule counts exactly this as a run site",
			src: `package shapes
func probe(ctx context.Context, build shelloutCmd) (string, bool) {
	out, err := runProbe(ctx, build)
	if !probeAnswered(err) {
		return "", false
	}
	return string(out), true
}`,
		},
		{
			name: "builder_that_never_runs", want: 0, wantInside: 0,
			needle: `*exec.Cmd`,
			why: "plutilBundleIDCmd's shape. Building a command is not running one, and the injected-builder idiom every seam " +
				"in this package uses depends on this staying clean under BOTH rules",
			src: `package shapes
func plutilBundleIDCmd(plist string) shelloutCmd {
	return func(ctx context.Context) *exec.Cmd {
		return exec.CommandContext(ctx, plutilPath, "-extract", "CFBundleIdentifier", "raw", "-o", "-", plist)
	}
}`,
		},
		{
			name: "package_level_var_iife_runs_a_child", want: 1, wantInside: 0,
			needle: `var cached = func() string {`,
			why: "kittenPath is exactly this idiom. file.Decls only descends into FuncDecls, so a shellout here is invisible " +
				"to a walk that forgets GenDecl — and it would leave the inside-count at 0 too, i.e. the vacuity arm could not notice",
			src: `package shapes
var cached = func() string {
	out, err := exec.CommandContext(ctx, psPath, "-p", "1").Output()
	if err != nil {
		return ""
	}
	return string(out)
}()`,
		},
		{
			name: "two_runs_inside_runProbe", want: 0, wantInside: 2,
			needle: `warm(ctx).Run()`,
			why: "the duplication the rule was written to delete, moved INSIDE the one function allowed to have it. No finding — " +
				"the live rule catches this on its second arm, and this row is what pins that the count reaches it",
			src: `package shapes
func runProbe(ctx context.Context, build shelloutCmd) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, shelloutTimeout)
	defer cancel()
	warm(ctx).Run()
	return build(ctx).Output()
}`,
		},
		{
			name: "Run_Start_and_CombinedOutput_spellings_outside", want: 3, wantInside: 0,
			needle: `.CombinedOutput()`,
			why:    "one finding per run METHOD, not one per function: a rule keyed on .Output() alone would miss three ways to start a child",
			src: `package shapes
func probe(ctx context.Context) {
	exec.CommandContext(ctx, kittenPath).Run()
	exec.CommandContext(ctx, kittenPath).Start()
	exec.CommandContext(ctx, kittenPath).CombinedOutput()
}`,
		},
		{
			name: "a_non_exec_Run_method_is_still_reported", want: 1, wantInside: 0,
			needle: `server.Run()`,
			why: "a DECLARED LIMIT, pinned so it is learned from a test rather than an incident: matching is by method NAME with " +
				"no type information (see the file header on why this parses source), so any zero-argument .Run() is reported. " +
				"A false positive is a reviewable diff; a false negative is the ninth shellout",
			src: `package shapes
func start(server *http.Server) {
	server.Run()
}`,
		},
	}
}

// TestRunProbeRoutingCatchesEveryKnownShape runs #1547's production routing
// detector over its corpus.
func TestRunProbeRoutingCatchesEveryKnownShape(t *testing.T) {
	seen := map[string]bool{}
	totalFindings, totalInside := 0, 0

	for _, shape := range runProbeRoutingShapes() {
		if seen[shape.name] {
			t.Fatalf("duplicate corpus case %q: one would silently shadow the other", shape.name)
		}
		seen[shape.name] = true

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

		findings, inside := scanShelloutRouting(fset, map[string]*ast.File{shape.name + ".go": file})
		totalFindings += len(findings)
		totalInside += inside

		if len(findings) != shape.want {
			t.Errorf("%s: got %d findings, want %d — %s\n%v\n--- source ---\n%s",
				shape.name, len(findings), shape.want, shape.why, findings, shape.src)
		}
		if inside != shape.wantInside {
			t.Errorf("%s: detector saw %d run sites inside runProbe, want %d — %s",
				shape.name, inside, shape.wantInside, shape.why)
		}
	}

	// Two vacuity guards, in opposite directions — the same pair the
	// classification corpus carries. Without the first, a detector that
	// reports everything satisfies every want:1 row; without the second, one
	// whose inside-count is stuck at zero satisfies every want:0 row AND would
	// leave the live rule's second arm measuring nothing.
	if totalFindings == 0 {
		t.Fatal("the corpus reported no findings at all — the routing scan cannot fail, so every want:0 case above proves nothing")
	}
	if totalInside == 0 {
		t.Fatal("the corpus never counted a run inside runProbe — the vacuity arm of the live rule is measuring nothing")
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
