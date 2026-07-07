package shard

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestShardRoundTrip(t *testing.T) {
	want := Shard{
		ID:                 "2.1",
		Name:               "basic-turn",
		Description:        "Smallest possible round-trip.",
		Process:            "1. Send a prompt.\n2. Agent replies.",
		AcceptanceCriteria: "- Final state: `ready`\n- Tool calls: ≤ 0",
	}

	b, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got Shard
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !reflect.DeepEqual(want, got) {
		t.Fatalf("round-trip mismatch:\n want %+v\n  got %+v", want, got)
	}
	if string(b) == "" {
		t.Fatal("empty marshal")
	}
}

func TestAgentCellRoundTrip(t *testing.T) {
	want := ShardAgent{
		ScenarioID: "basic-turn",
		Metadata: ShardMetadata{
			AgentSupports:   "yes",
			Confidence:      0.85,
			AgentCLIVersion: "0.86.2",
			Notes:           "looks good",
		},
		Details: ShardDetails{
			Assessment: json.RawMessage(`{"agent_supports":"yes"}`),
			Recipe:     json.RawMessage(`{"script":[]}`),
		},
	}
	b, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got ShardAgent
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !reflect.DeepEqual(want, got) {
		t.Fatalf("round-trip mismatch:\n want %+v\n  got %+v", want, got)
	}
	// Expected / ExpectedMeta stay empty and must be omitted from the wire form.
	if got.Details.Expected != nil || got.Details.ExpectedMeta != nil {
		t.Fatalf("expected/expected_meta should be empty")
	}
	var generic map[string]any
	_ = json.Unmarshal(b, &generic)
	if _, ok := generic["details"].(map[string]any)["expected"]; ok {
		t.Fatalf("expected key should be omitted, got: %s", b)
	}
}

