// architecture_test.go lives at the module root, not in a subpackage, so
// packages.Load's "./..." pattern can see every package in the module from
// a single load.
//
// It is not the only architecture rule in this directory. The rule table below
// is about the IMPORT GRAPH — which packages a package may depend on — and
// loads with NeedName|NeedImports. architecture_hookbody_test.go asks a
// different kind of question, about which EXPRESSIONS a function may contain
// (#1389: only hookjson.DecodeConfined may read an inbound request body), and
// needs syntax and type information over a narrow pattern. Adding a rule of the
// first kind means adding a row here; a rule of the second kind does not fit
// this table at all.
package core_test

import (
	"strings"
	"testing"

	"golang.org/x/tools/go/packages"
)

// layeringRule pairs a source-package prefix with the import prefixes it may
// not reach into.
type layeringRule struct {
	name              string
	sourcePrefix      string
	forbiddenPrefixes []string
}

func TestArchitectureLayerImportDirection(t *testing.T) {
	cfg := &packages.Config{Mode: packages.NeedName | packages.NeedImports, Dir: "."}
	pkgs, err := packages.Load(cfg, "./...")
	if err != nil {
		t.Fatalf("packages.Load: %v", err)
	}
	if n := packages.PrintErrors(pkgs); n > 0 {
		t.Fatalf("packages.Load reported %d package error(s); build is broken", n)
	}
	if len(pkgs) == 0 {
		t.Fatalf("packages.Load returned no packages for pattern \"./...\"")
	}

	rules := []layeringRule{
		{
			name:              "domain must not import ports, adapters, or application",
			sourcePrefix:      "irrlicht/core/domain/",
			forbiddenPrefixes: []string{"irrlicht/core/ports/", "irrlicht/core/adapters/", "irrlicht/core/application/"},
		},
		{
			name:              "ports must not import adapters",
			sourcePrefix:      "irrlicht/core/ports/",
			forbiddenPrefixes: []string{"irrlicht/core/adapters/"},
		},
		{
			name:              "application/services must reach adapters through ports, not directly into adapters/inbound",
			sourcePrefix:      "irrlicht/core/application/services/",
			forbiddenPrefixes: []string{"irrlicht/core/adapters/inbound/"},
		},
		// pkg/ is the shared leaf layer: stdlib, other pkg/ packages, and
		// domain types. It is depended on from every layer that imports
		// anything at all — domain (2 packages), adapters (62), application
		// (6), cmd (4) — so an edge out of pkg/ into adapters/ or
		// application/ inverts the direction for all of them at once. The
		// domain edge is the sharpest: domain/agent imports pkg/tailer, so
		// pkg/ reaching outward would put adapters transitively underneath
		// domain.
		//
		// Added by #1391, where the obvious fix for a shared JSONC decode was
		// to have pkg/tailer import the hookjson adapter. Nothing in this table
		// bound pkg/ at the time, so that import would have passed this test
		// and quietly established the repo's first pkg -> adapters edge. It
		// happens to be caught today by the compiler — domain/agent imports
		// pkg/tailer and hookjson imports domain/agent, so the edge closes an
		// import cycle — but that is an accident of which adapter it was, not a
		// rule. An adapter that imports no domain package would have compiled
		// fine.
		//
		// ports/ is deliberately absent from the forbidden list. Today the two
		// do not touch in either direction — no pkg/ package imports ports/,
		// and no ports/ package imports pkg/ — but a leaf implementing a port
		// interface would be legitimate, and this rule is meant to pin the
		// direction that is actually wrong rather than the widest one
		// available.
		//
		// Like every rule here it sees only non-test imports: packages.Load
		// runs without Tests, so a pkg/**/*_test.go importing an adapter would
		// pass. None does today. That gap is pre-existing and shared by all
		// four rules, not introduced with this one.
		{
			name:              "pkg must not import adapters or application",
			sourcePrefix:      "irrlicht/core/pkg/",
			forbiddenPrefixes: []string{"irrlicht/core/adapters/", "irrlicht/core/application/"},
		},
	}

	for _, pkg := range pkgs {
		checkPackageLayering(t, pkg, rules)
	}
}

// checkPackageLayering reports a layering violation for every rule whose
// sourcePrefix matches pkg and which directly imports one of that rule's
// forbidden prefixes.
func checkPackageLayering(t *testing.T, pkg *packages.Package, rules []layeringRule) {
	for _, rule := range rules {
		if !hasLayerPrefix(pkg.PkgPath, rule.sourcePrefix) {
			continue
		}
		for importPath := range pkg.Imports { // map key is the direct import path
			if matchesAnyLayerPrefix(importPath, rule.forbiddenPrefixes) {
				t.Errorf("layering violation (%s): %q imports %q", rule.name, pkg.PkgPath, importPath)
			}
		}
	}
}

// matchesAnyLayerPrefix reports whether path falls under any of prefixes.
func matchesAnyLayerPrefix(path string, prefixes []string) bool {
	for _, prefix := range prefixes {
		if hasLayerPrefix(path, prefix) {
			return true
		}
	}
	return false
}

func hasLayerPrefix(path, prefix string) bool {
	return path == strings.TrimSuffix(prefix, "/") || strings.HasPrefix(path, prefix)
}
