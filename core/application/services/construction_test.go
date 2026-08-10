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
// would naturally reach for. The scan below matches it — but only in THIS
// directory: the walk is `ParseDir(".")`, so it reaches the `package
// services_test` files beside it and nothing in core/e2e or core/cmd. Such a
// literal would compile there and go unseen; none exists today. And the literals these
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
	//
	// Note the POLARITY, which is the opposite of nilTolerant's and is a
	// deliberate trade rather than an oversight. nilTolerant is an opt-OUT, so
	// a new map is covered by default. This is an opt-IN, so a new
	// unusable-at-zero field is NOT covered until someone lists it. Inverting
	// it — "every non-map/chan field must be non-zero unless declared" — was
	// considered and rejected: the zero value of most fields here is not merely
	// legal but the documented intent (`costTracker`, `historyTracker`,
	// `cacheBloat`, `hookLiveness`, `recorder`, `consentGate` and `now` are all
	// "nil = disabled"), so the flip costs ~30 exemption entries across the two
	// types whose reason is uniformly "optional dependency" and drowns the five
	// that carry real information. Deriving the set from the AST does not help
	// either: a field forgotten everywhere is precisely one that is assigned
	// nowhere, which is indistinguishable from an optional one.
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

// guardedFor returns the table row for a type name, and fails loudly if there
// is none.
//
// The live guards look their row up BY NAME rather than by position. An earlier
// draft indexed the slice (`guardedConstructions()[0]`) and needed a third test
// whose only job was to pin the row order — coverage of nothing, defending a
// hazard that a lookup simply does not have, and a regression against the
// named-row table #1444 already had.
func guardedFor(t *testing.T, typeName string) guardedConstruction {
	t.Helper()
	for _, g := range guardedConstructions() {
		if g.typeName == typeName {
			return g
		}
	}
	t.Fatalf("no guardedConstructions row for %s — the guard below would check nothing", typeName)
	return guardedConstruction{}
}

// guardedConstructions is the table both guards run over. A third type joins by
// adding a row here plus the two one-line tests that name it.
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

// collectTypeDecls maps every type name declared in the given files to the type
// expression it names, so the scan can see through
// `type detList []*SessionDetector`. Without it a defined container type is a
// bare *ast.Ident that matches nothing, and `detList{{}}` constructs a service
// the scan reports as clean.
//
// It walks with ast.Inspect rather than over file.Decls, so a type declared
// INSIDE a function body is collected too. Package scope alone was an
// asymmetry, not a policy: a table-driven test declaring `type tc struct{…}`
// beside its table is ordinary Go, and identical source being caught at package
// scope but silent one indent deeper is the kind of hole that gets found by
// accident years later. Scope is deliberately ignored — a local type shadowing
// a package-level one of the same name is vanishingly rare, and the only
// consequence of over-resolving is a report on a literal that elided its type,
// which is always a construction of whatever the container says it is.
func collectTypeDecls(files map[string]*ast.File) map[string]ast.Expr {
	decls := map[string]ast.Expr{}
	for _, file := range files {
		ast.Inspect(file, func(n ast.Node) bool {
			recordTypeSpecs(n, decls)
			return true
		})
	}
	return decls
}

// recordTypeSpecs adds every type this node declares, if it declares any.
func recordTypeSpecs(n ast.Node, into map[string]ast.Expr) {
	gd, ok := n.(*ast.GenDecl)
	if !ok || gd.Tok != token.TYPE {
		return
	}
	for _, spec := range gd.Specs {
		if ts, ok := spec.(*ast.TypeSpec); ok {
			into[ts.Name.Name] = ts.Type
		}
	}
}

// typeResolutionDepth bounds the declaration chase below.
//
// Bounded rather than cycle-detected, but NOT for the reason it is tempting to
// give: `type a *a` and the mutual pair `type p *q; type q *p` both compile, so
// "a cycle would not have compiled" is simply false. The bound is what makes
// resolution total on any input, compiling or not — which matters because this
// is a source scan and is meant to keep working on a file mid-edit.
const typeResolutionDepth = 10

// literalScan carries the per-package state the recursive walk needs.
type literalScan struct {
	typeName string
	decls    map[string]ast.Expr
	report   func(ast.Node)
}

