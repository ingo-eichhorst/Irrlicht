// hookcontracts_shapes_test.go is the corpus behind the tripwire in
// hookcontracts_test.go: one synthetic adapter test package per way of wiring
// (or of appearing to wire) a contract family, each pinned to the verdict the
// detector must return for it.
//
// It exists because TestEveryHookInstallWiresItsContractFamilies passes by
// construction — every hooks-declaring adapter wired every required family the
// day it landed, which is what that test being green says — and
// docs/testing-philosophy.md is explicit that this is the
// condition the mutation rule exists for, and that the mutation belongs
// COMMITTED beside the assertion rather than described in a PR body nothing
// re-runs. The real-tree mutations (delete an adapter's hookpath_test.go, or
// rename its Test function so the wiring becomes a dead helper) are recorded in
// PR #1740's body; these are the versions that keep running afterwards.
//
// Three groups, and the second and third carry the value:
//
//   - CAPABILITIES (wantCalled) — spellings the detector must credit. Several
//     are things a grep cannot do: an aliased import, a two-hop helper chain, a
//     call that crosses from the external test package into an in-package test
//     helper.
//   - MUST NOT CREDIT (wantAbsent / wantUnreached) — the two weaknesses #1740
//     predicted a static walk would have, pinned as executable cases: a
//     reference in a COMMENT, and a DEAD test. Plus the false positive a
//     name-based rule produces (a local helper that merely shares the name),
//     and a reference that is not a call.
//   - DECLARED LIMITS (marked limit:true) — cases where the detector's answer
//     is knowingly wrong, recorded so they are learned from a test rather than
//     from an incident. A skipped test still counts; a wiring held in a
//     package-level var of func type is not seen at all. Both directions are
//     stated: the first over-credits, the second under-credits (fail-closed).
//
// Every case asserts the construct it plants is actually PRESENT in the source
// it hands the detector, before any verdict is checked — a corpus that has
// quietly stopped containing its own cases reads as a pass (#1450, and the same
// guard architecture_hookbody_shapes_test.go carries).
package agents

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"golang.org/x/tools/go/packages"
)

// corpusModule and corpusContractPkg are the synthetic stand-ins for this
// repo's own module and contracttesting package. The detector takes the
// contract package path as an argument precisely so this corpus can exist: the
// real one is under core/internal/, which no temp module can import.
const (
	corpusModule      = "wiringshapes.test"
	corpusContractPkg = corpusModule + "/contracttesting"
	corpusCasesDir    = "cases"
)

// The two entry points the corpus exercises. One is enough for most rows; the
// second exists so at least one row can pin that the detector resolves calls
// PER NAME rather than reporting "some contract was called".
const (
	epConfined   = "AssertHookPathConfined"
	epDisclosure = "AssertHookDisclosureMatchesInstalled"
)

// verdict is the detector's three-valued answer about one entry point in one
// package. Three values rather than a bool, because "saw nothing" and "saw a
// call that nothing runs" are different findings that need different advice,
// and a corpus that collapsed them could not tell a detector which reports
// dead wirings from one which is simply blind to them.
type verdict int

const (
	wantAbsent    verdict = iota // no call of this entry point was seen at all
	wantCalled                   // a call reachable from a function `go test` runs
	wantUnreached                // a call exists, but nothing `go test` runs reaches it
)

func (v verdict) String() string {
	switch v {
	case wantCalled:
		return "called"
	case wantUnreached:
		return "unreached"
	default:
		return "absent"
	}
}

// wiringCase is one synthetic adapter test package.
type wiringCase struct {
	// dir is the package directory, and the case's identity in failures.
	dir string
	// files are the sources, filename → content. At least one must be a
	// _test.go, or the case is testing the detector's file filter rather than
	// what it claims to.
	files map[string]string
	// want lists the non-absent expectations. Any entry point NOT named here
	// must come back absent — which is how the partial-wiring row proves the
	// detector resolves per name rather than per package.
	want map[string]verdict
	// wantRoots, when non-nil, pins the number of go-test entry functions the
	// detector found. Only the rows whose subject IS the root rule set it.
	wantRoots *int
	// needle must appear in the case's own sources.
	needle string
	// why the case exists.
	why string
	// limit marks a row whose pinned verdict is a KNOWN WRONG ANSWER, recorded
	// deliberately. Printed in failures so a future rewrite does not read it as
	// desired behaviour.
	limit bool
}

