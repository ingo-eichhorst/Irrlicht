package synth

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"irrlicht/tools/agent-onboarding/internal/groundtruth"
)

func TestRun_emitsRulesetAndConflictReportForSeedShape(t *testing.T) {
	dir := t.TempDir()
	signalsPath := filepath.Join(dir, "signals.jsonl")
	gtPath := filepath.Join(dir, "ground_truth.jsonl")
	stagingDir := filepath.Join(dir, "staging")

	start := time.Date(2026, 5, 14, 12, 0, 0, 0, time.UTC)

	// Synthetic signals: one transcript line per labeled point.
	signals := []Signal{
		{Ts: start, Sensor: "transcript", Kind: "line", Payload: mustJSON(t, map[string]string{"line": `{"kind":"transcript_new"}`})},
		{Ts: start.Add(15 * time.Second), Sensor: "transcript", Kind: "line", Payload: mustJSON(t, map[string]string{"line": `{"kind":"transcript_activity"}`})},
		{Ts: start.Add(22 * time.Second), Sensor: "transcript", Kind: "line", Payload: mustJSON(t, map[string]string{"line": `{"stop_reason":"end_turn"}`})},
	}
	if err := writeJSONL(signalsPath, signals); err != nil {
		t.Fatal(err)
	}

	meta := groundtruth.Meta{SchemaVersion: 1, Agent: "claudecode", Scenario: "test", RecordingStartedAt: start}
	labels := []groundtruth.Label{
		{TsOffsetMs: 0, Marker: "session_created", ExpectedState: "ready", ToleranceMs: 1000},
		{TsOffsetMs: 15000, Marker: "turn_start", ExpectedState: "working", ToleranceMs: 2000},
		{TsOffsetMs: 22000, Marker: "turn_done", ExpectedState: "ready", ToleranceMs: 2000},
	}
	{
		f, err := os.Create(gtPath)
		if err != nil {
			t.Fatal(err)
		}
		if err := groundtruth.Write(f, meta, labels); err != nil {
			t.Fatal(err)
		}
		f.Close()
	}

	err := Run(context.Background(), Input{
		Agent: "claudecode", Scenario: "test",
		SignalsPath: signalsPath, GroundTruth: gtPath, StagingDir: stagingDir,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Ruleset should have at least one rule per labeled point.
	var rs Ruleset
	loadJSON(t, filepath.Join(stagingDir, "ruleset.json"), &rs)
	if len(rs.Rules) < 1 {
		t.Errorf("expected at least 1 rule, got %d", len(rs.Rules))
	}

	// Conflict report should be empty for this clean corpus.
	var cr ConflictReport
	loadJSON(t, filepath.Join(stagingDir, "synthesis_conflicts.json"), &cr)
	if len(cr.Conflicts) != 0 {
		t.Errorf("expected no conflicts on clean corpus, got %d: %+v", len(cr.Conflicts), cr.Conflicts)
	}

	// Driver protocol exists and has the turn-done signal we'd expect.
	var dp DriverProtocol
	loadJSON(t, filepath.Join(stagingDir, "driver_protocol.json"), &dp)
	if dp.TurnDoneSignal == "" {
		t.Errorf("driver protocol should detect a turn-done signal: %+v", dp)
	}
}

func TestRun_reportsConflictsWhenSignalsMissing(t *testing.T) {
	dir := t.TempDir()
	signalsPath := filepath.Join(dir, "signals.jsonl")
	gtPath := filepath.Join(dir, "ground_truth.jsonl")
	stagingDir := filepath.Join(dir, "staging")

	// Empty signals.
	os.WriteFile(signalsPath, []byte(""), 0o644)

	meta := groundtruth.Meta{SchemaVersion: 1, Agent: "x", Scenario: "y"}
	labels := []groundtruth.Label{
		{TsOffsetMs: 0, Marker: "a", ExpectedState: "ready"},
	}
	f, _ := os.Create(gtPath)
	groundtruth.Write(f, meta, labels)
	f.Close()

	if err := Run(context.Background(), Input{
		Agent: "x", Scenario: "y", SignalsPath: signalsPath, GroundTruth: gtPath, StagingDir: stagingDir,
	}); err != nil {
		t.Fatal(err)
	}

	var cr ConflictReport
	loadJSON(t, filepath.Join(stagingDir, "synthesis_conflicts.json"), &cr)
	if len(cr.Conflicts) != 1 || cr.Conflicts[0].Marker != "a" {
		t.Errorf("expected 1 conflict on `a`, got %+v", cr.Conflicts)
	}
}

func writeJSONL(path string, signals []Signal) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	for _, s := range signals {
		if err := enc.Encode(s); err != nil {
			return err
		}
	}
	return nil
}

func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func loadJSON(t *testing.T, path string, into any) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, into); err != nil {
		t.Fatalf("unmarshal %s: %v", path, err)
	}
}
