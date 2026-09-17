package muse

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"irrlicht/core/pkg/tailer"
)

// line parses a raw JSONL string into the map shape ParseLine receives.
// Inline lines below are synthetic-but-shaped-exactly-like-real captures
// (field names and value shapes lifted from real records read directly off
// ~/.local/share/muse/sessions/ while writing this parser); the multi-line
// flows that need real, redacted transcript slices live in testdata/ — see
// fixtureLines.
func line(t *testing.T, raw string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatalf("bad test line: %v", err)
	}
	return m
}

// fixtureLines reads a testdata JSONL fixture into its individual lines.
func fixtureLines(t *testing.T, name string) []string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	for _, l := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(l) != "" {
			lines = append(lines, l)
		}
	}
	return lines
}

// writeTranscript writes lines to a session-shaped transcript and returns its
// path: <tmp>/<YYYY>/<MM>/<DD>/<session-id>/session.jsonl, matching Muse's
// layout (adapter.go).
func writeTranscript(t *testing.T, lines []string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "2026", "09", "13", "01a09c40-6dd8-7732-8c3f-5a7618ffaee4")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, transcriptFilename)
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// tailFixture replays fixture lines through a fresh tailer and returns the
// resulting metrics — the junie/copilot permission_test pattern.
func tailFixture(t *testing.T, lines []string) *tailer.SessionMetrics {
	t.Helper()
	path := writeTranscript(t, lines)
	m, err := tailer.NewTranscriptTailer(path, &Parser{}, AdapterName).TailAndProcess()
	if err != nil {
		t.Fatalf("TailAndProcess: %v", err)
	}
	return m
}

// --- kind:"run" / event.kind:"started" ---

func TestParseLine_RunStarted_OpensUserMessage(t *testing.T) {
	p := &Parser{}
	ev := p.ParseLine(line(t, `{"payload_type":"runtime.session","payload":{"kind":"run",
		"run_id":"r1","event":{"kind":"started","prompt":"ls"}}}`))
	if ev.EventType != "user_message" {
		t.Errorf("EventType = %q, want user_message", ev.EventType)
	}
	if !ev.ClearToolNames {
		t.Error("expected ClearToolNames")
	}
	if ev.UserText != "ls" {
		t.Errorf("UserText = %q, want %q", ev.UserText, "ls")
	}
}

func TestParseLine_RunStarted_ResetsStaleErrorClass(t *testing.T) {
	p := &Parser{lastErrorClass: "config_error"}
	p.ParseLine(line(t, `{"payload_type":"runtime.session","payload":{"kind":"run",
		"run_id":"r2","event":{"kind":"started","prompt":"go"}}}`))
	if p.lastErrorClass != "" {
		t.Errorf("lastErrorClass = %q, want cleared on a new run", p.lastErrorClass)
	}
}

// --- kind:"run" / event.kind:"terminal" ---

func TestParseLine_RunTerminal_Completed_TurnDone(t *testing.T) {
	p := &Parser{}
	ev := p.ParseLine(line(t, `{"payload_type":"runtime.session","payload":{"kind":"run",
		"run_id":"r1","event":{"kind":"terminal","terminal":"completed","reason":null,
		"turn_duration_ms":14542}}}`))
	if ev.EventType != "turn_done" {
		t.Errorf("EventType = %q, want turn_done", ev.EventType)
	}
	if ev.SessionError != nil {
		t.Errorf("SessionError = %+v, want nil on a clean completion", ev.SessionError)
	}
}

func TestParseLine_RunTerminal_Failed_UsesPrecedingErrorClass(t *testing.T) {
	p := &Parser{}
	// The real sequence, verified live: run_fatal_error_classified always
	// arrives on the line immediately before the terminal:"failed" it
	// explains (4 separate captured subagent transcripts, all
	// error_class:"config_error" — see testdata/real-turn-failed.jsonl).
	p.ParseLine(line(t, `{"payload_type":"runtime.session","payload":{"kind":"run",
		"run_id":"r3","event":{"kind":"run_fatal_error_classified","error_class":"config_error"}}}`))
	ev := p.ParseLine(line(t, `{"payload_type":"runtime.session","payload":{"kind":"run",
		"run_id":"r3","event":{"kind":"terminal","terminal":"failed",
		"reason":"invalid run configuration: provider does not support base instructions",
		"turn_duration_ms":11}}}`))
	if ev.EventType != "turn_done" {
		t.Errorf("EventType = %q, want turn_done", ev.EventType)
	}
	if ev.SessionError == nil {
		t.Fatal("expected SessionError on terminal:failed")
	}
	if ev.SessionError.Class != "config_error" {
		t.Errorf("SessionError.Class = %q, want config_error", ev.SessionError.Class)
	}
	if ev.SessionError.Phase != tailer.ErrorPhaseUnknown {
		t.Errorf("SessionError.Phase = %q, want ErrorPhaseUnknown", ev.SessionError.Phase)
	}
	if !strings.Contains(ev.SessionError.Message, "base instructions") {
		t.Errorf("SessionError.Message = %q, want it to carry the reason", ev.SessionError.Message)
	}
}

func TestParseLine_RunTerminal_Failed_FallsBackWithNoErrorClass(t *testing.T) {
	p := &Parser{}
	// The corpus's OTHER real failure (reason "model stream idle timeout
	// after 30000ms") has no preceding run_fatal_error_classified at all —
	// it's preceded by kind:"task"/"timed_out" instead.
	ev := p.ParseLine(line(t, `{"payload_type":"runtime.session","payload":{"kind":"run",
		"run_id":"r4","event":{"kind":"terminal","terminal":"failed",
		"reason":"model stream idle timeout after 30000ms","turn_duration_ms":76325}}}`))
	if ev.SessionError == nil {
		t.Fatal("expected SessionError")
	}
	if ev.SessionError.Class != "terminal_failed" {
		t.Errorf("SessionError.Class = %q, want the generic fallback terminal_failed", ev.SessionError.Class)
	}
}

func TestParseLine_RunTerminal_Cancelled_NoErrorNoInterruptFlag(t *testing.T) {
	p := &Parser{}
	ev := p.ParseLine(line(t, `{"payload_type":"runtime.session","payload":{"kind":"run",
		"run_id":"r5","event":{"kind":"terminal","terminal":"cancelled",
		"reason":"cancelled during model step","turn_duration_ms":2877101}}}`))
	if ev.EventType != "turn_done" {
		t.Errorf("EventType = %q, want turn_done (the turn objectively ended)", ev.EventType)
	}
	if ev.SessionError != nil {
		t.Errorf("SessionError = %+v, want nil — cancellation is not a session failure", ev.SessionError)
	}
	if ev.IsUserInterrupt {
		t.Error("IsUserInterrupt must stay false: whether this represents a real human ESC is UNVERIFIED")
	}
}

// --- kind:"task" / event.kind:"status" / details.phase:"retry_scheduled" ---

// retryScheduledLine builds a kind:"task" retry_scheduled record in the shape
// a real muse provider retry emits (live-confirmed:
// ~/.local/share/muse/sessions/2026/09/13/01a09c47-506a-7763-b82a-9ab89a9e195a/session.jsonl,
// task_id 01a09c50-27eb-7e83-ac0e-d19d69bdade4, and 86 similar records across
// the corpus).
func retryScheduledLine(t *testing.T, extraFacetFields string) map[string]any {
	t.Helper()
	return line(t, `{"payload_type":"runtime.session","payload":{"kind":"task",
		"run_id":"r1","task_id":"task_1","event":{"kind":"status","task_id":"task_1",
		"message":"retrying meta model stream in 5000ms (attempt 2/10)",
		"details":{"phase":"retry_scheduled","facets":[
		{"kind":"external_attempt","system":"meta","operation":"model.response",
		"attempt":1,"next_attempt":2,"max_attempts":10,"retry_delay_ms":5000,
		"error_kind":"rate_limited","http_status":429`+extraFacetFields+`},
		{"kind":"producer","detail":{"kind":"provider","provider":"meta"}}
		]}}}}`)
}