func intp(n int) *int { return &n }

// stubSource is the corpus's stand-in for contracttesting. Signatures are only
// as real as they need to be to compile a call.
const stubSource = `package contracttesting

import "testing"

func AssertHookPathConfined(t *testing.T, r any)               { _ = t; _ = r }
func AssertHookDisclosureMatchesInstalled(t *testing.T, d any) { _ = t; _ = d }
`

// wiringShapes is the corpus. Order is capabilities, then must-not-credit, then
// declared limits.
func wiringShapes() []wiringCase {
	return []wiringCase{
		// ---------- capabilities ----------
		{
			dir:    "direct",
			want:   map[string]verdict{epConfined: wantCalled},
			needle: "func TestHookPathConfined(t *testing.T)",
			why:    "the spelling every hooks-declaring adapter uses today",
			files: map[string]string{
				"x_test.go": `package shape

import (
	"testing"

	"` + corpusContractPkg + `"
)

func TestHookPathConfined(t *testing.T) {
	contracttesting.AssertHookPathConfined(t, nil)
}
`,
			},
		},
		{
			dir:    "subtest_closure",
			want:   map[string]verdict{epConfined: wantCalled},
			needle: `t.Run("confined", func(t *testing.T) {`,
			why:    "the call is inside a closure passed to t.Run; the enclosing Test is still what runs it",
			files: map[string]string{
				"x_test.go": `package shape

import (
	"testing"

	"` + corpusContractPkg + `"
)

func TestReceiver(t *testing.T) {
	t.Run("confined", func(t *testing.T) {
		contracttesting.AssertHookPathConfined(t, nil)
	})
}
`,
			},
		},
		{
			dir:    "helper_two_hops",
			want:   map[string]verdict{epConfined: wantCalled},
			needle: "func wireOuter(t *testing.T) {\n\twireInner(t)",
			why:    "Test → wireOuter → wireInner → the contract; a one-hop-only reachability walk would miss it",
			files: map[string]string{
				"x_test.go": `package shape

import (
	"testing"

	"` + corpusContractPkg + `"
)

func TestReceiver(t *testing.T) { wireOuter(t) }

func wireOuter(t *testing.T) {
	wireInner(t)
}

func wireInner(t *testing.T) {
	contracttesting.AssertHookPathConfined(t, nil)
}
`,
			},
		},
		{
			dir:    "method_hop",
			want:   map[string]verdict{epConfined: wantCalled},
			needle: "func (f *fixture) wire(t *testing.T)",
			why:    "the hop is a METHOD, whose call resolves through info.Uses on the selector rather than on a bare ident",
			files: map[string]string{
				"x_test.go": `package shape

import (
	"testing"

	"` + corpusContractPkg + `"
)

type fixture struct{}

func (f *fixture) wire(t *testing.T) {
	contracttesting.AssertHookPathConfined(t, nil)
}

func TestReceiver(t *testing.T) {
	f := &fixture{}
	f.wire(t)
}
`,
			},
		},
		{
			dir:    "aliased_import",
			want:   map[string]verdict{epConfined: wantCalled},
			needle: `ct "` + corpusContractPkg + `"`,
			why: "the import is aliased, so the source never contains the string " +
				"\"contracttesting.AssertHookPathConfined\" — a grep-based rule reports this adapter as unwired",
			files: map[string]string{
				"x_test.go": `package shape

import (
	"testing"

	ct "` + corpusContractPkg + `"
)

func TestReceiver(t *testing.T) {
	ct.AssertHookPathConfined(t, nil)
}
`,
			},
		},
		{
			dir:    "external_test_package",
			want:   map[string]verdict{epConfined: wantCalled},
			needle: "package shape_test",
			why:    "the wiring lives in the external test package, a separate types.Package from the one under test",
			files: map[string]string{
				"doc.go": "package shape\n",
				"x_test.go": `package shape_test

import (
	"testing"

	"` + corpusContractPkg + `"
)

func TestReceiver(t *testing.T) {
	contracttesting.AssertHookPathConfined(t, nil)
}
`,
			},
		},
		{
			dir:    "cross_variant",
			want:   map[string]verdict{epConfined: wantCalled},
			needle: "shape.WireIt(t)",
			why: "the chain CROSSES variants: an external test package calls a helper declared in an " +
				"in-package _test.go. Scanning the two variants separately breaks this edge",
			files: map[string]string{
				"doc.go": "package shape\n",
				"helper_test.go": `package shape

import (
	"testing"

	"` + corpusContractPkg + `"
)

func WireIt(t *testing.T) {
	contracttesting.AssertHookPathConfined(t, nil)
}
`,
				"x_test.go": `package shape_test

import (
	"testing"

	"` + corpusModule + `/` + corpusCasesDir + `/cross_variant"
)

func TestReceiver(t *testing.T) {
	shape.WireIt(t)
}
`,
			},
		},
		{
			dir:    "helper_in_non_test_file",
			want:   map[string]verdict{epConfined: wantCalled},
			needle: "// non-test file",
			why: "the helper is in an ordinary .go file. It still EXECUTES when a Test calls it, so " +
				"restricting call sites to _test.go would under-credit a real wiring",
			files: map[string]string{
				"helper.go": `// non-test file
package shape

import (
	"testing"

	"` + corpusContractPkg + `"
)

func WireIt(t *testing.T) {
	contracttesting.AssertHookPathConfined(t, nil)
}
`,
				"x_test.go": `package shape

import "testing"

func TestReceiver(t *testing.T) { WireIt(t) }
`,
			},
		},
		{
			dir:       "benchmark_root",
			want:      map[string]verdict{epConfined: wantCalled},
			wantRoots: intp(1),
			needle:    "func BenchmarkReceiver(b *testing.B)",
			why:       "a Benchmark is also run by the go tool, so it is a root; pinned with wantRoots so the root rule itself is under test",
			files: map[string]string{
				"x_test.go": `package shape

import (
	"testing"

	"` + corpusContractPkg + `"
)

func BenchmarkReceiver(b *testing.B) {
	wire(b)
}

func wire(tb testing.TB) {
	_ = tb
	contracttesting.AssertHookPathConfined(nil, nil)
}
`,
			},
		},
		{
			dir: "partial_wiring",
			want: map[string]verdict{
				epConfined: wantCalled,
				// epDisclosure deliberately unlisted → must be absent.
			},
			needle: "AssertHookPathConfined",
			why: "one family wired and the other not. Without this row a detector that answered " +
				"\"some contract was called\" would satisfy every other case in the corpus",
			files: map[string]string{
				"x_test.go": `package shape

import (
	"testing"

	"` + corpusContractPkg + `"
)

func TestReceiver(t *testing.T) {
	contracttesting.AssertHookPathConfined(t, nil)
}
`,
			},
		},

		// ---------- must not credit ----------
		{
			dir:    "only_in_a_comment",
			want:   map[string]verdict{}, // both absent
			needle: "//\tcontracttesting.AssertHookPathConfined(t, nil)",
			why: "#1740's first stated worry about a static walk: the name appears ONLY in a comment. " +
				"A comment is not an ast.CallExpr, so an AST walk cannot be satisfied by one",
			files: map[string]string{
				"x_test.go": `package shape

import "testing"

// TestReceiver would wire the contract like this:
//
//	contracttesting.AssertHookPathConfined(t, nil)
//
// but it does not.
func TestReceiver(t *testing.T) {
	_ = t
}
`,
			},
		},
		{
			dir:    "in_a_string_literal",
			want:   map[string]verdict{},
			needle: `t.Skip("TODO: wire contracttesting.AssertHookPathConfined")`,
			why:    "the same worry one spelling over — a TODO string, a skip reason, a doc table",
			files: map[string]string{
				"x_test.go": `package shape

import "testing"

func TestReceiver(t *testing.T) {
	t.Skip("TODO: wire contracttesting.AssertHookPathConfined")
}
`,
			},
		},
		{
			dir:    "referenced_not_called",
			want:   map[string]verdict{},
			needle: "var _ = contracttesting.AssertHookPathConfined",
			why: "a reference that executes nothing — at file scope and again inside the Test. Requiring a " +
				"CallExpr is what keeps a compile-time nod from counting as a wiring",
			files: map[string]string{
				"x_test.go": `package shape

import (
	"testing"

	"` + corpusContractPkg + `"
)

var _ = contracttesting.AssertHookPathConfined

func TestReceiver(t *testing.T) {
	f := contracttesting.AssertHookPathConfined
	_ = f
}
`,
			},
		},
		{
			dir:    "locally_shadowed",
			want:   map[string]verdict{},
			needle: "func AssertHookPathConfined(t *testing.T, r any)",
			why: "MUST NOT FIRE: a helper declared in the adapter's own test package with the same NAME. " +
				"A name-based rule credits it; resolving the callee's package does not",
			files: map[string]string{
				"x_test.go": `package shape

import "testing"

func AssertHookPathConfined(t *testing.T, r any) { _ = t; _ = r }

func TestReceiver(t *testing.T) {
	AssertHookPathConfined(t, nil)
}
`,
			},
		},
		{
			dir:    "dead_helper",
			want:   map[string]verdict{epConfined: wantUnreached},
			needle: "func wireHookPathConfined(t *testing.T)",
			why: "#1740's second stated worry: the wiring is real code in a real file, and nothing runs it. " +
				"Reported as UNREACHED rather than absent so the failure says re-attach it, not re-write it",
			files: map[string]string{
				"x_test.go": `package shape

import (
	"testing"

	"` + corpusContractPkg + `"
)

func wireHookPathConfined(t *testing.T) {
	contracttesting.AssertHookPathConfined(t, nil)
}

func TestSomethingElse(t *testing.T) {
	_ = t
}
`,
			},
		},
		{
			dir:       "no_test_functions_at_all",
			want:      map[string]verdict{epConfined: wantUnreached},
			wantRoots: intp(0),
			needle:    "func wireHookPathConfined(t *testing.T)",
			why: "the vacuity condition the tripwire fatals on: a test file with no go-test entry function. " +
				"Roots must read 0, so 'nothing is wired' and 'nothing could be reached' stay distinguishable",
			files: map[string]string{
				"x_test.go": `package shape

import (
	"testing"

	"` + corpusContractPkg + `"
)

func wireHookPathConfined(t *testing.T) {
	contracttesting.AssertHookPathConfined(t, nil)
}
`,
			},
		},
		{
			dir:       "lowercase_after_prefix",
			want:      map[string]verdict{epConfined: wantUnreached},
			wantRoots: intp(0),
			needle:    "func Testify(t *testing.T)",
			why: "cmd/go does NOT run Testify: the rune after the prefix is lowercase. Treating it as a root " +
				"would credit a wiring the go tool never executes",
			files: map[string]string{
				"x_test.go": `package shape

import (
	"testing"

	"` + corpusContractPkg + `"
)

func Testify(t *testing.T) {
	contracttesting.AssertHookPathConfined(t, nil)
}
`,
			},
		},

		// ---------- declared limits ----------
		{
			dir:    "skipped_test",
			want:   map[string]verdict{epConfined: wantCalled},
			limit:  true,
			needle: `t.Skip("upstream CLI not installed")`,
			why: "LIMIT, over-crediting: reachability here is syntactic and does not evaluate, so a test that " +
				"skips before it reaches the contract still counts as wired. The DIRECTORY NAME is load-bearing " +
				"too — it ends in _test, and this row is what caught testVariantUnitsByPackage recovering the " +
				"package under test by trimming that suffix off PkgPath instead of reading the variant tag",
			files: map[string]string{
				"x_test.go": `package shape

import (
	"testing"

	"` + corpusContractPkg + `"
)

func TestReceiver(t *testing.T) {
	t.Skip("upstream CLI not installed")
	contracttesting.AssertHookPathConfined(t, nil)
}
`,
			},
		},
		{
			dir:    "guarded_by_constant_false",
			want:   map[string]verdict{epConfined: wantCalled},
			limit:  true,
			needle: "if false {",
			why:    "LIMIT, over-crediting: the same non-evaluation one spelling over",
			files: map[string]string{
				"x_test.go": `package shape

import (
	"testing"

	"` + corpusContractPkg + `"
)

func TestReceiver(t *testing.T) {
	if false {
		contracttesting.AssertHookPathConfined(t, nil)
	}
}
`,
			},
		},
		{
			dir:    "value_then_called",
			want:   map[string]verdict{},
			limit:  true,
			needle: "confine := contracttesting.AssertHookPathConfined",
			why: "LIMIT, under-crediting (fail-closed): the entry point is taken as a VALUE and called through " +
				"a variable, so the call expression names a types.Var and resolves to no function. This is the " +
				"price of requiring a CallExpr, which is what makes referenced_not_called above come back absent",
			files: map[string]string{
				"x_test.go": `package shape

import (
	"testing"

	"` + corpusContractPkg + `"
)

func TestReceiver(t *testing.T) {
	confine := contracttesting.AssertHookPathConfined
	confine(t, nil)
}
`,
			},
		},
		{
			dir:    "package_var_func_literal",
			want:   map[string]verdict{},
			limit:  true,
			needle: "var wire = func(t *testing.T) {",
			why: "LIMIT, under-crediting (fail-closed): the literal's body hangs off a ValueSpec, not a " +
				"FuncDecl, so the walk never enters it and the wiring is reported missing. Same declared limit " +
				"contracttesting/seam_walk_corpus_test.go pins for its own walk. Loud, not silent",
			files: map[string]string{
				"x_test.go": `package shape

import (
	"testing"

	"` + corpusContractPkg + `"
)

var wire = func(t *testing.T) {
	contracttesting.AssertHookPathConfined(t, nil)
}

func TestReceiver(t *testing.T) {
	wire(t)
}
`,
			},
		},
	}
}

