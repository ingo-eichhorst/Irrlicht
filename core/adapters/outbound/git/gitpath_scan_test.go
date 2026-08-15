package git

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"sort"
	"strings"
	"testing"
)

// This file is the half of #1554 the seam cannot do.
//
// Adapter.path makes the safe spelling available; it does not stop the unsafe
// one from coming back. The unsafe one is an assignment to the package var
// gitPath, and its cost is entirely invisible at the site: the test that
// writes it looks self-contained (it saves, assigns, and restores in
// t.Cleanup), while what it actually does is hand every CONCURRENTLY running
// test in this package a different git for the duration. What those tests then
// report is a timeout or an empty result — a timing flake, not a fixture
// collision — and the assignment is nowhere in the failure.
//
// It is a SOURCE scan rather than a behavioural one, for the reason
// construction_test.go's scan is (#1400/#1450): the hazard is created by a
// construct that no test needs to execute wrongly. Whether it bites depends on
// which other test the scheduler happens to run at the same time, which is the
// one thing about it that is not deterministic — so a runtime check would be
// green on almost every run of a package that is already broken.
//
// It is deliberately narrow. It says nothing about t.Parallel(), because
// "assign to a shared var only from a serial test" is a rule about the whole
// package's future scheduling that no local read can check, and #1554's own
// history is the argument: the mutation WAS legal under that rule for as long
// as nobody wrote a second test, and the rule is what nobody was enforcing.
// Forbidding the assignment outright is checkable, and the seam makes it free.

// gitPathVar is the package var this file protects. Named once so the scan and
// its corpus cannot drift onto different symbols.
const gitPathVar = "gitPath"

// assignmentsToGitPath returns one description per place in file that WRITES
// the package var gitPath — a plain assignment, or taking its address, which
// is the same write one indirection away.
//
// It has no type information, and two consequences follow that are stated
// rather than left to be discovered:
//
//   - A local or parameter named gitPath is flagged even though it shadows the
//     var rather than writing it. That is a false positive by the letter of
//     what this checks, and it is kept: a shadow of this name is exactly what
//     makes reviewing for the real thing impossible, and the corpus pins the
//     behaviour so it is learned from a test rather than from a surprise.
//   - A field or selector spelled `x.gitPath = y` is NOT flagged, because the
//     LHS is a SelectorExpr, not the bare identifier. Adapter.path is
//     deliberately named `path` and not `gitPath` for the neighbouring reason,
//     but a future field of that name would be invisible here. Pinned as a
//     must-not-flag row.
//
// The declaration itself (`var gitPath = pathutil.MustResolve("git")`) is a
// ValueSpec, not an assignment, so it does not trip this.
func assignmentsToGitPath(fset *token.FileSet, file *ast.File) []string {
	var found []string
	report := func(pos token.Pos, how string) {
		found = append(found, fmt.Sprintf("%s (%s)", fset.Position(pos), how))
	}

	ast.Inspect(file, func(n ast.Node) bool {
		switch v := n.(type) {
		case *ast.AssignStmt:
			for _, lhs := range v.Lhs {
				if id, ok := lhs.(*ast.Ident); ok && id.Name == gitPathVar {
					report(id.Pos(), "assignment")
				}
			}
		case *ast.UnaryExpr:
			if v.Op != token.AND {
				return true
			}
			if id, ok := v.X.(*ast.Ident); ok && id.Name == gitPathVar {
				report(id.Pos(), "address taken")
			}
		}
		return true
	})
	return found
}

// declaresGitPath reports whether file contains gitPath's own var declaration.
// It is the vacuity guard's input: a scan that has not seen the declaration is
// not reading the package it thinks it is, and its silence would then mean
// nothing at all.
func declaresGitPath(file *ast.File) bool {
	declared := false
	ast.Inspect(file, func(n ast.Node) bool {
		spec, ok := n.(*ast.ValueSpec)
		if !ok {
			return true
		}
		for _, name := range spec.Names {
			if name.Name == gitPathVar {
				declared = true
			}
		}
		return true
	})
	return declared
}

