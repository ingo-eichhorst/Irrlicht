package processlifecycle

import (
	"context"
	"go/ast"
	"go/token"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// This file grades #1534's counters. Every one of them passes the moment it is
// written, so what is asserted here is not "the number went up" but the three
// ways a counter can be present and useless:
//
//   - it counts everything as an answer, so the number that matters is always
//     zero (TestProbeCounterSeparatesAnsweredFromUnanswered);
//   - two probes share a bucket, so a climbing count cannot say which one
//     stopped answering (TestEveryProbeSiteDeclaresItsOwnKind, and its runtime
//     twin one tool at a time);
//   - it counts something that answered as a non-answer, which is
//     indistinguishable from correct until the day it matters (the exactness of
//     every delta assertion below — see probesSince).
//
// The counters are process-global and never reset, which is deliberate: a reset
// hook would be a second way to zero them and the daemon has no use for one. So
// every assertion here is a DELTA across the call under test, which also makes
// the file safe under `go test -count=N`. Nothing here calls t.Parallel: Go
// defers parallel tests until the serial ones have finished, so a serial delta
// cannot overlap the package's real-ceiling shellout tests.

// probeLedger is one reading of every per-probe counter. The client-loop
// figures are deliberately not in it: they are not on the (kind, outcome) axis,
// and folding them in would let a probe assertion vouch for a client-loop one.
type probeLedger struct {
	rows map[string]ProbeCount
}

// readProbeLedger snapshots the per-probe counters.
func readProbeLedger() probeLedger {
	l := probeLedger{rows: map[string]ProbeCount{}}
	for _, row := range ProbeCounts() {
		l.rows[row.Probe] = row
	}
	return l
}

// probesSince returns the probe rows that MOVED since before, with the deltas
// in place of the totals — and only those. Returning the movers rather than
// every row is what makes a bare reflect.DeepEqual against a one-entry map an
// exhaustive claim: it says this probe moved by exactly this much AND no other
// probe moved at all. That second half is the vacuity guard #1364's first
// obligation exists for — a counter that counts everything looks identical to
// one that counts correctly until something asserts the silence.
func probesSince(before probeLedger) map[string]ProbeCount {
	moved := map[string]ProbeCount{}
	after := readProbeLedger()
	for probe, now := range after.rows {
		was := before.rows[probe]
		delta := ProbeCount{
			Probe:      probe,
			Answered:   now.Answered - was.Answered,
			Unanswered: now.Unanswered - was.Unanswered,
			MemoHits:   now.MemoHits - was.MemoHits,
		}
		if probeDeltaMoved(delta) {
			moved[probe] = delta
		}
	}
	return moved
}

// probeDeltaMoved reports whether any of a row's three counters changed. All
// three, not just the two outcomes: a memo hit that appeared where none was
// expected is exactly the #1544 confusion these deltas exist to catch, and a
// predicate that ignored it would let it pass as silence.
func probeDeltaMoved(delta ProbeCount) bool {
	return delta.Answered != 0 || delta.Unanswered != 0 || delta.MemoHits != 0
}

// assertProbesMoved fails unless exactly want moved. want is keyed by probe
// token; its ProbeCount values carry deltas.
func assertProbesMoved(t *testing.T, before probeLedger, want map[string]ProbeCount, why string) {
	t.Helper()
	got := probesSince(before)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("probe counters moved by %v, want %v — %s", got, want, why)
	}
}

// answeringCmd builds a child that runs to a normal exit with status code.
// /bin/sh is present on every platform this package builds for.
func answeringCmd(code string) shelloutCmd {
	return func(ctx context.Context) *exec.Cmd {
		return exec.CommandContext(ctx, "/bin/sh", "-c", "exit "+code)
	}
}

// missingBinaryCmd builds a child that can never start. A fork/exec failure is
// the cheapest deterministic non-answer: unlike a ceiling kill it costs no
// wall-clock time, and shellout.Answered's own corpus pins it on the same side
// of the line ("a probe that never ran, not a tool reporting nothing").
func missingBinaryCmd() shelloutCmd {
	return func(ctx context.Context) *exec.Cmd {
		return exec.CommandContext(ctx, "/nonexistent/probe-1534")
	}
}

