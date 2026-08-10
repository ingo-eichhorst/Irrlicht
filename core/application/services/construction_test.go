package services

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"irrlicht/core/domain/permission"
)

// This file covers issue #1400 (PermissionService) and #1450 (SessionDetector):
// a service built by a bare composite literal instead of by its allocator skips
// every map allocation, and panics on the first write — in the writer, several
// frames from the literal that caused it.
//
// The two guards here are deliberately different in kind, because the hazard
// has two halves and neither guard sees the other's:
//
//   - The SOURCE SCAN guards the CONSTRUCTION SITES. It scans this package's
//     own source, so it fires on a literal that no test ever executes. That
//     matters because the quiet case is a suite which only drives read paths:
//     it constructs the trap and never springs it, leaving the panic for
//     whoever adds an unrelated test months later. It reports file and line of
//     the literal, which the runtime panic never does.
//   - The REFLECTION WALK guards the ALLOCATOR ITSELF. A new map- or
//     channel-typed field added to the struct and forgotten in the allocator is
//     a hole the source scan cannot see, because every construction site is
//     still perfectly legal.
//
// Both are parameterised over a table of guarded types rather than duplicated
// per type: #1450 is #1400 in a second struct in the same package, and a second
// copy of a scanner is a second thing to keep in step.
//
// Neither is a compile-time guard, and that is not a shortfall of effort. #1400
// floated an unexported zero-width field to make the literal uncompilable, but
// it is inert for both types: every field of both structs is already
// unexported, so Go already forbids another package from setting one in a
// literal. The one spelling that still compiles from outside is
// `&services.T{}` with no fields at all — which is not harmless just because it
// populates nothing: it is a fully-nil service that panics on first use, and it
// is what an external test in this same directory (`package services_test`)
// would naturally reach for. The scan below matches it. And the literals these
// issues were actually filed about live in THIS package, where unexported
// fields are freely settable and no unexported marker field could ever have
// reached them.

// guardedConstruction describes one type in this package whose construction is
// funnelled through a single allocator.
type guardedConstruction struct {
	// typeName is matched textually because the scan is a source scan: it
	// deliberately has no type information, so that it stays cheap and keeps
	// working on a file that does not compile.
	typeName string

	// allocator is the one function allowed to write a composite literal of
	// typeName.
	allocator string

	// nilTolerant names each map- or channel-typed field the allocator
	// deliberately leaves nil, with the reason. A field is exempt only by being
	// written down here — the default is that it must be initialised, so a new
	// map- or channel-typed field is covered by a test that already exists
	// rather than by whoever adds it remembering to write one.
	nilTolerant map[string]string

	// mustBeNonZero names each field that is NEITHER a map nor a channel and
	// whose zero value is nevertheless unusable, with the reason. The
	// reflection walk cannot discover these — a zero int, a nil pointer and a
	// nil func are all perfectly ordinary on other fields of the same struct —
	// so they are the half of the allocator's job that has to be written down.
	//
	// This list is why "nil maps" understates both issues. PermissionService
	// needed detectInterval, because time.NewTicker panics outright on a
	// non-positive interval. SessionDetector needs five more, and none of them
	// panics: they fail silently, which is worse.
	mustBeNonZero map[string]string

	// paths returns the constructions to walk by reflection: the allocator
	// itself, and every exported constructor built on top of it. Both, because
	// they can disagree — a constructor assigns dependencies AFTER the
	// allocator runs, so a future dependency-supplied map would re-nil an
	// allocated field with a walk that only ever saw the allocator staying
	// green.
	paths func() []constructionPath
}

type constructionPath struct {
	name  string
	value reflect.Value // the constructed *T
}

