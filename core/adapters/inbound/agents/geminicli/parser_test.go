package geminicli

import (
	"bufio"
	"encoding/json"
	"os"
	"testing"

	"irrlicht/core/pkg/tailer"
)

// decode is a tiny helper turning a JSON object literal into the raw map the
// tailer hands ParseLine.
func decode(t *testing.T, s string) map[string]interface{} {
	t.Helper()
	var m map[string]interface{}
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatalf("decode %q: %v", s, err)
	}
	return m
}

func TestParseLine_SkipsHeaderAndHeartbeat(t *testing.T) {
	p := &Parser{}

	header := decode(t, `{"sessionId":"s","projectHash":"h","startTime":"2026-06-08T21:40:47.487Z","kind":"main"}`)
	if ev := p.ParseLine(header); !ev.Skip {
		t.Errorf("session header: want Skip, got EventType=%q", ev.EventType)
	}

	heartbeat := decode(t, `{"$set":{"lastUpdated":"2026-06-08T21:41:47.353Z"}}`)
	if ev := p.ParseLine(heartbeat); !ev.Skip {
		t.Errorf("lastUpdated heartbeat: want Skip, got EventType=%q", ev.EventType)
	}
}

func TestParseLine_HarvestsCWDFromBootstrap(t *testing.T) {
	p := &Parser{}
	boot := decode(t, `{"$set":{"messages":[{"id":"d0","type":"user","content":[{"text":"<session_context>\nWorkspace stuff.\n- **Workspace Directories:**\n  - /private/tmp/gem-probe\n- **Directory Structure:**\n</session_context>"}]}]}}`)

	ev := p.ParseLine(boot)
	if !ev.Skip {
		t.Fatalf("bootstrap $set: want Skip, got EventType=%q", ev.EventType)
	}
	if p.cwd != "/private/tmp/gem-probe" {
		t.Fatalf("cwd: want /private/tmp/gem-probe, got %q", p.cwd)
	}

	// The next real event must carry the harvested cwd so it lands in state.CWD.
	prompt := decode(t, `{"id":"u1","type":"user","content":[{"text":"hello"}]}`)
	ev = p.ParseLine(prompt)
	if ev.EventType != "user_message" {
		t.Fatalf("prompt: want user_message, got %q (skip=%v)", ev.EventType, ev.Skip)
	}
	if ev.CWD != "/private/tmp/gem-probe" {
		t.Errorf("prompt CWD: want /private/tmp/gem-probe, got %q", ev.CWD)
	}
	if !ev.ClearToolNames {
		t.Errorf("user prompt should ClearToolNames")
	}
}

func TestParseLine_UserFunctionResponseIsToolResult(t *testing.T) {
	p := &Parser{}
	line := decode(t, `{"id":"u2","type":"user","content":[
		{"functionResponse":{"id":"call_a","name":"update_topic","response":{"output":"ok"}}},
		{"functionResponse":{"id":"call_b","name":"list_directory","response":{"output":"empty"}}}
	]}`)

	ev := p.ParseLine(line)
	if ev.Skip {
		t.Fatal("tool-result user message should not be skipped")
	}
	if ev.EventType != "function_call_output" {
		t.Errorf("EventType: want function_call_output, got %q", ev.EventType)
	}
	if ev.ClearToolNames {
		t.Error("tool result must NOT ClearToolNames (it is not a new user turn)")
	}
	if got := ev.ToolResultIDs; len(got) != 2 || got[0] != "call_a" || got[1] != "call_b" {
		t.Errorf("ToolResultIDs: want [call_a call_b], got %v", got)
	}
}

func TestParseLine_AssistantPlaceholderStaysWorking(t *testing.T) {
	p := &Parser{}
	// Empty streaming placeholder, no tool calls: an assistant_message, NOT a
	// turn_done — the session must stay working.
	line := decode(t, `{"id":"g1","type":"gemini","content":"","tokens":{"input":100,"output":5,"cached":0,"total":105},"model":"gemini-3-flash-preview"}`)
	ev := p.ParseLine(line)
	if ev.EventType != "assistant_message" {
		t.Fatalf("placeholder: want assistant_message, got %q", ev.EventType)
	}
	if ev.Contribution == nil || ev.Contribution.Usage.Input != 100 || ev.Contribution.Usage.Output != 5 {
		t.Errorf("placeholder contribution: want input=100 output=5, got %+v", ev.Contribution)
	}
}

func TestParseLine_AssistantToolCallOpensTool(t *testing.T) {
	p := &Parser{}
	line := decode(t, `{"id":"g2","type":"gemini","content":"","model":"gemini-3-flash-preview","toolCalls":[
		{"id":"call_a","name":"update_topic","status":"success"}
	]}`)
	ev := p.ParseLine(line)
	if ev.EventType != "assistant_message" {
		t.Fatalf("tool-calling message: want assistant_message (not turn_done), got %q", ev.EventType)
	}
	if len(ev.ToolUses) != 1 || ev.ToolUses[0].Name != "update_topic" || ev.ToolUses[0].ID != "call_a" {
		t.Errorf("ToolUses: want [{call_a update_topic}], got %v", ev.ToolUses)
	}
	if len(ev.ToolResultIDs) != 1 || ev.ToolResultIDs[0] != "call_a" {
		t.Errorf("success toolCall should self-close: want ToolResultIDs=[call_a], got %v", ev.ToolResultIDs)
	}
}