// TestProbeCounterSeparatesAnsweredFromUnanswered is the central claim: the two
// outcomes are different columns, and a child that ran is never counted as one
// that did not.
//
// The exit-3 row is not a duplicate of the exit-0 one. It pins
// probeOutcomeRule's deliberate choice — ANSWERED means the child ran to a
// normal exit, ANY status, rather than the call site's own exit-code allowlist
// — so a future change that folded lsof's or pgrep's allowlist into runProbe
// would reclassify it and be caught here rather than silently shifting what
// every published number means.
func TestProbeCounterSeparatesAnsweredFromUnanswered(t *testing.T) {
	ctx := context.Background()

	before := readProbeLedger()
	if _, err := runProbe(ctx, probePSTTY, answeringCmd("0")); err != nil {
		t.Fatalf("a child that exits 0 must not error: %v", err)
	}
	assertProbesMoved(t, before, map[string]ProbeCount{
		"ps.tty": {Probe: "ps.tty", Answered: 1},
	}, "a child that ran to a normal exit is an ANSWER, and is counted by nobody as a non-answer")

	before = readProbeLedger()
	if _, err := runProbe(ctx, probePSTTY, answeringCmd("3")); err == nil {
		t.Fatal("a child that exits 3 must return an error, or this row is testing the exit-0 case again")
	}
	assertProbesMoved(t, before, map[string]ProbeCount{
		"ps.tty": {Probe: "ps.tty", Answered: 1},
	}, "a NON-ZERO normal exit is still an answer: probeOutcomeRule counts the child running, not the call site's allowlist")

	before = readProbeLedger()
	if _, err := runProbe(ctx, probePSTTY, missingBinaryCmd()); err == nil {
		t.Fatal("a missing binary must return an error, or this row proves nothing")
	}
	assertProbesMoved(t, before, map[string]ProbeCount{
		"ps.tty": {Probe: "ps.tty", Unanswered: 1},
	}, "a child that never started is the non-answer every issue in this family closed without a count of")
}

// TestProbeCounterKeepsEachKindInItsOwnBucket is the runtime half of the
// per-call-site key. `ps` answers two different questions here and `lsof`
// three; a bucket shared between them still climbs under load and still cannot
// say which question stopped being answered, which is the whole diagnostic
// value.
func TestProbeCounterKeepsEachKindInItsOwnBucket(t *testing.T) {
	ctx := context.Background()

	before := readProbeLedger()
	if _, err := runProbe(ctx, probePSProcInfo, answeringCmd("0")); err != nil {
		t.Fatalf("ps.proc_info probe: %v", err)
	}
	assertProbesMoved(t, before, map[string]ProbeCount{
		"ps.proc_info": {Probe: "ps.proc_info", Answered: 1},
	}, "the ancestry read and the tty read both run `ps` and must not share a bucket")

	before = readProbeLedger()
	if _, err := runProbe(ctx, probeLsofCWD, missingBinaryCmd()); err == nil {
		t.Fatal("a missing binary must return an error")
	}
	if _, err := runProbe(ctx, probeLsofWriter, missingBinaryCmd()); err == nil {
		t.Fatal("a missing binary must return an error")
	}
	assertProbesMoved(t, before, map[string]ProbeCount{
		"lsof.cwd":    {Probe: "lsof.cwd", Unanswered: 1},
		"lsof.writer": {Probe: "lsof.writer", Unanswered: 1},
	}, "three lsof questions, three buckets: a shared one would report six failures against one name and diagnose neither")
}

// TestProbeMemoHitIsCountedAndIsNotAnAnswer covers #1544's interaction. A memo
// hit starts no child, so runProbe never sees it — and counting it as an
// "answer" would inflate the denominator the non-answer rate is read against
// with calls no probe was ever asked.
func TestProbeMemoHitIsCountedAndIsNotAnAnswer(t *testing.T) {
	before := readProbeLedger()
	observeProbeMemoHit(probePlutilBundleID)
	assertProbesMoved(t, before, map[string]ProbeCount{
		"plutil.bundle_id": {Probe: "plutil.bundle_id", MemoHits: 1},
	}, "a memo hit is its own outcome: it is neither an answered child nor an unanswered one")
}