// guardedConstructions is the table both guards run over. A third type joins by
// adding a row.
func guardedConstructions() []guardedConstruction {
	return []guardedConstruction{
		{
			typeName:  "PermissionService",
			allocator: "newPermissionService",
			nilTolerant: map[string]string{
				"factories": "dependency-supplied (nil means demo mode: no watcher ever starts) " +
					"and only ever read — startWatching does one lookup, and a nil-map read is legal",
			},
			mustBeNonZero: map[string]string{
				"detectInterval": "Start's time.NewTicker panics outright on a non-positive interval, " +
					"so a service that skipped the allocator is unstartable as well as nil-unsafe",
			},
			paths: func() []constructionPath {
				return []constructionPath{
					{"newPermissionService", reflect.ValueOf(newPermissionService())},
					{"NewPermissionService", reflect.ValueOf(NewPermissionService(PermissionServiceDeps{
						Store: emptyPermStore{}, Log: &gateLog{},
					}))},
				}
			},
		},
		{
			typeName:  "SessionDetector",
			allocator: "newSessionDetector",
			// Every map and channel on SessionDetector is allocator-owned;
			// nothing is dependency-supplied, so there is nothing to exempt.
			nilTolerant: map[string]string{},
			mustBeNonZero: map[string]string{
				"deletedCooldown": "at zero every tombstoned session is immediately re-creatable from " +
					"the late-arriving writes of a dying process — the exact ghost-session bug the " +
					"field exists to prevent, and it is silent",
				"signals": "session.SignalHolds has no nil-receiver guards (every method takes its own " +
					"lock), and the detector dereferences it on the classify path without checking",
				"dwell": "nil is nil-receiver-safe but silently disables state hysteresis (#1366) — " +
					"right for a test that opts into it deliberately, catastrophic in production",
				"bgLiveProbe": "nil is nil-guarded but silently disables background-process liveness " +
					"detection (#445): every backgrounded session reads as finished",
				"bgPIDProbe": "the PID-reporting half of the same probe (#661), silent in the same way",
			},
			paths: func() []constructionPath {
				return []constructionPath{
					{"newSessionDetector", reflect.ValueOf(newSessionDetector())},
					{"NewSessionDetector", reflect.ValueOf(NewSessionDetector(nil, SessionDetectorDeps{}))},
				}
			},
		},
	}
}

// ---------------------------------------------------------------------------
// Guard 1 — the source scan
// ---------------------------------------------------------------------------

// packageTypeDecls maps every package-level type name in the parsed package to
// the type expression it names, so the scan can see through
// `type detList []*SessionDetector`. Without it a defined container type is a
// bare *ast.Ident that matches nothing, and `detList{{}}` constructs a service
// the scan reports as clean.
func packageTypeDecls(pkg *ast.Package) map[string]ast.Expr {
	decls := map[string]ast.Expr{}
	for _, file := range pkg.Files {
		for _, d := range file.Decls {
			gd, ok := d.(*ast.GenDecl)
			if !ok || gd.Tok != token.TYPE {
				continue
			}
			for _, spec := range gd.Specs {
				if ts, ok := spec.(*ast.TypeSpec); ok {
					decls[ts.Name.Name] = ts.Type
				}
			}
		}
	}
	return decls
}

// typeResolutionDepth bounds the declaration chase below. Bounded rather than
// cycle-detected: a cyclic type declaration does not compile, so the only thing
// the bound protects against is this scan hanging on source that was already
// broken.
const typeResolutionDepth = 10

// literalScan carries the per-package state the recursive walk needs.
type literalScan struct {
	typeName string
	decls    map[string]ast.Expr
	report   func(ast.Node)
}

// underlying follows package-level type declarations until it reaches a type
// expression that is not a bare identifier, so the scan can see through
// `type detList []*SessionDetector`. Without it a defined container type is an
// *ast.Ident that matches nothing structural, and `detList{{}}` constructs a
// service the scan reports as clean.
//
// It stops at the guarded type's own name, which is load-bearing: the
// declaration table holds `SessionDetector` too, mapped to its *ast.StructType,
// and chasing that far would resolve every literal of the guarded type into an
// anonymous struct that names nothing.
func (s *literalScan) underlying(t ast.Expr) ast.Expr {
	for range typeResolutionDepth {
		switch v := t.(type) {
		case *ast.ParenExpr:
			t = v.X
		case *ast.Ident:
			if v.Name == s.typeName {
				return t
			}
			next, ok := s.decls[v.Name]
			if !ok {
				return t
			}
			t = next
		default:
			return t
		}
	}
	return t
}

