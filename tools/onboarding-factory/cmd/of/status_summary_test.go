package main

import (
	"encoding/json"
	"strings"
	"testing"

	"irrlicht/tools/onboarding-factory/internal/matrix"
)

// TestStatusSummaryCounts pins the per-agent bucket counts against richRepo's
// documented cell layout. The counts ARE the product of `--summary`, so they
// are written out literally here rather than recomputed from the matrix — a
// test that re-derives them would pass against any derivation, correct or not.
func TestStatusSummaryCounts(t *testing.T) {
	root := richRepo(t)
	code, out, errs := runOf("status", "--summary", "--json", "--repo-root", root)
	if code != exitOK {
		t.Fatalf("exit=%d stderr=%s", code, errs)
	}
	var v summaryView
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		t.Fatalf("bad json: %v\n%s", err, out)
	}

	// richRepo has no adapters.json and no core scenarios, so every row
	// carries an empty maturity claim, earns `planned`, and settles 0 of the
	// core 12 (#1369). That is the correct reading of a catalog the maturity
	// model does not apply to — not a defect in the fixture.
	const core = 12
	want := map[string]agentSummary{
		"aider":      {Agent: "aider", Recorded: 1, Total: 1, Earned: matrix.MaturityPlanned, CoreTotal: core},
		"claudecode": {Agent: "claudecode", Recorded: 1, Pending: 1, Blocked: 2, Unobservable: 1, NotApplicable: 1, Unknown: 1, Total: 7, Earned: matrix.MaturityPlanned, CoreTotal: core},
		"codex":      {Agent: "codex", Recorded: 1, Pending: 1, Total: 2, Earned: matrix.MaturityPlanned, CoreTotal: core},
		"hermes":     {Agent: "hermes", Recorded: 1, Total: 1, Earned: matrix.MaturityPlanned, CoreTotal: core},
	}
	if len(v.Agents) != len(want) {
		t.Fatalf("want %d agent rows, got %d: %+v", len(want), len(v.Agents), v.Agents)
	}
	for _, got := range v.Agents {
		w, ok := want[got.Agent]
		if !ok {
			t.Fatalf("unexpected agent row %q", got.Agent)
		}
		if got != w {
			t.Errorf("%s: got %+v want %+v", got.Agent, got, w)
		}
	}

	wantTotal := agentSummary{Agent: "total", Recorded: 4, Pending: 2, Blocked: 2, Unobservable: 1, NotApplicable: 1, Unknown: 1, Total: 11, CoreTotal: core * len(want)}
	if v.Total != wantTotal {
		t.Errorf("total: got %+v want %+v", v.Total, wantTotal)
	}
}

// TestStatusSummaryAgreesWithFullDump is the real anti-lossy assertion: it
// compares the summary against the OTHER command's output rather than against
// itself. The skill's warning is that "lossy summaries drop cells", and only a
// cross-check against the full per-cell dump can detect a dropped cell — a
// self-consistency check cannot, because a dropped cell lowers the buckets and
// the total together.
func TestStatusSummaryAgreesWithFullDump(t *testing.T) {
	root := richRepo(t)

	code, dumpOut, errs := runOf("status", "--json", "--repo-root", root)
	if code != exitOK {
		t.Fatalf("full dump exit=%d stderr=%s", code, errs)
	}
	var dump statusView
	if err := json.Unmarshal([]byte(dumpOut), &dump); err != nil {
		t.Fatalf("bad dump json: %v", err)
	}
	perAgent := map[string]int{}
	cells := 0
	for _, sv := range dump.Scenarios {
		for agent := range sv.Cells {
			perAgent[agent]++
			cells++
		}
	}

	code, sumOut, errs := runOf("status", "--summary", "--json", "--repo-root", root)
	if code != exitOK {
		t.Fatalf("summary exit=%d stderr=%s", code, errs)
	}
	var v summaryView
	if err := json.Unmarshal([]byte(sumOut), &v); err != nil {
		t.Fatalf("bad summary json: %v", err)
	}
	if v.Total.Total != cells {
		t.Errorf("summary counts %d cells, the full dump has %d", v.Total.Total, cells)
	}
	for _, a := range v.Agents {
		if a.Total != perAgent[a.Agent] {
			t.Errorf("%s: summary counts %d cells, full dump has %d", a.Agent, a.Total, perAgent[a.Agent])
		}
	}
}

