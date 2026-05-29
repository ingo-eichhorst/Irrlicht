package shard

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestShardRoundTrip(t *testing.T) {
	idle := true
	want := Shard{
		ID:          "2.1",
		Name:        "basic-turn",
		Section:     "Turn shape",
		Feature:     "Basic turn",
		Description: "Smallest possible round-trip.",
		Requires:    []string{"headless_mode"},
		Verify:      json.RawMessage(`["final_state","tool_calls_max"]`),
		IdleOnly:    &idle,
		Agents: map[string]ShardAgent{
			"aider": {
				RecordingDir: "aider/scenarios/basic-turn",
				Artifacts: ShardArtifacts{
					Events:       "aider/scenarios/basic-turn/events.jsonl",
					TranscriptMD: "aider/scenarios/basic-turn/transcript.md",
					Manifest:     "aider/scenarios/basic-turn/manifest.json",
					Recordings:   []string{"aider/scenarios/basic-turn/recordings/2026-05-25"},
				},
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
			},
		},
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

	// Expected / ExpectedMeta stay empty and must be omitted from the wire form.
	if got.Agents["aider"].Details.Expected != nil || got.Agents["aider"].Details.ExpectedMeta != nil {
		t.Fatalf("expected/expected_meta should be empty")
	}
	if string(b) == "" {
		t.Fatal("empty marshal")
	}
	var generic map[string]any
	_ = json.Unmarshal(b, &generic)
	if _, ok := generic["agents"].(map[string]any)["aider"].(map[string]any)["details"].(map[string]any)["expected"]; ok {
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
		s, i, ok := splitID(c.id)
		if ok != c.ok || (ok && (s != c.section || i != c.idx)) {
			t.Errorf("splitID(%q) = (%d,%d,%v), want (%d,%d,%v)", c.id, s, i, ok, c.section, c.idx, c.ok)
		}
	}
}

func TestLoadAllSkipsMetaAndSortsByID(t *testing.T) {
	dir := t.TempDir()
	scen := filepath.Join(dir, "replaydata", "scenarios")
	if err := os.MkdirAll(scen, 0o755); err != nil {
		t.Fatal(err)
	}

	write := func(name string, s Shard) {
		b, _ := json.MarshalIndent(s, "", "  ")
		if err := os.WriteFile(filepath.Join(scen, name), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// Write out of order (2.10 then 2.9) to prove numeric sorting.
	write("ten.json", Shard{ID: "2.10", Name: "ten", Agents: map[string]ShardAgent{}})
	write("nine.json", Shard{ID: "2.9", Name: "nine", Agents: map[string]ShardAgent{}})
	// _meta.json must be skipped even though it ends in .json.
	if err := os.WriteFile(filepath.Join(scen, "_meta.json"), []byte(`{"min_versions":{}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	// A malformed shard is skipped, not fatal.
	if err := os.WriteFile(filepath.Join(scen, "broken.json"), []byte(`{not json`), 0o644); err != nil {
		t.Fatal(err)
	}

	got := LoadAll(dir)
	if len(got) != 2 {
		t.Fatalf("LoadAll returned %d shards, want 2: %+v", len(got), got)
	}
	if got[0].ID != "2.9" || got[1].ID != "2.10" {
		t.Fatalf("sort order wrong: got [%s, %s], want [2.9, 2.10]", got[0].ID, got[1].ID)
	}

	// Load reads a single shard by name (filename minus .json).
	s, ok := Load(dir, "nine")
	if !ok || s.ID != "2.9" {
		t.Fatalf("Load(nine) = (%+v, %v), want id 2.9", s, ok)
	}
	if _, ok := Load(dir, "missing"); ok {
		t.Fatal("Load(missing) should report ok=false")
	}
	if _, ok := Load(dir, "broken"); ok {
		t.Fatal("Load(broken) should report ok=false on malformed JSON")
	}
}

func TestLoadAllMissingDir(t *testing.T) {
	if got := LoadAll(filepath.Join(t.TempDir(), "nope")); got != nil {
		t.Fatalf("LoadAll on missing dir = %+v, want nil", got)
	}
}

func TestLoadMeta(t *testing.T) {
	dir := t.TempDir()
	scen := filepath.Join(dir, "replaydata", "scenarios")
	if err := os.MkdirAll(scen, 0o755); err != nil {
		t.Fatal(err)
	}

	// Missing file → empty Meta, never an error.
	if m := LoadMeta(dir); m.MinVersions != nil || m.TranscriptExtensions != nil {
		t.Fatalf("LoadMeta on missing file = %+v, want empty", m)
	}

	body := `{"min_versions":{"aider":"0.86.0","claudecode":"2.0.0"},` +
		`"transcript_extensions":{"aider":"md","claudecode":"jsonl"}}`
	if err := os.WriteFile(filepath.Join(scen, "_meta.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	m := LoadMeta(dir)
	if m.MinVersions["aider"] != "0.86.0" || m.MinVersions["claudecode"] != "2.0.0" {
		t.Fatalf("min_versions wrong: %+v", m.MinVersions)
	}
	if m.TranscriptExtensions["aider"] != "md" || m.TranscriptExtensions["claudecode"] != "jsonl" {
		t.Fatalf("transcript_extensions wrong: %+v", m.TranscriptExtensions)
	}

	// Malformed → empty Meta.
	if err := os.WriteFile(filepath.Join(scen, "_meta.json"), []byte(`{not json`), 0o644); err != nil {
		t.Fatal(err)
	}
	if m := LoadMeta(dir); m.MinVersions != nil {
		t.Fatalf("LoadMeta on malformed = %+v, want empty", m)
	}
}

func TestAgents(t *testing.T) {
	dir := t.TempDir()
	scen := filepath.Join(dir, "replaydata", "scenarios")
	if err := os.MkdirAll(scen, 0o755); err != nil {
		t.Fatal(err)
	}

	// Empty/missing _meta → empty column set.
	if got := Agents(dir); len(got) != 0 {
		t.Fatalf("Agents on missing _meta = %+v, want empty", got)
	}

	// Keys returned SORTED regardless of JSON order.
	body := `{"min_versions":{"pi":"0.70.0","aider":"0.86.0","codex":"0.50.0"}}`
	if err := os.WriteFile(filepath.Join(scen, "_meta.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	got := Agents(dir)
	want := []string{"aider", "codex", "pi"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Agents = %v, want %v", got, want)
	}
}