// TestAncestryReadsMemoHitIsCounted drives the real per-resolve memo (#1544's
// second half). Its first read goes to the injected probe — not runProbe, so
// nothing is counted for it here — and its second is served from seen, which is
// the call the counter at runProbe cannot see.
func TestAncestryReadsMemoHitIsCounted(t *testing.T) {
	reads := newAncestryReadsVia(func(ctx context.Context, pid int) (int, string, error) {
		return 1, "/bin/launchd", nil
	})
	ctx := context.Background()
	if _, _, err := reads.probe(ctx, 4242); err != nil {
		t.Fatalf("first read: %v", err)
	}

	before := readProbeLedger()
	if _, _, err := reads.probe(ctx, 4242); err != nil {
		t.Fatalf("second read: %v", err)
	}
	assertProbesMoved(t, before, map[string]ProbeCount{
		"ps.proc_info": {Probe: "ps.proc_info", MemoHits: 1},
	}, "a ps served from ancestryReads is a call the probe was asked and never ran for; #1544's hand-back is that a memo hides exactly this")
}

// TestUndeclaredProbeKindIsCountedNotDropped is the guard on the guard. A kind
// nothing declared cannot be attributed to a row, and dropping the observation
// would make an uncounted probe indistinguishable from one that never ran —
// this issue's own failure mode, reproduced inside its fix.
func TestUndeclaredProbeKindIsCountedNotDropped(t *testing.T) {
	beforeUndeclared := UndeclaredProbeKinds()
	before := readProbeLedger()

	observeProbe(probeKind("not.a.declared.kind"), nil)
	observeProbeMemoHit(probeKind("not.a.declared.kind"))

	if got := UndeclaredProbeKinds() - beforeUndeclared; got != 2 {
		t.Errorf("undeclared observations = %d, want 2 — an unattributable probe must be reported, not swallowed", got)
	}
	assertProbesMoved(t, before, map[string]ProbeCount{},
		"an undeclared kind must not be silently folded into some other probe's row")
}

// TestProbeCountsReportsEveryDeclaredKind pins the snapshot's shape. Every
// declared kind is present even at zero, because "this probe never ran" is
// itself a finding — on a machine where a tool is missing, the zero row IS the
// evidence — and an omitted row cannot be told apart from a probe this build
// does not have.
func TestProbeCountsReportsEveryDeclaredKind(t *testing.T) {
	if len(probeCounts) != len(allProbeKinds) {
		t.Fatalf("probeCounts has %d entries for %d declared kinds — two kinds share a token, so they share a bucket",
			len(probeCounts), len(allProbeKinds))
	}
	rows := ProbeCounts()
	if len(rows) != len(allProbeKinds) {
		t.Fatalf("ProbeCounts returned %d rows for %d declared kinds", len(rows), len(allProbeKinds))
	}
	seen := map[string]bool{}
	for _, row := range rows {
		seen[row.Probe] = true
	}
	for _, kind := range allProbeKinds {
		if !seen[string(kind)] {
			t.Errorf("ProbeCounts omits %q", kind)
		}
	}
	if !sort.SliceIsSorted(rows, func(i, j int) bool { return rows[i].Probe < rows[j].Probe }) {
		t.Errorf("ProbeCounts is unsorted: %v — two captures of the same daemon must diff cleanly", rows)
	}
}

// --- #1558's attached-client loop counters ---------------------------------
//
// The mechanism half, platform-neutral. The loop these describe is darwin-only,
// so what the runtime tests beside it (probecount_darwin_test.go) cannot cover
// on another platform is covered here by driving the observers directly — and
// the source scan below is deliberately here too, because parsePackageSources
// ignores build tags and a gate that only runs on darwin is a gate whose
// absence on linux reads as a pass.

// clientLoopLedger is one reading of every per-multiplexer counter. Separate
// from probeLedger for the reason stated on it: two different axes, and one
// ledger would let an assertion about one vouch for the other.
type clientLoopLedger struct {
	rows map[string]ClientLoopCount
}

// readClientLoopLedger snapshots the per-multiplexer counters.
func readClientLoopLedger() clientLoopLedger {
	l := clientLoopLedger{rows: map[string]ClientLoopCount{}}
	for _, row := range ClientLoopCounts() {
		l.rows[row.Multiplexer] = row
	}
	return l
}

