package dsh

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/klauspost/compress/zstd"

	"irrlicht/core/pkg/tailer"
)

func parseRecord(t *testing.T, parser *Parser, line string) *tailer.ParsedEvent {
	t.Helper()
	var raw map[string]any
	if err := json.Unmarshal([]byte(line), &raw); err != nil {
		t.Fatalf("decode record: %v", err)
	}
	return parser.ParseLine(raw)
}

// TestParserRefusesUnknownVersion is the mutation fixture for issue #1980.
// Removing parseSession's version comparison makes this test accept the v99
// header and process its turn/start record.
func TestParserRefusesUnknownVersion(t *testing.T) {
	events := parseFixture(t, filepath.Join("testdata", "unknown-version.jsonl"))
	if len(events) != 2 {
		t.Fatalf("event count = %d, want 2", len(events))
	}
	assertUnsupportedVersionError(t, events[0])
	assertRefusedEvent(t, events[0])
	assertUnsupportedVersionError(t, events[1])
	assertRefusedEvent(t, events[1])
}

func parseFixture(t *testing.T, path string) []*tailer.ParsedEvent {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	defer f.Close()

	parser := &Parser{}
	scanner := bufio.NewScanner(f)
	var events []*tailer.ParsedEvent
	for scanner.Scan() {
		events = append(events, parseRecord(t, parser, scanner.Text()))
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan fixture: %v", err)
	}
	return events
}

func assertUnsupportedVersionError(t *testing.T, event *tailer.ParsedEvent) {
	t.Helper()
	if event.SessionError == nil {
		t.Fatal("SessionError is nil")
	}
	if event.SessionError.Class != "unsupported_transcript_version" {
		t.Errorf("SessionError = %+v", event.SessionError)
	}
}

func assertRefusedEvent(t *testing.T, event *tailer.ParsedEvent) {
	t.Helper()
	if !event.Skip {
		t.Errorf("event was not skipped: %+v", event)
	}
	if event.EventType != "" {
		t.Errorf("event type = %q, want empty", event.EventType)
	}
}

func TestParserMapsMeasuredSessionHeader(t *testing.T) {
	header := parseRecord(t, &Parser{}, `{"type":"session","version":3,"id":"session-600e7941-bf4f-4da4-9ef6-489168e13724","createdAt":1789676506637,"cwd":"/Users/ingo/work"}`)
	if !header.Skip {
		t.Error("session header is not skipped")
	}
	if header.CWD != "/Users/ingo/work" {
		t.Errorf("CWD = %q, want /Users/ingo/work", header.CWD)
	}
	if header.Timestamp.UnixMilli() != 1789676506637 {
		t.Errorf("timestamp = %d, want 1789676506637", header.Timestamp.UnixMilli())
	}
}

func TestParserMapsMeasuredTurnStart(t *testing.T) {
	start := parseRecord(t, &Parser{}, `{"type":"turn/start","seq":4,"time":1789676506647,"data":{"turn":1}}`)
	if start.EventType != "turn_start" {
		t.Errorf("turn/start = %+v", start)
	}
}

