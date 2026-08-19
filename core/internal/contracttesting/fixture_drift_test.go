// fixture_drift_test.go closes the direction the vacuity guards cannot see.
//
// Each obligation of a receiver-shaped family states its fixture TWICE: once in
// the contract's own t.Run body, which is what a real adapter runs, and once in
// the self-test's armBuilder table, which is what the negative self-tests drive.
// The restatement is deliberate — those t.Run bodies are what an adapter author
// reads to learn what their adapter owes, and #1512 declined to fold them into a
// shared table for exactly that reason — but only ONE direction of drift is
// caught today. A SELF-TEST that stops posting the input its obligation grades
// goes silent against a deliberately-wrong fixture, and its own case fails. An
// ENTRY POINT that changes an input does not: the contract goes on grading
// adapters against the new input while the self-tests go on proving something
// about the old one, both stay green, and the committed mutation evidence
// quietly attests to an input production no longer posts (#1520).
//
// That is worse than a stale number — #1503's species, an artefact that
// documents behaviour without being produced by it — because these self-tests
// ARE the evidence the contracts can fail at all. Evidence that has detached
// from what it certifies is indistinguishable from evidence that still holds.
//
// Two rules, over one parse of the package's own sources. Both follow
// seam_walk_test.go's shape rather than inventing a second one: derive the
// subject list from the code, keep the decision procedure in a pure function so
// a committed corpus can pin its verdicts, and let an exemption carry a reason.
//
// # Rule 1 — the two statements agree, obligation by obligation
//
// TestTheEntryPointAndItsSelfTestsStateTheSameFixture takes the duplication as
// given (acceptance option 2 of #1520) and removes the silence instead. For each
// receiver-shaped family it compares, per obligation:
//
//   - the multiset of BASIC LITERALS the two bodies contain — the subdirectory
//     name, the ".." repeat count, the event-name suffix, the symlink error
//     format. This is where "a different path spelling, a different event name"
//     actually lives;
//   - the set of SHARED IDENTIFIERS each body references, where "shared" means
//     declared at package level in a NON-test file: the arm it drives, the
//     `what` constant its failure will print, the fixture helper it builds
//     through, the logger type it hands over. Everything a self-test
//     legitimately adds — fakePathReceiver, receiverBreak, its own locals — is
//     declared in a _test.go and drops out by construction rather than by an
//     exemption list;
//   - the obligation NAMES, in order. That half catches the drift no per-input
//     comparison can: an obligation added to the entry point that no self-test
//     drives. Nothing else would notice, because the seam walk's rule 2 fires
//     only when a new obligation also introduces a new ARM, and a seventh
//     confinement obligation reusing assertRefused introduces none.
//
// The one legitimate asymmetry is the reporter seam itself — production passes
// realT(t), a self-test passes the armT it was handed — and it is named in
// seamAdapters rather than absorbed into a looser comparison.
//
// What it does NOT compare is expression STRUCTURE. Two bodies holding the same
// literals and the same shared references in a different arrangement agree as
// far as this rule is concerned; that limit is pinned by a want:false corpus row
// rather than left to be discovered. Structural comparison was tried first and
// abandoned: the two sides already differ in ways nobody should have to mirror —
// the entry point binds `root := r.Root(t)` where the builder inlines it, and
// obligation 6 calls the same path `inTree` on one side and `spelled` on the
// other — so an AST-equality rule would have been red on arrival and could only
// be made green by mandating a mirroring the code does not owe.
//
// # Rule 2 — every receiverBreak knob is still spent
//
// The smaller sibling from the same issue: a knob no self-test uses is dead
// evidence with no failure attached, and nothing checked it. Two obligations per
// knob, because a knob can die at either end. The fixture can stop HONOURING it
// — the receiver ignores the field, so setting it yields a correct receiver and
// the negative self-test that sets it grades nothing while still going through
// the motions. Or a self-test can stop SPENDING it, which is the committed
// mutation nothing runs.
//
// The knob set is derived, never listed: every field of receiverBreak, plus
// every constant of every enum type a receiverBreak field is declared with. The
// second half matters more than it looks — `confine` and `receipt` are single
// fields carrying three and four distinct mutations, so a field-level rule would
// report `confine` as spent while confineAcceptUnresolvable rotted.
package contracttesting

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// unspentKnobs names any receiverBreak knob deliberately not driven by a
// negative self-test, with the reason it is not.
//
// Deliberately empty, and it has to stay reviewable when it is not: an entry is
// a claim that a committed mutation is worth keeping while nothing runs it,
// which is the shape #1479 exists to refuse. Its keys are existence-checked
// below, the same way deferredToTheSeam's are, so an entry that stopped naming a
// real knob fails rather than exempting nothing.
var unspentKnobs = map[string]string{}