// names reports whether the type expression IS the guarded struct type, seeing
// through pointers, parentheses, a qualified package selector and a chain of
// package-level type declarations.
//
// The *ast.SelectorExpr arm is what makes the scan see
// `services.SessionDetector{}` — the spelling the `package services_test` files
// in this same directory use, and the only literal an external package can
// still write now that every field is unexported. It matches on the selector
// alone rather than on `pkg.Type`, so an import alias cannot slip past.
func (s *literalScan) names(t ast.Expr) bool {
	switch v := s.underlying(t).(type) {
	case *ast.Ident:
		return v.Name == s.typeName
	case *ast.StarExpr:
		return s.names(v.X)
	case *ast.SelectorExpr:
		return v.Sel.Name == s.typeName
	}
	return false
}

// visit reports lit if the type it is KNOWN to have is the guarded type, and
// otherwise follows the edges along which a nested literal may ELIDE its type.
//
// typ is lit.Type when the literal spells its own type, or the element/key type
// inherited from the container when it does not. Go allows elision in exactly
// three places — a slice or array element, a map key, and a map value (a struct
// field value may NOT elide its type; `T{f: {}}` does not compile) — so
// following those edges reaches every literal the scan would otherwise miss, at
// any depth.
//
// Depth is the part #1444's single-level matcher got wrong in the other
// direction: it looked one element down from a container, so
// `[][]*SessionDetector{{{}}}` and `map[string][]*SessionDetector{"a": {{}}}`
// hide a construction two levels down and both read as clean.
func (s *literalScan) visit(lit *ast.CompositeLit, typ ast.Expr) {
	if typ == nil {
		return
	}
	if s.names(typ) {
		s.report(lit)
		return
	}
	switch v := s.underlying(typ).(type) {
	case *ast.ArrayType:
		// Covers slices and arrays alike; Len distinguishes them and the
		// elision rule is the same for both.
		for _, el := range lit.Elts {
			s.descend(el, v.Elt)
		}
	case *ast.MapType:
		for _, el := range lit.Elts {
			if kv, ok := el.(*ast.KeyValueExpr); ok {
				s.descend(kv.Key, v.Key)
				s.descend(kv.Value, v.Value)
			}
		}
	}
	// Any other type — most importantly *ast.StructType — is deliberately not
	// descended into. A table-driven test's
	// `[]struct{ svc *PermissionService }{...}` mentions the guarded type in
	// its element type while its elements hold values that came FROM a
	// constructor, and reporting that would be a false positive on ordinary,
	// correct test code. A guard that cries wolf on the right thing gets
	// deleted. Nothing is lost: a struct field value cannot elide its type, so
	// any literal in there spells its own and is found by the top-level walk.
}

// descend follows one container edge into an element that ELIDED its type.
// An element that spells its own type is skipped here and found by the
// top-level walk instead, so nothing is reported twice.
func (s *literalScan) descend(e ast.Expr, typ ast.Expr) {
	if u, ok := e.(*ast.UnaryExpr); ok && u.Op == token.AND {
		e = u.X
	}
	if lit, ok := e.(*ast.CompositeLit); ok && lit.Type == nil {
		s.visit(lit, typ)
	}
}

// allocatorDecl returns the allowed allocator's declaration in this file, or
// nil. Its extent is what tells a legitimate literal from a bypass: the check
// is positional rather than "which function body am I walking", because walking
// only function bodies would skip every package-scope declaration, and
// `var x = &SessionDetector{}` at package scope is a perfectly ordinary shape
// for a shared test fixture — which is exactly where the original offender
// lived.
func allocatorDecl(file *ast.File, allocator string) *ast.FuncDecl {
	for _, d := range file.Decls {
		if fn, ok := d.(*ast.FuncDecl); ok && fn.Recv == nil && fn.Name.Name == allocator {
			return fn
		}
	}
	return nil
}

