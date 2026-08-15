package processlifecycle

import (
	"io/fs"

	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// This file is the enforcement half of #1538 and #1547, and it is the half
// that matters. It holds two rules over the same eight shellouts, and they are
// stated as two because neither implies the other:
//
//   - ROUTING (#1547, TestEveryChildProcessRunsThroughRunProbe): no child is
//     started outside runProbe. This makes the bounded-shellout SHAPE — derive
//     the ceiling from the CALLER's context, name it, cancel it —
//     unrepresentable at a second site rather than merely correct at today's
//     eight.
//   - CLASSIFICATION (#1538, below): the error a run site produces must be
//     classified or propagated. runProbe does NOT make this one redundant, and
//     that is the finding that shaped #1547's implementation: a caller can
//     route perfectly through runProbe and still write
//     `if err != nil { return "" }`, which is the family's exact collapse. So
//     the helper was built to COMPOSE with this rule — it returns (out, err)
//     and leaves the verdict at the call site — rather than to replace it,
//     which the issue had assumed it would.
//
// The rest of this header is the classification rule.
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

// runProbeName is the one helper that may start a child in this package
// (#1547). Two rules read it and they ask opposite questions of the same name:
//
//   - TestEveryChildProcessRunsThroughRunProbe requires every run METHOD to be
//     inside it, so the ceiling stays one copy.
//   - The classification rule below treats a CALL to it as a run site of its
//     own, so what its eight callers do with the error is graded exactly as
//     what a direct .Output() caller's was. Without that second half the
//     refactor would have made this guard blind: run sites went from 8 to 1 the
//     moment the sites were routed, which is measured — the vacuity floor
//     fired ("found 1 child-process run sites, expected at least 8") rather
//     than the rule quietly passing, which is the whole reason that floor is a
//     count and not a bool.
const runProbeName = "runProbe"

// shelloutClassifiers are the UNQUALIFIED calls that count as answering "did
// the child answer?". lsofProbeRan is here because it IS probeAnswered with
// lsof's allowlist bound (osutil_darwin.go), and #1501's tmuxListedClients is
// the third: it is probeAnswered with an EMPTY allowlist and a further exit-0
// requirement, because tmux has no "nothing to report" status at all. A fourth
// tool-specific wrapper is added here in the same breath as being written —
// that instruction has now been followed once, which is the evidence it works.
var shelloutClassifiers = map[string]bool{
	"probeAnswered":     true,
	"lsofProbeRan":      true,
	"tmuxListedClients": true,
}

// shelloutPkgClassifiers are the QUALIFIED spellings, keyed by the source text
// "<package identifier>.<function>".
//
// #1543 promoted probeAnswered's implementation to core/pkg/shellout.Answered
// so an outbound adapter one hexagon layer away could reach it. This package
// still calls the local probeAnswered — which now forwards — so nothing here
// changed spelling, and that is exactly why this entry is needed rather than
// obviously needed: the moment a site here writes shellout.Answered(err)
// directly, which is a completely reasonable thing to write, an *ast.Ident
// match stops recognising it.
//
// MEASURED before this entry existed, by rewriting processTTYVia's classifier
// call to the qualified spelling: the guard reported
//
//	osutil_darwin.go:67 in processTTYVia(): the error of a child process (err)
//	is neither passed to probeAnswered/lsofProbeRan nor returned
//
// i.e. a FALSE POSITIVE against correctly-classified code, not a false
// negative. That is the loud direction and the guard's own doc argues for
// preferring it — but a rule that fails the build on the sanctioned fix
// teaches the next author to spell it the way the rule expects rather than the
// way that is right, so it is fixed rather than left as a known quirk.
//
// Matching is on the package IDENTIFIER, not an import path: this scan has no
// type information (see the file header on why it parses source). An aliased
// import of core/pkg/shellout would therefore not be recognised, which is the
// same class of limit the run-method table already accepts and is pinned as a
// corpus row rather than left to be discovered.
var shelloutPkgClassifiers = map[string]bool{
	"shellout.Answered": true,
}

// isShelloutClassifier reports whether fun names a call that classifies a
// child-process error, in either spelling.
func isShelloutClassifier(fun ast.Expr) bool {
	switch f := fun.(type) {
	case *ast.Ident:
		return shelloutClassifiers[f.Name]
	case *ast.SelectorExpr:
		pkg, ok := f.X.(*ast.Ident)
		if !ok {
			return false
		}
		return shelloutPkgClassifiers[pkg.Name+"."+f.Sel.Name]
	}
	return false
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
			switch d := decl.(type) {
			case *ast.FuncDecl:
				if d.Body == nil {
					continue
				}
				f, n := scanScope(fset, base, d.Name.Name, d.Body)
				findings, runSites = append(findings, f...), runSites+n
			case *ast.GenDecl:
				// A package-level `var x = func() T { … }()` is a function body
				// that file.Decls alone never reaches, and this package already
				// uses that idiom (kittenPath resolves a CLI in one). Before it
				// was walked, a shellout there was invisible AND left runSites
				// at zero — so the vacuity floor below could not notice either.
				f, n := scanScope(fset, base, "var initialiser", d)
				findings, runSites = append(findings, f...), runSites+n
			}
		}
	}
	return findings, runSites
}

