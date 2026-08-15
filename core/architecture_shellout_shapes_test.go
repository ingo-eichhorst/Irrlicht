package core_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// This is the committed mutation evidence for architecture_shellout_test.go's
// rule, in the shape architecture_hookbody_shapes_test.go uses: one row per
// spelling, pinned to the verdict the detector must return.
//
// It is committed rather than described because a mutation recorded only in a
// merged PR body is re-run by nothing, and a detector that silently stops
// discriminating looks exactly like a clean tree (AGENTS.md, Testing).
//
// The `want: 0` rows carry as much of the value as the `want: 1` rows. A
// detector that flags everything and one that flags correctly are
// indistinguishable without cases that must stay SILENT — and three of the
// silent rows below are false positives a text-based `grep exec.Command` rule
// produces, which is the rule anyone would reach for first.
type shelloutShape struct {
	name   string
	src    string // a complete file in package shapes
	needle string // a substring that MUST still appear in src
	// sibling is a SECOND file of the same package, empty for most rows. It
	// exists because #1559's shape spans two files — processlifecycle declares
	// noAggregateBudget in osutil.go and every shellout that could be handed it
	// lives in osutil_darwin.go — so a corpus that could only express one file
	// per row would have graded the detector on the one arrangement the defect
	// does not take.
	sibling       string
	siblingNeedle string // asserted the same way as needle when sibling is set
	want          int    // findings the detector must report
	wantBounded   int    // boundable sites it must SEE (0 findings from 0 sites proves nothing)
	why           string
}