// knobsHonouredByAbsence names every knob the fixture implements by matching
// NOTHING, so a walk looking for the knob's own name cannot see it.
//
// One entry, and it is worth reading rather than skipping: receiptNever is the
// receiver that forgets to count entirely, and the fixture expresses that by
// having serve and dispatch each guard their placement with an `if` that this
// setting matches none of. Naming it here is the same trade deferredToTheSeam
// makes — the knowledge a walk cannot derive is written down and
// existence-checked, rather than being inferred by the next reader from the
// absence of a branch.
var knobsHonouredByAbsence = map[string]string{
	"receiptNever": "the receiver that forgets entirely — it is honoured by matching none of serve's " +
		"and dispatch's placement guards, so no branch names it (#1413, #1497).",
}

// seamAdapters are the two spellings of the reporter seam, and the ONE
// legitimate asymmetry between an entry point's statement of a fixture and a
// self-test's. Production wires an arm with realT(t); a self-test hands it the
// armT it was given. Neither says anything about the fixture, so both are
// dropped before the statements are compared.
//
// Named here rather than absorbed into a looser comparison, because the whole
// value of rule 1 is that everything else has to match.
var seamAdapters = map[string]bool{"realT": true, "armT": true}

// --- the walk ---

// fixtureFacts is everything both rules need, from one parse.
type fixtureFacts struct {
	// knobs maps every receiverBreak knob to what is known about it. Populated
	// in two passes: the fields during the walk, the enum constants afterwards,
	// because a field and its enum are declared in no guaranteed order.
	knobs map[string]knob

	// knobEnums is every named, non-builtin type a receiverBreak field is
	// declared with.
	knobEnums map[string]bool

	// enumConsts is every typed constant in the package, in declaration order,
	// filtered against knobEnums once the walk is done.
	enumConsts []enumConst

	// honoured is every identifier appearing in a function BODY of a _test.go
	// that does NOT drive obligations — the fixture and the wirings. A knob the
	// fixture never reads is a knob whose mutation produces a correct receiver.
	//
	// Bodies rather than whole files, because a struct field's declaration and a
	// const spec are not bodies: a knob that is merely declared would otherwise
	// count as honoured by its own definition, which is the vacuity this rule
	// exists to refuse.
	honoured map[string]bool

	// spent is every identifier mentioned in a _test.go that also mentions
	// mustReport — the same seeding rule seam_walk_test.go's rule 2 uses, and it
	// carries the same property: being NAMED by a positive test is not being
	// DRIVEN by a negative one.
	spent map[string]bool

	// shared is every name declared at package level in a NON-test file: the
	// vocabulary an entry point and a self-test must agree on. Test-file
	// declarations are excluded on purpose, because that is exactly the set a
	// self-test may legitimately reference and an entry point may not.
	shared map[string]bool

	// families is one row per package-level receiverFamily var, so a fourth
	// receiver-shaped family is graded by existing rather than by being added to
	// a table here.
	families []familyStatement

	// funcs maps a function name to its declaration, across every file.
	funcs map[string]*ast.FuncDecl
}

// knob is one committed receiverBreak mutation knob.
type knob struct {
	// where is the file:line it is declared at.
	where string

	// what names the knob's kind for a failure message.
	what string

	// correct marks the zero value of an enum knob — the CORRECT setting, spent
	// by every family's vacuity guard through receiverBreak{} and therefore
	// never named by a negative self-test.
	correct bool
}

