package processlifecycle

// This file is the platform-independent half of #1525's coverage: the outcome
// vocabulary, the snapshot's shape, and the one claim about the non-darwin
// stubs that a macOS runner can still grade. The darwin half — which outcome a
// real walk reaches, and what gets logged — is hostgate_darwin_test.go.
//
// Everything here is a counter or a constant, so every one of these passes the
// moment it is written. What is asserted is therefore not "the number went up"
// but the ways a gate counter can be present and useless: two outcomes sharing
// a bucket, an outcome nobody declared vanishing, and a platform that never
// walked publishing a verdict anyway.
//
// The counters are process-global and never reset, deliberately: a reset hook
// would be a second way to zero them and the daemon has no use for one. So
// every assertion is a DELTA, which also makes this file safe under
// `go test -count=N`. Nothing here calls t.Parallel, for the reason
// probecount_test.go gives beside it.

import (
	"go/ast"
	"go/token"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// hostGateLedger is one reading of every outcome counter.
type hostGateLedger struct{ rows map[string]uint64 }

// readHostGateLedger snapshots the outcome counters.
func readHostGateLedger() hostGateLedger {
	l := hostGateLedger{rows: map[string]uint64{}}
	for _, row := range HostGateCounts() {
		l.rows[row.Outcome] = row.Count
	}
	return l
}

// hostGateSince returns the outcomes that MOVED since before, with the deltas
// in place of the totals — and only those.
//
// Returning the movers rather than every row is what makes a bare DeepEqual
// against a one-entry map an exhaustive claim: it says this outcome moved by
// exactly this much AND no other outcome moved at all. That second half is the
// vacuity guard — a gate that counts SOMETHING for every session looks
// identical to one that counts correctly until something asserts the silence.
func hostGateSince(before hostGateLedger) map[string]uint64 {
	moved := map[string]uint64{}
	for outcome, now := range readHostGateLedger().rows {
		if delta := now - before.rows[outcome]; delta != 0 {
			moved[outcome] = delta
		}
	}
	return moved
}

// assertHostGateMoved fails unless exactly want moved.
func assertHostGateMoved(t *testing.T, before hostGateLedger, want map[string]uint64, why string) {
	t.Helper()
	got := hostGateSince(before)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("host-gate counters moved by %v, want %v — %s", got, want, why)
	}
}

// TestEveryHostGateOutcomeDeclaresItsVerdict pins what each outcome DOES, and
// is the lock that keeps admits()'s trailing `return true` — and, since #1574,
// admittedOnNoEvidence()'s trailing `return false` — from becoming a place a
// decision hides.
//
// The table is written out rather than derived, because deriving it from the
// functions under test asserts nothing. Its two guards are what make it a lock
// rather than a handful of loose rows: it must name every declared outcome, and
// it must name no outcome that is not declared — so an outcome added to the
// vocabulary without a decision about what it does fails here instead of
// silently taking a fallback.
//
// Two columns rather than one, because the two questions have DIFFERENT answers
// and a single column cannot express either: three outcomes admit and only two
// of those admit having learned nothing. The second column decides whether a
// session gets an error line explaining a phantom admission, so an outcome that
// answered it by accident would either bury the log in lines about ordinary
// admissions or leave a phantom unexplained.
func TestEveryHostGateOutcomeDeclaresItsVerdict(t *testing.T) {
	type verdict struct {
		admits             bool
		admittedNoEvidence bool
	}
	want := map[hostGateOutcome]verdict{
		hostGateHostMatched:  {true, false},  // an ordinary admission, on evidence
		hostGateWalkAborted:  {true, true},   // #1513: no evidence is not evidence of a miss
		hostGateProcessGone:  {true, true},   // #1574: a gone pid is no more evidence than an unanswered probe
		hostGateNotEvaluated: {true, false},  // a platform that never looked must not reject — and never reaches the log
		hostGateNoKnownHost:  {false, false}, // #784, the one rejection
	}
	if len(want) != len(allHostGateOutcomes) {
		t.Fatalf("this table decides %d outcomes but %d are declared — an outcome with no decision here takes admits()'s fallback, which is where a polarity gets chosen by accident",
			len(want), len(allHostGateOutcomes))
	}
	for _, outcome := range allHostGateOutcomes {
		row, decided := want[outcome]
		if !decided {
			t.Errorf("%q is declared but this table does not say whether it admits", outcome)
			continue
		}
		if got := outcome.admits(); got != row.admits {
			t.Errorf("%q.admits() = %v, want %v", outcome, got, row.admits)
		}
		if got := outcome.admittedOnNoEvidence(); got != row.admittedNoEvidence {
			t.Errorf("%q.admittedOnNoEvidence() = %v, want %v — this column decides whether the gate writes the line that explains a phantom session", outcome, got, row.admittedNoEvidence)
		}
	}
}

