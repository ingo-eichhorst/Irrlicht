package aider

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"irrlicht/core/domain/event"
)

// fakeHandler captures HandleEvent calls for test assertions.
type fakeHandler struct {
	mu     sync.Mutex
	events []*event.HookEvent
	err    error
}

func (f *fakeHandler) HandleEvent(evt *event.HookEvent) error {
	f.mu.Lock()
	f.events = append(f.events, evt)
	f.mu.Unlock()
	return f.err
}

func (f *fakeHandler) last() *event.HookEvent {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.events) == 0 {
		return nil
	}
	return f.events[len(f.events)-1]
}

func (f *fakeHandler) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.events)
}

// makeAdapter creates an Adapter with a fresh fakeHandler.
func makeAdapter() (*Adapter, *fakeHandler) {
	h := &fakeHandler{}
	a := New(h, "aider-test-session", "/tmp/proj")
	return a, h
}

// --- ProcessEvent tests -------------------------------------------------------

func TestProcessCliSession_EmitsNotification(t *testing.T) {
	a, h := makeAdapter()

	evt := &AnalyticsEvent{
		Event:      "cli session",
		Properties: map[string]interface{}{"main_model": "claude-3-5-sonnet-20241022"},
		Time:       1754760001,
	}
	if err := a.ProcessEvent(evt); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got := h.last()
	if got == nil {
		t.Fatal("handler not called")
	}
	if got.HookEventName != "Notification" {
		t.Errorf("want HookEventName=Notification, got %q", got.HookEventName)
	}
	if got.SessionID != "aider-test-session" {
		t.Errorf("want SessionID=aider-test-session, got %q", got.SessionID)
	}
	if got.Adapter != "aider" {
		t.Errorf("want Adapter=aider, got %q", got.Adapter)
	}
	if got.Model != "claude-3-5-sonnet-20241022" {
		t.Errorf("want Model=claude-3-5-sonnet-20241022, got %q", got.Model)
	}
}

func TestProcessCliSession_NoModel(t *testing.T) {
	a, h := makeAdapter()

	evt := &AnalyticsEvent{
		Event:      "cli session",
		Properties: map[string]interface{}{},
		Time:       1754760001,
	}
	if err := a.ProcessEvent(evt); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got := h.last()
	if got == nil {
		t.Fatal("handler not called")
	}
	if got.Model != "" {
		t.Errorf("want empty Model when not in properties, got %q", got.Model)
	}
}

func TestProcessMessageSendStarting_EmitsUserPromptSubmit(t *testing.T) {
	a, h := makeAdapter()

	evt := &AnalyticsEvent{Event: "message_send_starting", Time: 1754760010}
	if err := a.ProcessEvent(evt); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got := h.last()
	if got == nil {
		t.Fatal("handler not called")
	}
	if got.HookEventName != "UserPromptSubmit" {
		t.Errorf("want HookEventName=UserPromptSubmit, got %q", got.HookEventName)
	}
	if got.Adapter != "aider" {
		t.Errorf("want Adapter=aider, got %q", got.Adapter)
	}
}

func TestProcessAICommentsExecute_EmitsUserPromptSubmit(t *testing.T) {
	a, h := makeAdapter()

	evt := &AnalyticsEvent{Event: "ai-comments execute", Time: 1754760012}
	if err := a.ProcessEvent(evt); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got := h.last(); got == nil || got.HookEventName != "UserPromptSubmit" {
		t.Errorf("want UserPromptSubmit, got %v", got)
	}
}

func TestProcessExit_EmitsSessionEnd(t *testing.T) {
	a, h := makeAdapter()

	evt := &AnalyticsEvent{
		Event:      "exit",
		Properties: map[string]interface{}{"reason": "Completed main CLI coder.run"},
		Time:       1754760099,
	}
	if err := a.ProcessEvent(evt); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got := h.last()
	if got == nil {
		t.Fatal("handler not called")
	}
	if got.HookEventName != "SessionEnd" {
		t.Errorf("want HookEventName=SessionEnd, got %q", got.HookEventName)
	}
	if got.Adapter != "aider" {
		t.Errorf("want Adapter=aider, got %q", got.Adapter)
	}
}

