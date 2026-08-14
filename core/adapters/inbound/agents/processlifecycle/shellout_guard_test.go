package processlifecycle

import (
	"io/fs"

	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// This file is the enforcement half of #1538, and it is the half that matters.
//
// Applying one shared predicate at today's eight shellouts is a refactor: it
// leaves the package correct and leaves the NEXT shellout free to classify its
// error however its author likes. Six instances of this family (#1485, #1492,
// #1513, #1524, #1533, #1537) were each found by a human reading code, one
// incident at a time, and #1538 exists because a seventh was already queued in
// a comment before the sixth was fixed. A guard that fails the build is the
// only thing here that survives the author who has not read any of those
// issues.
//
// The rule, stated as narrowly as it can be while still binding:
//
//	In this package, the error returned by running a child process must be
//	either CLASSIFIED by the shared predicate or PROPAGATED to the caller.
//	It may not be discarded, and it may not be collapsed into a zero value.
//
// Propagation counts because it is a legitimate second answer to the same
// question — readProcInfo and CWDOf both wrap and return, which keeps the two
// facts distinguishable at the caller rather than merging them here. What the
// rule forbids is the one spelling every issue in the family was filed about:
// `if err != nil { return <zero value> }`.
//
// Three things this rule deliberately does NOT do:
//
//   - It has no exemption list, for the reason architecture_test.go and
//     architecture_hookbody_test.go have none: a shellout that genuinely needs
//     different treatment amends the rule in a reviewable diff, where an
//     exemption list accumulates entries nobody re-reads.
//   - It says nothing about POLARITY. Whether a non-answer should fail open
//     (IsKnownInteractiveHost gates admission — #1513) or degrade an
//     enrichment (hostIdentity feeds a click target — #1492) is a property of
//     what the answer gates, and no static rule can see that. It only requires
//     that the fact reaches the call site at all.
//   - It cannot see a call site that classifies correctly and then throws the
//     verdict away, which is what #1533 and #1537's sixth instance actually
//     were. That is MEASURED, not assumed: a mutation that kept the
//     lsofProbeRan call in WriterOf while collapsing the return to (0, nil)
//     left this rule green. Seeing it statically needs the verdict's dataflow,
//     which a syntactic scan does not have, so it is pinned one layer up by the
//     fold tests in tty_darwin_test.go and kittywindow_darwin_test.go — and as
//     a want:0 row in the corpus, so the blind spot is a reviewable fact rather
//     than a surprise for whoever next trusts this rule.
//
// It scans SOURCE with go/parser rather than loading the package with
// go/packages, and that is load-bearing rather than a convenience: every one
// of the eight shellouts is behind `//go:build darwin`, so a type-aware load
// on a linux runner would find zero of them and pass — a gate whose absence
// reads as a pass, which is the exact defect this package keeps producing.
// Parsing the directory ignores build tags, so linux CI checks the darwin
// files too.

// shelloutRunMethods are the *exec.Cmd methods that actually START a child.
// Building a command is not running one — plutilBundleIDCmd returns an
// *exec.Cmd it never runs, and the injected-builder idiom this package uses
// for testability depends on that being fine.
//
// Matching is by method NAME, with no type information, so a future
// non-exec .Run() in this package would be reported. That is the intended
// direction: a false positive is a reviewable diff, a false negative is
// instance seven. Wait is deliberately absent — sync.WaitGroup.Wait would
// make the rule fire on code that starts no process at all, and nothing in
// this package calls (*exec.Cmd).Wait.
var shelloutRunMethods = map[string]bool{
	"Output":         true,
	"CombinedOutput": true,
	"Run":            true,
	"Start":          true,
}

// shelloutClassifiers are the calls that count as answering "did the child
// answer?". lsofProbeRan is here because it IS probeAnswered with lsof's
// allowlist bound (osutil_darwin.go); a third tool-specific wrapper would be
// added here in the same breath as being written.
var shelloutClassifiers = map[string]bool{
	"probeAnswered": true,
	"lsofProbeRan":  true,
}

// shelloutFinding is one unclassified shellout error.
type shelloutFinding struct {
	File   string
	Line   int
	Func   string
	Reason string
}

func (f shelloutFinding) String() string {
	return f.File + ":" + strconv.Itoa(f.Line) + " in " + f.Func + "(): " + f.Reason
}

// scanShelloutClassification is the production detector, shared by the live
// rule and by its corpus. It returns one finding per offending run site, plus
// the total number of run sites seen — the second is what makes a silent
// "no findings" distinguishable from a scan that looked at nothing.
func scanShelloutClassification(fset *token.FileSet, files map[string]*ast.File) (findings []shelloutFinding, runSites int) {
	for name, file := range files {
		base := filepath.Base(name)
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			f, n := scanFuncForShellouts(fset, base, fn)
			findings = append(findings, f...)
			runSites += n
		}
	}
	return findings, runSites
}

// scanFuncForShellouts applies the rule to one function body.
func scanFuncForShellouts(fset *token.FileSet, file string, fn *ast.FuncDecl) (findings []shelloutFinding, runSites int) {
	for _, run := range runCallsIn(fn.Body) {
		runSites++
		errName, bound := errIdentFor(fn.Body, run)
		line := fset.Position(run.Pos()).Line
		switch {
		case !bound:
			findings = append(findings, shelloutFinding{file, line, fn.Name.Name,
				"the error of a child process is discarded outright"})
		case errName == "_":
			findings = append(findings, shelloutFinding{file, line, fn.Name.Name,
				"the error of a child process is assigned to _"})
		case !classifiedOrPropagated(fn.Body, errName, run.Pos()):
			findings = append(findings, shelloutFinding{file, line, fn.Name.Name,
				"the error of a child process (" + errName + ") is neither passed to probeAnswered/lsofProbeRan nor returned — " +
					"a non-answer collapses into a zero value here (#1538)"})
		}
	}
	return findings, runSites
}