// scanScope applies the rule to ONE scope — a function body, or a declaration's
// initialiser expressions — then recurses into any nested function literals as
// scopes of their own.
//
// body is ast.Node rather than *ast.BlockStmt so a GenDecl can be handed in
// directly. The narrower type bought nothing (every use forwards to
// inspectScope/splitScope, which take ast.Node) and cost a synthetic
// BlockStmt-wrapping-a-CompositeLit that no Go parser produces.
//
// The nesting matters: a closure has its own returns, so a `return err` in the
// enclosing function does not propagate a closure's error and vice versa.
// Treating a whole FuncDecl as one flat body would let either vouch for the
// other.
func scanScope(fset *token.FileSet, file, name string, body ast.Node) (findings []shelloutFinding, runSites int) {
	runs, lits := splitScope(body)
	for _, run := range runs {
		runSites++
		errName, kind := errBindingFor(body, run)
		line := fset.Position(run.Pos()).Line
		switch kind {
		case errReturnedDirectly:
			// `return cmd.Output()` — the error goes straight to the caller.
		case errDiscarded:
			findings = append(findings, shelloutFinding{file, line, name,
				"the error of a child process is discarded outright"})
		case errBlank:
			findings = append(findings, shelloutFinding{file, line, name,
				"the error of a child process is assigned to _"})
		default:
			if !classifiedOrPropagated(body, errName, run.Pos()) {
				findings = append(findings, shelloutFinding{file, line, name,
					"the error of a child process (" + errName + ") is neither passed to probeAnswered/lsofProbeRan/shellout.Answered nor returned — " +
						"a non-answer collapses into a zero value here (#1538)"})
			}
		}
	}
	for _, lit := range lits {
		f, n := scanScope(fset, file, name+".func", lit.Body)
		findings, runSites = append(findings, f...), runSites+n
	}
	return findings, runSites
}

// splitScope returns the run calls belonging to THIS scope and the function
// literals that open their own. It stops descending at a nested literal so the
// two sets never overlap.
func splitScope(root ast.Node) (runs []*ast.CallExpr, lits []*ast.FuncLit) {
	ast.Inspect(root, func(n ast.Node) bool {
		if n == nil {
			return false
		}
		if lit, ok := n.(*ast.FuncLit); ok && ast.Node(lit) != root {
			lits = append(lits, lit)
			return false
		}
		if call, ok := n.(*ast.CallExpr); ok && (isRunMethodCall(call) || isRunProbeCall(call)) {
			runs = append(runs, call)
		}
		return true
	})
	return runs, lits
}

