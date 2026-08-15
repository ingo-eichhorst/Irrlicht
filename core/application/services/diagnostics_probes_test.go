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
		OutcomeRule:                      "a child that ran to a normal exit ANSWERED; one killed, never started, or whose fork failed did not",
		HerdrCandidatesProbed:            9,
		HerdrCandidatesAbandonedOnBudget: 3,
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
	if got["herdr_client_candidates_abandoned_on_budget"] != float64(3) {
		t.Errorf("herdr abandonment = %v, want 3 (#1558)", got["herdr_client_candidates_abandoned_on_budget"])
	}
	if got["herdr_client_candidates_probed"] != float64(9) {
		t.Errorf("herdr probed = %v, want 9 — without the denominator the abandonment count cannot be read", got["herdr_client_candidates_probed"])
	}
	if note, _ := got["herdr_abandonment_note"].(string); !strings.Contains(note, "#1529") {
		t.Errorf("a non-zero abandonment must explain itself: %q", note)
	}
}

// TestProbesBundleStaysTerseWhenNothingIsWrong is the vacuity guard on the
// notes. A section that prints its explanation unconditionally is one whose
// explanation carries no information — a reader cannot tell a machine with a
// failing probe from a healthy one by looking at it.
func TestProbesBundleStaysTerseWhenNothingIsWrong(t *testing.T) {
	got := probesJSON(t, buildTestServiceWithProbes(t, func() ProbeHealthSnapshot {
		return ProbeHealthSnapshot{
			Probes:                []ProbeCount{{Probe: "lsof.cwd", Answered: 17}},
			OutcomeRule:           "a child that ran to a normal exit ANSWERED",
			HerdrCandidatesProbed: 9,
		}
	}))

	for _, field := range []string{"unanswered_note", "herdr_abandonment_note", "undeclared_probe_kinds_note", "undeclared_probe_kinds"} {
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
		"herdr_client_candidates_probed", "herdr_client_candidates_abandoned_on_budget",
		// The notes that only make sense beside counts. outcome_rule is here
		// too: a counting rule printed next to no counts describes numbers this
		// section does not carry.
		"memo_note", "outcome_rule", "unanswered_note", "herdr_abandonment_note",
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
