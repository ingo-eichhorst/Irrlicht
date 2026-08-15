// fixture_drift_corpus_test.go is fixture_drift_test.go's committed mutation
// evidence, in the shape AGENTS.md asks for: one deliberately-wrong case per
// thing the two rules claim to catch, kept in the tree rather than described in
// a merged PR body that nothing re-runs.
//
// Both rules pass by construction against a package that has not drifted, which
// is exactly the condition the mutation rule exists for — so their whole value
// is that they CAN fail, and this file is where that is demonstrated.
//
// The must-NOT-report rows carry as much of the value as the must-report ones,
// per #1450. A comparator that reported unconditionally would satisfy every
// mutation below and read as thorough coverage, so every table here has a
// vacuity row; and three rows pin DECLARED LIMITS rather than successes — the
// reporter seam's two spellings, a self-test's own test-only scaffolding, and
// (the one that matters) two bodies holding the same literals and the same
// shared references in a different arrangement, which rule 1 cannot tell apart.
// Pinning a limit is how it gets learned from a test instead of from an
// incident.
package contracttesting

import (
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

// --- building facts from synthetic sources ---

// factsFromSources runs the real absorb over sources held as strings, so the
// corpus grades the walk the package actually uses rather than a second reader
// written to agree with it.
//
// Nothing is type-checked, only parsed: a corpus row references receiverBreak
// and fakeReceiver without declaring them precisely because that is what a real
// self-test does — those live in _test.go files and must drop out of the shared
// vocabulary on their own.
func factsFromSources(t *testing.T, sources map[string]string) fixtureFacts {
	t.Helper()
	f := newFixtureFacts()
	fset := token.NewFileSet()
	// Iterated in map order on purpose: absorb's result does not depend on which
	// file it reaches first, which is exactly why resolveEnumKnobs runs after the
	// whole walk rather than during it. A corpus that had to fix an order would
	// be hiding that property instead of exercising it.
	for name, src := range sources {
		file, err := parser.ParseFile(fset, name, src, 0)
		if err != nil {
			t.Fatalf("parse corpus source %s: %v\n%s", name, err, src)
		}
		f.absorb(fset, name, strings.HasSuffix(name, "_test.go"), file)
	}
	f.resolveEnumKnobs()
	return *f
}

// --- rule 1's corpus: extraction and comparison, end to end ---

// corpusSharedDecls is the corpus's NON-test file, so everything it declares is
// "shared" — the vocabulary both sides of an obligation must agree on. It mirrors
// the real package's shape: an arm, a fixture helper, the `what` constants an
// arm's failure prints, and the entry-point half of the reporter seam.
const corpusSharedDecls = `package p

type armT struct{}

func realT(t *testing.T) armT { return armT{} }

func assertRefused(t reporter, path, what string) {}

func mkSubdir(t *testing.T, root, name string) string { return "" }

const (
	whatOutOfTree = "a well-formed transcript outside every declared root"
	whatTraversal = "a path rooted in the declared tree that climbs out of it"

	// inTree collides on purpose with a name the obligation bodies below bind
	// as a LOCAL. Without it the row pinning that locals are subtracted would
	// pass while proving nothing, because an identifier no non-test file
	// declares drops out of both statements anyway.
	inTree = "a package-level declaration the bodies below shadow"
)
`

// statementRow is one corpus case: the entry point's t.Run bodies and the
// self-test's armBuilder rows, verbatim, plus what the comparator must say.
type statementRow struct {
	name string

	// entry is spliced into an AssertCorpus function body.
	entry string

	// self is spliced into the []armBuilder literal corpusArmBuilders returns.
	self string

	// want is a fragment of the message the comparator must report. Empty means
	// it must stay SILENT — the vacuity and declared-limit rows.
	want string

	// why records what the row is evidence for, and appears in its failure.
	why string
}

// corpusSources splices one row into the two files the walk reads.
func corpusSources(row statementRow) map[string]string {
	return map[string]string{
		"shared.go": corpusSharedDecls,
		"family_test.go": "package p\n\nfunc AssertCorpus(t *testing.T) {\n" + row.entry +
			"}\n\nfunc corpusArmBuilders() []armBuilder {\n\treturn []armBuilder{\n" + row.self +
			"\t}\n}\n\nvar corpusFamily = receiverFamily{entryPoint: \"AssertCorpus\", builders: corpusArmBuilders}\n",
	}
}

// The obligation both sides state correctly, in the two spellings the real
// package uses: the entry point binds the root and posts through realT, the
// builder inlines the root, hoists the receiver and posts through armT.
const (
	corpusEntryInTree = `	t.Run("in_tree_path_accepted", func(t *testing.T) {
		root := r.Root(t)
		inTree := r.WriteTranscript(t, mkSubdir(t, root, "in-tree"))
		assertRefused(realT(t), inTree, whatTraversal)
	})
`
	corpusSelfInTree = `		{"in_tree_path_accepted", func(t *testing.T, brk receiverBreak) func(armT) {
			r := fakeReceiver(brk)
			inTree := r.WriteTranscript(t, mkSubdir(t, r.Root(t), "in-tree"))
			return func(at armT) { assertRefused(at, inTree, whatTraversal) }
		}},
`
	corpusEntryTraversal = `	t.Run("parent_traversal_rejected", func(t *testing.T) {
		root := r.Root(t)
		traversal := root + strings.Repeat("/..", 32)
		assertRefused(realT(t), traversal, whatOutOfTree)
	})
`
	corpusSelfTraversal = `		{"parent_traversal_rejected", func(t *testing.T, brk receiverBreak) func(armT) {
			r := fakeReceiver(brk)
			traversal := r.Root(t) + strings.Repeat("/..", 32)
			return func(at armT) { assertRefused(at, traversal, whatOutOfTree) }
		}},
`
)

func statementRows() []statementRow {
	return []statementRow{
		{
			name:  "an_undrifted_pair_is_passed",
			entry: corpusEntryInTree + corpusEntryTraversal,
			self:  corpusSelfInTree + corpusSelfTraversal,
			why: "the vacuity guard. A comparator that reported unconditionally would satisfy every row below " +
				"and read as excellent coverage. It also pins two DECLARED LIMITS at once, because the two sides " +
				"here already differ: the entry point binds `root :=` where the builder inlines r.Root(t), and " +
				"the builder names receiverBreak and fakeReceiver, which no entry point may reference",
		},
		{
			name: "a_string_literal_changed_on_the_entry_side",
			entry: `	t.Run("in_tree_path_accepted", func(t *testing.T) {
		root := r.Root(t)
		inTree := r.WriteTranscript(t, mkSubdir(t, root, "somewhere-else"))
		assertRefused(realT(t), inTree, whatTraversal)
	})
`,
			self: corpusSelfInTree,
			want: "post different literals",
			why: "#1520's headline failure, in its most literal form: the contract posts a path the self-tests " +
				"stopped posting, and both stay green while the committed evidence attests to an input production " +
				"no longer produces",
		},
		{
			name: "an_int_literal_changed_on_the_entry_side",
			entry: `	t.Run("parent_traversal_rejected", func(t *testing.T) {
		root := r.Root(t)
		traversal := root + strings.Repeat("/..", 2)
		assertRefused(realT(t), traversal, whatOutOfTree)
	})
`,
			self: corpusSelfTraversal,
			want: "post different literals",
			why: "a traversal shortened from 32 segments to 2 no longer bottoms out at \"/\", so the obligation " +
				"grades a different input than the one the self-test's mutation was measured against. An " +
				"identifier-only comparison would miss it entirely",
		},
		{
			name: "a_shared_constant_swapped_on_the_entry_side",
			entry: `	t.Run("parent_traversal_rejected", func(t *testing.T) {
		root := r.Root(t)
		traversal := root + strings.Repeat("/..", 32)
		assertRefused(realT(t), traversal, whatTraversal)
	})
`,
			self: corpusSelfTraversal,
			want: "reference different shared declarations",
			why: "the four refusal obligations share ONE arm and differ only in the `what` they print, which is " +
				"the fragment a negative self-test matches on. An entry point that prints a neighbour's `what` " +
				"leaves every self-test matching a message the contract no longer emits",
		},
		{
			name: "a_shared_helper_dropped_on_the_entry_side",
			entry: `	t.Run("in_tree_path_accepted", func(t *testing.T) {
		root := r.Root(t)
		inTree := r.WriteTranscript(t, root+"/"+"in-tree")
		assertRefused(realT(t), inTree, whatTraversal)
	})
`,
			self: corpusSelfInTree,
			want: "reference different shared declarations",
			why: "the entry point stops building through the shared fixture helper and hand-rolls the same thing. " +
				"The two constructions may agree today and diverge on the next edit to the helper, which is the " +
				"duplication this rule exists to keep honest",
		},
		{
			name:  "an_obligation_only_the_entry_point_runs",
			entry: corpusEntryInTree + corpusEntryTraversal,
			self:  corpusSelfInTree,
			want:  "states different obligations",
			why: "the drift no per-input comparison can see: a seventh obligation added to a contract with no " +
				"self-test driving it. The seam walk's rule 2 stays green because reusing an existing arm " +
				"introduces no new arm for it to notice",
		},
		{
			name: "an_obligation_renamed_on_one_side",
			entry: `	t.Run("in_tree_path_still_accepted", func(t *testing.T) {
		root := r.Root(t)
		inTree := r.WriteTranscript(t, mkSubdir(t, root, "in-tree"))
		assertRefused(realT(t), inTree, whatTraversal)
	})
`,
			self: corpusSelfInTree,
			want: "states different obligations",
			why: "receiverFamily.drive looks an obligation up BY NAME and fatals when it misses, so a rename on " +
				"the entry-point side would be found — but only for a name some self-test still asks for. A " +
				"rename that nothing drives is silent without this",
		},
		{
			name:  "two_obligations_swapped_in_order",
			entry: corpusEntryTraversal + corpusEntryInTree,
			self:  corpusSelfInTree + corpusSelfTraversal,
			want:  "states different obligations",
			why: "order is part of the statement: the confinement family's obligation 1 runs first precisely " +
				"because everything below it is a negative assertion, and a receiver that has stopped working " +
				"satisfies negative assertions perfectly",
		},
		{
			name:  "a_keyed_builder_row_is_still_read",
			entry: corpusEntryInTree,
			self: `		{name: "in_tree_path_accepted", build: func(t *testing.T, brk receiverBreak) func(armT) {
			r := fakeReceiver(brk)
			inTree := r.WriteTranscript(t, mkSubdir(t, r.Root(t), "in-tree"))
			return func(at armT) { assertRefused(at, inTree, whatTraversal) }
		}},
`,
			why: "a DECLARED LIMIT avoided rather than accepted: builderRow reads the keyed spelling as well as " +
				"the positional one, because a rule that quietly stopped matching when a table was rewritten in " +
				"the other style would report \"no obligations\" for a reason unrelated to any drift",
		},
		{
			name:  "the_two_sides_name_their_locals_differently",
			entry: corpusEntryInTree,
			self: `		{"in_tree_path_accepted", func(t *testing.T, brk receiverBreak) func(armT) {
			r := fakeReceiver(brk)
			spelled := r.WriteTranscript(t, mkSubdir(t, r.Root(t), "in-tree"))
			return func(at armT) { assertRefused(at, spelled, whatTraversal) }
		}},
`,
			why: "a false report this rule must not make. The real obligation 6 calls the same path inTree on one " +
				"side and spelled on the other, and the corpus's shared file declares an inTree of its own so the " +
				"collision is real rather than assumed — a name the body BINDS is a local, not a shared reference, " +
				"and a rule that reported on it would be reporting a variable name as a fixture drift",
		},
		{
			name: "the_same_literals_in_a_different_arrangement",
			entry: `	t.Run("in_tree_path_accepted", func(t *testing.T) {
		first := mkSubdir(t, r.Root(t), "linked")
		second := mkSubdir(t, r.Root(t), "dangling")
		assertRefused(realT(t), first+second, whatTraversal)
	})
`,
			self: `		{"in_tree_path_accepted", func(t *testing.T, brk receiverBreak) func(armT) {
			r := fakeReceiver(brk)
			first := mkSubdir(t, r.Root(t), "dangling")
			second := mkSubdir(t, r.Root(t), "linked")
			return func(at armT) { assertRefused(at, first+second, whatTraversal) }
		}},
`,
			why: "THE declared limit, pinned rather than left to be discovered. Rule 1 compares a multiset of " +
				"literals and a set of shared references, not expression structure, so two bodies that use the " +
				"same pieces differently agree as far as it is concerned. Structural comparison was tried and " +
				"abandoned: the two sides legitimately differ in arrangement (see the vacuity row), so an " +
				"AST-equality rule would have been red on arrival",
		},
		{
			name: "an_entry_point_that_loops_over_its_obligations",
			entry: `	for _, name := range []string{"in_tree_path_accepted"} {
		t.Run(name, func(t *testing.T) {
			root := r.Root(t)
			inTree := r.WriteTranscript(t, mkSubdir(t, root, "in-tree"))
			assertRefused(realT(t), inTree, whatTraversal)
		})
	}
`,
			self: corpusSelfInTree,
			want: "states different obligations",
			why: "extraction reads TOP-LEVEL t.Run calls only, so a looped entry point yields nothing. It is " +
				"reported here as an empty obligation list; in the real rule the extraction guard fatals first, " +
				"which is the loud failure a walk that cannot read its input owes (AGENTS.md)",
		},
	}
}

// TestStatementComparisonCatchesEveryKnownDrift is rule 1's committed mutation
// evidence: one deliberately-drifted pair per shape it claims to catch, plus the
// rows it must leave alone.
func TestStatementComparisonCatchesEveryKnownDrift(t *testing.T) {
	rows := statementRows()
	if len(rows) == 0 {
		t.Fatal("the corpus is empty, which grades nothing")
	}
	var reported, silent int
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			f := factsFromSources(t, corpusSources(row))
			if len(f.families) != 1 {
				t.Fatalf("the corpus source declares %d receiverFamily vars, want exactly 1 — the row would then grade nothing", len(f.families))
			}
			fam := f.families[0]
			entry := obligationsOfEntryPoint(f.funcs[fam.entryPoint], f.shared)
			self := obligationsOfBuilderTable(f.funcs[fam.builders], f.shared)
			got := strings.Join(compareStatements(fam.varName, entry, self), "\n")

			if row.want == "" {
				if got != "" {
					t.Fatalf("the comparator reported against a pair it must pass:\n%s\n\nthis row is evidence for: %s", got, row.why)
				}
				return
			}
			if got == "" {
				t.Fatalf("the comparator said nothing about a pair that has drifted — this row is evidence for: %s", row.why)
			}
			if !strings.Contains(got, row.want) {
				t.Fatalf("the comparator reported, but NOT on the drift under test: wanted a message containing %q, got:\n%s\n\nthis row is evidence for: %s",
					row.want, got, row.why)
			}
		})
		if row.want == "" {
			silent++
		} else {
			reported++
		}
	}
	// Both populations have to be non-empty. A corpus of only-reporting rows
	// certifies a comparator that fires on everything; one of only-silent rows
	// certifies one that fires on nothing.
	if reported == 0 || silent == 0 {
		t.Fatalf("the corpus has %d must-report and %d must-stay-silent rows — it needs both, or it grades a comparator that is constant", reported, silent)
	}
}