func TestContractWiringDetectorCatchesEveryKnownShape(t *testing.T) {
	shapes := wiringShapes()
	if len(shapes) == 0 {
		t.Fatal("the corpus is empty; this test would pass having pinned nothing")
	}
	dir := writeWiringCorpus(t, shapes)
	byCase := loadWiringCorpus(t, dir, shapes)

	want := []string{epConfined, epDisclosure}
	for _, shape := range shapes {
		units := byCase[shape.dir]
		if len(units) == 0 {
			t.Fatalf("case %s: the corpus load returned no test-variant files for it — every "+
				"verdict below would be about nothing", shape.dir)
		}
		scan := scanContractWirings(units, corpusContractPkg, want)
		if scan.TestFiles == 0 {
			t.Fatalf("case %s: detector walked 0 _test.go files (%s)", shape.dir, describeScan(scan))
		}
		if shape.wantRoots != nil && scan.Roots != *shape.wantRoots {
			t.Errorf("case %s: detector found %d go-test entry function(s), want %d (%s)\n\twhy this case exists: %s",
				shape.dir, scan.Roots, *shape.wantRoots, describeScan(scan), shape.why)
		}
		for _, ep := range want {
			got := verdictFor(scan, ep)
			expect := shape.want[ep] // zero value is wantAbsent
			if got == expect {
				continue
			}
			t.Errorf("case %s: detector reports %s = %s, want %s (%s)\n\twhy this case exists: %s%s",
				shape.dir, ep, got, expect, describeScan(scan), shape.why, limitNote(shape))
		}
	}
}

