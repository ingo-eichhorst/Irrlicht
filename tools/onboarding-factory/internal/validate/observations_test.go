package validate

import (
	"os"
	"path/filepath"
	"testing"
)

// writeRec writes a recording dir with a replay golden carrying the given
// summary, under scenarioDir/recordings/<name>/.
func mkGoldenRec(t *testing.T, scenarioDir, name, summaryJSON string) {
	t.Helper()
	dir := filepath.Join(scenarioDir, "recordings", name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	golden := `{"schema_version":1,"summary":` + summaryJSON + `}`
	if err := os.WriteFile(filepath.Join(dir, "transcript.jsonl.replay.json.golden"), []byte(golden), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeExpected(t *testing.T, scenarioDir, meta string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(scenarioDir, "expected.jsonl"), []byte(meta+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestObservationsSkippedNoRecording(t *testing.T) {
	rep, err := ValidateObservations(t.TempDir())
	if err != nil || !rep.Skipped || !rep.Pass {
		t.Fatalf("want skipped+pass, got %+v err=%v", rep, err)
	}
}

func TestObservationsHardAssertsPass(t *testing.T) {
	dir := t.TempDir()
	mkGoldenRec(t, dir, "2026-05-01-00-00-00_x", `{"estimated_cost_usd":0.12,"cum_input_tokens":10,"cum_output_tokens":20,"model_name":"claude-opus-4-7"}`)
	writeExpected(t, dir, `{"schema_version":1,"scenario_id":"s","observations":{"model":"claude-opus-4-7","cost_nonzero":true,"tokens_nonzero":true}}`)
	rep, err := ValidateObservations(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !rep.Pass || len(rep.Asserts) != 3 {
		t.Fatalf("want pass + 3 asserts, got %+v", rep)
	}
	for _, a := range rep.Asserts {
		if !a.OK {
			t.Fatalf("assert %s should pass: %+v", a.Field, a)
		}
	}
}

func TestObservationsModelMismatchFails(t *testing.T) {
	dir := t.TempDir()
	mkGoldenRec(t, dir, "2026-05-01-00-00-00_x", `{"estimated_cost_usd":0.12,"model_name":"gpt-5"}`)
	writeExpected(t, dir, `{"schema_version":1,"scenario_id":"s","observations":{"model":"claude-opus-4-7"}}`)
	rep, _ := ValidateObservations(dir)
	if rep.Pass {
		t.Fatalf("model mismatch must fail: %+v", rep)
	}
}

func TestObservationsCostNonzeroFails(t *testing.T) {
	dir := t.TempDir()
	mkGoldenRec(t, dir, "2026-05-01-00-00-00_x", `{"estimated_cost_usd":0,"model_name":"m"}`)
	writeExpected(t, dir, `{"schema_version":1,"scenario_id":"s","observations":{"cost_nonzero":true}}`)
	rep, _ := ValidateObservations(dir)
	if rep.Pass {
		t.Fatalf("zero cost must fail cost_nonzero: %+v", rep)
	}
}

func TestObservationsSoftDriftReportedNotFailed(t *testing.T) {
	dir := t.TempDir()
	// prior cheaper; current 3× → > default 50% band → drift, but no hard assert.
	mkGoldenRec(t, dir, "2026-05-01-00-00-00_a", `{"estimated_cost_usd":0.10,"cum_input_tokens":100,"model_name":"m"}`)
	mkGoldenRec(t, dir, "2026-05-02-00-00-00_b", `{"estimated_cost_usd":0.30,"cum_input_tokens":105,"model_name":"m"}`)
	rep, _ := ValidateObservations(dir)
	if !rep.Pass {
		t.Fatalf("drift must NOT fail (soft): %+v", rep)
	}
	var costDrift bool
	for _, d := range rep.Drifts {
		if d.Field == "cost_usd" {
			costDrift = true
		}
		if d.Field == "input_tokens" {
			t.Fatalf("5%% token change should be within tolerance, not a drift: %+v", d)
		}
	}
	if !costDrift {
		t.Fatalf("3× cost change should be a drift: %+v", rep.Drifts)
	}
}

func TestObservationsModelDrift(t *testing.T) {
	dir := t.TempDir()
	mkGoldenRec(t, dir, "2026-05-01-00-00-00_a", `{"model_name":"claude-opus-4-7"}`)
	mkGoldenRec(t, dir, "2026-05-02-00-00-00_b", `{"model_name":"claude-opus-4-8"}`)
	rep, _ := ValidateObservations(dir)
	if !rep.Pass {
		t.Fatalf("model drift is soft, must not fail: %+v", rep)
	}
	found := false
	for _, d := range rep.Drifts {
		if d.Field == "model" && d.Prior == "claude-opus-4-7" && d.Current == "claude-opus-4-8" {
			found = true
		}
	}
	if !found {
		t.Fatalf("want model drift, got %+v", rep.Drifts)
	}
}