// resolve strips the spellings that stand between a type expression and its
// structure — parentheses, pointers, and chains of type declarations — until it
// reaches something the caller can switch on. It is the ONE place any
// resolution rule lives, so a hardening cannot reach some callers and not
// others; `deref` is the single axis its two callers disagree on.
//
// Pointers are stripped HERE, inside the bounded loop, rather than by a
// recursive arm in names(). That placement does two jobs: it makes names()
// total (a recursive arm would run forever on `type a *a`, which compiles), and
// it lets the container switch in visit() see through a pointer-to-container
// element type such as `[]*[]*SessionDetector{{{}}}`.
//
// It stops at the guarded type's own name, which is load-bearing: the
// declaration table holds `SessionDetector` too, mapped to its *ast.StructType,
// and chasing that far would resolve every literal of the guarded type into an
// anonymous struct that names nothing.
// derefPointers / keepPointers name resolve's second argument at its call
// sites, because `resolve(t, true)` says nothing about what is being decided.
const (
	derefPointers = true
	keepPointers  = false
)

func (s *literalScan) resolve(t ast.Expr, deref bool) ast.Expr {
	for range typeResolutionDepth {
		switch v := t.(type) {
		case *ast.ParenExpr:
			t = v.X
		case *ast.StarExpr:
			if !deref {
				return t
			}
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

// isGuarded reports whether an already-resolved expression names the guarded
// struct.
//
// The *ast.SelectorExpr arm is what makes the scan see
// `services.SessionDetector{}` — the spelling the `package services_test` files
// in this same directory use, and the only literal an external package can
// write now that every field is unexported. It matches on the selector alone
// rather than on `pkg.Type`, so an import alias cannot slip past. That is a
// deliberate trade: a same-named type in some other package would be reported
// too, which repo-wide is not a live risk (these two declarations are the only
// ones) and would be a false positive worth taking over an alias bypass.
func (s *literalScan) isGuarded(t ast.Expr) bool {
	switch v := t.(type) {
	case *ast.Ident:
		return v.Name == s.typeName
	case *ast.SelectorExpr:
		return v.Sel.Name == s.typeName
	}
	return false
}

// names reports whether the type expression denotes the guarded struct, through
// any number of pointers.
func (s *literalScan) names(t ast.Expr) bool {
	return s.isGuarded(s.resolve(t, derefPointers))
}

// namesValueType is names() minus the pointer stripping: it reports whether the
// expression denotes the guarded struct BY VALUE.
//
// The distinction only matters for the two non-literal spellings below.
// `new(SessionDetector)` and `var d SessionDetector` both produce the trap,
// while `new(*SessionDetector)` and `var d *SessionDetector` produce a harmless
// nil pointer — and `core/cmd/irrlichd/main.go` holds one of the latter.
func (s *literalScan) namesValueType(t ast.Expr) bool {
	return s.isGuarded(s.resolve(t, keepPointers))
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
	switch v := s.resolve(typ, derefPointers).(type) {
	case *ast.ArrayType:
		s.descendSequence(lit, v)
	case *ast.MapType:
		s.descendMap(lit, v)
	}
	// Any other type — most importantly *ast.StructType — is deliberately not
	// descended into. A table-driven test's
	// `[]struct{ svc *PermissionService }{...}` mentions the guarded type in
	// its element type while its elements hold values that came FROM a
	// constructor, and reporting that would be a false positive on ordinary,
	// correct test code. A guard that cries wolf on the right thing gets
	// deleted. Nothing is lost to elision: a struct field value cannot elide
	// its type, so any literal in there spells its own and is found by the
	// top-level walk.
	//
	// Two shapes are knowingly outside this: a struct that EMBEDS the guarded
	// type by value (`type w struct{ SessionDetector }; w{}` really does
	// construct one), and a generic instantiation (`box[*SessionDetector]{{}}`,
	// an *ast.IndexExpr this resolves nothing for). Neither is reachable in
	// this package today — SessionDetector carries three sync.Mutex, so
	// embedding it by value invites vet's copylocks on the first copy, and
	// there are no generics here — and both are named rather than left for the
	// next reader to discover.
}

// descendSequence follows a slice or array literal's elements. Len is what
// distinguishes the two and the elision rule is identical for both, so one arm
// covers them.
func (s *literalScan) descendSequence(lit *ast.CompositeLit, typ *ast.ArrayType) {
	for _, el := range lit.Elts {
		s.descend(el, typ.Elt)
	}
}

// descendMap follows a map literal's keys AND values — a key can be an elided
// construction too (`map[*SessionDetector]bool{{}: true}`).
func (s *literalScan) descendMap(lit *ast.CompositeLit, typ *ast.MapType) {
	for _, el := range lit.Elts {
		kv, ok := el.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		s.descend(kv.Key, typ.Key)
		s.descend(kv.Value, typ.Value)
	}
}

// descend follows one container edge into an element that ELIDED its type.
// An element that spells its own type is skipped here and found by the
// top-level walk instead, so nothing is reported twice.
//
// The *ast.KeyValueExpr strip is not only for maps: Go permits INDEX keys in
// slice and array literals, so `[]*SessionDetector{0: {}}` and
// `[3]*SessionDetector{1: {}}` are elided constructions whose element is a
// key-value pair. #1444's matcher stripped these and this one initially did
// not, which made the rewrite quietly narrower than what it replaced — the one
// regression its own eighteen planted shapes could not have caught, because
// every one of them was an ADDITION to coverage. The corpus below now pins the
// predecessor's cases as locks for exactly that reason.
func (s *literalScan) descend(e ast.Expr, typ ast.Expr) {
	if kv, ok := e.(*ast.KeyValueExpr); ok {
		e = kv.Value
	}
	if lit, ok := e.(*ast.CompositeLit); ok && lit.Type == nil {
		s.visit(lit, typ)
	}
}

// reportNonLiteralZeroValues covers the two spellings that produce the very
// same fully-nil struct without ever writing a composite literal:
// `new(SessionDetector)` and `var d SessionDetector`. Both were verified to
// panic identically ("assignment to entry in nil map"), and `new(T)` in
// particular is idiomatic Go that an unfamiliar author is likelier to reach for
// than any of the nested-elision shapes.
//
// Only the by-value spellings count. `new(*SessionDetector)` and
// `var d *SessionDetector` are nil pointers, harmless, and the latter is live
// in core/cmd/irrlichd/main.go.
func (s *literalScan) reportNonLiteralZeroValues(n ast.Node) {
	switch v := n.(type) {
	case *ast.CallExpr:
		if s.isNewOfGuardedType(v) {
			s.report(v)
		}
	case *ast.ValueSpec:
		if s.isZeroValueVarDecl(v) {
			s.report(v)
		}
	}
}

// isNewOfGuardedType matches `new(SessionDetector)`.
func (s *literalScan) isNewOfGuardedType(call *ast.CallExpr) bool {
	id, ok := call.Fun.(*ast.Ident)
	if !ok || id.Name != "new" {
		return false
	}
	if len(call.Args) != 1 {
		return false
	}
	return s.namesValueType(call.Args[0])
}

// isZeroValueVarDecl matches `var d SessionDetector` with no initialiser. With
// one, the value came from somewhere the scan already judges on its own merits.
func (s *literalScan) isZeroValueVarDecl(spec *ast.ValueSpec) bool {
	if spec.Type == nil || len(spec.Values) != 0 {
		return false
	}
	return s.namesValueType(spec.Type)
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
			// Not a literal at all, or a literal with no type of its own. The
			// latter is an elided container element: unreachable from here,
			// because only its container knows what type it has, so it is
			// visited through scan.descend instead.
			scan.reportNonLiteralZeroValues(n)
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
		decls := collectTypeDecls(pkg.Files)
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
	assertNeverBuiltByBareLiteral(t, guardedFor(t, "PermissionService"))
}

func TestSessionDetectorIsNeverBuiltByBareLiteral(t *testing.T) {
	assertNeverBuiltByBareLiteral(t, guardedFor(t, "SessionDetector"))
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
			w := &nilMapWalk{t: t, g: g, ctor: path.name, seen: map[string]bool{}}
			checked := w.walk(v, "")
			if checked == 0 {
				t.Fatal("no map- or channel-typed fields were checked — the reflection " +
					"walk is not seeing the struct, so its silence proves nothing")
			}
			for name := range g.nilTolerant {
				if !w.seen[name] {
					t.Errorf("nilTolerant names %s.%s, which is not a map- or channel-typed "+
						"field of this struct — the exemption grants nothing. Rename or "+
						"remove it.", g.typeName, name)
				}
			}
			assertNonZeroFields(t, g, path.name, v)
		})
	}
}

