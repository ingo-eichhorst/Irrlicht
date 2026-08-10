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

// This file covers issue #1400: a PermissionService built by a bare composite
// literal instead of by newPermissionService skips every map allocation, and
// panics on the first effect that writes one.
//
// The two tests here are deliberately different in kind, because the hazard has
// two halves and neither test sees the other's:
//
//   - TestPermissionServiceIsNeverBuiltByBareLiteral guards the CONSTRUCTION
//     SITES — it scans this package's own source, so it fires on a literal that
//     no test ever executes. That matters because the quiet case is a suite
//     which only drives Remove paths, or permissions with no effect closures:
//     it constructs the trap and never springs it, leaving the panic for whoever
//     adds an unrelated test months later.
//   - TestNewPermissionServiceInitialisesEveryOwnedMap guards the ALLOCATOR
//     ITSELF — a new map- or channel-typed field added to the struct and
//     forgotten in newPermissionService is a hole the source scan cannot see,
//     because every construction site is still perfectly legal.
//
// Neither is a compile-time guard, and that is not a shortfall of effort. The
// issue floated an unexported zero-width field to make the literal
// uncompilable, but it would be inert here: every field of PermissionService is
// already unexported, so Go already forbids another package from setting one in
// a literal. The one spelling that still compiles from outside is
// `&services.PermissionService{}` with no fields at all — which is not harmless
// just because it populates nothing: it is a fully-nil service that panics on
// first use, and it is what an external test in this same directory
// (`package services_test`) would naturally reach for. The scan below matches
// it. And the literal this issue was actually filed about lives in THIS
// package, where unexported fields are freely settable and no unexported marker
// field could ever have reached it.

// permissionServiceAllocator is the one function allowed to write a
// PermissionService composite literal.
const permissionServiceAllocator = "newPermissionService"

// permissionServiceTypeName is matched textually because the scan is a source
// scan: it deliberately has no type information, so that it stays cheap and
// keeps working on a file that does not compile.
const permissionServiceTypeName = "PermissionService"

// namesPermissionServiceDirectly reports whether the type expression IS the
// struct type. The *ast.SelectorExpr arm is what makes the scan see
// `services.PermissionService{}` — the spelling the `package services_test`
// files in this same directory would use, and the only literal an external
// package can still write now that every field is unexported.
func namesPermissionServiceDirectly(t ast.Expr) bool {
	switch v := t.(type) {
	case *ast.Ident:
		return v.Name == permissionServiceTypeName
	case *ast.StarExpr:
		return namesPermissionServiceDirectly(v.X)
	case *ast.SelectorExpr:
		return v.Sel.Name == permissionServiceTypeName
	}
	return false
}

// containerElementNamesPermissionService reports whether the type expression is
// a slice, array or map whose ELEMENT is the service — the case where the
// constructions are the container's elements rather than the container itself,
// and where each element may elide its type (`[]*PermissionService{{...}}`) and
// so carry nothing of its own for the scan to match.
//
// Deliberately narrower than "the type expression mentions PermissionService
// anywhere": a table-driven test's `[]struct{ svc *PermissionService }{...}`
// mentions it too, while its elements hold values that came FROM a constructor.
// Matching that would be a false positive on ordinary, correct test code — and
// a guard that cries wolf on the right thing gets deleted.
func containerElementNamesPermissionService(t ast.Expr) bool {
	switch v := t.(type) {
	case *ast.ArrayType:
		return namesPermissionServiceDirectly(v.Elt)
	case *ast.MapType:
		return namesPermissionServiceDirectly(v.Value)
	}
	return false
}

// unwrapElement strips the `&` and the `key:` a container element may carry, so
// that both `[]*PermissionService{{...}}` and
// `map[string]*PermissionService{"a": {...}}` reach the inner literal. An
// element written with an elided type has CompositeLit.Type == nil, which is
// precisely why elements are found through their container rather than on their
// own.
func unwrapElement(e ast.Expr) ast.Expr {
	if kv, ok := e.(*ast.KeyValueExpr); ok {
		e = kv.Value
	}
	if u, ok := e.(*ast.UnaryExpr); ok && u.Op == token.AND {
		e = u.X
	}
	return e
}

// allocatorDecl returns the allowed allocator's declaration in this file, or
// nil. Its extent is what tells a legitimate literal from a bypass: the check
// is positional rather than "which function body am I walking", because walking
// only function bodies would skip every package-scope declaration, and
// `var x = &PermissionService{}` at package scope is a perfectly ordinary shape
// for a shared test fixture — which is exactly where the original offender
// lived.
func allocatorDecl(file *ast.File) *ast.FuncDecl {
	for _, d := range file.Decls {
		if fn, ok := d.(*ast.FuncDecl); ok && fn.Recv == nil && fn.Name.Name == permissionServiceAllocator {
			return fn
		}
	}
	return nil
}

// scanFileForServiceLiterals reports every PermissionService composite literal
// in one file that is not inside the allocator, and whether the allocator's own
// literal was seen (the caller's vacuity guard).
func scanFileForServiceLiterals(fset *token.FileSet, path string, file *ast.File) (offenders []string, sawAllocator bool) {
	alloc := allocatorDecl(file)

	report := func(n ast.Node) {
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
			return true
		}
		switch {
		case namesPermissionServiceDirectly(lit.Type):
			report(lit)
		case containerElementNamesPermissionService(lit.Type):
			// Each element literal constructs one service, and an element
			// written with an elided type carries no type of its own to match,
			// so it is only reachable from its container.
			for _, el := range lit.Elts {
				if inner, ok := unwrapElement(el).(*ast.CompositeLit); ok {
					report(inner)
				}
			}
		}
		return true
	})
	return offenders, sawAllocator
}

