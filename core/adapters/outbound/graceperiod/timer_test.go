package graceperiod

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// writeTranscript writes JSONL transcript lines to a file for testing.
func writeTranscript(t *testing.T, dir string, lines []map[string]interface{}) string {
	t.Helper()
	path := filepath.Join(dir, "test-session.jsonl")
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create transcript: %v", err)
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	for _, line := range lines {
		if err := enc.Encode(line); err != nil {
			t.Fatalf("encode transcript line: %v", err)
		}
	}
	return path
}

func TestTimer_FiresWhenNoOpenToolCall(t *testing.T) {
	dir := t.TempDir()

	// Transcript with a completed tool call (tool_use + tool_result).
	transcript := writeTranscript(t, dir, []map[string]interface{}{
		{"type": "user", "timestamp": "2026-03-20T10:00:00Z"},
		{"type": "assistant", "timestamp": "2026-03-20T10:00:01Z"},
		{"type": "tool_use", "timestamp": "2026-03-20T10:00:02Z"},
		{"type": "tool_result", "timestamp": "2026-03-20T10:00:03Z"},
	})

	var mu sync.Mutex
	var fired []string
	handler := func(sessionID string) {
		mu.Lock()
		fired = append(fired, sessionID)
		mu.Unlock()
	}

	timer := New(50*time.Millisecond, handler)
	timer.Reset("sess-1", transcript)

	// Wait for the timer to fire.
	time.Sleep(200 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	if len(fired) != 1 || fired[0] != "sess-1" {
		t.Fatalf("expected handler to fire for sess-1, got: %v", fired)
	}

	if timer.ActiveCount() != 0 {
		t.Fatalf("expected 0 active timers after fire, got %d", timer.ActiveCount())
	}
}

func TestTimer_DoesNotFireWhenOpenToolCall(t *testing.T) {
	dir := t.TempDir()

	// Transcript with an open tool call (tool_use without tool_result).
	transcript := writeTranscript(t, dir, []map[string]interface{}{
		{"type": "user", "timestamp": "2026-03-20T10:00:00Z"},
		{"type": "assistant", "timestamp": "2026-03-20T10:00:01Z"},
		{"type": "tool_use", "timestamp": "2026-03-20T10:00:02Z"},
	})

	var mu sync.Mutex
	var fired []string
	handler := func(sessionID string) {
		mu.Lock()
		fired = append(fired, sessionID)
		mu.Unlock()
	}

	timer := New(50*time.Millisecond, handler)
	timer.Reset("sess-2", transcript)

	time.Sleep(200 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	if len(fired) != 0 {
		t.Fatalf("expected handler NOT to fire when tool call open, got: %v", fired)
	}
}

func TestTimer_ResetCancelsPrevious(t *testing.T) {
	dir := t.TempDir()

	transcript := writeTranscript(t, dir, []map[string]interface{}{
		{"type": "user", "timestamp": "2026-03-20T10:00:00Z"},
	})

	var mu sync.Mutex
	var fired []string
	handler := func(sessionID string) {
		mu.Lock()
		fired = append(fired, sessionID)
		mu.Unlock()
	}

	timer := New(100*time.Millisecond, handler)
	timer.Reset("sess-3", transcript)

	// Reset before the first timer fires — cancels the previous.
	time.Sleep(50 * time.Millisecond)
	timer.Reset("sess-3", transcript)

	// Wait past the original timer deadline but before the new one fires.
	time.Sleep(70 * time.Millisecond)
	mu.Lock()
	count := len(fired)
	mu.Unlock()
	if count != 0 {
		t.Fatalf("expected 0 fires after reset (original cancelled), got %d", count)
	}

	// Wait for the second timer to fire.
	time.Sleep(100 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if len(fired) != 1 {
		t.Fatalf("expected exactly 1 fire after second timer, got %d", len(fired))
	}
}

func TestTimer_StopCancelsTimer(t *testing.T) {
	dir := t.TempDir()

	transcript := writeTranscript(t, dir, []map[string]interface{}{
		{"type": "user", "timestamp": "2026-03-20T10:00:00Z"},
	})

	var mu sync.Mutex
	var fired []string
	handler := func(sessionID string) {
		mu.Lock()
		fired = append(fired, sessionID)
		mu.Unlock()
	}

	timer := New(50*time.Millisecond, handler)
	timer.Reset("sess-4", transcript)
	timer.Stop("sess-4")

	time.Sleep(200 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	if len(fired) != 0 {
		t.Fatalf("expected handler NOT to fire after Stop, got: %v", fired)
	}
	if timer.ActiveCount() != 0 {
		t.Fatalf("expected 0 active timers, got %d", timer.ActiveCount())
	}
}

func TestTimer_StopAllCancelsAllTimers(t *testing.T) {
	dir := t.TempDir()

	transcript := writeTranscript(t, dir, []map[string]interface{}{
		{"type": "user", "timestamp": "2026-03-20T10:00:00Z"},
	})

	var mu sync.Mutex
	var fired []string
	handler := func(sessionID string) {
		mu.Lock()
		fired = append(fired, sessionID)
		mu.Unlock()
	}

	timer := New(50*time.Millisecond, handler)
	timer.Reset("sess-a", transcript)
	timer.Reset("sess-b", transcript)
	timer.Reset("sess-c", transcript)
	timer.StopAll()

	time.Sleep(200 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	if len(fired) != 0 {
		t.Fatalf("expected no fires after StopAll, got: %v", fired)
	}
	if timer.ActiveCount() != 0 {
		t.Fatalf("expected 0 active timers, got %d", timer.ActiveCount())
	}
}

func TestTimer_MultipleSessions(t *testing.T) {
	dir := t.TempDir()

	// Session with no open tool calls — should fire.
	closedTranscript := writeTranscript(t, dir, []map[string]interface{}{
		{"type": "user", "timestamp": "2026-03-20T10:00:00Z"},
		{"type": "tool_use", "timestamp": "2026-03-20T10:00:01Z"},
		{"type": "tool_result", "timestamp": "2026-03-20T10:00:02Z"},
	})

	// Session with open tool call — should NOT fire.
	dir2 := t.TempDir()
	openTranscript := writeTranscript(t, dir2, []map[string]interface{}{
		{"type": "user", "timestamp": "2026-03-20T10:00:00Z"},
		{"type": "tool_use", "timestamp": "2026-03-20T10:00:01Z"},
	})

	var mu sync.Mutex
	var fired []string
	handler := func(sessionID string) {
		mu.Lock()
		fired = append(fired, sessionID)
		mu.Unlock()
	}

	timer := New(50*time.Millisecond, handler)
	timer.Reset("sess-closed", closedTranscript)
	timer.Reset("sess-open", openTranscript)

	time.Sleep(200 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	if len(fired) != 1 || fired[0] != "sess-closed" {
		t.Fatalf("expected only sess-closed to fire, got: %v", fired)
	}
}

func TestTimer_MissingTranscriptDoesNotFire(t *testing.T) {
	var mu sync.Mutex
	var fired []string
	handler := func(sessionID string) {
		mu.Lock()
		fired = append(fired, sessionID)
		mu.Unlock()
	}

	timer := New(50*time.Millisecond, handler)
	timer.Reset("sess-missing", "/nonexistent/path/transcript.jsonl")

	time.Sleep(200 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	if len(fired) != 0 {
		t.Fatalf("expected handler NOT to fire for missing transcript, got: %v", fired)
	}
}

func TestTimer_StopNonexistentSessionIsNoop(t *testing.T) {
	timer := New(50*time.Millisecond, func(string) {})
	// Should not panic.
	timer.Stop("nonexistent")
}