// enumConst is one typed constant, before it is known whether its type is a
// receiverBreak knob enum.
type enumConst struct {
	name  string
	typ   string
	where string
}

// familyStatement is one receiverFamily var: the entry point it names and the
// builder table it drives.
type familyStatement struct {
	varName    string
	entryPoint string
	builders   string
}

// newFixtureFacts allocates every map absorb writes into. It exists so the
// corpus can build facts from synthetic sources through the same absorb the real
// walk uses, rather than through a second reader that could disagree with it.
func newFixtureFacts() *fixtureFacts {
	return &fixtureFacts{
		knobs:     map[string]knob{},
		knobEnums: map[string]bool{},
		honoured:  map[string]bool{},
		spent:     map[string]bool{},
		shared:    map[string]bool{},
		funcs:     map[string]*ast.FuncDecl{},
	}
}

func parseFixtureFacts(t *testing.T, dir string) fixtureFacts {
	t.Helper()
	f := newFixtureFacts()
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
		isTest := strings.HasSuffix(name, "_test.go")
		sawTest = sawTest || isTest
		sawNonTest = sawNonTest || !isTest
		f.absorb(fset, name, isTest, file)
	}
	// A walk that read nothing satisfies both rules perfectly — the same refusal
	// parseSeamFacts makes, for the same reason (AGENTS.md: a verification
	// mechanism must fail loudly when it cannot run).
	if !sawNonTest || !sawTest {
		t.Fatalf("the walk read %v non-test and %v test files — a parse that finds nothing satisfies every rule below", sawNonTest, sawTest)
	}
	f.resolveEnumKnobs()
	return *f
}

// absorb records everything one file contributes.
func (f *fixtureFacts) absorb(fset *token.FileSet, name string, isTest bool, file *ast.File) {
	driving := identsIn(file)["mustReport"]
	for _, decl := range file.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			f.funcs[d.Name.Name] = d
			if !isTest && d.Recv == nil {
				f.shared[d.Name.Name] = true
			}
			if isTest && !driving && d.Body != nil {
				for id := range identsIn(d.Body) {
					f.honoured[id] = true
				}
			}
		case *ast.GenDecl:
			f.absorbGenDecl(fset, name, isTest, d)
		}
	}
	if isTest && driving {
		for id := range identsIn(file) {
			f.spent[id] = true
		}
	}
}

// absorbGenDecl records one file's type, const and var declarations: the shared
// vocabulary, receiverBreak's fields and enums, and the receiverFamily rows.
func (f *fixtureFacts) absorbGenDecl(fset *token.FileSet, file string, isTest bool, d *ast.GenDecl) {
	// inherited is per-block state: inside one const block a spec with no type
	// of its own inherits the last one named, which is how an iota enum is
	// spelled and the reason a constant's type cannot be read off its own spec.
	var inherited string
	for _, spec := range d.Specs {
		switch s := spec.(type) {
		case *ast.TypeSpec:
			if !isTest {
				f.shared[s.Name.Name] = true
			}
			if st, ok := s.Type.(*ast.StructType); ok && s.Name.Name == "receiverBreak" {
				f.absorbKnobFields(fset, file, st)
			}
		case *ast.ValueSpec:
			if s.Type != nil {
				inherited = ""
				if id, ok := s.Type.(*ast.Ident); ok {
					inherited = id.Name
				}
			}
			for _, n := range s.Names {
				if !isTest {
					f.shared[n.Name] = true
				}
			}
			if d.Tok == token.CONST && inherited != "" {
				for _, n := range s.Names {
					f.enumConsts = append(f.enumConsts, enumConst{name: n.Name, typ: inherited, where: where(fset, file, n.Pos())})
				}
			}
			if d.Tok == token.VAR {
				f.absorbFamily(s)
			}
		}
	}
}

// absorbKnobFields records receiverBreak's fields, and the enum types its fields
// are declared with so their constants can be collected as knobs too.
func (f *fixtureFacts) absorbKnobFields(fset *token.FileSet, file string, st *ast.StructType) {
	for _, field := range st.Fields.List {
		for _, n := range field.Names {
			f.knobs[n.Name] = knob{where: where(fset, file, n.Pos()), what: "field"}
		}
		if id, ok := field.Type.(*ast.Ident); ok && !isBuiltinType(id.Name) {
			f.knobEnums[id.Name] = true
		}
	}
}