func TestParseLine_TaskStatus_RetryScheduled_SetsSessionErrorRetrying(t *testing.T) {
	ev := (&Parser{}).ParseLine(retryScheduledLine(t, ""))
	if !ev.Skip {
		t.Error("expected Skip=true (bookkeeping, folded via applySkippedEvent)")
	}
	if ev.SessionError == nil {
		t.Fatal("expected SessionError")
	}
	se := ev.SessionError
	if se.Phase != tailer.ErrorPhaseRetrying {
		t.Errorf("Phase = %q, want retrying", se.Phase)
	}
	if se.Class != "rate_limited" {
		t.Errorf("Class = %q, want rate_limited", se.Class)
	}
	if se.HTTPStatus == nil || *se.HTTPStatus != 429 {
		t.Errorf("HTTPStatus = %v, want 429", se.HTTPStatus)
	}
	if se.Attempt == nil || *se.Attempt != 1 {
		t.Errorf("Attempt = %v, want 1", se.Attempt)
	}
	if se.MaxAttempts == nil || *se.MaxAttempts != 10 {
		t.Errorf("MaxAttempts = %v, want 10", se.MaxAttempts)
	}
	if se.RetryIn == nil || *se.RetryIn != 5*time.Second {
		t.Errorf("RetryIn = %v, want 5s", se.RetryIn)
	}
	if !se.ClearedByTurnBoundary() {
		t.Error("ErrorPhaseRetrying must clear on the next turn boundary")
	}
}

func TestParseLine_TaskStatus_RetryScheduled_NoHTTPStatus_NilNotZero(t *testing.T) {
	// A "transport"/"decode" error_kind genuinely carries no http_status in
	// the real corpus — OptInt must read that as nil, not a fabricated 0.
	ev := (&Parser{}).ParseLine(line(t, `{"payload_type":"runtime.session","payload":{"kind":"task",
		"run_id":"r1","task_id":"task_1","event":{"kind":"status","task_id":"task_1",
		"message":"retrying meta model stream in 5000ms (attempt 2/10)",
		"details":{"phase":"retry_scheduled","facets":[
		{"kind":"external_attempt","system":"meta","operation":"model.response",
		"attempt":1,"next_attempt":2,"max_attempts":10,"retry_delay_ms":5000,
		"error_kind":"transport"}
		]}}}}`))
	if ev.SessionError == nil {
		t.Fatal("expected SessionError")
	}
	if ev.SessionError.HTTPStatus != nil {
		t.Errorf("HTTPStatus = %v, want nil for a transport error_kind", ev.SessionError.HTTPStatus)
	}
}

func TestParseLine_TaskStatus_OtherPhases_Skipped(t *testing.T) {
	for _, phase := range []string{"opening_stream", "stream_succeeded"} {
		t.Run(phase, func(t *testing.T) {
			ev := (&Parser{}).ParseLine(line(t, `{"payload_type":"runtime.session","payload":{"kind":"task",
				"run_id":"r1","task_id":"task_1","event":{"kind":"status","task_id":"task_1",
				"details":{"phase":"`+phase+`","facets":[{"kind":"external_attempt","attempt":1,"max_attempts":10}]}}}}`))
			if !ev.Skip {
				t.Error("expected Skip=true")
			}
			if ev.SessionError != nil {
				t.Errorf("SessionError = %+v, want nil", ev.SessionError)
			}
		})
	}
}

// --- kind:"run" / event.kind:"model_completed" (token accounting) ---

func TestParseLine_ModelCompleted_NetsCacheReadFromInput(t *testing.T) {
	setHome(t) // no catalog fixture — isolates this test from the real machine's model-catalog
	// Verified live against 476 real model_completed records: input_tokens
	// is inclusive of cache_read_tokens (never smaller), the OpenAI-
	// Responses-API convention codex's parser also corrects for.
	ev := (&Parser{}).ParseLine(line(t, `{"payload_type":"runtime.session","payload":{"kind":"run",
		"run_id":"r1","event":{"kind":"model_completed","usage":{"input_tokens":35023,
		"output_tokens":378,"cached_tokens":29297,"cache_write_tokens":0,
		"cache_read_tokens":29297,"reasoning_tokens":76},"duration_ms":4343,
		"model":"muse-spark-1.3-contributor"}}}`))
	if !ev.Skip {
		t.Error("model_completed should be Skip=true bookkeeping")
	}
	if ev.Contribution == nil {
		t.Fatal("expected a Contribution")
	}
	want := tailer.UsageBreakdown{Input: 35023 - 29297, Output: 378, CacheRead: 29297}
	if ev.Contribution.Usage != want {
		t.Errorf("Usage = %+v, want %+v", ev.Contribution.Usage, want)
	}
	if ev.ModelName != "muse-spark-1.3-contributor" {
		t.Errorf("ModelName = %q", ev.ModelName)
	}
	if ev.Tokens == nil || ev.Tokens.Input != want.Input || ev.Tokens.Output != want.Output {
		t.Errorf("Tokens = %+v, want Input=%d Output=%d", ev.Tokens, want.Input, want.Output)
	}
	// Defect 1 (issue #1960 stage 3): Total was never set at all, so
	// SessionMetrics.TotalTokens (and the context-utilization percentage
	// derived from it) stayed 0 forever. want.Total sums the same four
	// buckets the fix now sums — CacheCreation5m is 0 here (no
	// cache_write_tokens in this fixture).
	wantTotal := want.Input + want.Output + want.CacheRead + want.CacheCreation5m
	if ev.Tokens == nil || ev.Tokens.Total != wantTotal {
		t.Errorf("Tokens.Total = %v, want %d", ev.Tokens, wantTotal)
	}
}

func TestParseLine_ModelCompleted_FallsBackToCachedTokens(t *testing.T) {
	setHome(t) // "m" isn't a real model, but isolate anyway for hermeticity
	// The --provider echo shape observed live omits cache_read_tokens
	// entirely and carries only cached_tokens.
	ev := (&Parser{}).ParseLine(line(t, `{"payload_type":"runtime.session","payload":{"kind":"run",
		"run_id":"r1","event":{"kind":"model_completed","usage":{"input_tokens":100,
		"output_tokens":10,"cached_tokens":40},"model":"m"}}}`))
	if ev.Contribution.Usage.CacheRead != 40 {
		t.Errorf("CacheRead = %d, want 40 (fallback to cached_tokens)", ev.Contribution.Usage.CacheRead)
	}
	if ev.Contribution.Usage.Input != 60 {
		t.Errorf("Input = %d, want 60 (100-40)", ev.Contribution.Usage.Input)
	}
	if ev.Tokens == nil || ev.Tokens.Total != 110 {
		t.Errorf("Tokens.Total = %v, want 110 (60 fresh input + 10 output + 40 cache-read)", ev.Tokens)
	}
}

func TestParseLine_ModelCompleted_AllZeroNoModel_NoContribution(t *testing.T) {
	// The real --provider echo shape: no "model" key, all-zero usage.
	ev := (&Parser{}).ParseLine(line(t, `{"payload_type":"runtime.session","payload":{"kind":"run",
		"run_id":"r1","event":{"kind":"model_completed","usage":{"input_tokens":0,
		"output_tokens":0,"cached_tokens":0,"reasoning_tokens":0},"duration_ms":3}}}`))
	if ev.Contribution != nil {
		t.Errorf("Contribution = %+v, want nil for an all-zero, model-less event", ev.Contribution)
	}
	if ev.Tokens != nil {
		t.Errorf("Tokens = %+v, want nil", ev.Tokens)
	}
}

