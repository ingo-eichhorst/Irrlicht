package validate

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"irrlicht/tools/agent-onboarding/internal/groundtruth"
)

func TestRun_passesWhenObservedMatchesLabels(t *testing.T) {
	dir := t.TempDir()
	start := time.Date(2026, 5, 14, 12, 0, 0, 0, time.UTC)

	events := []map[string]any{
		{"kind": "state_transition", "ts": start.Format(time.RFC3339Nano), "new_state": "ready"},
		{"kind": "state_transition", "ts": start.Add(15 * time.Second).Format(time.RFC3339Nano), "prev_state": "ready", "new_state": "working"},
		{"kind": "state_transition", "ts": start.Add(22 * time.Second).Format(time.RFC3339Nano), "prev_state": "working", "new_state": "ready"},
	}
	writeJSONL(t, filepath.Join(dir, "events.jsonl"), events)

	gtPath := filepath.Join(dir, "ground_truth.jsonl")
	f, _ := os.Create(gtPath)
	groundtruth.Write(f, groundtruth.Meta{SchemaVersion: 1, Agent: "x", Scenario: "y", RecordingStartedAt: start}, []groundtruth.Label{
		{TsOffsetMs: 0, Marker: "created", ExpectedState: "ready"},
		{TsOffsetMs: 15000, Marker: "start", ExpectedState: "working", ToleranceMs: 2000},
		{TsOffsetMs: 22000, Marker: "done", ExpectedState: "ready", ToleranceMs: 2000},
	})
	f.Close()

	r, err := Run(context.Background(), Input{
		Agent: "x", Scenario: "y",
		EventsPath: filepath.Join(dir, "events.jsonl"), GroundTruth: gtPath,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !r.Pass {
		t.Errorf("expected pass; labels=%+v", r.Labels)
	}
	if r.Result() != "pass" {
		t.Errorf("Result() = %q", r.Result())
	}
}

func TestRun_failsOnStateMismatch(t *testing.T) {
	dir := t.TempDir()
	start := time.Date(2026, 5, 14, 12, 0, 0, 0, time.UTC)
	events := []map[string]any{
		{"kind": "state_transition", "ts": start.Format(time.RFC3339Nano), "new_state": "ready"},
		{"kind": "state_transition", "ts": start.Add(15 * time.Second).Format(time.RFC3339Nano), "new_state": "waiting"},
	}
	writeJSONL(t, filepath.Join(dir, "events.jsonl"), events)
	gtPath := filepath.Join(dir, "ground_truth.jsonl")
	f, _ := os.Create(gtPath)
	groundtruth.Write(f, groundtruth.Meta{SchemaVersion: 1, Agent: "x", Scenario: "y", RecordingStartedAt: start}, []groundtruth.Label{
		{TsOffsetMs: 15000, Marker: "start", ExpectedState: "working", ToleranceMs: 2000},
	})
	f.Close()
	r, err := Run(context.Background(), Input{
		Agent: "x", Scenario: "y",
		EventsPath: filepath.Join(dir, "events.jsonl"), GroundTruth: gtPath,
	})
	if err != nil {
		t.Fatal(err)
	}
	if r.Pass {
		t.Error("expected fail on state mismatch")
	}
	if r.Labels[0].Note == "" {
		t.Error("expected explanatory note")
	}
}

func TestRun_failsOnTolerance(t *testing.T) {
	dir := t.TempDir()
	start := time.Date(2026, 5, 14, 12, 0, 0, 0, time.UTC)
	events := []map[string]any{
		// Working transition arrives 5s later than expected.
		{"kind": "state_transition", "ts": start.Add(20 * time.Second).Format(time.RFC3339Nano), "new_state": "working"},
	}
	writeJSONL(t, filepath.Join(dir, "events.jsonl"), events)
	gtPath := filepath.Join(dir, "ground_truth.jsonl")
	f, _ := os.Create(gtPath)
	groundtruth.Write(f, groundtruth.Meta{SchemaVersion: 1, Agent: "x", Scenario: "y", RecordingStartedAt: start}, []groundtruth.Label{
		{TsOffsetMs: 15000, Marker: "start", ExpectedState: "working", ToleranceMs: 1000},
	})
	f.Close()
	r, err := Run(context.Background(), Input{
		Agent: "x", Scenario: "y",
		EventsPath: filepath.Join(dir, "events.jsonl"), GroundTruth: gtPath,
	})
	if err != nil {
		t.Fatal(err)
	}
	if r.Pass {
		t.Error("expected fail on tolerance breach")
	}
}

func TestWriteCoverage_updatesOnlyValidatorOwnedKeys(t *testing.T) {
	dir := t.TempDir()
	coveragePath := filepath.Join(dir, "coverage.json")
	initial := map[string]any{
		"scenarios": []any{
			map[string]any{
				"id": "basic-turn",
				"coverage": map[string]any{
					"claudecode": map[string]any{
						"agent_supports":    "yes",
						"irrlicht_observes": "yes",
						"notes":             "manual",
					},
				},
			},
		},
	}
	b, _ := json.MarshalIndent(initial, "", "  ")
	os.WriteFile(coveragePath, b, 0o644)

	err := WriteCoverage(coveragePath, "basic-turn", "claudecode", CoverageCell{
		LastTested: time.Date(2026, 5, 14, 12, 0, 0, 0, time.UTC),
		AgentVersion: "2.1.140", AdapterVersion: "irrlichd 0.3.13",
		Result: "pass",
	})
	if err != nil {
		t.Fatal(err)
	}
	updated, _ := os.ReadFile(coveragePath)
	var parsed map[string]any
	json.Unmarshal(updated, &parsed)
	cell := parsed["scenarios"].([]any)[0].(map[string]any)["coverage"].(map[string]any)["claudecode"].(map[string]any)
	// Maintainer-owned fields preserved.
	if cell["agent_supports"] != "yes" || cell["notes"] != "manual" {
		t.Errorf("maintainer fields overwritten: %+v", cell)
	}
	// Validator-owned fields written.
	if cell["result"] != "pass" || cell["agent_version"] != "2.1.140" {
		t.Errorf("validator fields not written: %+v", cell)
	}
}

func writeJSONL(t *testing.T, path string, events []map[string]any) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	for _, e := range events {
		if err := enc.Encode(e); err != nil {
			t.Fatal(err)
		}
	}
}
