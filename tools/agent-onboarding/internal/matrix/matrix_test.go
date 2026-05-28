package matrix

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// These tests are the parity golden for the bash gates the matrix model
// replaces. The fixtures and expected verdicts are ported VERBATIM from
// scripts/lib/completeness-gate_test.sh and consistency-gate_test.sh so the Go
// router is proven to reproduce the bash decision tables bit-for-bit before the
// bash bodies are gutted into thin clients.

// --- pure decision tables (no IO) ---

func TestComputeRoute(t *testing.T) {
	cases := []struct {
		s, d, drv string
		want      Route
	}{
		{"yes", "full", "ready", RouteRecordNow},
		{"no", "n/a", "n/a", RouteFrozen},
		{"yes", "incapable", "ready", RouteFrozen},
		{"yes", "full", "gap:keys", RouteDriverGap},
		{"yes", "unknown", "ready", RouteInconclusive},
		// keyless/empty axes route record_now (bash else branch).
		{"", "", "", RouteRecordNow},
		{"yes", "n.a.", "ready", RouteFrozen},
	}
	for _, c := range cases {
		if got := computeRoute(c.s, c.d, c.drv); got != c.want {
			t.Errorf("computeRoute(%q,%q,%q) = %q; want %q", c.s, c.d, c.drv, got, c.want)
		}
	}
}

func TestCellVerdict(t *testing.T) {
	cases := []struct {
		route    Route
		appl     ApplicableState
		recorded bool
		blocked  string
		want     Verdict
	}{
		{RouteRecordNow, AppFalse, false, "", VerdictContradictRecord},
		{RouteRecordNow, AppFalse, false, "infra", VerdictOK},
		{RouteRecordNow, AppTrue, false, "", VerdictOK},
		{RouteFrozen, AppTrue, false, "", VerdictContradictFrozen},
		{RouteFrozen, AppFalse, false, "", VerdictOK},
		{RouteRecordNow, AppFalse, true, "", VerdictOK}, // recorded short-circuits
		{RouteDriverGap, AppFalse, false, "", VerdictOK},
		{RouteInconclusive, AppFalse, false, "", VerdictOK},
	}
	for _, c := range cases {
		if got := CellVerdict(c.route, c.appl, c.recorded, c.blocked); got != c.want {
			t.Errorf("CellVerdict(%q,%q,%v,%q) = %q; want %q", c.route, c.appl, c.recorded, c.blocked, got, c.want)
		}
	}
}

func TestDeriveDisplayState(t *testing.T) {
	cases := []struct {
		supports, daemon, driver string
		rec                      bool
		want                     string
	}{
		{"no", "full", "ready", true, "n.a."},
		{"unknown", "full", "ready", true, "unknown"},
		{"yes", "n/a", "ready", true, "n.a."},
		{"yes", "incapable", "ready", true, "unobservable"},
		{"yes", "bug", "ready", true, "blocked-daemon"},
		{"yes", "full", "gap:keys", true, "blocked-driver"},
		{"yes", "unknown", "ready", false, "unknown"},
		{"yes", "full", "ready", false, "pending-record"},
		{"yes", "full", "ready", true, "observed"},
	}
	for _, c := range cases {
		if got := DeriveDisplayState(c.supports, c.daemon, c.driver, c.rec); got != c.want {
			t.Errorf("DeriveDisplayState(%q,%q,%q,%v) = %q; want %q", c.supports, c.daemon, c.driver, c.rec, got, c.want)
		}
	}
}

// --- loader / disposition parity (ported from completeness-gate_test.sh) ---