func TestParseLine_GoalUsageAttribution_SkippedNotDoubleCounted(t *testing.T) {
	ev := (&Parser{}).ParseLine(line(t, `{"payload_type":"runtime.session","payload":{"kind":"run",
		"run_id":"r1","event":{"kind":"goal_usage_attribution","record":{"usage_id":"u1",
		"quantity":{"unit":"tokens","input_tokens":29356,"output_tokens":99,"cached_tokens":0,
		"reasoning_tokens":28},"owner":{"requester_kind":"main","owner_type":"main_root"}}}}}`))
	if !ev.Skip {
		t.Error("goal_usage_attribution should be Skip=true")
	}
	if ev.Contribution != nil {
		t.Errorf("Contribution = %+v, want nil — model_completed already counted this usage", ev.Contribution)
	}
}

// --- kind:"run" / event.kind:"assistant_message_committed" ---

func TestParseLine_AssistantMessageCommitted_WaitingCue(t *testing.T) {
	ev := (&Parser{}).ParseLine(line(t, `{"payload_type":"runtime.session","payload":{"kind":"run",
		"run_id":"r1","event":{"kind":"assistant_message_committed",
		"text":"Want me to drill into projects/ or filter out dotfiles?"}}}`))
	if ev.EventType != "assistant_message" {
		t.Errorf("EventType = %q, want assistant_message", ev.EventType)
	}
	if !ev.PendingWaitingCue {
		t.Error("expected PendingWaitingCue for a trailing question")
	}
}

func TestParseLine_AssistantMessageCommitted_EmptyTextNoWaitingCue(t *testing.T) {
	ev := (&Parser{}).ParseLine(line(t, `{"payload_type":"runtime.session","payload":{"kind":"run",
		"run_id":"r1","event":{"kind":"assistant_message_committed","text":""}}}`))
	if ev.EventType != "assistant_message" {
		t.Errorf("EventType = %q, want assistant_message", ev.EventType)
	}
	if ev.PendingWaitingCue {
		t.Error("empty text must never set PendingWaitingCue")
	}
}

