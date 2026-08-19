package services

// This file holds probes.json's coverage (#1534). It is a separate file from
// diagnostics_service_test.go rather than an appendix to it: that file already
// carries the bundle's shape, redaction, liveness and three generations of hook
// counters, and a fifth unrelated subject in it is a file nobody can hold in
// their head. The shared wiring (buildTestServiceWith) stays there, next to the
// service it builds.

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// buildTestServiceWithProbes is buildTestService with an explicit probe-health
// source, so a test can drive probes.json's two collection modes: nil is the
// --diagnose CLI, non-nil is the daemon.
func buildTestServiceWithProbes(t *testing.T, probeHealth func() ProbeHealthSnapshot) *DiagnosticsService {
	t.Helper()
	return buildTestServiceWith(t, nil, probeHealth)
}

// probesJSON pulls probes.json out of a freshly written bundle.
func probesJSON(t *testing.T, svc *DiagnosticsService) map[string]any {
	t.Helper()
	var buf bytes.Buffer
	if err := svc.WriteBundle(&buf); err != nil {
		t.Fatalf("WriteBundle: %v", err)
	}
	raw, ok := untar(t, buf.Bytes())["probes.json"]
	if !ok {
		t.Fatal("bundle has no probes.json")
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("probes.json is not valid JSON: %v\n%s", err, raw)
	}
	return out
}

// probeHealthFixture is a daemon snapshot with one failing probe, one healthy
// one, one memoized one and one that never ran.
func probeHealthFixture() ProbeHealthSnapshot {
	return ProbeHealthSnapshot{
		// Deliberately out of order: the view sorts, so two captures of the
		// same daemon diff cleanly instead of churning.
		Probes: []ProbeCount{
			{Probe: "plutil.bundle_id", Answered: 40, Unanswered: 2, MemoHits: 118},
			{Probe: "kitten.window"},
			{Probe: "lsof.cwd", Answered: 17},
		},
		OutcomeRule: "a child that ran to a normal exit ANSWERED; one killed, never started, or whose fork failed did not",
		// Deliberately out of order for the same reason Probes is, and
		// deliberately asymmetric: herdr starved on 2 of its 3 abandonments
		// while tmux abandoned once and starved on none. A fixture where the
		// two multiplexers agreed could not tell a per-row reading from a
		// summed one (#1558).
		ClientLoops: []ClientLoopCount{
			{Multiplexer: "tmux", CandidatesProbed: 40, AbandonedOnBudget: 1},
			{Multiplexer: "herdr", CandidatesProbed: 9, AbandonedOnBudget: 3, StarvedByScan: 2},
		},
		ClientLoopStarvationRule: "an abandonment with ZERO candidates probed in that loop was STARVED BY THE SCAN (#1529/#1558)",
		// Deliberately out of order for the same reason Probes is.
		HostGate: []HostGateOutcomeCount{
			{Outcome: "rejected.no_known_host", Count: 4},
			{Outcome: "admitted.walk_aborted", Count: 3},
			{Outcome: "admitted.host_matched", Count: 11},
			{Outcome: "admitted.process_gone", Count: 2},
			{Outcome: "admitted.not_evaluated", Count: 0},
		},
		HostGateOutcomeRule: "a walk that ran and found an allow-listed host ADMITS; a walk stopped by a child that did not ANSWER ADMITS on no evidence (#1513); a walk stopped because `ps` answered that a process in the chain no longer exists ADMITS on no evidence too, in its own row (#1574)",
	}
}

