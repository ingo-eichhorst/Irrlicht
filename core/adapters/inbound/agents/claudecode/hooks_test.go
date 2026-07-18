package claudecode

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"irrlicht/core/domain/permission"
	"irrlicht/core/domain/session"
	"irrlicht/core/internal/contracttesting"
)

// mockTarget records calls to HandlePermissionHook, HandleCompactHook,
// HandleStopHook and HandleIdlePromptHook for assertions.
type mockTarget struct {
	mu              sync.Mutex
	calls           []hookCall
	compactCalls    []compactCall
	stopCalls       []stopCall
	idlePromptCalls []idlePromptCall
}

type hookCall struct {
	sessionID, transcriptPath, hookEventName string
}

type compactCall struct {
	sessionID, transcriptPath, trigger string
}

type stopCall struct {
	sessionID, transcriptPath, lastAssistantText string
	waitingCue                                   bool
}

type idlePromptCall struct {
	sessionID, transcriptPath string
}

func (m *mockTarget) HandlePermissionHook(sessionID, transcriptPath, hookEventName string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, hookCall{sessionID, transcriptPath, hookEventName})
}

func (m *mockTarget) HandleCompactHook(sessionID, transcriptPath, trigger string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.compactCalls = append(m.compactCalls, compactCall{sessionID, transcriptPath, trigger})
}

func (m *mockTarget) HandleStopHook(sessionID, transcriptPath, lastAssistantText string, waitingCue bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.stopCalls = append(m.stopCalls, stopCall{sessionID, transcriptPath, lastAssistantText, waitingCue})
}

func (m *mockTarget) HandleIdlePromptHook(sessionID, transcriptPath string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.idlePromptCalls = append(m.idlePromptCalls, idlePromptCall{sessionID, transcriptPath})
}

func (m *mockTarget) getCalls() []hookCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]hookCall{}, m.calls...)
}

func (m *mockTarget) reset() {
	m.mu.Lock()
	m.calls = nil
	m.compactCalls = nil
	m.stopCalls = nil
	m.idlePromptCalls = nil
	m.mu.Unlock()
}

func (m *mockTarget) getIdlePromptCalls() []idlePromptCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]idlePromptCall{}, m.idlePromptCalls...)
}

func (m *mockTarget) getCompactCalls() []compactCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]compactCall{}, m.compactCalls...)
}

func (m *mockTarget) getStopCalls() []stopCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]stopCall{}, m.stopCalls...)
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
	handler := NewHookHandler(target, nil, nil, mockLogger{})

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
	handler := NewHookHandler(target, nil, nil, mockLogger{})

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
	handler := NewHookHandler(target, nil, nil, mockLogger{})

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
	handler := NewHookHandler(target, nil, nil, mockLogger{})

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

// TestHookHandler_PreCompactManual verifies a manual /compact PreCompact hook
// routes to HandleCompactHook so the detector can force working for the
// compaction window (#657).
func TestHookHandler_PreCompactManual(t *testing.T) {
	target := &mockTarget{}
	handler := NewHookHandler(target, nil, nil, mockLogger{})

	payload := hookPayload{
		TranscriptPath: "/Users/u/.claude/projects/p/sess-comp.jsonl",
		HookEventName:  "PreCompact",
		Trigger:        "manual",
	}
	body, _ := json.Marshal(payload)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/hooks/claudecode", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	handler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if len(target.getCalls()) != 0 {
		t.Errorf("PreCompact must not reach HandlePermissionHook; got %+v", target.getCalls())
	}
	compact := target.getCompactCalls()
	if len(compact) != 1 {
		t.Fatalf("got %d HandleCompactHook calls, want 1", len(compact))
	}
	if compact[0].sessionID != "sess-comp" {
		t.Errorf("sessionID = %q, want %q", compact[0].sessionID, "sess-comp")
	}
	if compact[0].trigger != "manual" {
		t.Errorf("trigger = %q, want %q", compact[0].trigger, "manual")
	}
}

