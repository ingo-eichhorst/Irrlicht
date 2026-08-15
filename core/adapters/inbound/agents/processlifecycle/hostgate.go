// hostgate.go counts what the #784 host-admission gate actually DECIDED, and
// says so out loud the first time it admitted on no evidence at all (#1525).
//
// The gate has three outcomes on darwin and, until this file, exactly one of
// them left a trace:
//
//	completed walk, allow-listed host    admit    reported by nothing
//	completed walk, no allow-listed host reject   one LogInfo in admitHost
//	aborted walk                         admit    reported by nothing
//
// Before #1513 the aborted walk was silently a REJECTION — a legitimate session
// that never appeared, permanently, because SessionDetector caches the
// rejection in hostGateRejected and never evicts from it. After #1513 it is
// silently an ADMISSION, which is the better failure and is still silent: a
// false admission now produces a session nobody started interactively, with
// nothing anywhere to explain where it came from. And the one existing log line
// fires for the completed-MISS case only, so a daemon whose gate is thrashing
// on `ps` timeouts and one whose gate is never exercised at all produce the
// same events.log.
//
// Three decisions here are load-bearing.
//
//   - COUNTED WHERE THE THREE-WAY ANSWER EXISTS, which is inside the darwin
//     walk and nowhere above it. The issue proposed counting in
//     SessionDetector.admitHost "because the darwin function returns a bare
//     bool"; that reasoning inverts. admitHost receives a bare bool, so the WHY
//     is precisely what it does not have — and it receives that bool through
//     PIDManager.AllowsSession, which returns true for four further reasons
//     that are not gate outcomes at all (the adapter opted out, no discoverer
//     is installed, no PID was found, no gate is wired). The three-way
//     distinction lives in hostGateOutcomeFrom, which has term, bundleID and
//     complete, and that is where it is counted.
//   - THE VERDICT IS DERIVED FROM THE COUNTED OUTCOME, not computed beside it.
//     isKnownInteractiveHostFrom is hostGateOutcomeFrom(...).admits(), so there
//     is one classifier and the number cannot drift from the behaviour it
//     describes. The cost is stated rather than hidden: a mutation that
//     conflates two outcomes in the classifier also moves the verdict whenever
//     the two differ in polarity, so such a mutation reddens a behavioural lock
//     as well as a counter test. That is the property, not a testing accident.
//   - A PLATFORM THAT DOES NOT WALK REPORTS THAT, rather than a verdict. The
//     linux and other stubs return true unconditionally; counting that as
//     "admitted.host_matched" would publish an ancestry verdict from a daemon
//     that never looked at an ancestor. They observe admitted.not_evaluated,
//     which is its own row, so a Linux bundle says "the gate was consulted N
//     times and this platform declines to look" instead of three zeros that
//     read as "the gate never fired".
//
// The counters are read into the diagnostics bundle beside #1534's per-probe
// answered/unanswered rows, in probes.json rather than a section of their own,
// because an aborted walk is the DOWNSTREAM view of a probe that did not
// answer: the cause and the effect belong on one page, and probesView computes
// the reconciliation between them. See hostGateReconciliationNote there, and
// #1574 for the known reason the two figures can legitimately disagree.
package processlifecycle

import (
	"fmt"
	"sort"
	"sync/atomic"

	"irrlicht/core/ports/outbound"
)

// hostGateOutcome names one way the #784 admission gate can end.
//
// The token's PREFIX is the verdict — every "admitted." row admitted, the one
// "rejected." row rejected — so the table in a pasted bundle can be read
// without a legend. admits() still switches on the values rather than on the
// prefix: the prefix is documentation, and a policy decided by string matching
// is one a rename silently changes.
type hostGateOutcome string

const (
	// hostGateHostMatched is a completed walk that found a curated terminal or
	// IDE (termProgramByAppName) or an allow-listed embedded host
	// (knownEmbeddedHostBundleIDs). The ordinary admission.
	hostGateHostMatched hostGateOutcome = "admitted.host_matched"
	// hostGateWalkAborted is a walk that could not be completed — a `ps` or
	// `plutil` that did not answer — so "" says nothing about the host. Admits
	// on no evidence (#1513), and is the outcome this file exists for.
	hostGateWalkAborted hostGateOutcome = "admitted.walk_aborted"
	// hostGateNotEvaluated is a platform that does not walk ancestry at all.
	// Its own row rather than a silence, because "the gate declined to look"
	// and "the gate looked and found a host" are different facts and only one
	// of them is a verdict.
	hostGateNotEvaluated hostGateOutcome = "admitted.not_evaluated"
	// hostGateNoKnownHost is a completed walk that found no allow-listed host.
	// The rejection — this is #784 working, and the one outcome that already
	// had a log line before #1525.
	hostGateNoKnownHost hostGateOutcome = "rejected.no_known_host"
)