// clientLoopsSince returns the rows that MOVED, with deltas in place of totals,
// and only those — so a bare reflect.DeepEqual against a one-entry map is an
// exhaustive claim: this multiplexer moved by exactly this much AND no other
// multiplexer moved at all. The second half is what #1558's whole keying rests
// on, since the defect it removes is one producer's loop landing in another's
// bucket, which is invisible to any assertion that only checks its own row.
func clientLoopsSince(before clientLoopLedger) map[string]ClientLoopCount {
	moved := map[string]ClientLoopCount{}
	for mux, now := range readClientLoopLedger().rows {
		was := before.rows[mux]
		delta := ClientLoopCount{
			Multiplexer:       mux,
			CandidatesProbed:  now.CandidatesProbed - was.CandidatesProbed,
			AbandonedOnBudget: now.AbandonedOnBudget - was.AbandonedOnBudget,
			StarvedByScan:     now.StarvedByScan - was.StarvedByScan,
		}
		if delta.CandidatesProbed != 0 || delta.AbandonedOnBudget != 0 || delta.StarvedByScan != 0 {
			moved[mux] = delta
		}
	}
	return moved
}

// assertClientLoopsMoved fails unless exactly want moved.
func assertClientLoopsMoved(t *testing.T, before clientLoopLedger, want map[string]ClientLoopCount, why string) {
	t.Helper()
	if got := clientLoopsSince(before); !reflect.DeepEqual(got, want) {
		t.Errorf("client loop counters moved by %v, want %v — %s", got, want, why)
	}
}

// TestClientLoopStarvationIsDecidedByTheCandidateCount is the mechanism claim
// under #1558's figure, driven without the darwin loop: an abandonment with no
// candidates probed is starvation, one with candidates probed is not, and both
// are abandonments.
//
// Both figures come from ONE call, so `starved <= abandoned` holds by
// construction. That is asserted anyway, because the property this file exists
// to protect is that a counter cannot quietly stop discriminating, and "the two
// are written together" is a claim about the code rather than about the numbers.
func TestClientLoopStarvationIsDecidedByTheCandidateCount(t *testing.T) {
	before := readClientLoopLedger()
	observeClientCandidatesAbandoned(clientLoopTmux, 0)
	assertClientLoopsMoved(t, before, map[string]ClientLoopCount{
		"tmux": {Multiplexer: "tmux", AbandonedOnBudget: 1, StarvedByScan: 1},
	}, "zero candidates probed means the scan ahead of the loop spent the whole budget (#1558)")

	before = readClientLoopLedger()
	observeClientCandidatesAbandoned(clientLoopTmux, 3)
	assertClientLoopsMoved(t, before, map[string]ClientLoopCount{
		"tmux": {Multiplexer: "tmux", AbandonedOnBudget: 1},
	}, "an abandonment after three candidates is the LOOP spending its own share; counting it as starved "+
		"would attribute to the scan time the candidates spent, and a per-stage split would not help it")

	before = readClientLoopLedger()
	observeClientCandidateProbed(clientLoopHerdr)
	assertClientLoopsMoved(t, before, map[string]ClientLoopCount{
		"herdr": {Multiplexer: "herdr", CandidatesProbed: 1},
	}, "a probed candidate is the denominator and is neither an abandonment nor a starvation")
}

// TestUndeclaredClientLoopKindIsCountedNotDropped is the guard on the guard,
// the sibling of TestUndeclaredProbeKindIsCountedNotDropped. A multiplexer
// nothing declared cannot be attributed to a row, and dropping the observation
// would make an uncounted loop indistinguishable from one that never ran.
func TestUndeclaredClientLoopKindIsCountedNotDropped(t *testing.T) {
	beforeUndeclared := UndeclaredClientLoopKinds()
	before := readClientLoopLedger()

	observeClientCandidateProbed(clientLoopKind("not.a.multiplexer"))
	observeClientCandidatesAbandoned(clientLoopKind("not.a.multiplexer"), 0)

	if got := UndeclaredClientLoopKinds() - beforeUndeclared; got != 2 {
		t.Errorf("undeclared observations = %d, want 2 — an unattributable loop must be reported, not swallowed", got)
	}
	assertClientLoopsMoved(t, before, map[string]ClientLoopCount{},
		"an undeclared multiplexer must not be silently folded into some other producer's row")
}

