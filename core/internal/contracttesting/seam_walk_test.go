// seam_walk_test.go makes #1479's reporter seam STRUCTURAL.
//
// #1479 applied the seam per-arm, by hand. Nothing failed if a new arm took
// *testing.T, and nothing failed if a whole family had no self-test at all —
// that this was deliberate rather than an oversight was recorded only in prose,
// which is the exact failure mode this package exists to remove (#1497).
//
// Two rules, over one parse of the package's own sources.
//
// # Rule 1 — who may hold a *testing.T
//
// A function in a non-test file whose FIRST parameter is *testing.T can decide
// a verdict through a channel no negative self-test can swap. That is allowed
// for exactly three kinds of function, and everything else is a violation:
//
//   - an exported Assert… entry point, which is where a real *testing.T
//     legitimately enters the package;
//   - realT, which is the adapter between the two halves;
//   - anything named in deferredToTheSeam, with its reason and its issue — the
//     applyWritesNoUserFile shape from core/cmd/irrlichd/managedfiles_test.go.
//
// It keys on the parameter TYPE AND POSITION, never on a name convention. A
// naming rule is satisfied by renaming, which is not a fix.
//
// testing.TB counts as the same type for this rule, and that is not pedantry:
// TB carries Errorf/Fatalf/Helper AND an unexported method, so it cannot be
// implemented outside the testing package — no recorder can satisfy it in any
// spelling, which makes a TB-first arm exactly as unswappable as a
// *testing.T-first one. An earlier draft treated TB as "not the seam type" and
// its corpus filed that spelling under "must NOT flag", so the hole came with
// an approved spelling (review of PR #1512). *testing.B and *testing.M are
// included on the same argument and at no cost, rather than left as a judgement
// call for the next reader.
//
// # Rule 2 — every arm is actually driven
//
// An ARM is a function whose first parameter is reporter or armT: the seam
// exists for it, so something must go through it. Rule 2 requires every arm to
// be reachable from a reference in some _test.go of this package, along a chain
// that does NOT pass through an exported Assert… entry point.
//
// Both halves of that sentence are load-bearing:
//
//   - Reference-based, not filename-based. The permission-gate family's
//     self-tests live in permission_gate_test.go, so a
//     <family>_selftest_test.go rule would need a special case on day one.
//   - Not through an entry point, because a family's VACUITY GUARD calls the
//     entry point (TestAssertHookReceiverPermissionGated_PassesACorrectReceiver
//     does exactly that) and would otherwise mark every arm of a self-test-less
//     family as covered. A positive test proves an arm passes a correct
//     fixture; it proves nothing about whether the arm can fail, which is the
//     only thing this rule is about.
//
// # What it does not cover
//
// The edge set is identifier REFERENCES inside a function body, not resolved
// call targets — arms are assigned to struct fields as values here
// (deliveryRules.first), and a call-expression-only walk would report those as
// uncalled. That over-approximates: it can only make rule 2 miss a violation,
// never fabricate one. And neither rule sees a function literal assigned to a
// package var, since that is not a FuncDecl; TestSeamWalkCorpus pins that as a
// known limit rather than leaving it to be discovered.
package contracttesting

