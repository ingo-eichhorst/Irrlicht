// architecture_hookbody_test.go is the second architecture rule in this
// directory, and it is deliberately a separate file from architecture_test.go:
// the layering rules there are a question about the IMPORT GRAPH
// (packages.Load with NeedName|NeedImports, no syntax), while this one is a
// question about SOURCE — which expressions appear inside a function — and so
// needs NeedSyntax|NeedTypes|NeedTypesInfo over a much narrower pattern. One
// load cannot serve both cheaply, and a rule table that mixes "which packages
// may I import" with "which expressions may I write" reads as one thing when it
// is two.
//
// # What this enforces (issue #1389)
//
// An inbound hook body carries a caller-supplied transcript_path on a local,
// unauthenticated endpoint, and #1361 established that a receiver must confine
// that path to the adapter's own declared roots before anything downstream
// opens it. contracttesting.AssertHookPathConfined checks that a receiver DOES
// confine — but only for a receiver that wired the contract. A receiver nobody
// wired it for is invisible to it. That is not hypothetical: within #1361
// itself the claudecode statusline receiver, registered three lines from the
// one being fixed, was left unconfined by the author of the rule, and a later
// review pass caught it rather than any gate.
//
// So the obligation is moved from "a contract the next adapter must remember to
// wire" to "the build fails": inside core/adapters/inbound/agents/..., reading
// an inbound *http.Request's body is the exclusive privilege of
// hookjson.DecodeConfined, which cannot decode without being handed a
// *PathConfiner.
//
// # Why the rule in issue #1389 is not the rule implemented here
//
// #1389 proposed keying on "a file that references a HookEndpointPath may not
// call json.NewDecoder(r.Body)". Measured against the tree, that selects
// nothing: HookEndpointPath is declared and referenced only in the three
// hookinstaller.go files, which never touch r.Body, and the fourth receiver's
// route constant is not even called HookEndpointPath (it is
// StatuslineEndpointPath). The rule would have passed vacuously on the day it
// landed AND against the exact statusline bug it was written to catch.
//
// The repair is to stop trying to recognize "a hook-receiving file" at all.
// There is no file pattern here, so there is no file a new receiver can hide
// in: the predicate is the untrusted-input operation itself. Any code that
// reads an inbound request body inside an agent adapter is in scope, whatever
// it is called and whatever file it lives in.
//
// # Why the scope is exactly core/adapters/inbound/agents/...
//
// Narrower would reintroduce the too-narrow failure above. Wider fires on the
// daemon's own REST API in core/cmd/irrlichd, which decodes bodies for a living
// and has nothing to do with transcript paths. The chosen boundary is load-
// bearing rather than convenient: an inbound HTTP endpoint hosted by an AGENT
// ADAPTER on the daemon's local unauthenticated port is precisely the #1361
// threat surface, and today there is not one such endpoint that is anything
// other than a hook receiver (the only functions under this tree taking an
// *http.Request are the four receivers' serve funcs, their constructor closures
// and hookjson's own).
//
// # What it does NOT cover
//
//   - A hook receiver defined OUTSIDE core/adapters/inbound/agents/... — e.g.
//     one written directly in core/cmd/irrlichd next to registerHookRoutes.
//     Nothing here sees it. AGENTS.md places adapters in this tree, and the four
//     receivers are all here, but that is a convention, not a check.
//   - A receiver that hands the whole *http.Request to a helper OUTSIDE this
//     tree, which then reads .Body. Passing r.Body outward IS caught (the
//     selector appears at the call site); passing r itself is not.
//   - Test files. packages.Load runs without Tests, like every rule in
//     architecture_test.go. Unlike there, this is close to harmless: a receiver
//     declared in a _test.go is never registered on the production mux, so a
//     body read there cannot reach a user. It is still a gap for a helper shared
//     between test and non-test code.
//   - Reflection, generated code, or a body read spelled through an
//     interface value whose dynamic type is *http.Request.
package core_test

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"sort"
	"strings"
	"testing"

	"golang.org/x/tools/go/packages"
)

// agentAdapterPattern is the tree the rule governs, relative to core/.
const agentAdapterPattern = "./adapters/inbound/agents/..."

// chokepointPkg and chokepointFunc name the single place an inbound request
// body may be read. Spelled as strings rather than reached through an import
// because this file must be able to report that the chokepoint has DISAPPEARED
// — a rule that resolved the symbol would stop compiling instead, which is a
// worse failure mode for a guard whose whole job is to be legible when broken.
const (
	chokepointPkg  = "irrlicht/core/adapters/inbound/agents/hookjson"
	chokepointFunc = "DecodeConfined"
)

// knownAgentPackages are packages the load MUST return. This is the vacuity
// guard, and it is the one that matters: a pattern typo, a build break confined
// to this subtree, or a packages.Load that silently returns nothing all produce
// a perfectly green run of a rule that scanned no code at all. Naming the four
// receivers' packages plus the chokepoint's means "found no violations" can
// only be reported about a tree that was actually read.
var knownAgentPackages = []string{
	chokepointPkg,
	"irrlicht/core/adapters/inbound/agents/claudecode",
	"irrlicht/core/adapters/inbound/agents/codex",
	"irrlicht/core/adapters/inbound/agents/copilot",
}

