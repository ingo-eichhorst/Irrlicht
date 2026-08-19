//go:build darwin

package processlifecycle

// This file is the darwin half of #1525's coverage: which outcome a real
// evaluation reaches, what the gate says about it, and the cross-check against
// #1534's probe counters that neither issue could perform on its own.
//
// Every assertion is a delta, taken serially, for the reason probecount_test.go
// gives beside it. The injected-walk tests use walk/aborted/gone/finished from
// osutil_darwin_test.go, because driving the two walks to chosen verdicts is
// the only way to reach every outcome on purpose — in a live chain a `ps` that
// fails for one walk fails for the other, and only ONE of the two no-evidence
// ends can be arranged live at all (a reaped pid; a `ps` cannot be pushed over
// its ceiling on purpose).

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

// TestHostGateNoEvidenceOutcomesDoNotShareABucket is the arm #1525 is about,
// widened by #1574 into the exact mirror of the completed-outcomes test above.
//
// Three of the four darwin outcomes carry the SAME two empty strings, and how
// the walk ended is the only thing separating them. The first split — an
// admission on no evidence from "#784 working" — is #1513's, and merging those
// produces a number that climbs identically on a healthy machine and on one
// whose gate has quietly stopped gating. The second split is #1574's, between
// the two admissions on no evidence, and it exists because only ONE of them is
// meant to be read against #1534's per-probe non-answer rows: a walk stopped by
// a `ps` that ANSWERED "no such process" made admitted.walk_aborted climb on a
// machine whose probes were fine.
//
// Injected walks, because a real `ps` cannot be driven over its 2s ceiling on
// purpose. The live production path is TestAnAnsweredPsProbeNeverProducesAn
// AbortedWalk below, which reaches the gone arm with a reaped pid and measures
// both ledgers across the call.
func TestHostGateNoEvidenceOutcomesDoNotShareABucket(t *testing.T) {
	ctx := context.Background()

	before := readHostGateLedger()
	if !isKnownInteractiveHostVia(ctx, 4242, walk("", aborted), walk("", finished)) {
		t.Fatal("a walk stopped by an unanswered probe must admit (#1513), or this test is measuring the wrong behaviour")
	}
	assertHostGateMoved(t, before, map[string]uint64{"admitted.walk_aborted": 1},
		"a walk that could not be answered must not be counted as a completed miss: both resolve to \"\", and only one of them is evidence")

	before = readHostGateLedger()
	if !isKnownInteractiveHostVia(ctx, 4242, walk("", gone), walk("", finished)) {
		t.Fatal("a walk stopped by a process that no longer exists must admit too (#1574): a gone pid says nothing about the host")
	}
	assertHostGateMoved(t, before, map[string]uint64{"admitted.process_gone": 1},
		"#1574: nothing failed to answer here, so folding this into admitted.walk_aborted sends a reader hunting for a probe that is healthy")
}

