package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// TestDeriveSectionIndex mirrors annotateCatalogCodes: section numbers in
// first-appearance order (from 1); index increments within a section (from 1).
func TestDeriveSectionIndex(t *testing.T) {
	cat := []rawCatalog{
		{ID: "a", Section: "Session lifecycle"},
		{ID: "b", Section: "Session lifecycle"},
		{ID: "c", Section: "Turn shape"},
		{ID: "d", Section: "Turn shape"},
		{ID: "e", Section: "Session lifecycle"}, // back to section 1
		{ID: "f", Section: "Metrics"},
	}
	got := deriveSectionIndex(cat)
	want := []string{"1.1", "1.2", "2.1", "2.2", "1.3", "3.1"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("deriveSectionIndex = %v, want %v", got, want)
	}
}

func TestCandidateDirs(t *testing.T) {
	scenarios := []rawScenario{
		{Name: "basic-turn", CoverageID: "basic-turn"},
		{Name: "multi-turn-conversation", CoverageID: "basic-turn"},
		{Name: "unrelated", CoverageID: "other"},
	}
	got := candidateDirs("basic-turn", scenarios)
	want := []string{"basic-turn", "multi-turn-conversation"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("candidateDirs = %v, want %v", got, want)
	}
}

func TestRepresentativeVariant(t *testing.T) {
	scenarios := []rawScenario{
		{Name: "multi-turn-conversation", CoverageID: "basic-turn", Desc: "variant"},
		{Name: "basic-turn", CoverageID: "basic-turn", Desc: "named"},
	}
	// Prefer the variant whose name IS the cid even if it appears later.
	if rep := representativeVariant("basic-turn", scenarios); rep == nil || rep.Desc != "named" {
		t.Fatalf("representativeVariant should pick the cid-named variant, got %+v", rep)
	}
	// Fall back to the first coverage_id match when no name==cid exists.
	only := []rawScenario{{Name: "agent-question-pending", CoverageID: "user-blocking-question", Desc: "fallback"}}
	if rep := representativeVariant("user-blocking-question", only); rep == nil || rep.Desc != "fallback" {
		t.Fatalf("representativeVariant fallback failed, got %+v", rep)
	}
	if rep := representativeVariant("nope", only); rep != nil {
		t.Fatalf("representativeVariant for absent cid should be nil, got %+v", rep)
	}
}