// TestProbesBundleReportsCounts covers the daemon-collected form: the numbers a
// bug report needs in order to answer "is this probe failing, and how often",
// which every issue in the #1485/#1492/#1513/#1524/#1533/#1537 family closed
// without.
func TestProbesBundleReportsCounts(t *testing.T) {
	got := probesJSON(t, buildTestServiceWithProbes(t, probeHealthFixture))

	if got["collected_from"] != "daemon" {
		t.Errorf("collected_from = %v, want \"daemon\"", got["collected_from"])
	}
	if got["total_unanswered"] != float64(2) {
		t.Errorf("total_unanswered = %v, want 2 — the total is published, not left for a reader to sum", got["total_unanswered"])
	}
	rows, _ := got["probes"].([]any)
	if len(rows) != 3 {
		t.Fatalf("probes has %d rows, want 3: %v", len(rows), got["probes"])
	}
	first, _ := rows[0].(map[string]any)
	if first["probe"] != "kitten.window" {
		t.Errorf("first row = %v, want kitten.window (sorted by probe)", first)
	}
	if first["answered"] != float64(0) || first["unanswered"] != float64(0) {
		t.Errorf("a probe that never ran must still be reported at zero, not omitted: %v", first)
	}
	// The memo column is the #1544 interaction. Folding it into "answered"
	// would inflate the denominator the non-answer rate is read against with
	// calls that started no child.
	last, _ := rows[2].(map[string]any)
	if last["probe"] != "plutil.bundle_id" || last["memo_hits"] != float64(118) || last["answered"] != float64(40) {
		t.Errorf("plutil row = %v, want answered:40 memo_hits:118 kept apart", last)
	}
	if note, _ := got["memo_note"].(string); !strings.Contains(note, "memo_hits") {
		t.Errorf("memo_note must say that hits are excluded from answered/unanswered: %q", note)
	}
	if rule, _ := got["outcome_rule"].(string); !strings.Contains(rule, "normal exit") {
		t.Errorf("outcome_rule must carry the counting rule the numbers were produced by: %q", rule)
	}
	unanswered, _ := got["unanswered_note"].(string)
	if !strings.Contains(unanswered, "plutil.bundle_id") || !strings.Contains(unanswered, "#784") {
		t.Errorf("a non-zero unanswered count must name the probe and what fails open on it: %q", unanswered)
	}
}

// TestProbesBundleReportsClientLoopsPerMultiplexer is #1558's own coverage.
//
// Three claims, and each fails on a different mutation: the rows are PER
// MULTIPLEXER (a summed pair cannot say which scan starved, and the two differ
// by more than an order of magnitude); the starvation column is separate from
// the abandonment one (they were one number, which is why the issue could not
// be decided); and the notes name both figures against their denominators.
func TestProbesBundleReportsClientLoopsPerMultiplexer(t *testing.T) {
	got := probesJSON(t, buildTestServiceWithProbes(t, probeHealthFixture))

	rows, _ := got["client_host_loops"].([]any)
	if len(rows) != 2 {
		t.Fatalf("client_host_loops has %d rows, want 2: %v — one row per multiplexer, including one that never ran", len(rows), got["client_host_loops"])
	}
	first, _ := rows[0].(map[string]any)
	if first["multiplexer"] != "herdr" {
		t.Errorf("first row = %v, want herdr (sorted by multiplexer) — two captures of one daemon must diff cleanly", first)
	}
	byMux := map[string]map[string]any{}
	for _, row := range rows {
		r, _ := row.(map[string]any)
		mux, _ := r["multiplexer"].(string)
		byMux[mux] = r
	}
	// The asymmetry IS the assertion: herdr starved twice, tmux never, and a
	// view that summed them would publish "3 starved of 4" against neither.
	if byMux["herdr"]["starved_by_scan"] != float64(2) || byMux["herdr"]["abandoned_on_budget"] != float64(3) || byMux["herdr"]["candidates_probed"] != float64(9) {
		t.Errorf("herdr row = %v, want probed:9 abandoned:3 starved:2 kept apart", byMux["herdr"])
	}
	if byMux["tmux"]["starved_by_scan"] != float64(0) || byMux["tmux"]["abandoned_on_budget"] != float64(1) {
		t.Errorf("tmux row = %v, want abandoned:1 starved:0 — an abandonment the LOOP caused is not a starvation", byMux["tmux"])
	}
	if rule, _ := got["client_host_loop_starvation_rule"].(string); !strings.Contains(rule, "ZERO candidates probed") {
		t.Errorf("client_host_loop_starvation_rule must carry the rule the rows were produced by, from the package that produced them: %q", rule)
	}

	abandon, _ := got["client_host_loop_abandonment_note"].(string)
	for _, want := range []string{"herdr abandoned 3 of 9", "tmux abandoned 1 of 40", "#1529", "deferred host-window recovery"} {
		if !strings.Contains(abandon, want) {
			t.Errorf("the abandonment note does not mention %q — it must name each multiplexer against its own denominator and say the answer fails safe: %q", want, abandon)
		}
	}
	starved, _ := got["client_host_loop_starvation_note"].(string)
	for _, want := range []string{"#1558", "herdr starved 2 of its 3", "availability, never correctness", "DISTRIBUTION"} {
		if !strings.Contains(starved, want) {
			t.Errorf("the starvation note does not mention %q — it must name the multiplexer, say what it costs, and refuse to invite tuning against a distribution nobody has: %q", want, starved)
		}
	}
	// tmux appears in the fixed legend naming both scans, which is why this
	// checks the ACCUSATION rather than the token: a note that credits every
	// multiplexer with a starvation says nothing about which one is degrading.
	if strings.Contains(starved, "tmux starved") {
		t.Errorf("the starvation note reports tmux as starved, and it starved on nothing: %q", starved)
	}
}

