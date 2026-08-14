// architecture_shellout_test.go is the third architecture rule in this
// directory, and like architecture_hookbody_test.go it is deliberately a
// separate file from architecture_test.go's layering table.
//
// The layering table asks a question about the IMPORT GRAPH. This one asks
// which EXPRESSIONS may appear anywhere under core/:
//
//	A child process started from core/ must be built with
//	exec.CommandContext, and the context it is given must not be a bare
//	context.Background()/TODO() written at the call site.
//
// WHY IT EXISTS. Six issues (#1485, #1492, #1513, #1524, #1533, #1537) fixed
// one sentence six times — "I could not look" collapsed into "I looked and
// there was nothing" — and #1538 gave processlifecycle a shared predicate plus
// a package-local AST guard so a seventh instance there would fail the build.
// That guard scans `parsePackageSources(t, ".")`: its own directory. #1543 was
// found in core/adapters/outbound/git, one hexagon layer away and completely
// invisible to it — seven plain exec.Command calls with no context and no
// timeout at all, inside a long-lived daemon.
//
// So the enforcement gap was not "processlifecycle might regress"; it was that
// nothing anywhere asked the question of the rest of core/. This rule is the
// repo-wide half, and it is deliberately the WEAKER, cheaper question:
// processlifecycle's guard reasons per-site about what each call does with its
// error, which is the strong property but needs per-package knowledge of the
// classifiers in scope. This one asks only whether a bound can reach the child
// at all — a question with the same answer in every package, and one an author
// cannot satisfy by accident.
//
// WHAT IT DOES NOT COVER, stated because a rule whose limits are implied reads
// as stronger than it is:
//
//   - It does not check that the context carries a DEADLINE. `ctx` derived
//     from a caller-supplied context satisfies this rule whether or not
//     anything ever called WithTimeout on it. Catching that needs dataflow the
//     syntactic scan does not have; what stops it in practice is that the
//     ceiling is then a named constant per package, which is a reviewable
//     absence rather than an invisible one.
//   - It says nothing about CLASSIFICATION — whether a non-answer is
//     distinguishable from an empty answer at the call site. That is
//     processlifecycle's guard, and core/pkg/shellout.Answered is the shared
//     predicate #1543 promoted so a second package could reach it.
//   - It matches on the SELECTOR's package identifier, so `import xexec
//     "os/exec"` followed by xexec.Command(…) is invisible to it — measured
//     against the detector, findings=0. So is a dot-import, and so is
//     os.StartProcess, which starts a child without going through os/exec at
//     all. All three are pinned as want:0 corpus rows, the same way the
//     sibling guard in processlifecycle pins its own aliased-import limit,
//     because a limit that is merely true is learned from an incident while a
//     limit that is pinned is learned from a test. Fixing them needs type
//     information, which this scan deliberately does not have (see the last
//     paragraph).
//   - It is scoped to core/. tools/ is a separate module of developer tooling
//     that is not a long-lived daemon.
//
// There is deliberately NO EXEMPTION LIST, for the reason architecture_test.go
// and architecture_hookbody_test.go have none: a call site that genuinely
// needs an unbounded child amends the rule in a reviewable diff, where an
// exemption list accumulates entries nobody re-reads.
//
// Like #1538's guard it parses SOURCE with go/parser rather than loading with
// go/packages, and that is load-bearing rather than a convenience: eight of the
// repo's shellouts are behind `//go:build darwin`, so a type-aware load on a
// linux runner would find zero of them and pass — a gate whose absence reads as
// a pass, which is the exact defect this family keeps producing.
package core_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// shelloutBoundFinding is one child process that cannot be bounded.
type shelloutBoundFinding struct {
	File   string
	Line   int
	Reason string
}

func (f shelloutBoundFinding) String() string {
	return f.File + ":" + strconv.Itoa(f.Line) + ": " + f.Reason
}

