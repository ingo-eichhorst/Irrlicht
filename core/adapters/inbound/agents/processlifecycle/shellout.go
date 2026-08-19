package processlifecycle

import (
	"context"
	"os/exec"
	"time"

	"irrlicht/core/pkg/shellout"
)

// shelloutTimeout is the ceiling EVERY bounded shellout in this package runs
// under. It was an unnamed `2*time.Second` literal in eight places until #1538,
// while three doc comments and a test assertion had already made the value
// load-bearing ("half of 2s") — so the number was a contract that nothing named.
//
// Two sibling packages already name their own copy and one of them points here
// for the justification: core/pkg/cliprobe's exported Timeout, and
// core/adapters/outbound/control's execTimeout, whose doc reads "matching the
// process observer's 2-second ceiling (process_darwin.go)" — a cross-reference
// to a constant this package did not have. Naming it here does not merge those
// (a consent-path version check and a window-targeting probe are entitled to
// different ceilings, and control's is a different layer); it gives that
// reference something to point AT.
//
// The value is a compromise between two failures the shellouts sit between: a
// probe that blocks session discovery, and a probe killed before it answers —
// and #1538's whole subject is that the second is only safe because
// probeAnswered reports it as a non-answer rather than as an empty one.
//
// Since #1529 every site derives this ceiling FROM the caller's context rather
// than from context.Background(), so a child is killed at whichever comes
// first: its own ceiling, or an aggregate deadline the caller imposed across a
// whole sequence of them (clientHostBudget is the one such aggregate today;
// noAggregateBudget names the reads that deliberately have none). Nothing about
// the per-child value changed — what changed is that a caller can now cap the
// SUM, which this constant never could.
//
// What the probes actually COST is what makes 2s a compromise rather than a
// guess (darwin/arm64, 10 CPU, idle, warm; n as shown):
//
//	ps.proc_info         median 3.4ms   p99  4.1ms  min  2.7ms  n=300
//	ps.tty               median 3.2ms   p99  4.1ms  min  2.7ms  n=300
//	plutil.bundle_id     median 5.4ms   p99  7.2ms  min  4.4ms  n=300
//	pgrep.discover       median 21.6ms  p99 27.2ms  min 20.6ms  n=20
//	lsof.cwd             median 45.1ms  p99 46.8ms  min 44.3ms  n=20
//	lsof.writer          median 295.9ms p99 349.2ms min 251.9ms n=20
//	lsof.herdr_clients   median 289.5ms p99 339.7ms min 257.4ms n=20
//
// So 2s is roughly 6x the heaviest probe's p99, and the two whole-process-table
// `lsof` scans are what make it a ceiling rather than a formality — they are two
// orders of magnitude above the `ps` reads sharing it.
//
// READ min, NOT median, when carrying any of these to another machine. median
// and p99 are statements about the LOAD as much as about the exec: measured on
// this machine minutes apart, `ps.proc_info` moved 3.4ms -> 7.1ms and
// `plutil.bundle_id` 5.4ms -> 10.2ms simply by running twelve busy loops beside
// them. That is the whole of #1572's 4x discrepancy (see bundleIDMemo).
//
// regenerate: IRRLICHT_MEASURE_PROBE_COSTS=1 go test
// ./core/adapters/inbound/agents/processlifecycle/ -run TestMeasureProbeCosts -v
const shelloutTimeout = 2 * time.Second

// shelloutCmd builds one bounded child process. Every injected shellout in this
// package is one of these, and the site's own arguments are CLOSED OVER rather
// than passed through: a builder that took them would be handing the callee
// values it already holds, and no test has ever read one — every fixture in
// this package spells them `_`.
//
// Injection is the point, not the arguments. The distinction each caller draws
// is a property of the CHILD PROCESS — ran to a normal exit, versus killed or
// never started — which no faked return value can pin and no arrangement of
// live processes can be driven into on purpose. It began as bundleIDCmd
// (#1524); #1538 made it one type instead of five, because five signatures
// carrying arguments nobody reads is five chances to forget the ceiling.
type shelloutCmd func(ctx context.Context) *exec.Cmd

