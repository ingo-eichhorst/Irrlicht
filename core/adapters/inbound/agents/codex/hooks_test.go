package codex

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"irrlicht/core/domain/permission"
	"irrlicht/core/internal/contracttesting"
)

// mockTarget records calls to HandlePermissionHook, HandleStopHook and
// ClearPermissionPending for assertions.
type mockTarget struct {
	mu         sync.Mutex
	permCalls  []permCall
	stopCalls  []stopCall
	clearCalls []string
}

type permCall struct {
	sessionID, transcriptPath, hookEventName string
}

type stopCall struct {
	sessionID, transcriptPath, lastAssistantText string
	waitingCue                                   bool
}

func (m *mockTarget) HandlePermissionHook(sessionID, transcriptPath, hookEventName string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.permCalls = append(m.permCalls, permCall{sessionID, transcriptPath, hookEventName})
}

func (m *mockTarget) HandleStopHook(sessionID, transcriptPath, lastAssistantText string, waitingCue bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.stopCalls = append(m.stopCalls, stopCall{sessionID, transcriptPath, lastAssistantText, waitingCue})
}

func (m *mockTarget) ClearPermissionPending(sessionID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.clearCalls = append(m.clearCalls, sessionID)
}

func (m *mockTarget) getPermCalls() []permCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]permCall{}, m.permCalls...)
}

func (m *mockTarget) getStopCalls() []stopCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]stopCall{}, m.stopCalls...)
}

func (m *mockTarget) getClearCalls() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string{}, m.clearCalls...)
}

func (m *mockTarget) totalCalls() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.permCalls) + len(m.stopCalls) + len(m.clearCalls)
}

func (m *mockTarget) reset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.permCalls = nil
	m.stopCalls = nil
	m.clearCalls = nil
}

// mutableGate is a ConsentGranter whose grant state can be flipped between
// permission states for the AssertPermissionGated contract.
type mutableGate struct {
	mu      sync.Mutex
	granted bool
}

func (g *mutableGate) Granted(string, string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.granted
}

func (g *mutableGate) setGranted(v bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.granted = v
}

// mockLogger is a no-op outbound.Logger for the handler under test.
type mockLogger struct{}

func (mockLogger) LogInfo(string, string, string)                        {}
func (mockLogger) LogError(string, string, string)                       {}
func (mockLogger) LogProcessingTime(string, string, int64, int, string) {}
func (mockLogger) Close() error                                          { return nil }

func postHook(t *testing.T, handler http.HandlerFunc, payload codexHookPayload) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/hooks/codex", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	handler(rec, req)
	return rec
}

func TestHookHandler_PermissionRequest(t *testing.T) {
	target := &mockTarget{}
	handler := NewHookHandler(target, nil, mockLogger{})

	rec := postHook(t, handler, codexHookPayload{
		SessionID:     "sess-1",
		HookEventName: HookPermissionRequest,
		ToolName:      "shell",
	})

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200", rec.Code)
	}
	calls := target.getPermCalls()
	if len(calls) != 1 {
		t.Fatalf("HandlePermissionHook calls: got %d, want 1", len(calls))
	}
	if calls[0].sessionID != "sess-1" || calls[0].hookEventName != HookPermissionRequest {
		t.Errorf("call: got %+v, want sessionID=sess-1 event=PermissionRequest", calls[0])
	}
}

func TestHookHandler_PostToolUseClears(t *testing.T) {
	target := &mockTarget{}
	handler := NewHookHandler(target, nil, mockLogger{})

	postHook(t, handler, codexHookPayload{
		SessionID:     "sess-1",
		HookEventName: HookPostToolUse,
		ToolName:      "shell",
	})

	calls := target.getPermCalls()
	if len(calls) != 1 || calls[0].hookEventName != HookPostToolUse {
		t.Fatalf("PostToolUse should route to HandlePermissionHook; got %+v", calls)
	}
}

func TestHookHandler_Stop(t *testing.T) {
	target := &mockTarget{}
	handler := NewHookHandler(target, nil, mockLogger{})

	rec := postHook(t, handler, codexHookPayload{
		SessionID:            "sess-1",
		HookEventName:        HookStop,
		LastAssistantMessage: "All set. Want me to continue?",
	})

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200", rec.Code)
	}
	// Stop must clear any pending approval (deny-then-abort edge) before
	// recording turn-done.
	clears := target.getClearCalls()
	if len(clears) != 1 || clears[0] != "sess-1" {
		t.Fatalf("ClearPermissionPending: got %v, want [sess-1]", clears)
	}
	stops := target.getStopCalls()
	if len(stops) != 1 {
		t.Fatalf("HandleStopHook calls: got %d, want 1", len(stops))
	}
	if stops[0].sessionID != "sess-1" {
		t.Errorf("stop sessionID: got %q, want sess-1", stops[0].sessionID)
	}
	if stops[0].lastAssistantText == "" {
		t.Error("stop lastAssistantText: got empty, want the final message text")
	}
	// The trailing "?" is a waiting cue → the turn routes to waiting, not ready.
	if !stops[0].waitingCue {
		t.Error("stop waitingCue: got false, want true (message ends in a question)")
	}
}