// TestHostGateSharingReadsReportsWhyTheWalkStopped drives the PRODUCTION wiring
// — the two walks built over one shared read memo, which is where walkEndOf
// joins a walk's completeness bit to the reason its reads recorded.
//
// The injected-walk tests above cannot reach that join: they hand
// hostGateOutcomeVia a fixed end and never build a memo, so a walkEndOf that
// answered walkProcessGone for everything would leave every one of them green.
// Here the end is derived, from reads that are scripted one layer lower.
//
// Three arms, because two of them are each other's vacuity guard: a gate that
// reported process_gone for every incomplete walk and a gate that reported it
// correctly are indistinguishable without the killed-`ps` row, and a gate that
// reported it for every walk at all is indistinguishable without the completed
// row.
func TestHostGateSharingReadsReportsWhyTheWalkStopped(t *testing.T) {
	ctx := context.Background()
	noBundleID := func(context.Context, string) (string, error) { return "", nil }

	goneTable := newScriptedProcTable() // carries no pids at all: every read answers "no such process"
	before := readHostGateLedger()
	if !hostGateOutcomeSharingReads(ctx, 4242, newAncestryReadsVia(goneTable.read), noBundleID).admits() {
		t.Fatal("a walk stopped by a gone process must admit (#1574/#1513)")
	}
	assertHostGateMoved(t, before, map[string]uint64{"admitted.process_gone": 1},
		"the production wiring must carry the reason the reads recorded, not just the completeness bit the walk returns")

	killed := newScriptedProcTable()
	killed.rows[4242] = procInfoAnswer{ppid: 1, cmd: "/bin/zsh"}
	killed.fails[4242] = 1
	before = readHostGateLedger()
	if !hostGateOutcomeSharingReads(ctx, 4242, newAncestryReadsVia(killed.read), noBundleID).admits() {
		t.Fatal("a walk stopped by a `ps` that did not answer must admit (#1513)")
	}
	assertHostGateMoved(t, before, map[string]uint64{"admitted.walk_aborted": 1},
		"a killed `ps` reported as a gone process sends a reader looking for a race when what they have is a dying probe — #1574 in the other direction")

	live := newScriptedProcTable()
	live.rows[4242] = procInfoAnswer{ppid: 1, cmd: "/bin/zsh"}
	before = readHostGateLedger()
	if hostGateOutcomeSharingReads(ctx, 4242, newAncestryReadsVia(live.read), noBundleID).admits() {
		t.Fatal("a completed walk that found no allow-listed host must still reject — #784")
	}
	assertHostGateMoved(t, before, map[string]uint64{"rejected.no_known_host": 1},
		"a walk that RAN and found nothing is #784 working, and must not be swept into either no-evidence row")
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

// TestHostGateLogsAnAdmissionOnNoEvidenceFirstAtErrorThenAtInfo pins the rate
// decision and the two arms it produces.
//
// The rate is deliberate: this arm fires once per new-session admission for an
// opted-in adapter, and onNewSession already writes an unconditional line for
// every one of those, so a line per occurrence adds no flood. What the split
// buys is that the FIRST one shouts — an error line is what a bug reporter and
// a grep both find — while every occurrence still names its own session,
// because the harm this issue describes is per-session.
//
// The line NAMES ITS OUTCOME since #1574, and that is what makes one log path
// legitimate for both no-evidence admissions. A reaped pid is the only
// arrangement this entry point can be driven into — it takes a pid, so there is
// nothing to inject and a `ps` cannot be pushed over its ceiling on purpose — so
// a second report function for the aborted arm would be a code path nothing
// executes. The token in the line is what a reader pairs with the row in
// probes.json, and noEvidenceReason is what tells the two apart in prose.
func TestHostGateLogsAnAdmissionOnNoEvidenceFirstAtErrorThenAtInfo(t *testing.T) {
	pid := exitedPID(t)
	log := &recordingLogger{}
	gate := HostGate(log)

	if !gate("sess-first", pid) || !gate("sess-second", pid) {
		t.Fatal("an admission on no evidence must admit (#1513/#1574)")
	}

	info, errs := log.hostGateLines()
	if len(errs) != 1 {
		t.Fatalf("host-gate error lines = %d, want exactly 1: the first such walk shouts and the rest do not (%v)", len(errs), errs)
	}
	if errs[0].sessionID != "sess-first" {
		t.Errorf("the first line names session %q, want sess-first — a line that cannot name the session it admitted explains no phantom", errs[0].sessionID)
	}
	for _, want := range []string{
		"NO EVIDENCE", fmt.Sprintf("pid %d", pid), "host_gate", "#1534", "skipping session bound to a non-interactive host",
		// The outcome token, because this line and the probes.json row have to
		// name each other: a reader who cannot tell which of the two
		// no-evidence rows this line belongs to cannot use either.
		string(hostGateProcessGone), "no such process",
	} {
		if !strings.Contains(errs[0].message, want) {
			t.Errorf("the first no-evidence line does not mention %q — it must name its own outcome, where the volume is, and which OTHER host-gate line it is not: %q", want, errs[0].message)
		}
	}
	if strings.Contains(errs[0].message, "killed by its 2s ceiling") {
		t.Errorf("the line for a REAPED pid gives the unanswered-probe reason — the two admissions on no evidence would then be indistinguishable in events.log, which is #1574 reproduced in the logging: %q", errs[0].message)
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

// TestAnAnsweredPsProbeNeverProducesAnAbortedWalk is the cross-check #1525 asks
// for against #1534's counters, and it is the same claim its predecessor made,
// read from the other side of #1574.
//
// The reasoning behind the cross-check is that a walk aborts because a bounded
// child did not answer, so an abort ought to imply an upstream non-answer. Until
// #1574 it did not, and this test (as TestAbortedWalkCanFollowAnAnsweredPsProbe,
// PR #1576) measured the disagreement and locked it: `ps` exits 1 for a pid it
// cannot find, which is a real "no such process" and is counted ANSWERED by
// probeOutcomeRule, while readProcInfo classified ANY non-nil error as a
// failure — so the gate reported "admitted on a walk I could not complete"
// beside a ps.proc_info row reporting perfect health. Its own doc named this
// rewrite as the moment the fix landed.
//
// What it locks now is that the two ledgers describe the same event. An answered
// "no such process" reaches its own outcome (admitted.process_gone), so
// admitted.walk_aborted keeps meaning what its doc says — a walk stopped by a
// child that did NOT answer — and a reader who watches that row climb can go
// looking for the non-answer underneath it instead of being told by a note that
// the number is allowed to lie.
//
// The VERDICT is deliberately unchanged: both outcomes admit (#1513), because a
// process that no longer exists is no more evidence of a non-interactive host
// than an unanswered probe is. What changed is which of the two the daemon
// reports.
func TestAnAnsweredPsProbeNeverProducesAnAbortedWalk(t *testing.T) {
	// Resolved before the ledgers are read: exitedPID spawns and reaps a
	// process of its own, and anything it runs would otherwise land in the
	// deltas below.
	pid := exitedPID(t)

	beforeProbes := readProbeLedger()
	beforeGate := readHostGateLedger()

	if !IsKnownInteractiveHost(pid) {
		t.Fatal("a reaped pid says nothing about the host, so the gate must still admit (#1513)")
	}

	assertHostGateMoved(t, beforeGate, map[string]uint64{"admitted.process_gone": 1},
		"`ps` ANSWERED that this pid does not exist, so nothing here failed to answer — counting it as admitted.walk_aborted is #1574, and it makes the one row a reader acts on climb on a machine whose probes are perfectly healthy")
	assertProbesMoved(t, beforeProbes, map[string]ProbeCount{
		"ps.proc_info": {Probe: "ps.proc_info", Answered: 1},
	}, "the probe ANSWERED, and after #1574 the gate row above is derived from that same answer — the two ledgers agree instead of contradicting each other")
}