// runProbe starts one bounded child process and hands its output and error
// back unclassified. It is the ONLY place in this package that applies
// shelloutTimeout, and — enforced by TestEveryChildProcessRunsThroughRunProbe
// — the only place that starts a child at all.
//
// It exists because #1538 extracted the two VALUES out of the bounded-shellout
// block (the ceiling and the predicate) and left the SHAPE copied at all eight
// sites:
//
//	ctx, cancel := context.WithTimeout(ctx, shelloutTimeout)
//	defer cancel()
//	out, err := build(ctx).Output()
//
// Three lines that must be got right together — derive from the CALLER's
// context so an aggregate deadline still caps the sum (#1529), name the
// ceiling, and cancel — written out eight times. A ninth site copying a
// neighbour got them right; a ninth site written from scratch had three
// independent chances not to. Now it has none: there is one copy, and the
// routing rule makes a second inexpressible rather than merely detectable.
//
// ctx is the caller's AGGREGATE budget, and the child's ceiling is derived
// from it, so the child dies at whichever comes first. A caller with no
// context of its own passes context.Background() — the three
// outbound.ProcessObserver methods do, because that port takes no context and
// widening it is a different change — and still gets the child ceiling. That
// is not the bare Background() core/architecture_shellout_test.go forbids:
// nothing here reaches exec.CommandContext without passing through the
// WithTimeout below.
//
// Those three deliberately spell context.Background() rather than reusing
// noAggregateBudget() (osutil.go), and the two are NOT interchangeable even
// though they evaluate to the same thing. noAggregateBudget names a DECISION —
// a host read that COULD take an aggregate and deliberately does not, with a
// polarity argument about which way the answer would move if it did. These
// three name an ABSENCE: outbound.ProcessObserver takes no context, so there is
// nothing to thread and no such decision was made. Collapsing either into the
// other attaches one's justification to the other's call site, which is why
// this is written down rather than left looking like an inconsistency worth
// tidying.
//
// WHAT IT DELIBERATELY DOES NOT DO is classify. #1547 proposed the wider
// signature (out []byte, answered bool, err error), on the premise that
// folding the predicate in would let TestEveryBoundedShelloutClassifiesItsError
// be deleted. It would not: a caller can spell `out, _, _ := runProbe(…)`, or
// `if err != nil { return "" }` on the third result, and both are the family's
// exact collapse routed perfectly through the helper. The guard therefore has
// to survive, and it reasons about "does this run site's error reach a
// classifier" — so the helper that COMPOSES with it is the one that returns
// (out, err) and leaves the verdict, whose POLARITY is per-call-site anyway, at
// the call site. See the file header of shellout_guard_test.go for the rule and
// what each half of it can and cannot see.
//
// WHAT IT DOES DO, since #1534, is COUNT. Being the only place a child starts
// is exactly what makes one counting site cover every probe, present and
// future — eight call sites' worth of remembering removed the same way #1547
// removed eight copies of the ceiling. The counter has to know WHICH question
// was asked, which is why this signature grew a parameter rather than deriving
// one: a shelloutCmd closes its arguments over (see the type), so there is
// nothing in build to derive a kind from, and TestEveryProbeSiteDeclaresItsOwnKind
// grades what each site passes.
//
// Counting is still not CLASSIFYING, and the distinction is the same one the
// paragraph above draws. observeProbe records whether the CHILD answered —
// shellout.Answered's tool-independent half — and touches no site's exit-code
// allowlist and no site's polarity. probecount.go's probeOutcomeRule carries
// that argument and states what it costs.
func runProbe(ctx context.Context, kind probeKind, build shelloutCmd) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, shelloutTimeout)
	defer cancel()
	out, err := build(ctx).Output()
	observeProbe(kind, err)
	return out, err
}

// probeAnswered is this package's binding of the shared predicate
// core/pkg/shellout.Answered, which every bounded child process in the repo
// classifies its error with (#1538). The implementation, and the measurements
// behind it — why a mid-run deadline kill is an *exec.ExitError that does NOT
// wrap context.DeadlineExceeded, and why ProcessState.Exited() rather than
// errors.Is is the ran-vs-killed discriminator — live there.
//
// It is a pure alias — same signature, same semantics, binding nothing — and
// saying so is better than the tidier story that it is "this package's binding
// of the empty-variadic form". That story is false: process_darwin.go calls
// probeAnswered(err, pgrepNoMatch), so the variadic is forwarded, not bound.
// lsofProbeRan is the only real binding in this package.
//
// #1543 left its survival provisional — "deleting it is a reasonable
// follow-up" — and #1547 is where that was settled, by disturbing the
// machinery for other reasons and asking the question again with the diff
// already open. The answer came back the same, and the reason is worth stating
// once so it is not re-opened a third time:
//
//   - The package needs a local name for the predicate regardless, because
//     lsofProbeRan is a REAL binding built on it. Deleting the alias while
//     keeping lsofProbeRan would leave the local vocabulary existing for
//     exactly one tool.
//   - shellout_guard_test.go's shelloutClassifiers table IS that vocabulary,
//     and five committed corpus rows pin how the guard recognises a call to it.
//     Deleting the alias deletes those rows and buys nothing behavioural — it
//     spends predecessor coverage on a rename.
//
// Narrowing it to a non-variadic form remains as unavailable as ever:
// process_darwin.go calls probeAnswered(err, pgrepNoMatch).
func probeAnswered(err error, answeredExitCodes ...int) bool {
	return shellout.Answered(err, answeredExitCodes...)
}