func shelloutShapes() []shelloutShape {
	return []shelloutShape{
		// ---- caught ----
		{
			name:   "plain_exec_command",
			needle: `exec.Command(`,
			want:   1, wantBounded: 0,
			why: "#1543's exact shape: seven of these in the git adapter, no context, no ceiling.",
			src: `package shapes

import "os/exec"

func f() ([]byte, error) {
	cmd := exec.Command("git", "rev-parse", "HEAD")
	return cmd.Output()
}
`,
		},
		{
			name:   "exec_command_chained_without_a_variable",
			needle: `exec.Command("git", "status").Output()`,
			want:   1, wantBounded: 0,
			why: "The call is an operand rather than an assignment RHS; a rule keyed on assignment misses it.",
			src: `package shapes

import "os/exec"

func f() ([]byte, error) { return exec.Command("git", "status").Output() }
`,
		},
		{
			name:   "exec_command_inside_a_closure",
			needle: `exec.Command(`,
			want:   1, wantBounded: 0,
			why: "A nested function literal is still core/ source; #1538's guard had to grow closure walking for the same reason.",
			src: `package shapes

import "os/exec"

var build = func() error { return exec.Command("git", "gc").Run() }
`,
		},
		{
			name:   "commandcontext_with_bare_background",
			needle: `context.Background()`,
			want:   1, wantBounded: 0,
			why: "exec.Command wearing a context: it reads as bounded to anyone skimming for CommandContext, and nothing can ever cancel it.",
			src: `package shapes

import (
	"context"
	"os/exec"
)

func f() error { return exec.CommandContext(context.Background(), "git", "fsck").Run() }
`,
		},
		{
			name:   "commandcontext_with_bare_todo",
			needle: `context.TODO()`,
			want:   1, wantBounded: 0,
			why: "context.TODO() is the same hole as Background() and is what a half-finished refactor leaves behind.",
			src: `package shapes

import (
	"context"
	"os/exec"
)

func f() error { return exec.CommandContext(context.TODO(), "git", "fsck").Run() }
`,
		},
		{
			name:   "commandcontext_with_a_package_local_root_helper",
			needle: `exec.CommandContext(noAggregateBudget()`,
			want:   1, wantBounded: 0,
			why: "#1559: the same site as the two rows above with a name in front of it. Naming an absent AGGREGATE is fine; handing that name to a CHILD is the thing this rule forbids, and it read as bounded to both the rule and a reviewer.",
			src: `package shapes

import (
	"context"
	"os/exec"
)

func noAggregateBudget() context.Context { return context.Background() }

func f() error { return exec.CommandContext(noAggregateBudget(), "git", "fsck").Run() }
`,
		},
		{
			name:          "a_root_helper_declared_in_ANOTHER_FILE_of_the_same_package",
			needle:        `exec.CommandContext(noAggregateBudget()`,
			siblingNeedle: `return context.Background()`,
			want:          1, wantBounded: 0,
			why: "The arrangement #1559 actually occurs in, and the one a file-local collection would miss: processlifecycle declares the helper in osutil.go and every shellout that could take it is in osutil_darwin.go.",
			src: `package shapes

import "os/exec"

func f() error { return exec.CommandContext(noAggregateBudget(), "git", "fsck").Run() }
`,
			sibling: `package shapes

import "context"

func noAggregateBudget() context.Context { return context.Background() }
`,
		},
		{
			name:   "a_helper_that_returns_another_root_helper",
			needle: `func alias() context.Context { return root() }`,
			want:   1, wantBounded: 0,
			why: "One indirection is the same unbounded child with one more hop; the collection is a fixpoint so a single rename-and-wrap does not defeat it.",
			src: `package shapes

import (
	"context"
	"os/exec"
)

func root() context.Context { return context.Background() }

func alias() context.Context { return root() }

func f() error { return exec.CommandContext(alias(), "git", "fsck").Run() }
`,
		},
		{
			name:   "a_root_helper_that_takes_an_argument",
			needle: `func rootFor(pid int) context.Context`,
			want:   1, wantBounded: 0,
			why: "Pins a deliberate asymmetry: the LITERAL form must take no arguments (context.Background does not), but a helper is free to take a pid and return the root anyway, and it is no more bounded for that.",
			src: `package shapes

import (
	"context"
	"os/exec"
)

func rootFor(pid int) context.Context { return context.Background() }

func f(pid int) error { return exec.CommandContext(rootFor(pid), "git", "fsck").Run() }
`,
		},
		{
			name:   "two_sites_one_bounded_one_not",
			needle: `exec.CommandContext(ctx`,
			want:   1, wantBounded: 1,
			why: "A function with one correct shellout is exactly where a second, unbounded one gets added; the correct one must not vouch for it.",
			src: `package shapes

import (
	"context"
	"os/exec"
)

func f(ctx context.Context) {
	_ = exec.CommandContext(ctx, "git", "status").Run()
	_ = exec.Command("git", "gc").Run()
}
`,
		},

		// ---- clean ----
		{
			name:   "commandcontext_with_a_derived_context",
			needle: `exec.CommandContext(ctx`,
			want:   0, wantBounded: 1,
			why: "The vacuity guard for every row above: the sanctioned spelling must be silent, or a detector that flags everything would satisfy them all.",
			src: `package shapes

import (
	"context"
	"os/exec"
	"time"
)

func f() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, "git", "status").Run()
}
`,
		},
		{
			name:   "commandcontext_with_a_caller_supplied_context",
			needle: `func f(ctx context.Context)`,
			want:   0, wantBounded: 1,
			why: "A DECLARED LIMIT, pinned so it is learned from a test rather than an incident: this rule cannot see whether ctx carries a deadline, only that one can reach the child.",
			src: `package shapes

import (
	"context"
	"os/exec"
)

func f(ctx context.Context) error { return exec.CommandContext(ctx, "git", "status").Run() }
`,
		},
		{
			name:   "a_helper_that_returns_its_callers_context",
			needle: `func fromParent(parent context.Context) context.Context { return parent }`,
			want:   0, wantBounded: 1,
			why: "#1559's anti-greedy row, and the sharpest one: identical in SHAPE to the caught helper rows — one statement, one result, an Ident call in argument 0 — differing only in what the body returns. A collection keyed on the shape rather than the body flags it.",
			src: `package shapes

import (
	"context"
	"os/exec"
)

func fromParent(parent context.Context) context.Context { return parent }

func f(parent context.Context) error {
	return exec.CommandContext(fromParent(parent), "git", "status").Run()
}
`,
		},
		{
			name:   "a_helper_that_derives_from_its_callers_context",
			needle: `return context.WithValue(parent`,
			want:   0, wantBounded: 1,
			why: "The other half of the same guard: a body that IS a context.* call, so a collection keyed on the package identifier rather than on Background/TODO flags it. Also the shape a per-package context decorator really takes.",
			src: `package shapes

import (
	"context"
	"os/exec"
)

type key struct{}

func tagged(parent context.Context) context.Context { return context.WithValue(parent, key{}, "git") }

func f(parent context.Context) error {
	return exec.CommandContext(tagged(parent), "git", "status").Run()
}
`,
		},
		{
			name:   "a_METHOD_returning_a_root_context_is_NOT_caught",
			needle: `func (reads) noBudget() context.Context { return context.Background() }`,
			want:   0, wantBounded: 1,
			why: "a DECLARED LIMIT (#1559): a receiver makes the call site r.noBudget() a selector, and knowing what r is needs the type information this scan deliberately does not have — the same wall the aliased-import row hits.",
			src: `package shapes

import (
	"context"
	"os/exec"
)

type reads struct{}

func (reads) noBudget() context.Context { return context.Background() }

func f(r reads) error { return exec.CommandContext(r.noBudget(), "git", "gc").Run() }
`,
		},
		{
			name:   "a_root_helper_in_ANOTHER_PACKAGE_is_NOT_caught",
			needle: `other.NoBudget()`,
			want:   0, wantBounded: 1,
			why: "a DECLARED LIMIT (#1559): the collection is per package, so a cross-package helper is a selector whose target is unresolvable without types. The scope is where the shape occurs — a root context is named for one package's reasons.",
			src: `package shapes

import (
	"os/exec"

	"example.com/other"
)

func f() error { return exec.CommandContext(other.NoBudget(), "git", "gc").Run() }
`,
		},
		{
			name:   "a_root_helper_with_more_than_one_statement_is_NOT_caught",
			needle: `c := context.Background()`,
			want:   0, wantBounded: 1,
			why: "a DECLARED LIMIT (#1559): resolving this needs intra-function dataflow, which is the same thing the variable row below needs and the reason both stop here rather than at a different place.",
			src: `package shapes

import (
	"context"
	"os/exec"
)

func noBudget() context.Context {
	c := context.Background()
	return c
}

func f() error { return exec.CommandContext(noBudget(), "git", "gc").Run() }
`,
		},
		{
			name:   "a_VARIABLE_holding_a_root_context_is_NOT_caught",
			needle: `ctx := context.Background()`,
			want:   0, wantBounded: 1,
			why: "a DECLARED LIMIT stated on isBareRootContext since #1543 and pinned only by #1559 — the case where a limit was merely true for two issues. It is the cheapest evasion of the whole rule, so it is the one most worth learning from a test rather than an incident.",
			src: `package shapes

import (
	"context"
	"os/exec"
)

func f() error {
	ctx := context.Background()
	return exec.CommandContext(ctx, "git", "gc").Run()
}
`,
		},
		{
			name:   "a_different_package_named_exec",
			needle: `myexec.Command(`,
			want:   0, wantBounded: 0,
			why: "A false positive a text grep for 'exec.Command(' produces. Matching requires the selector's own package identifier to be exec.",
			src: `package shapes

import myexec "example.com/notos/exec"

func f() { myexec.Command("anything") }
`,
		},
		{
			name:   "a_method_named_Command_on_a_local_value",
			needle: `runner.Command(`,
			want:   0, wantBounded: 0,
			why: "Second text-grep false positive: a receiver that happens to be spelled with a Command method starts no child process.",
			src: `package shapes

type shell struct{}

func (shell) Command(string) error { return nil }

func f(runner shell) error { return runner.Command("git") }
`,
		},
		{
			name:   "the_word_exec_Command_in_a_comment_or_string",
			needle: `"never use exec.Command here"`,
			want:   0, wantBounded: 0,
			why: "Third text-grep false positive, and the one most likely to appear for real — a doc comment naming the banned spelling, exactly as this repo's own comments do.",
			src: `package shapes

// f must never use exec.Command; see #1543.
const advice = "never use exec.Command here"
`,
		},
		{
			name:   "exec_Cmd_built_as_a_struct_literal",
			needle: `&exec.Cmd{`,
			want:   1, wantBounded: 0,
			why: "#1551 QA, S1: the natural thing to reach for once exec.Command is banned, and the WORST of the three — exec.Cmd has no exported context field, so this is unbounded by construction rather than by omission.",
			src: `package shapes

import "os/exec"

func f() error {
	cmd := &exec.Cmd{Path: "/usr/bin/git", Args: []string{"git", "gc"}}
	return cmd.Run()
}
`,
		},
		{
			name:   "a_package_level_var_holding_exec_Command_is_NOT_caught",
			needle: `var run = exec.Command`,
			want:   0, wantBounded: 0,
			why: "a DECLARED LIMIT (#1551 QA, S1), measured: the rule matches the CALLEE, and `run(...)` names an Ident whose value is only knowable with type information.",
			src: `package shapes

import "os/exec"

var run = exec.Command

func f() error { return run("git", "gc").Run() }
`,
		},
		{
			name:   "a_child_started_through_a_value_built_elsewhere_is_NOT_caught",
			needle: `builder.build("git", "gc").Run()`,
			want:   0, wantBounded: 0,
			why: "a DECLARED LIMIT (#1551 QA, S1): with no exec.* selector in the file there is nothing syntactic to match. Note the sibling shape WITH the builder in the same file IS caught, via the exec.Command inside it — the gap is cross-file, not the method value itself.",
			src: `package shapes

type cmdBuilder interface{ build(string, ...string) interface{ Run() error } }

func f(builder cmdBuilder) error { return builder.build("git", "gc").Run() }
`,
		},
		{
			name:   "an_aliased_import_of_os_exec_is_NOT_caught",
			needle: `xexec.Command(`,
			want:   0, wantBounded: 0,
			why: "a DECLARED LIMIT (#1551 review finding 9), pinned so it is learned from a test rather than an incident: matching is on the selector's package identifier, and this scan has no type information. The sibling guard in processlifecycle pins the same limit for the same reason.",
			src: `package shapes

import xexec "os/exec"

func f() error { return xexec.Command("git", "gc").Run() }
`,
		},
		{
			name:   "os_StartProcess_is_NOT_caught",
			needle: `os.StartProcess(`,
			want:   0, wantBounded: 0,
			why: "a DECLARED LIMIT: a child can be started without os/exec at all. Nothing in core/ does, and widening the rule to os.StartProcess would need the same type information the alias case does.",
			src: `package shapes

import "os"

func f() (*os.Process, error) {
	return os.StartProcess("/usr/bin/git", []string{"git", "gc"}, &os.ProcAttr{})
}
`,
		},
		{
			name:   "a_bare_Command_call_with_no_package_qualifier",
			needle: `= Command(`,
			want:   0, wantBounded: 0,
			why: "A DECLARED LIMIT: a dot-import of os/exec would make this a real child process and the rule would miss it. Nothing in core/ dot-imports, and pinning it here means the gap is a known fact rather than a surprise.",
			src: `package shapes

func Command(string) error { return nil }

var err = Command("git")
`,
		},
	}
}