// TestPermissionServiceIsNeverBuiltByBareLiteral fails on any PermissionService
// composite literal in this package outside the allocator.
//
// It reports the file and line of the literal. That is the entire point: the
// runtime failure a bypass produces reports neither. It surfaces as
// "assignment to entry in nil map" with a stack whose deepest frame is
// recordEffectResult, several calls away from the construction that caused it,
// and only if some test happens to drive an effect that writes a map.
func TestPermissionServiceIsNeverBuiltByBareLiteral(t *testing.T) {
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
		for path, file := range pkg.Files {
			found, sawAllocator := scanFileForServiceLiterals(fset, path, file)
			offenders = append(offenders, found...)
			seenAllocator = seenAllocator || sawAllocator
		}
	}

	// Vacuity guard. A scan that matches nothing at all — a renamed type, a
	// parser that silently read the wrong directory — would otherwise report a
	// clean package while checking nothing, which is the failure mode this
	// whole file exists to remove.
	if !seenAllocator {
		t.Fatalf("found no PermissionService literal inside %s(); the scan is not "+
			"looking where it thinks it is, so its silence proves nothing",
			permissionServiceAllocator)
	}

	if len(offenders) > 0 {
		t.Errorf("PermissionService built by a bare composite literal at %s.\n"+
			"Call %s() and assign what you need instead: the literal skips every map "+
			"allocation, so the service panics on the first effect that writes one — "+
			"and it panics in recordEffectResult, not here (#1400).",
			strings.Join(offenders, ", "), permissionServiceAllocator)
	}
}

// nilTolerantFields names each field newPermissionService deliberately leaves
// nil, with the reason. A field is exempt only by being written down here —
// the default is that it must be initialised, so a new map- or channel-typed
// field is covered by a test that already exists rather than by whoever adds it
// remembering to write one.
var nilTolerantFields = map[string]string{
	"factories": "dependency-supplied (nil means demo mode: no watcher ever starts) " +
		"and only ever read — startWatching does one lookup, and a nil-map read is legal",
}

// TestNewPermissionServiceInitialisesEveryOwnedMap walks the struct by
// reflection rather than naming today's fields, so it fails when a FUTURE map-
// or channel-typed field is added to PermissionService and forgotten in the
// allocator. That is the half of #1400 the source scan cannot reach.
func TestNewPermissionServiceInitialisesEveryOwnedMap(t *testing.T) {
	// BOTH construction paths, because they can disagree. The allocator makes
	// every map, and NewPermissionService then overwrites eight fields from
	// deps — so a future dep-supplied map (the shape `s.factories =
	// deps.Factories` already has) would re-nil a field the allocator had
	// allocated, and a walk that only ever saw the allocator would stay green
	// while production panicked.
	for _, tc := range []struct {
		name string
		svc  *PermissionService
	}{
		{permissionServiceAllocator, newPermissionService()},
		{"NewPermissionService", NewPermissionService(PermissionServiceDeps{
			Store: emptyPermStore{}, Log: &gateLog{},
		})},
	} {
		t.Run(tc.name, func(t *testing.T) {
			checked := assertNoNilOwnedMaps(t, tc.name, reflect.ValueOf(tc.svc).Elem(), "")
			if checked == 0 {
				t.Fatal("no map- or channel-typed fields were checked — the reflection " +
					"walk is not seeing the struct, so its silence proves nothing")
			}

			// detectInterval is not a map, but it is the same shape of bug:
			// Start's time.NewTicker panics outright on a non-positive
			// interval, so a service that skipped the allocator is unstartable
			// as well as nil-unsafe. This is why the issue's "a nil-guard makes
			// the zero value usable" is not quite true for this type.
			if tc.svc.detectInterval <= 0 {
				t.Errorf("%s left detectInterval at %v; time.NewTicker panics on a "+
					"non-positive interval", tc.name, tc.svc.detectInterval)
			}
		})
	}
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
func assertNoNilOwnedMaps(t *testing.T, ctor string, v reflect.Value, prefix string) int {
	t.Helper()
	typ := v.Type()
	checked := 0
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		name := prefix + f.Name
		if f.Anonymous && f.Type.Kind() == reflect.Struct {
			checked += assertNoNilOwnedMaps(t, ctor, v.Field(i), name+".")
			continue
		}
		switch f.Type.Kind() {
		case reflect.Map, reflect.Chan:
		default:
			continue
		}
		if reason, exempt := nilTolerantFields[name]; exempt {
			t.Logf("%s: nil tolerated — %s", name, reason)
			continue
		}
		checked++
		if v.Field(i).IsNil() {
			t.Errorf("%s leaves %s (%s) nil. Every write to it panics with "+
				"\"assignment to entry in nil map\", far from here. Either allocate it "+
				"in the allocator, or add it to nilTolerantFields with the reason it is "+
				"safe to leave nil (#1400).", ctor, name, f.Type)
		}
	}
	return checked
}