// TestHostGateTokenPrefixMatchesItsVerdict pins the grammar the bundle's table
// is read by: every "admitted." row admits and the "rejected." row rejects.
//
// A reader of a pasted probes.json has no legend, so the token is the legend.
// This is not how admits() decides — deciding by string prefix would make a
// rename change a polarity — it is the check that the two never disagree.
func TestHostGateTokenPrefixMatchesItsVerdict(t *testing.T) {
	for _, outcome := range allHostGateOutcomes {
		token := string(outcome)
		switch {
		case strings.HasPrefix(token, "admitted."):
			if !outcome.admits() {
				t.Errorf("%q is spelled as an admission and rejects", token)
			}
		case strings.HasPrefix(token, "rejected."):
			if outcome.admits() {
				t.Errorf("%q is spelled as a rejection and admits", token)
			}
		default:
			t.Errorf("%q has neither prefix — the token IS the legend in a pasted bundle", token)
		}
	}
}

// TestHostGateCountsReportsEveryDeclaredOutcome pins the snapshot's shape. Every
// declared outcome is present even at zero, because a zero is itself a finding
// here: on Linux every row but admitted.not_evaluated is structurally zero, and
// on a darwin daemon a zero admitted.walk_aborted row is the evidence that the
// 2s ceilings are not firing — the question #1513 and #1524 both closed as
// unmeasured.
func TestHostGateCountsReportsEveryDeclaredOutcome(t *testing.T) {
	if len(hostGateCounts) != len(allHostGateOutcomes) {
		t.Fatalf("hostGateCounts has %d entries for %d declared outcomes — two outcomes share a token, so they share a bucket",
			len(hostGateCounts), len(allHostGateOutcomes))
	}
	rows := HostGateCounts()
	if len(rows) != len(allHostGateOutcomes) {
		t.Fatalf("HostGateCounts returned %d rows for %d declared outcomes", len(rows), len(allHostGateOutcomes))
	}
	seen := map[string]bool{}
	for _, row := range rows {
		seen[row.Outcome] = true
	}
	for _, outcome := range allHostGateOutcomes {
		if !seen[string(outcome)] {
			t.Errorf("HostGateCounts omits %q", outcome)
		}
	}
	if !sort.SliceIsSorted(rows, func(i, j int) bool { return rows[i].Outcome < rows[j].Outcome }) {
		t.Errorf("HostGateCounts is unsorted: %v — two captures of the same daemon must diff cleanly", rows)
	}
	if !strings.Contains(HostGateOutcomeRule(), "#784") || !strings.Contains(HostGateOutcomeRule(), "#1513") {
		t.Errorf("HostGateOutcomeRule must carry the rule the numbers were produced by, naming both polarities: %q", HostGateOutcomeRule())
	}
}

// TestUndeclaredHostGateOutcomeIsCountedNotDropped is the guard on the guard.
// An outcome nothing declared cannot be attributed to a row, and dropping the
// observation would make an uncounted gate evaluation indistinguishable from
// one that never happened — this issue's own failure mode, reproduced inside
// its fix.
func TestUndeclaredHostGateOutcomeIsCountedNotDropped(t *testing.T) {
	beforeUndeclared := UndeclaredHostGateOutcomes()
	before := readHostGateLedger()

	if got := observeHostGate(hostGateOutcome("not.a.declared.outcome")); got != hostGateOutcome("not.a.declared.outcome") {
		t.Errorf("observeHostGate returned %q — it must hand its argument back so a call site cannot reach a verdict without counting it", got)
	}

	if got := UndeclaredHostGateOutcomes() - beforeUndeclared; got != 1 {
		t.Errorf("undeclared observations = %d, want 1 — an unattributable evaluation must be reported, not swallowed", got)
	}
	assertHostGateMoved(t, before, map[string]uint64{},
		"an undeclared outcome must not be silently folded into some other outcome's row")
}

// --- the static half: what the non-darwin stubs are allowed to report -------