// resolveEnumKnobs turns the collected typed constants into knobs, keeping only
// those whose type a receiverBreak field is declared with. The FIRST constant of
// each such type is the zero value, i.e. the correct setting.
func (f *fixtureFacts) resolveEnumKnobs() {
	seen := map[string]bool{}
	for _, c := range f.enumConsts {
		if !f.knobEnums[c.typ] {
			continue
		}
		f.knobs[c.name] = knob{where: c.where, what: "setting of " + c.typ, correct: !seen[c.typ]}
		seen[c.typ] = true
	}
}

// absorbFamily records one package-level receiverFamily var.
func (f *fixtureFacts) absorbFamily(s *ast.ValueSpec) {
	for i, v := range s.Values {
		lit, ok := v.(*ast.CompositeLit)
		if !ok || i >= len(s.Names) {
			continue
		}
		if id, ok := lit.Type.(*ast.Ident); !ok || id.Name != "receiverFamily" {
			continue
		}
		row := familyStatement{varName: s.Names[i].Name}
		for _, elt := range lit.Elts {
			kv, ok := elt.(*ast.KeyValueExpr)
			if !ok {
				continue
			}
			key, ok := kv.Key.(*ast.Ident)
			if !ok {
				continue
			}
			switch key.Name {
			case "entryPoint":
				row.entryPoint = stringLit(kv.Value)
			case "builders":
				if id, ok := kv.Value.(*ast.Ident); ok {
					row.builders = id.Name
				}
			}
		}
		f.families = append(f.families, row)
	}
}

func where(fset *token.FileSet, file string, pos token.Pos) string {
	return file + ":" + strconv.Itoa(fset.Position(pos).Line)
}

// isBuiltinType reports whether name is a predeclared type. A receiverBreak
// field declared with one carries no constants to collect, and treating `bool`
// as an enum would make every `true` in the package a knob.
func isBuiltinType(name string) bool {
	switch name {
	case "bool", "string", "int", "int8", "int16", "int32", "int64",
		"uint", "uint8", "uint16", "uint32", "uint64", "uintptr",
		"float32", "float64", "complex64", "complex128",
		"byte", "rune", "error", "any":
		return true
	}
	return false
}

// stringLit unquotes a string literal expression, or returns "".
func stringLit(e ast.Expr) string {
	lit, ok := e.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return ""
	}
	s, err := strconv.Unquote(lit.Value)
	if err != nil {
		return ""
	}
	return s
}

// --- rule 1: the two statements agree ---

// obligation is one obligation's fixture statement, as ONE side states it.
type obligation struct {
	name string

	// literals is the multiset of basic literals the body contains, sorted. A
	// multiset rather than a set because "posted twice" and "posted once" are
	// different statements.
	literals []string

	// shared is the set of package-level, non-test identifiers the body
	// references, sorted.
	shared []string
}

// statementOf reads one obligation's fixture statement out of a body.
func statementOf(name string, body ast.Node, shared map[string]bool) obligation {
	ob := obligation{name: name}
	local := localsIn(body)
	set := map[string]bool{}
	ast.Inspect(body, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.BasicLit:
			// The kind is part of the key so the string "32" and the integer 32
			// are not the same statement.
			ob.literals = append(ob.literals, n.Kind.String()+" "+n.Value)
		case *ast.Ident:
			if shared[n.Name] && !seamAdapters[n.Name] && !local[n.Name] {
				set[n.Name] = true
			}
		}
		return true
	})
	slices.Sort(ob.literals)
	ob.shared = sortedKeys(set)
	return ob
}

