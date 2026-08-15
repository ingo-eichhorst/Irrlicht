//go:build darwin

package processlifecycle

// This file is the darwin half of #1525's coverage: which outcome a real
// evaluation reaches, what the gate says about it, and the cross-check against
// #1534's probe counters that neither issue could perform on its own.
//
// Every assertion is a delta, taken serially, for the reason probecount_test.go
// gives beside it. The injected-walk tests use walk/aborted/finished from
// osutil_darwin_test.go, because driving the two walks to chosen verdicts is
// the only way to reach all three outcomes on purpose — in a live chain a `ps`
// that fails for one walk fails for the other.

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// recordingLogger captures what the gate wrote, so the two arms of the
// aborted-walk line can be told apart. It is not a shared mock: the arms differ
// by LEVEL, and a recorder that flattened them would make the whole rate
// decision untestable.
type recordingLogger struct {
	info  []loggedLine
	errs  []loggedLine
	other int
}

type loggedLine struct{ component, sessionID, message string }

func (l *recordingLogger) LogInfo(component, sessionID, message string) {
	l.info = append(l.info, loggedLine{component, sessionID, message})
}

func (l *recordingLogger) LogError(component, sessionID, message string) {
	l.errs = append(l.errs, loggedLine{component, sessionID, message})
}

func (l *recordingLogger) LogProcessingTime(string, string, int64, int, string) { l.other++ }
func (l *recordingLogger) Close() error                                         { return nil }

// hostGateLines returns only the lines this gate wrote, so a future logger
// shared with something else cannot inflate the counts below.
func (l *recordingLogger) hostGateLines() (info, errs []loggedLine) {
	for _, line := range l.info {
		if line.component == logComponentHostGate {
			info = append(info, line)
		}
	}
	for _, line := range l.errs {
		if line.component == logComponentHostGate {
			errs = append(errs, line)
		}
	}
	return info, errs
}

// TestHostGateCompletedOutcomesDoNotShareABucket is the central claim for the
// two outcomes a COMPLETED walk can reach: an allow-listed host and no
// allow-listed host are different facts and land in different rows.
//
// It is the counter's reason for existing at all. Before #1525 the completed
// miss was the only outcome that left any trace, so a daemon admitting
// everything and one rejecting correctly produced the same evidence; a counter
// that merged the two would reproduce that inside its own fix.
func TestHostGateCompletedOutcomesDoNotShareABucket(t *testing.T) {
	ctx := context.Background()

	before := readHostGateLedger()
	if !isKnownInteractiveHostVia(ctx, 4242, walk("iTerm.app", finished), walk("", finished)) {
		t.Fatal("a curated terminal must admit, or this row is not testing the matched outcome")
	}
	assertHostGateMoved(t, before, map[string]uint64{"admitted.host_matched": 1},
		"a completed walk that found a curated terminal is the ordinary admission")

	before = readHostGateLedger()
	if !isKnownInteractiveHostVia(ctx, 4242, walk("", finished), walk("md.obsidian", finished)) {
		t.Fatal("an allow-listed embedded host must admit (#728)")
	}
	assertHostGateMoved(t, before, map[string]uint64{"admitted.host_matched": 1},
		"the embedded-host carve-out is the same outcome as a curated match: both are a walk that RAN and found an allow-listed host")

	before = readHostGateLedger()
	if isKnownInteractiveHostVia(ctx, 4242, walk("", finished), walk("com.steipete.codexbar", finished)) {
		t.Fatal("a completed walk that found no allow-listed host must reject — #784")
	}
	assertHostGateMoved(t, before, map[string]uint64{"rejected.no_known_host": 1},
		"#784 working is its own row: merged with the admission above, a gate that stopped gating would look identical to one that never had to")
}

