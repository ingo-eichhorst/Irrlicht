package cursor

import (
	"encoding/json"
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

// --- helpers ---

func marshalJSON(t *testing.T, v any) []byte {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return data
}

// readAndHandleBytes is exposed via the unexported method on Adapter (same package).
// We reuse the internal method to avoid stdin dependency in tests.
func feedAdapter(t *testing.T, h *fakeHandler, input []byte) (int, error) {
	t.Helper()
	a := New(h)
	// We can't call readAndHandleBytes directly from test (it's unexported in the
	// package under test) — but this IS the same package, so it's fine.
	return a.readAndHandleBytes(input)
}

func basePayload(eventName string) map[string]any {
	return map[string]any{
		"hook_event_name":  eventName,
		"conversation_id":  "conv_abc123",
		"model":            "claude-sonnet-4-5",
		"cursor_version":   "0.45.0",
		"workspace_roots":  []string{"/tmp/proj"},
		"transcript_path":  "/tmp/proj/.cursor/transcript.jsonl",
	}
}

// --- session start ---

func TestTranslateSessionStartNew(t *testing.T) {
	p := basePayload("sessionStart")
	p["source"] = "new"
	p["permission_mode"] = "default"

	h := &fakeHandler{}
	_, err := feedAdapter(t, h, marshalJSON(t, p))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if h.lastEvent.HookEventName != "SessionStart" {
		t.Errorf("want HookEventName=SessionStart, got %q", h.lastEvent.HookEventName)
	}
	if h.lastEvent.SessionID != "cursor_conv_abc123" {
		t.Errorf("want SessionID=cursor_conv_abc123, got %q", h.lastEvent.SessionID)
	}
	if h.lastEvent.Matcher != "" {
		t.Errorf("want Matcher empty for new session, got %q", h.lastEvent.Matcher)
	}
	if h.lastEvent.CWD != "/tmp/proj" {
		t.Errorf("want CWD=/tmp/proj, got %q", h.lastEvent.CWD)
	}
	if h.lastEvent.Model != "claude-sonnet-4-5" {
		t.Errorf("want Model=claude-sonnet-4-5, got %q", h.lastEvent.Model)
	}
	if h.lastEvent.Adapter != "cursor" {
		t.Errorf("want Adapter=cursor, got %q", h.lastEvent.Adapter)
	}
}

func TestTranslateSessionStartResume(t *testing.T) {
	p := basePayload("sessionStart")
	p["source"] = "resume"

	h := &fakeHandler{}
	_, err := feedAdapter(t, h, marshalJSON(t, p))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if h.lastEvent.Matcher != "resume" {
		t.Errorf("want Matcher=resume, got %q", h.lastEvent.Matcher)
	}
}

func TestTranslateSessionStartStartup(t *testing.T) {
	p := basePayload("sessionStart")
	p["source"] = "startup"

	h := &fakeHandler{}
	_, err := feedAdapter(t, h, marshalJSON(t, p))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if h.lastEvent.Matcher != "startup" {
		t.Errorf("want Matcher=startup, got %q", h.lastEvent.Matcher)
	}
}

// --- session end ---

func TestTranslateSessionEnd(t *testing.T) {
	reasons := []string{"user_exit", "timeout", "clear", "logout", ""}

	for _, reason := range reasons {
		t.Run("reason="+reason, func(t *testing.T) {
			p := basePayload("sessionEnd")
			p["reason"] = reason

			h := &fakeHandler{}
			_, err := feedAdapter(t, h, marshalJSON(t, p))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if h.lastEvent.HookEventName != "SessionEnd" {
				t.Errorf("want HookEventName=SessionEnd, got %q", h.lastEvent.HookEventName)
			}
			if h.lastEvent.Reason != reason {
				t.Errorf("want Reason=%q, got %q", reason, h.lastEvent.Reason)
			}
		})
	}
}

// --- stop ---

func TestTranslateStop(t *testing.T) {
	p := basePayload("stop")
	p["stop_reason"] = "end_turn"

	h := &fakeHandler{}
	_, err := feedAdapter(t, h, marshalJSON(t, p))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if h.lastEvent.HookEventName != "Stop" {
		t.Errorf("want HookEventName=Stop, got %q", h.lastEvent.HookEventName)
	}
}

// --- tool use ---

func TestTranslatePreToolUse(t *testing.T) {
	p := basePayload("preToolUse")
	p["tool_name"] = "ReadFile"
	p["tool_input"] = map[string]any{"path": "foo.go"}

	h := &fakeHandler{}
	_, err := feedAdapter(t, h, marshalJSON(t, p))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if h.lastEvent.HookEventName != "PreToolUse" {
		t.Errorf("want HookEventName=PreToolUse, got %q", h.lastEvent.HookEventName)
	}
	if h.lastEvent.ToolName != "ReadFile" {
		t.Errorf("want ToolName=ReadFile, got %q", h.lastEvent.ToolName)
	}
}

func TestTranslatePostToolUse(t *testing.T) {
	p := basePayload("postToolUse")
	p["tool_name"] = "WriteFile"

	h := &fakeHandler{}
	_, err := feedAdapter(t, h, marshalJSON(t, p))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if h.lastEvent.HookEventName != "PostToolUse" {
		t.Errorf("want HookEventName=PostToolUse, got %q", h.lastEvent.HookEventName)
	}
}

func TestTranslatePostToolUseFailure(t *testing.T) {
	p := basePayload("postToolUseFailure")
	p["tool_name"] = "Bash"
	p["error"] = "permission denied"

	h := &fakeHandler{}
	_, err := feedAdapter(t, h, marshalJSON(t, p))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// postToolUseFailure maps to PostToolUse
	if h.lastEvent.HookEventName != "PostToolUse" {
		t.Errorf("want HookEventName=PostToolUse, got %q", h.lastEvent.HookEventName)
	}
}

// --- shell execution ---

func TestTranslateBeforeShellExecution(t *testing.T) {
	p := basePayload("beforeShellExecution")
	p["command"] = "ls -la"
	p["working_dir"] = "/tmp/proj"

	h := &fakeHandler{}
	_, err := feedAdapter(t, h, marshalJSON(t, p))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if h.lastEvent.HookEventName != "PreToolUse" {
		t.Errorf("want HookEventName=PreToolUse, got %q", h.lastEvent.HookEventName)
	}
	if h.lastEvent.ToolName != "Bash" {
		t.Errorf("want ToolName=Bash, got %q", h.lastEvent.ToolName)
	}
}

func TestTranslateAfterShellExecution(t *testing.T) {
	p := basePayload("afterShellExecution")
	p["command"] = "ls -la"
	p["exit_code"] = 0

	h := &fakeHandler{}
	_, err := feedAdapter(t, h, marshalJSON(t, p))
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

// --- user prompt ---

func TestTranslateBeforeSubmitPrompt(t *testing.T) {
	p := basePayload("beforeSubmitPrompt")
	p["prompt"] = "Help me fix this bug"

	h := &fakeHandler{}
	_, err := feedAdapter(t, h, marshalJSON(t, p))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if h.lastEvent.HookEventName != "UserPromptSubmit" {
		t.Errorf("want HookEventName=UserPromptSubmit, got %q", h.lastEvent.HookEventName)
	}
	if h.lastEvent.Prompt != "Help me fix this bug" {
		t.Errorf("want Prompt=%q, got %q", "Help me fix this bug", h.lastEvent.Prompt)
	}
}

// --- subagent ---

func TestTranslateSubagentStart(t *testing.T) {
	p := basePayload("subagentStart")
	p["subagent_id"] = "sub_xyz789"
	p["parent_conversation_id"] = "conv_abc123"

	h := &fakeHandler{}
	_, err := feedAdapter(t, h, marshalJSON(t, p))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if h.lastEvent.HookEventName != "SessionStart" {
		t.Errorf("want HookEventName=SessionStart, got %q", h.lastEvent.HookEventName)
	}
	// Subagent uses its own ID as session key.
	if h.lastEvent.SessionID != "cursor_sub_xyz789" {
		t.Errorf("want SessionID=cursor_sub_xyz789, got %q", h.lastEvent.SessionID)
	}
	if h.lastEvent.ParentSessionID != "cursor_conv_abc123" {
		t.Errorf("want ParentSessionID=cursor_conv_abc123, got %q", h.lastEvent.ParentSessionID)
	}
}

func TestTranslateSubagentStop(t *testing.T) {
	p := basePayload("subagentStop")
	p["subagent_id"] = "sub_xyz789"

	h := &fakeHandler{}
	_, err := feedAdapter(t, h, marshalJSON(t, p))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if h.lastEvent.HookEventName != "SubagentStop" {
		t.Errorf("want HookEventName=SubagentStop, got %q", h.lastEvent.HookEventName)
	}
}

// --- compaction ---

func TestTranslatePreCompactAuto(t *testing.T) {
	p := basePayload("preCompact")
	p["compact_type"] = "auto"

	h := &fakeHandler{}
	_, err := feedAdapter(t, h, marshalJSON(t, p))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if h.lastEvent.HookEventName != "PreCompact" {
		t.Errorf("want HookEventName=PreCompact, got %q", h.lastEvent.HookEventName)
	}
	if h.lastEvent.Matcher != "auto" {
		t.Errorf("want Matcher=auto, got %q", h.lastEvent.Matcher)
	}
}

// --- after agent thought ---

func TestTranslateAfterAgentThought(t *testing.T) {
	p := basePayload("afterAgentThought")
	p["thought"] = "I should check the file first"

	h := &fakeHandler{}
	_, err := feedAdapter(t, h, marshalJSON(t, p))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// afterAgentThought maps to PreToolUse to keep session in "working" during reasoning.
	if h.lastEvent.HookEventName != "PreToolUse" {
		t.Errorf("want HookEventName=PreToolUse, got %q", h.lastEvent.HookEventName)
	}
}

// --- workspace_roots → cwd ---

func TestWorkspaceRootsFirstEntryBecomessCWD(t *testing.T) {
	p := basePayload("stop")
	p["workspace_roots"] = []string{"/primary/root", "/secondary/root"}

	h := &fakeHandler{}
	_, err := feedAdapter(t, h, marshalJSON(t, p))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if h.lastEvent.CWD != "/primary/root" {
		t.Errorf("want CWD=/primary/root, got %q", h.lastEvent.CWD)
	}
}

func TestWorkspaceRootsEmptyAllowsEmptyCWD(t *testing.T) {
	p := basePayload("stop")
	p["workspace_roots"] = []string{}

	h := &fakeHandler{}
	_, err := feedAdapter(t, h, marshalJSON(t, p))
	// Empty workspace_roots is allowed — CWD will be empty.
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if h.lastEvent.CWD != "" {
		t.Errorf("want CWD empty, got %q", h.lastEvent.CWD)
	}
}

// --- error cases ---

func TestMissingConversationID(t *testing.T) {
	p := map[string]any{
		"hook_event_name": "sessionStart",
		"workspace_roots": []string{"/tmp/proj"},
		// missing conversation_id
	}

	h := &fakeHandler{}
	_, err := feedAdapter(t, h, marshalJSON(t, p))
	if err == nil {
		t.Fatal("expected error for missing conversation_id, got nil")
	}
}

func TestMissingHookEventName(t *testing.T) {
	p := map[string]any{
		"conversation_id": "conv_abc",
		"workspace_roots": []string{"/tmp/proj"},
		// missing hook_event_name
	}

	h := &fakeHandler{}
	_, err := feedAdapter(t, h, marshalJSON(t, p))
	if err == nil {
		t.Fatal("expected error for missing hook_event_name, got nil")
	}
}

func TestUnknownEventName(t *testing.T) {
	p := basePayload("unknownEvent")

	h := &fakeHandler{}
	_, err := feedAdapter(t, h, marshalJSON(t, p))
	if err == nil {
		t.Fatal("expected error for unknown event name, got nil")
	}
}

func TestPayloadSizeLimit(t *testing.T) {
	h := &fakeHandler{}
	a := New(h)

	giant := make([]byte, event.MaxPayloadSize+1)
	for i := range giant {
		giant[i] = 'x'
	}
	_, err := a.readAndHandleBytes(giant)
	if err == nil {
		t.Fatal("expected error for oversized payload, got nil")
	}
}

func TestInvalidJSON(t *testing.T) {
	h := &fakeHandler{}
	_, err := feedAdapter(t, h, []byte("not valid json"))
	if err == nil {
		t.Fatal("expected error for invalid JSON, got nil")
	}
}

// --- SessionIDPrefix ---

func TestSessionIDPrefix(t *testing.T) {
	p := basePayload("stop")
	p["conversation_id"] = "myconv"

	h := &fakeHandler{}
	_, err := feedAdapter(t, h, marshalJSON(t, p))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if h.lastEvent.SessionID != "cursor_myconv" {
		t.Errorf("want SessionID=cursor_myconv, got %q", h.lastEvent.SessionID)
	}
}

// --- IsApprovalProne ---

func TestIsApprovalProne(t *testing.T) {
	tests := []struct {
		tool string
		want bool
	}{
		{"Bash", true},
		{"bash", true},
		{"shell_execute", true},
		{"RunProcess", true},
		{"WriteFile", true},
		{"EditDocument", true},
		{"CreateFile", true},
		{"DeleteRecord", true},
		{"ReadFile", false},
		{"ListDirectory", false},
		{"SearchCode", false},
		{"", false},
	}

	for _, tc := range tests {
		got := IsApprovalProne(tc.tool)
		if got != tc.want {
			t.Errorf("IsApprovalProne(%q) = %v, want %v", tc.tool, got, tc.want)
		}
	}
}

// --- transcript path ---

func TestTranscriptPathPreserved(t *testing.T) {
	p := basePayload("preToolUse")
	p["tool_name"] = "ReadFile"
	p["transcript_path"] = "/tmp/proj/.cursor/transcript.jsonl"

	h := &fakeHandler{}
	_, err := feedAdapter(t, h, marshalJSON(t, p))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if h.lastEvent.TranscriptPath != "/tmp/proj/.cursor/transcript.jsonl" {
		t.Errorf("want TranscriptPath preserved, got %q", h.lastEvent.TranscriptPath)
	}
}

