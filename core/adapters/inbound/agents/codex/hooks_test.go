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

// mockTarget records calls to HandlePermissionHook and HandleStopHook for
// assertions.
type mockTarget struct {
	mu        sync.Mutex
	permCalls []permCall
	stopCalls []stopCall
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

func (m *mockTarget) totalCalls() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.permCalls) + len(m.stopCalls)
}

func (m *mockTarget) reset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.permCalls = nil
	m.stopCalls = nil
}

// mutableGate is a ConsentGranter whose grant state can be flipped between
// permission states for the AssertPermissionGated contract. It grants (or
// denies) every key uniformly.
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

// keyedGate grants exactly the permission keys in its set — used to exercise
// the hooks-granted / transcripts-denied combination.
type keyedGate map[string]bool

func (g keyedGate) Granted(_, key string) bool { return g[key] }

// mockLogger is a no-op outbound.Logger for the handler under test.
type mockLogger struct{}

func (mockLogger) LogInfo(string, string, string)                       {}
func (mockLogger) LogError(string, string, string)                      {}
func (mockLogger) LogProcessingTime(string, string, int64, int, string) {}
func (mockLogger) Close() error                                         { return nil }

// writeSessionTranscript creates a Codex rollout file whose session_meta header
// carries id, so sessionIDFromPath (and thus the handler) resolves to id.
func writeSessionTranscript(t *testing.T, id string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "rollout-2026-07-18T00-00-00-abcdefabcdef.jsonl")
	meta := `{"type":"session_meta","payload":{"id":"` + id + `"}}` + "\n"
	if err := os.WriteFile(path, []byte(meta), 0o600); err != nil {
		t.Fatalf("write transcript: %v", err)
	}
	return path
}

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
		TranscriptPath: writeSessionTranscript(t, "sess-1"),
		HookEventName:  HookPermissionRequest,
		ToolName:       "shell",
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
		TranscriptPath: writeSessionTranscript(t, "sess-1"),
		HookEventName:  HookPostToolUse,
		ToolName:       "shell",
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
		TranscriptPath:       writeSessionTranscript(t, "sess-1"),
		HookEventName:        HookStop,
		LastAssistantMessage: "All set. Want me to continue?",
	})

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200", rec.Code)
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
		TranscriptPath:       writeSessionTranscript(t, "sess-1"),
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
	// the same way fswatcher assigns IDs.
	target := &mockTarget{}
	handler := NewHookHandler(target, nil, mockLogger{})
	postHook(t, handler, codexHookPayload{
		TranscriptPath: writeSessionTranscript(t, "real-session-uuid"),
		HookEventName:  HookPermissionRequest,
		ToolName:       "shell",
	})

	calls := target.getPermCalls()
	if len(calls) != 1 {
		t.Fatalf("HandlePermissionHook calls: got %d, want 1", len(calls))
	}
	if calls[0].sessionID != "real-session-uuid" {
		t.Errorf("resolved sessionID: got %q, want real-session-uuid (from session_meta)", calls[0].sessionID)
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

func TestHookHandler_MissingTranscriptPath(t *testing.T) {
	target := &mockTarget{}
	handler := NewHookHandler(target, nil, mockLogger{})
	rec := postHook(t, handler, codexHookPayload{HookEventName: HookPermissionRequest})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status: got %d, want 400 (transcript_path is required to resolve the session id)", rec.Code)
	}
	if target.totalCalls() != 0 {
		t.Error("target should not be called when transcript_path is missing")
	}
}

func TestHookHandler_UnresolvableTranscriptDropped(t *testing.T) {
	// A transcript_path that can't be read (no header yet / gone) is dropped
	// with 200 rather than mis-keyed onto a guessed session id.
	target := &mockTarget{}
	handler := NewHookHandler(target, nil, mockLogger{})
	rec := postHook(t, handler, codexHookPayload{
		TranscriptPath: filepath.Join(t.TempDir(), "does-not-exist.jsonl"),
		HookEventName:  HookPermissionRequest,
	})
	if rec.Code != http.StatusOK {
		t.Errorf("status: got %d, want 200 (unresolvable transcript is dropped, not an error)", rec.Code)
	}
	if target.totalCalls() != 0 {
		t.Error("target should not be called when the session id can't be resolved")
	}
}

func TestHookHandler_TranscriptsConsentGatesTheRead(t *testing.T) {
	// The receiver reads the transcript file to resolve the session id, so that
	// read must be gated behind the "transcripts" consent — not merely the
	// "hooks" write consent. With hooks granted but transcripts denied, the
	// hook is dropped and the target is never called (issue #1174 review).
	target := &mockTarget{}
	gate := keyedGate{PermissionKeyHooks: true, PermissionKeyTranscripts: false}
	handler := NewHookHandler(target, gate, mockLogger{})

	rec := postHook(t, handler, codexHookPayload{
		TranscriptPath: writeSessionTranscript(t, "sess-1"),
		HookEventName:  HookPermissionRequest,
	})
	if rec.Code != http.StatusOK {
		t.Errorf("status: got %d, want 200", rec.Code)
	}
	if target.totalCalls() != 0 {
		t.Error("transcript read (and dispatch) happened without transcripts consent")
	}
}

func TestHookHandler_UnrecognizedEvent(t *testing.T) {
	target := &mockTarget{}
	handler := NewHookHandler(target, nil, mockLogger{})
	rec := postHook(t, handler, codexHookPayload{
		TranscriptPath: writeSessionTranscript(t, "sess-1"),
		HookEventName:  "PreToolUse", // not installed for codex
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
		TranscriptPath: writeSessionTranscript(t, "sess-gate"),
		HookEventName:  HookPermissionRequest,
		ToolName:       "shell",
	}
	contracttesting.AssertPermissionGated(t, contracttesting.PermissionGate{
		SetState: func(state permission.State) { gate.setGranted(state == permission.StateGranted) },
		Exercise: func() { target.reset(); postHook(t, handler, payload) },
		Observe:  func() bool { return target.totalCalls() > 0 },
	})
}