// TestHostGateAbortedWalkIsItsOwnOutcome is the arm this issue is about, and
// the conflation it guards against is the exact one #1513 created.
//
// An aborted walk and a completed miss carry the SAME two empty strings; the
// completeness bit is the only thing separating "admitted on no evidence" from
// "#784 working". Counting them together produces a number that climbs on a
// healthy machine and on a machine whose gate has quietly stopped gating, which
// is the state of the world this issue was filed about.
func TestHostGateAbortedWalkIsItsOwnOutcome(t *testing.T) {
	ctx := context.Background()

	before := readHostGateLedger()
	if !isKnownInteractiveHostVia(ctx, 4242, walk("", aborted), walk("", finished)) {
		t.Fatal("an aborted walk must admit (#1513), or this test is measuring the wrong behaviour")
	}
	assertHostGateMoved(t, before, map[string]uint64{"admitted.walk_aborted": 1},
		"an aborted walk must not be counted as a completed miss: both resolve to \"\", and only one of them is evidence")

	// The live path, end to end, through the production entry point rather than
	// injected walks: a reaped pid's first readProcInfo fails, which is the
	// branch a `ps` over its 2s ceiling takes.
	pid := exitedPID(t)
	before = readHostGateLedger()
	if !IsKnownInteractiveHost(pid) {
		t.Fatal("a reaped pid's walk cannot be completed and must admit (#1513)")
	}
	assertHostGateMoved(t, before, map[string]uint64{"admitted.walk_aborted": 1},
		"the outcome the whole production path reaches for an unreadable process is the aborted one")
}

// TestHostGateMatchedAdmissionIsCountedByNobodyAsAnAbort is the vacuity guard.
//
// A gate that counted SOMETHING for every session is indistinguishable from one
// that counts correctly until a test asserts the silence — and the silence that
// matters is this one, because admitted.walk_aborted is the row a reader acts
// on. If an ordinary admission contributed to it, every healthy daemon would
// report a gate that had stopped gating.
func TestHostGateMatchedAdmissionIsCountedByNobodyAsAnAbort(t *testing.T) {
	ctx := context.Background()
	before := readHostGateLedger()

	if !isKnownInteractiveHostVia(ctx, 4242, walk("iTerm.app", finished), walk("", finished)) {
		t.Fatal("a curated terminal must admit")
	}

	moved := hostGateSince(before)
	if moved["admitted.walk_aborted"] != 0 {
		t.Errorf("an ordinary admission moved admitted.walk_aborted by %d — the one row a reader acts on must not climb on a healthy daemon",
			moved["admitted.walk_aborted"])
	}
	if moved["rejected.no_known_host"] != 0 {
		t.Errorf("an ordinary admission moved rejected.no_known_host by %d", moved["rejected.no_known_host"])
	}
	if moved["admitted.not_evaluated"] != 0 {
		t.Errorf("a darwin walk moved admitted.not_evaluated by %d — that row means the platform declined to look", moved["admitted.not_evaluated"])
	}
}

// TestHostGateLogsTheAbortedWalkFirstAtErrorThenAtInfo pins the rate decision
// and the two arms it produces.
//
// The rate is deliberate: this arm fires once per new-session admission for an
// opted-in adapter, and onNewSession already writes an unconditional line for
// every one of those, so a line per occurrence adds no flood. What the split
// buys is that the FIRST one shouts — an error line is what a bug reporter and
// a grep both find — while every occurrence still names its own session,
// because the harm this issue describes is per-session.
func TestHostGateLogsTheAbortedWalkFirstAtErrorThenAtInfo(t *testing.T) {
	pid := exitedPID(t)
	log := &recordingLogger{}
	gate := HostGate(log)

	if !gate("sess-first", pid) || !gate("sess-second", pid) {
		t.Fatal("an aborted walk must admit (#1513)")
	}

	info, errs := log.hostGateLines()
	if len(errs) != 1 {
		t.Fatalf("host-gate error lines = %d, want exactly 1: the first aborted walk shouts and the rest do not (%v)", len(errs), errs)
	}
	if errs[0].sessionID != "sess-first" {
		t.Errorf("the first line names session %q, want sess-first — a line that cannot name the session it admitted explains no phantom", errs[0].sessionID)
	}
	for _, want := range []string{"could not complete", fmt.Sprintf("pid %d", pid), "host_gate", "#1534", "skipping session bound to a non-interactive host"} {
		if !strings.Contains(errs[0].message, want) {
			t.Errorf("the first aborted-walk line does not mention %q — it must name where the volume is and which OTHER host-gate line it is not: %q", want, errs[0].message)
		}
	}

	if len(info) != 1 {
		t.Fatalf("host-gate info lines = %d, want exactly 1: every occurrence names its own session, because the harm is per-session (%v)", len(info), info)
	}
	if info[0].sessionID != "sess-second" {
		t.Errorf("the repeat line names session %q, want sess-second", info[0].sessionID)
	}
}