// scanShelloutBounds is the production detector, shared by the live rule and by
// its corpus (architecture_shellout_shapes_test.go).
//
// It returns one finding per offending site, plus the number of BOUNDABLE sites
// it saw. The second is what makes a silent "no findings" distinguishable from
// a scan that looked at nothing — the same floor #1538's guard carries, and the
// reason it is a count rather than a bool is that most of the tree could stop
// being scanned while one survivor kept a `> 0` check green.
func scanShelloutBounds(fset *token.FileSet, file *ast.File, name string) (findings []shelloutBoundFinding, bounded int) {
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		pkg, ok := sel.X.(*ast.Ident)
		if !ok || pkg.Name != "exec" {
			return true
		}
		line := fset.Position(call.Pos()).Line
		switch sel.Sel.Name {
		case "Command":
			findings = append(findings, shelloutBoundFinding{name, line,
				"exec.Command starts a child no context can reach: it has no ceiling, and " +
					"cmd.WaitDelay does nothing without one (measured — see " +
					"core/adapters/outbound/git's TestOrphanHoldingStdoutIsANonAnswer). " +
					"Use exec.CommandContext under a named per-package timeout (#1543)"})
		case "CommandContext":
			if len(call.Args) > 0 && isBareRootContext(call.Args[0]) {
				findings = append(findings, shelloutBoundFinding{name, line,
					"exec.CommandContext given a bare context.Background()/TODO() is exec.Command " +
						"wearing a context: nothing can ever cancel it. Derive a bounded context " +
						"first (#1543)"})
				return true
			}
			bounded++
		}
		return true
	})
	return findings, bounded
}

// isBareRootContext reports whether e is literally context.Background() or
// context.TODO() written at the call site.
//
// Only the LITERAL spelling: a variable that happens to hold one is invisible
// here, and that is the declared limit rather than an oversight — the point is
// that `exec.CommandContext(context.Background(), …)` reads as bounded to a
// reviewer skimming for "CommandContext" while being exactly as unbounded as
// exec.Command.
func isBareRootContext(e ast.Expr) bool {
	call, ok := e.(*ast.CallExpr)
	if !ok || len(call.Args) != 0 {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	if !ok || pkg.Name != "context" {
		return false
	}
	return sel.Sel.Name == "Background" || sel.Sel.Name == "TODO"
}

// parseCoreSources parses every non-test .go file under core/, ignoring build
// constraints on purpose — see the file header.
func parseCoreSources(t *testing.T) (*token.FileSet, map[string]*ast.File) {
	t.Helper()
	fset := token.NewFileSet()
	files := map[string]*ast.File{}
	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			// testdata holds deliberately odd fixtures, and bin/ holds build
			// output; neither is code this rule constrains.
			if d.Name() == "testdata" || d.Name() == "bin" || d.Name() == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			return perr
		}
		files[path] = f
		return nil
	})
	if err != nil {
		t.Fatalf("walk core/: %v", err)
	}
	if len(files) == 0 {
		t.Fatalf("parsed no files under core/ — the scan is looking in the wrong place, so its silence proves nothing")
	}
	return fset, files
}

// TestEveryChildProcessInCoreCanBeBounded is the live rule.
func TestEveryChildProcessInCoreCanBeBounded(t *testing.T) {
	fset, files := parseCoreSources(t)

	var findings []shelloutBoundFinding
	total := 0
	for name, f := range files {
		got, bounded := scanShelloutBounds(fset, f, name)
		findings = append(findings, got...)
		total += bounded
	}

	// Vacuity guard. A rule reporting no violations because it found no
	// shellouts is indistinguishable from a clean tree, and this one runs on a
	// linux runner where most of them are behind a build tag it does not
	// evaluate. The floor is deliberately below today's count (13, measured
	// across 7 files) so an intentional removal does not fail here for the
	// wrong reason, but a scan that stops seeing the tree fails loudly.
	const knownBoundedSites = 10
	if total < knownBoundedSites {
		t.Fatalf("found %d boundable child-process sites under core/, expected at least %d: "+
			"the scan is not looking where it thinks it is, so its silence proves nothing",
			total, knownBoundedSites)
	}

	for _, f := range findings {
		t.Errorf("%s", f)
	}
	if len(findings) > 0 {
		t.Log("A child process this daemon cannot bound is one it can wait on forever: #1543's git adapter " +
			"had seven, on directories that can be network mounts or repos holding a lock.")
		t.Log("Fix: exec.CommandContext under a per-package named ceiling, and classify the result with " +
			"core/pkg/shellout.Answered so a non-answer stays distinguishable from an empty answer.")
	}
}