// runCallsIn returns every call in body that starts a child process.
func runCallsIn(body *ast.BlockStmt) []*ast.CallExpr {
	var out []*ast.CallExpr
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if sel, ok := call.Fun.(*ast.SelectorExpr); ok && shelloutRunMethods[sel.Sel.Name] && len(call.Args) == 0 {
			out = append(out, call)
		}
		return true
	})
	return out
}

// errIdentFor finds the name the run call's error is bound to. bound is false
// when the call is a bare statement, i.e. the error is thrown away.
func errIdentFor(body *ast.BlockStmt, run *ast.CallExpr) (name string, bound bool) {
	ast.Inspect(body, func(n ast.Node) bool {
		switch s := n.(type) {
		case *ast.AssignStmt:
			if len(s.Rhs) == 1 && containsNode(s.Rhs[0], run) && len(s.Lhs) > 0 {
				if id, ok := s.Lhs[len(s.Lhs)-1].(*ast.Ident); ok {
					name, bound = id.Name, true
				}
			}
		case *ast.ExprStmt:
			if s.X == ast.Expr(run) {
				name, bound = "", false
			}
		}
		return true
	})
	return name, bound
}

// classifiedOrPropagated reports whether errName is passed to a classifier or
// returned, at a position AFTER the run call.
//
// The position test is what stops one classified shellout from vouching for a
// second, unclassified one later in the same function — a function-wide "does
// the name appear anywhere" check would call that clean, and a function with
// two shellouts is exactly where a new one gets added.
func classifiedOrPropagated(body *ast.BlockStmt, errName string, after token.Pos) bool {
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		if found || n == nil || n.Pos() <= after {
			return !found
		}
		switch s := n.(type) {
		case *ast.CallExpr:
			if id, ok := s.Fun.(*ast.Ident); ok && shelloutClassifiers[id.Name] {
				for _, arg := range s.Args {
					if usesIdent(arg, errName) {
						found = true
					}
				}
			}
		case *ast.ReturnStmt:
			for _, r := range s.Results {
				if usesIdent(r, errName) {
					found = true
				}
			}
		}
		return !found
	})
	return found
}

func containsNode(root ast.Node, target ast.Node) bool {
	hit := false
	ast.Inspect(root, func(n ast.Node) bool {
		if n == target {
			hit = true
		}
		return !hit
	})
	return hit
}

func usesIdent(root ast.Node, name string) bool {
	hit := false
	ast.Inspect(root, func(n ast.Node) bool {
		if id, ok := n.(*ast.Ident); ok && id.Name == name {
			hit = true
		}
		return !hit
	})
	return hit
}

// parsePackageSources parses every non-test .go file in dir, ignoring build
// constraints on purpose — see the file header.
func parsePackageSources(t *testing.T, dir string) (*token.FileSet, map[string]*ast.File) {
	t.Helper()
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, dir, func(fi fs.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", dir, err)
	}
	files := map[string]*ast.File{}
	for _, pkg := range pkgs {
		for name, f := range pkg.Files {
			files[name] = f
		}
	}
	if len(files) == 0 {
		t.Fatalf("parsed no files in %s — the scan is looking in the wrong place, so its silence proves nothing", dir)
	}
	return fset, files
}

// TestEveryBoundedShelloutClassifiesItsError is the live rule.
func TestEveryBoundedShelloutClassifiesItsError(t *testing.T) {
	fset, files := parsePackageSources(t, ".")
	findings, runSites := scanShelloutClassification(fset, files)

	// Vacuity guard. A rule that reports no violations because it found no
	// shellouts is indistinguishable from a clean package, and this one runs
	// on a linux runner where every shellout is behind a build tag it does
	// not evaluate. The floor is deliberately below today's count so an
	// intentional removal does not fail here for the wrong reason, but a
	// scan that stops seeing the package fails loudly.
	const knownRunSites = 8
	if runSites < knownRunSites {
		t.Fatalf("found %d child-process run sites, expected at least %d: the scan is not looking where it thinks it is, so its silence proves nothing",
			runSites, knownRunSites)
	}

	for _, f := range findings {
		t.Errorf("%s", f)
	}
	if len(findings) > 0 {
		t.Log("Every issue in this family is one sentence: \"I could not look\" collapsed into \"I looked and there was nothing\" (#1485, #1492, #1513, #1524, #1533, #1537).")
		t.Log("Fix: classify with probeAnswered(err[, answeredExitCodes...]) and decide at THIS call site what a non-answer means, or return the error.")
	}
}

// TestEveryBoundedShelloutUsesTheNamedCeiling is #1538's second half. The 2s
// ceiling was an unnamed literal in eight places while three doc comments and
// a test assertion already treated the value as a contract ("half of 2s"), and
// a sibling package's own constant pointed here for the justification.
func TestEveryBoundedShelloutUsesTheNamedCeiling(t *testing.T) {
	fset, files := parsePackageSources(t, ".")

	seen := 0
	for name, file := range files {
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || len(call.Args) != 2 {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "WithTimeout" {
				return true
			}
			seen++
			if id, ok := call.Args[1].(*ast.Ident); ok && id.Name == "shelloutTimeout" {
				return true
			}
			t.Errorf("%s:%d: a bounded shellout's ceiling is spelled inline; use shelloutTimeout so the value stays one fact (#1538)",
				filepath.Base(name), fset.Position(call.Pos()).Line)
			return true
		})
	}
	if seen == 0 {
		t.Fatal("found no context.WithTimeout calls at all — the scan proves nothing")
	}
}
