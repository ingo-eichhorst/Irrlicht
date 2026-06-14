package services_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"irrlicht/core/domain/agent"
	"irrlicht/core/domain/session"
)

// TestSessionDetector_Activity_FrozenTranscript_DoesNotBumpUpdatedAt is the
// regression test for issue #667. A gemini-cli session whose process died
// mid-turn (pid=0, transcript frozen on a function_call_output) stays working,
// and the 5s refreshStaleSessions ticker re-reads its frozen transcript. That
// re-read used to refresh UpdatedAt to wall-clock "now" every pass, so the
// ready-TTL age-out (which measures now-UpdatedAt) never fired and the session
// was pinned working forever (~23h observed).
//
// The fix gates the activity bump on real transcript growth. A no-op refresh
// over a frozen transcript must touch neither UpdatedAt nor EventCount; a pass
// that observes new bytes must bump both. The state machine still runs every
// pass, so a genuine transition is unaffected.
func TestSessionDetector_Activity_FrozenTranscript_DoesNotBumpUpdatedAt(t *testing.T) {
	dir := t.TempDir()
	tp := filepath.Join(dir, "gem.jsonl")
	if err := os.WriteFile(tp, []byte("{\"type\":\"user\"}\n"), 0o644); err != nil {
		t.Fatalf("write transcript: %v", err)
	}

	tw := newMockAgentWatcher().withIdentity(agent.Identity{Name: "gemini-cli"})
	pw := newMockProcessWatcher()
	repo := newMockRepo()
	det := newDetector(tw, pw, repo)

	// gemini-cli dead-process shape: working, pid=0, last real event was a tool
	// result. LastTranscriptSize=0 so the first activity pass sees growth.
	oldTs := time.Now().Add(-time.Hour).Unix()
	repo.states["gem"] = &session.SessionState{
		SessionID:          "gem",
		Adapter:            "gemini-cli",
		State:              session.StateWorking,
		PID:                0,
		TranscriptPath:     tp,
		FirstSeen:          oldTs,
		UpdatedAt:          oldTs,
		LastTranscriptSize: 0,
		Metrics: &session.SessionMetrics{
			HasOpenToolCall: false,
			LastEventType:   "function_call_output",
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- det.Run(ctx) }()
	defer func() { cancel(); <-done }()

	// Let seedFromDisk finish (it re-reads metrics but never bumps EventCount /
	// LastTranscriptSize) before injecting activity.
	time.Sleep(50 * time.Millisecond)

	// Terminal:true bypasses the activity debounce so each event is processed
	// immediately and synchronously on the Run goroutine — no 2s coalescing
	// timer, so the frozen-vs-grown passes are deterministic. Terminal is read
	// only by the debounce fast-path; it does not affect classification.
	activity := func() agent.Event {
		return agent.Event{Type: agent.EventActivity, SessionID: "gem", TranscriptPath: tp, Terminal: true}
	}

	// Pass A — the transcript grew (0 → S1): UpdatedAt and EventCount must bump.
	tw.ch <- activity()
	waitForCondition(func() bool { return repo.eventCountOf("gem") == 1 }, 2*time.Second)
	if got := repo.eventCountOf("gem"); got != 1 {
		t.Fatalf("grown transcript: EventCount = %d, want 1", got)
	}
	tsA := repo.updatedAtOf("gem")
	if tsA <= oldTs {
		t.Fatalf("grown transcript did not advance UpdatedAt: got %d, want > %d", tsA, oldTs)
	}

	// Pass B — the transcript is frozen (size unchanged): no bump. The pass
	// produces no observable field change, so wait on the Save count.
	savesBefore := repo.savesCount()
	tw.ch <- activity()
	waitForCondition(func() bool { return repo.savesCount() > savesBefore }, 2*time.Second)
	if got := repo.eventCountOf("gem"); got != 1 {
		t.Fatalf("frozen transcript bumped EventCount: got %d, want 1 (unchanged)", got)
	}
	if got := repo.updatedAtOf("gem"); got != tsA {
		t.Fatalf("frozen-transcript refresh bumped UpdatedAt: got %d, want %d (unchanged)", got, tsA)
	}

	// Pass C — append new bytes, then refresh: bumping must resume.
	f, err := os.OpenFile(tp, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("open transcript for append: %v", err)
	}
	if _, err := f.WriteString("{\"type\":\"assistant\"}\n"); err != nil {
		t.Fatalf("append transcript: %v", err)
	}
	_ = f.Close()

	tw.ch <- activity()
	waitForCondition(func() bool { return repo.eventCountOf("gem") == 2 }, 2*time.Second)
	if got := repo.eventCountOf("gem"); got != 2 {
		t.Fatalf("grown transcript after freeze: EventCount = %d, want 2", got)
	}
}
