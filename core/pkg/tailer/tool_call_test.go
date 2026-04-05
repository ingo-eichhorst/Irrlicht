package tailer

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeTranscriptLines writes JSONL entries to a temp file and returns the path.
func writeTranscriptLines(t *testing.T, lines []map[string]interface{}) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	enc := json.NewEncoder(f)
	for _, line := range lines {
		if err := enc.Encode(line); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

func ts(offset int) string {
	return time.Now().Add(time.Duration(offset) * time.Second).Format(time.RFC3339)
}

func TestHasOpenToolCall_NoToolEvents(t *testing.T) {
	path := writeTranscriptLines(t, []map[string]interface{}{
		{"type": "user", "timestamp": ts(0)},
		{"type": "assistant", "timestamp": ts(1)},
	})

	tailer := NewTranscriptTailer(path)
	m, err := tailer.TailAndProcess()
	if err != nil {
		t.Fatal(err)
	}
	if m.HasOpenToolCall {
		t.Error("expected HasOpenToolCall=false with no tool events")
	}
	if m.OpenToolCallCount != 0 {
		t.Errorf("expected OpenToolCallCount=0, got %d", m.OpenToolCallCount)
	}
}

func TestHasOpenToolCall_SinglePairedToolCall(t *testing.T) {
	path := writeTranscriptLines(t, []map[string]interface{}{
		{"type": "user", "timestamp": ts(0)},
		{"type": "tool_use", "timestamp": ts(1)},
		{"type": "tool_result", "timestamp": ts(2)},
		{"type": "assistant", "timestamp": ts(3)},
	})

	tailer := NewTranscriptTailer(path)
	m, err := tailer.TailAndProcess()
	if err != nil {
		t.Fatal(err)
	}
	if m.HasOpenToolCall {
		t.Error("expected HasOpenToolCall=false when tool_use is paired with tool_result")
	}
	if m.OpenToolCallCount != 0 {
		t.Errorf("expected OpenToolCallCount=0, got %d", m.OpenToolCallCount)
	}
}

func TestHasOpenToolCall_OneOpenToolCall(t *testing.T) {
	path := writeTranscriptLines(t, []map[string]interface{}{
		{"type": "user", "timestamp": ts(0)},
		{"type": "tool_use", "timestamp": ts(1)},
		// No matching tool_result
	})

	tailer := NewTranscriptTailer(path)
	m, err := tailer.TailAndProcess()
	if err != nil {
		t.Fatal(err)
	}
	if !m.HasOpenToolCall {
		t.Error("expected HasOpenToolCall=true with unmatched tool_use")
	}
	if m.OpenToolCallCount != 1 {
		t.Errorf("expected OpenToolCallCount=1, got %d", m.OpenToolCallCount)
	}
}

func TestHasOpenToolCall_ParallelToolCalls(t *testing.T) {
	// Simulate 3 parallel tool_use events, only 1 tool_result so far
	path := writeTranscriptLines(t, []map[string]interface{}{
		{"type": "tool_use", "timestamp": ts(0)},
		{"type": "tool_use", "timestamp": ts(1)},
		{"type": "tool_use", "timestamp": ts(2)},
		{"type": "tool_result", "timestamp": ts(3)},
	})

	tailer := NewTranscriptTailer(path)
	m, err := tailer.TailAndProcess()
	if err != nil {
		t.Fatal(err)
	}
	if !m.HasOpenToolCall {
		t.Error("expected HasOpenToolCall=true with 2 unmatched tool_use events")
	}
	if m.OpenToolCallCount != 2 {
		t.Errorf("expected OpenToolCallCount=2, got %d", m.OpenToolCallCount)
	}
}

func TestHasOpenToolCall_ToolCallEventType(t *testing.T) {
	// The "tool_call" event type (legacy format) should also be counted
	path := writeTranscriptLines(t, []map[string]interface{}{
		{"type": "user", "timestamp": ts(0)},
		{"tool_call": map[string]interface{}{"name": "Bash"}, "timestamp": ts(1)},
		// No matching tool_result
	})

	tailer := NewTranscriptTailer(path)
	m, err := tailer.TailAndProcess()
	if err != nil {
		t.Fatal(err)
	}
	if !m.HasOpenToolCall {
		t.Error("expected HasOpenToolCall=true with unmatched tool_call")
	}
	if m.OpenToolCallCount != 1 {
		t.Errorf("expected OpenToolCallCount=1, got %d", m.OpenToolCallCount)
	}
}

func TestHasOpenToolCall_ExtraResultsClamped(t *testing.T) {
	// If we start reading mid-stream, we might see more tool_results than
	// tool_use events. The count should be clamped to 0.
	path := writeTranscriptLines(t, []map[string]interface{}{
		{"type": "tool_result", "timestamp": ts(0)},
		{"type": "tool_result", "timestamp": ts(1)},
		{"type": "assistant", "timestamp": ts(2)},
	})

	tailer := NewTranscriptTailer(path)
	m, err := tailer.TailAndProcess()
	if err != nil {
		t.Fatal(err)
	}
	if m.HasOpenToolCall {
		t.Error("expected HasOpenToolCall=false when results exceed uses")
	}
	if m.OpenToolCallCount != 0 {
		t.Errorf("expected OpenToolCallCount=0, got %d", m.OpenToolCallCount)
	}
}

// --- Local command tests (issues #62, #64) ---

// userMsg builds a user message event with string content (as Claude Code writes for local commands).
func userMsg(offset int, content string) map[string]interface{} {
	return map[string]interface{}{
		"type":      "user",
		"timestamp": ts(offset),
		"message": map[string]interface{}{
			"role":    "user",
			"content": content,
		},
	}
}

func TestLocalCommand_ClearTransitionsToReady(t *testing.T) {
	// A completed turn followed by /clear should end with LastEventType="turn_done".
	path := writeTranscriptLines(t, []map[string]interface{}{
		{"type": "user", "timestamp": ts(0)},
		{"type": "assistant", "timestamp": ts(1)},
		{"type": "system", "subtype": "stop_hook_summary", "timestamp": ts(2)},
		// /clear events
		userMsg(3, "<local-command-caveat>Caveat: The messages below were generated by the user while running local commands. DO NOT respond to them."),
		userMsg(4, "<command-name>/clear</command-name>\n            <command-message>clear</command-message>"),
		{"type": "system", "subtype": "local_command", "timestamp": ts(5)},
	})

	tailer := NewTranscriptTailer(path)
	m, err := tailer.TailAndProcess()
	if err != nil {
		t.Fatal(err)
	}
	if m.LastEventType != "turn_done" {
		t.Errorf("after /clear: LastEventType = %q, want %q", m.LastEventType, "turn_done")
	}
}

func TestLocalCommand_ContextPreservesState(t *testing.T) {
	// A completed turn followed by /context should preserve LastEventType="turn_done".
	// /context does NOT emit system local_command — only user messages.
	path := writeTranscriptLines(t, []map[string]interface{}{
		{"type": "user", "timestamp": ts(0)},
		{"type": "assistant", "timestamp": ts(1)},
		{"type": "system", "subtype": "stop_hook_summary", "timestamp": ts(2)},
		// /context events
		userMsg(3, "<local-command-caveat>Caveat: The messages below were generated by the user while running local commands."),
		userMsg(4, "<command-name>/context</command-name>\n            <command-message>context</command-message>"),
		userMsg(5, "<local-command-stdout>\x1b[1mContext Usage\x1b[22m\n...</local-command-stdout>"),
	})

	tailer := NewTranscriptTailer(path)
	m, err := tailer.TailAndProcess()
	if err != nil {
		t.Fatal(err)
	}
	if m.LastEventType != "turn_done" {
		t.Errorf("after /context: LastEventType = %q, want %q", m.LastEventType, "turn_done")
	}
}

func TestLocalCommand_ShellEscapePreservesState(t *testing.T) {
	// ! shell escape should not change state from a completed turn.
	path := writeTranscriptLines(t, []map[string]interface{}{
		{"type": "user", "timestamp": ts(0)},
		{"type": "assistant", "timestamp": ts(1)},
		{"type": "system", "subtype": "turn_duration", "timestamp": ts(2)},
		// ! ls events
		userMsg(3, "<local-command-caveat>Caveat: The messages below were generated by the user while running local commands."),
		userMsg(4, "<command-name>!</command-name>\n            <command-message>ls</command-message>"),
		userMsg(5, "<local-command-stdout>file1.txt\nfile2.txt\n</local-command-stdout>"),
	})

	tailer := NewTranscriptTailer(path)
	m, err := tailer.TailAndProcess()
	if err != nil {
		t.Fatal(err)
	}
	if m.LastEventType != "turn_done" {
		t.Errorf("after ! shell escape: LastEventType = %q, want %q", m.LastEventType, "turn_done")
	}
}

func TestLocalCommand_SkillNotFiltered(t *testing.T) {
	// Skill invocations (/seo, /simplify) start with <command-message>,
	// NOT <command-name> or <local-command-caveat>. They ARE real user input.
	path := writeTranscriptLines(t, []map[string]interface{}{
		{"type": "system", "subtype": "stop_hook_summary", "timestamp": ts(0)},
		userMsg(1, "<command-message>seo</command-message>\n<command-name>/seo</command-name>"),
	})

	tailer := NewTranscriptTailer(path)
	m, err := tailer.TailAndProcess()
	if err != nil {
		t.Fatal(err)
	}
	if m.LastEventType != "user" {
		t.Errorf("skill command: LastEventType = %q, want %q", m.LastEventType, "user")
	}
}

func TestLocalCommand_ClearFromWorkingState(t *testing.T) {
	// /clear while agent is working (mid-turn, no turn_done yet).
	// The system local_command event should set turn_done.
	path := writeTranscriptLines(t, []map[string]interface{}{
		{"type": "user", "timestamp": ts(0)},
		{"type": "assistant", "timestamp": ts(1), "message": map[string]interface{}{
			"role": "assistant",
			"content": []interface{}{
				map[string]interface{}{"type": "tool_use", "name": "Bash"},
			},
		}},
		// /clear before tool result comes back
		userMsg(2, "<local-command-caveat>Caveat: The messages below were generated by the user while running local commands."),
		userMsg(3, "<command-name>/clear</command-name>\n            <command-message>clear</command-message>"),
		{"type": "system", "subtype": "local_command", "timestamp": ts(4)},
	})

	tailer := NewTranscriptTailer(path)
	m, err := tailer.TailAndProcess()
	if err != nil {
		t.Fatal(err)
	}
	if m.LastEventType != "turn_done" {
		t.Errorf("after /clear mid-turn: LastEventType = %q, want %q", m.LastEventType, "turn_done")
	}
}

func TestHasOpenToolCall_MultipleRoundsAllClosed(t *testing.T) {
	// Multiple tool use/result rounds, all closed
	path := writeTranscriptLines(t, []map[string]interface{}{
		{"type": "tool_use", "timestamp": ts(0)},
		{"type": "tool_result", "timestamp": ts(1)},
		{"type": "tool_use", "timestamp": ts(2)},
		{"type": "tool_result", "timestamp": ts(3)},
		{"type": "tool_use", "timestamp": ts(4)},
		{"type": "tool_result", "timestamp": ts(5)},
	})

	tailer := NewTranscriptTailer(path)
	m, err := tailer.TailAndProcess()
	if err != nil {
		t.Fatal(err)
	}
	if m.HasOpenToolCall {
		t.Error("expected HasOpenToolCall=false when all tool calls are paired")
	}
	if m.OpenToolCallCount != 0 {
		t.Errorf("expected OpenToolCallCount=0, got %d", m.OpenToolCallCount)
	}
}
