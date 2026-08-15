// probecount.go counts what every bounded child process in this package did
// (issue #1534).
//
// Six issues — #1485, #1492, #1513, #1524, #1533, #1537 — each fixed one probe
// that collapsed "I could not look" into "I looked and there was nothing", and
// every one of them closed with the same footer: nothing measures how often a
// probe actually fails to answer. That is not a documentation gap. Three of
// those fixes fail OPEN — IsKnownInteractiveHost admits on a walk it could not
// complete, which is the right call on no evidence — so a probe failing
// constantly and a probe that never fails produce the SAME observable daemon:
// the #784 gate quietly stops gating and there is nothing anywhere to look at.
// A counter is what keeps that visible.
//
// Three decisions here are load-bearing, and each is a choice against an
// obvious alternative:
//
//   - COUNTED AT runProbe, which since #1547 is the only place in this package
//     that starts a child (TestEveryChildProcessRunsThroughRunProbe). One
//     counting site instead of eight, and a ninth probe is counted by existing
//     rather than by someone remembering.
//   - KEYED PER CALL SITE, not per tool. `lsof` runs on three different
//     questions here and `ps` on two; an "lsof: 4 non-answers" that cannot say
//     which question went unanswered is the single-scalar failure #1364 already
//     paid for. TestEveryProbeSiteDeclaresItsOwnKind makes a shared bucket a
//     build failure rather than a thing to notice.
//   - ANSWERED means the child RAN TO A NORMAL EXIT. See probeOutcomeRule below
//     for why that is the hard half of shellout.Answered and not each site's
//     own allowlist — this file must not acquire a second copy of any call
//     site's policy.
//
// A third outcome, memoHits, exists because #1544 put two memos in front of two
// of these probes and said so in its own hand-back: "a memo now also hides how
// often the probe runs". A `plutil` served from bundleIDMemo, or a `ps` served
// from ancestryReads, starts no child and would be invisible here. Reporting
// "N answered" while silently excluding hits is a figure that misleads in
// exactly the way this issue exists to prevent, so a hit is counted where it
// happens — in the memo — and reported beside the runs rather than folded into
// them.
package processlifecycle

import (
	"sort"
	"sync/atomic"
)

// probeKind names ONE bounded child process this package starts: the question,
// not the binary. Two kinds may run the same tool — `ps` answers both an
// ancestry read and a controlling-TTY read — and merging them would produce a
// count that cannot say which question stopped being answered.
//
// The values are stable, low-cardinality tokens: they are published in a
// diagnostics bundle a bug reporter pastes, so they are read by humans and
// diffed between captures.
type probeKind string

const (
	// probePSProcInfo is readProcInfo's `ps -o ppid=,comm=` — the per-PID read
	// both ancestry walks are built out of, and the one behind ancestryReads.
	probePSProcInfo probeKind = "ps.proc_info"
	// probePSTTY is processTTY's `ps -o tty=` (#1533).
	probePSTTY probeKind = "ps.tty"
	// probePlutilBundleID is bundleIDForAppPath's `plutil -extract
	// CFBundleIdentifier` (#1524), the one behind bundleIDMemo.
	probePlutilBundleID probeKind = "plutil.bundle_id"
	// probeLsofCWD is the observer's CWDOf `lsof -d cwd`.
	probeLsofCWD probeKind = "lsof.cwd"
	// probeLsofWriter is the observer's WriterOf `lsof <path>` (#1537).
	probeLsofWriter probeKind = "lsof.writer"
	// probeLsofHerdrClients is herdrClientPIDs' `lsof <client log>` (#1485).
	probeLsofHerdrClients probeKind = "lsof.herdr_clients"
	// probePgrepDiscover is runPgrep, behind both FindByName and FindByCmdline
	// — one run site, so one kind.
	probePgrepDiscover probeKind = "pgrep.discover"
	// probeKittenWindow is kittyWindowIDForPID's `kitten @ ls` (#1537).
	probeKittenWindow probeKind = "kitten.window"
)

// allProbeKinds is every declared kind. It is the set
// TestEveryProbeSiteDeclaresItsOwnKind grades the call sites against and the
// order ProbeCounts reports in, so a kind that stops being used, or a call site
// that names one nobody declared, is a failure rather than a silent zero.
var allProbeKinds = []probeKind{
	probePSProcInfo,
	probePSTTY,
	probePlutilBundleID,
	probeLsofCWD,
	probeLsofWriter,
	probeLsofHerdrClients,
	probePgrepDiscover,
	probeKittenWindow,
}

