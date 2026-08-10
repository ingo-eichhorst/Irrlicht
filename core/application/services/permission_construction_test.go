package services

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
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
// a literal (only `&services.PermissionService{}` with no fields at all
// compiles, which populates nothing and is useless). The literal this issue was
// actually filed about lives in THIS package, where unexported fields are
// freely settable and no unexported marker field can reach it.

// permissionServiceAllocator is the one function allowed to write a
// PermissionService composite literal.
const permissionServiceAllocator = "newPermissionService"

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
			for _, decl := range file.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Body == nil {
					continue
				}
				allowed := fn.Recv == nil && fn.Name.Name == permissionServiceAllocator
				ast.Inspect(fn.Body, func(n ast.Node) bool {
					lit, ok := n.(*ast.CompositeLit)
					if !ok {
						return true
					}
					id, ok := lit.Type.(*ast.Ident)
					if !ok || id.Name != "PermissionService" {
						return true
					}
					if allowed {
						seenAllocator = true
						return true
					}
					offenders = append(offenders, fmt.Sprintf("%s:%d (in %s)",
						filepath.Base(path), fset.Position(lit.Pos()).Line, fn.Name.Name))
					return true
				})
			}
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
	s := newPermissionService()
	v := reflect.ValueOf(s).Elem()
	typ := v.Type()

	checked := 0
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		switch f.Type.Kind() {
		case reflect.Map, reflect.Chan:
		default:
			continue
		}
		if reason, exempt := nilTolerantFields[f.Name]; exempt {
			t.Logf("%s: nil tolerated — %s", f.Name, reason)
			continue
		}
		checked++
		if v.Field(i).IsNil() {
			t.Errorf("%s() leaves %s (%s) nil. Every write to it panics with "+
				"\"assignment to entry in nil map\", far from here. Either allocate it "+
				"in the allocator, or add it to nilTolerantFields with the reason it is "+
				"safe to leave nil (#1400).",
				permissionServiceAllocator, f.Name, f.Type)
		}
	}

	if checked == 0 {
		t.Fatal("no map- or channel-typed fields were checked — the reflection walk " +
			"is not seeing the struct, so its silence proves nothing")
	}

	// detectInterval is not a map, but it is the same shape of bug: Start's
	// time.NewTicker panics outright on a non-positive interval, so a service
	// that skipped the allocator is unstartable as well as nil-unsafe. This is
	// why the issue's "a nil-guard makes the zero value usable" is not quite
	// true for this type.
	if s.detectInterval <= 0 {
		t.Errorf("%s() left detectInterval at %v; time.NewTicker panics on a "+
			"non-positive interval", permissionServiceAllocator, s.detectInterval)
	}
}

// TestPermissionServiceSourceScanReadsRealFiles pins the assumption the source
// scan rests on: that the test binary's working directory is the package
// directory, so ParseDir(".") reads this package rather than nothing.
func TestPermissionServiceSourceScanReadsRealFiles(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("reading the package directory: %v", err)
	}
	found := false
	for _, e := range entries {
		if e.Name() == "permission_service.go" {
			found = true
		}
	}
	if !found {
		t.Fatal("permission_service.go is not in the test's working directory; " +
			"the source scan above is reading the wrong place")
	}
}