func TestParserReconcilesMeasuredTodoWriteSnapshots(t *testing.T) {
	parser := &Parser{}
	initial := parseRecord(t, parser, `{"type":"todo/write","seq":17,"time":1789787679937,"data":{"todos":[{"content":"draft a greeting","status":"pending"},{"content":"refine the greeting","status":"pending"},{"content":"reply done","status":"pending"}]}}`)
	if initial.Skip {
		t.Fatalf("todo/write was skipped: %+v", initial)
	}
	if len(initial.TaskDeltas) != 3 {
		t.Fatalf("initial task deltas = %+v, want three creates", initial.TaskDeltas)
	}
	assertTodoSnapshot(t, initial, []tailer.TaskSnapshotEntry{
		{ID: "1", Subject: "draft a greeting", Status: "pending"},
		{ID: "2", Subject: "refine the greeting", Status: "pending"},
		{ID: "3", Subject: "reply done", Status: "pending"},
	})

	progressed := parseRecord(t, parser, `{"type":"todo/write","seq":31,"time":1789787696051,"data":{"todos":[{"content":"draft a greeting","status":"in_progress"},{"content":"refine the greeting","status":"pending"},{"content":"reply done","status":"pending"}]}}`)
	if len(progressed.TaskDeltas) != 1 || progressed.TaskDeltas[0].Op != tailer.TaskOpUpdate || progressed.TaskDeltas[0].ID != "1" || progressed.TaskDeltas[0].Status != "in_progress" {
		t.Fatalf("progressed task deltas = %+v, want update for first todo", progressed.TaskDeltas)
	}
	assertTodoSnapshot(t, progressed, []tailer.TaskSnapshotEntry{
		{ID: "1", Subject: "draft a greeting", Status: "in_progress"},
		{ID: "2", Subject: "refine the greeting", Status: "pending"},
		{ID: "3", Subject: "reply done", Status: "pending"},
	})

	cleared := parseRecord(t, parser, `{"type":"todo/write","seq":32,"time":1789787696052,"data":{"todos":[]}}`)
	if cleared.Skip {
		t.Fatalf("empty todo/write was skipped: %+v", cleared)
	}
	assertTodoSnapshot(t, cleared, []tailer.TaskSnapshotEntry{})
	if len(cleared.TaskDeltas) != 0 {
		t.Fatalf("empty todo/write task deltas = %+v, want none", cleared.TaskDeltas)
	}
}

func assertTodoSnapshot(t *testing.T, event *tailer.ParsedEvent, want []tailer.TaskSnapshotEntry) {
	t.Helper()
	if event.TaskSnapshot == nil {
		t.Fatal("task snapshot is nil")
	}
	if !reflect.DeepEqual(*event.TaskSnapshot, want) {
		t.Fatalf("task snapshot = %+v, want %+v", *event.TaskSnapshot, want)
	}
}

func TestParserMapsMeasuredCompactionBoundaries(t *testing.T) {
	parser := &Parser{}
	start := parseRecord(t, parser, `{"type":"compaction/start","seq":18,"time":1789776655200,"data":{"compactionId":"6c72d0f0-d416-43c7-b2ef-f528e43eda56","turn":null}}`)
	if start.Skip || start.EventType != "turn_start" {
		t.Fatalf("compaction/start = %+v, want turn_start", start)
	}
	end := parseRecord(t, parser, `{"type":"compaction/end","seq":21,"time":1789776673596,"data":{"compactionId":"6c72d0f0-d416-43c7-b2ef-f528e43eda56","turn":null}}`)
	if end.Skip || end.EventType != "turn_done" {
		t.Fatalf("compaction/end = %+v, want turn_done", end)
	}
}

func TestParserSkipsInlineCompactionBoundaries(t *testing.T) {
	parser := &Parser{}
	start := parseRecord(t, parser, `{"type":"compaction/start","seq":18,"time":1789776655200,"data":{"compactionId":"inline","turn":7}}`)
	if !start.Skip {
		t.Fatalf("inline compaction/start = %+v, want skipped", start)
	}
	end := parseRecord(t, parser, `{"type":"compaction/end","seq":21,"time":1789776673596,"data":{"compactionId":"inline","turn":7}}`)
	if !end.Skip {
		t.Fatalf("inline compaction/end = %+v, want skipped", end)
	}
}

func TestParserMapsMeasuredApprovalAsked(t *testing.T) {
	asked := parseRecord(t, &Parser{}, `{"type":"approval/asked","seq":35,"time":1789677803101,"data":{"id":"approval-1","toolName":"bash","callId":"757004337","reason":"write outside workspace"}}`)
	if asked.EventType != "permission_requested" {
		t.Errorf("event type = %q, want permission_requested", asked.EventType)
	}
	if !reflect.DeepEqual(asked.PermissionRequestIDs, []string{"approval-1"}) {
		t.Errorf("request IDs = %v, want approval-1", asked.PermissionRequestIDs)
	}
}

func TestParserMapsMeasuredApprovalDecided(t *testing.T) {
	decided := parseRecord(t, &Parser{}, `{"type":"approval/decided","seq":36,"time":1789677812727,"data":{"id":"approval-1","outcome":"rejected"}}`)
	if decided.EventType != "permission_completed" {
		t.Errorf("event type = %q, want permission_completed", decided.EventType)
	}
	if !reflect.DeepEqual(decided.PermissionResolvedIDs, []string{"approval-1"}) {
		t.Errorf("resolved IDs = %v, want approval-1", decided.PermissionResolvedIDs)
	}
}

