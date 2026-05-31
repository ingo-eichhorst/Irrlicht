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
		rec, applic              bool
		want                     string
	}{
		{"no", "full", "ready", true, true, "n.a."},
		{"unknown", "full", "ready", true, true, "unknown"},
		{"yes", "n/a", "ready", true, true, "n.a."},
		{"yes", "incapable", "ready", true, true, "unobservable"},
		{"yes", "bug", "ready", true, true, "blocked-daemon"},
		{"yes", "full", "gap:keys", true, true, "blocked-driver"},
		{"yes", "unknown", "ready", false, true, "unknown"},
		{"yes", "full", "ready", false, true, "pending-record"},
		// applicable:false (record_blocked deferral), not recorded → n.a., NOT pending-record.
		{"yes", "full", "ready", false, false, "n.a."},
		{"yes", "full", "ready", true, true, "observed"},
	}
	for _, c := range cases {
		if got := DeriveDisplayState(c.supports, c.daemon, c.driver, c.rec, c.applic); got != c.want {
			t.Errorf("DeriveDisplayState(%q,%q,%q,rec=%v,applic=%v) = %q; want %q", c.supports, c.daemon, c.driver, c.rec, c.applic, got, c.want)
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

// writeShardFixture writes a t.TempDir repo with a consolidated
// replaydata/agents/scenarios.json + per-cell metadata.json files (folders id-prefixed,
// each carrying scenario_id). Returns the repo root.
func writeShardFixture(t *testing.T, agent string, shards map[string]shardFix) string {
	t.Helper()
	root := t.TempDir()
	mustWriteFile := func(path string, b []byte) {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, b, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	scenarios := []map[string]any{}
	i := 0
	for name, fx := range shards {
		i++
		id := "1." + itoa(i)
		scenarios = append(scenarios, map[string]any{
			"id": id, "name": name, "section": "Sec", "feature": name,
		})
		folder := "1-" + itoa(i) + "_" + name

		// Agent cell metadata.json (written even when minimal so the matrix sees the cell).
		cell := map[string]any{"scenario_id": name}
		if fx.recorded {
			// "Recorded" is a disk fact: a recordings/<name>/ dir under the cell.
			recDir := filepath.Join(root, "replaydata", "agents", agent, "scenarios", folder,
				"recordings", "2026-01-01-00-00-00_irrlichd-test")
			mustWriteFile(filepath.Join(recDir, "events.jsonl"), []byte("{}\n"))
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
		cb, _ := json.MarshalIndent(cell, "", "  ")
		mustWriteFile(filepath.Join(root, "replaydata", "agents", agent, "scenarios", folder, "metadata.json"), cb)
	}

	catalog := map[string]any{
		"meta":      map[string]any{"min_versions": map[string]string{agent: "1.0.0"}},
		"scenarios": scenarios,
	}
	cb, _ := json.MarshalIndent(catalog, "", "  ")
	mustWriteFile(filepath.Join(root, "replaydata", "agents", "scenarios.json"), cb)
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

// (The loader-level consistency-gate parity test TestDisagreements was removed
// in #511 along with Matrix.Disagreements: single-source shards can't disagree
// with a second file, so the consistency gate — and its model method — are gone.)