// TestClientLoopCountsReportsEveryDeclaredKind pins the snapshot's shape, for
// the reason TestProbeCountsReportsEveryDeclaredKind pins the probe one: on a
// machine that uses only one multiplexer the ZERO row IS the evidence, and an
// omitted row cannot be told apart from a producer this build does not have.
func TestClientLoopCountsReportsEveryDeclaredKind(t *testing.T) {
	if len(clientLoopCounts) != len(allClientLoopKinds) {
		t.Fatalf("clientLoopCounts has %d entries for %d declared kinds — two kinds share a token, so they share a bucket",
			len(clientLoopCounts), len(allClientLoopKinds))
	}
	rows := ClientLoopCounts()
	if len(rows) != len(allClientLoopKinds) {
		t.Fatalf("ClientLoopCounts returned %d rows for %d declared kinds", len(rows), len(allClientLoopKinds))
	}
	seen := map[string]bool{}
	for _, row := range rows {
		seen[row.Multiplexer] = true
	}
	for _, kind := range allClientLoopKinds {
		if !seen[string(kind)] {
			t.Errorf("ClientLoopCounts omits %q", kind)
		}
	}
	if !sort.SliceIsSorted(rows, func(i, j int) bool { return rows[i].Multiplexer < rows[j].Multiplexer }) {
		t.Errorf("ClientLoopCounts is unsorted: %v — two captures of the same daemon must diff cleanly", rows)
	}
	if !strings.Contains(ClientLoopStarvationRule(), "ZERO candidates probed") {
		t.Errorf("the exported starvation rule must carry the definition the numbers were produced by, "+
			"or the bundle prints a second copy that can describe behaviour it no longer has: %q", ClientLoopStarvationRule())
	}
}

// TestEveryClientLoopSiteDeclaresItsOwnKind is the tripwire that makes a THIRD
// multiplexer producer covered by existing rather than by remembering — which
// is exactly what #1501 did not have when it reused herdr's counter.
//
// A "site" is a call to resolveClientHostIdentityVia that names a declared
// constant. A call that FORWARDS a clientLoopKind parameter (the
// resolveClientHostIdentity wrapper) names no multiplexer and is not a site;
// it still counts toward the vacuity floor, so the scan cannot go quiet by
// stopping to recognise anything.
func TestEveryClientLoopSiteDeclaresItsOwnKind(t *testing.T) {
	fset, files := parsePackageSources(t, ".")
	declared := declaredClientLoopKindIdents(t, files)

	if len(declared) != len(allClientLoopKinds) {
		t.Fatalf("the source declares %d clientLoopKind constants but allClientLoopKinds lists %d — a kind that is not in allClientLoopKinds has no counter",
			len(declared), len(allClientLoopKinds))
	}
	values := declaredValues(declared)
	for _, kind := range allClientLoopKinds {
		if !values[string(kind)] {
			t.Errorf("allClientLoopKinds contains %q, which no clientLoopKind constant declares", kind)
		}
	}

	usedBy, sites := clientLoopKindsBySite(fset, files, declared)

	// Vacuity floor, the same shape and reason as the probe scan's: a scan that
	// found no sites reports no violations and is indistinguishable from a
	// clean package. Three: the two producers plus the forwarding wrapper.
	const knownClientLoopSites = 3
	if sites < knownClientLoopSites {
		t.Fatalf("found %d resolveClientHostIdentityVia call sites, expected at least %d: the scan is not looking where it thinks it is, so its silence proves nothing",
			sites, knownClientLoopSites)
	}

	for kind, where := range usedBy {
		if len(where) > 1 {
			t.Errorf("%s is passed by %d call sites (%s) — they share one bucket, so a climbing count cannot say which multiplexer stopped resolving, and the two scans behind them differ by more than an order of magnitude (#1558)",
				kind, len(where), strings.Join(where, ", "))
		}
	}
	for name := range declared {
		if len(usedBy[name]) == 0 {
			t.Errorf("%s is declared but passed by no call site — it publishes a permanent zero row that no producer can ever move", name)
		}
	}
}

// clientLoopKindsBySite returns, per declared kind identifier, the `file:line`
// of every call site that passes it — plus how many resolveClientHostIdentityVia
// calls were seen at all, the vacuity floor's input. An argument that is not a
// declared constant (a forwarded parameter) contributes to sites and to no
// kind, so it can neither be mistaken for coverage nor silence the floor.
func clientLoopKindsBySite(fset *token.FileSet, files map[string]*ast.File, declared map[string]string) (usedBy map[string][]string, sites int) {
	usedBy = map[string][]string{}
	for name, file := range files {
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			fn, ok := call.Fun.(*ast.Ident)
			if !ok || fn.Name != "resolveClientHostIdentityVia" || len(call.Args) < 2 {
				return true
			}
			sites++
			id, ok := call.Args[1].(*ast.Ident)
			if !ok {
				return true
			}
			if _, isKind := declared[id.Name]; !isKind {
				return true
			}
			usedBy[id.Name] = append(usedBy[id.Name],
				filepath.Base(name)+":"+strconv.Itoa(fset.Position(call.Pos()).Line))
			return true
		})
	}
	return usedBy, sites
}

