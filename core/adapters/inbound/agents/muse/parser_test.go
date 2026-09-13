package muse

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

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

// --- kind:"run" / event.kind:"model_completed" (token accounting) ---

func TestParseLine_ModelCompleted_NetsCacheReadFromInput(t *testing.T) {
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
}

func TestParseLine_ModelCompleted_FallsBackToCachedTokens(t *testing.T) {
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

// --- kind:"approval" ---

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

func TestParseLine_ApprovalBookkeeping_Skipped(t *testing.T) {
	for _, kind := range []string{"automated_review_started", "automated_review_completed", "stage_requirement_resolved"} {
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
// -> decision_applied) end to end and asserts the wait opens then closes.
func TestParseLine_ApprovalFlow_RealFixture(t *testing.T) {
	m := tailFixture(t, fixtureLines(t, "real-approval-flow.jsonl"))
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
	ev := (&Parser{}).ParseLine(line(t, `{"payload_type":"run.model.configured",
		"payload":{"kind":"run_model","record":{"provider_id":"meta","model_id":"muse-spark-1.3-contributor",
		"source":"startup"}}}`))
	if !ev.Skip {
		t.Error("run.model.configured should be Skip=true")
	}
	if ev.ModelName != "muse-spark-1.3-contributor" {
		t.Errorf("ModelName = %q", ev.ModelName)
	}
}

func TestParseLine_ModelReconfigure_UsesEffectiveModel(t *testing.T) {
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
func TestParseLine_TurnCompleted_RealFixture(t *testing.T) {
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