func TestParserMapsMeasuredTurnEnd(t *testing.T) {
	end := parseRecord(t, &Parser{}, `{"type":"turn/end","seq":21,"time":1789678181870,"data":{"turn":1,"reason":{"kind":"completed"}}}`)
	if end.EventType != "turn_done" {
		t.Errorf("event type = %q, want turn_done", end.EventType)
	}
	if end.SessionError != nil {
		t.Errorf("SessionError = %+v, want nil", end.SessionError)
	}
}

func TestParserMapsMeasuredRequestContext(t *testing.T) {
	parser := &Parser{}
	context := parseRecord(t, parser, `{"type":"request/context","seq":14,"time":1789678150150,"data":{"provider":"lmstudio","model":"qwen/qwen3.5-9b","contextWindow":262144}}`)
	if !context.Skip {
		t.Error("request/context is not skipped")
	}
	if context.ModelName != "qwen/qwen3.5-9b" {
		t.Errorf("model = %q, want qwen/qwen3.5-9b", context.ModelName)
	}
	if context.ContextWindow != 262144 {
		t.Errorf("context window = %d, want 262144", context.ContextWindow)
	}
}

func TestParserRefusalSurvivesTailerRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.v99.jsonl.zstd")
	writeZstdFrame(t, path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC,
		`{"type":"session","version":99,"id":"session-600e7941-bf4f-4da4-9ef6-489168e13724","createdAt":1789676506637,"cwd":"/Users/ingo/work"}`+"\n")

	before := newTestTranscriptTailer(path)
	metrics := tailTranscript(t, before)
	assertUnsupportedSessionError(t, metrics)
	ledger := roundTripLedger(t, before.GetLedgerState())

	writeZstdFrame(t, path, os.O_WRONLY|os.O_APPEND,
		`{"type":"user/message","time":1789676506725,"data":{"role":"user","content":[{"type":"text","text":"Continue"}],"source":{"kind":"user"}}}`+"\n")

	after := newTestTranscriptTailer(path)
	after.SetLedgerState(ledger)
	metrics = tailTranscript(t, after)
	assertUnsupportedSessionError(t, metrics)
	if metrics.LastEventType == "user_message" {
		t.Error("unknown-version parser accepted a user message after restart")
	}
}

func newTestTranscriptTailer(path string) *tailer.TranscriptTailer {
	transcriptTailer := tailer.NewTranscriptTailer(path, &Parser{}, AdapterName)
	transcriptTailer.DisableModelConfigFallback()
	return transcriptTailer
}

func tailTranscript(t *testing.T, transcriptTailer *tailer.TranscriptTailer) *tailer.SessionMetrics {
	t.Helper()
	metrics, err := transcriptTailer.TailAndProcess()
	if err != nil {
		t.Fatalf("TailAndProcess: %v", err)
	}
	return metrics
}

func assertUnsupportedSessionError(t *testing.T, metrics *tailer.SessionMetrics) {
	t.Helper()
	if metrics.SessionError == nil {
		t.Fatal("SessionError is nil")
	}
	if metrics.SessionError.Class != "unsupported_transcript_version" {
		t.Errorf("SessionError = %+v", metrics.SessionError)
	}
}

func roundTripLedger(t *testing.T, ledger tailer.LedgerState) tailer.LedgerState {
	t.Helper()
	encoded, err := json.Marshal(ledger)
	if err != nil {
		t.Fatalf("marshal ledger: %v", err)
	}
	var restored tailer.LedgerState
	if err := json.Unmarshal(encoded, &restored); err != nil {
		t.Fatalf("unmarshal ledger: %v", err)
	}
	return restored
}

func writeZstdFrame(t *testing.T, path string, flags int, lines string) {
	t.Helper()
	encoder, err := zstd.NewWriter(nil, zstd.WithEncoderCRC(true))
	if err != nil {
		t.Fatalf("create encoder: %v", err)
	}
	compressed := encoder.EncodeAll([]byte(lines), nil)
	encoder.Close()
	file, err := os.OpenFile(path, flags, 0o600)
	if err != nil {
		t.Fatalf("open transcript: %v", err)
	}
	if _, err := file.Write(compressed); err != nil {
		_ = file.Close()
		t.Fatalf("write transcript: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close transcript: %v", err)
	}
}

