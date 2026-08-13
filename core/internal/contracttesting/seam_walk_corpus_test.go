// seam_walk_corpus_test.go is the committed corpus for seam_walk_test.go's two
// rules: source spellings and call graphs pinned to the verdict each detector
// must return, INCLUDING the verdicts it must NOT return.
//
// It exists because of #1450. There, a rewritten guard silently dropped a case
// its predecessor caught while eighteen hand-planted probes all passed — every
// one of them an addition to coverage, none a lock on what already worked. A
// guard is worth only what its deliberate failures prove, and a guard with no
// corpus proves whatever its author happened to try that afternoon.
//
// The want:false rows are the more valuable half. Rule 1 is the kind of rule
// that is tempting to write as `grep 't \*testing\.T'`, and four rows below are
// exactly what that grep gets wrong: a *testing.T in second position, a
// *testing.T inside a struct FIELD type, a *testing.T inside a PARAMETER's func
// type, and a function literal assigned to a var. The first three are real
// false positives a name- or text-based rule produces. The last, together with
// the aliased-import row, is a deliberate LIMIT rather than a claim of safety —
// an ast.FuncDecl walk over syntactic types cannot see either — and pinning
// them here is how the next reader learns that from a test rather than from an
// incident.
//
// A want:false row is also how this corpus's own worst defect shipped and was
// caught: `testing.TB` sat in the must-NOT-flag block, so the rule's biggest
// hole came with an approved spelling. It is now in the must-flag block with
// the reason attached (review of PR #1512).
package contracttesting

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// TestSeamWalkCorpus pins firstParamType — the whole of rule 1's decision — over
// every spelling a parameter list can take.
func TestSeamWalkCorpus(t *testing.T) {
	cases := []struct {
		name string
		src  string
		// fn is the function the case is about, or "" when the source
		// deliberately declares none.
		fn   string
		want string
		// role is the verdict rule 1 and rule 2 actually consume. Pinning it
		// beside the type render is what gives the CASE LIST coverage: an
		// earlier draft pinned the render alone, so testing.TB missing from the
		// list was invisible to every row here (review of PR #1512).
		role string
	}{
		// --- the shapes rule 1 must FLAG (want "*testing.T") ---
		{"plain", "func arm(t *testing.T) {}", "arm", "*testing.T", roleTestingT},
		{"with trailing params", "func arm(t *testing.T, s string) {}", "arm", "*testing.T", roleTestingT},
		{"method with a receiver", "func (r *rec) arm(t *testing.T) {}", "arm", "*testing.T", roleTestingT},
		{"variadic tail", "func arm(t *testing.T, xs ...string) {}", "arm", "*testing.T", roleTestingT},
		{"two names sharing one type", "func arm(t, u *testing.T) {}", "arm", "*testing.T", roleTestingT},
		{"exported but not an Assert entry point", "func Helper(t *testing.T) {}", "Helper", "*testing.T", roleTestingT},
		{"an Assert entry point is still detected, and exempted by NAME not by type",
			"func AssertThing(t *testing.T) {}", "AssertThing", "*testing.T", roleTestingT},

		// testing.TB is FLAGGED, and the reason is worth reading: TB carries
		// Errorf/Fatalf/Helper and an unexported method, so no recorder can
		// implement it in any spelling. A TB-first arm is exactly as
		// unswappable as a *testing.T-first one. An earlier draft of this
		// corpus filed it under "must NOT flag", which made the hole an
		// APPROVED spelling — found in review of PR #1512. *testing.B and
		// *testing.M are flagged on the same argument.
		{"the TB interface — unswappable, so rule 1 owns it too",
			"func arm(tb testing.TB) {}", "arm", "testing.TB", roleTestingT},
		{"a benchmark", "func arm(b *testing.B) {}", "arm", "*testing.B", roleTestingT},
		{"a TestMain", "func arm(m *testing.M) {}", "arm", "*testing.M", roleTestingT},

		// --- the shapes rule 1 must NOT flag ---
		{"*testing.T in SECOND position", "func arm(s string, t *testing.T) {}", "arm", "string", roleNeither},
		{"pointer to pointer", "func arm(t **testing.T) {}", "arm", "**testing.T", roleNeither},
		{"no parameters at all", "func arm() {}", "arm", "", roleNeither},
		{"a reporter — rule 2's subject, not rule 1's", "func arm(t reporter) {}", "arm", "reporter", roleArm},
		{"an armT — likewise", "func arm(t armT) {}", "arm", "armT", roleArm},
		{"*testing.T inside a PARAMETER's func type",
			"func arm(build func(t *testing.T) string) {}", "arm", "func(t *testing.T) string", roleNeither},
		// A declared LIMIT, not an approval: the walk renders the type as
		// written, so an aliased testing import hides the seam type from rule
		// 1. Nothing in this repo aliases it and no rule forbids doing so, and
		// pinning the limit here is how the next reader learns it from a test
		// rather than from an incident — the same treatment the func-literal
		// row gets (review of PR #1512).
		{"an ALIASED testing import — a declared limit of a syntactic type render",
			"func arm(t *tst.T) {}", "arm", "*tst.T", roleNeither},

		// --- shapes that declare no function at all ---
		{"*testing.T inside a struct FIELD type",
			"type S struct{ Root func(t *testing.T) string }", "", "", roleNeither},
		{"a function LITERAL assigned to a package var — a known LIMIT of an ast.FuncDecl walk, " +
			"pinned so it is discovered here and not in an incident",
			"var arm = func(t *testing.T) {}", "", "", roleNeither},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fset := token.NewFileSet()
			file, err := parser.ParseFile(fset, "corpus.go", "package p\nimport \"testing\"\nimport tst \"testing\"\nvar _ = testing.Short\nvar _ = tst.Short\ntype rec struct{}\n"+c.src+"\n", 0)
			if err != nil {
				t.Fatalf("the corpus row does not parse, so it pins nothing: %v", err)
			}
			var got, gotRole string
			var found bool
			for _, decl := range file.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Name.Name != c.fn {
					continue
				}
				found, got, gotRole = true, firstParamType(fn), seamRole(fn)
			}
			if c.fn == "" {
				// Every row asserts the construct it plants is actually there,
				// because a corpus that quietly stops containing its own cases
				// reads as a pass (core/architecture_hookbody_shapes_test.go).
				if found {
					t.Fatalf("this row is about a source that declares no function, but one was found")
				}
				if !declaresSomething(file) {
					t.Fatal("the row planted nothing at all")
				}
				return
			}
			if !found {
				t.Fatalf("the corpus row no longer declares %q — it pins nothing", c.fn)
			}
			if got != c.want {
				t.Errorf("firstParamType = %q, want %q", got, c.want)
			}
			if gotRole != c.role {
				t.Errorf("seamRole = %q, want %q — this is the verdict rule 1 and rule 2 consume, "+
					"and pinning only the type render above is what hid testing.TB's absence from the case list",
					gotRole, c.role)
			}
		})
	}
}