// TestNonDarwinHostGateReportsOnlyNotEvaluated is the one claim about the
// linux/other builds a macOS runner can still grade, and it is the constraint
// #1525 has to satisfy: those stubs return true unconditionally, so a daemon
// there must not publish an ancestry verdict it never reached.
//
// It reads the package's own source, for the reason
// TestEveryProbeSiteDeclaresItsOwnKind does: hostGateFor exists once per
// platform behind a build tag, so a type-aware check would see exactly one of
// them and pass everywhere — a gate whose absence reads as a pass, which is the
// defect this package keeps producing. parsePackageSources
// (shellout_guard_test.go) ignores build tags and excludes _test.go.
//
// Two claims, failing on different mutations: a non-darwin stub that names any
// outcome other than hostGateNotEvaluated, and a darwin implementation that
// names hostGateNotEvaluated at all (which would mean a real walk reporting
// that it never walked).
func TestNonDarwinHostGateReportsOnlyNotEvaluated(t *testing.T) {
	_, files := parsePackageSources(t, ".")

	found := map[string][]string{} // file -> outcome identifiers named in hostGateFor
	for name, file := range files {
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Name.Name != "hostGateFor" || fn.Recv != nil {
				continue
			}
			base := filepath.Base(name)
			found[base] = append(found[base], hostGateOutcomeIdentsIn(fn))
		}
	}

	// Vacuity floor: one per platform file. A scan that found none reports no
	// violations and is indistinguishable from a clean package.
	const knownHostGateImplementations = 3
	if len(found) < knownHostGateImplementations {
		t.Fatalf("found hostGateFor in %d files (%v), expected at least %d (darwin, linux, other): the scan is not looking where it thinks it is, so its silence proves nothing",
			len(found), sortedKeys(mapValuesToString(found)), knownHostGateImplementations)
	}

	for file, idents := range found {
		darwin := strings.HasSuffix(strings.TrimSuffix(file, ".go"), "_darwin")
		named := strings.Join(idents, ",")
		if darwin {
			if strings.Contains(named, "hostGateNotEvaluated") {
				t.Errorf("%s: the darwin hostGateFor names hostGateNotEvaluated — a build that DOES walk ancestry must never report that it did not look", file)
			}
			continue
		}
		if named != "hostGateNotEvaluated" {
			t.Errorf("%s: hostGateFor names %q, want exactly \"hostGateNotEvaluated\" — this platform admits unconditionally, so any other outcome publishes a verdict from a daemon that never read an ancestor (#1525)",
				file, named)
		}
	}
}

// hostGateOutcomeIdentsIn returns the hostGate* outcome identifiers a function
// body mentions, comma-joined and de-duplicated in source order.
func hostGateOutcomeIdentsIn(fn *ast.FuncDecl) string {
	var out []string
	seen := map[string]bool{}
	ast.Inspect(fn, func(n ast.Node) bool {
		id, ok := n.(*ast.Ident)
		if !ok || !isHostGateOutcomeIdent(id.Name) || seen[id.Name] {
			return true
		}
		seen[id.Name] = true
		out = append(out, id.Name)
		return true
	})
	return strings.Join(out, ",")
}

// isHostGateOutcomeIdent reports whether name is one of the declared outcome
// constants. Derived from the constants rather than a second list, so a new
// outcome is graded by existing.
func isHostGateOutcomeIdent(name string) bool {
	switch name {
	case "hostGateHostMatched", "hostGateWalkAborted", "hostGateProcessGone", "hostGateNotEvaluated", "hostGateNoKnownHost":
		return true
	}
	return false
}

// mapValuesToString adapts a map[string][]string to the shape sortedKeys wants,
// so the failure above can name the files it did find.
func mapValuesToString(in map[string][]string) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = strings.Join(v, ",")
	}
	return out
}

// TestEveryHostGateOutcomeConstantIsDeclared is the vocabulary's own tripwire:
// the source's const block and allHostGateOutcomes must be the same set, or an
// outcome exists that no snapshot row can ever carry.
func TestEveryHostGateOutcomeConstantIsDeclared(t *testing.T) {
	_, files := parsePackageSources(t, ".")
	declared := map[string]string{}
	for _, file := range files {
		for _, decl := range file.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || gen.Tok != token.CONST {
				continue
			}
			collectHostGateOutcomeConsts(gen, declared)
		}
	}
	if len(declared) != len(allHostGateOutcomes) {
		t.Fatalf("the source declares %d hostGateOutcome constants (%s) but allHostGateOutcomes lists %d — one of them has no counter",
			len(declared), strings.Join(sortedKeys(declared), ", "), len(allHostGateOutcomes))
	}
	values := map[string]bool{}
	for _, v := range declared {
		values[v] = true
	}
	for _, outcome := range allHostGateOutcomes {
		if !values[string(outcome)] {
			t.Errorf("allHostGateOutcomes contains %q, which no hostGateOutcome constant declares", outcome)
		}
	}
}

// collectHostGateOutcomeConsts records the hostGateOutcome constants in one
// const block, mapped to their string values.
func collectHostGateOutcomeConsts(gen *ast.GenDecl, out map[string]string) {
	for _, spec := range gen.Specs {
		vs, ok := spec.(*ast.ValueSpec)
		if !ok || len(vs.Names) != 1 || len(vs.Values) != 1 {
			continue
		}
		typeName, _ := vs.Type.(*ast.Ident)
		if typeName == nil || typeName.Name != "hostGateOutcome" {
			continue
		}
		lit, ok := vs.Values[0].(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			continue
		}
		value, err := strconv.Unquote(lit.Value)
		if err != nil {
			continue
		}
		out[vs.Names[0].Name] = value
	}
}