func limitNote(c wiringCase) string {
	if !c.limit {
		return ""
	}
	return "\n\tNOTE: this row pins a DECLARED LIMIT — the verdict above is a known wrong answer, " +
		"recorded so it is learned from a test rather than from an incident. If a rewrite changed it, " +
		"that may be an improvement; update the row deliberately."
}

func verdictFor(s wiringScan, ep string) verdict {
	if _, ok := s.Called[ep]; ok {
		return wantCalled
	}
	if _, ok := s.Unreached[ep]; ok {
		return wantUnreached
	}
	return wantAbsent
}

// writeWiringCorpus materializes the corpus as a self-contained module and
// asserts every planted shape actually landed in its own sources.
func writeWiringCorpus(t *testing.T, shapes []wiringCase) string {
	t.Helper()

	dir := t.TempDir()
	mustWriteCorpusFile(t, filepath.Join(dir, "go.mod"), "module "+corpusModule+"\n\ngo 1.25\n")
	stub := filepath.Join(dir, "contracttesting")
	if err := os.MkdirAll(stub, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", stub, err)
	}
	mustWriteCorpusFile(t, filepath.Join(stub, "stub.go"), stubSource)

	seen := map[string]bool{}
	for _, shape := range shapes {
		if seen[shape.dir] {
			t.Fatalf("duplicate case dir %q — one case would silently overwrite the other", shape.dir)
		}
		seen[shape.dir] = true
		assertPlantedWiringPresent(t, shape)

		caseDir := filepath.Join(dir, corpusCasesDir, shape.dir)
		if err := os.MkdirAll(caseDir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", caseDir, err)
		}
		for name, src := range shape.files {
			mustWriteCorpusFile(t, filepath.Join(caseDir, name), src)
		}
	}
	return dir
}