import (
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// deferredToTheSeam names every function in a non-test file of this package
// that still takes a *testing.T first and is NOT an entry point, with the
// reason it is allowed to.
//
// The admission rule is "this function must be able to FAIL THE RUN, and no
// obligation owns that failure". Two different kinds of function satisfy it,
// and they are listed as two kinds rather than generalized because the
// generalization is false — and a false one here reads as "you don't need to
// look" (review of PR #1512, against an earlier draft that claimed every entry
// was fixture machinery):
//
//   - FIXTURE MACHINERY — mkSubdir, receiptTranscript, unknownEventTranscript,
//     unknownEventName. This is the line reporter.go's fixtureT draws: a
//     fixture that cannot be BUILT is a real failure and has to be loud, and
//     recording it as "the obligation fired" is #1479's second instance in
//     miniature.
//   - A HELPER WITH NO FAMILY — CompareGolden. It does decide a verdict, which
//     is exactly what breaks the generalization, but it grades golden files for
//     adapter tests and has no obligations to drive, so there is no self-test
//     for the seam to serve.
//
// The map is deliberately not empty. #1497 chose to write it here rather than
// in #1479 so it would list what is genuinely still deferred instead of being
// written and immediately emptied.
var deferredToTheSeam = map[string]string{
	"CompareGolden": "not a contract family at all — a golden-file comparison used by adapter tests. " +
		"It has no obligations to drive and no family to self-test (#1497).",
	"mkSubdir": "fixture construction: a directory that cannot be created is a failure of the test " +
		"environment, not of the receiver under test, and must be loud (#1479).",
	"receiptTranscript": "fixture construction: relocates the adapter's root and writes a transcript " +
		"into it, reporting its own failures (#1479).",
	"unknownEventTranscript": "fixture construction, same shape as receiptTranscript (#1479).",
	"unknownEventName": "derives a per-sub-test, per-invocation event name from t.Name(), which no " +
		"reporter carries. It decides no verdict; a name it cannot derive is a fixture failure (#1364, #1479).",
	"unknownEventHash": "the t.Name() half the two name functions share, so they cannot drift onto " +
		"different derivations. It decides no verdict (#1512 review).",
}

// --- the walk ---

// seamFacts is everything both rules need, from one parse.
type seamFacts struct {
	// testingTFirst maps a non-test function's name to the file:line it is
	// declared at, for every function whose first parameter is *testing.T.
	testingTFirst map[string]string

	// arms is the same, for every function whose first parameter is reporter or
	// armT.
	arms map[string]string

	// refs maps a non-test function's name to the identifiers its body
	// mentions.
	refs map[string]map[string]bool

	// entryPoints is the set of exported Assert… functions.
	entryPoints map[string]bool

	// declaredInTests is every function DECLARED by a _test.go, driving or not.
	// It exists so a probe can confirm it still names something real before
	// grading it (TestParseSeamFactsSeedsOnlyFromDrivingTestFiles) out of the
	// parse this walk already performs, rather than out of a second one that
	// could disagree with it about which files it read.
	declaredInTests map[string]bool

	// referencedInTests is every identifier mentioned in a _test.go that also
	// mentions mustReport.
	//
	// The mustReport condition is what makes this a claim about being DRIVEN
	// rather than about being NAMED. Seeding from every test file meant an arm
	// listed in a table literal counted as covered: deleting a family's four
	// negative self-tests outright, leaving only the now-uncalled table that
	// named them, passed rule 2 in full — measured in review of PR #1512, and
	// that is precisely the regression this rule exists to catch. mustReport is
	// the one call every negative self-test in this package makes and no
	// positive test does, so requiring it in the same FILE separates the two
	// without keying on a filename — which matters, because
	// permission_gate_test.go holds both.
	referencedInTests map[string]bool
}

func parseSeamFacts(t *testing.T, dir string) seamFacts {
	t.Helper()
	f := seamFacts{
		testingTFirst:     map[string]string{},
		arms:              map[string]string{},
		refs:              map[string]map[string]bool{},
		entryPoints:       map[string]bool{},
		declaredInTests:   map[string]bool{},
		referencedInTests: map[string]bool{},
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	fset := token.NewFileSet()
	var sawTest, sawNonTest bool
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") {
			continue
		}
		file, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		if strings.HasSuffix(name, "_test.go") {
			sawTest = true
			f.absorbTestFile(file)
			continue
		}
		sawNonTest = true
		f.absorbNonTestFile(fset, name, file)
	}
	// A walk that read nothing passes both rules perfectly. Refusing here is
	// the same "a verification mechanism must fail loudly when it cannot run"
	// rule the package header states (#1423, #1446).
	if !sawNonTest || !sawTest {
		t.Fatalf("the walk read %v non-test and %v test files — a parse that finds nothing satisfies every rule below", sawNonTest, sawTest)
	}
	return f
}

// absorbTestFile records what one _test.go declares and, if it drives
// obligations, what it references.
//
// The declaration half is unconditional and the reference half is gated on
// mustReport: a probe needs to know a symbol still EXISTS wherever it lives,
// while coverage may only be seeded by a file that actually drives an arm.
func (f seamFacts) absorbTestFile(file *ast.File) {
	idents := identsIn(file)
	for _, decl := range file.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok {
			f.declaredInTests[fn.Name.Name] = true
		}
	}
	if !idents["mustReport"] {
		return
	}
	for id := range idents {
		f.referencedInTests[id] = true
	}
}