func TestNewPermissionServiceInitialisesEveryOwnedMap(t *testing.T) {
	assertAllocatorLeavesNothingUnusable(t, guardedFor(t, "PermissionService"))
}

func TestNewSessionDetectorInitialisesEveryOwnedMap(t *testing.T) {
	assertAllocatorLeavesNothingUnusable(t, guardedFor(t, "SessionDetector"))
}

// emptyPermStore is the smallest thing NewPermissionService will accept: it
// loads an empty set and reports no error, so the walk sees the constructor's
// normal path rather than its error path.
type emptyPermStore struct{}

func (emptyPermStore) Load() (permission.Set, error) { return permission.Set{}, nil }
func (emptyPermStore) Save(permission.Set) error     { return nil }

// nilMapWalk walks a construction's map- and channel-typed fields, descending into
// embedded structs (whose own fields are promoted, so a nil map inside one is
// indistinguishable at the use site from a nil map on the outer struct).
//
// It records every field name it reached in seen, so the caller can refuse to
// treat an empty walk as a pass AND can check that every nilTolerant entry
// actually named a field it walked past. A stale nilTolerant key fails safe on
// its own — the field simply starts being checked — but it is still a claim
// about this struct that quietly stopped being true, and the neighbouring
// mustBeNonZero polices exactly that. Two maps in one table disagreeing about
// whether their keys are verified is the kind of asymmetry that reads as a
// decision when it is an omission.
type nilMapWalk struct {
	t    *testing.T
	g    guardedConstruction
	ctor string
	seen map[string]bool // every map/chan field name reached, at any depth
}

