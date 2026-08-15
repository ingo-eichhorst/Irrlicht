package core_test

import (
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
	name        string
	src         string // a complete file in package shapes
	needle      string // a substring that MUST still appear in src
	want        int    // findings the detector must report
	wantBounded int    // boundable sites it must SEE (0 findings from 0 sites proves nothing)
	why         string
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

			fset := token.NewFileSet()
			f, err := parser.ParseFile(fset, sh.name+".go", sh.src, parser.ParseComments)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			findings, bounded := scanShelloutBounds(fset, f, sh.name+".go")

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