// declaredClientLoopKindIdents returns the identifier names of every
// `clientLoopKind` constant, mapped to its string value. Derived from the
// source rather than listed here, so a new kind is graded by existing.
func declaredClientLoopKindIdents(t *testing.T, files map[string]*ast.File) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, file := range files {
		for _, decl := range file.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || gen.Tok != token.CONST {
				continue
			}
			for _, spec := range gen.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				if typ, ok := vs.Type.(*ast.Ident); !ok || typ.Name != "clientLoopKind" {
					continue
				}
				for i, name := range vs.Names {
					out[name.Name] = probeKindLiteral(vs, i)
				}
			}
		}
	}
	if len(out) == 0 {
		t.Fatal("no clientLoopKind constants found in the package source — the scan is looking in the wrong place, so its silence proves nothing")
	}
	return out
}

// --- the static half: every run site names its own declared kind -----------

// TestEveryProbeSiteDeclaresItsOwnKind is #1534's tripwire, and it is where a
// NINTH probe is covered by existing rather than by remembering.
//
// It reads the package's own source, for the reason the shellout guard beside
// it does: every one of these sites is behind `//go:build darwin`, so a
// type-aware load on a linux runner would find none of them and pass — a gate
// whose absence reads as a pass, which is the exact defect this package keeps
// producing. parsePackageSources (shellout_guard_test.go) ignores build tags
// and excludes _test.go, so a test driving runProbe directly (this file does)
// is not graded as a production site.
//
// Three claims, and they fail on different mutations:
//
//   - every runProbe call passes a kind from the declared const block — a
//     site passing a bare string, or a kind nobody declared, is a row that
//     lands in undeclaredProbes at runtime and in nothing at all in the bundle;
//   - no two sites pass the SAME kind — the shared-bucket mutation;
//   - every declared kind is used by some site — so a kind left behind by a
//     removed probe is deleted rather than published forever at zero.
func TestEveryProbeSiteDeclaresItsOwnKind(t *testing.T) {
	fset, files := parsePackageSources(t, ".")
	declared := declaredProbeKindIdents(t, files)
	assertDeclarationMatchesAllProbeKinds(t, declared)

	usedBy, sites := probeKindsBySite(t, fset, files, declared)

	// Vacuity floor, the same shape and for the same reason as the shellout
	// guard's: a scan that found no sites reports no violations and is
	// indistinguishable from a clean package.
	const knownProbeSites = 8
	if sites < knownProbeSites {
		t.Fatalf("found %d runProbe call sites, expected at least %d: the scan is not looking where it thinks it is, so its silence proves nothing",
			sites, knownProbeSites)
	}

	assertNoTwoSitesShareAKind(t, usedBy)
	assertEveryDeclaredKindIsUsed(t, declared, usedBy)
}

// assertDeclarationMatchesAllProbeKinds checks that the set this scan reads out
// of the source and the set the runtime side counts into are the same one.
// Without it, a const added to the block but left out of allProbeKinds would
// satisfy every claim below while counting into nothing.
func assertDeclarationMatchesAllProbeKinds(t *testing.T, declared map[string]string) {
	t.Helper()
	if len(declared) != len(allProbeKinds) {
		t.Fatalf("the source declares %d probeKind constants but allProbeKinds lists %d — a kind that is not in allProbeKinds has no counter",
			len(declared), len(allProbeKinds))
	}
	values := declaredValues(declared)
	for _, kind := range allProbeKinds {
		if !values[string(kind)] {
			t.Errorf("allProbeKinds contains %q, which no probeKind constant declares", kind)
		}
	}
}

// assertNoTwoSitesShareAKind is the shared-bucket claim.
func assertNoTwoSitesShareAKind(t *testing.T, usedBy map[string][]string) {
	t.Helper()
	for kind, where := range usedBy {
		if len(where) > 1 {
			t.Errorf("%s is passed by %d call sites (%s) — they share one bucket, so a climbing count cannot say which probe stopped answering. A kind names a call site; a genuinely shared one amends this rule in a reviewable diff.",
				kind, len(where), strings.Join(where, ", "))
		}
	}
}