// TestHookHandler_PreCompactAuto verifies an auto-compaction PreCompact hook is
// accepted but ignored — the session is already working mid-turn, so forcing it
// would be a spurious blip (#657).
func TestHookHandler_PreCompactAuto(t *testing.T) {
	target := &mockTarget{}
	handler := NewHookHandler(target, nil, nil, mockLogger{})

	payload := hookPayload{
		TranscriptPath: "/Users/u/.claude/projects/p/sess-auto.jsonl",
		HookEventName:  "PreCompact",
		Trigger:        "auto",
	}
	body, _ := json.Marshal(payload)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/hooks/claudecode", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	handler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (accept but ignore)", rec.Code, http.StatusOK)
	}
	if got := target.getCompactCalls(); len(got) != 0 {
		t.Errorf("auto PreCompact should not dispatch; got %+v", got)
	}
}

// TestHookHandler_Stop verifies a Stop hook routes to HandleStopHook with the
// turn's final assistant text (display-truncated) and waitingCue=false when the
// message is a plain completion, so the classifier can mark the turn done →
// ready (#1161). It must not reach the permission or compact paths.
func TestHookHandler_Stop(t *testing.T) {
	target := &mockTarget{}
	handler := NewHookHandler(target, nil, nil, mockLogger{})

	payload := hookPayload{
		TranscriptPath:       "/Users/u/.claude/projects/p/sess-stop.jsonl",
		HookEventName:        "Stop",
		LastAssistantMessage: "All tests pass. The refactor is complete.",
	}
	body, _ := json.Marshal(payload)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/hooks/claudecode", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	handler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if len(target.getCalls()) != 0 || len(target.getCompactCalls()) != 0 {
		t.Errorf("Stop must not reach permission/compact paths; perm=%+v compact=%+v",
			target.getCalls(), target.getCompactCalls())
	}
	stops := target.getStopCalls()
	if len(stops) != 1 {
		t.Fatalf("got %d HandleStopHook calls, want 1", len(stops))
	}
	if stops[0].sessionID != "sess-stop" {
		t.Errorf("sessionID = %q, want %q", stops[0].sessionID, "sess-stop")
	}
	if stops[0].lastAssistantText != "All tests pass. The refactor is complete." {
		t.Errorf("lastAssistantText = %q, want the full (short) message", stops[0].lastAssistantText)
	}
	if stops[0].waitingCue {
		t.Errorf("waitingCue = true for a plain completion message, want false")
	}
}

// TestHookHandler_StopEndingInQuestion verifies a Stop hook whose final message
// ends on a question is reported with waitingCue=true, so a turn that ended by
// asking the user routes to waiting rather than ready (#1161).
func TestHookHandler_StopEndingInQuestion(t *testing.T) {
	target := &mockTarget{}
	handler := NewHookHandler(target, nil, nil, mockLogger{})

	payload := hookPayload{
		TranscriptPath:       "/Users/u/.claude/projects/p/sess-q.jsonl",
		HookEventName:        "Stop",
		LastAssistantMessage: "I can take either approach. Which option do you prefer?",
	}
	body, _ := json.Marshal(payload)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/hooks/claudecode", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	handler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	stops := target.getStopCalls()
	if len(stops) != 1 {
		t.Fatalf("got %d HandleStopHook calls, want 1", len(stops))
	}
	if !stops[0].waitingCue {
		t.Errorf("waitingCue = false for a message ending in a question, want true")
	}
}

func TestHookHandler_NotificationIdlePrompt(t *testing.T) {
	target := &mockTarget{}
	handler := NewHookHandler(target, nil, nil, mockLogger{})

	rec := postHook(t, handler, hookPayload{
		TranscriptPath:   "/Users/u/.claude/projects/p/sess-idle.jsonl",
		HookEventName:    "Notification",
		NotificationType: "idle_prompt",
	})

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if len(target.getCalls()) != 0 || len(target.getStopCalls()) != 0 || len(target.getCompactCalls()) != 0 {
		t.Errorf("idle_prompt Notification must not reach permission/stop/compact paths")
	}
	idles := target.getIdlePromptCalls()
	if len(idles) != 1 {
		t.Fatalf("got %d HandleIdlePromptHook calls, want 1", len(idles))
	}
	if idles[0].sessionID != "sess-idle" {
		t.Errorf("sessionID = %q, want %q", idles[0].sessionID, "sess-idle")
	}
	if idles[0].transcriptPath != "/Users/u/.claude/projects/p/sess-idle.jsonl" {
		t.Errorf("transcriptPath = %q, want the payload path", idles[0].transcriptPath)
	}
}