// TestStatusRejectsUnknownScenario pins the guard added alongside --summary:
// an unmatched --scenario must fail loudly rather than render a table of
// zeros that reads as a real measurement.
func TestStatusRejectsUnknownScenario(t *testing.T) {
	root := richRepo(t)
	for _, args := range [][]string{
		{"status", "--scenario", "bogus", "--repo-root", root},
		{"status", "--summary", "--scenario", "bogus", "--repo-root", root},
	} {
		code, out, errs := runOf(args...)
		if code != exitUsage {
			t.Errorf("%v: want exit %d, got %d (stdout=%q stderr=%q)", args, exitUsage, code, out, errs)
		}
	}
	// A real scenario id still works, so the guard accepts both spellings.
	if code, _, errs := runOf("status", "--summary", "--scenario", "1.1", "--repo-root", root); code != exitOK {
		t.Errorf("scenario by id should be accepted: exit=%d stderr=%s", code, errs)
	}
}

// TestStatusSummaryText checks the human-readable rendering, which is the form
// the skill and a human at a terminal actually read.
func TestStatusSummaryText(t *testing.T) {
	root := richRepo(t)
	code, out, errs := runOf("status", "--summary", "--repo-root", root)
	if code != exitOK {
		t.Fatalf("exit=%d stderr=%s", code, errs)
	}
	for _, want := range []string{"recorded", "blocked", "unobservable", "claudecode", "total"} {
		if !strings.Contains(out, want) {
			t.Errorf("summary text missing %q:\n%s", want, out)
		}
	}
	// The claudecode row must carry its seven cells.
	var row string
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "claudecode") {
			row = line
		}
	}
	if row == "" {
		t.Fatalf("no claudecode row:\n%s", out)
	}
	// The cell total is no longer last: #1369 appended maturity / earned /
	// core columns after it. Assert its position from the header rather than
	// from the end, so a future column does not silently move the assertion.
	fields := strings.Fields(row)
	if len(fields) < 4 || fields[len(fields)-4] != "7" {
		t.Errorf("claudecode row should carry its 7-cell total before the maturity columns, got %q", row)
	}
	if fields[len(fields)-1] != "0/12" {
		t.Errorf("claudecode row should settle 0 of the core 12, got %q", row)
	}
}

// TestStatusSummaryAgentFilter confirms --summary composes with --agent rather
// than silently ignoring it.
func TestStatusSummaryAgentFilter(t *testing.T) {
	root := richRepo(t)
	code, out, errs := runOf("status", "--summary", "--agent", "codex", "--json", "--repo-root", root)
	if code != exitOK {
		t.Fatalf("exit=%d stderr=%s", code, errs)
	}
	var v summaryView
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		t.Fatalf("bad json: %v\n%s", err, out)
	}
	if len(v.Agents) != 1 || v.Agents[0].Agent != "codex" {
		t.Fatalf("agent filter not applied: %+v", v.Agents)
	}
	if v.Total.Total != 2 {
		t.Errorf("filtered total should count only codex's 2 cells, got %d", v.Total.Total)
	}
}