// TestHostGateSaysNothingWhenItDidNotAdmitOnNoEvidence is the log line's own
// vacuity guard. A line written for every evaluation explains nothing, and it
// would drown the one case it exists for in exactly the way
// hookjson.IgnoreUnknownEvent was built to avoid.
//
// The rejection arm is deliberately silent HERE rather than everywhere: it is
// SessionDetector.admitHost that logs it, and that line already existed. Two
// lines for one rejection would be the third diagnosis this section keeps
// trying to keep distinguishable.
func TestHostGateSaysNothingWhenItDidNotAdmitOnNoEvidence(t *testing.T) {
	log := &recordingLogger{}
	gate := HostGate(log)

	if gate("sess-launchd", 1) {
		t.Fatal("launchd is readable and is not an interactive host — a completed walk that found nothing must still reject (#784)")
	}

	info, errs := log.hostGateLines()
	if len(info) != 0 || len(errs) != 0 {
		t.Errorf("the gate wrote %d info and %d error line(s) for a completed walk: %v %v — only the admitted-on-no-evidence arm is this component's to report",
			len(info), len(errs), info, errs)
	}
}

// TestHostGateAgreesWithIsKnownInteractiveHost is the #1390 lesson applied to
// two entry points: the daemon wires HostGate, while every other test in this
// package drives IsKnownInteractiveHost, so a divergence between them would
// mean the tested object is not the shipped one.
//
// They share hostGateFor, so this cannot fail without an edit that splits them
// — which is what it is here to catch. Two live pids, one per polarity: a
// completed rejection and an aborted admission.
func TestHostGateAgreesWithIsKnownInteractiveHost(t *testing.T) {
	gate := HostGate(&recordingLogger{})
	for _, pid := range []int{1, exitedPID(t)} {
		if got, want := gate("sess", pid), IsKnownInteractiveHost(pid); got != want {
			t.Errorf("pid %d: HostGate = %v, IsKnownInteractiveHost = %v — the daemon wires the first and the suite grades the second", pid, got, want)
		}
	}
}

// TestAbortedWalkCanFollowAnAnsweredPsProbe is the cross-check #1525 asks for
// against #1534's counters, and it CONFIRMS #1574 by measurement rather than by
// reading the source.
//
// The reasoning behind the cross-check is that a walk aborts because a bounded
// child did not answer, so an abort ought to imply an upstream non-answer. It
// does not, and this is why: `ps` exits 1 for a pid it cannot find, which is a
// real "no such process" and is counted ANSWERED by probeOutcomeRule — while
// readProcInfo classifies any non-nil error as a failure, so the walk aborts.
// The gate then reports "admitted on a walk I could not complete" beside a
// ps.proc_info row reporting perfect health.
//
// This is a LOCK on the disagreement, not on the defect being unfixed: if #1574
// is resolved by giving readProcInfo processTTYVia's classification, a reaped
// pid stops aborting the walk and this test goes red, which is the moment to
// rewrite it. hostGateReconciliationNote in the diagnostics bundle says the same
// thing to a reader who only has the numbers.
func TestAbortedWalkCanFollowAnAnsweredPsProbe(t *testing.T) {
	// Resolved before the ledgers are read: exitedPID spawns and reaps a
	// process of its own, and anything it runs would otherwise land in the
	// deltas below.
	pid := exitedPID(t)

	beforeProbes := readProbeLedger()
	beforeGate := readHostGateLedger()

	if !IsKnownInteractiveHost(pid) {
		t.Fatal("a walk that could not be completed must admit (#1513)")
	}

	assertHostGateMoved(t, beforeGate, map[string]uint64{"admitted.walk_aborted": 1},
		"the walk aborted, so the gate admitted on no evidence")
	assertProbesMoved(t, beforeProbes, map[string]ProbeCount{
		"ps.proc_info": {Probe: "ps.proc_info", Answered: 1},
	}, "#1574: `ps` ANSWERED (exit 1, no such process) and readProcInfo treated it as a failure, so an aborted walk can sit beside a probe row reporting health — this is the pair of numbers the bundle's reconciliation note explains")
}