// TestShelloutBoundScanCatchesEveryKnownShape drives the PRODUCTION detector
// (scanShelloutBounds), not a copy of it — a corpus grading a reimplementation
// grades the wrong thing.
func TestShelloutBoundScanCatchesEveryKnownShape(t *testing.T) {
	seen := map[string]bool{}
	totalFindings, totalBounded := 0, 0

	for _, sh := range shelloutShapes() {
		if seen[sh.name] {
			t.Fatalf("duplicate shape name %q — one row is silently shadowing another", sh.name)
		}
		seen[sh.name] = true

		t.Run(sh.name, func(t *testing.T) {
			// Anti-rot: a row whose source stopped containing the construct it
			// plants would pass on a detector that reports nothing, which is
			// the "verification mechanism that cannot run" failure inside the
			// mechanism meant to catch it (AGENTS.md). Shared with the
			// hookbody corpus, which also refuses a row declaring NO needle —
			// the case this file's first draft let through silently.
			assertPlantedShapePresent(t, sh.name, sh.src, sh.needle)
			if sh.sibling != "" {
				// A two-file row is two plantings, and the second is the half
				// that carries the case — a sibling that stopped declaring the
				// helper would leave the row grading the single-file shape
				// under a two-file name.
				assertPlantedShapePresent(t, sh.name+" (sibling)", sh.sibling, sh.siblingNeedle)
			}

			fset := token.NewFileSet()
			pkg := map[string]*ast.File{}
			parse := func(file, src string) {
				f, err := parser.ParseFile(fset, file, src, parser.ParseComments)
				if err != nil {
					t.Fatalf("parse %s: %v", file, err)
				}
				pkg[file] = f
			}
			parse(sh.name+".go", sh.src)
			if sh.sibling != "" {
				parse(sh.name+"_sibling.go", sh.sibling)
			}

			// The production pipeline in the order the live rule runs it:
			// collect the PACKAGE's root-context helpers first, then scan each
			// file against that one set. Doing it per file here would grade a
			// weaker detector than the one that ships.
			helpers := rootContextHelpers(pkg)
			var findings []shelloutBoundFinding
			bounded := 0
			for file, f := range pkg {
				got, b := scanShelloutBounds(fset, f, file, helpers)
				findings = append(findings, got...)
				bounded += b
			}

			if len(findings) != sh.want {
				t.Errorf("got %d finding(s), want %d — %s\nfindings: %v", len(findings), sh.want, sh.why, findings)
			}
			if bounded != sh.wantBounded {
				t.Errorf("saw %d boundable site(s), want %d — %s", bounded, sh.wantBounded, sh.why)
			}
			totalFindings += len(findings)
			totalBounded += bounded
		})
	}

	// Two opposite vacuity guards over the corpus as a whole, because each
	// alone is satisfied by a detector stuck in one direction.
	if totalFindings == 0 {
		t.Fatal("the whole corpus produced no findings — the detector reports nothing, so every want:0 row above is meaningless")
	}
	if totalBounded == 0 {
		t.Fatal("the whole corpus saw no boundable sites — the live rule's floor could not notice a scan that stopped looking")
	}
}
