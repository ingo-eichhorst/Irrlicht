// Package agentonboarding_test exercises the synth → gen → validate
// path end-to-end on a synthetic corpus. The unit tests in each internal
// package cover their boundaries with mocks; this file is the only place
// the full pipeline runs with each phase's real output flowing into the
// next.
//
// Run with: go test ./tools/agent-onboarding/ -run TestIntegration
package agentonboarding_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"irrlicht/tools/agent-onboarding/internal/codegen"
	"irrlicht/tools/agent-onboarding/internal/groundtruth"
	"irrlicht/tools/agent-onboarding/internal/synth"
	"irrlicht/tools/agent-onboarding/internal/validate"
)

// TestIntegration_SynthGenValidate runs the full pipeline against a
// synthetic recording shaped like a real claudecode multi-turn session:
//
//	signals.jsonl     — 3 transcript lines (session_new, user prompt, end_turn)
//	events.jsonl      — 3 state_transitions (ready / working / ready)
//	ground_truth.jsonl — 3 labels matching the transitions
//
// The test asserts:
//  1. synth produces a non-empty ruleset.json and zero conflicts
//  2. gen produces compilable Go (gofmt rejects bad output, so a
//     successful Generate() proves syntactic validity)
//  3. validate reports PASS against the events.jsonl
//
// This is the regression handhold for cross-phase contract breaks. The
// codex codegen-pid bug from review pass 1 would have surfaced here.
func TestIntegration_SynthGenValidate(t *testing.T) {
	root := t.TempDir()
	scenarioDir := filepath.Join(root, "scenario")
	if err := os.MkdirAll(scenarioDir, 0o755); err != nil {
		t.Fatal(err)
	}

	start := time.Date(2026, 5, 14, 12, 0, 0, 0, time.UTC)

	// 1. Synthetic signals.jsonl.
	signalsPath := filepath.Join(scenarioDir, "signals.jsonl")
	writeJSONL(t, signalsPath, []map[string]any{
		{"ts": start.Format(time.RFC3339Nano), "sensor": "transcript", "kind": "line",
			"payload": map[string]string{"line": `{"kind":"transcript_new"}`}},
		{"ts": start.Add(15 * time.Second).Format(time.RFC3339Nano), "sensor": "transcript", "kind": "line",
			"payload": map[string]string{"line": `{"kind":"user","content":"hi"}`}},
		{"ts": start.Add(22 * time.Second).Format(time.RFC3339Nano), "sensor": "transcript", "kind": "line",
			"payload": map[string]string{"line": `{"stop_reason":"end_turn"}`}},
	})

	// 2. Synthetic events.jsonl (the daemon's state transitions).
	writeJSONL(t, filepath.Join(scenarioDir, "events.jsonl"), []map[string]any{
		{"kind": "state_transition", "ts": start.Format(time.RFC3339Nano), "new_state": "ready"},
		{"kind": "state_transition", "ts": start.Add(15 * time.Second).Format(time.RFC3339Nano),
			"prev_state": "ready", "new_state": "working"},
		{"kind": "state_transition", "ts": start.Add(22 * time.Second).Format(time.RFC3339Nano),
			"prev_state": "working", "new_state": "ready"},
	})

	// 3. Hand-authored ground_truth.jsonl.
	gtPath := filepath.Join(scenarioDir, "ground_truth.jsonl")
	gtFile, _ := os.Create(gtPath)
	if err := groundtruth.Write(gtFile,
		groundtruth.Meta{SchemaVersion: 1, Agent: "intagent", Scenario: "test", RecordingStartedAt: start},
		[]groundtruth.Label{
			{TsOffsetMs: 0, Marker: "created", ExpectedState: "ready", ToleranceMs: 1000},
			{TsOffsetMs: 15000, Marker: "turn_start", ExpectedState: "working", ToleranceMs: 2000},
			{TsOffsetMs: 22000, Marker: "turn_done", ExpectedState: "ready", ToleranceMs: 2000},
		}); err != nil {
		t.Fatal(err)
	}
	gtFile.Close()

	// === Phase 3: synth ============================================
	stageDir := filepath.Join(root, "stage")
	if err := synth.Run(context.Background(), synth.Input{
		Agent: "intagent", Scenario: "test",
		SignalsPath: signalsPath, GroundTruth: gtPath, StagingDir: stageDir,
	}); err != nil {
		t.Fatalf("synth: %v", err)
	}

	rulesetBytes, err := os.ReadFile(filepath.Join(stageDir, "ruleset.json"))
	if err != nil {
		t.Fatalf("read ruleset: %v", err)
	}
	var rs struct {
		Rules []map[string]any `json:"rules"`
	}
	if err := json.Unmarshal(rulesetBytes, &rs); err != nil {
		t.Fatalf("unmarshal ruleset: %v", err)
	}
	if len(rs.Rules) == 0 {
		t.Fatalf("synth produced empty ruleset.json:\n%s", rulesetBytes)
	}

	conflictsBytes, _ := os.ReadFile(filepath.Join(stageDir, "synthesis_conflicts.json"))
	var conflicts struct {
		Conflicts []any `json:"conflicts"`
	}
	json.Unmarshal(conflictsBytes, &conflicts)
	if len(conflicts.Conflicts) != 0 {
		t.Errorf("synth reported %d conflicts on clean corpus:\n%s",
			len(conflicts.Conflicts), conflictsBytes)
	}

	// === Phase 4: gen ============================================
	adapterDir := filepath.Join(root, "adapter")
	driverDir := filepath.Join(root, "driver")
	if err := codegen.Generate(codegen.Input{
		Agent: "intagent", StagingDir: stageDir,
		AdapterOutDir: adapterDir, DriverOutDir: driverDir,
		DisplayName: "Integration Agent", ProcessName: "intagent",
	}); err != nil {
		t.Fatalf("codegen: %v", err)
	}
	for _, want := range []string{"agent.go", "parser.go", "pid.go", "classifier_rules.go", "parser_test.go"} {
		st, err := os.Stat(filepath.Join(adapterDir, want))
		if err != nil || st.Size() == 0 {
			t.Errorf("missing or empty: %s", want)
		}
	}
	// Generated agent.go must reference the AdapterName const we asked for.
	agentSrc, _ := os.ReadFile(filepath.Join(adapterDir, "agent.go"))
	if !strings.Contains(string(agentSrc), `AdapterName = "intagent"`) {
		t.Errorf("agent.go missing AdapterName:\n%s", agentSrc)
	}

	// === Phase 5: validate =====================================
	result, err := validate.Run(context.Background(), validate.Input{
		Agent: "intagent", Scenario: "test",
		EventsPath:  filepath.Join(scenarioDir, "events.jsonl"),
		GroundTruth: gtPath,
	})
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if !result.Pass {
		t.Errorf("validate failed on clean corpus: %+v", result.Labels)
	}
	passes := 0
	for _, l := range result.Labels {
		if l.Pass {
			passes++
		}
	}
	if passes != 3 {
		t.Errorf("expected 3/3 labels to pass, got %d", passes)
	}
}

func writeJSONL(t *testing.T, path string, rows []map[string]any) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	for _, row := range rows {
		if err := enc.Encode(row); err != nil {
			t.Fatal(err)
		}
	}
}