// localsIn is every name the body binds itself: `x :=`, a var/const spec, a
// parameter, a range variable.
//
// They are subtracted because the two sides legitimately name their locals
// differently — obligation 6 calls the same path inTree on one side and spelled
// on the other — so a local that happens to collide with a package-level
// declaration would otherwise appear in one statement and not the other, and
// rule 1 would report a drift that is only a variable name. That is the one
// direction a false report is expensive in: this package's whole subject is
// evidence you can trust.
func localsIn(body ast.Node) map[string]bool {
	out := map[string]bool{}
	add := func(exprs []ast.Expr) {
		for _, e := range exprs {
			if id, ok := e.(*ast.Ident); ok {
				out[id.Name] = true
			}
		}
	}
	ast.Inspect(body, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.AssignStmt:
			if n.Tok == token.DEFINE {
				add(n.Lhs)
			}
		case *ast.ValueSpec:
			for _, id := range n.Names {
				out[id.Name] = true
			}
		case *ast.RangeStmt:
			add([]ast.Expr{n.Key, n.Value})
		case *ast.FuncLit:
			for _, p := range n.Type.Params.List {
				for _, id := range p.Names {
					out[id.Name] = true
				}
			}
		}
		return true
	})
	return out
}

// obligationsOfEntryPoint reads the entry point's statement: one obligation per
// top-level t.Run("name", func…) in its body.
//
// TOP-LEVEL statements only, rather than an ast.Inspect over the whole function.
// A nested t.Run belongs to the obligation that opened it, not to the family,
// and folding it in would silently invent an obligation the builder table has no
// row for. An entry point that moved its t.Run calls into a loop yields nothing
// here and trips the extraction guard rather than passing quietly.
func obligationsOfEntryPoint(fn *ast.FuncDecl, shared map[string]bool) []obligation {
	var out []obligation
	if fn == nil || fn.Body == nil {
		return nil
	}
	for _, stmt := range fn.Body.List {
		expr, ok := stmt.(*ast.ExprStmt)
		if !ok {
			continue
		}
		call, ok := expr.X.(*ast.CallExpr)
		if !ok || len(call.Args) != 2 {
			continue
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Run" {
			continue
		}
		name := stringLit(call.Args[0])
		lit, ok := call.Args[1].(*ast.FuncLit)
		if !ok || name == "" {
			continue
		}
		out = append(out, statementOf(name, lit.Body, shared))
	}
	return out
}

// obligationsOfBuilderTable reads the self-test's statement: one obligation per
// armBuilder element of the slice the builders function returns.
func obligationsOfBuilderTable(fn *ast.FuncDecl, shared map[string]bool) []obligation {
	var out []obligation
	if fn == nil || fn.Body == nil {
		return nil
	}
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		lit, ok := n.(*ast.CompositeLit)
		if !ok {
			return true
		}
		arr, ok := lit.Type.(*ast.ArrayType)
		if !ok {
			return true
		}
		if id, ok := arr.Elt.(*ast.Ident); !ok || id.Name != "armBuilder" {
			return true
		}
		for _, elt := range lit.Elts {
			row, ok := elt.(*ast.CompositeLit)
			if !ok {
				continue
			}
			name, body := builderRow(row)
			if name == "" || body == nil {
				continue
			}
			out = append(out, statementOf(name, body, shared))
		}
		return false
	})
	return out
}

// builderRow reads one armBuilder element, keyed or positional. Both spellings
// are read rather than one being mandated: a rule that quietly stops matching
// when a table is rewritten in the other style reports "no obligations" for a
// reason unrelated to any drift.
func builderRow(row *ast.CompositeLit) (string, ast.Node) {
	var name string
	var body ast.Node
	for i, elt := range row.Elts {
		value := elt
		field := ""
		if kv, ok := elt.(*ast.KeyValueExpr); ok {
			value = kv.Value
			if id, ok := kv.Key.(*ast.Ident); ok {
				field = id.Name
			}
		} else if i == 0 {
			field = "name"
		} else if i == 1 {
			field = "build"
		}
		switch field {
		case "name":
			name = stringLit(value)
		case "build":
			if lit, ok := value.(*ast.FuncLit); ok {
				body = lit.Body
			}
		}
	}
	return name, body
}