// --- rule 2's corpus: knob extraction ---

// TestKnobExtractionReadsEveryKnobAndNothingElse pins what a receiverBreak knob
// IS, which is the half of rule 2 no violation table can reach: the rule grades
// the knobs it was handed, and a walk that stopped collecting the enum constants
// would hand it a short list and pass.
//
// The must-NOT-collect rows carry the value here. A field declared with a
// builtin type contributes no enum — treating `bool` as one would make every
// `true` in the package a knob — and a const block of a type no receiverBreak
// field mentions is not a knob set at all.
func TestKnobExtractionReadsEveryKnobAndNothingElse(t *testing.T) {
	const source = `package p

type confineOverride int

const (
	confineFaithful confineOverride = iota
	confineRawPrefix
	confineCleanedPrefix
)

type unrelatedEnum int

const (
	unrelatedZero unrelatedEnum = iota
	unrelatedOne
)

type receiverBreak struct {
	forwardCallersSpelling bool
	confine                confineOverride
	receipt                receiptPlacement
}

type receiptPlacement int

const (
	receiptAfterChannelConsent receiptPlacement = iota
	receiptNever
)
`
	f := factsFromSources(t, map[string]string{"fixture_test.go": source})

	// The enum whose constants follow receiverBreak in the source is the point
	// of resolving after the whole walk rather than during it: a field and its
	// enum are declared in no guaranteed order.
	want := map[string]bool{
		"forwardCallersSpelling":     false,
		"confine":                    false,
		"receipt":                    false,
		"confineFaithful":            true,
		"confineRawPrefix":           false,
		"confineCleanedPrefix":       false,
		"receiptAfterChannelConsent": true,
		"receiptNever":               false,
	}
	for name, correct := range want {
		k, ok := f.knobs[name]
		if !ok {
			t.Errorf("%s was not collected as a knob — rule 2 would grade a short list and pass", name)
			continue
		}
		if k.correct != correct {
			t.Errorf("%s: collected with correct=%v, want %v — the FIRST constant of an enum is its zero value, "+
				"i.e. the correct setting, and is the one knob no negative self-test may be required to name", name, k.correct, correct)
		}
	}
	for _, name := range []string{"unrelatedZero", "unrelatedOne"} {
		if _, ok := f.knobs[name]; ok {
			t.Errorf("%s was collected as a knob, but no receiverBreak field is declared with its type — a rule that "+
				"collects every typed constant in the package would demand a self-test for constants that are not mutations at all", name)
		}
	}
	if f.knobEnums["bool"] {
		t.Error("`bool` was collected as a knob enum — a receiverBreak field may be declared with a builtin type, " +
			"and treating one as an enum would make every predeclared constant a knob")
	}
	if len(f.knobs) != len(want) {
		t.Errorf("collected %d knobs from a source declaring %d, so something outside the table above was picked up: %v",
			len(f.knobs), len(want), sortedKeys(f.knobs))
	}
}