// TestHookHandler_NotificationNonIdleType asserts the defense-in-depth reject:
// only idle_prompt drives state, even though the installer already narrows the
// matcher — a broadened settings.json matcher must not dispatch other types.
func TestHookHandler_NotificationNonIdleType(t *testing.T) {
	for _, ntype := range []string{"permission_prompt", "auth_success", "agent_completed", ""} {
		target := &mockTarget{}
		handler := NewHookHandler(target, nil, nil, mockLogger{})

		rec := postHook(t, handler, hookPayload{
			TranscriptPath:   "/Users/u/.claude/projects/p/sess-n.jsonl",
			HookEventName:    "Notification",
			NotificationType: ntype,
		})

		if rec.Code != http.StatusOK {
			t.Fatalf("type %q: status = %d, want %d", ntype, rec.Code, http.StatusOK)
		}
		if n := len(target.getIdlePromptCalls()); n != 0 {
			t.Errorf("type %q: got %d HandleIdlePromptHook calls, want 0", ntype, n)
		}
	}
}

// TestHookHandler_NotificationConsentGated confirms the Notification path is
// behind the same "hooks" consent gate as every other event: nothing is
// dispatched while the permission is not granted.
func TestHookHandler_NotificationConsentGated(t *testing.T) {
	target := &mockTarget{}
	handler := NewHookHandler(target, nil, fakeGate(false), mockLogger{})

	rec := postHook(t, handler, hookPayload{
		TranscriptPath:   "/Users/u/.claude/projects/p/sess-gated.jsonl",
		HookEventName:    "Notification",
		NotificationType: "idle_prompt",
	})

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if n := len(target.getIdlePromptCalls()); n != 0 {
		t.Fatalf("dispatched %d idle-prompt calls while permission not granted", n)
	}
}

func TestHookHandler_PostToolUseFailure(t *testing.T) {
	target := &mockTarget{}
	handler := NewHookHandler(target, nil, nil, mockLogger{})

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
	handler := NewHookHandler(target, nil, nil, mockLogger{})

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
	handler := NewHookHandler(target, nil, nil, mockLogger{})

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
	handler := NewHookHandler(target, nil, nil, mockLogger{})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/hooks/claudecode", nil)
	rec := httptest.NewRecorder()
	handler(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
	}
}

func TestHookHandler_MalformedJSON(t *testing.T) {
	target := &mockTarget{}
	handler := NewHookHandler(target, nil, nil, mockLogger{})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/hooks/claudecode", bytes.NewReader([]byte("not json")))
	rec := httptest.NewRecorder()
	handler(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

// fakeGate is a ConsentGranter with a fixed answer.
type fakeGate bool

func (g fakeGate) Granted(_, _ string) bool { return bool(g) }

// mutableGate is a ConsentGranter whose answer can change between calls —
// needed to drive a single handler instance through all three consent
// states for contracttesting.AssertPermissionGated.
type mutableGate struct {
	mu      sync.Mutex
	granted bool
}

func (g *mutableGate) Granted(_, _ string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.granted
}

func (g *mutableGate) setGranted(v bool) {
	g.mu.Lock()
	g.granted = v
	g.mu.Unlock()
}

func TestHookHandler_ConsentGateDropsWhenNotGranted(t *testing.T) {
	target := &mockTarget{}
	handler := NewHookHandler(target, nil, fakeGate(false), mockLogger{})

	payload := hookPayload{
		TranscriptPath: "/Users/u/.claude/projects/p/sess-123.jsonl",
		HookEventName:  "PermissionRequest",
		ToolName:       "Bash",
	}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/hooks/claudecode", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	handler(rec, req)

	// 200 keeps the curl hook quiet, but nothing is dispatched.
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if calls := target.getCalls(); len(calls) != 0 {
		t.Fatalf("dispatched %d calls while permission not granted", len(calls))
	}
}

func TestHookHandler_ConsentGatePassesWhenGranted(t *testing.T) {
	target := &mockTarget{}
	handler := NewHookHandler(target, nil, fakeGate(true), mockLogger{})

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
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if calls := target.getCalls(); len(calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(calls))
	}
}

// mockMarkerTarget records IngestTaskEstimate / IngestTaskSummary calls for
// assertions (#604, #738).
type mockMarkerTarget struct {
	mu        sync.Mutex
	calls     []markerCall
	summaries []summaryCall
}

type markerCall struct {
	transcriptPath string
	est            *session.TaskEstimate
}

type summaryCall struct {
	transcriptPath string
	text           string
	observedAt     int64
}

func (m *mockMarkerTarget) IngestTaskEstimate(transcriptPath string, est *session.TaskEstimate) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, markerCall{transcriptPath, est})
}

