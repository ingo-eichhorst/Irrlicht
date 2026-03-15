package copilot

import (
	"bytes"
	"encoding/json"
	"fmt"
	"testing"

	"irrlicht/core/domain/event"
)

// fakeHandler captures the last HandleEvent call.
type fakeHandler struct {
	lastEvent *event.HookEvent
	err       error
}

func (f *fakeHandler) HandleEvent(evt *event.HookEvent) error {
	f.lastEvent = evt
	return f.err
}

func TestDeriveSessionKey(t *testing.T) {
	key1 := DeriveSessionKey("/Users/ingo/projects/foo")
	key2 := DeriveSessionKey("/Users/ingo/projects/foo")
	key3 := DeriveSessionKey("/Users/ingo/projects/bar")

	if key1 != key2 {
		t.Errorf("same cwd should produce same key: got %q and %q", key1, key2)
	}
	if key1 == key3 {
		t.Errorf("different cwd should produce different key: both got %q", key1)
	}
	if len(key1) != len("copilot-")+16 {
		t.Errorf("expected key length %d, got %d (%q)", len("copilot-")+16, len(key1), key1)
	}
}

func TestTranslateSessionStart(t *testing.T) {
	tests := []struct {
		source          string
		wantMatcher     string
	}{
		{"new", ""},
		{"resume", "resume"},
		{"startup", "startup"},
		{"", ""},
	}

	for _, tc := range tests {
		t.Run("source="+tc.source, func(t *testing.T) {
			h := &fakeHandler{}
			a := New(h, "sessionStart")

			payload := map[string]interface{}{
				"timestamp":    1741042200000,
				"cwd":          "/tmp/proj",
				"source":       tc.source,
				"initialPrompt": "do something",
			}
			_, err := a.readAndHandleBytes(marshalJSON(t, payload))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if h.lastEvent == nil {
				t.Fatal("handler not called")
			}
			if h.lastEvent.HookEventName != "SessionStart" {
				t.Errorf("want HookEventName=SessionStart, got %q", h.lastEvent.HookEventName)
			}
			if h.lastEvent.Matcher != tc.wantMatcher {
				t.Errorf("want Matcher=%q, got %q", tc.wantMatcher, h.lastEvent.Matcher)
			}
			if h.lastEvent.Adapter != "copilot" {
				t.Errorf("want Adapter=copilot, got %q", h.lastEvent.Adapter)
			}
			wantSessionID := DeriveSessionKey("/tmp/proj")
			if h.lastEvent.SessionID != wantSessionID {
				t.Errorf("want SessionID=%q, got %q", wantSessionID, h.lastEvent.SessionID)
			}
		})
	}
}

func TestTranslateSessionEnd(t *testing.T) {
	tests := []struct {
		reason     string
		wantReason string
	}{
		{"user_exit", "prompt_input_exit"},
		{"complete", "complete"},
		{"error", "error"},
		{"abort", "abort"},
		{"timeout", "timeout"},
	}

	for _, tc := range tests {
		t.Run("reason="+tc.reason, func(t *testing.T) {
			h := &fakeHandler{}
			a := New(h, "sessionEnd")

			payload := map[string]interface{}{
				"timestamp": 1741042500000,
				"cwd":       "/tmp/proj",
				"reason":    tc.reason,
			}
			_, err := a.readAndHandleBytes(marshalJSON(t, payload))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if h.lastEvent.HookEventName != "SessionEnd" {
				t.Errorf("want HookEventName=SessionEnd, got %q", h.lastEvent.HookEventName)
			}
			if h.lastEvent.Reason != tc.wantReason {
				t.Errorf("want Reason=%q, got %q", tc.wantReason, h.lastEvent.Reason)
			}
		})
	}
}

func TestTranslatePreToolUse(t *testing.T) {
	h := &fakeHandler{}
	a := New(h, "preToolUse")

	payload := map[string]interface{}{
		"timestamp": 1741042320000,
		"cwd":       "/tmp/proj",
		"toolName":  "edit",
		"toolArgs":  `{"path":"foo.go"}`,
	}
	_, err := a.readAndHandleBytes(marshalJSON(t, payload))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if h.lastEvent.HookEventName != "PreToolUse" {
		t.Errorf("want HookEventName=PreToolUse, got %q", h.lastEvent.HookEventName)
	}
	// Copilot uses lowercase; adapter should TitleCase it.
	if h.lastEvent.ToolName != "Edit" {
		t.Errorf("want ToolName=Edit (TitleCased), got %q", h.lastEvent.ToolName)
	}
}