// writeSyntheticRepo lays down a minimal repo: scenarios.json, two agent
// capabilities, and a recording cell where the recording lives in a VARIANT
// folder (agent-question-pending) rather than the coverage_id folder
// (user-blocking-question) — exercising the candidateDirs + recipe-by-folder
// resolution in one shot.
func writeSyntheticRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()

	scenarios := map[string]any{
		"catalog": []map[string]any{
			{"id": "basic-turn", "section": "Turn shape", "feature": "Basic turn"},
			{"id": "user-blocking-question", "section": "Turn shape", "feature": "Blocking question"},
		},
		"scenarios": []map[string]any{
			{
				"name": "basic-turn", "coverage_id": "basic-turn",
				"requires": []string{"headless_mode"},
				"verify":   []string{"final_state"},
				"description": "a basic turn",
				"by_adapter": map[string]any{
					"aider": map[string]any{"applicable": true, "script": []any{}},
					"pi":    map[string]any{"applicable": true, "script": []any{}},
				},
			},
			{
				"name": "agent-question-pending", "coverage_id": "user-blocking-question",
				"requires":    []string{"headless_mode"},
				"description": "agent asks a question",
				"by_adapter": map[string]any{
					"pi": map[string]any{"applicable": true, "script": []any{"variant-recipe"}},
				},
			},
			{
				"name": "user-blocking-question", "coverage_id": "user-blocking-question",
				"description": "blocking question (cid-named variant)",
				"by_adapter": map[string]any{
					"pi": map[string]any{"applicable": true, "script": []any{"cid-recipe"}},
				},
			},
		},
		"min_versions": map[string]string{"aider": "0.86.0", "pi": "0.70.0"},
	}
	mustWriteJSON(t, filepath.Join(root, ".claude", "skills", "ir:onboard-agent", "scenarios.json"), scenarios)

	mustWriteJSON(t, filepath.Join(root, "replaydata", "agents", "aider", "capabilities.json"),
		map[string]any{"agent": "aider", "transcript_extension": "md"})
	mustWriteJSON(t, filepath.Join(root, "replaydata", "agents", "pi", "capabilities.json"),
		map[string]any{"agent": "pi"})

	// aider basic-turn recording (transcript.md).
	aiderBT := filepath.Join(root, "replaydata", "agents", "aider", "scenarios", "basic-turn")
	mustWrite(t, filepath.Join(aiderBT, "events.jsonl"), "{}\n")
	mustWrite(t, filepath.Join(aiderBT, "transcript.md"), "# transcript\n")
	mustWriteJSON(t, filepath.Join(aiderBT, "manifest.json"),
		map[string]any{"agent_cli_version": "0.86.2", "daemon_version": "0.4.7", "expected_pass_rate": "5/5"})
	mustWriteJSON(t, filepath.Join(aiderBT, "assessment.json"),
		map[string]any{"agent_supports": "yes", "daemon_capability": "full", "driver_capability": "ready",
			"confidence": 0.85, "body": "## Verdict\n\nlooks good here"})

	// pi user-blocking-question recording lives in the VARIANT folder.
	piAQP := filepath.Join(root, "replaydata", "agents", "pi", "scenarios", "agent-question-pending")
	mustWrite(t, filepath.Join(piAQP, "events.jsonl"), "{}\n")
	mustWrite(t, filepath.Join(piAQP, "transcript.jsonl"), "{}\n")
	mustWrite(t, filepath.Join(piAQP, "transcript.jsonl.replay.json.golden"), "{}\n")
	mustWriteJSON(t, filepath.Join(piAQP, "assessment.json"),
		map[string]any{"agent_supports": "yes", "body": "pi handles it"})
	mustWrite(t, filepath.Join(piAQP, "recordings", "2026-05-25-ts", "events.jsonl"), "{}\n")

	return root
}

