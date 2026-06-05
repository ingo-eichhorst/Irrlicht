package gastown

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"
)

// recordingLogger captures LogError lines so the timeout-logging path is
// observable in tests. It satisfies outbound.Logger.
type recordingLogger struct {
	mu     sync.Mutex
	errors []string
}

func (l *recordingLogger) LogInfo(eventType, sessionID, message string) {}
func (l *recordingLogger) LogError(eventType, sessionID, errorMsg string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.errors = append(l.errors, eventType+": "+errorMsg)
}
func (l *recordingLogger) LogProcessingTime(string, string, int64, int, string) {}
func (l *recordingLogger) Close() error                                         { return nil }

func (l *recordingLogger) snapshot() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]string, len(l.errors))
	copy(out, l.errors)
	return out
}

// timedOutCtx returns a context whose deadline has already elapsed, so
// ctx.Err() == context.DeadlineExceeded — the exact signal recordFetch keys on
// to distinguish a fetch timeout from a non-timeout failure (bad JSON, gt gone).
func timedOutCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 0)
	t.Cleanup(cancel)
	<-ctx.Done()
	return ctx
}

func TestRecordFetchLogsOncePerTimeoutStreak(t *testing.T) {
	logger := &recordingLogger{}
	p := &poller{
		fetchTimeout: 5 * time.Second,
		logger:       logger,
		timingOut:    make(map[string]bool),
	}
	deadlineCtx := timedOutCtx(t)

	// Two consecutive timed-out fetches: the streak logs exactly once.
	p.recordFetch(deadlineCtx, "rig list", "cached rigs.json")
	p.recordFetch(deadlineCtx, "rig list", "cached rigs.json")

	got := logger.snapshot()
	if len(got) != 1 {
		t.Fatalf("expected 1 log for a consecutive timeout streak, got %d: %v", len(got), got)
	}
	if !strings.Contains(got[0], "gastown-poller") ||
		!strings.Contains(got[0], "rig list") ||
		!strings.Contains(got[0], "timed out") ||
		!strings.Contains(got[0], "cached rigs.json") {
		t.Fatalf("log line missing fetch/fallback detail: %q", got[0])
	}

	// gt recovers (success clears the streak via clearFetchTimeout).
	p.clearFetchTimeout("rig list")
	if n := len(logger.snapshot()); n != 1 {
		t.Fatalf("clearing a streak must not log; got %d lines", n)
	}

	// gt wedges again: a fresh streak logs once more.
	p.recordFetch(deadlineCtx, "rig list", "cached rigs.json")
	if n := len(logger.snapshot()); n != 2 {
		t.Fatalf("a new timeout streak must log again; got %d lines", n)
	}
}

func TestRecordFetchNonTimeoutDoesNotLogAndResetsStreak(t *testing.T) {
	logger := &recordingLogger{}
	p := &poller{
		fetchTimeout: 5 * time.Second,
		logger:       logger,
		timingOut:    make(map[string]bool),
	}

	// A non-deadline failure (e.g. bad JSON, gt missing) must not log a timeout
	// and must leave no streak armed, so a later genuine timeout still logs.
	p.recordFetch(context.Background(), "polecat list", "empty")
	if n := len(logger.snapshot()); n != 0 {
		t.Fatalf("non-timeout failure must not log a timeout; got %d lines", n)
	}

	p.recordFetch(timedOutCtx(t), "polecat list", "empty")
	if n := len(logger.snapshot()); n != 1 {
		t.Fatalf("a genuine timeout after a non-timeout failure must log; got %d lines", n)
	}
}

func TestRecordFetchNilLoggerNoPanic(t *testing.T) {
	p := &poller{
		fetchTimeout: 5 * time.Second,
		timingOut:    make(map[string]bool),
	}
	// logger left nil — must be a no-op, not a panic.
	p.recordFetch(timedOutCtx(t), "boot status", "empty")
}