// scanFileForLiterals reports every guarded-type composite literal in one file
// that is not inside the allocator, and whether the allocator's own literal was
// seen (the caller's vacuity guard).
func scanFileForLiterals(fset *token.FileSet, path string, file *ast.File, g guardedConstruction, decls map[string]ast.Expr) (offenders []string, sawAllocator bool) {
	alloc := allocatorDecl(file, g.allocator)

	scan := &literalScan{typeName: g.typeName, decls: decls}
	scan.report = func(n ast.Node) {
		if alloc != nil && n.Pos() >= alloc.Pos() && n.End() <= alloc.End() {
			sawAllocator = true
			return
		}
		offenders = append(offenders, fmt.Sprintf("%s:%d",
			filepath.Base(path), fset.Position(n.Pos()).Line))
	}

	ast.Inspect(file, func(n ast.Node) bool {
		lit, ok := n.(*ast.CompositeLit)
		if !ok || lit.Type == nil {
			// A literal with no type of its own is an elided container element.
			// It is unreachable from here — only its container knows what type
			// it has — so it is visited through scan.descend instead.
			return true
		}
		scan.visit(lit, lit.Type)
		return true
	})
	return offenders, sawAllocator
}

// assertNeverBuiltByBareLiteral fails on any composite literal of the guarded
// type in this package outside its allocator.
//
// It reports the file and line of the literal. That is the entire point: the
// runtime failure a bypass produces reports neither. It surfaces as "assignment
// to entry in nil map" with a stack whose deepest frame is whichever method
// happened to write first, several calls away from the construction that caused
// it — and only if some test happens to drive a path that writes a map at all.
func assertNeverBuiltByBareLiteral(t *testing.T, g guardedConstruction) {
	t.Helper()
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", nil, 0)
	if err != nil {
		t.Fatalf("parsing this package's source: %v", err)
	}
	if len(pkgs) == 0 {
		t.Fatal("parsed no packages — the scan would pass vacuously")
	}

	seenAllocator := false
	var offenders []string

	for _, pkg := range pkgs {
		decls := packageTypeDecls(pkg)
		for path, file := range pkg.Files {
			found, sawAllocator := scanFileForLiterals(fset, path, file, g, decls)
			offenders = append(offenders, found...)
			seenAllocator = seenAllocator || sawAllocator
		}
	}

	// Vacuity guard. A scan that matches nothing at all — a renamed type, a
	// parser that silently read the wrong directory — would otherwise report a
	// clean package while checking nothing, which is the failure mode this
	// whole file exists to remove.
	if !seenAllocator {
		t.Fatalf("found no %s literal inside %s(); the scan is not looking where "+
			"it thinks it is, so its silence proves nothing", g.typeName, g.allocator)
	}

	if len(offenders) > 0 {
		t.Errorf("%s built by a bare composite literal at %s.\n"+
			"Call %s() and assign what you need instead: the literal skips every map "+
			"allocation, so the service panics on the first write — and it panics in "+
			"the writer, not here (#1400, #1450).",
			g.typeName, strings.Join(offenders, ", "), g.allocator)
	}
}

func TestPermissionServiceIsNeverBuiltByBareLiteral(t *testing.T) {
	assertNeverBuiltByBareLiteral(t, guardedConstructions()[0])
}

func TestSessionDetectorIsNeverBuiltByBareLiteral(t *testing.T) {
	assertNeverBuiltByBareLiteral(t, guardedConstructions()[1])
}

// TestGuardedConstructionTableMatchesItsTests pins the two indexes above to the
// rows they are meant to name. Without it, inserting a row at the top would
// silently point both tests at the wrong type — and both would still pass,
// because each row's guard is satisfied by its own allocator.
func TestGuardedConstructionTableMatchesItsTests(t *testing.T) {
	want := []string{"PermissionService", "SessionDetector"}
	got := guardedConstructions()
	if len(got) != len(want) {
		t.Fatalf("guardedConstructions has %d rows, the tests below name %d — "+
			"a new row needs its own Test…IsNeverBuiltByBareLiteral and "+
			"Test…InitialisesEveryOwnedMap", len(got), len(want))
	}
	for i, name := range want {
		if got[i].typeName != name {
			t.Errorf("row %d is %s, but the tests index it as %s", i, got[i].typeName, name)
		}
	}
}