// --- rule 2's corpus: the violation table ---

// knobRow is one synthetic knob-facts case.
type knobRow struct {
	name   string
	facts  fixtureFacts
	exempt knobExemptions
	want   string
	why    string
}

// knobFactsFor builds a fact set with one knob in a named state, so each row
// below varies exactly one thing.
func knobFactsFor(k knob, honoured, spent bool) fixtureFacts {
	f := *newFixtureFacts()
	f.knobs["probe"] = k
	if honoured {
		f.honoured["probe"] = true
	}
	if spent {
		f.spent["probe"] = true
	}
	return f
}

func knobRows() []knobRow {
	field := knob{where: "receiver_fixture_test.go:1", what: "field"}
	setting := knob{where: "receiver_fixture_test.go:2", what: "setting of confineOverride"}
	correct := knob{where: "receiver_fixture_test.go:3", what: "setting of confineOverride", correct: true}

	return []knobRow{
		{
			name:  "a_knob_that_is_honoured_and_spent_is_passed",
			facts: knobFactsFor(field, true, true),
			why:   "the vacuity guard: a rule that reported unconditionally would satisfy every row below",
		},
		{
			name:  "a_knob_no_self_test_spends_is_reported",
			facts: knobFactsFor(field, true, false),
			want:  "spent by no negative self-test",
			why:   "#1520's smaller sibling: a committed mutation with no failure attached is evidence for nothing",
		},
		{
			name:  "a_knob_no_fixture_honours_is_reported",
			facts: knobFactsFor(field, false, true),
			want:  "read by no fixture",
			why: "the other end of the same death. A knob the fixture ignores builds a CORRECT receiver, so the " +
				"case setting it goes through every motion and grades nothing",
		},
		{
			name:  "an_enum_setting_no_self_test_spends_is_reported",
			facts: knobFactsFor(setting, true, false),
			want:  "spent by no negative self-test",
			why: "the half a field-level rule misses: `confine` and `receipt` are single fields carrying three and " +
				"four distinct mutations, so a field-level rule calls `confine` spent while one of its settings rots",
		},
		{
			name:  "the_correct_setting_need_not_be_spent",
			facts: knobFactsFor(correct, true, false),
			why: "the zero value is what receiverBreak{} MEANS. Every family's vacuity guard spends it and no " +
				"negative self-test names it, so requiring it would be requiring the correct setting to be a mutation",
		},
		{
			name:  "the_correct_setting_must_still_be_honoured",
			facts: knobFactsFor(correct, false, false),
			want:  "read by no fixture",
			why: "what stops the exemption above from being a silent skip: the zero value is exempt from being " +
				"SPENT, not from naming a behaviour the receiver implements",
		},
		{
			name:   "an_exempted_knob_is_passed",
			facts:  knobFactsFor(field, true, false),
			exempt: knobExemptions{unspent: map[string]string{"probe": "a reason"}},
			why:    "an exemption with a reason is the reviewable escape hatch, the shape deferredToTheSeam uses",
		},
		{
			name:   "an_exemption_with_a_blank_reason_is_reported",
			facts:  knobFactsFor(field, true, false),
			exempt: knobExemptions{unspent: map[string]string{"probe": ""}},
			want:   "with no reason",
			why:    "an exemption whose justification is blank is one nobody can review",
		},
		{
			name:   "a_knob_honoured_by_absence_is_passed",
			facts:  knobFactsFor(field, false, true),
			exempt: knobExemptions{byAbsence: map[string]string{"probe": "a reason"}},
			why: "receiptNever's shape: the fixture implements it by matching none of its placement guards, so no " +
				"branch names it and a walk looking for the name cannot see it",
		},
		{
			name:   "an_exemption_that_names_no_knob_is_reported",
			facts:  knobFactsFor(field, true, true),
			exempt: knobExemptions{unspent: map[string]string{"gone": "a reason"}},
			want:   "which is no longer a receiverBreak knob",
			why:    "an exemption that exempts nothing is a silent no-op, and these maps exist to be reviewed",
		},
	}
}

// TestKnobViolationsCatchesEveryKnownShape is rule 2's committed mutation
// evidence.
func TestKnobViolationsCatchesEveryKnownShape(t *testing.T) {
	rows := knobRows()
	var reported, silent int
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			got := strings.Join(knobViolations(row.facts, row.exempt), "\n")
			if row.want == "" {
				if got != "" {
					t.Fatalf("the rule reported against a knob set it must pass:\n%s\n\nthis row is evidence for: %s", got, row.why)
				}
				return
			}
			if got == "" {
				t.Fatalf("the rule said nothing about a dead knob — this row is evidence for: %s", row.why)
			}
			if !strings.Contains(got, row.want) {
				t.Fatalf("the rule reported, but NOT on the shape under test: wanted a message containing %q, got:\n%s\n\nthis row is evidence for: %s",
					row.want, got, row.why)
			}
		})
		if row.want == "" {
			silent++
		} else {
			reported++
		}
	}
	if reported == 0 || silent == 0 {
		t.Fatalf("the corpus has %d must-report and %d must-stay-silent rows — it needs both, or it grades a rule that is constant", reported, silent)
	}
}