// Marker early in a long message must survive — AssistantText keeps only the
// last 200 runes (issue #558 contract, mirrored from codex/aider/opencode/pi).
func TestParseLine_AssistantMessageCommitted_TaskEstimate_SurvivesTruncation(t *testing.T) {
	long := `<!-- {"marker":"irrlicht-eta","total_rounds":6,"completed_rounds":2} --> `
	for i := 0; i < 50; i++ {
		long += "filler prose "
	}
	raw, err := json.Marshal(map[string]any{
		"payload_type": "runtime.session",
		"payload": map[string]any{
			"kind":   "run",
			"run_id": "r1",
			"event": map[string]any{
				"kind": "assistant_message_committed",
				"text": long,
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	ev := (&Parser{}).ParseLine(line(t, string(raw)))
	if ev.TaskEstimate == nil {
		t.Fatal("expected TaskEstimate from full-text scan")
	}
	if ev.TaskEstimate.TotalRounds != 6 || ev.TaskEstimate.CompletedRounds != 2 {
		t.Errorf("rounds = %d/%d, want 2/6", ev.TaskEstimate.CompletedRounds, ev.TaskEstimate.TotalRounds)
	}
}

// --- kind:"run" / event.kind:"todo_snapshot_updated" ---

// todoSnapshotLine builds a todo_snapshot_updated record in the shape a real
// muse write_todos call emits (live-confirmed:
// ~/.local/share/muse/sessions/2026/09/13/01a09c47-506a-7763-b82a-9ab89a9e195a/session.jsonl,
// revisions 1-4).
func todoSnapshotLine(t *testing.T, revision int, items []map[string]any) map[string]any {
	t.Helper()
	its := make([]any, 0, len(items))
	for _, it := range items {
		its = append(its, it)
	}
	raw, err := json.Marshal(map[string]any{
		"payload_type": "runtime.session",
		"payload": map[string]any{
			"kind":   "run",
			"run_id": "r1",
			"event": map[string]any{
				"kind":        "todo_snapshot_updated",
				"revision":    revision,
				"source_tool": "write_todos",
				"items":       its,
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return line(t, string(raw))
}

func TestParseLine_TodoSnapshotUpdated_FirstRevisionCreates(t *testing.T) {
	p := &Parser{}
	ev := p.ParseLine(todoSnapshotLine(t, 1, []map[string]any{
		{"text": "Task A", "status": "in_progress"},
		{"text": "Task B", "status": "pending"},
		{"text": "Task C", "status": "pending"},
	}))
	if ev == nil || ev.Skip {
		t.Fatal("expected non-skipped event")
	}
	if len(ev.TaskDeltas) != 4 {
		t.Fatalf("TaskDeltas len = %d, want 4 (3 creates + 1 update for Task A's in_progress)", len(ev.TaskDeltas))
	}
	if ev.TaskDeltas[0] != (tailer.TaskDelta{Op: tailer.TaskOpCreate, Subject: "Task A"}) {
		t.Errorf("deltas[0] = %+v, want create Task A", ev.TaskDeltas[0])
	}
	if ev.TaskDeltas[1] != (tailer.TaskDelta{Op: tailer.TaskOpUpdate, ID: "1", Status: "in_progress"}) {
		t.Errorf("deltas[1] = %+v, want update id=1 status=in_progress", ev.TaskDeltas[1])
	}
	if ev.TaskDeltas[2] != (tailer.TaskDelta{Op: tailer.TaskOpCreate, Subject: "Task B"}) {
		t.Errorf("deltas[2] = %+v, want create Task B", ev.TaskDeltas[2])
	}
	if ev.TaskDeltas[3] != (tailer.TaskDelta{Op: tailer.TaskOpCreate, Subject: "Task C"}) {
		t.Errorf("deltas[3] = %+v, want create Task C", ev.TaskDeltas[3])
	}
	if ev.TaskSnapshot == nil || len(*ev.TaskSnapshot) != 3 {
		t.Fatalf("TaskSnapshot = %+v, want 3 entries", ev.TaskSnapshot)
	}
}

func TestParseLine_TodoSnapshotUpdated_SecondRevisionWalksStatuses(t *testing.T) {
	p := &Parser{}
	p.ParseLine(todoSnapshotLine(t, 1, []map[string]any{
		{"text": "Task A", "status": "in_progress"},
		{"text": "Task B", "status": "pending"},
	}))
	ev := p.ParseLine(todoSnapshotLine(t, 2, []map[string]any{
		{"text": "Task A", "status": "completed"},
		{"text": "Task B", "status": "in_progress"},
	}))
	if ev == nil || ev.Skip {
		t.Fatal("expected non-skipped event")
	}
	if len(ev.TaskDeltas) != 2 {
		t.Fatalf("TaskDeltas len = %d, want 2 (Task A completed, Task B in_progress)", len(ev.TaskDeltas))
	}
	if ev.TaskDeltas[0] != (tailer.TaskDelta{Op: tailer.TaskOpUpdate, ID: "1", Status: "completed"}) {
		t.Errorf("deltas[0] = %+v, want update id=1 status=completed", ev.TaskDeltas[0])
	}
	if ev.TaskDeltas[1] != (tailer.TaskDelta{Op: tailer.TaskOpUpdate, ID: "2", Status: "in_progress"}) {
		t.Errorf("deltas[1] = %+v, want update id=2 status=in_progress", ev.TaskDeltas[1])
	}
}

func TestParseLine_TodoSnapshotUpdated_EmptyItems_Skipped(t *testing.T) {
	ev := (&Parser{}).ParseLine(todoSnapshotLine(t, 1, nil))
	if !ev.Skip {
		t.Error("expected Skip=true for an empty items list")
	}
}

func TestParseLine_TodoSnapshotUpdated_NeverLooksLikeTurnDone(t *testing.T) {
	ev := (&Parser{}).ParseLine(todoSnapshotLine(t, 1, []map[string]any{
		{"text": "Task A", "status": "pending"},
	}))
	if ev.EventType == "turn_done" || ev.EventType == "assistant" || ev.EventType == "assistant_output" {
		t.Errorf("EventType = %q must not match any session.SessionMetrics.IsAgentDone string", ev.EventType)
	}
}

// --- kind:"run" / assistant_tool_calls_committed + tool_result_batch_committed ---

func TestParseLine_ToolCall_OpenAndClose_PairByCallID(t *testing.T) {
	p := &Parser{}
	open := p.ParseLine(line(t, `{"payload_type":"runtime.session","payload":{"kind":"run",
		"run_id":"r1","event":{"kind":"assistant_tool_calls_committed","message_id":"m1",
		"tool_calls":[{"id":"fc_1","call_id":"call_1","name":"bash",
		"args":"{\"command\":\"ls -la\"}"}]}}}`))
	if open.EventType != "function_call" {
		t.Errorf("EventType = %q, want function_call", open.EventType)
	}
	if len(open.ToolUses) != 1 || open.ToolUses[0].ID != "call_1" || open.ToolUses[0].Name != "bash" {
		t.Errorf("ToolUses = %+v", open.ToolUses)
	}

	closeEv := p.ParseLine(line(t, `{"payload_type":"runtime.session","payload":{"kind":"run",
		"run_id":"r1","event":{"kind":"tool_result_batch_committed","batch_id":"m1",
		"results":[{"tool_call_index":0,"tool_call_id":"call_1",
		"text":"{\"chunk_id\":\"e1\",\"exit_code\":0,\"terminal_status\":\"completed\",\"output\":\"ok\"}"}]}}}`))
	if closeEv.EventType != "function_call_output" {
		t.Errorf("EventType = %q, want function_call_output", closeEv.EventType)
	}
	if len(closeEv.ToolResultIDs) != 1 || closeEv.ToolResultIDs[0] != "call_1" {
		t.Errorf("ToolResultIDs = %v", closeEv.ToolResultIDs)
	}
	if closeEv.IsError {
		t.Error("exit_code 0 must not set IsError")
	}
}

func TestParseLine_ToolResult_NonZeroExitCode_SetsIsError(t *testing.T) {
	// SYNTHETIC: no failing command was ever captured in the available
	// corpus (every real sample succeeded, exit_code 0) — this pins the
	// best-effort sniff's intended behavior on a shape it has never actually
	// seen live. See resultTextReportsNonZeroExit's doc.
	ev := (&Parser{}).ParseLine(line(t, `{"payload_type":"runtime.session","payload":{"kind":"run",
		"run_id":"r1","event":{"kind":"tool_result_batch_committed","batch_id":"m1",
		"results":[{"tool_call_index":0,"tool_call_id":"call_1",
		"text":"{\"chunk_id\":\"e1\",\"exit_code\":1,\"terminal_status\":\"completed\",\"output\":\"no such file\"}"}]}}}`))
	if !ev.IsError {
		t.Error("expected IsError for a nonzero exit_code")
	}
}

func TestParseLine_ToolResult_UnrecognizedTextShape_NoErrorGuessed(t *testing.T) {
	ev := (&Parser{}).ParseLine(line(t, `{"payload_type":"runtime.session","payload":{"kind":"run",
		"run_id":"r1","event":{"kind":"tool_result_batch_committed","batch_id":"m1",
		"results":[{"tool_call_index":0,"tool_call_id":"call_1","text":"reminder decision recorded"}]}}}`))
	if ev.IsError {
		t.Error("plain, non-JSON result text must never be read as a failure")
	}
}

// --- tool_batch.effect.terminal (task_completion.kind) / inbox_item_queued ---

func TestParseLine_ToolBatchEffectTerminal_PendingCompletion_OpensBackgroundSpawn(t *testing.T) {
	ev := (&Parser{}).ParseLine(line(t, `{"payload_type":"tool_batch.effect.terminal",
		"payload":{"kind":"tool_batch_effect","run_id":"r1","record":{"kind":"terminal",
		"effect_id":"e1","task_id":"task_1","call_id":"call_1",
		"outcome":{"kind":"completed","task_completion":{"kind":"pending"},"output_ref_count":0}}}}`))
	if ev.Skip {
		t.Fatal("expected non-skipped event for a pending task_completion")
	}
	if len(ev.BackgroundSpawns) != 1 || ev.BackgroundSpawns[0].BashID != "task_1" {
		t.Errorf("BackgroundSpawns = %+v, want one spawn for task_1", ev.BackgroundSpawns)
	}
	if ev.EventType == "turn_done" || ev.EventType == "assistant" || ev.EventType == "assistant_output" {
		t.Errorf("EventType = %q must not match any IsAgentDone string", ev.EventType)
	}
}

func TestParseLine_ToolBatchEffectTerminal_OrdinaryClose_Skipped(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
	}{
		{"terminal", `"outcome":{"kind":"completed","task_completion":{"kind":"terminal","terminal":{"kind":"completed"}},"output_ref_count":0}`},
		{"complete", `"outcome":{"kind":"completed","task_completion":{"kind":"complete"},"output_ref_count":0}`},
		{"cancelled_no_task_completion", `"outcome":{"kind":"cancelled"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ev := (&Parser{}).ParseLine(line(t, `{"payload_type":"tool_batch.effect.terminal",
				"payload":{"kind":"tool_batch_effect","run_id":"r1","record":{"kind":"terminal",
				"effect_id":"e1","task_id":"task_1","call_id":"call_1",`+tc.body+`}}}`))
			if !ev.Skip {
				t.Error("expected Skip=true for a non-pending task_completion")
			}
			if len(ev.BackgroundSpawns) != 0 {
				t.Errorf("BackgroundSpawns = %+v, want none", ev.BackgroundSpawns)
			}
		})
	}
}

func TestParseLine_InboxItemQueued_BackgroundTaskTerminal_ClosesSpawn(t *testing.T) {
	ev := (&Parser{}).ParseLine(line(t, `{"payload_type":"runtime.session","payload":{"kind":"run",
		"run_id":"r1","event":{"kind":"inbox_item_queued","item_id":"i1",
		"source":{"source":"background_task_terminal","task_id":"task_1",
		"terminal_kind":"completed"}}}}`))
	if !ev.Skip {
		t.Error("expected Skip=true (bookkeeping, folded via applySkippedEvent)")
	}
	if !ev.OriginTaskNotification {
		t.Error("expected OriginTaskNotification=true")
	}
	if len(ev.TerminatedBackgroundTaskIDs) != 1 || ev.TerminatedBackgroundTaskIDs[0] != "task_1" {
		t.Errorf("TerminatedBackgroundTaskIDs = %v, want [task_1]", ev.TerminatedBackgroundTaskIDs)
	}
}

func TestParseLine_InboxItemQueued_OtherSources_NoTermination(t *testing.T) {
	for _, source := range []string{"subagent_result", "user_steer"} {
		t.Run(source, func(t *testing.T) {
			ev := (&Parser{}).ParseLine(line(t, `{"payload_type":"runtime.session","payload":{"kind":"run",
				"run_id":"r1","event":{"kind":"inbox_item_queued","item_id":"i1",
				"source":{"source":"`+source+`","task_id":"task_1"}}}}`))
			if !ev.Skip {
				t.Error("expected Skip=true")
			}
			if len(ev.TerminatedBackgroundTaskIDs) != 0 {
				t.Errorf("TerminatedBackgroundTaskIDs = %v, want none", ev.TerminatedBackgroundTaskIDs)
			}
		})
	}
}

// End-to-end: BackgroundProcessCount reaches SessionMetrics and clears on the
// real corpus's own spawn/terminate pair (issue #1960's 3-3_background-process).
func TestBackgroundProcess_SpawnThenTerminate_CountsThenClears(t *testing.T) {
	// tailFixture writes each slice element as one physical JSONL line, so
	// (unlike the line() helper used elsewhere in this file, which parses a
	// single string as a whole document) these must be single-line JSON.
	spawn := `{"payload_type":"tool_batch.effect.terminal","payload":{"kind":"tool_batch_effect","run_id":"r1","record":{"kind":"terminal","effect_id":"e1","task_id":"task_1","call_id":"call_1","outcome":{"kind":"completed","task_completion":{"kind":"pending"},"output_ref_count":0}}}}`
	terminate := `{"payload_type":"runtime.session","payload":{"kind":"run","run_id":"r1","event":{"kind":"inbox_item_queued","item_id":"i1","source":{"source":"background_task_terminal","task_id":"task_1","terminal_kind":"completed"}}}}`

	m := tailFixture(t, []string{spawn})
	if m.BackgroundProcessCount != 1 {
		t.Fatalf("BackgroundProcessCount after spawn = %d, want 1", m.BackgroundProcessCount)
	}

	m = tailFixture(t, []string{spawn, terminate})
	if m.BackgroundProcessCount != 0 {
		t.Fatalf("BackgroundProcessCount after terminate = %d, want 0", m.BackgroundProcessCount)
	}
}

// --- kind:"approval" ---

// approvalLine builds a kind:"approval" record in the shape a real Muse
// approval-lifecycle event emits (format-spec §5; field shapes cross-checked
// against testdata/real-approval-flow.jsonl, a real redacted capture). extra
// is merged into event on top of kind/pending_action_id, so each call only
// has to supply the fields it cares about discriminating on
// (presentation_phase, status, timed_out, failure, ...).
func approvalLine(t *testing.T, eventKind, pendingActionID string, extra map[string]any) map[string]any {
	t.Helper()
	event := map[string]any{
		"kind":              eventKind,
		"pending_action_id": pendingActionID,
	}
	for k, v := range extra {
		event[k] = v
	}
	raw, err := json.Marshal(map[string]any{
		"payload_type": "runtime.session",
		"payload": map[string]any{
			"kind":   "approval",
			"run_id": "r1",
			"event":  event,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return line(t, string(raw))
}

// TestParseLine_ApprovalRequested_PresentationPhase is table-driven over
// requested's presentation_phase — issue #1978's actual discriminator.
// Corpus-scanned across all 148 top-level muse sessions under
// ~/.local/share/muse/sessions/ (nested subagent copies excluded — they fold
// into the parent session): "automated_reviewing" (68 occurrences) means
// muse's own :auto-review LLM judge decides first, no user involved yet;
// "human_pending" (9 occurrences) means the user must decide right now. Every
// other row here (missing/unrecognized value) is a fail-safe case, not one
// observed in the corpus.
//
// automated_reviewing must NOT open a wait for that pending_action_id — RED
// today, because parseApprovalRequested opens unconditionally on every
// "requested" event regardless of presentation_phase. Every other case must
// open, including a missing or unrecognized value: a wait this parser cannot
// classify is shown as waiting rather than hidden, and that also preserves
// today's behavior (which never reads presentation_phase at all) for any
// future value muse adds.
func TestParseLine_ApprovalRequested_PresentationPhase(t *testing.T) {
	tests := []struct {
		name      string
		extra     map[string]any
		wantOpens bool
	}{
		{"human_pending_lock", map[string]any{"presentation_phase": "human_pending"}, true},
		{"automated_reviewing_red", map[string]any{"presentation_phase": "automated_reviewing"}, false},
		{"missing_failsafe_lock", map[string]any{}, true},
		{"unknown_value_failsafe_lock", map[string]any{"presentation_phase": "some_future_phase"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			extra := map[string]any{"tool_call_id": "call_1", "tool_name": "bash"}
			for k, v := range tt.extra {
				extra[k] = v
			}
			ev := (&Parser{}).ParseLine(approvalLine(t, "requested", "pa1", extra))
			opened := len(ev.PermissionRequestIDs) == 1 && ev.PermissionRequestIDs[0] == "pa1"
			if opened != tt.wantOpens {
				t.Errorf("opened = %v (PermissionRequestIDs=%v), want opens=%v", opened, ev.PermissionRequestIDs, tt.wantOpens)
			}
		})
	}
}

// TestParseLine_ApprovalReviewCompleted_OutcomeDiscriminator is table-driven
// over automated_review_completed's outcome. A clean automated approval
// (status=approved, timed_out=false, failure=null) must stay resolved and
// open nothing — that row is a LOCK: today's unconditional Skip=true in
// parseApprovalEvent's default branch already leaves nothing open for it.
// Every other row must RE-OPEN the prompt for the same pending_action_id,
// because the wait has genuinely become the user's — those rows are RED
// today, since automated_review_completed is skipped regardless of its
// outcome right now.
func TestParseLine_ApprovalReviewCompleted_OutcomeDiscriminator(t *testing.T) {
	tests := []struct {
		name      string
		extra     map[string]any
		wantOpens bool
	}{
		{"clean_approval_lock", map[string]any{"status": "approved", "timed_out": false, "failure": nil}, false},
		{"escalated_red", map[string]any{"status": "escalated", "timed_out": false, "failure": nil}, true},
		{"timed_out_red", map[string]any{"status": "approved", "timed_out": true, "failure": nil}, true},
		{"failure_red", map[string]any{"status": "approved", "timed_out": false, "failure": map[string]any{"kind": "provider_error"}}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ev := (&Parser{}).ParseLine(approvalLine(t, "automated_review_completed", "pa1", tt.extra))
			opened := len(ev.PermissionRequestIDs) == 1 && ev.PermissionRequestIDs[0] == "pa1"
			if opened != tt.wantOpens {
				t.Errorf("opened = %v (PermissionRequestIDs=%v, Skip=%v), want opens=%v",
					opened, ev.PermissionRequestIDs, ev.Skip, tt.wantOpens)
			}
		})
	}
}

// TestParseLine_ApprovalFlow_HumanPending_NoAutoReview_OpensThenCloses is a
// LOCK: a muse profile without auto-review still genuinely asks the user —
// requested carries presentation_phase="human_pending" and there is no
// automated_review_* pair at all, just requested -> decision_applied. Passes
// before and after the fix.
func TestParseLine_ApprovalFlow_HumanPending_NoAutoReview_OpensThenCloses(t *testing.T) {
	p := &Parser{}
	req := p.ParseLine(approvalLine(t, "requested", "pa-human", map[string]any{
		"presentation_phase": "human_pending",
		"tool_call_id":       "call_1",
		"tool_name":          "bash",
	}))
	if len(req.PermissionRequestIDs) != 1 || req.PermissionRequestIDs[0] != "pa-human" {
		t.Fatalf("PermissionRequestIDs = %v, want [pa-human]", req.PermissionRequestIDs)
	}

	done := p.ParseLine(approvalLine(t, "decision_applied", "pa-human", map[string]any{
		"decision":      "approved",
		"policy_result": "allow",
	}))
	if len(done.PermissionResolvedIDs) != 1 || done.PermissionResolvedIDs[0] != "pa-human" {
		t.Fatalf("PermissionResolvedIDs = %v, want [pa-human]", done.PermissionResolvedIDs)
	}
}

// TestParseLine_ApprovalRequested_OpensPermission carries no
// presentation_phase at all, so it also doubles as the
// missing-field-fail-safe lock alongside
// TestParseLine_ApprovalRequested_PresentationPhase's missing_failsafe_lock
// row above.
func TestParseLine_ApprovalRequested_OpensPermission(t *testing.T) {
	ev := (&Parser{}).ParseLine(line(t, `{"payload_type":"runtime.session","payload":{"kind":"approval",
		"run_id":"r1","event":{"kind":"requested","pending_action_id":"pa1",
		"tool_call_id":"call_1","tool_name":"bash"}}}`))
	if ev.EventType != "permission_requested" {
		t.Errorf("EventType = %q, want permission_requested", ev.EventType)
	}
	if len(ev.PermissionRequestIDs) != 1 || ev.PermissionRequestIDs[0] != "pa1" {
		t.Errorf("PermissionRequestIDs = %v", ev.PermissionRequestIDs)
	}
}

func TestParseLine_ApprovalDecisionApplied_ClosesPermission(t *testing.T) {
	ev := (&Parser{}).ParseLine(line(t, `{"payload_type":"runtime.session","payload":{"kind":"approval",
		"event":{"kind":"decision_applied","pending_action_id":"pa1","decision":"approved",
		"policy_result":"allow"}}}`))
	if ev.EventType != "permission_completed" {
		t.Errorf("EventType = %q, want permission_completed", ev.EventType)
	}
	if len(ev.PermissionResolvedIDs) != 1 || ev.PermissionResolvedIDs[0] != "pa1" {
		t.Errorf("PermissionResolvedIDs = %v", ev.PermissionResolvedIDs)
	}
}

// TestParseLine_ApprovalBookkeeping_Skipped covers only the two approval
// event kinds that stay pure bookkeeping under issue #1978's fix:
// automated_review_started never became a close signal (the corrected design
// keys off requested's own presentation_phase instead), and
// stage_requirement_resolved is unrelated per-requirement noise. It
// deliberately no longer covers automated_review_completed —
// TestParseLine_ApprovalReviewCompleted_OutcomeDiscriminator above tests that
// one in full, because its outcome (status/timed_out/failure) now decides
// whether it re-opens the prompt.
func TestParseLine_ApprovalBookkeeping_Skipped(t *testing.T) {
	for _, kind := range []string{"automated_review_started", "stage_requirement_resolved"} {
		ev := (&Parser{}).ParseLine(line(t, `{"payload_type":"runtime.session","payload":{"kind":"approval",
			"event":{"kind":"`+kind+`","pending_action_id":"pa1"}}}`))
		if !ev.Skip {
			t.Errorf("%s: expected Skip=true (bookkeeping about HOW a decision was reached)", kind)
		}
		if len(ev.PermissionRequestIDs) != 0 || len(ev.PermissionResolvedIDs) != 0 {
			t.Errorf("%s: must not itself open/close a permission wait", kind)
		}
	}
}

// TestParseLine_ApprovalFlow_RealFixture replays a real, redacted approval
// flow (requested -> automated_review_started -> automated_review_completed
// -> decision_applied) end to end. real-approval-flow.jsonl's own requested
// record carries presentation_phase="automated_reviewing", so this is
// user-observable, RED-FIRST evidence for issue #1978: for the whole review
// window (before the judge's automated_review_completed verdict lands), no
// wait should be visible, because the judge is deciding, not the user. That
// assertion fails today — parseApprovalRequested opens on every "requested"
// event regardless of presentation_phase, and automated_review_started
// (Skip=true) never closes it. The end-state assertion below is unchanged
// and already passes: decision_applied closes the wait unconditionally.
func TestParseLine_ApprovalFlow_RealFixture(t *testing.T) {
	lines := fixtureLines(t, "real-approval-flow.jsonl")

	reviewing := tailFixture(t, lines[:2]) // requested -> automated_review_started
	if reviewing.TranscriptPermissionPending {
		t.Error("TranscriptPermissionPending = true during the judge's own automated review window, want false (issue #1978)")
	}

	m := tailFixture(t, lines)
	if m.TranscriptPermissionPending {
		t.Error("TranscriptPermissionPending should be false once decision_applied closes it")
	}
}

// TestParseLine_ApprovalFlow_Escalated_ReviewWindow_NotWaiting is RED-FIRST,
// user-observable evidence. real-approval-flow-escalated.jsonl is a captured,
// redacted copy of the reporter's OWN escalated approval — the five records
// at seq 1157/1158/1217/1268/1269 of session 01a0ab28-…, renumbered 1..5 with
// workspace paths scrubbed and nothing else altered, so its recorded_at gaps
// are the real 18.73s and 40.89s cited below. Its requested record carries
// presentation_phase="automated_reviewing", so the same
// review-window claim as the approved flow above applies — no wait should be
// visible while the judge is still deciding. Fails today for the same
// reason. Measured on the reporter's own transcript (issue #1978 triage):
// this review window ran 18.73s wrongly reported as waiting.
func TestParseLine_ApprovalFlow_Escalated_ReviewWindow_NotWaiting(t *testing.T) {
	lines := fixtureLines(t, "real-approval-flow-escalated.jsonl")
	m := tailFixture(t, lines[:2]) // requested -> automated_review_started
	if m.TranscriptPermissionPending {
		t.Error("TranscriptPermissionPending = true during the judge's review window, want false (issue #1978)")
	}
}

// TestParseLine_ApprovalFlow_Escalated_JudgeEscalates_ThenResolved is a LOCK,
// two-step: once the judge escalates (status=escalated, outcome=escalate),
// the wait genuinely becomes the user's and must be open — true both today
// (requested already opened it, unconditionally) and after the fix
// (automated_review_completed's outcome discriminator re-opens it). Then
// decision_applied still closes it unconditionally, exactly as in the
// approved flow. Neither assertion in this test changes behavior across the
// fix; it pins the arc the fix must not break. Measured on the reporter's own
// transcript: escalation-to-resolution ran a genuine 22.16s (40.89s total
// minus the 18.73s review window above) — this really was the user's wait.
func TestParseLine_ApprovalFlow_Escalated_JudgeEscalates_ThenResolved(t *testing.T) {
	lines := fixtureLines(t, "real-approval-flow-escalated.jsonl")

	escalated := tailFixture(t, lines[:3]) // + automated_review_completed{status:escalated}
	if !escalated.TranscriptPermissionPending {
		t.Error("TranscriptPermissionPending = false after an escalated review, want true — the user genuinely must decide now")
	}

	m := tailFixture(t, lines) // + decision_applied
	if m.TranscriptPermissionPending {
		t.Error("TranscriptPermissionPending should be false once decision_applied closes it")
	}
}

// --- metadata / route_facts / model identity ---

func TestParseLine_Metadata_AgentVersion(t *testing.T) {
	ev := (&Parser{}).ParseLine(line(t, `{"payload_type":"runtime.session.metadata",
		"payload":{"kind":"metadata","record":{"workspace_root":"/x",
		"build":{"sha":"f9fb6cc301","semver":"1.2.1"}}}}`))
	if !ev.Skip {
		t.Error("metadata should be Skip=true")
	}
	if ev.AgentVersion != "1.2.1" {
		t.Errorf("AgentVersion = %q, want 1.2.1", ev.AgentVersion)
	}
}

func TestParseLine_RouteFacts_CWD(t *testing.T) {
	ev := (&Parser{}).ParseLine(line(t, `{"payload_type":"runtime.session.route_facts",
		"payload":{"kind":"route_facts","record":{"cwd":"/Users/x/projects/irrlicht","pid":123}}}`))
	if !ev.Skip {
		t.Error("route_facts should be Skip=true")
	}
	if ev.CWD != "/Users/x/projects/irrlicht" {
		t.Errorf("CWD = %q", ev.CWD)
	}
}

func TestParseLine_RunModelConfigured_SetsModelName(t *testing.T) {
	setHome(t) // no catalog present — ContextWindow must stay 0, not guessed
	ev := (&Parser{}).ParseLine(line(t, `{"payload_type":"run.model.configured",
		"payload":{"kind":"run_model","record":{"provider_id":"meta","model_id":"muse-spark-1.3-contributor",
		"source":"startup"}}}`))
	if !ev.Skip {
		t.Error("run.model.configured should be Skip=true")
	}
	if ev.ModelName != "muse-spark-1.3-contributor" {
		t.Errorf("ModelName = %q", ev.ModelName)
	}
	if ev.ContextWindow != 0 {
		t.Errorf("ContextWindow = %d, want 0 (no model-catalog fixture in this test's isolated $HOME)", ev.ContextWindow)
	}
}

// TestParseLine_RunModelConfigured_SetsContextWindowFromCatalog is the
// positive twin of the test above: with a model-catalog fixture present
// under an isolated $HOME, run.model.configured must resolve ContextWindow
// alongside ModelName — the exact wiring the model-context-display cell
// (replaydata/agents/muse/scenarios/1-8_model-context-display/metadata.json)
// named as missing.
func TestParseLine_RunModelConfigured_SetsContextWindowFromCatalog(t *testing.T) {
	home := setHome(t)
	writeCatalog(t, home, "catalog.json", modelCatalogRow{ModelID: "muse-spark-1.3-contributor", ContextLimit: 1007997})
	ev := (&Parser{}).ParseLine(line(t, `{"payload_type":"run.model.configured",
		"payload":{"kind":"run_model","record":{"provider_id":"meta","model_id":"muse-spark-1.3-contributor",
		"source":"startup"}}}`))
	if ev.ContextWindow != 1007997 {
		t.Errorf("ContextWindow = %d, want 1007997", ev.ContextWindow)
	}
}

func TestParseLine_ModelReconfigure_UsesEffectiveModel(t *testing.T) {
	setHome(t)
	ev := (&Parser{}).ParseLine(line(t, `{"payload_type":"runtime.model_reconfigure.completed",
		"payload":{"kind":"model_reconfigure","record":{"previous":{"model_id":""},
		"effective":{"model_id":"muse-spark-1.3-contributor"}}}}`))
	if !ev.Skip {
		t.Error("model_reconfigure should be Skip=true")
	}
	if ev.ModelName != "muse-spark-1.3-contributor" {
		t.Errorf("ModelName = %q", ev.ModelName)
	}
}

// --- session.end / command.invoked / unknown ---

func TestParseLine_SessionEnd_Skipped(t *testing.T) {
	ev := (&Parser{}).ParseLine(line(t, `{"payload_type":"session.end","payload":{"kind":"session_end",
		"record":{"session_id":"s1","exit_reason":"clean","uptime_ms":416717}}}`))
	if !ev.Skip {
		t.Error("session.end should be Skip=true — process liveness, not this event, signals a session ended")
	}
}

func TestParseLine_CommandInvoked_Skipped(t *testing.T) {
	ev := (&Parser{}).ParseLine(line(t, `{"payload_type":"command.invoked","payload":{"kind":"command_invoked",
		"record":{"session_id":"s1","command":"/usage"}}}`))
	if !ev.Skip {
		t.Error("command.invoked should be Skip=true")
	}
}

func TestParseLine_UnknownPayloadTypes_Skipped(t *testing.T) {
	// tool_batch.effect.* and approval_wait.effect.* are deliberately
	// unhandled (see parser.go's package doc) — confirm they fall to the
	// safe default rather than crashing or being misread.
	for _, raw := range []string{
		`{"payload_type":"tool_batch.effect.started","payload":{"kind":"tool_batch_effect",
			"record":{"kind":"started","call_id":"call_1","tool_name":"bash"}}}`,
		`{"payload_type":"tool_batch.effect.terminal","payload":{"kind":"tool_batch_effect",
			"record":{"kind":"terminal","call_id":"call_1","outcome":{"kind":"completed"}}}}`,
		`{"payload_type":"approval_wait.effect.started","payload":{"kind":"approval_wait_effect",
			"record":{"kind":"started","pending_action_id":"pa1"}}}`,
		`{"payload_type":"subagent.control.spawn_accepted","payload":{"kind":"subagent_control",
			"record":{"kind":"spawn_accepted","subagent_id":"sa1"}}}`,
		`{"payload_type":"runtime.session.task","payload":{"task_id":"t1",
			"event":{"kind":"proposed","task_kind":"session_name.allocate"}}}`,
	} {
		ev := (&Parser{}).ParseLine(line(t, raw))
		if !ev.Skip {
			t.Errorf("expected Skip=true for %s", raw)
		}
		if ev.EventType != "" {
			t.Errorf("expected empty EventType for %s, got %q", raw, ev.EventType)
		}
	}
}

func TestParseLine_MissingPayloadType_SkippedNotCrash(t *testing.T) {
	ev := (&Parser{}).ParseLine(map[string]any{"some_other_field": true})
	if !ev.Skip {
		t.Error("a line with no payload_type must be Skip=true, not crash")
	}
}

// --- kind:"task" (deliberately unused for tool tracking; see package doc) ---

func TestParseLine_TaskKindEvents_Skipped(t *testing.T) {
	ev := (&Parser{}).ParseLine(line(t, `{"payload_type":"runtime.session","payload":{"kind":"task",
		"task_id":"t1","event":{"kind":"side_effect_intent","operation":"tool:bash",
		"policy_decision":"allow:policy"}}}`))
	if !ev.Skip {
		t.Error("kind:task events should be Skip=true — tool tracking uses assistant_tool_calls_committed instead")
	}
}

// --- the retained_frame structural trap ---

func TestParseLine_RetainedFrame_RealFixture_MergesToSkip(t *testing.T) {
	lines := fixtureLines(t, "real-retained-frame.jsonl")
	if len(lines) != 1 {
		t.Fatalf("fixture should be exactly one outer line, got %d", len(lines))
	}
	ev := (&Parser{}).ParseLine(line(t, lines[0]))
	// Both real children (permission_format_declared, permission_profile_
	// committed) are unhandled bookkeeping, so the merged verdict is
	// Skip=true with zero deltas — but the fact that it decoded both
	// children without error, rather than short-circuiting on the outer
	// shape, is exactly what this test pins.
	if !ev.Skip {
		t.Errorf("expected Skip=true for a real session_permission_transaction frame, got EventType=%q", ev.EventType)
	}
	if len(ev.ToolUses) != 0 || len(ev.PermissionRequestIDs) != 0 {
		t.Errorf("expected zero deltas from pure permission-profile bookkeeping, got ToolUses=%v PermissionRequestIDs=%v",
			ev.ToolUses, ev.PermissionRequestIDs)
	}
}

func TestParseLine_RetainedFrame_NoChildren_SkippedNotCrash(t *testing.T) {
	ev := (&Parser{}).ParseLine(line(t, `{"retained_frame":"session_permission_transaction",
		"outer_log_ordinal":1,"children":[]}`))
	if !ev.Skip {
		t.Error("an empty retained_frame must be Skip=true, not crash")
	}
}

// TestParseLine_RetainedFrame_MergesMultipleChildDeltas is SYNTHETIC: no
// retained_frame in the available corpus ever wrapped anything but the two
// permission-profile bookkeeping records (real-retained-frame.jsonl above).
// This constructs a hypothetical frame wrapping two run-kind children — a
// "started" and an "assistant_tool_calls_committed" — to pin the actual
// contract the package doc promises: decodeAndRoute drives EVERY child, and
// mergeChildEvents folds their deltas into ONE event rather than keeping
// only the last child or the first. If a future muse release ever nests a
// real run/task/approval record inside a retained_frame, this is the
// behavior it gets.
func TestParseLine_RetainedFrame_MergesMultipleChildDeltas(t *testing.T) {
	started := `{"schema_version":1,"payload_type":"runtime.session","payload":{"kind":"run",
		"run_id":"r1","event":{"kind":"started","prompt":"ls"}}}`
	toolCall := `{"schema_version":1,"payload_type":"runtime.session","payload":{"kind":"run",
		"run_id":"r1","event":{"kind":"assistant_tool_calls_committed","message_id":"m1",
		"tool_calls":[{"id":"fc_1","call_id":"call_1","name":"bash","args":"{}"}]}}}`
	escape := func(s string) string {
		b, err := json.Marshal(s)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	frame := `{"retained_frame":"synthetic_test_frame","outer_log_ordinal":1,"children":[` +
		`{"child_index":0,"record_json":` + escape(started) + `},` +
		`{"child_index":1,"record_json":` + escape(toolCall) + `}]}`

	ev := (&Parser{}).ParseLine(line(t, frame))
	if ev.Skip {
		t.Fatal("expected a non-skipped merged event: the tool-call child carries real deltas")
	}
	// EventType takes the LAST non-empty child's value (the tool call).
	if ev.EventType != "function_call" {
		t.Errorf("EventType = %q, want function_call (last child wins)", ev.EventType)
	}
	// But the FIRST child's delta (ClearToolNames, UserText) must not be lost.
	if !ev.ClearToolNames {
		t.Error("expected ClearToolNames folded in from the first child")
	}
	if ev.UserText != "ls" {
		t.Errorf("UserText = %q, want %q folded in from the first child", ev.UserText, "ls")
	}
	if len(ev.ToolUses) != 1 || ev.ToolUses[0].ID != "call_1" {
		t.Errorf("ToolUses = %+v, want the second child's tool call", ev.ToolUses)
	}
}

// --- real, redacted end-to-end fixtures ---

// TestParseLine_SessionHeader_RealFixture replays a real, redacted session
// opening (metadata, route_facts, model_reconfigure.completed,
// run.model.configured, then the first turn's "started") and asserts
// CWD/ModelName/AgentVersion all surface, and that the header bookkeeping
// itself contributed no activity — only "started" did.
//
// The header records alone (before "started") are all Skip=true and add
// nothing to the tailer's MessageHistory, and LastCWD/LastAssistantText only
// get copied onto SessionMetrics once MessageHistory is non-empty
// (core/pkg/tailer/tailer_metrics.go computeMetrics's early-return guard) —
// so a fixture of pure header bookkeeping with no turn at all would read
// LastCWD="" despite route_facts having set it internally. Including the
// real first "started" event (as every genuine session does within
// milliseconds) is what makes CWD/AgentVersion observable here, and
// LastEventType below pins that "started" — not the header — is what
// produced the session's only activity.
func TestParseLine_SessionHeader_RealFixture(t *testing.T) {
	setHome(t) // isolate from the real machine's model-catalog
	m := tailFixture(t, fixtureLines(t, "real-session-header.jsonl"))
	if m.LastCWD == "" {
		t.Error("expected CWD to surface from route_facts")
	}
	if m.ModelName == "" {
		t.Error("expected ModelName to surface from model_reconfigure.completed / run.model.configured")
	}
	if m.AgentVersion != "1.2.1" {
		t.Errorf("AgentVersion = %q, want 1.2.1", m.AgentVersion)
	}
	if m.LastEventType != "user_message" {
		t.Errorf("LastEventType = %q, want user_message (from the first \"started\", not the header bookkeeping)", m.LastEventType)
	}
}

// TestParseLine_TurnCompleted_RealFixture replays a real, redacted turn
// (started -> model_completed -> tool call open/close -> assistant message
// -> terminal:completed) and asserts the turn settles with tokens counted
// and no open tool call left behind.
//
// This is the exact fixture (and, via the isolated $HOME + fixture catalog
// below, the exact model-context-display shape) a throwaway probe used
// while diagnosing issue #1960 stage 3's defects 1+2: before either fix,
// tl := tailer.NewTranscriptTailer(path, &Parser{}, AdapterName);
// m, _ := tl.TailAndProcess() reported TotalTokens=0, ContextWindow=0,
// ModelName="muse-spark-1.3-contributor" — model identity worked, both
// token-derived metrics were dead. The two assertions below pin the fix.
func TestParseLine_TurnCompleted_RealFixture(t *testing.T) {
	home := setHome(t)
	// The fixture's own model, muse-spark-1.3-contributor (grep -o
	// '"model"[^,}]*' testdata/real-turn-completed.jsonl), with the
	// context_limit live-confirmed on this machine
	// (~/.local/share/muse/model-catalog/*.json, 2026-09-14).
	writeCatalog(t, home, "catalog.json", modelCatalogRow{ModelID: "muse-spark-1.3-contributor", ContextLimit: 1007997})

	m := tailFixture(t, fixtureLines(t, "real-turn-completed.jsonl"))
	if m.LastEventType != "turn_done" {
		t.Errorf("LastEventType = %q, want turn_done", m.LastEventType)
	}
	if m.HasOpenToolCall {
		t.Error("expected no open tool call once the turn settled")
	}
	if m.CumInputTokens == 0 || m.CumOutputTokens == 0 {
		t.Errorf("expected nonzero cumulative tokens, got input=%d output=%d", m.CumInputTokens, m.CumOutputTokens)
	}
	if m.SessionError != nil {
		t.Errorf("SessionError = %+v, want nil for a clean completion", m.SessionError)
	}
	// Defect 1: TokenSnapshot.Total was never set, so SessionMetrics.TotalTokens
	// (the context-utilization numerator) stayed 0 no matter how much usage
	// model_completed reported.
	if m.TotalTokens == 0 {
		t.Error("TotalTokens = 0, want nonzero (defect 1: TokenSnapshot.Total was never set)")
	}
	// Defect 2: nothing in this package ever set ContextWindow, so the
	// denominator was always 0 too — even with a catalog fixture present.
	if m.ContextWindow != 1007997 {
		t.Errorf("ContextWindow = %d, want 1007997 (defect 2: no model-catalog read)", m.ContextWindow)
	}
	if m.ContextUtilization <= 0 {
		t.Errorf("ContextUtilization = %v, want > 0 now that both TotalTokens and ContextWindow are set", m.ContextUtilization)
	}
}

// TestParseLine_TurnFailed_RealFixture replays a real, redacted failed turn
// (started -> run_fatal_error_classified -> terminal:failed) end to end and
// asserts the sticky SessionError survives the pass.
func TestParseLine_TurnFailed_RealFixture(t *testing.T) {
	m := tailFixture(t, fixtureLines(t, "real-turn-failed.jsonl"))
	if m.SessionError == nil {
		t.Fatal("expected a sticky SessionError")
	}
	if m.SessionError.Class != "config_error" {
		t.Errorf("SessionError.Class = %q, want config_error", m.SessionError.Class)
	}
}