// allHostGateOutcomes is every declared outcome. It is the set the snapshot
// reports and the set the tests grade the verdict table against, so an outcome
// added without a decision about what it does is a failure rather than a row
// that silently never appears.
var allHostGateOutcomes = []hostGateOutcome{
	hostGateHostMatched,
	hostGateWalkAborted,
	hostGateNotEvaluated,
	hostGateNoKnownHost,
}

// admits reports whether this outcome lets the session through.
//
// Three of the four admit, which is the fail-open polarity #1513 chose for this
// gate and #1524 confirmed for the plutil half: a probe that could not be asked
// is not evidence of a non-interactive host, and rejecting on it costs the user
// a legitimate session for the lifetime of the daemon.
//
// The trailing return is reached only by an outcome nobody enumerated. It
// admits for the same reason, and TestEveryHostGateOutcomeDeclaresItsVerdict is
// what stops that line from becoming a place decisions hide.
func (o hostGateOutcome) admits() bool {
	switch o {
	case hostGateHostMatched, hostGateWalkAborted, hostGateNotEvaluated:
		return true
	case hostGateNoKnownHost:
		return false
	}
	return true
}

// hostGateCounts maps every declared outcome to its counter. Built once at init
// and never written again — only the atomics inside it are — so it needs no
// mutex, the same property probeCounts has and for the same reason.
var hostGateCounts = func() map[hostGateOutcome]*atomic.Uint64 {
	m := make(map[hostGateOutcome]*atomic.Uint64, len(allHostGateOutcomes))
	for _, outcome := range allHostGateOutcomes {
		m[outcome] = &atomic.Uint64{}
	}
	return m
}()

// undeclaredHostGateOutcomes counts observations naming an outcome that is not
// in allHostGateOutcomes.
//
// It exists so a mis-wired arm fails LOUDLY rather than in the one way this
// whole file is about. Dropping the observation would make an uncounted gate
// evaluation indistinguishable from one that never happened, which is #1525's
// own failure mode reproduced inside its fix — the same argument
// undeclaredProbes carries one file over.
var undeclaredHostGateOutcomes atomic.Uint64

// hostGateOutcomeRule records what the four rows mean, because a bundle is
// pasted by a bug reporter who has not read this file.
const hostGateOutcomeRule = "a walk that ran and found an allow-listed host ADMITS; a walk that ran and found none REJECTS (#784); " +
	"a walk that could not be completed ADMITS on no evidence (#1513); a platform that does not walk ancestry at all is NOT an evaluation"

// observeHostGate records one gate evaluation and returns the outcome, so a
// call site reads `return observeHostGate(...)` and cannot reach the verdict
// without counting it.
func observeHostGate(outcome hostGateOutcome) hostGateOutcome {
	counter, declared := hostGateCounts[outcome]
	if !declared {
		undeclaredHostGateOutcomes.Add(1)
		return outcome
	}
	counter.Add(1)
	return outcome
}

// HostGateOutcomeCount is one outcome's count, as the diagnostics bundle
// reports it. Exported for the composition root, which converts it into the
// application layer's own shape — application/services must not import an
// inbound adapter (core/architecture_test.go), the same seam ProbeCount and
// hookjson.UnknownEvents cross.
type HostGateOutcomeCount struct {
	// Outcome is the token, e.g. "admitted.walk_aborted".
	Outcome string
	// Count is how many gate evaluations ended that way.
	Count uint64
}