// absorbNonTestFile records every function one non-test file declares: the role
// each rule keys on, whether it is an entry point, and the identifiers its body
// mentions (rule 2's edge set).
func (f seamFacts) absorbNonTestFile(fset *token.FileSet, name string, file *ast.File) {
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok {
			continue
		}
		where := name + ":" + strconv.Itoa(fset.Position(fn.Pos()).Line)
		switch seamRole(fn) {
		case roleTestingT:
			f.testingTFirst[fn.Name.Name] = where
		case roleArm:
			f.arms[fn.Name.Name] = where
		}
		if fn.Name.IsExported() && strings.HasPrefix(fn.Name.Name, "Assert") {
			f.entryPoints[fn.Name.Name] = true
		}
		f.refs[fn.Name.Name] = identsIn(fn)
	}
}

// identsIn is every identifier appearing anywhere under n.
//
// Rule 2's edges are identifier REFERENCES rather than resolved call targets,
// because arms are assigned to struct fields as values here
// (deliveryRules.first) and a call-expression walk would report those as
// uncalled. That over-approximates in the safe direction: it can make rule 2
// miss a violation, never fabricate one.
func identsIn(n ast.Node) map[string]bool {
	out := map[string]bool{}
	ast.Inspect(n, func(n ast.Node) bool {
		if id, ok := n.(*ast.Ident); ok {
			out[id.Name] = true
		}
		return true
	})
	return out
}

// seamRole classifies one function by the only thing either rule looks at: the
// type of its FIRST parameter.
//
// It is a named function rather than a switch inside parseSeamFacts so the
// corpus can pin the DECISION and not merely the type render. That distinction
// is not cosmetic: the corpus originally pinned firstParamType alone, so the
// case list — which is where testing.TB was missing — had no committed coverage
// at all, and the hole was found by review rather than by a test (PR #1512).
func seamRole(fn *ast.FuncDecl) string {
	switch firstParamType(fn) {
	case "*testing.T", "testing.TB", "*testing.B", "*testing.M":
		return roleTestingT
	case "reporter", "armT":
		return roleArm
	}
	return roleNeither
}

const (
	// roleTestingT: reports through a channel no self-test can swap, so rule 1
	// owns it.
	roleTestingT = "testing-T"
	// roleArm: reports through the seam, so rule 2 owns it.
	roleArm = "arm"
	// roleNeither: outside both rules.
	roleNeither = ""
)

// firstParamType renders the first parameter's type, or "" when there is none.
//
// It renders through go/types.ExprString, which is what this repo's other AST
// guard already uses for exactly this job (core/architecture_hookbody_test.go).
// The first draft hand-rolled a renderer that collapsed every unlisted
// expression into one placeholder, so a corpus row could pin only the
// placeholder rather than the real spelling. ExprString is purely syntactic and
// needs no type information, so it is a drop-in.
func firstParamType(fn *ast.FuncDecl) string {
	if fn.Type.Params == nil || len(fn.Type.Params.List) == 0 {
		return ""
	}
	return types.ExprString(fn.Type.Params.List[0].Type)
}

// --- rule 1 ---

// TestEveryTestingTHolderIsAnEntryPointOrDeferred is rule 1.
func TestEveryTestingTHolderIsAnEntryPointOrDeferred(t *testing.T) {
	f := parseSeamFacts(t, ".")
	if len(f.testingTFirst) == 0 {
		t.Fatal("no function in this package takes a *testing.T first — the entry points do, so the walk is not reading what it thinks it is")
	}

	for _, name := range sortedKeys(f.testingTFirst) {
		if f.entryPoints[name] || name == "realT" {
			continue
		}
		if reason, ok := deferredToTheSeam[name]; ok {
			if reason == "" {
				t.Errorf("%s (%s) is deferred with no reason — an exemption whose justification is blank is an exemption nobody can review",
					name, f.testingTFirst[name])
			}
			continue
		}
		t.Errorf("%s (%s) takes a *testing.T as its first parameter, so it reports through a channel no negative self-test can swap. "+
			"Take a reporter (or armT, if it also builds fixture state), or name it in deferredToTheSeam with the reason it cannot",
			name, f.testingTFirst[name])
	}

	// An exemption that stopped naming a real function is a silent no-op, and
	// this map's whole job is to be reviewable — the same reasoning
	// construction_test.go's existence-checked exemption maps carry.
	for _, name := range sortedKeys(deferredToTheSeam) {
		if _, ok := f.testingTFirst[name]; !ok {
			t.Errorf("deferredToTheSeam names %q, which no longer takes a *testing.T first (or no longer exists) — "+
				"remove the entry rather than leaving an exemption that exempts nothing", name)
		}
	}
}

// --- rule 2 ---