// probeTally is one kind's three counters.
//
// Atomics rather than a mutex, for the reason PathConfiner's fixed array of
// atomics gives: what these exist to observe is a BURST of non-answers under
// load, and instrumentation that serializes exactly when the events arrive
// together would blunt the thing it measures.
type probeTally struct {
	answered   atomic.Uint64
	unanswered atomic.Uint64
	memoHits   atomic.Uint64
}

// probeCounts maps every declared kind to its tally. The map is built once at
// init and NEVER written again — only the atomics inside it are — so it needs
// no mutex of its own. That is a property of this declaration, not a habit:
// adding an entry at runtime would make the map a shared mutable one and every
// read here a data race.
var probeCounts = func() map[probeKind]*probeTally {
	m := make(map[probeKind]*probeTally, len(allProbeKinds))
	for _, kind := range allProbeKinds {
		m[kind] = &probeTally{}
	}
	return m
}()

// undeclaredProbes counts observations naming a kind that is not in
// allProbeKinds.
//
// It exists so that a mis-wired site fails LOUDLY rather than in the one way
// this whole file is about. The two obvious alternatives are both worse: a
// panic puts a diagnostics counter on the daemon's critical path, and dropping
// the observation makes an uncounted probe indistinguishable from one that
// never ran — which is the exact silence #1534 was filed against, reproduced
// inside its own fix. TestEveryProbeSiteDeclaresItsOwnKind is what makes this
// unreachable from production; this is what happens when that guard is wrong.
var undeclaredProbes atomic.Uint64

// probeOutcomeRule records what "answered" means here, because the answer is
// NOT each call site's own verdict and the difference matters when reading the
// numbers.
//
// A probe is counted ANSWERED when the child ran to a normal exit — any status
// — and UNANSWERED when it was killed, never started, or its fork failed. That
// is shellout.Answered with no exit-code allowlist: the "hard half" its own doc
// comment describes as the part that does not differ per tool.
//
// The per-tool allowlists (lsofNothingToReport, pgrepNoMatch) are deliberately
// NOT applied here, and that is the decision rather than an oversight. An
// allowlist encodes what a particular NORMAL exit MEANS to one call site — a
// policy, per-site by construction, and #1547 kept runProbe free of exactly
// that. Folding it in would put a second copy of every site's policy here, one
// copy able to drift from the other, and a counter that disagrees with the code
// it describes is worse than no counter.
//
// What that costs, stated rather than implied: for a tool with an allowlist,
// a normal-but-not-allowlisted exit (lsof exit 2, pgrep exit 2) is counted
// ANSWERED while its call site treats it as carrying no verdict. That reading
// is still the right one for this counter's question — the probe DID run — and
// the case the fail-open gates actually degrade on, a child killed by the 2s
// ceiling under load, lands on the unanswered side in every spelling. The one
// row where the two forms disagree in shellout's own corpus is exit 152, a
// process killed under an exhausted CPU limit REPORTED THROUGH A SHELL; every
// probe here execs its binary directly, so a SIGXCPU'd child arrives as a
// signal (Exited() false) and is counted unanswered.
const probeOutcomeRule = "a child that ran to a normal exit ANSWERED; one killed, never started, or whose fork failed did not"

// observeProbe records one bounded child process and what became of it. Called
// from runProbe and nowhere else, so every probe in this package is counted by
// existing rather than by remembering.
func observeProbe(kind probeKind, err error) {
	tally, declared := probeCounts[kind]
	if !declared {
		undeclaredProbes.Add(1)
		return
	}
	if probeAnswered(err) {
		tally.answered.Add(1)
		return
	}
	tally.unanswered.Add(1)
}

// observeProbeMemoHit records one call to a probe that a memo answered without
// starting a child (#1544). Its two call sites are the two memos: bundleIDMemo
// (osutil_darwin.go) and ancestryReads (osutil.go).
//
// It is a separate outcome rather than an "answered", because those two facts
// are used for different things and merging them loses both: answered +
// unanswered is how often a CHILD RAN, which is the denominator the non-answer
// rate is a fraction of, while memoHits is how much work the memos removed.
func observeProbeMemoHit(kind probeKind) {
	tally, declared := probeCounts[kind]
	if !declared {
		undeclaredProbes.Add(1)
		return
	}
	tally.memoHits.Add(1)
}