func (w *nilMapWalk) walk(v reflect.Value, prefix string) int {
	w.t.Helper()
	typ := v.Type()
	checked := 0
	for i := range typ.NumField() {
		f := typ.Field(i)
		name := prefix + f.Name
		if f.Anonymous && f.Type.Kind() == reflect.Struct {
			checked += w.walk(v.Field(i), name+".")
			continue
		}
		if f.Type.Kind() != reflect.Map && f.Type.Kind() != reflect.Chan {
			continue
		}
		w.seen[name] = true
		if reason, exempt := w.g.nilTolerant[name]; exempt {
			w.t.Logf("%s: nil tolerated — %s", name, reason)
			continue
		}
		checked++
		if v.Field(i).IsNil() {
			w.t.Errorf("%s leaves %s (%s) nil. Every write to it panics with "+
				"\"assignment to entry in nil map\", far from here. Either allocate it "+
				"in the allocator, or add it to nilTolerant with the reason it is "+
				"safe to leave nil (#1400, #1450).", w.ctor, name, f.Type)
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

// ---------------------------------------------------------------------------
// The shape corpus — locks on what the source scan must and must not report
// ---------------------------------------------------------------------------

// literalShapes is the corpus itself, lifted out of the test body so the
// assertions stay readable beside it rather than under a hundred lines of
// data. Each case is a whole Go file: __TYPE__ is substituted with the guarded
// type's name and __ALLOC__ with its allocator's, so every shape runs against
// both rows of the table.
func literalShapes() []struct {
	name string
	src  string
	want int // offenders the scan must report
} {
	return []struct {
		name string
		src  string
		want int // offenders the scan must report
	}{
		// ---- caught: the plain spellings -------------------------------
		{"bare pointer in a function", `package p
	func f() { _ = &__TYPE__{} }`, 1},
		{"bare value literal in a function", `package p
	func f() { _ = __TYPE__{} }`, 1},
		{"package-scope var", `package p
	var x = &__TYPE__{}`, 1},
		{"struct field value spelling its own type", `package p
	var x = struct{ d *__TYPE__ }{d: &__TYPE__{}}`, 1},
		{"returned from a package-scope closure", `package p
	var x = func() *__TYPE__ { return &__TYPE__{} }`, 1},

		// ---- caught: elision, one level (#1444's cases, as locks) ------
		{"slice element, type elided", `package p
	var x = []*__TYPE__{{}}`, 1},
		{"map value, type elided", `package p
	var x = map[string]*__TYPE__{"a": {}}`, 1},
		{"array element, type elided", `package p
	var x = [2]*__TYPE__{{}, {}}`, 2},
		{"map key, type elided", `package p
	var x = map[*__TYPE__]bool{{}: true}`, 1},

		// ---- caught: elision behind an INDEX KEY (the #1450 regression) --
		{"keyed slice element", `package p
	var x = []*__TYPE__{0: {}}`, 1},
		{"keyed array element", `package p
	var x = [3]*__TYPE__{1: {}}`, 1},

		// ---- caught: elision, more than one level ----------------------
		{"slice of slice, elided twice", `package p
	var x = [][]*__TYPE__{{{}}}`, 1},
		{"map of slice, elided twice", `package p
	var x = map[string][]*__TYPE__{"a": {{}}}`, 1},
		{"pointer to a container element", `package p
	var x = []*[]*__TYPE__{{{}}}`, 1},

		// ---- caught: through type declarations -------------------------
		{"defined container type, package scope", `package p
	type detList []*__TYPE__
	var x = detList{{}}`, 1},
		{"type alias, package scope", `package p
	type detAlias = __TYPE__
	var x = &detAlias{}`, 1},
		{"defined container type, FUNCTION scope", `package p
	func f() {
		type detList []*__TYPE__
		_ = detList{{}}
	}`, 1},
		{"type alias, FUNCTION scope", `package p
	func f() {
		type detAlias = __TYPE__
		_ = detAlias{}
	}`, 1},

		// ---- caught: qualified from outside the package ----------------
		{"qualified selector", `package p
	var x = &services.__TYPE__{}`, 1},
		{"qualified selector under an import alias", `package p
	var x = &svc.__TYPE__{}`, 1},

		// ---- caught: the zero value without any literal at all ---------
		{"new(T)", `package p
	func f() { _ = new(__TYPE__) }`, 1},
		{"var of the struct type, no initialiser", `package p
	func f() { var d __TYPE__; _ = d }`, 1},

		// ---- reported exactly once, not twice --------------------------
		{"explicit element type inside a container", `package p
	var x = []*__TYPE__{&__TYPE__{}}`, 1},

		// ---- NOT reported: ordinary, correct code ----------------------
		{"table-driven struct holding constructor results", `package p
	var x = []struct{ d *__TYPE__ }{{d: __ALLOC__()}}`, 0},
		{"map holding constructor results", `package p
	var x = map[string]*__TYPE__{"a": __ALLOC__()}`, 0},
		{"slice holding constructor results", `package p
	var x = []*__TYPE__{__ALLOC__()}`, 0},
		{"empty elided containers", `package p
	var x = [][]*__TYPE__{{}}
	var y = map[string][]*__TYPE__{"a": {}}`, 0},
		{"new of a POINTER to the struct", `package p
	func f() { _ = new(*__TYPE__) }`, 0},
		{"var of a POINTER to the struct", `package p
	func f() { var d *__TYPE__; _ = d }`, 0},
		{"a var of the struct type WITH an initialiser", `package p
	func f() { var d *__TYPE__ = __ALLOC__(); _ = d }`, 0},
		{"a differently-named type", `package p
	var x = &__TYPE__Stub{}`, 0},
	}
}

// TestSourceScanCatchesEveryKnownShape is the permanent record of every
// construction spelling the scan is required to catch, and every ordinary shape
// it is required to leave alone.
//
// It exists because of how #1450's own regression was found. That PR planted
// eighteen shapes against the rewritten scanner and all eighteen were caught —
// but every one of them was an ADDITION to coverage, so none could notice that
// the rewrite had silently DROPPED a case #1444 handled (an index-keyed slice
// element, `[]*T{0: {}}`). A rewritten guard needs its predecessor's cases
// replayed against it as locks, not just its own new ones, and a probe planted
// by hand during one PR is evidence that expires the moment the PR merges.
//
// Each case is parsed from source rather than planted in a real file, so the
// corpus costs nothing at runtime, cannot perturb the package it guards, and
// records shapes that would not compile in place.
//
// The corpus is run against BOTH guarded types by substituting the type name,
// which is what keeps the two rows honestly symmetric: a hardening that only
// reached one of them fails here.
func TestSourceScanCatchesEveryKnownShape(t *testing.T) {
	cases := literalShapes()

	total := 0
	for _, g := range guardedConstructions() {
		for _, tc := range cases {
			t.Run(g.typeName+"/"+tc.name, func(t *testing.T) {
				// Distinct tokens, not the literal type name: substituting
				// "SessionDetector" first would also rewrite the substring
				// inside "newSessionDetector", so the allocator substitution
				// silently became a no-op that only looked correct because
				// both allocators happen to be spelled new+TypeName.
				src := strings.ReplaceAll(tc.src, "__TYPE__", g.typeName)
				src = strings.ReplaceAll(src, "__ALLOC__", g.allocator)
				fset := token.NewFileSet()
				file, err := parser.ParseFile(fset, "shape.go", src, 0)
				if err != nil {
					t.Fatalf("corpus case does not parse — fix the case, not the scan: %v\n%s", err, src)
				}
				decls := collectTypeDecls(map[string]*ast.File{"shape.go": file})
				got, _ := scanFileForLiterals(fset, "shape.go", file, g, decls)
				if len(got) != tc.want {
					t.Errorf("scan reported %d construction(s), want %d: %v\n--- source ---\n%s",
						len(got), tc.want, got, src)
				}
				total += len(got)
			})
		}
	}

	// Vacuity guard, the same shape the two live guards carry: a scanner that
	// reported nothing at all would otherwise satisfy every want-0 case and
	// look like a pass.
	if total == 0 {
		t.Fatal("the corpus reported no constructions at all — the scan is inert, " +
			"so every want-0 case above proves nothing")
	}
}