// declaresSomething confirms a no-function row still carries a declaration, so
// an empty or mistyped source cannot pass as "declares no function".
func declaresSomething(file *ast.File) bool {
	// The preamble alone contributes two imports, two vars and a type, so a row
	// that planted nothing still has five. Requiring more is what makes this a
	// check rather than a formality.
	return len(file.Decls) > 5
}

// TestSeamWalkCoverageCorpus pins rule 2's propagation, which is the half a
// spelling corpus cannot reach.
//
// The last two rows are the ones the rule exists for: an arm reachable ONLY
// through an exported entry point is NOT covered, because a family's vacuity
// guard calls the entry point and would otherwise mark every arm of a
// self-test-less family as driven.
func TestSeamWalkCoverageCorpus(t *testing.T) {
	cases := []struct {
		name  string
		facts seamFacts
		want  map[string]bool
	}{
		{
			name: "an arm a test references directly is covered",
			facts: seamFacts{
				arms:              map[string]string{"armA": "x.go:1"},
				refs:              map[string]map[string]bool{},
				entryPoints:       map[string]bool{},
				referencedInTests: map[string]bool{"armA": true},
			},
			want: map[string]bool{"armA": true},
		},
		{
			name: "an arm reached through a covered helper is covered",
			facts: seamFacts{
				arms:              map[string]string{"armA": "x.go:1", "helper": "x.go:2"},
				refs:              map[string]map[string]bool{"helper": {"armA": true}},
				entryPoints:       map[string]bool{},
				referencedInTests: map[string]bool{"helper": true},
			},
			want: map[string]bool{"armA": true, "helper": true},
		},
		{
			name: "coverage propagates transitively, two hops",
			facts: seamFacts{
				arms: map[string]string{"armA": "x.go:1", "mid": "x.go:2", "top": "x.go:3"},
				refs: map[string]map[string]bool{
					"top": {"mid": true},
					"mid": {"armA": true},
				},
				entryPoints:       map[string]bool{},
				referencedInTests: map[string]bool{"top": true},
			},
			want: map[string]bool{"armA": true, "mid": true, "top": true},
		},
		{
			// The mustReport CLAUSE — "named by a positive-test file is not
			// driven" — is not reachable from this table: coveredArms is handed
			// facts, and the clause lives in how parseSeamFacts BUILDS them. A
			// row asserting it here would be byte-identical to this one and
			// could never fail independently, which is this package's own
			// thesis turned on its corpus. It is pinned by
			// TestParseSeamFactsSeedsOnlyFromDrivingTestFiles instead.
			name: "an arm nothing references is NOT covered",
			facts: seamFacts{
				arms:              map[string]string{"armA": "x.go:1"},
				refs:              map[string]map[string]bool{},
				entryPoints:       map[string]bool{},
				referencedInTests: map[string]bool{},
			},
			want: map[string]bool{},
		},
		{
			name: "an arm reached only through an UNCOVERED helper is NOT covered",
			facts: seamFacts{
				arms:              map[string]string{"armA": "x.go:1"},
				refs:              map[string]map[string]bool{"helper": {"armA": true}},
				entryPoints:       map[string]bool{},
				referencedInTests: map[string]bool{},
			},
			want: map[string]bool{},
		},
		{
			name: "an arm reached ONLY through an exported entry point is NOT covered — " +
				"a family with a vacuity guard and no negative self-test",
			facts: seamFacts{
				arms:              map[string]string{"armA": "x.go:1"},
				refs:              map[string]map[string]bool{"AssertFamily": {"armA": true}},
				entryPoints:       map[string]bool{"AssertFamily": true},
				referencedInTests: map[string]bool{"AssertFamily": true},
			},
			want: map[string]bool{},
		},
		{
			name: "the same arm becomes covered as soon as a test drives it directly — " +
				"the vacuity guard for the entry-point row above",
			facts: seamFacts{
				arms:              map[string]string{"armA": "x.go:1"},
				refs:              map[string]map[string]bool{"AssertFamily": {"armA": true}},
				entryPoints:       map[string]bool{"AssertFamily": true},
				referencedInTests: map[string]bool{"AssertFamily": true, "armA": true},
			},
			want: map[string]bool{"armA": true},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := coveredArms(c.facts)
			for name := range c.facts.arms {
				if got[name] != c.want[name] {
					t.Errorf("arm %q covered = %v, want %v", name, got[name], c.want[name])
				}
			}
		})
	}
}