// completenessFixture builds the exact tree the bash completeness test uses.
func completenessFixture(t *testing.T) (scenariosPath, agentsRoot string) {
	t.Helper()
	tmp := t.TempDir()
	scenariosPath = filepath.Join(tmp, "scenarios.json")
	writeFile(t, scenariosPath, `{"scenarios":[
  {"name":"rec","coverage_id":"rec","requires":["feat_a"],"by_adapter":{"fake":{"script":[{"type":"send"}]}}},
  {"name":"gap","coverage_id":"gap","requires":["feat_a"],"by_adapter":{"fake":{"script":[{"type":"keys"}]}}},
  {"name":"frozen","coverage_id":"frozen","requires":["feat_a"],"by_adapter":{"fake":{"script":[{"type":"send"}]}}},
  {"name":"degraded","coverage_id":"degraded","requires":["feat_a"],"by_adapter":{"fake":{"applicable":false}}},
  {"name":"missed","coverage_id":"missed","requires":["feat_a"]},
  {"name":"ready-unrec","coverage_id":"ready-unrec","requires":["feat_a"],"by_adapter":{"fake":{"script":[{"type":"send"}]}}},
  {"name":"dual","coverage_id":"dual","requires":["feat_a"],"by_adapter":{"fake":{"script":[{"type":"send"}]}}},
  {"name":"dual-variant","coverage_id":"dual","requires":["feat_a"],"by_adapter":{"fake":{"script":[{"type":"send"}]}}},
  {"name":"na","coverage_id":"na","requires":["feat_x"],"by_adapter":{"fake":{"script":[{"type":"send"}]}}},
  {"name":"line-only","coverage_id":"line-only","requires":["feat_a"],"requires_transport":["line_based"],"by_adapter":{"fake":{"script":[{"type":"send"}]}}},
  {"name":"mixfreeze-a","coverage_id":"mixfreeze","requires":["feat_a"],"by_adapter":{"fake":{"applicable":false}}},
  {"name":"mixfreeze-b","coverage_id":"mixfreeze","requires":["feat_a"],"by_adapter":{"fake":{"script":[{"type":"send"}]}}},
  {"name":"divreq-a","coverage_id":"divreq","requires":["feat_a"],"by_adapter":{"fake":{"script":[{"type":"send"}]}}},
  {"name":"divreq-b","coverage_id":"divreq","requires":["feat_x"],"by_adapter":{"fake":{"script":[{"type":"send"}]}}}
]}`)

	agentsRoot = filepath.Join(tmp, "agents")
	sdir := filepath.Join(agentsRoot, "fake", "scenarios")
	writeFile(t, filepath.Join(agentsRoot, "fake", "capabilities.json"),
		`{"agent":"fake","transport":"structured_store","features":{"feat_a":true,"feat_b":true,"feat_x":false}}`)

	mkAssess := func(dir, s, d, drv string) {
		writeFile(t, filepath.Join(sdir, dir, "assessment.json"),
			`{"agent_supports":"`+s+`","daemon_capability":"`+d+`","driver_capability":"`+drv+`"}`)
	}
	record := func(dir string) {
		writeFile(t, filepath.Join(sdir, dir, "transcript.jsonl"), "{}\n")
		writeFile(t, filepath.Join(sdir, dir, "events.jsonl"), "{}\n")
	}
	mkAssess("rec", "yes", "full", "ready")
	record("rec")
	mkAssess("gap", "partial", "full", "gap:keys")
	mkAssess("frozen", "no", "n/a", "ready")
	mkAssess("degraded", "yes", "full", "ready")
	mkAssess("ready-unrec", "yes", "full", "ready")
	mkAssess("dual", "yes", "full", "ready")
	record("dual")
	mkAssess("mixfreeze", "yes", "full", "ready")
	return scenariosPath, agentsRoot
}