// TestProbesBundleStaysTerseWhenNothingIsWrong is the vacuity guard on the
// notes. A section that prints its explanation unconditionally is one whose
// explanation carries no information — a reader cannot tell a machine with a
// failing probe from a healthy one by looking at it.
func TestProbesBundleStaysTerseWhenNothingIsWrong(t *testing.T) {
	got := probesJSON(t, buildTestServiceWithProbes(t, func() ProbeHealthSnapshot {
		return ProbeHealthSnapshot{
			Probes:      []ProbeCount{{Probe: "lsof.cwd", Answered: 17}},
			OutcomeRule: "a child that ran to a normal exit ANSWERED",
			// A busy but healthy loop: candidates probed, nothing abandoned,
			// nothing starved. This is the vacuity guard for #1558's figure —
			// a run that does NOT starve must be counted by nobody as starved,
			// and its essay must not appear.
			ClientLoops: []ClientLoopCount{{Multiplexer: "herdr", CandidatesProbed: 9}},
		}
	}))

	// The rows themselves are NOT conditional and must still be published; only
	// the essays are. Asserted here rather than left to the daemon test,
	// because "healthy" is exactly the state in which an omitted zero row would
	// look like an absent producer.
	if rows, ok := got["client_host_loops"].([]any); !ok || len(rows) != 1 {
		t.Errorf("client_host_loops = %v, want the row at zero — a healthy machine publishes the figure and not its explanation", got["client_host_loops"])
	}

	for _, field := range []string{
		"unanswered_note", "undeclared_probe_kinds_note", "undeclared_probe_kinds",
		// #1558's two essays: a machine whose client loops abandoned nothing
		// gets the rows and no prose. The starvation one is the load-bearing
		// half — an explanation that fires on a healthy loop cannot report the
		// degradation the issue is about.
		"client_host_loop_abandonment_note", "client_host_loop_starvation_note",
		"undeclared_client_loop_kinds", "undeclared_client_loop_kinds_note",
		// #1525's essays follow the same rule: a machine whose gate never
		// admitted on no evidence gets the rows and no prose.
		"host_gate_aborted_walk_note", "host_gate_reconciliation_note",
		"undeclared_host_gate_outcomes", "undeclared_host_gate_outcomes_note",
	} {
		if v, ok := got[field]; ok {
			t.Errorf("%s is present on a healthy machine: %v — an explanation that always fires explains nothing", field, v)
		}
	}
	if got["total_unanswered"] != float64(0) {
		t.Errorf("total_unanswered = %v, want 0 — the FIGURE is not conditional, only its essay is", got["total_unanswered"])
	}
}