// compareStatements reports every way the two statements of one family's
// obligations disagree, or nil when they agree.
//
// It is a pure function over the extracted statements so a committed corpus can
// pin its verdicts — including the ones it must NOT return. A comparator that
// reported unconditionally would satisfy every mutation and read as thorough.
func compareStatements(family string, entry, self []obligation) []string {
	var out []string
	entryNames, selfNames := obligationNames(entry), obligationNames(self)
	if !slices.Equal(entryNames, selfNames) {
		return []string{fmt.Sprintf("%s states different obligations on its two sides:\n  the entry point: %v\n  the self-tests:  %v\n"+
			"An obligation the entry point runs and no self-test drives has no evidence it can fail; one a self-test drives and the "+
			"entry point no longer runs is evidence for nothing (#1520)", family, entryNames, selfNames)}
	}
	for i := range entry {
		e, s := entry[i], self[i]
		if !slices.Equal(e.literals, s.literals) {
			out = append(out, fmt.Sprintf("%s/%s: the two sides post different literals:\n  the entry point: %v\n  the self-tests:  %v\n"+
				"The self-tests' committed mutation evidence is about an input the contract no longer posts",
				family, e.name, e.literals, s.literals))
		}
		if !slices.Equal(e.shared, s.shared) {
			out = append(out, fmt.Sprintf("%s/%s: the two sides reference different shared declarations:\n  the entry point: %v\n  the self-tests:  %v\n"+
				"Both sides must drive the same arm, through the same fixture helpers, against the same message constants",
				family, e.name, e.shared, s.shared))
		}
	}
	return out
}

func obligationNames(obs []obligation) []string {
	out := make([]string, 0, len(obs))
	for _, ob := range obs {
		out = append(out, ob.name)
	}
	return out
}

// TestTheEntryPointAndItsSelfTestsStateTheSameFixture is rule 1.
func TestTheEntryPointAndItsSelfTestsStateTheSameFixture(t *testing.T) {
	f := parseFixtureFacts(t, ".")
	if len(f.families) == 0 {
		t.Fatal("no package-level receiverFamily var was found — either the receiver-shaped families are gone or the walk is not reading the package")
	}

	for _, fam := range f.families {
		t.Run(fam.varName, func(t *testing.T) {
			entryFn, ok := f.funcs[fam.entryPoint]
			if !ok {
				t.Fatalf("%s names entry point %q, which this package does not declare", fam.varName, fam.entryPoint)
			}
			buildersFn, ok := f.funcs[fam.builders]
			if !ok {
				t.Fatalf("%s names builder table %q, which this package does not declare", fam.varName, fam.builders)
			}

			entry := obligationsOfEntryPoint(entryFn, f.shared)
			self := obligationsOfBuilderTable(buildersFn, f.shared)
			// Extraction guards first. "The two sides agree" is trivially true of
			// two empty statements, and an extractor that stopped matching would
			// report exactly that (AGENTS.md: a verification mechanism must fail
			// loudly when it cannot run).
			if len(entry) == 0 {
				t.Fatalf("no t.Run obligation was extracted from %s — the rule below would pass by finding nothing", fam.entryPoint)
			}
			if len(self) == 0 {
				t.Fatalf("no armBuilder row was extracted from %s — the rule below would pass by finding nothing", fam.builders)
			}
			if !slices.ContainsFunc(entry, func(ob obligation) bool { return len(ob.literals) > 0 }) {
				t.Fatalf("not one obligation of %s carries a literal — every input this rule exists to compare has vanished from the extraction, "+
					"which is indistinguishable from two sides that agree", fam.entryPoint)
			}

			for _, msg := range compareStatements(fam.varName, entry, self) {
				t.Error(msg)
			}
		})
	}
}

// --- rule 2: every receiverBreak knob is still spent ---

// knobExemptions carries the two exemption maps rule 2 consults, so the corpus
// can drive the rule against a set of its own.
type knobExemptions struct {
	unspent   map[string]string
	byAbsence map[string]string
}