func TestParserMapsAssistantUsage(t *testing.T) {
	parser := &Parser{}
	assistant := parseRecord(t, parser, `{"type":"assistant/message","seq":17,"time":1789678181815,"data":{"message":{"role":"assistant","content":[{"type":"reasoning","text":"hidden"},{"type":"text","text":"Done."}],"source":{"kind":"model","provider":"lmstudio","model":"qwen/qwen3.5-9b"}},"usage":{"inputTokens":7899,"outputTokens":24,"totalTokens":7923}}}`)
	if assistant.EventType != "assistant_message" {
		t.Errorf("event type = %q, want assistant_message", assistant.EventType)
	}
	if assistant.ModelName != "qwen/qwen3.5-9b" {
		t.Errorf("model = %q, want qwen/qwen3.5-9b", assistant.ModelName)
	}
	if assistant.AssistantText != "Done." {
		t.Errorf("assistant text = %q, want Done.", assistant.AssistantText)
	}
	assertAssistantTokens(t, assistant)
}

func assertAssistantTokens(t *testing.T, assistant *tailer.ParsedEvent) {
	t.Helper()
	if assistant.Tokens == nil {
		t.Fatal("tokens are nil")
	}
	wantTokens := tailer.TokenSnapshot{Input: 7899, Output: 24, Total: 7923}
	if *assistant.Tokens != wantTokens {
		t.Errorf("tokens = %+v, want %+v", *assistant.Tokens, wantTokens)
	}
	if assistant.Contribution == nil {
		t.Fatal("contribution is nil")
	}
	wantUsage := tailer.UsageBreakdown{Input: 7899, Output: 24}
	if assistant.Contribution.Usage != wantUsage {
		t.Errorf("contribution = %+v, want %+v", assistant.Contribution.Usage, wantUsage)
	}
}

func TestParserMapsToolLifecycle(t *testing.T) {
	parser := &Parser{}
	call := parseRecord(t, parser, `{"type":"tool/call","seq":18,"time":1789678034523,"data":{"callId":"496248271","name":"read","arguments":"{}"}}`)
	if !reflect.DeepEqual(call.ToolUses, []tailer.ToolUse{{ID: "496248271", Name: "read"}}) {
		t.Errorf("tool/call = %+v", call)
	}
	result := parseRecord(t, parser, `{"type":"tool/result","seq":21,"time":1789678034628,"data":{"message":{"source":{"kind":"tool","callId":"496248271"},"content":[{"type":"tool-result","toolCallId":"496248271","isError":false}]}}}`)
	if !reflect.DeepEqual(result.ToolResultIDs, []string{"496248271"}) {
		t.Errorf("tool/result = %+v", result)
	}
	if result.IsError {
		t.Error("tool/result is marked as an error")
	}
}

func TestParserMapsUserMessage(t *testing.T) {
	user := parseRecord(t, &Parser{}, `{"type":"user/message","time":1789676506725,"data":{"role":"user","content":[{"type":"text","text":"Read note.txt"}],"source":{"kind":"user"}}}`)
	if user.EventType != "user_message" {
		t.Errorf("event type = %q, want user_message", user.EventType)
	}
	if !user.ClearToolNames {
		t.Error("user message does not clear tool names")
	}
	if user.UserText != "Read note.txt" {
		t.Errorf("user text = %q, want Read note.txt", user.UserText)
	}
}

func TestParserSkipsPluginUserMessage(t *testing.T) {
	plugin := parseRecord(t, &Parser{}, `{"type":"user/message","time":1789676506726,"data":{"role":"user","content":[{"type":"text","text":"runtime context"}],"source":{"kind":"plugin"}}}`)
	if !plugin.Skip {
		t.Errorf("plugin user message was not skipped: %+v", plugin)
	}
}