// TestProbesBundleOmitsCountsWhenNotCollectedInDaemon is the honesty
// obligation, and it is a step stronger than hooks.json's.
//
// `irrlichd --diagnose` builds its bundle in a separate process, and that
// process is NOT structurally quiet about probes: collecting processes.json
// walks every adapter's matcher through the observer, which on macOS shells out
// to pgrep and lsof. So this form's counters are small, real, and about nothing
// but its own bundle collection — publishing them under the daemon's field
// names would produce a plausible-looking measurement of something nobody
// measured, which is worse than a zero because a zero announces itself.
func TestProbesBundleOmitsCountsWhenNotCollectedInDaemon(t *testing.T) {
	got := probesJSON(t, buildTestServiceWithProbes(t, nil))

	if got["collected_from"] != "cli" {
		t.Errorf("collected_from = %v, want \"cli\"", got["collected_from"])
	}
	// Every count, not just the list. A zero here reads as an all-clear, and
	// the numbers this process could truthfully print are answers to a
	// different question.
	for _, field := range []string{
		"probes", "total_unanswered", "undeclared_probe_kinds",
		// #1558's rows and their essays, omitted for the HOST GATE's reason
		// rather than this section's — see
		// TestProbesBundleSaysWhyClientLoopCountsAreMissing.
		"client_host_loops", "client_host_loop_starvation_rule",
		"client_host_loop_abandonment_note", "client_host_loop_starvation_note",
		"undeclared_client_loop_kinds",
		// The notes that only make sense beside counts. outcome_rule is here
		// too: a counting rule printed next to no counts describes numbers this
		// section does not carry.
		"memo_note", "outcome_rule", "unanswered_note",
		// #1525's rows and their essays, omitted for a DIFFERENT reason than
		// the probe rows above — see TestProbesBundleSaysWhyHostGateCountsAreMissing.
		"host_gate", "host_gate_outcome_rule", "host_gate_aborted_walk_note",
		"host_gate_reconciliation_note", "undeclared_host_gate_outcomes",
	} {
		if v, ok := got[field]; ok {
			t.Errorf("%s is present without a daemon to read it from: %v", field, v)
		}
	}
	note, _ := got["note"].(string)
	if !strings.Contains(note, "/debug/bundle") {
		t.Errorf("note does not say where the real evidence is: %q", note)
	}
	// The reason is not the hook section's reason, and stating the wrong one
	// would teach the next reader that this process runs no probes.
	if !strings.Contains(note, "NOT zero") {
		t.Errorf("note must say that this process's counters are non-zero and describe its own collection, not that they are structurally zero: %q", note)
	}
}

// --- the #784 host-admission gate's outcomes (#1525) ------------------------
//
// These rows live in probes.json rather than a section of their own because an
// aborted walk is the DOWNSTREAM view of a probe that did not answer: the cause
// and the effect are only useful side by side, and the reconciliation between
// them is computed here. hooksView's own comment makes the same call for its
// three hook diagnoses.