// isRunMethodCall reports whether call is `<something>.Output()` and friends —
// an expression that actually STARTS a child. Building a command is not
// running one (plutilBundleIDCmd returns an *exec.Cmd it never runs), and the
// zero-argument check is what separates the two.
func isRunMethodCall(call *ast.CallExpr) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	return ok && shelloutRunMethods[sel.Sel.Name] && len(call.Args) == 0
}

// isRunProbeCall reports whether call invokes the package's one shellout
// helper. It is matched as a bare *ast.Ident because runProbe is unexported
// and package-local: a qualified spelling would be a different function.
func isRunProbeCall(call *ast.CallExpr) bool {
	id, ok := call.Fun.(*ast.Ident)
	return ok && id.Name == runProbeName
}

// errBinding is how a run call's error reaches (or fails to reach) a name.
type errBinding int

const (
	errNamed            errBinding = iota // out, err := cmd.Output()
	errBlank                              // out, _  := cmd.Output()
	errDiscarded                          // cmd.Run() as a bare statement
	errReturnedDirectly                   // return cmd.Output()
)

// errBindingFor finds how the run call's error is bound.
func errBindingFor(body ast.Node, run *ast.CallExpr) (name string, kind errBinding) {
	name, kind = "", errDiscarded
	inspectScope(body, func(n ast.Node) bool {
		switch s := n.(type) {
		case *ast.AssignStmt:
			if len(s.Rhs) == 1 && containsNode(s.Rhs[0], run) && len(s.Lhs) > 0 {
				name, kind = lhsName(s.Lhs[len(s.Lhs)-1])
			}
		case *ast.ValueSpec:
			// `var out, err = cmd.Output()` binds exactly like :=.
			if len(s.Values) == 1 && containsNode(s.Values[0], run) && len(s.Names) > 0 {
				name, kind = lhsName(s.Names[len(s.Names)-1])
			}
		case *ast.ReturnStmt:
			for _, r := range s.Results {
				if containsNode(r, run) {
					name, kind = "", errReturnedDirectly
				}
			}
		}
		return true
	})
	return name, kind
}

func lhsName(e ast.Expr) (string, errBinding) {
	id, ok := e.(*ast.Ident)
	if !ok {
		return "", errDiscarded
	}
	if id.Name == "_" {
		return "_", errBlank
	}
	return id.Name, errNamed
}

// inspectScope is ast.Inspect confined to ONE function scope: it never
// descends into a nested function literal, which opens a scope of its own.
//
// Every walk that reasons about "what does this function do with err" must use
// it. A plain ast.Inspect over the enclosing body reaches into closures, so a
// `return err` inside one would vouch for a collapse outside it — measured, as
// the closure_return_does_not_vouch_for_the_outer_collapse corpus row.
func inspectScope(root ast.Node, fn func(ast.Node) bool) {
	ast.Inspect(root, func(n ast.Node) bool {
		if n == nil {
			return false
		}
		if lit, ok := n.(*ast.FuncLit); ok && ast.Node(lit) != root {
			return false
		}
		return fn(n)
	})
}