// TestStatusAndSummaryAgreeOnNotApplicableSpelling is #1367's headline
// regression test. `of status` prints a cell's display state verbatim, while
// `of status --summary` buckets that same state under a column header — and
// the two used different spellings ("n.a." vs "n/a") for the identical state, retired-spelling-ok
// which quietly doubles every downstream grep and comparison.
//
// It reads the token out of `of status` rather than hard-coding it on that
// side, so the assertion is "the two commands agree", independent of which
// spelling wins; the separate literal check below is what pins WHICH one.
func TestStatusAndSummaryAgreeOnNotApplicableSpelling(t *testing.T) {
	root := richRepo(t)

	// richRepo's claudecode 6-1 cell has agent_supports="no" — the
	// not-applicable route.
	code, dumpOut, errs := runOf("status", "--json", "--repo-root", root)
	if code != exitOK {
		t.Fatalf("status --json exit=%d stderr=%s", code, errs)
	}
	var dump statusView
	if err := json.Unmarshal([]byte(dumpOut), &dump); err != nil {
		t.Fatalf("bad json: %v\n%s", err, dumpOut)
	}
	token := displayStateOf(dump, "6.1", "claudecode")
	if token == "" {
		t.Fatalf("no claudecode cell for scenario 6.1 in:\n%s", dumpOut)
	}

	code, sumOut, errs := runOf("status", "--summary", "--repo-root", root)
	if code != exitOK {
		t.Fatalf("status --summary exit=%d stderr=%s", code, errs)
	}
	headerLine := ""
	for _, l := range strings.Split(sumOut, "\n") {
		if strings.HasPrefix(l, "agent") {
			headerLine = l
			break
		}
	}
	if headerLine == "" {
		t.Fatalf("no column-header row in the summary:\n%s", sumOut)
	}
	if !strings.Contains(headerLine, token) {
		t.Errorf("`of status` renders the not-applicable state as %q but `of status --summary`'s header column does not use that token.\nheader: %q\nThe two commands must spell one state one way (#1367).", token, headerLine)
	}

	// And the agreed token is the canonical slash spelling, not the retired
	// dotted one.
	if token != "n/a" {
		t.Errorf("display state for a supports=no cell = %q; #1367 canonicalised it to %q", token, "n/a")
	}
	retired := "n.a." // retired-spelling-ok
	if strings.Contains(sumOut, retired) {
		t.Errorf("summary output still contains the retired %q spelling:\n%s", retired, sumOut)
	}
	textCode, statusText, textErrs := runOf("status", "--repo-root", root)
	if textCode != exitOK {
		t.Fatalf("status (text) exit=%d stderr=%s", textCode, textErrs)
	}
	if strings.Contains(statusText, retired) {
		t.Errorf("`of status` text output still contains the retired %q spelling:\n%s", retired, statusText)
	}
}

// displayStateOf pulls one cell's display state out of an `of status --json`
// dump.
func displayStateOf(dump statusView, scenarioID, agent string) string {
	for _, sv := range dump.Scenarios {
		if sv.ID == scenarioID {
			return sv.Cells[agent].DisplayState
		}
	}
	return ""
}

// TestStatusSummaryMaturityColumns covers the non-degenerate case the richRepo
// fixture cannot: richRepo has no adapters.json and no core scenarios, so
// every maturity assertion there pins the all-empty form and a wiring that
// returned constants would pass. maturityRepo (validate_maturity_test.go, same
// package) is the fixture with real claims — claudecode stable at 12/12, codex
// alpha at 4/12 with no metrics cell at all.
func TestStatusSummaryMaturityColumns(t *testing.T) {
	root := maturityRepo(t)
	code, out, errs := runOf("status", "--summary", "--json", "--repo-root", root)
	if code != exitOK {
		t.Fatalf("exit=%d stderr=%s", code, errs)
	}
	var v summaryView
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		t.Fatalf("bad json: %v\n%s", err, out)
	}
	want := map[string]struct {
		maturity, earned string
		settled          int
	}{
		"claudecode": {"stable", "stable", 12},
		"codex":      {"alpha", "alpha", 4},
	}
	if len(v.Agents) != len(want) {
		t.Fatalf("want %d rows, got %d: %+v", len(want), len(v.Agents), v.Agents)
	}
	for _, got := range v.Agents {
		w, ok := want[got.Agent]
		if !ok {
			t.Fatalf("unexpected row %q", got.Agent)
		}
		if got.Maturity != w.maturity || got.Earned != w.earned || got.CoreSettled != w.settled || got.CoreTotal != 12 {
			t.Errorf("%s: got maturity=%q earned=%q core=%d/%d, want %q/%q %d/12",
				got.Agent, got.Maturity, got.Earned, got.CoreSettled, got.CoreTotal,
				w.maturity, w.earned, w.settled)
		}
	}
	if v.Total.CoreSettled != 16 || v.Total.CoreTotal != 24 {
		t.Errorf("total core = %d/%d, want 16/24", v.Total.CoreSettled, v.Total.CoreTotal)
	}
}