// TestProbesBundleReportsHostGateOutcomes covers the daemon-collected form: the
// multi-way answer the gate reached, which before #1525 was reported by nothing
// except in the one case that rejected.
func TestProbesBundleReportsHostGateOutcomes(t *testing.T) {
	got := probesJSON(t, buildTestServiceWithProbes(t, probeHealthFixture))

	rows, _ := got["host_gate"].([]any)
	if len(rows) != 5 {
		t.Fatalf("host_gate has %d rows, want 5: %v — every declared outcome is published, including one that never happened", len(rows), got["host_gate"])
	}
	first, _ := rows[0].(map[string]any)
	if first["outcome"] != "admitted.host_matched" {
		t.Errorf("first row = %v, want admitted.host_matched (sorted by outcome)", first)
	}
	byOutcome := map[string]float64{}
	for _, row := range rows {
		r, _ := row.(map[string]any)
		outcome, _ := r["outcome"].(string)
		count, _ := r["count"].(float64)
		byOutcome[outcome] = count
	}
	// The outcomes are separate numbers, not one. A single "gate ran N times"
	// scalar is what this section exists to replace — and since #1574 that
	// includes the two admissions on no evidence, which have different causes
	// and different things to do about them.
	for outcome, want := range map[string]float64{
		"admitted.host_matched":  11,
		"admitted.walk_aborted":  3,
		"admitted.process_gone":  2,
		"rejected.no_known_host": 4,
		"admitted.not_evaluated": 0,
	} {
		if byOutcome[outcome] != want {
			t.Errorf("host_gate[%s] = %v, want %v", outcome, byOutcome[outcome], want)
		}
	}
	if rule, _ := got["host_gate_outcome_rule"].(string); !strings.Contains(rule, "#1513") {
		t.Errorf("host_gate_outcome_rule must carry the rule the rows were produced by, from the package that produced them: %q", rule)
	}

	note, _ := got["host_gate_aborted_walk_note"].(string)
	for _, want := range []string{"3 of 20", "no evidence", "#1513", "rejected.no_known_host", "host-gate"} {
		if !strings.Contains(note, want) {
			t.Errorf("the aborted-walk note does not mention %q — it must give the count against its denominator, say what an abort means, and name the OTHER row it is not: %q", want, note)
		}
	}
}

// TestProbesBundleReconcilesAbortedWalksWithProbeNonAnswers is the cross-check
// #1525 asks for, and the reason both figures had to land in one artifact.
//
// An aborted walk is caused by a bounded child that did not answer, so the two
// numbers ought to correspond. Until #1574 they could not: readProcInfo treated
// an ANSWERED "no such process" as a failure, so a walk aborted while
// ps.proc_info recorded health, and this note's job was to stop a reader
// concluding the counters were broken. Its job now is the opposite — the
// comparison is meant to hold, so an abort count with nothing under it is
// something to look at rather than something to excuse.
func TestProbesBundleReconcilesAbortedWalksWithProbeNonAnswers(t *testing.T) {
	t.Run("aborts with no recorded non-answers are no longer excused by #1574", func(t *testing.T) {
		got := probesJSON(t, buildTestServiceWithProbes(t, func() ProbeHealthSnapshot {
			return ProbeHealthSnapshot{
				Probes:   []ProbeCount{{Probe: "ps.proc_info", Answered: 900}, {Probe: "plutil.bundle_id", Answered: 40}},
				HostGate: []HostGateOutcomeCount{{Outcome: "admitted.walk_aborted", Count: 6}},
			}
		}))
		note, _ := got["host_gate_reconciliation_note"].(string)
		for _, want := range []string{"6 aborted walk(s)", "0 unanswered", "#1574", "second look", "could not parse"} {
			if !strings.Contains(note, want) {
				t.Errorf("the reconciliation note does not mention %q: %q", want, note)
			}
		}
		if strings.Contains(note, "NOT a broken counter") {
			t.Errorf("the note still tells a reader that aborts beside zero non-answers are expected — that was true only while readProcInfo mis-classified an answered \"no such process\" (#1574), and a stale reassurance here is worse than none: %q", note)
		}
	})

	t.Run("a gone-process row explains itself without a non-answer behind it", func(t *testing.T) {
		got := probesJSON(t, buildTestServiceWithProbes(t, func() ProbeHealthSnapshot {
			return ProbeHealthSnapshot{
				Probes:   []ProbeCount{{Probe: "ps.proc_info", Answered: 900}, {Probe: "plutil.bundle_id", Answered: 40}},
				HostGate: []HostGateOutcomeCount{{Outcome: "admitted.process_gone", Count: 4}},
			}
		}))
		note, _ := got["host_gate_reconciliation_note"].(string)
		for _, want := range []string{"4 admitted.process_gone", "NOT part of that comparison", "#1574"} {
			if !strings.Contains(note, want) {
				t.Errorf("a bundle whose gate admitted on gone processes and aborted on nothing must still explain the row it does carry — a reader seeing an admission row with zero non-answers underneath it and no note draws the #1574 conclusion the fix removed: %q", note)
			}
		}
	})

	t.Run("aborts beside real non-answers say what the numbers can and cannot be", func(t *testing.T) {
		got := probesJSON(t, buildTestServiceWithProbes(t, func() ProbeHealthSnapshot {
			return ProbeHealthSnapshot{
				Probes: []ProbeCount{
					{Probe: "ps.proc_info", Answered: 900, Unanswered: 5},
					{Probe: "plutil.bundle_id", Answered: 40, Unanswered: 2},
					// Not one of the two a walk can abort on: counting it here
					// would inflate the comparison with probes the gate never
					// ran.
					{Probe: "lsof.cwd", Unanswered: 99},
				},
				HostGate: []HostGateOutcomeCount{{Outcome: "admitted.walk_aborted", Count: 6}},
			}
		}))
		note, _ := got["host_gate_reconciliation_note"].(string)
		if !strings.Contains(note, "7 unanswered") {
			t.Errorf("the reconciliation note must sum only the two probes a walk can abort on (5+2), not every unanswered probe: %q", note)
		}
		if !strings.Contains(note, "not required to be equal") {
			t.Errorf("the note must say the two figures need not match, or a reader treats a mismatch as a defect: %q", note)
		}
	})
}