func TestGenerateDeterministicAndShaped(t *testing.T) {
	root := writeSyntheticRepo(t)

	first, err := generate(root)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	second, err := generate(root)
	if err != nil {
		t.Fatalf("generate (2nd): %v", err)
	}
	// Determinism: two in-memory generations must be byte-identical.
	if !reflect.DeepEqual(first, second) {
		t.Fatal("generate is not deterministic across runs")
	}

	// Exactly 2 shards + _meta.json.
	if len(first) != 3 {
		t.Fatalf("got %d files, want 3 (2 shards + _meta.json): %v", len(first), keys(first))
	}
	for _, name := range []string{"basic-turn.json", "user-blocking-question.json", "_meta.json"} {
		if _, ok := first[name]; !ok {
			t.Fatalf("missing output file %s; have %v", name, keys(first))
		}
	}

	// _meta.json shape.
	var meta metaFile
	if err := json.Unmarshal(first["_meta.json"], &meta); err != nil {
		t.Fatalf("meta unmarshal: %v", err)
	}
	if meta.TranscriptExtensions["aider"] != "md" || meta.TranscriptExtensions["pi"] != "jsonl" {
		t.Fatalf("transcript_extensions wrong: %+v", meta.TranscriptExtensions)
	}
	if meta.MinVersions["pi"] != "0.70.0" {
		t.Fatalf("min_versions wrong: %+v", meta.MinVersions)
	}

	// basic-turn shard: aider cell with recording in the basic-turn folder,
	// empty expected/expected_meta.
	var bt struct {
		ID     string `json:"id"`
		Agents map[string]struct {
			RecordingDir string `json:"recording_dir"`
			Artifacts    struct {
				TranscriptMD string `json:"transcript_md"`
			} `json:"artifacts"`
			Metadata struct {
				AgentCLIVersion string `json:"agent_cli_version"`
				Notes           string `json:"notes"`
			} `json:"metadata"`
			Details map[string]json.RawMessage `json:"details"`
		} `json:"agents"`
	}
	if err := json.Unmarshal(first["basic-turn.json"], &bt); err != nil {
		t.Fatalf("basic-turn unmarshal: %v", err)
	}
	if bt.ID != "1.1" {
		t.Fatalf("basic-turn id = %q, want 1.1", bt.ID)
	}
	aider, ok := bt.Agents["aider"]
	if !ok {
		t.Fatal("basic-turn missing aider cell")
	}
	if aider.RecordingDir != "aider/scenarios/basic-turn" {
		t.Fatalf("aider recording_dir = %q", aider.RecordingDir)
	}
	if aider.Artifacts.TranscriptMD != "aider/scenarios/basic-turn/transcript.md" {
		t.Fatalf("aider transcript_md = %q", aider.Artifacts.TranscriptMD)
	}
	if aider.Metadata.AgentCLIVersion != "0.86.2" {
		t.Fatalf("aider agent_cli_version = %q (manifest not read)", aider.Metadata.AgentCLIVersion)
	}
	if aider.Metadata.Notes != "looks good here" {
		t.Fatalf("aider notes = %q (firstParagraph wrong)", aider.Metadata.Notes)
	}
	if _, present := aider.Details["expected"]; present {
		t.Fatal("details.expected must be omitted")
	}
	if _, present := aider.Details["expected_meta"]; present {
		t.Fatal("details.expected_meta must be omitted")
	}

	// user-blocking-question shard: pi cell resolves to the variant folder and
	// the recipe comes from the variant named for that folder (variant-recipe),
	// NOT the cid-named variant (cid-recipe).
	var ubq struct {
		Agents map[string]struct {
			RecordingDir string `json:"recording_dir"`
			Artifacts    struct {
				Golden     string   `json:"golden"`
				Recordings []string `json:"recordings"`
			} `json:"artifacts"`
			Details struct {
				Recipe json.RawMessage `json:"recipe"`
			} `json:"details"`
		} `json:"agents"`
	}
	if err := json.Unmarshal(first["user-blocking-question.json"], &ubq); err != nil {
		t.Fatalf("ubq unmarshal: %v", err)
	}
	pi, ok := ubq.Agents["pi"]
	if !ok {
		t.Fatal("user-blocking-question missing pi cell")
	}
	if pi.RecordingDir != "pi/scenarios/agent-question-pending" {
		t.Fatalf("pi recording_dir = %q, want the variant folder", pi.RecordingDir)
	}
	if pi.Artifacts.Golden != "pi/scenarios/agent-question-pending/transcript.jsonl.replay.json.golden" {
		t.Fatalf("pi golden = %q", pi.Artifacts.Golden)
	}
	if len(pi.Artifacts.Recordings) != 1 || pi.Artifacts.Recordings[0] != "pi/scenarios/agent-question-pending/recordings/2026-05-25-ts" {
		t.Fatalf("pi recordings = %v", pi.Artifacts.Recordings)
	}
	if !jsonContains(pi.Details.Recipe, "variant-recipe") {
		t.Fatalf("pi recipe should come from the recording-folder variant, got: %s", pi.Details.Recipe)
	}
	if jsonContains(pi.Details.Recipe, "cid-recipe") {
		t.Fatalf("pi recipe wrongly used the cid-named variant: %s", pi.Details.Recipe)
	}
}

// helpers ------------------------------------------------------------------

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func mustWriteJSON(t *testing.T, path string, v any) {
	t.Helper()
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	mustWrite(t, path, string(b))
}

func keys(m map[string][]byte) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func jsonContains(raw json.RawMessage, needle string) bool {
	var s any
	if json.Unmarshal(raw, &s) != nil {
		return false
	}
	b, _ := json.Marshal(s)
	for i := 0; i+len(needle) <= len(b); i++ {
		if string(b[i:i+len(needle)]) == needle {
			return true
		}
	}
	return false
}