func TestHookHandler_StopNoCue(t *testing.T) {
	target := &mockTarget{}
	handler := NewHookHandler(target, nil, mockLogger{})

	postHook(t, handler, codexHookPayload{
		SessionID:            "sess-1",
		HookEventName:        HookStop,
		LastAssistantMessage: "Done. I applied the change and the tests pass.",
	})

	stops := target.getStopCalls()
	if len(stops) != 1 {
		t.Fatalf("HandleStopHook calls: got %d, want 1", len(stops))
	}
	if stops[0].waitingCue {
		t.Error("stop waitingCue: got true, want false (statement, no question or cue)")
	}
}

func TestHookHandler_ResolvesSessionIDFromTranscript(t *testing.T) {
	// A Codex session ID is the session_meta header's payload.id, not the
	// filename stem, so the handler must resolve it via the transcript path —
	// the same way fswatcher assigns IDs — and prefer it over the payload's
	// own session_id.
	dir := t.TempDir()
	transcript := filepath.Join(dir, "rollout-2026-07-18T00-00-00-abcdefabcdef.jsonl")
	meta := `{"type":"session_meta","payload":{"id":"real-session-uuid"}}` + "\n"
	if err := os.WriteFile(transcript, []byte(meta), 0o600); err != nil {
		t.Fatalf("write transcript: %v", err)
	}

	target := &mockTarget{}
	handler := NewHookHandler(target, nil, mockLogger{})
	postHook(t, handler, codexHookPayload{
		SessionID:      "hook-payload-session-id",
		TranscriptPath: transcript,
		HookEventName:  HookPermissionRequest,
		ToolName:       "shell",
	})

	calls := target.getPermCalls()
	if len(calls) != 1 {
		t.Fatalf("HandlePermissionHook calls: got %d, want 1", len(calls))
	}
	if calls[0].sessionID != "real-session-uuid" {
		t.Errorf("resolved sessionID: got %q, want real-session-uuid (from session_meta, not payload session_id)", calls[0].sessionID)
	}
}

func TestHookHandler_MethodNotAllowed(t *testing.T) {
	handler := NewHookHandler(&mockTarget{}, nil, mockLogger{})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/hooks/codex", nil)
	rec := httptest.NewRecorder()
	handler(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("status: got %d, want 405", rec.Code)
	}
}

func TestHookHandler_BadJSON(t *testing.T) {
	target := &mockTarget{}
	handler := NewHookHandler(target, nil, mockLogger{})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/hooks/codex", bytes.NewReader([]byte("{not json")))
	rec := httptest.NewRecorder()
	handler(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status: got %d, want 400", rec.Code)
	}
	if target.totalCalls() != 0 {
		t.Error("target should not be called on bad JSON")
	}
}

func TestHookHandler_MissingSessionIdentity(t *testing.T) {
	target := &mockTarget{}
	handler := NewHookHandler(target, nil, mockLogger{})
	rec := postHook(t, handler, codexHookPayload{HookEventName: HookPermissionRequest})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status: got %d, want 400", rec.Code)
	}
	if target.totalCalls() != 0 {
		t.Error("target should not be called when session identity is missing")
	}
}

func TestHookHandler_UnrecognizedEvent(t *testing.T) {
	target := &mockTarget{}
	handler := NewHookHandler(target, nil, mockLogger{})
	rec := postHook(t, handler, codexHookPayload{
		SessionID:     "sess-1",
		HookEventName: "PreToolUse", // not installed for codex
	})
	if rec.Code != http.StatusOK {
		t.Errorf("status: got %d, want 200 (unrecognized events are accepted and ignored)", rec.Code)
	}
	if target.totalCalls() != 0 {
		t.Error("target should not be called for an unrecognized event")
	}
}

func TestHookHandler_PermissionGateContract(t *testing.T) {
	target := &mockTarget{}
	gate := &mutableGate{}
	handler := NewHookHandler(target, gate, mockLogger{})
	payload := codexHookPayload{
		SessionID:     "sess-gate",
		HookEventName: HookPermissionRequest,
		ToolName:      "shell",
	}
	contracttesting.AssertPermissionGated(t, contracttesting.PermissionGate{
		SetState: func(state permission.State) { gate.setGranted(state == permission.StateGranted) },
		Exercise: func() { target.reset(); postHook(t, handler, payload) },
		Observe:  func() bool { return target.totalCalls() > 0 },
	})
}