// HostGateCounts snapshots every declared outcome, sorted by token.
//
// Every outcome is returned, including one that has never happened: a zero is
// itself a finding here. On Linux every row but admitted.not_evaluated is
// structurally zero, and on a darwin daemon a zero admitted.walk_aborted row is
// the evidence that the 2s ceilings are not firing — the question #1513 and
// #1524 both closed as unmeasured.
func HostGateCounts() []HostGateOutcomeCount {
	out := make([]HostGateOutcomeCount, 0, len(allHostGateOutcomes))
	for _, outcome := range allHostGateOutcomes {
		out = append(out, HostGateOutcomeCount{
			Outcome: string(outcome),
			Count:   hostGateCounts[outcome].Load(),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Outcome < out[j].Outcome })
	return out
}

// UndeclaredHostGateOutcomes is how many evaluations named an outcome nobody
// declared. Non-zero means HostGateCounts is INCOMPLETE and something is wired
// wrong; it is published rather than left to be inferred from rows that do not
// add up.
func UndeclaredHostGateOutcomes() uint64 { return undeclaredHostGateOutcomes.Load() }

// HostGateOutcomeRule is hostGateOutcomeRule, exported so the bundle prints the
// definition the numbers were actually produced by rather than a second copy of
// it written in the application layer.
func HostGateOutcomeRule() string { return hostGateOutcomeRule }

// logComponentHostGate is the events.log component these lines carry. Distinct
// from "session-detector", which owns the completed-MISS line, so the two
// diagnoses can be grepped apart as well as read apart.
const logComponentHostGate = "host-gate"

// HostGate is the production entry point: the #784 admission gate in the shape
// SessionDetector.SetHostGate wants, wired to report the aborted-walk arm.
//
// sessionID is carried IN rather than an outcome carried OUT, and that is the
// direction the seam widened for a reason. The gate is the only place that
// knows the pid and the three-way answer; admitHost is the only place that
// knows the id the session is about to get. Reporting a phantom admission needs
// both in one line, and passing the id inward keeps the outcome vocabulary
// inside this package — carrying it outward would require a second copy of
// these four tokens above the adapter boundary, since application/services may
// not import this package at all.
//
// The rate limiter is per-closure rather than package-global, so it is one
// first-sighting per daemon (HostGate is called once, at startup) with no reset
// hook to be a second way to zero it, and a test gets a fresh one per call.
func HostGate(log outbound.Logger) func(sessionID string, pid int) bool {
	var reportedFirstAbort atomic.Bool
	return func(sessionID string, pid int) bool {
		outcome := hostGateFor(pid)
		if outcome == hostGateWalkAborted {
			reportAbortedHostGateWalk(log, &reportedFirstAbort, sessionID, pid)
		}
		return outcome.admits()
	}
}

// reportAbortedHostGateWalk writes the aborted-walk line: the first at error
// level with the full explanation, every later one at info level naming its own
// session.
//
// The rate is decided rather than defaulted. A line per occurrence would be
// burial-by-noise if this fired per tool call, which is the failure
// hookjson.IgnoreUnknownEvent reports once-per-name to avoid — but this arm
// fires once per NEW SESSION admission for an opted-in adapter, and
// onNewSession already writes an unconditional "new session detected" line for
// every one of those. So a line per aborted admission is at most 1:1 with lines
// the daemon already emits, and only for the subset that aborted; there is no
// flood to protect against. What the split buys is severity: the harm is
// per-session (a session admitted on no evidence, with nothing to explain it),
// so every occurrence names its own session, while only the first shouts.
//
// A nil logger loses the line and keeps the count — hostGateFor has already
// observed by the time this runs — rather than panicking on the admission path.
func reportAbortedHostGateWalk(log outbound.Logger, reportedFirst *atomic.Bool, sessionID string, pid int) {
	if log == nil {
		return
	}
	if reportedFirst.CompareAndSwap(false, true) {
		log.LogError(logComponentHostGate, sessionID, fmt.Sprintf(
			"#784 host gate ADMITTED this session on an ancestry walk it could not complete (pid %d). "+
				"The walk was not answered, so it does not say the host is interactive — it says nothing at all, "+
				"and admitting is the deliberate direction (#1513): rejecting on an unanswered probe declines a "+
				"legitimate session for the lifetime of the daemon. If a session appeared that nobody started "+
				"interactively, this line is why. Further occurrences are logged at info level and counted: read "+
				"host_gate in the diagnostics bundle's probes.json for the volume, and the ps.proc_info / "+
				"plutil.bundle_id rows beside it for the probe that did not answer (#1534). This is NOT the "+
				"\"skipping session bound to a non-interactive host\" line, which is a walk that RAN and found no "+
				"allow-listed terminal or IDE — that one is #784 working.",
			pid))
		return
	}
	log.LogInfo(logComponentHostGate, sessionID, fmt.Sprintf(
		"#784 host gate admitted this session on an ancestry walk it could not complete (pid %d) — the same shape "+
			"as the first such line, which was logged at error level with the full explanation. host_gate in the "+
			"diagnostics bundle's probes.json carries the running count.",
		pid))
}