// bodyConsumingMethods are *http.Request methods that read the request body
// without ever naming the Body field. Without them the rule is a rule about one
// spelling rather than about reading the body: r.FormValue("transcript_path")
// is the same untrusted input arriving through a different door.
//
// MultipartReader and the ParseForm family consume the body directly;
// FormValue/PostFormValue/FormFile call ParseMultipartForm internally.
var bodyConsumingMethods = map[string]bool{
	"ParseForm":          true,
	"ParseMultipartForm": true,
	"FormValue":          true,
	"PostFormValue":      true,
	"FormFile":           true,
	"MultipartReader":    true,
}

// requestBodyRead is one place source code reads the body of an inbound
// *http.Request.
type requestBodyRead struct {
	Pkg  string // package path
	Pos  string // file:line:col
	Func string // enclosing top-level func or method, "" at file scope
	How  string // the expression as written, e.g. "r.Body" or "r.FormValue"
}

func (b requestBodyRead) String() string {
	where := b.Func
	if where == "" {
		where = "<file scope>"
	}
	return fmt.Sprintf("%s (%s in %s)", b.Pos, b.How, where)
}

func TestNoDirectRequestBodyReadsInAgentAdapters(t *testing.T) {
	pkgs := loadAgentAdapterPackages(t)

	var outside, inside []requestBodyRead
	for _, pkg := range pkgs {
		for _, read := range requestBodyReadsIn(pkg.Fset, pkg.Syntax, pkg.TypesInfo, pkg.PkgPath) {
			if read.Pkg == chokepointPkg {
				inside = append(inside, read)
				continue
			}
			outside = append(outside, read)
		}
	}

	// Arm A — the prohibition. Every hook receiver reaches its transcript path
	// through the chokepoint, so a body read anywhere else in this tree is
	// either a receiver that skipped confinement or a new kind of endpoint that
	// has to argue for itself in a reviewable diff.
	for _, read := range outside {
		t.Errorf("hook-body violation: %s reads an inbound *http.Request body at %s\n"+
			"\tinside %s, an inbound request body may only be read by %s.%s, which cannot decode without a *PathConfiner (issue #1389)\n"+
			"\tif this is a hook receiver: call hookjson.DecodeConfined instead of decoding r.Body yourself\n"+
			"\tif this is a new kind of endpoint that carries no path: this rule has no exemption list by design — amend the rule, with a test",
			read.Pkg, read, agentAdapterPattern, chokepointPkg, chokepointFunc)
	}

	// Arm B — the exemption is a pin, not a hole. One package is allowed to read
	// a request body; that permission is bounded to one function inside it, so
	// hookjson cannot later grow a second, non-confining decoder and inherit the
	// exemption by living in the right package. Arm B is also what makes a
	// vacuous Arm A impossible to mistake for a clean one: if the walker stops
	// finding body reads entirely, the count here goes to zero and fails.
	assertChokepointIsSingular(t, inside)
}

// assertChokepointIsSingular checks Arm B: the exempt package reads a request
// body in exactly one function, and that function is the chokepoint.
func assertChokepointIsSingular(t *testing.T, inside []requestBodyRead) {
	t.Helper()

	if len(inside) == 0 {
		t.Fatalf("no request-body read found in %s at all — either %s.%s no longer decodes the body "+
			"(in which case every receiver's confinement is now unenforced), or this rule's detector has "+
			"stopped matching and Arm A above is passing vacuously",
			chokepointPkg, chokepointPkg, chokepointFunc)
	}

	funcs := map[string][]requestBodyRead{}
	for _, read := range inside {
		funcs[read.Func] = append(funcs[read.Func], read)
	}
	for _, name := range sortedKeys(funcs) {
		if name == chokepointFunc {
			continue
		}
		for _, read := range funcs[name] {
			t.Errorf("hook-body violation: %s reads an inbound *http.Request body at %s\n"+
				"\t%s is exempt only for %s — the single decode every receiver is forced through.\n"+
				"\tA second body-reading function here is a second way to skip confinement (issue #1389)",
				chokepointPkg, read, chokepointPkg, chokepointFunc)
		}
	}
}