// TestParseSeamFactsSeedsOnlyFromDrivingTestFiles pins rule 2's mustReport
// clause, which is the one part of the seeding no corpus row over coveredArms
// can reach — those rows are handed synthetic facts, and this is about how the
// facts are BUILT.
//
// The clause exists because an arm counted as driven merely by being NAMED:
// deleting a family's negative self-tests and leaving the now-uncalled table
// that listed them passed rule 2 in full (review of PR #1512).
func TestParseSeamFactsSeedsOnlyFromDrivingTestFiles(t *testing.T) {
	f := parseSeamFacts(t, ".")

	// The vacuity half: a symbol declared in a file that DOES drive obligations
	// must be seeded, or the rule below could never cover anything.
	if !f.referencedInTests["mustReport"] {
		t.Fatal("mustReport itself is not in the seeded set — parseSeamFacts read no driving test file, " +
			"so rule 2 would report every arm as undriven for a reason unrelated to any family")
	}

	// The discriminating half. This symbol is declared in
	// hook_endpoint_addressfree_test.go, which drives the hook-endpoint family
	// through its exported entry point and calls mustReport nowhere — exactly
	// the positive-test shape the clause must not treat as coverage.
	const namedOnlyByAPositiveTest = "referenceBeaconHome"

	// Confirm the probe still names something real BEFORE grading it. A symbol
	// that has been renamed away is absent from the seeded set for a reason
	// that has nothing to do with the clause, and the assertion below would
	// then pass while checking nothing — which is how the first draft of this
	// test shipped, naming a symbol that never existed.
	if !f.declaredInTests[namedOnlyByAPositiveTest] {
		t.Fatalf("the probe symbol %q is no longer declared in any _test.go — pick another symbol that "+
			"a positive-test file declares, or this assertion checks nothing", namedOnlyByAPositiveTest)
	}
	if f.referencedInTests[namedOnlyByAPositiveTest] {
		t.Errorf("%q was seeded from a test file that never calls mustReport — being NAMED by a positive "+
			"test is not being DRIVEN by a negative one, and treating it as such is what let a family's "+
			"deleted self-tests pass rule 2", namedOnlyByAPositiveTest)
	}
}

// TestEveryArmIsDrivenByATest is rule 2.
func TestEveryArmIsDrivenByATest(t *testing.T) {
	f := parseSeamFacts(t, ".")
	if len(f.arms) == 0 {
		t.Fatal("this package declares no arm taking a reporter — either the seam is gone or the walk is not reading the package")
	}

	covered := coveredArms(f)

	for _, name := range sortedKeys(f.arms) {
		if covered[name] {
			continue
		}
		t.Errorf("arm %s (%s) is reached by no _test.go in this package except through an exported Assert… entry point. "+
			"Its family has a vacuity guard but no negative self-test, so nothing anywhere demonstrates the obligation can fail (#1453, #1479)",
			name, f.arms[name])
	}
}

// coveredArms computes rule 2's fixed point: an arm is covered when a _test.go
// references it, or when something covered (and not an entry point) mentions
// it.
//
// It is a separate function so TestSeamWalkCoverageCorpus can pin its verdicts
// on synthetic graphs. A propagation rule that quietly started covering
// everything would otherwise look exactly like a package with no violations.
func coveredArms(f seamFacts) map[string]bool {
	covered := map[string]bool{}
	for name := range f.arms {
		if f.referencedInTests[name] {
			covered[name] = true
		}
	}
	// Propagate backwards along "is mentioned by a covered function", skipping
	// exported entry points: a family's vacuity guard calls the entry point,
	// and a positive test says nothing about whether an arm can fail.
	for changed := true; changed; {
		changed = false
		for caller, body := range f.refs {
			if f.entryPoints[caller] || !isCovered(caller, covered, f) {
				continue
			}
			for callee := range body {
				if _, isArm := f.arms[callee]; isArm && !covered[callee] {
					covered[callee] = true
					changed = true
				}
			}
		}
	}
	return covered
}

// isCovered reports whether caller may propagate coverage. A non-arm helper
// that a test references directly counts, which is what lets coverage flow
// through a chain like assertFloorCoversEveryEvent -> assertFloorCoversEvent.
func isCovered(caller string, covered map[string]bool, f seamFacts) bool {
	return covered[caller] || f.referencedInTests[caller]
}

func sortedKeys[V any](m map[string]V) []string {
	return slices.Sorted(maps.Keys(m))
}