func TestProcessLaunched_Ignored(t *testing.T) {
	a, h := makeAdapter()

	if err := a.ProcessEvent(&AnalyticsEvent{Event: "launched"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if h.count() != 0 {
		t.Errorf("expected no events for 'launched', got %d", h.count())
	}
}

func TestProcessUnknownEvent_Ignored(t *testing.T) {
	a, h := makeAdapter()

	for _, name := range []string{"repo", "no-repo", "auto_commits", "command_add", "model warning"} {
		if err := a.ProcessEvent(&AnalyticsEvent{Event: name}); err != nil {
			t.Fatalf("unexpected error for %q: %v", name, err)
		}
	}
	if h.count() != 0 {
		t.Errorf("expected no events for ignored events, got %d", h.count())
	}
}

func TestProcessMessageSend_NoImmediateEvent(t *testing.T) {
	// Disable the timer so it doesn't fire and complicate the test.
	orig := WaitTimerDelay
	WaitTimerDelay = 10 * time.Hour
	defer func() { WaitTimerDelay = orig }()

	a, h := makeAdapter()

	if err := a.ProcessEvent(&AnalyticsEvent{Event: "message_send"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// No immediate event expected — Notification comes from the timer or notification command.
	if h.count() != 0 {
		t.Errorf("expected no immediate event for message_send, got %d", h.count())
	}

	a.cancelWaitTimer() // cleanup
}

func TestWaitTimer_FiresAfterMessageSend(t *testing.T) {
	orig := WaitTimerDelay
	WaitTimerDelay = 10 * time.Millisecond
	defer func() { WaitTimerDelay = orig }()

	a, h := makeAdapter()

	if err := a.ProcessEvent(&AnalyticsEvent{Event: "message_send"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Wait for the timer to fire.
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if h.count() >= 1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	if h.count() != 1 {
		t.Fatalf("expected 1 event from timer, got %d", h.count())
	}
	if h.last().HookEventName != "Notification" {
		t.Errorf("want timer to emit Notification, got %q", h.last().HookEventName)
	}
}

func TestWaitTimer_CancelledByMessageSendStarting(t *testing.T) {
	orig := WaitTimerDelay
	WaitTimerDelay = 50 * time.Millisecond
	defer func() { WaitTimerDelay = orig }()

	a, h := makeAdapter()

	// message_send starts the timer.
	if err := a.ProcessEvent(&AnalyticsEvent{Event: "message_send"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// message_send_starting cancels it before it fires.
	if err := a.ProcessEvent(&AnalyticsEvent{Event: "message_send_starting"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Wait past the timer deadline.
	time.Sleep(200 * time.Millisecond)

	// Only the UserPromptSubmit from message_send_starting should have fired.
	if h.count() != 1 {
		t.Fatalf("expected exactly 1 event, got %d", h.count())
	}
	if h.last().HookEventName != "UserPromptSubmit" {
		t.Errorf("want UserPromptSubmit, got %q", h.last().HookEventName)
	}
}

func TestWaitTimer_CancelledByExit(t *testing.T) {
	orig := WaitTimerDelay
	WaitTimerDelay = 50 * time.Millisecond
	defer func() { WaitTimerDelay = orig }()

	a, h := makeAdapter()

	if err := a.ProcessEvent(&AnalyticsEvent{Event: "message_send"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := a.ProcessEvent(&AnalyticsEvent{Event: "exit"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	time.Sleep(200 * time.Millisecond)

	// Only the SessionEnd from exit; no Notification from the timer.
	if h.count() != 1 {
		t.Fatalf("expected exactly 1 event, got %d", h.count())
	}
	if h.last().HookEventName != "SessionEnd" {
		t.Errorf("want SessionEnd, got %q", h.last().HookEventName)
	}
}

// --- TailFile tests -----------------------------------------------------------

// writeLines writes newline-terminated lines to a file and returns the file path.
func writeTempJSONL(t *testing.T, lines []string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "aider-test.jsonl")
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create temp file: %v", err)
	}
	for _, line := range lines {
		f.WriteString(line + "\n")
	}
	f.Close()
	return path
}

func TestTailFile_ProcessesExistingLines(t *testing.T) {
	lines := []string{
		`{"event":"launched","properties":{},"user_id":"u1","time":1000}`,
		`{"event":"cli session","properties":{"main_model":"gpt-4"},"user_id":"u1","time":1001}`,
		`{"event":"message_send_starting","properties":{},"user_id":"u1","time":1002}`,
		`{"event":"exit","properties":{"reason":"done"},"user_id":"u1","time":1003}`,
	}
	path := writeTempJSONL(t, lines)

	h := &fakeHandler{}
	a := New(h, "tail-test-session", "/tmp")

	// Disable timer so it doesn't interfere.
	orig := WaitTimerDelay
	WaitTimerDelay = 10 * time.Hour
	defer func() { WaitTimerDelay = orig }()

	stop := make(chan struct{})
	err := a.TailFile(path, stop)
	if err != nil {
		t.Fatalf("TailFile error: %v", err)
	}

	// Expect: cli session → Notification, message_send_starting → UserPromptSubmit, exit → SessionEnd
	if h.count() != 3 {
		t.Fatalf("expected 3 events, got %d", h.count())
	}

	h.mu.Lock()
	evts := h.events
	h.mu.Unlock()

	if evts[0].HookEventName != "Notification" {
		t.Errorf("[0] want Notification, got %q", evts[0].HookEventName)
	}
	if evts[1].HookEventName != "UserPromptSubmit" {
		t.Errorf("[1] want UserPromptSubmit, got %q", evts[1].HookEventName)
	}
	if evts[2].HookEventName != "SessionEnd" {
		t.Errorf("[2] want SessionEnd, got %q", evts[2].HookEventName)
	}
}

func TestTailFile_StopChannelExits(t *testing.T) {
	// Write a file without an exit event so the tailer would poll indefinitely.
	lines := []string{
		`{"event":"launched","properties":{},"user_id":"u1","time":1000}`,
	}
	path := writeTempJSONL(t, lines)

	h := &fakeHandler{}
	a := New(h, "stop-test-session", "/tmp")

	stop := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- a.TailFile(path, stop)
	}()

	// Close stop channel — tailer should exit promptly.
	close(stop)

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("TailFile should return nil on stop, got: %v", err)
		}
	case <-time.After(1 * time.Second):
		t.Fatal("TailFile did not exit after stop channel closed")
	}
}

func TestTailFile_NonexistentFile(t *testing.T) {
	h := &fakeHandler{}
	a := New(h, "notfound-session", "/tmp")

	stop := make(chan struct{})
	err := a.TailFile("/nonexistent/path/aider.jsonl", stop)
	if err == nil {
		t.Fatal("expected error for nonexistent file, got nil")
	}
}

func TestTailFile_SkipsMalformedLines(t *testing.T) {
	lines := []string{
		`not valid json`,
		`{"event":"cli session","properties":{},"user_id":"u1","time":1000}`,
		`{"event":"exit","properties":{},"user_id":"u1","time":1001}`,
	}
	path := writeTempJSONL(t, lines)

	h := &fakeHandler{}
	a := New(h, "malformed-session", "/tmp")

	stop := make(chan struct{})
	err := a.TailFile(path, stop)
	if err != nil {
		t.Fatalf("TailFile should not error on malformed lines: %v", err)
	}

	// Notification (cli session) + SessionEnd (exit)
	if h.count() != 2 {
		t.Fatalf("expected 2 events, got %d", h.count())
	}
}

func TestTailFile_AppendedLines(t *testing.T) {
	// Start with an empty file, then append lines after a short delay.
	dir := t.TempDir()
	path := filepath.Join(dir, "aider-append.jsonl")
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	h := &fakeHandler{}
	a := New(h, "append-session", "/tmp")

	stop := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- a.TailFile(path, stop)
	}()

	// Wait a bit, then append the exit event.
	time.Sleep(150 * time.Millisecond)
	f.WriteString(`{"event":"exit","properties":{},"user_id":"u1","time":1000}` + "\n")
	f.Close()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("TailFile error: %v", err)
		}
	case <-time.After(2 * time.Second):
		close(stop)
		t.Fatal("TailFile did not exit after exit event appended")
	}

	if h.count() != 1 || h.last().HookEventName != "SessionEnd" {
		t.Errorf("expected SessionEnd, got %d events", h.count())
	}
}