// loadAgentAdapterPackages loads the governed tree with the syntax and type
// information the detector needs, and refuses to return a set it cannot vouch
// for.
func loadAgentAdapterPackages(t *testing.T) []*packages.Package {
	t.Helper()

	cfg := &packages.Config{
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedSyntax |
			packages.NeedTypes | packages.NeedTypesInfo,
		Dir: ".",
	}
	pkgs, err := packages.Load(cfg, agentAdapterPattern)
	if err != nil {
		t.Fatalf("packages.Load(%q): %v", agentAdapterPattern, err)
	}
	if n := packages.PrintErrors(pkgs); n > 0 {
		t.Fatalf("packages.Load reported %d package error(s); build is broken", n)
	}
	if len(pkgs) == 0 {
		t.Fatalf("packages.Load returned no packages for pattern %q", agentAdapterPattern)
	}

	loaded := map[string]bool{}
	files := 0
	for _, pkg := range pkgs {
		loaded[pkg.PkgPath] = true
		files += len(pkg.Syntax)
		if pkg.TypesInfo == nil {
			t.Fatalf("package %q loaded without type information; the detector cannot tell "+
				"an *http.Request body from an *http.Response body without it", pkg.PkgPath)
		}
	}
	for _, want := range knownAgentPackages {
		if !loaded[want] {
			t.Fatalf("package %q was not loaded by pattern %q — the rule would pass having never read it",
				want, agentAdapterPattern)
		}
	}
	if files == 0 {
		t.Fatalf("pattern %q matched %d package(s) but zero parsed files", agentAdapterPattern, len(pkgs))
	}
	return pkgs
}

// requestBodyReadsIn returns every read of an inbound *http.Request's body in
// the given files.
//
// It is a plain function over (fset, files, info) rather than a method on
// anything so the shape corpus in architecture_hookbody_shapes_test.go can feed
// it synthetic packages and pin, case by case, which spellings it catches.
func requestBodyReadsIn(fset *token.FileSet, files []*ast.File, info *types.Info, pkgPath string) []requestBodyRead {
	var found []requestBodyRead
	for _, file := range files {
		// Walked one top-level declaration at a time rather than with a
		// push/pop stack over the whole file: ast.Inspect emits its nil
		// sentinel after EVERY node's children, not only after a FuncDecl's,
		// so a stack popped on nil unwinds on the first leaf and attributes
		// every later hit to file scope. Arm B depends on this name being
		// right — it is what distinguishes "hookjson decodes in DecodeConfined"
		// from "hookjson decodes somewhere else too".
		for _, decl := range file.Decls {
			name := ""
			if fn, ok := decl.(*ast.FuncDecl); ok {
				name = funcDeclName(fn)
			}
			ast.Inspect(decl, func(n ast.Node) bool {
				sel, ok := n.(*ast.SelectorExpr)
				if !ok || !readsRequestBody(sel, info) {
					return true
				}
				found = append(found, requestBodyRead{
					Pkg:  pkgPath,
					Pos:  fset.Position(sel.Sel.Pos()).String(),
					Func: name,
					How:  exprText(sel),
				})
				return true
			})
		}
	}
	return found
}

// readsRequestBody reports whether sel reads the body of an *http.Request —
// either the Body field itself or a method that consumes the body.
//
// The receiver's TYPE is checked, never its name: r.Body on an *http.Response
// is an ordinary outbound HTTP client read and must not fire, and an adapter
// that happens to call its request something other than "r" must still be
// caught. That is the whole reason this rule needs NeedTypesInfo rather than a
// grep.
func readsRequestBody(sel *ast.SelectorExpr, info *types.Info) bool {
	if sel.Sel == nil {
		return false
	}
	if sel.Sel.Name != "Body" && !bodyConsumingMethods[sel.Sel.Name] {
		return false
	}
	return isHTTPRequest(info.TypeOf(sel.X))
}

// isHTTPRequest reports whether t is net/http.Request or a pointer to it.
func isHTTPRequest(t types.Type) bool {
	if t == nil {
		return false
	}
	if ptr, ok := types.Unalias(t).(*types.Pointer); ok {
		t = ptr.Elem()
	}
	named, ok := types.Unalias(t).(*types.Named)
	if !ok {
		return false
	}
	obj := named.Obj()
	return obj != nil && obj.Pkg() != nil &&
		obj.Pkg().Path() == "net/http" && obj.Name() == "Request"
}

// funcDeclName renders a FuncDecl's name, qualified by receiver type for a
// method, so a violation report names something greppable.
func funcDeclName(decl *ast.FuncDecl) string {
	if decl.Name == nil {
		return ""
	}
	if decl.Recv == nil || len(decl.Recv.List) == 0 {
		return decl.Name.Name
	}
	return "(" + exprText(decl.Recv.List[0].Type) + ")." + decl.Name.Name
}

// exprText renders a selector or type expression back to source-ish text for
// the failure message. Only the shapes this file reports on are handled; the
// fallback keeps a message readable rather than empty.
func exprText(e ast.Expr) string {
	switch v := e.(type) {
	case *ast.Ident:
		return v.Name
	case *ast.StarExpr:
		return "*" + exprText(v.X)
	case *ast.SelectorExpr:
		return exprText(v.X) + "." + v.Sel.Name
	case *ast.IndexExpr:
		return exprText(v.X)
	case *ast.IndexListExpr:
		return exprText(v.X)
	case *ast.CallExpr:
		return exprText(v.Fun) + "(…)"
	case *ast.ParenExpr:
		return "(" + exprText(v.X) + ")"
	}
	return strings.TrimSpace(fmt.Sprintf("%T", e))
}

func sortedKeys(m map[string][]requestBodyRead) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