// classifiedOrPropagated reports whether errName is passed to a classifier or
// returned, at a position AFTER the run call and BEFORE errName is re-bound.
//
// Two windows, and both edges are load-bearing:
//
//   - The lower edge stops one classified shellout from vouching for a second,
//     unclassified one later in the same function — and a function with two
//     shellouts is exactly where a new one gets added.
//   - The upper edge stops a LATER, unrelated error from vouching for this one.
//     `err` is the most reused identifier in Go, so the extremely ordinary
//     `out, err := cmd.Output(); if err != nil { return 0, nil }; n, err :=
//     parse(out); return n, err` would otherwise read as clean: the trailing
//     `return n, err` returns a DIFFERENT error and says nothing about the
//     collapse above it. Measured — that shape reported findings=0 before this
//     edge existed, and it is a corpus row now.
func classifiedOrPropagated(body ast.Node, errName string, after token.Pos) bool {
	limit := rebindPos(body, errName, after)
	found := false
	inspectScope(body, func(n ast.Node) bool {
		if found || n == nil || n.Pos() <= after || (limit.IsValid() && n.Pos() >= limit) {
			return !found
		}
		switch s := n.(type) {
		case *ast.CallExpr:
			if isShelloutClassifier(s.Fun) {
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

// rebindPos returns the position of the first assignment after `after` that
// writes errName, or an invalid position when there is none.
func rebindPos(body ast.Node, errName string, after token.Pos) token.Pos {
	best := token.NoPos
	inspectScope(body, func(n ast.Node) bool {
		assign, ok := n.(*ast.AssignStmt)
		if !ok || assign.Pos() <= after {
			return true
		}
		for _, lhs := range assign.Lhs {
			if id, ok := lhs.(*ast.Ident); ok && id.Name == errName {
				if !best.IsValid() || assign.Pos() < best {
					best = assign.Pos()
				}
			}
		}
		return true
	})
	return best
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

// usesIdent reports whether name appears in root, NOT counting occurrences
// inside a function literal.
//
// The exclusion is the point rather than a detail. `return func() error {
// return err }` is a ReturnStmt whose results mention err, but it does not
// hand this probe's error to this caller — it hands back a closure. Counting
// it as propagation lets a collapse three lines above go unreported, which is
// the closure corpus row.
func usesIdent(root ast.Node, name string) bool {
	hit := false
	ast.Inspect(root, func(n ast.Node) bool {
		if hit || n == nil {
			return false
		}
		if _, ok := n.(*ast.FuncLit); ok {
			return false
		}
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
	//
	// Today's count is 9: the eight production call sites of runProbe, plus
	// the one .Output() inside runProbe itself (whose error is returned
	// directly, so it is clean). Before #1547 it was 8 direct .Output() sites,
	// and routing them WITHOUT teaching this scan about runProbe took it to 1
	// — which this floor reported rather than passing over.
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
		t.Log("Fix: classify with probeAnswered(err[, answeredExitCodes...]) — or shellout.Answered, the shared predicate it forwards to (#1543) — and decide at THIS call site what a non-answer means, or return the error.")
	}
}

// scanShelloutRouting is the production detector behind #1547's routing rule,
// shared by the live rule and by its corpus. It returns one finding per child
// started outside runProbe, plus the number started INSIDE it — the second is
// the vacuity guard, and it has to be a count rather than a bool because a
// runProbe that started two children would satisfy a `> 0` check while being
// exactly the duplication the rule exists to prevent.
//
// It uses a plain ast.Inspect rather than splitScope's scope-confined walk, and
// that is deliberate: a run method inside a function literal is still outside
// runProbe, and the literals in this package are precisely where a shellout
// would be smuggled — every site's builder IS a FuncLit, and a builder that
// ran its own command instead of returning it would be invisible to a walk
// that stopped at closures.
func scanShelloutRouting(fset *token.FileSet, files map[string]*ast.File) (findings []shelloutFinding, insideRunProbe int) {
	for name, file := range files {
		base := filepath.Base(name)
		for _, decl := range file.Decls {
			owner, node := "var initialiser", ast.Node(decl)
			if fn, ok := decl.(*ast.FuncDecl); ok {
				if fn.Body == nil {
					continue
				}
				owner, node = fn.Name.Name, fn.Body
			}
			ast.Inspect(node, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok || !isRunMethodCall(call) {
					return true
				}
				if owner == runProbeName {
					insideRunProbe++
					return true
				}
				findings = append(findings, shelloutFinding{base, fset.Position(call.Pos()).Line, owner,
					"a child process is started outside " + runProbeName + "() — so this site spells its own " +
						"ceiling, and the three things that have to be right together (derive from the CALLER's " +
						"context, name the ceiling, cancel) are copied here instead of being inherited (#1547)"})
				return true
			})
		}
	}
	return findings, insideRunProbe
}

// TestEveryChildProcessRunsThroughRunProbe is #1547's rule, and it is the
// half that makes the SHAPE unrepresentable where the classification rule
// below only makes a misuse detectable.
//
// Two arms, the shape core/architecture_hookbody_test.go uses: none outside,
// exactly one inside. The second is not decoration — it is what stops the
// exemption from being a hole. A runProbe that stopped running a child would
// leave the first arm trivially satisfied by a package that starts no process
// at all, which is this repo's most expensive failure mode; and a runProbe
// that ran TWO would be the duplication the rule was written to delete,
// wearing the blessed name.
//
// There is deliberately no exemption list, for the reason architecture_test.go
// and architecture_hookbody_test.go have none: a site that genuinely must
// start a child another way amends this rule in a reviewable diff.
//
// What it does NOT cover, stated because a rule whose limits are implied reads
// as stronger than it is: it matches run methods by NAME with no type
// information (see the file header on why this parses source), so a child
// started through os.StartProcess, or through a value rather than a direct
// method call, is invisible to it. Those are the same limits
// core/architecture_shellout_test.go declares and pins, one layer up and
// repo-wide.
func TestEveryChildProcessRunsThroughRunProbe(t *testing.T) {
	fset, files := parsePackageSources(t, ".")
	findings, inside := scanShelloutRouting(fset, files)

	for _, f := range findings {
		t.Errorf("%s", f)
	}
	if len(findings) > 0 {
		t.Logf("Fix: build the command as a shelloutCmd and hand it to %s(ctx, build) — it applies "+
			"shelloutTimeout to the caller's context and cancels it. The ceiling is one copy on purpose.", runProbeName)
	}
	if inside != 1 {
		t.Fatalf("%d child-process run sites inside %s(), want exactly 1: with 0 the rule above is "+
			"satisfied by a package that starts no process at all, and with more than 1 the duplication "+
			"it was written to delete has moved inside the one function allowed to have it",
			inside, runProbeName)
	}
}

// namedCeilings are the durations a context.WithTimeout in this package may be
// given. Both are package-level constants, and the rule is still that the
// argument is a NAMED one — an inline literal, or a local whose value nothing
// names, fails whichever site writes it.
//
// #1529 added the second entry rather than widening the rule to "any
// identifier", because the two are different KINDS of ceiling and a rule that
// could not tell them apart would be the weaker one:
//
//   - shelloutTimeout bounds ONE child process. Every shellout in this package
//     derives it, and #1538's whole point is that the value stays one fact.
//   - clientHostBudget bounds a SEQUENCE of them — the lsof scan plus every
//     herdr client candidate behind it — which is a bound shelloutTimeout
//     cannot express, and its absence is exactly what #1529 was filed about.
//
// A third entry is a reviewable diff, which is the property an "any named
// identifier" rule would have thrown away.
var namedCeilings = map[string]bool{
	"shelloutTimeout":  true,
	"clientHostBudget": true,
}

// TestEveryBoundedShelloutUsesTheNamedCeiling is #1538's second half. The 2s
// ceiling was an unnamed literal in eight places while three doc comments and
// a test assertion already treated the value as a contract ("half of 2s"), and
// a sibling package's own constant pointed here for the justification.
//
// #1547 proposed DELETING it, on the argument that "with one
// context.WithTimeout in the package, a re-spelled ceiling is not
// expressible". Re-derived against the tree as it actually is, that argument
// does not hold, and it fails twice over:
//
//   - There are TWO WithTimeout sites after runProbe, not one. clientHostBudget
//     bounds a SEQUENCE of children (#1529) and so is not a child shellout and
//     does not live inside runProbe. A second aggregate is foreseeable —
//     noAggregateBudget's own doc names IsKnownInteractiveHost's ancestry walk
//     as a standing gap (#1534) — and nothing but this rule would stop the next
//     one being spelled `2*time.Second` inline.
//   - Even for the CHILD ceiling, "one WithTimeout" is a consequence of the
//     routing rule, not of runProbe existing. Routing forbids running a child
//     elsewhere; it says nothing about writing context.WithTimeout elsewhere.
//     Keeping the VALUE one fact is this rule's job and is independent of it.
//
// So it is narrowed rather than deleted: the floor drops from 8 to 2 because
// runProbe collapsed eight child ceilings into one.
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
			if id, ok := call.Args[1].(*ast.Ident); ok && namedCeilings[id.Name] {
				return true
			}
			t.Errorf("%s:%d: a context ceiling here is spelled inline or names something outside %v; use one of those so each value stays one fact (#1538, #1529)",
				filepath.Base(name), fset.Position(call.Pos()).Line, sortedNames(namedCeilings))
			return true
		})
	}
	// A floor, not "> 0", for the same reason the classification rule counts
	// run sites rather than asking whether it saw any at all. Since #1547 it
	// equals the population rather than sitting below it: there is exactly one
	// child ceiling (runProbe's) and one aggregate (clientHostBudget's), and
	// either disappearing means the scan is looking somewhere else — a third
	// entry raises this number in the same reviewable diff that adds it.
	const knownCeilings = 2
	if seen < knownCeilings {
		t.Fatalf("found %d context.WithTimeout calls, expected at least %d: the scan is not looking where it thinks it is",
			seen, knownCeilings)
	}
}

// TestNoAggregateBudgetNeverReachesAChildProcess enforces the one claim
// noAggregateBudget's own doc makes about itself — that nothing hands it to
// exec.CommandContext — which until #1529 was a sentence nothing checked.
//
// It matters because that helper is a NAMED spelling of context.Background(),
// and core/architecture_shellout_test.go's repo-wide rule matches the LITERAL
// spelling only ("a variable that happens to hold one is invisible here", its
// own declared limit). So `exec.CommandContext(noAggregateBudget(), …)` would
// be exec.Command wearing two disguises instead of one: unbounded, and passing
// both the repo rule and a reviewer skimming for a blessed helper name. The
// helper exists to name an absent AGGREGATE, never an absent child ceiling —
// every site derives shelloutTimeout from it, and this is what keeps that true.
func TestNoAggregateBudgetNeverReachesAChildProcess(t *testing.T) {
	fset, files := parsePackageSources(t, ".")

	sites := 0
	for name, file := range files {
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || len(call.Args) == 0 {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "CommandContext" {
				return true
			}
			if pkg, ok := sel.X.(*ast.Ident); !ok || pkg.Name != "exec" {
				return true
			}
			sites++
			inner, ok := call.Args[0].(*ast.CallExpr)
			if !ok {
				return true
			}
			if id, ok := inner.Fun.(*ast.Ident); ok && id.Name == "noAggregateBudget" {
				t.Errorf("%s:%d: exec.CommandContext given noAggregateBudget() — that is a NAMED "+
					"context.Background(), so this child has no ceiling at all, and the repo-wide rule "+
					"in core/architecture_shellout_test.go matches only the literal spelling and cannot "+
					"see it. Derive shelloutTimeout from the caller's context instead (#1529, #1543)",
					filepath.Base(name), fset.Position(call.Pos()).Line)
			}
			return true
		})
	}

	// A floor for the same reason the two rules above carry one: a scan that
	// stopped seeing this package's shellouts would report nothing and read
	// exactly like a clean one.
	const knownCommandSites = 4
	if sites < knownCommandSites {
		t.Fatalf("found %d exec.CommandContext sites, expected at least %d: the scan is not looking where it thinks it is",
			sites, knownCommandSites)
	}
}

// sortedNames renders a name set deterministically, so a failure message does
// not vary run to run with Go's map iteration order.
func sortedNames(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for name := range set {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}
