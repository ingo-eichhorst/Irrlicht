package claudecode

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// mockTarget records calls to HandlePermissionHook for assertions.
type mockTarget struct {
	mu    sync.Mutex
	calls []hookCall
}

type hookCall struct {
	sessionID, transcriptPath, hookEventName string
}

func (m *mockTarget) HandlePermissionHook(sessionID, transcriptPath, hookEventName string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, hookCall{sessionID, transcriptPath, hookEventName})
}

func (m *mockTarget) getCalls() []hookCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]hookCall{}, m.calls...)
}

// mockLogger satisfies outbound.Logger.
type mockLogger struct{}

func (mockLogger) LogInfo(_, _, _ string)                                  {}
func (mockLogger) LogError(_, _, _ string)                                 {}
func (mockLogger) LogProcessingTime(_, _ string, _ int64, _ int, _ string) {}
func (mockLogger) Close() error                                            { return nil }

func TestSessionIDFromTranscriptPath(t *testing.T) {
	tests := []struct {
		path string
		want string
	}{
		{"/Users/u/.claude/projects/abc/00893aaf-19fa-41d2-8238-13269b9b3ca0.jsonl", "00893aaf-19fa-41d2-8238-13269b9b3ca0"},
		{"/tmp/test.jsonl", "test"},
		{"", ""},
		{"noext", "noext"},
	}
	for _, tt := range tests {
		got := sessionIDFromTranscriptPath(tt.path)
		if got != tt.want {
			t.Errorf("sessionIDFromTranscriptPath(%q) = %q, want %q", tt.path, got, tt.want)
		}
	}
}

func TestHookHandler_PermissionRequest(t *testing.T) {
	target := &mockTarget{}
	handler := NewHookHandler(target, mockLogger{})

	payload := hookPayload{
		TranscriptPath: "/Users/u/.claude/projects/p/sess-123.jsonl",
		HookEventName:  "PermissionRequest",
		ToolName:       "Bash",
	}
	body, _ := json.Marshal(payload)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/hooks/claudecode", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	handler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	calls := target.getCalls()
	if len(calls) != 1 {
		t.Fatalf("got %d calls, want 1", len(calls))
	}
	if calls[0].sessionID != "sess-123" {
		t.Errorf("sessionID = %q, want %q", calls[0].sessionID, "sess-123")
	}
	if calls[0].hookEventName != "PermissionRequest" {
		t.Errorf("hookEventName = %q, want %q", calls[0].hookEventName, "PermissionRequest")
	}
}

func TestHookHandler_PostToolUse(t *testing.T) {
	target := &mockTarget{}
	handler := NewHookHandler(target, mockLogger{})

	payload := hookPayload{
		TranscriptPath: "/Users/u/.claude/projects/p/sess-456.jsonl",
		HookEventName:  "PostToolUse",
		ToolName:       "Write",
		ToolUseID:      "toolu_abc",
	}
	body, _ := json.Marshal(payload)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/hooks/claudecode", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	handler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	calls := target.getCalls()
	if len(calls) != 1 || calls[0].hookEventName != "PostToolUse" {
		t.Fatalf("unexpected calls: %+v", calls)
	}
}

func TestHookHandler_PreToolUse(t *testing.T) {
	target := &mockTarget{}
	handler := NewHookHandler(target, mockLogger{})

	payload := hookPayload{
		TranscriptPath: "/Users/u/.claude/projects/p/sess-pre.jsonl",
		HookEventName:  "PreToolUse",
		ToolName:       "AskUserQuestion",
		ToolUseID:      "toolu_pre",
	}
	body, _ := json.Marshal(payload)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/hooks/claudecode", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	handler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	calls := target.getCalls()
	if len(calls) != 1 {
		t.Fatalf("got %d calls, want 1", len(calls))
	}
	if calls[0].sessionID != "sess-pre" {
		t.Errorf("sessionID = %q, want %q", calls[0].sessionID, "sess-pre")
	}
	if calls[0].hookEventName != "PreToolUse" {
		t.Errorf("hookEventName = %q, want %q", calls[0].hookEventName, "PreToolUse")
	}
}

// TestHookHandler_PreToolUse_RejectsUnexpectedTool verifies the defensive
// guard: even if settings.json was edited so PreToolUse fires for, say, Bash,
// the handler refuses to set the permission-pending flag. The matcher is not
// the sole filter.
func TestHookHandler_PreToolUse_RejectsUnexpectedTool(t *testing.T) {
	target := &mockTarget{}
	handler := NewHookHandler(target, mockLogger{})

	payload := hookPayload{
		TranscriptPath: "/Users/u/.claude/projects/p/sess-x.jsonl",
		HookEventName:  "PreToolUse",
		ToolName:       "Bash",
		ToolUseID:      "toolu_bash",
	}
	body, _ := json.Marshal(payload)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/hooks/claudecode", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	handler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (handler should accept but ignore)", rec.Code, http.StatusOK)
	}
	if len(target.getCalls()) != 0 {
		t.Errorf("PreToolUse for Bash should not dispatch; got %d calls", len(target.getCalls()))
	}
}

func TestHookHandler_PostToolUseFailure(t *testing.T) {
	target := &mockTarget{}
	handler := NewHookHandler(target, mockLogger{})

	payload := hookPayload{
		TranscriptPath: "/Users/u/.claude/projects/p/sess-789.jsonl",
		HookEventName:  "PostToolUseFailure",
		ToolName:       "Bash",
		IsInterrupt:    true,
	}
	body, _ := json.Marshal(payload)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/hooks/claudecode", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	handler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	calls := target.getCalls()
	if len(calls) != 1 || calls[0].hookEventName != "PostToolUseFailure" {
		t.Fatalf("unexpected calls: %+v", calls)
	}
}

func TestHookHandler_UnrecognizedEvent(t *testing.T) {
	target := &mockTarget{}
	handler := NewHookHandler(target, mockLogger{})

	payload := hookPayload{
		TranscriptPath: "/Users/u/.claude/projects/p/sess.jsonl",
		HookEventName:  "SessionStart",
		ToolName:       "",
	}
	body, _ := json.Marshal(payload)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/hooks/claudecode", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	handler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if len(target.getCalls()) != 0 {
		t.Error("unrecognized event should not dispatch to target")
	}
}

func TestHookHandler_MissingTranscriptPath(t *testing.T) {
	target := &mockTarget{}
	handler := NewHookHandler(target, mockLogger{})

	payload := hookPayload{
		HookEventName: "PermissionRequest",
		ToolName:      "Bash",
	}
	body, _ := json.Marshal(payload)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/hooks/claudecode", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	handler(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestHookHandler_WrongMethod(t *testing.T) {
	target := &mockTarget{}
	handler := NewHookHandler(target, mockLogger{})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/hooks/claudecode", nil)
	rec := httptest.NewRecorder()
	handler(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
	}
}

func TestHookHandler_MalformedJSON(t *testing.T) {
	target := &mockTarget{}
	handler := NewHookHandler(target, mockLogger{})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/hooks/claudecode", bytes.NewReader([]byte("not json")))
	rec := httptest.NewRecorder()
	handler(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}
