package session

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strconv"
	"strings"
	"testing"
)

// TestAllHookEvents_CoversEveryConstant is the guard that keeps AllHookEvents
// from becoming the very thing issue #1356 was about: a hand-maintained
// restatement of a list, drifting silently from it. AllHookEvents is the
// universe contracttesting.AssertHookDisclosureMatchesInstalled checks an
// adapter's consent copy against, so a constant missing from it weakens that
// contract silently rather than breaking anything loudly.
//
// It parses the package's source rather than scanning it with a regexp:
// constants are erased at compile time so there is nothing to reflect over, and
// a pattern tight enough to read one file's current formatting fails *open* on
// every other legal spelling — a typed const (`HookX string = "x"`), an
// ungrouped `const HookX = "x"` (live style elsewhere in this repo), or a
// constant added in another file of the package. Failing open is the one
// failure mode a drift guard cannot have.
func TestAllHookEvents_CoversEveryConstant(t *testing.T) {
	declared := hookEventConstants(t)
	if len(declared) == 0 {
		t.Fatal("no Hook* string constants found in package session — the scan has stopped finding what it checks")
	}

	listed := make(map[string]bool, len(AllHookEvents))
	for _, e := range AllHookEvents {
		listed[e] = true
	}
	for name, value := range declared {
		if !listed[value] {
			t.Errorf("hook event %q (%s) is declared but missing from AllHookEvents", value, name)
		}
	}

	values := make(map[string]bool, len(declared))
	for _, value := range declared {
		values[value] = true
	}
	for _, e := range AllHookEvents {
		if !values[e] {
			t.Errorf("AllHookEvents lists %q, which no Hook* constant declares", e)
		}
	}
}

// hookEventConstants returns every package-level `Hook*` identifier bound to a
// string literal, as name → value. go test runs with the package directory as
// its working directory, so "." is the package under test; a parse failure is
// fatal rather than an empty result.
func hookEventConstants(t *testing.T) map[string]string {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}

	fset := token.NewFileSet()
	found := map[string]string{}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			spec, ok := n.(*ast.ValueSpec)
			if !ok {
				return true
			}
			for i, ident := range spec.Names {
				if !strings.HasPrefix(ident.Name, "Hook") || i >= len(spec.Values) {
					continue
				}
				lit, ok := spec.Values[i].(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					continue
				}
				value, err := strconv.Unquote(lit.Value)
				if err != nil {
					t.Fatalf("unquote %s = %s: %v", ident.Name, lit.Value, err)
				}
				found[ident.Name] = value
			}
			return true
		})
	}
	return found
}