// assertPlantedWiringPresent is the guard against a corpus that has quietly
// stopped containing what it claims to. A case whose needle is missing proves
// nothing, and would read as a pass for every wantAbsent row.
//
// It also enforces the two structural preconditions a case cannot be
// meaningful without: at least one file, and at least one _test.go among them.
func assertPlantedWiringPresent(t *testing.T, c wiringCase) {
	t.Helper()

	if c.needle == "" {
		t.Fatalf("case %s declares no needle; every case must assert the construct it plants is present", c.dir)
	}
	if c.why == "" {
		t.Fatalf("case %s declares no reason; a row nobody can justify is a row nobody can delete safely", c.dir)
	}
	if len(c.files) == 0 {
		t.Fatalf("case %s declares no files", c.dir)
	}
	tests := 0
	joined := ""
	for name, src := range c.files {
		if strings.HasSuffix(name, "_test.go") {
			tests++
		}
		joined += src
	}
	if tests == 0 {
		t.Fatalf("case %s has no _test.go file; it would exercise the detector's file filter rather "+
			"than the shape it claims to test", c.dir)
	}
	if !strings.Contains(joined, c.needle) {
		t.Fatalf("case %s does not contain its own needle %q — the case is not testing what it says it tests",
			c.dir, c.needle)
	}
}