// knobViolations reports every way f's knob set has gone dead, or nil when every
// knob is both honoured and spent.
//
// Pure over the extracted facts, for the reason compareStatements is: the corpus
// drives it against synthetic fact sets, including ones it must pass.
//
// The two obligations are not redundant, and it is worth saying why the SPENT
// half does not subsume the HONOURED half. `spent` is reference-based — any
// mention of the knob in a file that drives obligations, the same
// over-approximation seam_walk_test.go's rule 2 makes and for the same reason —
// so a knob left behind in a table or a comment-adjacent expression counts as
// spent while nothing sets it. `honoured` grades the other end: a knob no
// fixture reads is one whose mutation builds a CORRECT receiver, so the case
// spending it goes through every motion and grades nothing.
func knobViolations(f fixtureFacts, exempt knobExemptions) []string {
	var out []string
	for _, name := range sortedKeys(f.knobs) {
		out = append(out, knobViolation(f, exempt, name)...)
	}
	out = append(out, danglingExemptions(f, "unspentKnobs", exempt.unspent)...)
	out = append(out, danglingExemptions(f, "knobsHonouredByAbsence", exempt.byAbsence)...)
	return out
}

// knobViolation grades one knob against both obligations.
func knobViolation(f fixtureFacts, exempt knobExemptions, name string) []string {
	var out []string
	k := f.knobs[name]
	if !f.honoured[name] {
		if reason, ok := exempt.byAbsence[name]; !ok {
			out = append(out, fmt.Sprintf("receiverBreak knob %s (%s, %s) is read by no fixture — a mutation that sets it builds a CORRECT "+
				"receiver, so the case spending it goes through every motion and grades nothing. Honour it in the fixture, delete it, or "+
				"name it in knobsHonouredByAbsence with the reason (#1520)", name, k.what, k.where))
		} else if reason == "" {
			out = append(out, blankExemption("knobsHonouredByAbsence", name))
		}
	}
	if k.correct {
		// The zero value is what receiverBreak{} MEANS, so every family's
		// vacuity guard spends it and no negative self-test ever names it.
		// Requiring it below would be requiring the correct setting to be a
		// mutation.
		return out
	}
	if f.spent[name] {
		return out
	}
	if reason, ok := exempt.unspent[name]; ok {
		if reason == "" {
			out = append(out, blankExemption("unspentKnobs", name))
		}
		return out
	}
	return append(out, fmt.Sprintf("receiverBreak knob %s (%s, %s) is spent by no negative self-test — it is a committed mutation with no "+
		"failure attached, which is evidence for nothing. Drive it from a <family>_selftest_test.go, delete it, or name it in unspentKnobs "+
		"with the reason (#1520)", name, k.what, k.where))
}

func blankExemption(mapName, knobName string) string {
	return fmt.Sprintf("%s exempts %q with no reason — an exemption whose justification is blank is one nobody can review", mapName, knobName)
}

// danglingExemptions reports an entry that stopped naming a real knob. An
// exemption that exempts nothing is a silent no-op, and these maps exist to be
// reviewed — the same reasoning deferredToTheSeam and construction_test.go's
// exemption maps carry.
func danglingExemptions(f fixtureFacts, mapName string, exempt map[string]string) []string {
	var out []string
	for _, name := range sortedKeys(exempt) {
		if _, ok := f.knobs[name]; !ok {
			out = append(out, fmt.Sprintf("%s names %q, which is no longer a receiverBreak knob — remove the entry rather than "+
				"leaving an exemption that exempts nothing", mapName, name))
		}
	}
	return out
}

// TestEveryReceiverBreakKnobIsHonouredAndSpent is rule 2.
func TestEveryReceiverBreakKnobIsHonouredAndSpent(t *testing.T) {
	f := parseFixtureFacts(t, ".")
	if len(f.knobs) == 0 {
		t.Fatal("receiverBreak has no knobs — either the fixture is gone or the walk is not reading the package")
	}
	if len(f.knobEnums) == 0 {
		t.Fatal("no receiverBreak field is declared with an enum type — the constants carrying most of the committed mutations would then " +
			"be invisible to this rule while its field half went on passing")
	}
	if !f.spent["mustReport"] {
		t.Fatal("mustReport is not in the spent set — parseFixtureFacts read no driving test file, so every knob would be reported as " +
			"unspent for a reason unrelated to any of them")
	}

	for _, msg := range knobViolations(f, knobExemptions{unspent: unspentKnobs, byAbsence: knobsHonouredByAbsence}) {
		t.Error(msg)
	}
}