func (m *mockMarkerTarget) IngestTaskSummary(transcriptPath, text string, observedAt int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.summaries = append(m.summaries, summaryCall{transcriptPath, text, observedAt})
}

func (m *mockMarkerTarget) getCalls() []markerCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]markerCall{}, m.calls...)
}

func (m *mockMarkerTarget) getSummaries() []summaryCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]summaryCall{}, m.summaries...)
}

func postHook(t *testing.T, handler http.HandlerFunc, payload hookPayload) *httptest.ResponseRecorder {
	t.Helper()
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/hooks/claudecode", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	handler(rec, req)
	return rec
}

func TestHookHandler_PreToolUse_MarkerInBashDescription(t *testing.T) {
	// The #604 carrier: a marker in a Bash description rides the PreToolUse
	// payload to the daemon, bypassing the transcript writer. The escaped
	// JSON-in-JSON shape is exactly what Claude Code sends.
	target := &mockTarget{}
	markers := &mockMarkerTarget{}
	handler := NewHookHandler(target, markers, nil, mockLogger{})

	rec := postHook(t, handler, hookPayload{
		TranscriptPath: "/Users/u/.claude/projects/p/sess-eta.jsonl",
		HookEventName:  "PreToolUse",
		ToolName:       "Bash",
		ToolInput:      json.RawMessage(`{"command":"go test ./...","description":"Run tests <!-- {\"marker\":\"irrlicht-eta\",\"total_rounds\":7,\"completed_rounds\":3} -->"}`),
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	calls := markers.getCalls()
	if len(calls) != 1 {
		t.Fatalf("got %d IngestTaskEstimate calls, want 1", len(calls))
	}
	if calls[0].transcriptPath != "/Users/u/.claude/projects/p/sess-eta.jsonl" {
		t.Errorf("transcriptPath = %q", calls[0].transcriptPath)
	}
	if calls[0].est.TotalRounds != 7 || calls[0].est.CompletedRounds != 3 {
		t.Errorf("est = %+v, want 3/7", calls[0].est)
	}
	if calls[0].est.UpdatedAt == 0 {
		t.Error("est.UpdatedAt must be stamped at receipt")
	}
	// The permission dispatch stays gated to user-input tools: a Bash
	// PreToolUse must NOT reach HandlePermissionHook.
	if got := target.getCalls(); len(got) != 0 {
		t.Errorf("HandlePermissionHook calls = %d, want 0 (two-path split)", len(got))
	}
}

func TestHookHandler_PreToolUse_SummaryInBashDescription(t *testing.T) {
	// The #738 carrier: a task-summary marker in a Bash description rides the
	// PreToolUse payload to the daemon, bypassing the transcript writer.
	target := &mockTarget{}
	markers := &mockMarkerTarget{}
	handler := NewHookHandler(target, markers, nil, mockLogger{})

	rec := postHook(t, handler, hookPayload{
		TranscriptPath: "/Users/u/.claude/projects/p/sess-sum.jsonl",
		HookEventName:  "PreToolUse",
		ToolName:       "Bash",
		ToolInput:      json.RawMessage(`{"command":"go build ./...","description":"Build core <!-- {\"marker\":\"irrlicht-summary\",\"summary\":\"add the logout button\"} -->"}`),
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	got := markers.getSummaries()
	if len(got) != 1 {
		t.Fatalf("got %d IngestTaskSummary calls, want 1", len(got))
	}
	if got[0].transcriptPath != "/Users/u/.claude/projects/p/sess-sum.jsonl" {
		t.Errorf("transcriptPath = %q", got[0].transcriptPath)
	}
	if got[0].text != "add the logout button" {
		t.Errorf("text = %q, want 'add the logout button'", got[0].text)
	}
	if got[0].observedAt == 0 {
		t.Error("observedAt must be stamped at receipt")
	}
	if calls := target.getCalls(); len(calls) != 0 {
		t.Errorf("HandlePermissionHook calls = %d, want 0", len(calls))
	}
}

func TestHookHandler_PreToolUse_NoMarkerNoIngest(t *testing.T) {
	markers := &mockMarkerTarget{}
	handler := NewHookHandler(&mockTarget{}, markers, nil, mockLogger{})

	postHook(t, handler, hookPayload{
		TranscriptPath: "/Users/u/.claude/projects/p/sess-1.jsonl",
		HookEventName:  "PreToolUse",
		ToolName:       "Bash",
		ToolInput:      json.RawMessage(`{"command":"ls","description":"List files"}`),
	})
	// A plain HTML comment without the marker key must not ingest either.
	postHook(t, handler, hookPayload{
		TranscriptPath: "/Users/u/.claude/projects/p/sess-1.jsonl",
		HookEventName:  "PreToolUse",
		ToolName:       "Write",
		ToolInput:      json.RawMessage(`{"file_path":"/tmp/x.html","content":"<!-- a comment -->"}`),
	})
	if got := markers.getCalls(); len(got) != 0 {
		t.Errorf("IngestTaskEstimate calls = %d, want 0", len(got))
	}
	if got := markers.getSummaries(); len(got) != 0 {
		t.Errorf("IngestTaskSummary calls = %d, want 0", len(got))
	}
}

func TestHookHandler_PreToolUse_UserInputToolStillDispatchesWithMarkerScan(t *testing.T) {
	// AskUserQuestion keeps its permission dispatch; an embedded marker in
	// its input is also picked up — the two paths are independent.
	target := &mockTarget{}
	markers := &mockMarkerTarget{}
	handler := NewHookHandler(target, markers, nil, mockLogger{})

	postHook(t, handler, hookPayload{
		TranscriptPath: "/Users/u/.claude/projects/p/sess-q.jsonl",
		HookEventName:  "PreToolUse",
		ToolName:       "AskUserQuestion",
		ToolInput:      json.RawMessage(`{"questions":[{"question":"Proceed? <!-- {\"marker\":\"irrlicht-eta\",\"total_rounds\":4,\"completed_rounds\":2} -->"}]}`),
	})
	if got := target.getCalls(); len(got) != 1 || got[0].hookEventName != "PreToolUse" {
		t.Errorf("HandlePermissionHook calls = %+v, want one PreToolUse dispatch", got)
	}
	if got := markers.getCalls(); len(got) != 1 || got[0].est.CompletedRounds != 2 {
		t.Errorf("IngestTaskEstimate calls = %+v, want one 2/4 ingest (nested input walk)", got)
	}
}

// TestHookHandler_PermissionGateContract wires the hooks permission's live
// per-request ConsentGranter check into the shared issue #797 contract: while
// PermissionKeyHooks is pending or denied, an inbound hook payload must
// dispatch to nothing; granted, it must dispatch; revoked, dispatch must
// stop again. TestHookHandler_ConsentGateDropsWhenNotGranted /
// ConsentGatePassesWhenGranted above cover the same call site by hand —
// this is the generalized, three-state version.
func TestHookHandler_PermissionGateContract(t *testing.T) {
	target := &mockTarget{}
	gate := &mutableGate{}
	handler := NewHookHandler(target, nil, gate, mockLogger{})
	payload := hookPayload{
		TranscriptPath: "/Users/u/.claude/projects/p/sess-gate.jsonl",
		HookEventName:  "PermissionRequest",
		ToolName:       "Bash",
	}

	contracttesting.AssertPermissionGated(t, contracttesting.PermissionGate{
		SetState: func(state permission.State) { gate.setGranted(state == permission.StateGranted) },
		Exercise: func() {
			target.reset()
			postHook(t, handler, payload)
		},
		Observe: func() bool { return len(target.getCalls()) > 0 },
	})
}
