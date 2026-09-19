package validate

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"irrlicht/core/application/replayengine"
	"irrlicht/tools/onboarding-factory/internal/matrix"
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

// TestObservationsDirectContextPass covers the direct context vector. A golden
// with total tokens, context window, and utilization satisfies the nonzero
// assertions. These fields are distinct from cost and cumulative tokens.
func TestObservationsDirectContextPass(t *testing.T) {
	dir := t.TempDir()
	mkGoldenRec(t, dir, "2026-06-28-00-00-00_x", `{"model_name":"gemini-3.5-flash","total_tokens":16353,"context_window":1048576,"context_utilization_percentage":1.56}`)
	writeExpected(t, dir, `{"schema_version":1,"scenario_id":"s","observations":{"model":"gemini-3.5-flash","total_tokens_nonzero":true,"context_window_nonzero":true,"context_utilization_nonzero":true}}`)
	rep, err := ValidateObservations(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !rep.Pass || len(rep.Asserts) != 4 {
		t.Fatalf("want pass + 4 asserts, got %+v", rep)
	}
	for _, a := range rep.Asserts {
		if !a.OK {
			t.Fatalf("assert %s should pass: %+v", a.Field, a)
		}
	}
}

// TestObservationsContextNonzeroFails checks that a missing context vector
// fails each direct nonzero assertion.
func TestObservationsContextNonzeroFails(t *testing.T) {
	dir := t.TempDir()
	mkGoldenRec(t, dir, "2026-06-28-00-00-00_x", `{"model_name":"gemini-3.5-flash"}`)
	writeExpected(t, dir, `{"schema_version":1,"scenario_id":"s","observations":{"context_window_nonzero":true,"context_utilization_nonzero":true,"total_tokens_nonzero":true}}`)
	rep, _ := ValidateObservations(dir)
	if rep.Pass {
		t.Fatalf("storeless golden must fail the context/token assertions: %+v", rep)
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

func TestTranscriptAssertionsRequireMatchingToolResult(t *testing.T) {
	dir := t.TempDir()
	name := "2026-09-18-00-00-00_x"
	mkGoldenRec(t, dir, name, `{}`)
	writeExpected(t, dir, `{"schema_version":1,"scenario_id":"s","transcript_assertions":[`+
		`{"name":"one successful bash round trip","where":{"type":"tool/call","data.name":"bash"},"min_count":1,"max_count":1,`+
		`"related":{"where":{"type":"tool/result","data.message.content.0.isError":false},"source_path":"data.callId","target_path":"data.message.source.callId"}},`+
		`{"name":"no approval request","where":{"type":"approval/asked"},"max_count":0}]}`)
	recDir := filepath.Join(dir, "recordings", name)
	good := "" +
		`{"type":"tool/call","data":{"callId":"call-1","name":"bash"}}` + "\n" +
		`{"type":"tool/result","data":{"message":{"source":{"callId":"call-1"},"content":[{"isError":false}]}}}` + "\n"
	if err := os.WriteFile(filepath.Join(recDir, "transcript.jsonl"), []byte(good), 0o644); err != nil {
		t.Fatal(err)
	}
	report, err := ValidateTranscriptForProfile(dir, matrix.ProfileCLILocal)
	if err != nil || report == nil || !report.Pass {
		t.Fatalf("matching tool round must pass: report=%+v err=%v", report, err)
	}

	bad := strings.Replace(good, `"callId":"call-1"},"content"`, `"callId":"other"},"content"`, 1)
	if err := os.WriteFile(filepath.Join(recDir, "transcript.jsonl"), []byte(bad), 0o644); err != nil {
		t.Fatal(err)
	}
	report, err = ValidateTranscriptForProfile(dir, matrix.ProfileCLILocal)
	if err != nil || report == nil || report.Pass {
		t.Fatalf("mismatched tool result must fail: report=%+v err=%v", report, err)
	}
}

func TestRecordAssertionsSkipMissingSpec(t *testing.T) {
	dir := t.TempDir()
	transcript, err := ValidateTranscriptForProfile(dir, matrix.ProfileCLILocal)
	if err != nil || transcript != nil {
		t.Fatalf("missing spec must skip transcript assertions: report=%+v err=%v", transcript, err)
	}
	events, err := ValidateEventsForProfile(dir, matrix.ProfileCLILocal)
	if err != nil || events != nil {
		t.Fatalf("missing spec must skip event assertions: report=%+v err=%v", events, err)
	}
}

func TestEventAssertionsRequireOwnedPaths(t *testing.T) {
	dir := t.TempDir()
	name := "2026-09-18-00-00-00_x"
	mkGoldenRec(t, dir, name, `{}`)
	writeExpected(t, dir, `{"schema_version":1,"scenario_id":"s","event_assertions":[`+
		`{"name":"distinct PID bindings","where":{"kind":"pid_discovered"},"min_count":2,"min_distinct":{"pid":2}},`+
		`{"name":"transcript paths belong to their sessions","known_failing":true,"where":{"kind":"transcript_removed"},"min_count":2,`+
		`"field_contains":[{"value_path":"session_id","container_path":"transcript_path"}]}]}`)
	recDir := filepath.Join(dir, "recordings", name)
	good := "" +
		`{"kind":"pid_discovered","session_id":"session-1","pid":101}` + "\n" +
		`{"kind":"pid_discovered","session_id":"session-2","pid":202}` + "\n" +
		`{"kind":"transcript_removed","session_id":"session-1","transcript_path":"/sessions/session-1/transcript.jsonl"}` + "\n" +
		`{"kind":"transcript_removed","session_id":"session-2","transcript_path":"/sessions/session-2/transcript.jsonl"}` + "\n"
	if err := os.WriteFile(filepath.Join(recDir, "events.jsonl"), []byte(good), 0o644); err != nil {
		t.Fatal(err)
	}
	report, err := ValidateEventsForProfile(dir, matrix.ProfileCLILocal)
	if err != nil || report == nil || !report.Pass || report.ExpectedPass() {
		t.Fatalf("an unexpectedly passing known failure must fail verification: report=%+v err=%v", report, err)
	}

	bad := strings.Replace(good, `/sessions/session-2/transcript.jsonl`, `/sessions/session-1/transcript.jsonl`, 1)
	if err := os.WriteFile(filepath.Join(recDir, "events.jsonl"), []byte(bad), 0o644); err != nil {
		t.Fatal(err)
	}
	report, err = ValidateEventsForProfile(dir, matrix.ProfileCLILocal)
	if err != nil || report == nil || report.Pass || !report.ExpectedPass() {
		t.Fatalf("crossed event path must fail: report=%+v err=%v", report, err)
	}

	badPID := strings.Replace(bad, `"pid":202`, `"pid":101`, 1)
	if err := os.WriteFile(filepath.Join(recDir, "events.jsonl"), []byte(badPID), 0o644); err != nil {
		t.Fatal(err)
	}
	report, err = ValidateEventsForProfile(dir, matrix.ProfileCLILocal)
	if err != nil || report == nil || report.ExpectedPass() {
		t.Fatalf("unrelated PID regression must not inherit the path waiver: report=%+v err=%v", report, err)
	}
}

func TestDeepseekTaskListAssertionsDetectMutations(t *testing.T) {
	dir, name, recordingDir, events := stageTaskListMutationFixture(t)
	assertTaskListNoWaitingMutation(t, dir, name, events)
	assertTaskListCompletedTurnMutations(t, dir, name, recordingDir)
}

func stageTaskListMutationFixture(t *testing.T) (string, string, string, []byte) {
	t.Helper()
	source, err := filepath.Abs(filepath.Join("..", "..", "..", "..", "replaydata", "agents", "deepseek-harness", "scenarios", "2-3_task-list"))
	if err != nil {
		t.Fatal(err)
	}
	expected, err := os.ReadFile(filepath.Join(source, "expected.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	recording, ok, err := matrix.NewestRecording(source, matrix.ProfileCLILocal)
	if err != nil || !ok {
		t.Fatalf("find task-list recording: ok=%v err=%v", ok, err)
	}
	events, err := os.ReadFile(filepath.Join(recording.Dir, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "expected.jsonl"), expected, 0o644); err != nil {
		t.Fatal(err)
	}
	name := "2026-09-19-00-00-00_mutation"
	mkGoldenRec(t, dir, name, `{}`)
	return dir, name, recording.Dir, events
}

func assertTaskListNoWaitingMutation(t *testing.T, dir, name string, events []byte) {
	t.Helper()
	eventPath := filepath.Join(dir, "recordings", name, "events.jsonl")
	if err := os.WriteFile(eventPath, events, 0o644); err != nil {
		t.Fatal(err)
	}
	baseline, err := ValidateEventsForProfile(dir, matrix.ProfileCLILocal)
	if err != nil || baseline == nil || !baseline.ExpectedPass() {
		t.Fatalf("unmutated recording must pass event assertions: report=%+v err=%v", baseline, err)
	}

	mutated := append(append([]byte{}, events...), []byte(`{"kind":"state_transition","new_state":"waiting"}`+"\n")...)
	if err := os.WriteFile(eventPath, mutated, 0o644); err != nil {
		t.Fatal(err)
	}
	result, err := ValidateEventsForProfile(dir, matrix.ProfileCLILocal)
	if err != nil || result == nil || result.ExpectedPass() {
		t.Fatalf("injected waiting transition must fail: report=%+v err=%v", result, err)
	}
	for _, assertion := range result.Asserts {
		if assertion.Name == "no waiting state" {
			if assertion.OK {
				t.Fatal("no waiting state assertion ignored the injected transition")
			}
			return
		}
	}
	t.Fatal("task-list spec has no no waiting state assertion")
}

func taskListTranscriptLines(t *testing.T, recordingDir string) ([][]byte, int) {
	t.Helper()
	var lines [][]byte
	finalTurnEnd, turnEnds := -1, 0
	path := filepath.Join(recordingDir, "transcript.jsonl.zstd")
	err := replayengine.ScanTranscriptLines(path, func(line []byte) error {
		var record struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(line, &record); err != nil {
			return err
		}
		if record.Type == "turn/end" {
			turnEnds++
			finalTurnEnd = len(lines)
		}
		lines = append(lines, append([]byte(nil), line...))
		return nil
	})
	if err != nil || turnEnds != 7 || finalTurnEnd < 0 {
		t.Fatalf("source transcript must contain seven completed turns: count=%d err=%v", turnEnds, err)
	}
	return lines, finalTurnEnd
}

func assertTaskListCompletedTurnMutations(t *testing.T, dir, name, recordingDir string) {
	t.Helper()
	lines, finalTurnEnd := taskListTranscriptLines(t, recordingDir)
	copyPath := filepath.Join(dir, "recordings", name, "transcript.jsonl")
	writeTaskListTranscript(t, copyPath, lines)
	baseline, err := ValidateTranscriptForProfile(dir, matrix.ProfileCLILocal)
	if err != nil || baseline == nil || !baseline.ExpectedPass() {
		t.Fatalf("unmutated transcript must pass assertions: report=%+v err=%v", baseline, err)
	}

	withoutFinalEnd := append(append([][]byte{}, lines[:finalTurnEnd]...), lines[finalTurnEnd+1:]...)
	writeTaskListTranscript(t, copyPath, withoutFinalEnd)
	assertTaskListCompletionFails(t, dir, "missing final turn/end")

	withAbortedFinalTurn := append([][]byte{}, lines...)
	withAbortedFinalTurn[finalTurnEnd] = abortedTaskListTurn(t, lines[finalTurnEnd])
	writeTaskListTranscript(t, copyPath, withAbortedFinalTurn)
	assertTaskListCompletionFails(t, dir, "aborted final turn/end")
}

func writeTaskListTranscript(t *testing.T, path string, lines [][]byte) {
	t.Helper()
	if err := os.WriteFile(path, append(bytes.Join(lines, []byte("\n")), '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
}

func assertTaskListCompletionFails(t *testing.T, dir, mutation string) {
	t.Helper()
	report, err := ValidateTranscriptForProfile(dir, matrix.ProfileCLILocal)
	if err != nil || report == nil || report.ExpectedPass() {
		t.Fatalf("%s must fail: report=%+v err=%v", mutation, report, err)
	}
	for _, assertion := range report.Asserts {
		if assertion.Name == "all seven turns complete" {
			if assertion.OK {
				t.Fatalf("turn completion assertion ignored %s", mutation)
			}
			return
		}
	}
	t.Fatal("task-list spec has no all seven turns complete assertion")
}

func abortedTaskListTurn(t *testing.T, line []byte) []byte {
	t.Helper()
	var record map[string]any
	if err := json.Unmarshal(line, &record); err != nil {
		t.Fatal(err)
	}
	data, ok := record["data"].(map[string]any)
	if !ok {
		t.Fatal("final turn/end has no data object")
	}
	reason, ok := data["reason"].(map[string]any)
	if !ok || reason["kind"] != "completed" {
		t.Fatalf("final turn/end has no completed reason: %v", data["reason"])
	}
	reason["kind"] = "aborted"
	abortedLine, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	return abortedLine
}

func TestCommittedRecordAssertions(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", "..", "..", "..", "replaydata", "agents"))
	if err != nil {
		t.Fatal(err)
	}
	matches, err := filepath.Glob(filepath.Join(root, "*", "scenarios", "*", "expected.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, expectedPath := range matches {
		scenarioDir := filepath.Dir(expectedPath)
		meta, err := loadExpectedMeta(scenarioDir)
		if err != nil {
			t.Fatalf("%s: %v", expectedPath, err)
		}
		if meta == nil || len(meta.TranscriptAssertions) == 0 && len(meta.EventAssertions) == 0 {
			continue
		}
		checked++
		checkCommittedTranscriptAssertions(t, expectedPath, scenarioDir, meta.TranscriptAssertions)
		checkCommittedEventAssertions(t, expectedPath, scenarioDir, meta.EventAssertions)
	}
	if checked == 0 {
		t.Fatal("no committed transcript or event assertions were discovered; the catalog gate checked nothing")
	}
	t.Logf("checked record assertions in %d committed cells", checked)
}

func checkCommittedTranscriptAssertions(
	t *testing.T,
	expectedPath string,
	scenarioDir string,
	assertions []RecordAssertion,
) {
	t.Helper()
	if len(assertions) == 0 {
		return
	}
	report, err := ValidateTranscriptForProfile(scenarioDir, matrix.ProfileCLILocal)
	if err != nil {
		t.Errorf("%s: %v", expectedPath, err)
		return
	}
	if report == nil || !report.Pass {
		t.Errorf("%s: transcript assertions failed: %+v", expectedPath, report)
	}
}

func checkCommittedEventAssertions(
	t *testing.T,
	expectedPath string,
	scenarioDir string,
	assertions []RecordAssertion,
) {
	t.Helper()
	if len(assertions) == 0 {
		return
	}
	report, err := ValidateEventsForProfile(scenarioDir, matrix.ProfileCLILocal)
	if err != nil {
		t.Errorf("%s: %v", expectedPath, err)
		return
	}
	if report == nil || !report.ExpectedPass() {
		t.Errorf("%s: event assertions failed: %+v", expectedPath, report)
	}
}
