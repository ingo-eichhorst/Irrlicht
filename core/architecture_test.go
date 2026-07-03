// architecture_test.go lives at the module root, not in a subpackage, so
// packages.Load's "./..." pattern can see every package in the module from
// a single load.
package core_test

import (
	"strings"
	"testing"

	"golang.org/x/tools/go/packages"
)

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

	rules := []struct {
		name              string
		sourcePrefix      string
		forbiddenPrefixes []string
	}{
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
	}

	for _, pkg := range pkgs {
		for _, rule := range rules {
			if !hasLayerPrefix(pkg.PkgPath, rule.sourcePrefix) {
				continue
			}
			for importPath := range pkg.Imports { // map key is the direct import path
				for _, forbidden := range rule.forbiddenPrefixes {
					if hasLayerPrefix(importPath, forbidden) {
						t.Errorf("layering violation (%s): %q imports %q", rule.name, pkg.PkgPath, importPath)
					}
				}
			}
		}
	}
}

func hasLayerPrefix(path, prefix string) bool {
	return path == strings.TrimSuffix(prefix, "/") || strings.HasPrefix(path, prefix)
}