// assertEveryDeclaredKindIsUsed keeps a kind left behind by a removed probe
// from being published forever as a zero row no probe can move.
func assertEveryDeclaredKindIsUsed(t *testing.T, declared map[string]string, usedBy map[string][]string) {
	t.Helper()
	for name := range declared {
		if len(usedBy[name]) == 0 {
			t.Errorf("%s is declared but passed by no call site — it publishes a permanent zero row that no probe can ever move", name)
		}
	}
}

// probeKindsBySite returns, per declared kind identifier, the `file:line` of
// every call site that passes it — plus how many runProbe calls were seen at
// all, which is the vacuity floor's input. A call whose kind argument is
// missing or is not a declared constant is reported here and contributes to
// sites but to no kind, so it cannot be mistaken for coverage.
func probeKindsBySite(t *testing.T, fset *token.FileSet, files map[string]*ast.File, declared map[string]string) (usedBy map[string][]string, sites int) {
	t.Helper()
	usedBy = map[string][]string{}
	for name, file := range files {
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || !isRunProbeCall(call) {
				return true
			}
			sites++
			where := filepath.Base(name) + ":" + strconv.Itoa(fset.Position(call.Pos()).Line)
			if kind := probeKindArgument(t, call, where, declared); kind != "" {
				usedBy[kind] = append(usedBy[kind], where)
			}
			return true
		})
	}
	return usedBy, sites
}

// probeKindArgument returns the declared kind identifier one runProbe call
// passes, or "" (having reported why) when it passes something else.
func probeKindArgument(t *testing.T, call *ast.CallExpr, where string, declared map[string]string) string {
	t.Helper()
	if len(call.Args) < 2 {
		t.Errorf("%s: runProbe is called with %d arguments — the probe kind is missing, so this child is counted under nothing",
			where, len(call.Args))
		return ""
	}
	id, ok := call.Args[1].(*ast.Ident)
	if !ok {
		t.Errorf("%s: runProbe's kind argument is not an identifier — only the declared probeKind constants (%s) are counted; anything else lands in undeclared_probe_kinds and appears in no row",
			where, strings.Join(sortedKeys(declared), ", "))
		return ""
	}
	if _, isKind := declared[id.Name]; !isKind {
		t.Errorf("%s: runProbe is passed %s, which is not one of the declared probeKind constants (%s) — an undeclared kind is counted in undeclared_probe_kinds and appears in no row",
			where, id.Name, strings.Join(sortedKeys(declared), ", "))
		return ""
	}
	return id.Name
}

// declaredProbeKindIdents returns the identifier names of every `probeKind`
// constant, mapped to its string value. Derived from the source rather than
// listed here, so a new kind is graded by existing.
func declaredProbeKindIdents(t *testing.T, files map[string]*ast.File) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, file := range files {
		for _, decl := range file.Decls {
			if gen, ok := decl.(*ast.GenDecl); ok && gen.Tok == token.CONST {
				collectProbeKindConsts(gen, out)
			}
		}
	}
	if len(out) == 0 {
		t.Fatal("no probeKind constants found in the package source — the scan is looking in the wrong place, so its silence proves nothing")
	}
	return out
}

// collectProbeKindConsts adds every `<name> probeKind = "<token>"` spec of one
// const block to out. The type is required on each spec rather than inherited
// from the first, which is how probecount.go spells them and what keeps a
// neighbouring const of some other type out of the set.
func collectProbeKindConsts(gen *ast.GenDecl, out map[string]string) {
	for _, spec := range gen.Specs {
		vs, ok := spec.(*ast.ValueSpec)
		if !ok {
			continue
		}
		if typ, ok := vs.Type.(*ast.Ident); !ok || typ.Name != "probeKind" {
			continue
		}
		for i, name := range vs.Names {
			out[name.Name] = probeKindLiteral(vs, i)
		}
	}
}

// probeKindLiteral returns the unquoted string value of spec's i-th value, or
// "" when there is none to read.
func probeKindLiteral(vs *ast.ValueSpec, i int) string {
	if i >= len(vs.Values) {
		return ""
	}
	lit, ok := vs.Values[i].(*ast.BasicLit)
	if !ok {
		return ""
	}
	return strings.Trim(lit.Value, `"`)
}

// declaredValues inverts declaredProbeKindIdents to a set of token values.
func declaredValues(declared map[string]string) map[string]bool {
	out := map[string]bool{}
	for _, v := range declared {
		out[v] = true
	}
	return out
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