func TestLessShardID(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"2.9", "2.10", true},  // numeric: 9 before 10
		{"2.10", "2.9", false}, // and not the reverse
		{"1.1", "2.1", true},   // section first
		{"2.1", "1.1", false},  //
		{"3.2", "3.2", false},  // equal → not less
		{"1.5", "1.5", false},  //
		{"foo", "bar", false},  // malformed → lexical ("foo" > "bar")
		{"bar", "foo", true},   // malformed → lexical
		{"2.1", "abc", true},   // one malformed → lexical; '2'(0x32) < 'a'(0x61)
	}
	for _, c := range cases {
		if got := lessShardID(c.a, c.b); got != c.want {
			t.Errorf("lessShardID(%q,%q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

func TestSplitID(t *testing.T) {
	cases := []struct {
		id           string
		section, idx int
		ok           bool
	}{
		{"2.10", 2, 10, true},
		{"1.1", 1, 1, true},
		{"12.34", 12, 34, true},
		{"2", 0, 0, false},     // no dot
		{"a.b", 0, 0, false},   // non-numeric
		{"2.", 0, 0, false},    // empty index
		{".3", 0, 0, false},    // empty section
		{"2.3.4", 0, 0, false}, // SplitN(2) → "2","3.4"; "3.4" not an int
	}
	for _, c := range cases {
		s, i, ok := SplitID(c.id)
		if ok != c.ok || (ok && (s != c.section || i != c.idx)) {
			t.Errorf("SplitID(%q) = (%d,%d,%v), want (%d,%d,%v)", c.id, s, i, ok, c.section, c.idx, c.ok)
		}
	}
}

// writeCatalog writes replaydata/agents/scenarios.json = {"meta":..., "scenarios":[...]}.
func writeCatalog(t *testing.T, dir, body string) {
	t.Helper()
	rd := filepath.Join(dir, "replaydata")
	if err := os.MkdirAll(filepath.Join(rd, "agents"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rd, "agents", "scenarios.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLoadAllSortsByID(t *testing.T) {
	dir := t.TempDir()
	// Write out of order (2.10 then 2.9) to prove numeric sorting.
	writeCatalog(t, dir, `{"meta":{"min_versions":{}},"scenarios":[
		{"id":"2.10","name":"ten"},
		{"id":"2.9","name":"nine"}
	]}`)

	got := LoadAll(dir)
	if len(got) != 2 {
		t.Fatalf("LoadAll returned %d scenarios, want 2: %+v", len(got), got)
	}
	if got[0].ID != "2.9" || got[1].ID != "2.10" {
		t.Fatalf("sort order wrong: got [%s, %s], want [2.9, 2.10]", got[0].ID, got[1].ID)
	}

	// Load finds a single scenario by name.
	s, ok := Load(dir, "nine")
	if !ok || s.ID != "2.9" {
		t.Fatalf("Load(nine) = (%+v, %v), want id 2.9", s, ok)
	}
	if _, ok := Load(dir, "missing"); ok {
		t.Fatal("Load(missing) should report ok=false")
	}
}

func TestLoadAllMissingFile(t *testing.T) {
	if got := LoadAll(filepath.Join(t.TempDir(), "nope")); got != nil {
		t.Fatalf("LoadAll on missing file = %+v, want nil", got)
	}
}

func TestLoadMeta(t *testing.T) {
	dir := t.TempDir()

	// Missing file → empty Meta, never an error.
	if m := LoadMeta(dir); m.MinVersions != nil || m.TranscriptExtensions != nil {
		t.Fatalf("LoadMeta on missing file = %+v, want empty", m)
	}

	writeCatalog(t, dir, `{"meta":{
		"min_versions":{"aider":"0.86.0","claudecode":"2.0.0"},
		"transcript_extensions":{"aider":"md","claudecode":"jsonl"}
	},"scenarios":[]}`)
	m := LoadMeta(dir)
	if m.MinVersions["aider"] != "0.86.0" || m.MinVersions["claudecode"] != "2.0.0" {
		t.Fatalf("min_versions wrong: %+v", m.MinVersions)
	}
	if m.TranscriptExtensions["aider"] != "md" || m.TranscriptExtensions["claudecode"] != "jsonl" {
		t.Fatalf("transcript_extensions wrong: %+v", m.TranscriptExtensions)
	}

	// Malformed → empty Meta.
	writeCatalog(t, dir, `{not json`)
	if m := LoadMeta(dir); m.MinVersions != nil {
		t.Fatalf("LoadMeta on malformed = %+v, want empty", m)
	}
}

func TestAgents(t *testing.T) {
	dir := t.TempDir()

	// Empty/missing meta → empty column set.
	if got := Agents(dir); len(got) != 0 {
		t.Fatalf("Agents on missing meta = %+v, want empty", got)
	}

	// Keys returned SORTED regardless of JSON order.
	writeCatalog(t, dir, `{"meta":{"min_versions":{"pi":"0.70.0","aider":"0.86.0","codex":"0.50.0"}},"scenarios":[]}`)
	got := Agents(dir)
	want := []string{"aider", "codex", "pi"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Agents = %v, want %v", got, want)
	}
}

func TestLoadAgentCell(t *testing.T) {
	dir := t.TempDir()
	cellDir := filepath.Join(dir, "replaydata", "agents", "aider", "scenarios", "basic-turn")
	if err := os.MkdirAll(cellDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Missing file → ok=false.
	if _, ok := LoadAgentCell(dir, "aider", "basic-turn"); ok {
		t.Fatal("LoadAgentCell on missing file should return ok=false")
	}

	cell := ShardAgent{
		ScenarioID: "basic-turn",
		Metadata:   ShardMetadata{AgentSupports: "yes"},
		Details:    ShardDetails{Assessment: json.RawMessage(`{"agent_supports":"yes"}`)},
	}
	b, _ := json.MarshalIndent(cell, "", "  ")
	if err := os.WriteFile(filepath.Join(cellDir, "metadata.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}

	got, ok := LoadAgentCell(dir, "aider", "basic-turn")
	if !ok {
		t.Fatal("LoadAgentCell should return ok=true")
	}
	if got.Metadata.AgentSupports != "yes" {
		t.Fatalf("AgentSupports = %q; want yes", got.Metadata.AgentSupports)
	}
	// The loader stamps the on-disk folder name onto the cell (json:"-").
	if got.Folder != "basic-turn" {
		t.Fatalf("Folder = %q; want basic-turn", got.Folder)
	}

	// Wrong agent → ok=false.
	if _, ok := LoadAgentCell(dir, "codex", "basic-turn"); ok {
		t.Fatal("LoadAgentCell for wrong agent should return ok=false")
	}
}

// TestAgentCellDirRejectsTraversal guards the CodeQL path-injection fix:
// filepath.Base alone doesn't strip a bare ".." segment (it IS its own last
// element), so AgentCellDir must special-case it — otherwise a ".."
// adapter/folder escapes the agents/<adapter>/ sandbox by one level.
func TestAgentCellDirRejectsTraversal(t *testing.T) {
	got := AgentCellDir("/repo", "..", "x")
	if strings.Contains(got, "..") {
		t.Fatalf("AgentCellDir(%q, %q, %q) = %q; must not contain \"..\"", "/repo", "..", "x", got)
	}
	got = AgentCellDir("/repo", "aider", "..")
	if strings.Contains(got, "..") {
		t.Fatalf("AgentCellDir with folder=%q = %q; must not contain \"..\"", "..", got)
	}
}

func TestLoadAllCells(t *testing.T) {
	dir := t.TempDir()
	// Register two adapters via the catalog meta.
	writeCatalog(t, dir, `{"meta":{"min_versions":{"aider":"1.0","codex":"1.0"}},"scenarios":[]}`)

	writeCell := func(adapter, folder string, cell ShardAgent) {
		t.Helper()
		d := filepath.Join(dir, "replaydata", "agents", adapter, "scenarios", folder)
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		b, _ := json.MarshalIndent(cell, "", "  ")
		if err := os.WriteFile(filepath.Join(d, "metadata.json"), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// Both cells carry scenario_id (every cell does post-restructure); folders
	// are id-prefixed and need not match the scenario name.
	writeCell("aider", "1-1_my-scenario", ShardAgent{
		ScenarioID: "my-scenario",
		Metadata:   ShardMetadata{AgentSupports: "yes"},
	})
	writeCell("codex", "1-1_my-variant", ShardAgent{
		ScenarioID: "my-scenario",
		Metadata:   ShardMetadata{AgentSupports: "partial"},
	})

	cells := LoadAllCells(dir, "my-scenario")
	if len(cells) != 2 {
		t.Fatalf("LoadAllCells returned %d cells, want 2: %+v", len(cells), cells)
	}
	if cells["aider"].Metadata.AgentSupports != "yes" {
		t.Errorf("aider AgentSupports = %q; want yes", cells["aider"].Metadata.AgentSupports)
	}
	if cells["codex"].Metadata.AgentSupports != "partial" {
		t.Errorf("codex AgentSupports = %q; want partial", cells["codex"].Metadata.AgentSupports)
	}

	// Unknown scenario → empty map (no metadata.json with that scenario_id).
	if cells := LoadAllCells(dir, "no-such-scenario"); len(cells) != 0 {
		t.Fatalf("LoadAllCells for unknown scenario = %+v, want empty", cells)
	}
}