func TestParseLine_FinalTextSettlesTurn(t *testing.T) {
	p := &Parser{}
	line := decode(t, `{"id":"g3","type":"gemini","content":"The current directory is empty.\n\nDONE","tokens":{"input":10081,"output":8,"cached":7443,"total":10089},"model":"gemini-3-flash-preview"}`)
	ev := p.ParseLine(line)
	if ev.EventType != "turn_done" {
		t.Fatalf("final text message: want turn_done, got %q", ev.EventType)
	}
	if ev.AssistantText == "" {
		t.Error("turn_done should still carry AssistantText for waiting display")
	}
	// input is inclusive of cached: bill (input-cached) as Input, cached as CacheRead.
	if c := ev.Contribution; c == nil || c.Usage.Input != 2638 || c.Usage.CacheRead != 7443 || c.Usage.Output != 8 {
		t.Errorf("contribution: want input=2638 cacheRead=7443 output=8, got %+v", ev.Contribution)
	}
}

func TestParseLine_StreamingReemissionNotDoubleBilled(t *testing.T) {
	p := &Parser{}
	first := decode(t, `{"id":"g4","type":"gemini","content":"","tokens":{"input":9925,"output":91,"cached":0,"total":10016},"model":"gemini-3-flash-preview"}`)
	if ev := p.ParseLine(first); ev.Contribution == nil {
		t.Fatal("first sight of a message should contribute")
	}
	// Same id, identical tokens (Gemini rewrites the message in place once it
	// gains toolCalls) — must NOT bill again.
	second := decode(t, `{"id":"g4","type":"gemini","content":"","tokens":{"input":9925,"output":91,"cached":0,"total":10016},"model":"gemini-3-flash-preview","toolCalls":[{"id":"c","name":"x","status":"success"}]}`)
	if ev := p.ParseLine(second); ev.Contribution != nil {
		t.Errorf("re-emission under same id must not double-bill, got %+v", ev.Contribution)
	}
}

// --- run_shell_command is_background tracking (issue #661) ---

// TestParseLine_BackgroundSpawn_FromShellToolCall asserts that a
// run_shell_command toolCall carrying is_background:true and a "Command moved
// to background (PID: N)" result emits a BackgroundSpawn keyed on the PID, so
// the shared tailer increments BackgroundProcessCount. The session must stay
// working (assistant message with an open tool call), NOT settle to ready.
func TestParseLine_BackgroundSpawn_FromShellToolCall(t *testing.T) {
	p := &Parser{}
	line := decode(t, `{"id":"g5","type":"gemini","content":"","model":"gemini-3.5-flash","toolCalls":[
		{"id":"run_shell_command__9cqstvzo","name":"run_shell_command","args":{"is_background":true,"command":"sleep 40 && echo BG_DONE"},
		 "result":[{"functionResponse":{"id":"run_shell_command__9cqstvzo","name":"run_shell_command","response":{"output":"Command moved to background (PID: 33701). Output hidden. Press Ctrl+B to view."}}}],
		 "status":"success"}
	]}`)
	ev := p.ParseLine(line)
	if ev.EventType != "assistant_message" {
		t.Fatalf("background-spawn message: want assistant_message (not turn_done), got %q", ev.EventType)
	}
	if len(ev.BackgroundSpawns) != 1 {
		t.Fatalf("BackgroundSpawns = %d, want 1", len(ev.BackgroundSpawns))
	}
	if got := ev.BackgroundSpawns[0].BashID; got != "33701" {
		t.Errorf("BackgroundSpawn.BashID = %q, want the PID 33701", got)
	}
	// Gemini hides background output (Ctrl+B to view) — no output file path, so
	// the lsof working-hold probe cannot engage (that part stays live-only).
	if got := ev.BackgroundSpawns[0].OutputPath; got != "" {
		t.Errorf("BackgroundSpawn.OutputPath = %q, want empty (Gemini bg output is in-process, no file)", got)
	}
}

// TestParseLine_NoBackgroundSpawnWhenForeground guards against a phantom spawn:
// a normal (foreground) run_shell_command must not register a background
// process even if its output happens to mention a PID.
func TestParseLine_NoBackgroundSpawnWhenForeground(t *testing.T) {
	p := &Parser{}
	line := decode(t, `{"id":"g6","type":"gemini","content":"","model":"gemini-3.5-flash","toolCalls":[
		{"id":"c1","name":"run_shell_command","args":{"command":"echo hi"},
		 "result":[{"functionResponse":{"id":"c1","name":"run_shell_command","response":{"output":"hi"}}}],
		 "status":"success"}
	]}`)
	if ev := p.ParseLine(line); len(ev.BackgroundSpawns) != 0 {
		t.Errorf("foreground command must not spawn a background process, got %+v", ev.BackgroundSpawns)
	}
}

