package matrix

import (
	"encoding/json"
	"os"
	"path/filepath"
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

// --- loader / disposition parity (shard-backed; ports completeness-gate_test.sh) ---

// shardFix is one (agent, scenario) cell in a shard fixture.
type shardFix struct {
	assessment string // raw assessment JSON (empty → no assessment blob)
	recipe     string // raw recipe JSON   (empty → no recipe blob)
	recorded   bool   // synthesize events + transcript artifacts
}

// writeShardFixture writes a t.TempDir repo with replaydata/scenarios shards +
// _meta.json so matrix.LoadRepo reads the shard layout (P2). Each cell is a
// (recipe, assessment, recorded) triple. Returns the repo root.
func writeShardFixture(t *testing.T, agent string, shards map[string]shardFix) string {
	t.Helper()
	root := t.TempDir()
	scen := filepath.Join(root, "replaydata", "scenarios")
	if err := os.MkdirAll(scen, 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite := func(name string, b []byte) {
		if err := os.WriteFile(filepath.Join(scen, name), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	meta, _ := json.Marshal(map[string]any{"min_versions": map[string]string{agent: "1.0.0"}})
	mustWrite("_meta.json", meta)

	i := 0
	for name, fx := range shards {
		i++
		cell := map[string]any{}
		if fx.recorded {
			cell["recording_dir"] = agent + "/scenarios/" + name
			cell["artifacts"] = map[string]any{
				"events":     agent + "/scenarios/" + name + "/events.jsonl",
				"transcript": agent + "/scenarios/" + name + "/transcript.jsonl",
			}
		}
		details := map[string]any{}
		if fx.assessment != "" {
			details["assessment"] = json.RawMessage(fx.assessment)
		}
		if fx.recipe != "" {
			details["recipe"] = json.RawMessage(fx.recipe)
		}
		if len(details) > 0 {
			cell["details"] = details
		}
		sh := map[string]any{
			"id":      "1." + itoa(i),
			"name":    name,
			"section": "Sec",
			"feature": name,
			"agents":  map[string]any{agent: cell},
		}
		b, _ := json.MarshalIndent(sh, "", "  ")
		mustWrite(name+".json", b)
	}
	return root
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

// TestDisposition is the loader-level parity test: each fixture cell's
// disposition must match the bash completeness gate, now reading the shard
// layout. Same outcomes as the legacy fixture — only the storage changed.
func TestDisposition(t *testing.T) {
	root := writeShardFixture(t, "testagent", map[string]shardFix{
		// recorded + record_now assessment → recorded.
		"scenario-a": {
			assessment: `{"agent_supports":"yes","daemon_capability":"full","driver_capability":"ready"}`,
			recorded:   true,
		},
		// assessed record_now, NOT recorded → assessed_not_recorded.
		"scenario-b": {
			assessment: `{"agent_supports":"yes","daemon_capability":"full","driver_capability":"ready"}`,
		},
		// frozen assessment + recipe applicable:false → applicable_false.
		"scenario-c": {
			assessment: `{"agent_supports":"no","daemon_capability":"n/a","driver_capability":"n/a"}`,
			recipe:     `{"applicable":false}`,
		},
		// driver gap → driver_gap.
		"scenario-gap": {
			assessment: `{"agent_supports":"partial","daemon_capability":"full","driver_capability":"gap:keys"}`,
		},
		// no assessment, not recorded → unassessed.
		"scenario-d": {},
	})
	m, err := LoadRepo(root)
	if err != nil {
		t.Fatalf("LoadRepo: %v", err)
	}

	cases := []struct {
		cid  string
		want Disposition
	}{
		{"scenario-a", DispRecorded},
		{"scenario-b", DispAssessedNotRecord},
		{"scenario-c", DispApplicableFalse},
		{"scenario-gap", DispDriverGap},
		{"scenario-d", DispUnassessed},
	}
	for _, c := range cases {
		cs, ok := m.Cell("testagent", c.cid)
		if !ok {
			t.Errorf("Cell(testagent,%q) missing", c.cid)
			continue
		}
		if cs.Disposition != c.want {
			t.Errorf("disposition[%s] = %q; want %q", c.cid, cs.Disposition, c.want)
		}
	}
}

// TestApplicableState exercises the shard-backed applicableState: recipe
// applicable:false → AppFalse; applicable:true/absent → AppTrue; no recipe →
// AppAbsent; absent agent → AppAbsent.
func TestApplicableState(t *testing.T) {
	root := writeShardFixture(t, "ag", map[string]shardFix{
		"cellA": {recipe: `{"applicable":false}`},
		"cellB": {recipe: `{"applicable":true}`},
		"cellC": {recipe: `{"script":[]}`}, // recipe present, applicable absent → AppTrue
		"cellD": {},                        // no recipe → AppAbsent
	})
	m, err := LoadRepo(root)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		cid  string
		want ApplicableState
	}{
		{"cellA", AppFalse},
		{"cellB", AppTrue},
		{"cellC", AppTrue},
		{"cellD", AppAbsent},
	}
	for _, c := range cases {
		if got := m.applicableState("ag", c.cid); got != c.want {
			t.Errorf("applicableState(ag,%s) = %q; want %q", c.cid, got, c.want)
		}
	}
}

// TestDisagreements is the loader-level parity test for the consistency gate:
// one FROZEN contradiction (frozen assessment + recipe applicable:true) and one
// RECORD contradiction (record_now assessment + recipe applicable:false, no
// record_blocked), now read from the shard layout. A record_blocked reason
// clears the RECORD contradiction; a recording clears the FROZEN one.
func TestDisagreements(t *testing.T) {
	build := func(blockedA bool, recordB bool) *Matrix {
		assessA := `{"schema_version":1,"agent_supports":"yes","daemon_capability":"full","driver_capability":"ready"`
		if blockedA {
			assessA += `,"record_blocked":"infra"`
		}
		assessA += "}"
		cellB := shardFix{
			assessment: `{"schema_version":1,"agent_supports":"no","daemon_capability":"n/a","driver_capability":"n/a"}`,
			recipe:     `{"applicable":true}`, // AppTrue + frozen route → FROZEN contradiction
		}
		if recordB {
			cellB.recorded = true
		}
		root := writeShardFixture(t, "ag", map[string]shardFix{
			// record_now + recipe applicable:false → CONTRADICTION_RECORD_NOW
			// (unless record_blocked is set).
			"cellA": {assessment: assessA, recipe: `{"applicable":false}`},
			// frozen + recipe applicable:true → CONTRADICTION_FROZEN
			// (unless recorded).
			"cellB": cellB,
		})
		m, err := LoadRepo(root)
		if err != nil {
			t.Fatal(err)
		}
		return m
	}

	dis := build(false, false).Disagreements()
	if len(dis) != 2 {
		t.Fatalf("want 2 disagreements, got %d: %+v", len(dis), dis)
	}
	var foundRecord, foundFrozen bool
	for _, d := range dis {
		switch d.Verdict {
		case VerdictContradictRecord:
			foundRecord = true
		case VerdictContradictFrozen:
			foundFrozen = true
		}
	}
	if !foundRecord {
		t.Errorf("missing RECORD contradiction for cellA: %+v", dis)
	}
	if !foundFrozen {
		t.Errorf("missing FROZEN contradiction for cellB: %+v", dis)
	}

	// record_blocked clears cellA's RECORD contradiction.
	for _, d := range build(true, false).Disagreements() {
		if d.CoverageID == "cellA" {
			t.Errorf("record_blocked should clear cellA, still got: %s", d.Message)
		}
	}

	// A recording clears cellB's FROZEN contradiction; with cellA also blocked,
	// all contradictions are gone.
	if dis := build(true, true).Disagreements(); len(dis) != 0 {
		t.Errorf("all contradictions should be cleared, got: %+v", dis)
	}
}