func TestParserMapsTerminalError(t *testing.T) {
	errorEnd := parseRecord(t, &Parser{}, `{"type":"turn/end","time":1789676444761,"data":{"reason":{"kind":"error","error":{"message":"No API key","code":"MISSING_CREDENTIAL"}}}}`)
	if errorEnd.SessionError == nil {
		t.Fatalf("error turn/end = %+v", errorEnd)
	}
	if errorEnd.SessionError.Class != "missing_credential" {
		t.Errorf("error turn/end = %+v", errorEnd)
	}
}

func TestZstdTranscriptEndToEnd(t *testing.T) {
	lines := strings.Join([]string{
		`{"type":"session","version":3,"id":"session-600e7941-bf4f-4da4-9ef6-489168e13724","createdAt":1789676506637,"cwd":"/Users/ingo/work"}`,
		`{"type":"request/header","seq":1,"time":1789676506640,"data":{"header":{"config":{"provider":"lmstudio","model":"qwen/qwen3.5-9b"}}}}`,
		`{"type":"request/context","seq":2,"time":1789676506641,"data":{"provider":"lmstudio","model":"qwen/qwen3.5-9b","contextWindow":262144}}`,
		`{"type":"turn/start","seq":4,"time":1789676506647,"data":{"turn":1}}`,
		`{"type":"assistant/message","seq":17,"time":1789678181815,"data":{"message":{"role":"assistant","content":[{"type":"text","text":"Done."}],"source":{"kind":"model","model":"qwen/qwen3.5-9b"}},"usage":{"inputTokens":10,"outputTokens":2,"totalTokens":12}}}`,
		`{"type":"tool/call","seq":18,"time":1789678181816,"data":{"callId":"call-1","name":"read"}}`,
		`{"type":"tool/result","seq":19,"time":1789678181817,"data":{"message":{"source":{"kind":"tool","callId":"call-1"},"content":[{"type":"tool-result","toolCallId":"call-1","isError":false}]}}}`,
		`{"type":"turn/end","seq":20,"time":1789678181818,"data":{"reason":{"kind":"completed"}}}`,
	}, "\n") + "\n"
	path := filepath.Join(t.TempDir(), "session.v3.jsonl.zstd")
	writeZstdFrame(t, path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, lines)
	transcriptTailer := tailer.NewTranscriptTailer(path, &Parser{}, AdapterName)
	transcriptTailer.DisableModelConfigFallback()
	metrics, err := transcriptTailer.TailAndProcess()
	if err != nil {
		t.Fatalf("TailAndProcess: %v", err)
	}
	assertLifecycleMetrics(t, metrics)
	assertUsageMetrics(t, metrics)
	assertClosedState(t, metrics)
}

func assertLifecycleMetrics(t *testing.T, metrics *tailer.SessionMetrics) {
	t.Helper()
	if metrics.LastCWD != "/Users/ingo/work" {
		t.Errorf("last CWD = %q, want /Users/ingo/work", metrics.LastCWD)
	}
	if metrics.LastEventType != "turn_done" {
		t.Errorf("last event type = %q, want turn_done", metrics.LastEventType)
	}
}

func assertUsageMetrics(t *testing.T, metrics *tailer.SessionMetrics) {
	t.Helper()
	if metrics.ModelName != "qwen/qwen3.5-9b" {
		t.Errorf("model = %q, want qwen/qwen3.5-9b", metrics.ModelName)
	}
	if metrics.TotalTokens != 12 {
		t.Errorf("total tokens = %d, want 12", metrics.TotalTokens)
	}
	if metrics.InputTokens != 10 {
		t.Errorf("input tokens = %d, want 10", metrics.InputTokens)
	}
	if metrics.OutputTokens != 2 {
		t.Errorf("output tokens = %d, want 2", metrics.OutputTokens)
	}
	if metrics.ContextWindow != 262144 {
		t.Errorf("context window = %d, want 262144", metrics.ContextWindow)
	}
	if metrics.ContextWindowUnknown {
		t.Error("context window is unknown")
	}
}

func assertClosedState(t *testing.T, metrics *tailer.SessionMetrics) {
	t.Helper()
	if metrics.HasOpenToolCall {
		t.Error("closed transcript has an open tool call")
	}
	if metrics.TranscriptPermissionPending {
		t.Error("closed transcript has a pending transcript permission")
	}
}
