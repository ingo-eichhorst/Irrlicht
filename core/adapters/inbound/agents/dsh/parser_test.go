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
	f, err := os.Open(filepath.Join("testdata", "unknown-version.jsonl"))
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
	if len(events) != 2 {
		t.Fatalf("event count = %d, want 2", len(events))
	}
	for i, event := range events {
		if event.SessionError == nil || event.SessionError.Class != "unsupported_transcript_version" {
			t.Errorf("event %d SessionError = %+v", i, event.SessionError)
		}
		if !event.Skip || event.EventType != "" {
			t.Errorf("event %d was not refused: %+v", i, event)
		}
	}
}

func TestParserMapsMeasuredLifecycleRecords(t *testing.T) {
	parser := &Parser{}

	header := parseRecord(t, parser, `{"type":"session","version":3,"id":"session-600e7941-bf4f-4da4-9ef6-489168e13724","createdAt":1789676506637,"cwd":"/Users/ingo/work"}`)
	if !header.Skip || header.CWD != "/Users/ingo/work" || header.Timestamp.UnixMilli() != 1789676506637 {
		t.Errorf("header = %+v", header)
	}

	start := parseRecord(t, parser, `{"type":"turn/start","seq":4,"time":1789676506647,"data":{"turn":1}}`)
	if start.EventType != "turn_start" {
		t.Errorf("turn/start = %+v", start)
	}

	asked := parseRecord(t, parser, `{"type":"approval/asked","seq":35,"time":1789677803101,"data":{"id":"approval-1","toolName":"bash","callId":"757004337","reason":"write outside workspace"}}`)
	if asked.EventType != "permission_requested" || !reflect.DeepEqual(asked.PermissionRequestIDs, []string{"approval-1"}) {
		t.Errorf("approval/asked = %+v", asked)
	}

	decided := parseRecord(t, parser, `{"type":"approval/decided","seq":36,"time":1789677812727,"data":{"id":"approval-1","outcome":"rejected"}}`)
	if decided.EventType != "permission_completed" || !reflect.DeepEqual(decided.PermissionResolvedIDs, []string{"approval-1"}) {
		t.Errorf("approval/decided = %+v", decided)
	}

	end := parseRecord(t, parser, `{"type":"turn/end","seq":21,"time":1789678181870,"data":{"turn":1,"reason":{"kind":"completed"}}}`)
	if end.EventType != "turn_done" || end.SessionError != nil {
		t.Errorf("completed turn/end = %+v", end)
	}
}

func TestParserMapsMeasuredRequestContext(t *testing.T) {
	parser := &Parser{}
	context := parseRecord(t, parser, `{"type":"request/context","seq":14,"time":1789678150150,"data":{"provider":"lmstudio","model":"qwen/qwen3.5-9b","contextWindow":262144}}`)
	if !context.Skip || context.ModelName != "qwen/qwen3.5-9b" || context.ContextWindow != 262144 {
		t.Errorf("request/context = %+v", context)
	}
}

func TestParserRefusalSurvivesTailerRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.v99.jsonl.zstd")
	writeZstdFrame(t, path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC,
		`{"type":"session","version":99,"id":"session-600e7941-bf4f-4da4-9ef6-489168e13724","createdAt":1789676506637,"cwd":"/Users/ingo/work"}`+"\n")

	before := tailer.NewTranscriptTailer(path, &Parser{}, AdapterName)
	before.DisableModelConfigFallback()
	metrics, err := before.TailAndProcess()
	if err != nil {
		t.Fatalf("first TailAndProcess: %v", err)
	}
	if metrics.SessionError == nil || metrics.SessionError.Class != "unsupported_transcript_version" {
		t.Fatalf("first SessionError = %+v", metrics.SessionError)
	}
	ledger := roundTripLedger(t, before.GetLedgerState())

	writeZstdFrame(t, path, os.O_WRONLY|os.O_APPEND,
		`{"type":"user/message","time":1789676506725,"data":{"role":"user","content":[{"type":"text","text":"Continue"}],"source":{"kind":"user"}}}`+"\n")

	after := tailer.NewTranscriptTailer(path, &Parser{}, AdapterName)
	after.DisableModelConfigFallback()
	after.SetLedgerState(ledger)
	metrics, err = after.TailAndProcess()
	if err != nil {
		t.Fatalf("second TailAndProcess: %v", err)
	}
	if metrics.SessionError == nil || metrics.SessionError.Class != "unsupported_transcript_version" {
		t.Errorf("restored SessionError = %+v", metrics.SessionError)
	}
	if metrics.LastEventType == "user_message" {
		t.Error("unknown-version parser accepted a user message after restart")
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

func TestParserMapsUserAndTerminalError(t *testing.T) {
	parser := &Parser{}
	user := parseRecord(t, parser, `{"type":"user/message","time":1789676506725,"data":{"role":"user","content":[{"type":"text","text":"Read note.txt"}],"source":{"kind":"user"}}}`)
	if user.EventType != "user_message" || !user.ClearToolNames || user.UserText != "Read note.txt" {
		t.Errorf("user message = %+v", user)
	}
	plugin := parseRecord(t, parser, `{"type":"user/message","time":1789676506726,"data":{"role":"user","content":[{"type":"text","text":"runtime context"}],"source":{"kind":"plugin"}}}`)
	if !plugin.Skip {
		t.Errorf("plugin user message was not skipped: %+v", plugin)
	}
	errorEnd := parseRecord(t, parser, `{"type":"turn/end","time":1789676444761,"data":{"reason":{"kind":"error","error":{"message":"No API key","code":"MISSING_CREDENTIAL"}}}}`)
	if errorEnd.SessionError == nil || errorEnd.SessionError.Class != "missing_credential" {
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
	if metrics.LastCWD != "/Users/ingo/work" || metrics.LastEventType != "turn_done" {
		t.Errorf("lifecycle metrics = %+v", metrics)
	}
}

func assertUsageMetrics(t *testing.T, metrics *tailer.SessionMetrics) {
	t.Helper()
	wantTokens := metrics.TotalTokens == 12 && metrics.InputTokens == 10 && metrics.OutputTokens == 2
	if metrics.ModelName != "qwen/qwen3.5-9b" || !wantTokens {
		t.Errorf("usage metrics = %+v", metrics)
	}
	if metrics.ContextWindow != 262144 || metrics.ContextWindowUnknown {
		t.Errorf("context metrics = %+v", metrics)
	}
}

func assertClosedState(t *testing.T, metrics *tailer.SessionMetrics) {
	t.Helper()
	if metrics.HasOpenToolCall || metrics.TranscriptPermissionPending {
		t.Errorf("closed transcript retained open state: %+v", metrics)
	}
}