func TestApplicableCoverageIDs(t *testing.T) {
	scenarios, root := completenessFixture(t)
	m, err := Load(Config{ScenariosPath: scenarios, AgentsRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(m.applicableCoverageIDs("fake"), "\n")
	// dual/mixfreeze collapse; na + line-only excluded; divreq IN via its feat_a variant.
	want := "degraded\ndivreq\ndual\nfrozen\ngap\nmissed\nmixfreeze\nready-unrec\nrec"
	if got != want {
		t.Errorf("applicable set:\n got=%q\nwant=%q", got, want)
	}
}

func TestDisposition(t *testing.T) {
	scenarios, root := completenessFixture(t)
	m, err := Load(Config{ScenariosPath: scenarios, AgentsRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		cid  string
		want Disposition
	}{
		{"rec", DispRecorded},
		{"gap", DispDriverGap},
		{"frozen", DispApplicableFalse},
		{"degraded", DispApplicableFalse},
		{"missed", DispUnassessed},
		{"ready-unrec", DispAssessedNotRecord},
		{"dual", DispRecorded},
		{"mixfreeze", DispAssessedNotRecord},
	}
	for _, c := range cases {
		cell, ok := m.Cell("fake", c.cid)
		if !ok {
			t.Errorf("%s: not applicable (expected a cell)", c.cid)
			continue
		}
		if cell.Disposition != c.want {
			t.Errorf("%s disposition = %q; want %q", c.cid, cell.Disposition, c.want)
		}
	}
}

// --- consistency / Disagreements parity (ported from consistency-gate_test.sh) ---

func TestApplicableState(t *testing.T) {
	tmp := t.TempDir()
	scenarios := filepath.Join(tmp, "scenarios.json")
	writeFile(t, scenarios, `{"catalog":[{"id":"cellA"},{"id":"cellB"},{"id":"cellC"},{"id":"cellD"}],
 "scenarios":[
   {"name":"cellA","coverage_id":"cellA","requires":[],"by_adapter":{"ag":{"applicable":false}}},
   {"name":"cellB","coverage_id":"cellB","requires":[],"by_adapter":{"ag":{"applicable":true}}},
   {"name":"cellC","coverage_id":"cellC","requires":[]},
   {"name":"cellD","coverage_id":"cellD","requires":[],"by_adapter":{"ag":null}}
 ]}`)
	root := filepath.Join(tmp, "agents")
	writeFile(t, filepath.Join(root, "ag", "capabilities.json"), `{"features":{},"transport":"line_based"}`)
	m, err := Load(Config{ScenariosPath: scenarios, AgentsRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		cid  string
		want ApplicableState
	}{
		{"cellA", AppFalse},
		{"cellB", AppTrue},
		{"cellC", AppAbsent},
		// A literal-null by_adapter entry is dropped (like jq select(. != null)),
		// so the cell is absent — NOT AppTrue.
		{"cellD", AppAbsent},
	}
	for _, c := range cases {
		if got := m.applicableState("ag", c.cid); got != c.want {
			t.Errorf("applicableState(ag,%s) = %q; want %q", c.cid, got, c.want)
		}
	}
}

func TestDisagreements(t *testing.T) {
	tmp := t.TempDir()
	scenarios := filepath.Join(tmp, "scenarios.json")
	writeFile(t, scenarios, `{"catalog":[{"id":"cellA"},{"id":"cellB"},{"id":"cellC"}],
 "scenarios":[
   {"name":"cellA","coverage_id":"cellA","requires":[],"by_adapter":{"ag":{"applicable":false}}},
   {"name":"cellB","coverage_id":"cellB","requires":[],"by_adapter":{"ag":{"applicable":true}}},
   {"name":"cellC","coverage_id":"cellC","requires":[]}
 ]}`)
	root := filepath.Join(tmp, "agents")
	writeFile(t, filepath.Join(root, "ag", "capabilities.json"), `{"features":{},"transport":"line_based"}`)
	mkassess := func(cid, s, d, drv, blocked string) {
		body := `{"schema_version":1,"agent_supports":"` + s + `","daemon_capability":"` + d + `","driver_capability":"` + drv + `"`
		if blocked != "" {
			body += `,"record_blocked":"` + blocked + `"`
		}
		body += "}"
		writeFile(t, filepath.Join(root, "ag", "scenarios", cid, "assessment.json"), body)
	}

	// cellA: record_now + applicable:false → contradiction.
	// cellB: frozen + applicable:true → contradiction.
	mkassess("cellA", "yes", "full", "ready", "")
	mkassess("cellB", "no", "n/a", "n/a", "")
	load := func() *Matrix {
		m, err := Load(Config{ScenariosPath: scenarios, AgentsRoot: root})
		if err != nil {
			t.Fatal(err)
		}
		return m
	}
	dis := load().Disagreements()
	if len(dis) != 2 {
		t.Fatalf("want 2 disagreements, got %d: %+v", len(dis), dis)
	}
	joined := ""
	for _, d := range dis {
		joined += d.Message + "\n"
	}
	if !strings.Contains(joined, "ag/cellA: assessment routes RECORD") {
		t.Errorf("missing record_now flag for cellA:\n%s", joined)
	}
	if !strings.Contains(joined, "ag/cellB: scenarios.json marks by_adapter.ag applicable:true") {
		t.Errorf("missing frozen flag for cellB:\n%s", joined)
	}

	// record_blocked clears the cellA contradiction.
	mkassess("cellA", "yes", "full", "ready", "infra")
	for _, d := range load().Disagreements() {
		if d.CoverageID == "cellA" {
			t.Errorf("record_blocked=infra should clear cellA, still got: %s", d.Message)
		}
	}

	// A committed fixture clears cellB.
	mkassess("cellB", "no", "n/a", "n/a", "")
	writeFile(t, filepath.Join(root, "ag", "scenarios", "cellB", "transcript.jsonl"), "{}\n")
	writeFile(t, filepath.Join(root, "ag", "scenarios", "cellB", "events.jsonl"), "{}\n")
	if dis := load().Disagreements(); len(dis) != 0 {
		t.Errorf("all contradictions should be cleared, got: %+v", dis)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