// TestNothingAssignsToTheResolvedGitPath walks this package's own source —
// production files and tests alike, since either could write the var — and
// fails on any write to gitPath.
//
// MUTATION EVIDENCE (#1554), all three seen red:
//   - Re-adding the assignment this issue is about
//     (`saved := gitPath; gitPath = stub; t.Cleanup(...)`) inside
//     TestProductionAdapterIsBounded fails it, naming both writes by file and
//     line. The same shape is pinned without a live edit by the `#1554's own
//     shape` row of the corpus below.
//   - Renaming gitPathVar so no declaration matches fails the second vacuity
//     guard (`scanned 4 file(s) and found no declaration of …`) rather than
//     reporting a clean package.
//   - Pointing the walk at ".." fails the first (`parsed no packages`).
func TestNothingAssignsToTheResolvedGitPath(t *testing.T) {
	t.Parallel()

	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", nil, 0)
	if err != nil {
		t.Fatalf("parsing this package's source: %v", err)
	}
	if len(pkgs) == 0 {
		t.Fatal("parsed no packages — the scan would pass vacuously")
	}

	files := 0
	sawDeclaration := false
	var offenders []string
	for _, pkg := range pkgs {
		for _, file := range pkg.Files {
			files++
			sawDeclaration = sawDeclaration || declaresGitPath(file)
			offenders = append(offenders, assignmentsToGitPath(fset, file)...)
		}
	}

	// Two vacuity guards, because "found nothing" and "looked at nothing" must
	// not produce the same output. The file count catches a walk pointed at
	// the wrong directory; the declaration catches a rename, after which the
	// scan would be looking for a symbol that no longer exists and reporting a
	// clean package forever.
	if files == 0 {
		t.Fatal("parsed no files — the scan would pass vacuously")
	}
	if !sawDeclaration {
		t.Fatalf("scanned %d file(s) and found no declaration of %s; the scan is not looking "+
			"where it thinks it is, so its silence proves nothing", files, gitPathVar)
	}

	if len(offenders) > 0 {
		sort.Strings(offenders)
		t.Errorf("%s is assigned at %s.\n"+
			"It is a package var shared by every test in this package, and the tests here "+
			"run in parallel: a test that repoints it hands every concurrently running test "+
			"a different git for the duration, which they report as a timing flake rather "+
			"than as this. Inject through the adapter instead — withBinary(path), or an "+
			"&Adapter{path: ...} literal (#1554).",
			gitPathVar, strings.Join(offenders, ", "))
	}
}

// TestGitPathAssignmentScanFlagsEverySpelling is the scan's committed mutation
// evidence: one row per spelling, pinned to the verdict the detector must
// return. The must-NOT-flag rows carry as much of the value as the others —
// a detector that flagged every mention of gitPath would satisfy every
// must-flag row and be useless, and two of them (the declaration, and the read
// on an assignment's right-hand side) are what this package's own source
// contains.
func TestGitPathAssignmentScanFlagsEverySpelling(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		src  string
		want bool
	}{
		{
			name: "#1554's own shape: save, repoint, restore in Cleanup",
			src: `func f(t *testing.T) {
				saved := gitPath
				gitPath = "/tmp/stub"
				t.Cleanup(func() { gitPath = saved })
			}`,
			want: true,
		},
		{
			name: "assignment inside a deferred closure",
			src:  `func f() { defer func() { gitPath = "x" }() }`,
			want: true,
		},
		{
			name: "second position of a multi-assignment",
			src:  `func f(a, b string) { a, gitPath = b, a }`,
			want: true,
		},
		{
			name: "address taken — the same write, one indirection away",
			src:  `func f() { p := &gitPath; *p = "x" }`,
			want: true,
		},
		{
			name: "declared limit: a parameter shadowing the var is flagged too",
			src:  `func f(gitPath string) { gitPath = "x" }`,
			want: true,
		},
		{
			name: "the declaration itself is not an assignment",
			src:  `var gitPath = resolve("git")`,
			want: false,
		},
		{
			name: "a read in a comparison",
			src:  `func f() bool { return gitPath == "git" }`,
			want: false,
		},
		{
			name: "a read on an assignment's right-hand side",
			src:  `func f(a *Adapter) { a.path = gitPath }`,
			want: false,
		},
		{
			name: "a field of that name is a selector, not the var",
			src:  `func f(x struct{ gitPath string }) { x.gitPath = "y" }`,
			want: false,
		},
		{
			name: "the blank identifier on the left, gitPath on the right",
			src:  `func f() { _ = gitPath }`,
			want: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			fset := token.NewFileSet()
			file, err := parser.ParseFile(fset, "corpus.go", "package git\n\n"+tc.src, 0)
			if err != nil {
				t.Fatalf("parsing the case's own source: %v\n%s", err, tc.src)
			}
			// The case must still contain what it plants: a corpus that
			// quietly stopped mentioning gitPath would read as a clean pass on
			// every must-not-flag row.
			if !strings.Contains(tc.src, gitPathVar) {
				t.Fatalf("case source no longer mentions %s, so it pins nothing", gitPathVar)
			}

			got := assignmentsToGitPath(fset, file)
			if (len(got) > 0) != tc.want {
				t.Errorf("scan reported %v, want flagged=%v for:\n%s", got, tc.want, tc.src)
			}
		})
	}
}