// TestProbesBundleSaysWhyHostGateCountsAreMissing is the honesty obligation for
// #1525's half of this section, and the reason it is a separate test from
// probes.json's own is that the true answer is DIFFERENT.
//
// `irrlichd --diagnose` genuinely runs probes — collecting processes.json
// shells out through the observer — so its probe counters are non-zero and
// irrelevant. It never builds a session detector, so it never installs the host
// gate and cannot evaluate it even once: those counters are structurally zero.
// Reporting the probe reason for both would tell a reader this process runs the
// gate; reporting the hook reason for both would be right here by accident.
func TestProbesBundleSaysWhyHostGateCountsAreMissing(t *testing.T) {
	got := probesJSON(t, buildTestServiceWithProbes(t, nil))

	note, _ := got["host_gate_note"].(string)
	if !strings.Contains(note, "structurally zero") {
		t.Errorf("the host-gate note must say these counters are structurally zero in this process — the gate is never installed here: %q", note)
	}
	if !strings.Contains(note, "DIFFERENT reason") {
		t.Errorf("the host-gate note must say it is not the probe counters' reason, or the two are read as one claim: %q", note)
	}
	if !strings.Contains(note, "/debug/bundle") {
		t.Errorf("the host-gate note does not say where the real evidence is: %q", note)
	}
	// The probe note must NOT have acquired the host gate's reason, which is
	// the mutation this pair exists to catch: one sentence covering both is
	// wrong about one of them and no test would notice.
	if probeNote, _ := got["note"].(string); !strings.Contains(probeNote, "NOT zero") {
		t.Errorf("the probe note stopped saying its own counters are non-zero here: %q", probeNote)
	}
}

// TestProbesBundleSaysWhyClientLoopCountsAreMissing is the same obligation for
// #1558's rows, and it is a third test rather than a line in the one above
// because a reader has to be told WHICH of the two reasons applies.
//
// It is the host gate's: reaching the attached-client loop needs
// ReadLauncherEnv or the liveness sweep's refreshMultiplexerHosts, and
// `irrlichd --diagnose` runs neither, so these counters are structurally zero
// here. A reader who carried the probe section's reason down — "small, real,
// about this process's own collection" — would conclude that this process
// resolves multiplexer hosts and that a zero starvation count means it found
// none.
func TestProbesBundleSaysWhyClientLoopCountsAreMissing(t *testing.T) {
	got := probesJSON(t, buildTestServiceWithProbes(t, nil))

	note, _ := got["client_host_loop_note"].(string)
	for _, want := range []string{"structurally zero", "SAME reason", "refreshMultiplexerHosts", "/debug/bundle", "#1558"} {
		if !strings.Contains(note, want) {
			t.Errorf("the client-loop note does not mention %q — it must name which of the two reasons applies, why, and where the real evidence is: %q", want, note)
		}
	}
}
