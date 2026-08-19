// architecture_shellout_test.go is the third architecture rule in this
// directory, and like architecture_hookbody_test.go it is deliberately a
// separate file from architecture_test.go's layering table.
//
// The layering table asks a question about the IMPORT GRAPH. This one asks
// which EXPRESSIONS may appear anywhere under core/:
//
//	A child process started from core/ must be built with
//	exec.CommandContext, and the context it is given must not be a bare
//	context.Background()/TODO() — written at the call site, or returned by a
//	package-local helper whose whole body is one of those two (#1559).
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
//
//     #1559 removed the ONE case where that mitigation was inert rather than
//     weak. A helper whose body is `return context.Background()` is not a
//     caller-supplied context that might lack a ceiling — it is provably the
//     root, and the named constant the limit falls back on is precisely what
//     does not exist there, so the fallback had nothing to say about it. That
//     spelling is now CAUGHT (see isBareRootContext), and what is left under
//     this limit is only its original subject: a context that came from
//     somewhere and may or may not have been bounded on the way.
//
//   - Resolving that named spelling is deliberately the CHEAPEST thing that
//     reaches the shape which occurs, so four narrower ones stay out of scope
//     and are pinned as want:0 corpus rows rather than merely stated. A helper
//     in ANOTHER package (`other.NoBudget()`) and a METHOD (`s.rootCtx()`) are
//     both selector calls whose target this scan cannot resolve without type
//     information — the same wall the aliased-import limit hits. A helper whose
//     body is more than one statement (`c := context.Background(); return c`)
//     needs intra-function dataflow. And a plain VARIABLE holding a root
//     context is invisible for the same reason, which was already stated on
//     isBareRootContext and had no row until #1559 gave it one. Widening to any
//     of the four is a reviewable diff here plus a row there; what makes the
//     stopping point defensible is that the first three all require the type
//     information the last paragraph of this header explains this rule cannot
//     have.
//
//   - It says nothing about CLASSIFICATION — whether a non-answer is
//     distinguishable from an empty answer at the call site. That is
//     processlifecycle's guard, and core/pkg/shellout.Answered is the shared
//     predicate #1543 promoted so a second package could reach it.
//
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
//
//   - It matches the CALLEE syntactically, so a child started through a value
//     rather than a direct call is invisible: a package-level `var run =
//     exec.Command` used as `run(…).Run()`, or a method value / interface
//     whose implementation lives in another file. Both are measured
//     (findings=0) and pinned as want:0 corpus rows. Seeing them needs type
//     information, which this scan deliberately does not have.
//
//   - It reads NON-TEST files only, and does not skip vendor/ (the repo has
//     none). Test code starting an unbounded child is out of scope on purpose:
//     the hazard this rule exists for is a long-lived daemon waiting forever,
//     and every fixture in core/adapters/outbound/git's own suite deliberately
//     builds stalling children.
//
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
//
// helpers is the package's root-context helper set (rootContextHelpers). It is
// a parameter rather than something derived per file because the shape #1559
// found occurs ACROSS files: processlifecycle declares noAggregateBudget in
// osutil.go and every one of its shellouts lives in osutil_darwin.go, so a
// file-local collection would have resolved nothing and passed.
func scanShelloutBounds(fset *token.FileSet, file *ast.File, name string, helpers map[string]bool) (findings []shelloutBoundFinding, bounded int) {
	ast.Inspect(file, func(n ast.Node) bool {
		// A composite literal is the one spelling that cannot be fixed by
		// passing a context, because exec.Cmd has no exported context field —
		// CommandContext sets an unexported one. So `&exec.Cmd{Path: …}`
		// followed by .Run() is unbounded BY CONSTRUCTION, and it is the
		// natural thing to reach for once exec.Command is banned. Flagged
		// rather than merely declared, because unlike the two limits in the
		// header this one needs no type information to see.
		if lit, ok := n.(*ast.CompositeLit); ok {
			if sel, ok := lit.Type.(*ast.SelectorExpr); ok {
				if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "exec" && sel.Sel.Name == "Cmd" {
					findings = append(findings, shelloutBoundFinding{name, fset.Position(lit.Pos()).Line,
						"an exec.Cmd built as a struct literal has no context field at all — " +
							"exec.CommandContext sets an unexported one, so nothing can ever cancel " +
							"this child. Build it with exec.CommandContext (#1543)"})
				}
			}
			return true
		}
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
			if len(call.Args) > 0 && isBareRootContext(call.Args[0], helpers) {
				findings = append(findings, shelloutBoundFinding{name, line,
					"exec.CommandContext given a bare context.Background()/TODO() — literally, or " +
						"through a package-local helper that returns one (#1559) — is exec.Command " +
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

// isBareRootContext reports whether e is context.Background() or context.TODO()
// — written at the call site, or reached through a package-local helper in
// helpers whose whole body returns one of them.
//
// The literal half is why the rule exists: `exec.CommandContext(
// context.Background(), …)` reads as bounded to a reviewer skimming for
// "CommandContext" while being exactly as unbounded as exec.Command.
//
// The named half is #1559, and the naming is what made it invisible rather than
// what made it safe. `exec.CommandContext(noAggregateBudget(), …)` is that same
// site wearing a second disguise: it passes a reviewer skimming for a blessed
// helper name too, and until #1559 it passed this rule, because a call to an
// Ident is not the *ast.SelectorExpr the literal half matches. Naming an absent
// AGGREGATE is a good practice this rule has no quarrel with (see that helper's
// doc); handing that name to a CHILD is the thing it forbids, and the two are
// one keystroke apart.
//
// A variable holding a root context is still invisible, and so is a helper
// spelled as a method or living in another package — declared limits, pinned as
// want:0 corpus rows so they are learned from a test rather than an incident
// (#1450). The header says what each would cost to close.
func isBareRootContext(e ast.Expr, helpers map[string]bool) bool {
	call, ok := e.(*ast.CallExpr)
	if !ok {
		return false
	}
	if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
		pkg, ok := sel.X.(*ast.Ident)
		if !ok || pkg.Name != "context" || len(call.Args) != 0 {
			return false
		}
		return sel.Sel.Name == "Background" || sel.Sel.Name == "TODO"
	}
	// Arguments are deliberately not constrained here, unlike the literal form
	// above: context.Background takes none, but a helper is free to take a pid
	// or a name and return the root anyway, and that spelling is no more
	// bounded for having an argument.
	id, ok := call.Fun.(*ast.Ident)
	return ok && helpers[id.Name]
}

// rootContextHelpers collects, from one PACKAGE's files, the names of functions
// whose whole body is `return <a bare root context>` — the named spelling of
// context.Background() that #1559 found the rule blind to.
//
// It is a fixpoint rather than a single pass so that one indirection does not
// defeat it: `func a() context.Context { return b() }` over a root-returning b
// is the same unbounded child with one more hop, and closing it costs the loop
// below rather than a second mechanism. Everything it takes is syntactic, so
// the header's argument for parsing source rather than loading types still
// holds — a helper declared behind //go:build darwin is collected on a linux
// runner exactly like every other declaration this file reads.
//
// What it deliberately does not collect: methods (a receiver makes the call a
// selector this scan cannot resolve), and any body of more than one statement.
// Both are pinned as want:0 rows.
func rootContextHelpers(files map[string]*ast.File) map[string]bool {
	returned := map[string]ast.Expr{}
	for _, file := range files {
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv != nil || fn.Body == nil || len(fn.Body.List) != 1 {
				continue
			}
			if fn.Type.Results == nil || len(fn.Type.Results.List) != 1 {
				continue
			}
			ret, ok := fn.Body.List[0].(*ast.ReturnStmt)
			if !ok || len(ret.Results) != 1 {
				continue
			}
			returned[fn.Name.Name] = ret.Results[0]
		}
	}

	helpers := map[string]bool{}
	for changed := true; changed; {
		changed = false
		for name, result := range returned {
			if helpers[name] {
				continue
			}
			if isBareRootContext(result, helpers) {
				helpers[name] = true
				changed = true
			}
		}
	}
	return helpers
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

	// Grouped by directory, because the root-context helper set is a PACKAGE
	// fact and the shape #1559 found spans two files of one (noAggregateBudget
	// is declared in processlifecycle/osutil.go; every shellout that could take
	// it lives in osutil_darwin.go). Directory rather than the package clause:
	// build-tagged files of the same package are read here regardless of tag,
	// which is the point of parsing rather than loading, and the directory is
	// what holds them together without evaluating one.
	byPackage := map[string]map[string]*ast.File{}
	for name, f := range files {
		dir := filepath.Dir(name)
		if byPackage[dir] == nil {
			byPackage[dir] = map[string]*ast.File{}
		}
		byPackage[dir][name] = f
	}

	var findings []shelloutBoundFinding
	total := 0
	for _, pkg := range byPackage {
		helpers := rootContextHelpers(pkg)
		for name, f := range pkg {
			got, bounded := scanShelloutBounds(fset, f, name, helpers)
			findings = append(findings, got...)
			total += bounded
		}
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

	// There is deliberately NO matching floor on the helper set, and the reason
	// is worth stating because every other mechanism in this family carries
	// one. A count here would assert that core/ contains at least N named
	// root-context helpers — i.e. it would require a smell to keep existing,
	// and would go red the day noAggregateBudget (today's only one) is
	// legitimately deleted. The floor that a broken collection needs lives in
	// the corpus instead, where it cannot be moved by the tree changing: the
	// helper rows in architecture_shellout_shapes_test.go are want:1, so a
	// rootContextHelpers that quietly collected nothing reddens there rather
	// than reading as a clean tree here.

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