// TestTailer_BackgroundProcessCount_GeminiSpawn drives the parser through the
// shared tailer and asserts the open-background-process accounting increments
// from a Gemini is_background launch. Mirrors the claudecode coverage in
// background_process_test.go.
func TestTailer_BackgroundProcessCount_GeminiSpawn(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/transcript.jsonl"
	if err := os.WriteFile(path, []byte(
		`{"id":"g5","type":"gemini","content":"","model":"gemini-3.5-flash","toolCalls":[{"id":"run_shell_command__9cqstvzo","name":"run_shell_command","args":{"is_background":true,"command":"sleep 40 && echo BG_DONE"},"result":[{"functionResponse":{"id":"run_shell_command__9cqstvzo","name":"run_shell_command","response":{"output":"Command moved to background (PID: 33701). Output hidden. Press Ctrl+B to view."}}}],"status":"success"}]}`+"\n",
	), 0o644); err != nil {
		t.Fatalf("write transcript: %v", err)
	}
	m, err := tailer.NewTranscriptTailer(path, &Parser{}, "gemini-cli").TailAndProcess()
	if err != nil {
		t.Fatalf("TailAndProcess: %v", err)
	}
	if m.BackgroundProcessCount != 1 {
		t.Fatalf("BackgroundProcessCount = %d, want 1", m.BackgroundProcessCount)
	}
	// Gemini reports no output file, so BackgroundProcessOutputs stays empty —
	// the lsof probe has nothing to inspect. Instead the PID surfaces in
	// BackgroundProcessPIDs, which the daemon's PID-liveness probe signals to
	// hold the session `working` until the process exits (issue #661).
	if len(m.BackgroundProcessOutputs) != 0 {
		t.Errorf("BackgroundProcessOutputs = %v, want empty (Gemini bg output is in-process)", m.BackgroundProcessOutputs)
	}
	if len(m.BackgroundProcessPIDs) != 1 || m.BackgroundProcessPIDs[0] != "33701" {
		t.Errorf("BackgroundProcessPIDs = %v, want [33701]", m.BackgroundProcessPIDs)
	}
}

// TestParse_RealSession replays the real captured transcript end-to-end and
// asserts the session-level signals: exactly one user turn, exactly one
// settle-to-ready, the workspace cwd, tool open/close, and deduped billing.
func TestParse_RealSession(t *testing.T) {
	f, err := os.Open("testdata/real-toolcall-session.jsonl")
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	defer f.Close()

	p := &Parser{}
	var (
		userMsgs, turnDones int
		lastCWD, finalText  string
		toolNames           = map[string]bool{}
		toolResults         = map[string]bool{}
		sumInput, sumOutput, sumCacheRead int64
	)

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1024*1024), 1024*1024)
	for sc.Scan() {
		var raw map[string]interface{}
		if err := json.Unmarshal(sc.Bytes(), &raw); err != nil {
			t.Fatalf("decode line: %v", err)
		}
		ev := p.ParseLine(raw)
		if ev.Skip {
			continue
		}
		if ev.CWD != "" {
			lastCWD = ev.CWD
		}
		switch ev.EventType {
		case "user_message":
			userMsgs++
		case "turn_done":
			turnDones++
			finalText = ev.AssistantText
		}
		for _, tu := range ev.ToolUses {
			toolNames[tu.Name] = true
		}
		for _, id := range ev.ToolResultIDs {
			toolResults[id] = true
		}
		if ev.Contribution != nil {
			sumInput += ev.Contribution.Usage.Input
			sumOutput += ev.Contribution.Usage.Output
			sumCacheRead += ev.Contribution.Usage.CacheRead
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("scan: %v", err)
	}

	if lastCWD != "/private/tmp/gem-probe" {
		t.Errorf("cwd: want /private/tmp/gem-probe, got %q", lastCWD)
	}
	if userMsgs != 1 {
		t.Errorf("user_message count: want 1, got %d", userMsgs)
	}
	if turnDones != 1 {
		t.Errorf("turn_done count: want 1, got %d", turnDones)
	}
	if finalText == "" {
		t.Error("final turn_done carried no AssistantText")
	}
	if !toolNames["update_topic"] {
		t.Errorf("expected update_topic in ToolUses, got %v", toolNames)
	}
	// Both tool results close: update_topic + list_directory.
	if len(toolResults) < 2 {
		t.Errorf("expected >=2 tool results closed, got %v", toolResults)
	}
	// 91bc4bb9 (input 9925, contributed once across its two emissions) +
	// 8fcff41a (input 10081 - cached 7443 = 2638).
	if sumInput != 12563 {
		t.Errorf("billed Input: want 12563 (dedup across re-emission), got %d", sumInput)
	}
	if sumOutput != 99 {
		t.Errorf("billed Output: want 99, got %d", sumOutput)
	}
	if sumCacheRead != 7443 {
		t.Errorf("billed CacheRead: want 7443, got %d", sumCacheRead)
	}
}