// loadWiringCorpus type-checks the corpus and returns each case's test-variant
// files, keyed by case dir.
func loadWiringCorpus(t *testing.T, dir string, shapes []wiringCase) map[string][]sourceUnit {
	t.Helper()

	cfg := &packages.Config{
		Mode: packages.NeedName | packages.NeedSyntax |
			packages.NeedTypes | packages.NeedTypesInfo,
		Tests: true,
		Dir:   dir,
		// GOWORK=off: the corpus is a standalone module in a temp dir, and an
		// inherited workspace would try to resolve it against this repo's.
		Env: append(os.Environ(), "GOWORK=off"),
	}
	pkgs, err := packages.Load(cfg, "./"+corpusCasesDir+"/...")
	if err != nil {
		t.Fatalf("packages.Load(corpus): %v", err)
	}
	if n := packages.PrintErrors(pkgs); n > 0 {
		t.Fatalf("the corpus does not build (%d error(s)) — without TypesInfo the detector resolves "+
			"no callee at all and every wantAbsent row would pass for the wrong reason", n)
	}

	prefix := corpusModule + "/" + corpusCasesDir + "/"
	byCase := map[string][]sourceUnit{}
	for under, units := range testVariantUnitsByPackage(t, pkgs) {
		name, ok := strings.CutPrefix(under, prefix)
		if !ok {
			t.Fatalf("corpus load returned an unexpected package %q", under)
		}
		byCase[name] = units
	}

	// The corpus is only worth what it loaded. A case that silently failed to
	// materialize would be reported by the per-case guard in the driver, but
	// naming the whole set here makes a wholesale load failure legible in one
	// line instead of N.
	var missing []string
	for _, shape := range shapes {
		if len(byCase[shape.dir]) == 0 {
			missing = append(missing, shape.dir)
		}
	}
	if len(missing) > 0 {
		slices.Sort(missing)
		t.Fatalf("the corpus load produced no test-variant files for %d case(s): %s",
			len(missing), strings.Join(missing, ", "))
	}
	return byCase
}

func mustWriteCorpusFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