// ---------------------------------------------------------------------------
// Guard 2 — the reflection walk
// ---------------------------------------------------------------------------

// assertAllocatorLeavesNothingUnusable walks the struct by reflection rather
// than naming today's fields, so it fails when a FUTURE map- or channel-typed
// field is added and forgotten in the allocator. That is the half of #1400 /
// #1450 the source scan cannot reach.
func assertAllocatorLeavesNothingUnusable(t *testing.T, g guardedConstruction) {
	t.Helper()
	for _, path := range g.paths() {
		t.Run(path.name, func(t *testing.T) {
			v := path.value.Elem()
			checked := assertNoNilOwnedMaps(t, g, path.name, v, "")
			if checked == 0 {
				t.Fatal("no map- or channel-typed fields were checked — the reflection " +
					"walk is not seeing the struct, so its silence proves nothing")
			}
			assertNonZeroFields(t, g, path.name, v)
		})
	}
}

func TestNewPermissionServiceInitialisesEveryOwnedMap(t *testing.T) {
	assertAllocatorLeavesNothingUnusable(t, guardedConstructions()[0])
}

func TestNewSessionDetectorInitialisesEveryOwnedMap(t *testing.T) {
	assertAllocatorLeavesNothingUnusable(t, guardedConstructions()[1])
}

// emptyPermStore is the smallest thing NewPermissionService will accept: it
// loads an empty set and reports no error, so the walk sees the constructor's
// normal path rather than its error path.
type emptyPermStore struct{}

func (emptyPermStore) Load() (permission.Set, error) { return permission.Set{}, nil }
func (emptyPermStore) Save(permission.Set) error     { return nil }

// assertNoNilOwnedMaps walks v's map- and channel-typed fields, descending into
// embedded structs (whose own fields are promoted, so a nil map inside one is
// indistinguishable at the use site from a nil map on the outer struct). It
// returns how many fields it actually checked, so the caller can refuse to
// treat an empty walk as a pass.
func assertNoNilOwnedMaps(t *testing.T, g guardedConstruction, ctor string, v reflect.Value, prefix string) int {
	t.Helper()
	typ := v.Type()
	checked := 0
	for i := range typ.NumField() {
		f := typ.Field(i)
		name := prefix + f.Name
		if f.Anonymous && f.Type.Kind() == reflect.Struct {
			checked += assertNoNilOwnedMaps(t, g, ctor, v.Field(i), name+".")
			continue
		}
		switch f.Type.Kind() {
		case reflect.Map, reflect.Chan:
		default:
			continue
		}
		if reason, exempt := g.nilTolerant[name]; exempt {
			t.Logf("%s: nil tolerated — %s", name, reason)
			continue
		}
		checked++
		if v.Field(i).IsNil() {
			t.Errorf("%s leaves %s (%s) nil. Every write to it panics with "+
				"\"assignment to entry in nil map\", far from here. Either allocate it "+
				"in the allocator, or add it to nilTolerant with the reason it is "+
				"safe to leave nil (#1400, #1450).", ctor, name, f.Type)
		}
	}
	return checked
}

// assertNonZeroFields checks the fields the reflection walk above structurally
// cannot find: a zero duration, a nil pointer and a nil func are ordinary on
// most fields, so which ones are unusable at zero is knowledge that has to be
// declared. Every name in mustBeNonZero must exist on the struct — a typo, or a
// field renamed out from under the table, would otherwise silently check
// nothing.
func assertNonZeroFields(t *testing.T, g guardedConstruction, ctor string, v reflect.Value) {
	t.Helper()
	typ := v.Type()
	for name, reason := range g.mustBeNonZero {
		f, ok := typ.FieldByName(name)
		if !ok {
			t.Errorf("mustBeNonZero names %s.%s, which does not exist — the entry "+
				"checks nothing. Rename or remove it.", g.typeName, name)
			continue
		}
		if v.FieldByIndex(f.Index).IsZero() {
			t.Errorf("%s leaves %s (%s) at its zero value: %s (#1400, #1450).",
				ctor, name, f.Type, reason)
		}
	}
}