func TestTranslatePostToolUse(t *testing.T) {
	h := &fakeHandler{}
	a := New(h, "postToolUse")

	payload := map[string]interface{}{
		"timestamp": 1741042325000,
		"cwd":       "/tmp/proj",
		"toolName":  "bash",
		"toolArgs":  `{"cmd":"ls"}`,
		"toolResult": map[string]interface{}{
			"resultType":      "success",
			"textResultForLlm": "file1\nfile2",
		},
	}
	_, err := a.readAndHandleBytes(marshalJSON(t, payload))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if h.lastEvent.HookEventName != "PostToolUse" {
		t.Errorf("want HookEventName=PostToolUse, got %q", h.lastEvent.HookEventName)
	}
	if h.lastEvent.ToolName != "Bash" {
		t.Errorf("want ToolName=Bash, got %q", h.lastEvent.ToolName)
	}
}

func TestTranslateAgentStop(t *testing.T) {
	h := &fakeHandler{}
	a := New(h, "agentStop")

	payload := map[string]interface{}{
		"timestamp": 1741042400000,
		"cwd":       "/tmp/proj",
	}
	_, err := a.readAndHandleBytes(marshalJSON(t, payload))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if h.lastEvent.HookEventName != "Stop" {
		t.Errorf("want HookEventName=Stop, got %q", h.lastEvent.HookEventName)
	}
}

func TestTranslateErrorOccurred(t *testing.T) {
	h := &fakeHandler{}
	a := New(h, "errorOccurred")

	payload := map[string]interface{}{
		"timestamp": 1741042450000,
		"cwd":       "/tmp/proj",
		"error": map[string]interface{}{
			"message": "Tool execution failed: permission denied",
			"name":    "ToolError",
		},
	}
	_, err := a.readAndHandleBytes(marshalJSON(t, payload))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// errorOccurred maps to "Stop" (session-ready state)
	if h.lastEvent.HookEventName != "Stop" {
		t.Errorf("want HookEventName=Stop, got %q", h.lastEvent.HookEventName)
	}
}

func TestTranslateMissingCWD(t *testing.T) {
	h := &fakeHandler{}
	a := New(h, "sessionStart")

	payload := map[string]interface{}{
		"timestamp": 1741042200000,
		"source":    "new",
		// no "cwd"
	}
	_, err := a.readAndHandleBytes(marshalJSON(t, payload))
	if err == nil {
		t.Fatal("expected error for missing cwd, got nil")
	}
}

func TestTranslateUnknownEvent(t *testing.T) {
	h := &fakeHandler{}
	a := New(h, "unknownEvent")

	payload := map[string]interface{}{
		"timestamp": 1741042200000,
		"cwd":       "/tmp/proj",
	}
	_, err := a.readAndHandleBytes(marshalJSON(t, payload))
	if err == nil {
		t.Fatal("expected error for unknown event, got nil")
	}
}

func TestPayloadSizeLimit(t *testing.T) {
	h := &fakeHandler{}
	a := New(h, "sessionStart")

	// Build a payload larger than MaxPayloadSize
	giant := make([]byte, event.MaxPayloadSize+1)
	for i := range giant {
		giant[i] = 'x'
	}
	_, err := a.readAndHandleBytes(giant)
	if err == nil {
		t.Fatal("expected error for oversized payload, got nil")
	}
}

func TestTitleCase(t *testing.T) {
	tests := []struct{ in, want string }{
		{"bash", "Bash"},
		{"edit", "Edit"},
		{"", ""},
		{"Bash", "Bash"},
		{"CREATE", "CREATE"},
	}
	for _, tc := range tests {
		if got := titleCase(tc.in); got != tc.want {
			t.Errorf("titleCase(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// helpers

func marshalJSON(t *testing.T, v interface{}) []byte {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return data
}

// readAndHandleBytes is a test helper that feeds bytes directly without reading stdin.
func (a *Adapter) readAndHandleBytes(input []byte) (int, error) {
	payloadSize := len(input)
	if payloadSize > event.MaxPayloadSize {
		return payloadSize, fmt.Errorf("payload size %d exceeds maximum %d", payloadSize, event.MaxPayloadSize)
	}

	var payload copilotPayload
	if err := json.NewDecoder(bytes.NewReader(input)).Decode(&payload); err != nil {
		return payloadSize, fmt.Errorf("failed to parse Copilot JSON: %w", err)
	}

	evt, err := a.translate(&payload)
	if err != nil {
		return payloadSize, err
	}

	if err := a.handler.HandleEvent(evt); err != nil {
		return payloadSize, err
	}
	return payloadSize, nil
}
