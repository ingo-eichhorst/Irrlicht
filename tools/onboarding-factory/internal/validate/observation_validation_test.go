package validate

import (
	"os"
	"path/filepath"
	"testing"
)

func TestObservationsSkippedNoRecording(t *testing.T) {
	rep, err := ValidateObservations(t.TempDir())
	if err != nil || !rep.Skipped || !rep.Pass {
		t.Fatalf("want skipped+pass, got %+v err=%v", rep, err)
	}
}

func TestObservationsHardAssertsPass(t *testing.T) {
	rep := validateObservationScenario(t,
		`{"estimated_cost_usd":0.12,"cum_input_tokens":10,"cum_output_tokens":20,"model_name":"claude-opus-4-7"}`,
		`{"model":"claude-opus-4-7","cost_nonzero":true,"tokens_nonzero":true}`)
	assertObservationPasses(t, rep, 3)
}

func TestObservationsModelMismatchFails(t *testing.T) {
	rep := validateObservationScenario(t,
		`{"estimated_cost_usd":0.12,"model_name":"gpt-5"}`,
		`{"model":"claude-opus-4-7"}`)
	if rep.Pass {
		t.Fatalf("model mismatch must fail: %+v", rep)
	}
}

func TestObservationsCostNonzeroFails(t *testing.T) {
	rep := validateObservationScenario(t,
		`{"estimated_cost_usd":0,"model_name":"m"}`,
		`{"cost_nonzero":true}`)
	if rep.Pass {
		t.Fatalf("zero cost must fail cost_nonzero: %+v", rep)
	}
}

func TestObservationsCumulativeTokensEquals(t *testing.T) {
	expected := `{"schema_version":1,"scenario_id":"s","observations":{"cumulative_tokens_equals":38436}}`
	baseline := observationScenario(t, "2026-09-19-00-00-00_baseline", `{"cum_input_tokens":38005,"cum_output_tokens":431}`, expected)
	assertObservationPasses(t, validateObservations(t, baseline), 1)
}

func TestObservationsCumulativeTokensEqualsDetectsCommittedMutation(t *testing.T) {
	expected := `{"schema_version":1,"scenario_id":"s","observations":{"cumulative_tokens_equals":38436}}`
	mutated, err := os.ReadFile(filepath.Join("testdata", "cumulative_tokens_equals_mutation.json"))
	if err != nil {
		t.Fatal(err)
	}
	dir := observationScenario(t, "2026-09-19-00-00-00_mutation", string(mutated), expected)
	rep := validateObservations(t, dir)
	if rep.Pass || len(rep.Asserts) != 1 || rep.Asserts[0].Field != "cumulative_tokens" || rep.Asserts[0].OK {
		t.Fatalf("committed cumulative-token mutation must fail: %+v", rep)
	}
}

func TestObservationsCumulativeTokensEqualsRejectsNegativeTarget(t *testing.T) {
	negative := observationScenario(t, "2026-09-19-00-00-00_negative", `{"cum_input_tokens":38005,"cum_output_tokens":431}`, `{"schema_version":1,"scenario_id":"s","observations":{"cumulative_tokens_equals":-1}}`)
	rep := validateObservations(t, negative)
	if rep.Pass || len(rep.Asserts) != 1 || rep.Asserts[0].Field != "cumulative_tokens" || rep.Asserts[0].Expected != "-1" {
		t.Fatalf("negative cumulative-token assertion must run and fail: %+v", rep)
	}
}

func TestObservationsDirectContextPass(t *testing.T) {
	rep := validateObservationScenario(t,
		`{"model_name":"gemini-3.5-flash","total_tokens":16353,"context_window":1048576,"context_utilization_percentage":1.56}`,
		`{"model":"gemini-3.5-flash","total_tokens_nonzero":true,"context_window_nonzero":true,"context_utilization_nonzero":true}`)
	assertObservationPasses(t, rep, 4)
}

func TestObservationsContextNonzeroFails(t *testing.T) {
	rep := validateObservationScenario(t,
		`{"model_name":"gemini-3.5-flash"}`,
		`{"context_window_nonzero":true,"context_utilization_nonzero":true,"total_tokens_nonzero":true}`)
	if rep.Pass {
		t.Fatalf("storeless golden must fail the context/token assertions: %+v", rep)
	}
}

func validateObservationScenario(t *testing.T, summary, observations string) *ObservationReport {
	t.Helper()
	dir := observationScenario(t, "2026-05-01-00-00-00_x", summary,
		`{"schema_version":1,"scenario_id":"s","observations":`+observations+`}`)
	return validateObservations(t, dir)
}

func observationScenario(t *testing.T, name, summary, expected string) string {
	t.Helper()
	dir := t.TempDir()
	mkGoldenRec(t, dir, name, summary)
	writeExpected(t, dir, expected)
	return dir
}

func validateObservations(t *testing.T, dir string) *ObservationReport {
	t.Helper()
	report, err := ValidateObservations(dir)
	if err != nil {
		t.Fatal(err)
	}
	return report
}

func assertObservationPasses(t *testing.T, report *ObservationReport, assertionCount int) {
	t.Helper()
	if !report.Pass || len(report.Asserts) != assertionCount {
		t.Fatalf("want pass + %d asserts, got %+v", assertionCount, report)
	}
	for _, assertion := range report.Asserts {
		if !assertion.OK {
			t.Fatalf("assert %s should pass: %+v", assertion.Field, assertion)
		}
	}
}

func TestObservationsSoftDriftReportedNotFailed(t *testing.T) {
	dir := t.TempDir()
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
	for _, d := range rep.Drifts {
		if d.Field == "model" && d.Prior == "claude-opus-4-7" && d.Current == "claude-opus-4-8" {
			return
		}
	}
	t.Fatalf("want model drift, got %+v", rep.Drifts)
}