// herdrCandidates counts the two ways resolveClientHostIdentityVia's loop can
// end for a candidate: it probed one, or it abandoned the rest on the aggregate
// budget (#1529's herdrClientBudget).
//
// This is #1558, and it is counted HERE rather than deferred because that issue
// says so in its own words: "#1534 is that nothing counts probe non-answers,
// and that now covers budget abandonment too, so this degradation is invisible
// by construction", with "count it first" named as the cheapest option and the
// measurement every other option needs.
//
// It is NOT a probe row and is deliberately kept off the (kind, outcome) axis:
// no child process is involved, so calling it "unanswered" would put a
// different kind of fact in a column whose meaning is fixed by
// probeOutcomeRule. It is published beside them instead.
//
// probed is the denominator, and it is the half #1558 does not ask for. Without
// it "12 abandoned" cannot be read at all — 12 out of 12 and 12 out of 10,000
// are different findings and a single scalar cannot tell them apart.
var herdrCandidates struct {
	probed            atomic.Uint64
	abandonedOnBudget atomic.Uint64
}

// observeHerdrCandidateProbed records one candidate the loop actually asked
// about.
func observeHerdrCandidateProbed() { herdrCandidates.probed.Add(1) }

// observeHerdrCandidatesAbandoned records ONE abandonment event — the loop
// stopping because the aggregate budget was gone — not one per candidate left
// unread. The loop breaks on the first expired check, so it never learns how
// many were left; counting the event is the fact it actually has.
func observeHerdrCandidatesAbandoned() { herdrCandidates.abandonedOnBudget.Add(1) }

// ProbeCount is one probe kind's counters, as the diagnostics bundle reports
// them. Exported for the composition root, which converts it into the
// application layer's own shape — application/services must not import an
// inbound adapter (core/architecture_test.go), the same seam
// hookjson.UnknownEvents crosses.
type ProbeCount struct {
	// Probe is the kind token, e.g. "lsof.herdr_clients".
	Probe string
	// Answered is how many children ran to a normal exit.
	Answered uint64
	// Unanswered is how many were killed, never started, or failed to fork —
	// the number every issue in this family closed without.
	Unanswered uint64
	// MemoHits is how many calls a memo answered without starting a child. Only
	// two kinds have a memo (ps.proc_info, plutil.bundle_id); for the rest this
	// is structurally zero, which is a finding of nothing rather than an
	// inability to look.
	MemoHits uint64
}

// ProbeCounts snapshots every declared kind, in allProbeKinds order.
//
// Every kind is returned, including one that has never run: a probe that
// produced nothing at all is itself a finding — on a machine where the daemon
// never resolves a host, or where a tool is missing, the ZERO row is the
// evidence — and omitting it would leave a reader unable to tell "never ran"
// from "this build has no such probe".
func ProbeCounts() []ProbeCount {
	out := make([]ProbeCount, 0, len(allProbeKinds))
	for _, kind := range allProbeKinds {
		tally := probeCounts[kind]
		out = append(out, ProbeCount{
			Probe:      string(kind),
			Answered:   tally.answered.Load(),
			Unanswered: tally.unanswered.Load(),
			MemoHits:   tally.memoHits.Load(),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Probe < out[j].Probe })
	return out
}

// UndeclaredProbeKinds is how many observations named a kind nobody declared.
// Non-zero means ProbeCounts is INCOMPLETE and something is wired wrong; it is
// published rather than left to be inferred from rows that do not add up.
func UndeclaredProbeKinds() uint64 { return undeclaredProbes.Load() }

// HerdrCandidateCounts is the herdr client loop's two figures (#1558).
type HerdrCandidateCounts struct {
	// Probed is how many attached-client candidates the loop asked about.
	Probed uint64
	// AbandonedOnBudget is how many times the loop stopped early because
	// herdrClientBudget was already spent — one per event, not per candidate.
	AbandonedOnBudget uint64
}

// HerdrCandidates snapshots that loop.
func HerdrCandidates() HerdrCandidateCounts {
	return HerdrCandidateCounts{
		Probed:            herdrCandidates.probed.Load(),
		AbandonedOnBudget: herdrCandidates.abandonedOnBudget.Load(),
	}
}

// ProbeOutcomeRule is probeOutcomeRule, exported so the diagnostics bundle
// prints the definition it is actually counting by rather than a second copy
// of it written in the application layer — the drift "a number which documents
// behaviour but is not produced by it" describes, one layer up.
func ProbeOutcomeRule() string { return probeOutcomeRule }
